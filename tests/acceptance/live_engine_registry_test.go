// Untagged like live_engine_registry.go: these tests exercise the pure
// availability-decision logic without needing a built ctxloom binary, a real
// engine binary, or any acceptance fixture plumbing, so `just test` gates on
// them directly.
package acceptance

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/shared/schema"
)

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

// claudeRow is the registered claude row with its binary swapped for one that
// is always on PATH, so these decisions run without claude installed.
func claudeRow() liveAgent {
	a := liveAgents["claude"]
	a.binary = "sh"
	return a
}

// TestEngineAvailable: an engine is available to a cell that runs THROUGH
// ctxloom only when the token its run authenticates with was captured at
// launch. An API key is not that token — production unsets it from every run —
// and no other credential (an ambient value, the real home's login) counts.
func TestEngineAvailable(t *testing.T) {
	cases := []struct {
		name      string
		agent     liveAgent
		creds     map[string]string // the launch capture
		env       map[string]string // the ambient environment, which must not count
		wantOK    bool
		wantMatch []string // substrings expected in the reason
	}{
		{
			name:      "binary not on PATH is unavailable regardless of the token",
			agent:     liveAgent{binary: "ctxloom-nonexistent-binary-xyz", engine: "claude-code"},
			creds:     map[string]string{claude.OAuthTokenEnv: "fake-token"},
			wantMatch: []string{`binary "ctxloom-nonexistent-binary-xyz" not found`},
		},
		{
			name:      "no binary configured at all is unavailable",
			agent:     liveAgent{engine: "claude-code"},
			creds:     map[string]string{claude.OAuthTokenEnv: "fake-token"},
			wantMatch: []string{"no binary configured"},
		},
		{
			name:      "the token captured at launch makes it available",
			agent:     claudeRow(),
			creds:     map[string]string{claude.OAuthTokenEnv: "fake-token"},
			wantOK:    true,
			wantMatch: []string{claude.OAuthTokenEnv + " captured at launch"},
		},
		{
			name:      "an API key alone is not the token: unavailable, naming the token and how to mint it",
			agent:     claudeRow(),
			creds:     map[string]string{claude.APIKeyEnv: "fake-key"},
			wantMatch: []string{claude.OAuthTokenEnv, "claude setup-token"},
		},
		{
			name:      "a token only in the AMBIENT environment does not count",
			agent:     claudeRow(),
			env:       map[string]string{claude.OAuthTokenEnv: "ambient-token"},
			wantMatch: []string{claude.OAuthTokenEnv, "claude setup-token"},
		},
		{
			name:      "an engine the registry does not know is unavailable, by name",
			agent:     liveAgent{binary: "sh", engine: "no-such-engine"},
			creds:     map[string]string{claude.OAuthTokenEnv: "fake-token"},
			wantMatch: []string{"no-such-engine"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withLaunchCredentials(t, tc.creds)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			ok, reason := engineAvailable(tc.agent)
			assert.Equal(t, tc.wantOK, ok, "reason was: %s", reason)
			for _, m := range tc.wantMatch {
				assert.Contains(t, reason, m)
			}
			for _, v := range tc.creds {
				assert.NotContains(t, reason, v, "a reason is printed: it names variables, never a value")
			}
		})
	}
}

// TestDirectEngineStatus: a cell that runs the claude binary DIRECTLY takes
// what the binary itself accepts — the token or an API key captured at launch,
// in the agent's preference order — and nothing else.
func TestDirectEngineStatus(t *testing.T) {
	cases := []struct {
		name      string
		agent     liveAgent
		creds     map[string]string
		wantOK    bool
		wantMatch []string
	}{
		{
			name:      "the token makes it available",
			agent:     claudeRow(),
			creds:     map[string]string{claude.OAuthTokenEnv: "fake-token"},
			wantOK:    true,
			wantMatch: []string{claude.OAuthTokenEnv + " captured at launch"},
		},
		{
			name:      "an API key alone makes it available: the vendor CLI accepts one",
			agent:     claudeRow(),
			creds:     map[string]string{claude.APIKeyEnv: "fake-key"},
			wantOK:    true,
			wantMatch: []string{claude.APIKeyEnv + " captured at launch"},
		},
		{
			name:      "nothing captured is unavailable, naming every variable it would take",
			agent:     claudeRow(),
			wantMatch: []string{claude.OAuthTokenEnv, claude.APIKeyEnv, "claude setup-token"},
		},
		{
			name:      "binary not on PATH is unavailable",
			agent:     liveAgent{binary: "ctxloom-nonexistent-binary-xyz", vendorCredEnvs: []string{claude.OAuthTokenEnv}},
			creds:     map[string]string{claude.OAuthTokenEnv: "fake-token"},
			wantMatch: []string{"not found"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withLaunchCredentials(t, tc.creds)
			status := directEngineStatus("claude", tc.agent)
			assert.Equal(t, "claude", status.name)
			assert.Equal(t, tc.wantOK, status.available, "reason was: %s", status.reason)
			for _, m := range tc.wantMatch {
				assert.Contains(t, status.reason, m)
			}
		})
	}
}

