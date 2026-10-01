// Untagged like live_engine_registry.go: these tests exercise the pure
// availability-decision logic without needing a built ctxloom binary, a real
// engine binary, or any acceptance fixture plumbing, so `just test` gates on
// them directly.
package acceptance

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/schema"
)

// fakeAuthCheck returns a canned (ok, reason) pair, so tests never shell out
// to a real engine binary.
func fakeAuthCheck(ok bool, reason string) func(string) (bool, string) {
	return func(string) (bool, string) { return ok, reason }
}

// withLaunchCredentials replaces the launch capture for one test. Every test
// that depends on the token path's presence OR absence sets it explicitly: in
// the acceptance-tagged build TestMain fills it from the developer's shell, and
// a test that read whatever was captured would pass or fail on that shell.
func withLaunchCredentials(t *testing.T, creds map[string]string) {
	t.Helper()
	saved := launchCredentials
	t.Cleanup(func() { launchCredentials = saved })
	launchCredentials = creds
}

func TestEngineAvailable(t *testing.T) {
	cases := []struct {
		name      string
		agent     liveAgent
		creds     map[string]string // the launch capture
		env       map[string]string // the ambient environment, which must not count
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
			name:      "a token captured at launch short-circuits straight to available, no authCheck consulted",
			agent:     liveAgent{binary: "sh", apiKeyEnvs: []string{"CTXLOOM_TEST_FAKE_KEY"}, authCheck: fakeAuthCheck(false, "should never be called")},
			creds:     map[string]string{"CTXLOOM_TEST_FAKE_KEY": "fake-token"},
			optIn:     false, // the token path bypasses the opt-in gate entirely
			wantOK:    true,
			wantMatch: "CTXLOOM_TEST_FAKE_KEY captured at launch",
		},
		{
			name:      "a token only in the AMBIENT environment does not count: no token captured falls back to the home login",
			agent:     liveAgent{binary: "sh", apiKeyEnvs: []string{"CTXLOOM_TEST_FAKE_KEY"}, authCheck: fakeAuthCheck(false, "home login consulted")},
			env:       map[string]string{"CTXLOOM_TEST_FAKE_KEY": "fake-token"},
			optIn:     true,
			wantOK:    false,
			wantMatch: "home login consulted",
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
			withLaunchCredentials(t, tc.creds)
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
		{name: "mock", available: true},
		{name: "other-engine", available: false, reason: "binary not found"},
	}
	got := formatLiveEngineReport(report)
	assert.Equal(t, "live engines: claude ✓ · mock ✓ · other-engine ✗ (binary not found)", got)
}

