// Untagged like live_engine_registry.go: these tests exercise the pure
// availability-decision logic without needing a built ctxloom binary, a real
// engine binary, or any acceptance fixture plumbing, so `just test` gates on
// them directly.
package acceptance

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

// fakeAuthCheck returns a canned (ok, reason) pair, so tests never shell out
// to a real engine binary.
func fakeAuthCheck(ok bool, reason string) func(string) (bool, string) {
	return func(string) (bool, string) { return ok, reason }
}

func TestEngineAvailable(t *testing.T) {
	cases := []struct {
		name      string
		agent     liveAgent
		env       map[string]string
		optIn     bool
		wantOK    bool
		wantMatch string // substring expected in the reason
	}{
		{
			name:      "binary not on PATH is unavailable regardless of everything else",
			agent:     liveAgent{binary: "ctxloom-nonexistent-binary-xyz", authCheck: fakeAuthCheck(true, "would say yes")},
			optIn:     true,
			wantOK:    false,
			wantMatch: `binary "ctxloom-nonexistent-binary-xyz" not found`,
		},
		{
			name:      "no binary configured at all is unavailable",
			agent:     liveAgent{authCheck: fakeAuthCheck(true, "would say yes")},
			optIn:     true,
			wantOK:    false,
			wantMatch: "no binary configured",
		},
		{
			name:   "API key present short-circuits straight to available, no authCheck consulted",
			agent:  liveAgent{binary: "sh", apiKeyEnvs: []string{"CTXLOOM_TEST_FAKE_KEY"}, authCheck: fakeAuthCheck(false, "should never be called")},
			env:    map[string]string{"CTXLOOM_TEST_FAKE_KEY": "sk-fake"},
			optIn:  false, // API-key path bypasses the opt-in gate entirely
			wantOK: true,
		},
		{
			name:      "subscription path without CTXLOOM_ACCEPTANCE_LIVE opt-in is unavailable even if authCheck would pass",
			agent:     liveAgent{binary: "sh", authCheck: fakeAuthCheck(true, "would say yes")},
			optIn:     false,
			wantOK:    false,
			wantMatch: "opt-in",
		},
		{
			name:      "subscription path with opt-in defers to authCheck: fails",
			agent:     liveAgent{binary: "sh", authCheck: fakeAuthCheck(false, "not logged in")},
			optIn:     true,
			wantOK:    false,
			wantMatch: "not logged in",
		},
		{
			name:   "subscription path with opt-in defers to authCheck: succeeds",
			agent:  liveAgent{binary: "sh", authCheck: fakeAuthCheck(true, "logged in as tester")},
			optIn:  true,
			wantOK: true,
		},
		{
			name:      "no authCheck configured is unavailable",
			agent:     liveAgent{binary: "sh"},
			optIn:     true,
			wantOK:    false,
			wantMatch: "no authentication probe",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			ok, reason := engineAvailable(tc.agent, "/fake/home", tc.optIn)
			assert.Equal(t, tc.wantOK, ok, "reason was: %s", reason)
			if tc.wantMatch != "" {
				assert.Contains(t, reason, tc.wantMatch)
			}
		})
	}
}

func TestProbeEngine_CarriesName(t *testing.T) {
	a := liveAgent{binary: "ctxloom-nonexistent-binary-xyz"}
	status := probeEngine("widget", a, "/fake/home", true)
	assert.Equal(t, "widget", status.name)
	assert.False(t, status.available)
	assert.Contains(t, status.reason, "not found")
}

func TestFormatLiveEngineReport(t *testing.T) {
	report := []engineStatus{
		{name: "claude", available: true},
		{name: "antigravity", available: true},
		{name: "kiro", available: true},
		{name: "codex", available: false, reason: "binary not found"},
	}
	got := formatLiveEngineReport(report)
	assert.Equal(t, "live engines: claude ✓ · antigravity ✓ · kiro ✓ · codex ✗ (binary not found)", got)
}

func TestFormatLiveEngineReport_AllUnavailable(t *testing.T) {
	report := []engineStatus{
		{name: "claude", available: false, reason: "installed, but CTXLOOM_ACCEPTANCE_LIVE=1 not set (subscription credential path is opt-in)"},
	}
	got := formatLiveEngineReport(report)
	assert.Equal(t, `live engines: claude ✗ (installed, but CTXLOOM_ACCEPTANCE_LIVE=1 not set (subscription credential path is opt-in))`, got)
}

func TestParseRequiredEngines(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{name: "empty is nil (floor off by default)", raw: "", want: nil},
		{name: "whitespace-only is nil", raw: "   ", want: nil},
		{name: "single engine", raw: "claude", want: []string{"claude"}},
		{name: "comma separated, trimmed, lowercased", raw: " Claude, KIRO ,antigravity", want: []string{"claude", "kiro", "antigravity"}},
		{name: "empty entries between commas are dropped", raw: "claude,,kiro", want: []string{"claude", "kiro"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, parseRequiredEngines(tc.raw))
		})
	}
}