func TestProbeEngine_CarriesName(t *testing.T) {
	a := liveAgent{binary: "ctxloom-nonexistent-binary-xyz"}
	status := probeEngine("widget", a)
	assert.Equal(t, "widget", status.name)
	assert.False(t, status.available)
	assert.Contains(t, status.reason, "not found")
}

// TestCaptureLaunchCredentials_HoldsWhatTheTokenGateReads binds the capture to
// production's answer: the variables the engine's token mode reads must be
// among those captured, or every through-ctxloom cell skips for a token the
// suite was launched with.
func TestCaptureLaunchCredentials_HoldsWhatTheTokenGateReads(t *testing.T) {
	t.Setenv(claude.OAuthTokenEnv, "fake-token")
	t.Setenv(claude.APIKeyEnv, "fake-key")
	withLaunchCredentials(t, captureLaunchCredentials())
	auth := probeTokenAuth(liveAgents["claude"].engine)
	require.True(t, auth.ok(), "reason: %s", auth.Reason)
	assert.Equal(t, map[string]string{claude.OAuthTokenEnv: "fake-token"}, auth.Env, "token mode takes the token and nothing else")
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
	report := computeLiveEngineReport()
	if assert.Len(t, report, len(liveAgentOrder)) {
		for i, name := range liveAgentOrder {
			assert.Equal(t, name, report[i].name)
		}
	}
}

func TestFormatLiveEngineReport_AllUnavailable(t *testing.T) {
	report := []engineStatus{
		{name: "claude", available: false, reason: "CLAUDE_CODE_OAUTH_TOKEN is not exported"},
	}
	got := formatLiveEngineReport(report)
	assert.Equal(t, `live engines: claude ✗ (CLAUDE_CODE_OAUTH_TOKEN is not exported)`, got)
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

// TestVendorCredential: the first CAPTURED vendorCredEnvs entry wins, in the
// agent's preference order; an ambient value the capture does not hold is
// ignored; nothing captured is ok=false.
func TestVendorCredential(t *testing.T) {
	a := liveAgent{vendorCredEnvs: []string{"CTXLOOM_TEST_ENV_A", "CTXLOOM_TEST_ENV_B"}}

	withLaunchCredentials(t, map[string]string{"CTXLOOM_TEST_ENV_A": "a-val", "CTXLOOM_TEST_ENV_B": "b-val"})
	got, ok := vendorCredential(a)
	assert.True(t, ok)
	assert.Equal(t, credentialMapping{EnvVar: "CTXLOOM_TEST_ENV_A", Value: "a-val"}, got, "preference order is vendorCredEnvs order")

	withLaunchCredentials(t, map[string]string{"CTXLOOM_TEST_ENV_B": "b-val"})
	got, ok = vendorCredential(a)
	assert.True(t, ok)
	assert.Equal(t, "CTXLOOM_TEST_ENV_B", got.EnvVar)

	withLaunchCredentials(t, nil)
	t.Setenv("CTXLOOM_TEST_ENV_A", "ambient")
	_, ok = vendorCredential(a)
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

// --- seeding a through-ctxloom cell -----------------------------------------

// recordEnv is a stand-in for TestEnvironment.SetChildEnv: it records exactly
// what key/value pairs seedLiveCredentials put onto the child environment, so
// these tests can assert THE VARIABLE THAT WAS ACTUALLY SET AND ITS VALUE —
// not merely that a function was called or that no error came back.
func recordEnv() (map[string]string, func(string, string)) {
	got := map[string]string{}
	return got, func(k, v string) { got[k] = v }
}

// The token is SET on the child — exactly that variable and value, because the
// scrub removed it from what the child inherits — and an API key captured
// beside it is not: production unsets it from every run, so handing it over
// would only make the cell differ from what it verifies.
func TestSeedLiveCredentials_SetsTheTokenAndNothingElse(t *testing.T) {
	withLaunchCredentials(t, map[string]string{claude.OAuthTokenEnv: "fake-token", claude.APIKeyEnv: "fake-key"})
	got, setEnv := recordEnv()
	require.NoError(t, seedLiveCredentials(liveAgents["claude"], setEnv))
	assert.Equal(t, map[string]string{claude.OAuthTokenEnv: "fake-token"}, got)
}

// Without the token the seed is a NAMED failure naming the fix — never a run
// on an API key production would unset, and never the real home's login.
func TestSeedLiveCredentials_NoTokenIsLoud(t *testing.T) {
	for name, creds := range map[string]map[string]string{
		"nothing captured": nil,
		"API key alone":    {claude.APIKeyEnv: "fake-key"},
	} {
		t.Run(name, func(t *testing.T) {
			withLaunchCredentials(t, creds)
			got, setEnv := recordEnv()
			err := seedLiveCredentials(liveAgents["claude"], setEnv)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "claude setup-token")
			assert.Contains(t, err.Error(), claude.OAuthTokenEnv)
			assert.NotContains(t, err.Error(), "fake-key")
			assert.Empty(t, got)
		})
	}
}