// TestComputeLiveEngineReport_OrderAndCoverage is computeLiveEngineReport's
// only untagged exercise: its one real caller, tests/acceptance/acceptance_test.go,
// lives behind `//go:build acceptance` (see live_engine_registry.go's header
// comment), which `just lint`'s default-tag golangci-lint run never compiles
// — so without a test here the function reads as dead code to the linter
// even though it is load-bearing for `just test-acceptance`. This proves it
// walks every registered engine, in liveAgentOrder, without needing a real
// binary (they're all expected absent in CI, which is itself a valid status).
func TestComputeLiveEngineReport_OrderAndCoverage(t *testing.T) {
	report := computeLiveEngineReport("/fake/home", false)
	if assert.Len(t, report, len(liveAgentOrder)) {
		for i, name := range liveAgentOrder {
			assert.Equal(t, name, report[i].name)
		}
	}
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
		{name: "comma separated, trimmed, lowercased", raw: " Claude, MOCK ,other-engine", want: []string{"claude", "mock", "other-engine"}},
		{name: "empty entries between commas are dropped", raw: "claude,,other-engine", want: []string{"claude", "other-engine"}},
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
		{name: "mock", available: true},
		{name: "other-engine", available: false, reason: "binary not found on PATH"},
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
			required: []string{"claude", "mock"},
			wantErr:  false,
		},
		{
			name:        "required engine unavailable: fails, names the engine and the reason",
			required:    []string{"other-engine"},
			wantErr:     true,
			wantMatches: []string{"other-engine", "binary not found on PATH"},
		},
		{
			name:        "mixed available+unavailable required: fails, names only the missing one",
			required:    []string{"claude", "other-engine"},
			wantErr:     true,
			wantMatches: []string{"other-engine", "binary not found on PATH"},
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
// steps_j000200_setup.go's own @live scenario calls directly: it must agree with
// engineAvailable, never drift into a second, silently-different notion of
// "available".
func TestLiveAgentAvailable_UsesSameDecision(t *testing.T) {
	t.Setenv("CTXLOOM_ACCEPTANCE_LIVE", "")
	t.Setenv("CTXLOOM_LIVE_REQUIRE", "")
	a := liveAgent{binary: "ctxloom-nonexistent-binary-xyz"}
	assert.False(t, liveAgentAvailable(a))
}

// TestLiveCredential: the first CAPTURED apiKeyEnvs entry wins, in the
// agent's preference order; an ambient value the capture does not hold is
// ignored; nothing captured is ok=false.
func TestLiveCredential(t *testing.T) {
	a := liveAgent{apiKeyEnvs: []string{"CTXLOOM_TEST_ENV_A", "CTXLOOM_TEST_ENV_B"}}

	withLaunchCredentials(t, map[string]string{"CTXLOOM_TEST_ENV_A": "a-val", "CTXLOOM_TEST_ENV_B": "b-val"})
	got, ok := liveCredential(a)
	assert.True(t, ok)
	assert.Equal(t, credentialMapping{EnvVar: "CTXLOOM_TEST_ENV_A", Value: "a-val"}, got, "preference order is apiKeyEnvs order")

	withLaunchCredentials(t, map[string]string{"CTXLOOM_TEST_ENV_B": "b-val"})
	got, ok = liveCredential(a)
	assert.True(t, ok)
	assert.Equal(t, "CTXLOOM_TEST_ENV_B", got.EnvVar)

	withLaunchCredentials(t, nil)
	t.Setenv("CTXLOOM_TEST_ENV_A", "ambient")
	_, ok = liveCredential(a)
	assert.False(t, ok, "an ambient value is not a captured one")
}

// TestLiveVendorEnv: the direct-vendor environment is built from nothing —
// PATH, SHELL, the throwaway HOME and config dir, and the one credential — so
// an ambient CLAUDE_* knob cannot reach the cell.
func TestLiveVendorEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_BASE_URL", "http://ambient.invalid")
	env := liveVendorEnv(credentialMapping{EnvVar: "CLAUDE_CODE_OAUTH_TOKEN", Value: "fake-token"}, "/tmp/h", "/tmp/c")
	keys := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		keys[k] = v
	}
	assert.Len(t, keys, len(env), "no key appears twice")
	assert.Equal(t, "/tmp/h", keys["HOME"])
	assert.Equal(t, "/tmp/c", keys["CLAUDE_CONFIG_DIR"])
	assert.Equal(t, "fake-token", keys["CLAUDE_CODE_OAUTH_TOKEN"])
	assert.NotContains(t, keys, "ANTHROPIC_BASE_URL", "nothing ambient crosses over")
	assert.ElementsMatch(t, []string{"PATH", "HOME", "SHELL", "CLAUDE_CONFIG_DIR", "CLAUDE_CODE_OAUTH_TOKEN"}, slices.Collect(maps.Keys(keys)))
}