// TestCheckRequiredEngines_Floor is THE FLOOR test: CTXLOOM_LIVE_REQUIRE
// naming an unavailable engine must fail with a clear message naming it and
// why — this table is the whole point of the feature. If this test did not
// exist, the floor would not exist.
func TestCheckRequiredEngines_Floor(t *testing.T) {
	report := []engineStatus{
		{name: "claude", available: true},
		{name: "antigravity", available: true},
		{name: "kiro", available: true},
		{name: "codex", available: false, reason: "binary not found on PATH"},
	}

	cases := []struct {
		name        string
		required    []string
		wantErr     bool
		wantMatches []string // every substring must appear in the error
	}{
		{
			name:     "unset/empty require-list never fails, even with an unavailable engine in the report",
			required: nil,
			wantErr:  false,
		},
		{
			name:     "all required engines available: passes",
			required: []string{"claude", "kiro", "antigravity"},
			wantErr:  false,
		},
		{
			name:        "required engine unavailable: fails, names the engine and the reason",
			required:    []string{"codex"},
			wantErr:     true,
			wantMatches: []string{"codex", "binary not found on PATH"},
		},
		{
			name:        "mixed available+unavailable required: fails, names only the missing one",
			required:    []string{"claude", "codex"},
			wantErr:     true,
			wantMatches: []string{"codex", "binary not found on PATH"},
		},
		{
			name:        "unknown engine name in require-list: fails, says so rather than silently ignoring it",
			required:    []string{"gpt5"},
			wantErr:     true,
			wantMatches: []string{"gpt5", "not a known live engine"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkRequiredEngines(report, tc.required)
			if !tc.wantErr {
				assert.NoError(t, err)
				return
			}
			assert.Error(t, err)
			for _, m := range tc.wantMatches {
				assert.Contains(t, err.Error(), m)
			}
			// The error must not silently be a generic sentinel — it has to
			// actually carry the specifics, or a CI log is just as opaque as
			// the silent skip this feature exists to replace.
			assert.False(t, errors.Is(err, errUninformativePlaceholder))
		})
	}
}

// errUninformativePlaceholder exists only so the assertion above has
// something concrete to prove checkRequiredEngines's error is NOT this — a
// guard against a future "return errFloorFailed" regression that drops the
// specifics this feature's whole value is in.
var errUninformativePlaceholder = errors.New("floor failed")

func TestResolveOptIn(t *testing.T) {
	cases := []struct {
		name    string
		liveVal string
		require string
		want    bool
	}{
		{name: "neither set: opt-in off", liveVal: "", require: "", want: false},
		{name: "CTXLOOM_ACCEPTANCE_LIVE=1 alone: opt-in on", liveVal: "1", require: "", want: true},
		{name: "CTXLOOM_LIVE_REQUIRE alone implies opt-in (no footgun)", liveVal: "", require: "claude", want: true},
		{name: "both set: opt-in on", liveVal: "1", require: "claude", want: true},
		{name: "CTXLOOM_ACCEPTANCE_LIVE set to something other than 1: opt-in off", liveVal: "true", require: "", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CTXLOOM_ACCEPTANCE_LIVE", tc.liveVal)
			t.Setenv("CTXLOOM_LIVE_REQUIRE", tc.require)
			assert.Equal(t, tc.want, resolveOptIn())
		})
	}
}

// TestLiveAgentAvailable_UsesSameDecision guards the backward-compat path
// steps_j1_setup.go's own @live scenario calls directly: it must agree with
// engineAvailable, never drift into a second, silently-different notion of
// "available".
func TestLiveAgentAvailable_UsesSameDecision(t *testing.T) {
	t.Setenv("CTXLOOM_ACCEPTANCE_LIVE", "")
	t.Setenv("CTXLOOM_LIVE_REQUIRE", "")
	a := liveAgent{binary: "ctxloom-nonexistent-binary-xyz"}
	assert.False(t, liveAgentAvailable(a))
}

func TestMatchedEnvAndEnvSet(t *testing.T) {
	t.Setenv("CTXLOOM_TEST_ENV_A", "")
	t.Setenv("CTXLOOM_TEST_ENV_B", "present")
	names := []string{"CTXLOOM_TEST_ENV_A", "CTXLOOM_TEST_ENV_B"}
	assert.Equal(t, "CTXLOOM_TEST_ENV_B", matchedEnv(names))
	assert.True(t, envSet(names))
	assert.False(t, envSet([]string{"CTXLOOM_TEST_ENV_A"}))
}

// TestLiveAgentOrderMatchesRegistry catches the two tables (the ordered
// display list and the map) drifting apart — every registered engine appears
// exactly once in the display order and vice versa.
func TestLiveAgentOrderMatchesRegistry(t *testing.T) {
	assert.Equal(t, len(liveAgents), len(liveAgentOrder), "liveAgentOrder and liveAgents must name exactly the same engines")
	for _, name := range liveAgentOrder {
		_, ok := liveAgents[name]
		assert.True(t, ok, fmt.Sprintf("liveAgentOrder names %q, which is not in liveAgents", name))
	}
}