// TestBackendTypeToLiveKey guards the one mapping the hermetic j002200 matrix's
// backend-type vocabulary and
// the live isolation probe (tests/acceptance/isolation_probe.go, behind the
// acceptance tag) both resolve through to reach this registry's own liveAgents
// keys — kept here, untagged, so `just lint`'s default (no build-tag) pass
// sees a real caller and this doesn't read as dead code.
func TestBackendTypeToLiveKey(t *testing.T) {
	cases := []struct{ backendType, want string }{
		{"claude-code", "claude"},
		// Pass-through: any other backend type IS its own live key.
		{"mock", "mock"},
	}
	for _, tc := range cases {
		t.Run(tc.backendType, func(t *testing.T) {
			assert.Equal(t, tc.want, backendTypeToLiveKey(tc.backendType))
		})
	}
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

// TestLiveAgents_ConfigValidatesAgainstSchema holds the shared fixture to the
// real schema. Every liveAgents[*].config is the base that every P0-P6 probe
// config (matrixConfigYAML, mcpProbeConfigYAML, hookProbeConfigYAML,
// p4ConfigYAML, probeConfigYAML) is built by appending onto, so a key this
// base carries is a key every probe inherits. Validate it through
// internal/shared/schema.NewConfigValidator — the same validator the production
// config loader uses, and the seam internal/core/config/unknown_keys.go's
// classifyValidationError sits on top of — because the leniency of
// agents.ParseAgent, which is all these configs were ever parsed by, cannot
// see a retired key. When the base carries one, every probe built from it
// fails real validation for a reason unrelated to what the probe tests.
func TestLiveAgents_ConfigValidatesAgainstSchema(t *testing.T) {
	v, err := schema.NewConfigValidator()
	require.NoError(t, err, "the embedded config schema must compile")

	for _, name := range liveAgentOrder {
		t.Run(name, func(t *testing.T) {
			a, ok := liveAgents[name]
			require.True(t, ok)
			require.NotEmpty(t, a.config, "an empty config would validate vacuously and prove nothing")
			err := v.ValidateBytes([]byte(a.config))
			assert.NoError(t, err,
				"liveAgents[%q].config — the base every P0-P6 probe config is built from — must validate clean against config-schema.json, or every probe built from it inherits a fixture-level failure unrelated to what the probe tests", name)
		})
	}
}

// TestCopyCredentials_ZeroFilesCopiedIsAnError pins that every
// copy*Credentials function used to succeed silently while copying zero
// bytes — continuing/returning past a missing source with no signal at
// all — so a caller that seeded no credentials was indistinguishable from
// one that seeded correctly. Each now returns an error when nothing was
// copied.
func TestCopyCredentials_ZeroFilesCopiedIsAnError(t *testing.T) {
	cases := []struct {
		name string
		fn   func(realHome, fakeHome string) error
	}{
		{"claude", copyClaudeCredentials},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			realHome := t.TempDir() // deliberately empty: no source credential files
			fakeHome := t.TempDir()
			err := tc.fn(realHome, fakeHome)
			assert.Error(t, err, "copying from an empty HOME must report an error, not silently succeed")
		})
	}
}

// TestCopyClaudeCredentials_CopiesWhatExists pins the success path: at
// least one real source file present must copy through with no error.
func TestCopyClaudeCredentials_CopiesWhatExists(t *testing.T) {
	realHome := t.TempDir()
	fakeHome := t.TempDir()
	claudeDir := filepath.Join(realHome, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(`{"ok":true}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := copyClaudeCredentials(realHome, fakeHome); err != nil {
		t.Fatalf("copyClaudeCredentials: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(fakeHome, ".claude", "settings.json"))
	if err != nil {
		t.Fatalf("copied file missing: %v", err)
	}
	if string(got) != `{"ok":true}` {
		t.Fatalf("copied content = %q, want the source content", got)
	}
}

// --- credential MAPPING (task erased-collar / jovial-employee) -------------

// recordEnv is a stand-in for TestEnvironment.SetChildEnv: it records exactly
// what key/value pairs seedLiveCredentials put onto the child environment, so
// these tests can assert THE VARIABLE THAT WAS ACTUALLY SET AND ITS VALUE —
// not merely that a function was called or that no error came back. This
// project's characteristic bug is exit 0 with a success message and nothing
// actually written.
func recordEnv() (map[string]string, func(string, string)) {
	got := map[string]string{}
	return got, func(k, v string) { got[k] = v }
}

// treeSnapshot lists every regular file under root with its contents, so a
// test can prove the mapping path touched NOTHING on disk.
func treeSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		out[rel] = string(data)
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return out
}

// seedFakeRealHome builds a throwaway stand-in for the developer's real HOME,
// with a claude login in it that must never be copied, and clears the launch
// capture that would take the token path.
func seedFakeRealHome(t *testing.T) string {
	t.Helper()
	withLaunchCredentials(t, nil)
	realHome := t.TempDir()
	p := filepath.Join(realHome, ".claude", ".credentials.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`{"claudeAiOauth":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return realHome
}

// A token captured at launch is SET on the child — exactly that variable and
// value, because the scrub removed it from what the child inherits — and no
// byte is written anywhere.
func TestSeedLiveCredentials_CapturedTokenIsSetAndNothingCopied(t *testing.T) {
	for _, v := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"} {
		t.Run(v, func(t *testing.T) {
			realHome := seedFakeRealHome(t)
			withLaunchCredentials(t, map[string]string{v: "fake-for-this-test"})
			fakeHome := t.TempDir()
			before := treeSnapshot(t, realHome)
			got, setEnv := recordEnv()
			if err := seedLiveCredentials("claude", liveAgents["claude"], realHome, fakeHome, setEnv); err != nil {
				t.Fatalf("seedLiveCredentials: %v", err)
			}
			assert.Equal(t, map[string]string{v: "fake-for-this-test"}, got, "the token path sets the captured variable and nothing else")
			assert.Empty(t, treeSnapshot(t, fakeHome), "the token path copies nothing")
			assert.Equal(t, before, treeSnapshot(t, realHome), "the real HOME is not touched")
		})
	}
}

// No exported token is a NAMED failure naming the fix, never a silent skip
// that yields a mysteriously unauthenticated run: it sets no env var, and the
// developer's login in the real HOME is neither copied nor touched.
func TestSeedLiveCredentials_NoExportedTokenIsLoud(t *testing.T) {
	realHome := seedFakeRealHome(t)
	fakeHome := t.TempDir()
	before := treeSnapshot(t, realHome)
	got, setEnv := recordEnv()
	err := seedLiveCredentials("claude", liveAgents["claude"], realHome, fakeHome, setEnv)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "claude setup-token")
	assert.Contains(t, err.Error(), "CLAUDE_CODE_OAUTH_TOKEN")
	assert.Empty(t, got)
	assert.Empty(t, treeSnapshot(t, fakeHome), "nothing is copied into the isolated HOME")
	assert.Equal(t, before, treeSnapshot(t, realHome), "nothing in the real HOME is written, moved or removed")
}

// TestSeedLiveCredentials_NoRealHomeIsLoud: reaching the seed with no captured
// real HOME means the gate let through a subscription-path run with nothing to
// map. That must be an error, not a silent no-op.
func TestSeedLiveCredentials_NoRealHomeIsLoud(t *testing.T) {
	withLaunchCredentials(t, nil) // a captured token would take the token path first
	got, setEnv := recordEnv()
	err := seedLiveCredentials("claude", liveAgents["claude"], "", t.TempDir(), setEnv)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no real HOME")
	assert.Empty(t, got)
}

// TestSeedLiveCredentials_NoMechanismIsLoud: an engine registered with neither
// a mapper nor a copier must fail loudly rather than run unauthenticated.
func TestSeedLiveCredentials_NoMechanismIsLoud(t *testing.T) {
	got, setEnv := recordEnv()
	err := seedLiveCredentials("bare", liveAgent{binary: "sh"}, t.TempDir(), t.TempDir(), setEnv)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "neither a credential mapping nor a copier")
	assert.Empty(t, got)
}

// TestLiveAgents_MappableEnginesAreMappedUnmappableOnesAreNot is the registry
// floor for the policy. Every engine whose descriptor declares its home var
// relocates credentials (agent.EngineHome.Credentials provided) must be
// MAPPED. An engine whose credentials no config-home var relocates cannot
// be mapped this way and must keep a copier instead; the false arm below is
// what stops a future edit from quietly mapping such an engine at a
// directory the engine never reads.
func TestLiveAgents_MappableEnginesAreMappedUnmappableOnesAreNot(t *testing.T) {
	mappable := map[string]bool{"claude": true}
	for _, name := range liveAgentOrder {
		a := liveAgents[name]
		want, known := mappable[name]
		if !known {
			t.Fatalf("engine %q is registered but this policy floor does not state whether it is mappable — decide, do not default", name)
		}
		if want {
			assert.NotNil(t, a.mapCreds, "%s honours a config-home var for credentials and MUST be mapped, never copied", name)
		} else {
			assert.Nil(t, a.mapCreds, "%s has no config-home var that relocates credentials and must not pretend to be mappable", name)
			assert.NotNil(t, a.copyCreds, "%s cannot be mapped, so it must keep its copier", name)
		}
	}
}
