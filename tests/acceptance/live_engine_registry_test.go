// Untagged like live_engine_registry.go: these tests exercise the pure
// availability-decision logic without needing a built ctxloom binary, a real
// engine binary, or any acceptance fixture plumbing, so `just test` gates on
// them directly.
package acceptance

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
		{name: "kiro", available: true},
		{name: "codex", available: false, reason: "binary not found"},
	}
	got := formatLiveEngineReport(report)
	assert.Equal(t, "live engines: claude ✓ · kiro ✓ · codex ✗ (binary not found)", got)
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
		{name: "comma separated, trimmed, lowercased", raw: " Claude, KIRO ,codex", want: []string{"claude", "kiro", "codex"}},
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
			required: []string{"claude", "kiro"},
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
// steps_j000200_setup.go's own @live scenario calls directly: it must agree with
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

// TestBackendTypeToLiveKey guards the one mapping the hermetic j002200 matrix's
// backend-type vocabulary (claude-code/codex/kiro/opencode) and
// the live isolation probe (tests/acceptance/isolation_probe.go, behind the
// acceptance tag) both resolve through to reach this registry's own liveAgents
// keys — kept here, untagged, so `just lint`'s default (no build-tag) pass
// sees a real caller and this doesn't read as dead code.
func TestBackendTypeToLiveKey(t *testing.T) {
	cases := []struct{ backendType, want string }{
		{"claude-code", "claude"},
		{"codex", "codex"},
		{"kiro", "kiro"},
		{"opencode", "opencode"},
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

// TestCodexRegistryEntry_IsWiredNotStub guards against a regression back to
// the old authCheckCodex stub ("codex has no live authentication probe
// implemented (declared but unavailable)") — codex now has a real probe, a
// real credential copier, and a real pinned-cheap-model config, confirmed
// live 2026-07-14 against an authenticated `codex login status`.
func TestCodexRegistryEntry_IsWiredNotStub(t *testing.T) {
	a, ok := liveAgents["codex"]
	assert.True(t, ok, "codex must remain a registered live agent")
	assert.Equal(t, "codex", a.binary)
	assert.Equal(t, ".codex", a.credDir)
	assert.NotNil(t, a.authCheck, "codex must have a real authCheck, not the old permanently-unavailable stub")
	assert.NotNil(t, a.copyCreds, "codex must have a credential copier, not nil (the old declared-but-unavailable state)")
	assert.Contains(t, a.config, "type: codex")
	assert.Contains(t, a.config, "gpt-5.4-mini", "codex must pin a cheap model — live tests prove context delivery, not model quality")
}

// TestCopyCredentials_ZeroFilesCopiedIsAnError pins that every
// copy*Credentials function used to succeed silently while copying zero
// bytes — continuing/returning past a missing source with no signal at
// all — so a caller that seeded no credentials was indistinguishable from
// one that seeded correctly. All four now return an error when nothing was
// copied.
func TestCopyCredentials_ZeroFilesCopiedIsAnError(t *testing.T) {
	cases := []struct {
		name string
		fn   func(realHome, fakeHome string) error
	}{
		{"claude", copyClaudeCredentials},
		{"kiro", copyKiroCredentials},
		{"codex", copyCodexCredentials},
		{"opencode", copyOpencodeCredentials},
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

// seedFakeRealHome builds a throwaway stand-in for the developer's real HOME
// carrying the one credential file each mappable engine requires.
func seedFakeRealHome(t *testing.T) string {
	t.Helper()
	realHome := t.TempDir()
	for rel, body := range map[string]string{
		filepath.Join(".claude", ".credentials.json"):             `{"claudeAiOauth":{"refreshToken":"real"}}`,
		filepath.Join(".codex", "auth.json"):                      `{"tokens":{"refresh_token":"real"}}`,
		filepath.Join(".local", "share", "opencode", "auth.json"): `{"openrouter":{"type":"api"}}`,
	} {
		p := filepath.Join(realHome, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return realHome
}

// TestMapCredentials_PointAtTheRealDirectory is the core of the mapped-not-copied
// policy: each mappable engine's own config-home env var is set to the REAL
// host directory, by exact name and exact value. Copying is what consumed the
// human's codex refresh token (jovial-employee); mapping lets the engine
// rotate the REAL file in place.
func TestMapCredentials_PointAtTheRealDirectory(t *testing.T) {
	realHome := seedFakeRealHome(t)
	cases := []struct {
		engine  string
		wantVar string
		wantDir string
	}{
		// credentialSeedSpecs["claude-code"]: HonoursVarForCreds true,
		// CLAUDE_CONFIG_DIR relocates config AND credentials.
		{"claude", "CLAUDE_CONFIG_DIR", filepath.Join(realHome, ".claude")},
		// credentialSeedSpecs["codex"]: HonoursVarForCreds true, auth.json
		// resolves from $CODEX_HOME only.
		{"codex", "CODEX_HOME", filepath.Join(realHome, ".codex")},
		// credentialSeedSpecs["opencode"]: HonoursVarForCreds true, but its
		// destSubdir is NESTED because opencode appends "/opencode" itself —
		// so the var points one level ABOVE the credential directory.
		{"opencode", "XDG_DATA_HOME", filepath.Join(realHome, ".local", "share")},
	}
	for _, tc := range cases {
		t.Run(tc.engine, func(t *testing.T) {
			a := liveAgents[tc.engine]
			if a.mapCreds == nil {
				t.Fatalf("%s must have a credential mapper", tc.engine)
			}
			mappings, err := a.mapCreds(realHome)
			if err != nil {
				t.Fatalf("mapCreds: %v", err)
			}
			assert.Equal(t, []credentialMapping{{EnvVar: tc.wantVar, Dir: tc.wantDir}}, mappings)
		})
	}
}

// TestSeedLiveCredentials_SetsTheMappedVarOnTheChild pins the whole gate path
// end to end: the value that lands on the child env is the REAL directory, and
// nothing whatsoever is written — not into the isolated HOME, not into the real
// one. A copy-based seed would fail both halves.
func TestSeedLiveCredentials_SetsTheMappedVarOnTheChild(t *testing.T) {
	realHome := seedFakeRealHome(t)
	fakeHome := t.TempDir()
	before := treeSnapshot(t, realHome)

	got, setEnv := recordEnv()
	if err := seedLiveCredentials("codex", liveAgents["codex"], realHome, fakeHome, setEnv); err != nil {
		t.Fatalf("seedLiveCredentials: %v", err)
	}

	assert.Equal(t, map[string]string{"CODEX_HOME": filepath.Join(realHome, ".codex")}, got,
		"codex must be MAPPED at the real ~/.codex, by that exact var and value")
	assert.Empty(t, treeSnapshot(t, fakeHome),
		"mapping must write NOTHING into the isolated HOME — a copy there is the jovial-employee bug")
	assert.Equal(t, before, treeSnapshot(t, realHome),
		"mapping must not write, move, truncate or delete anything in the real HOME")
}

// TestSeedLiveCredentials_APIKeyPathMapsAndCopiesNothing pins the second of the
// policy's two permitted paths: an API key rides the inherited env, so no var
// is set and no byte is written.
func TestSeedLiveCredentials_APIKeyPathMapsAndCopiesNothing(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-fake-for-this-test")
	realHome := seedFakeRealHome(t)
	fakeHome := t.TempDir()

	got, setEnv := recordEnv()
	if err := seedLiveCredentials("codex", liveAgents["codex"], realHome, fakeHome, setEnv); err != nil {
		t.Fatalf("seedLiveCredentials: %v", err)
	}
	assert.Empty(t, got, "the API-key path must set no credential mapping at all")
	assert.Empty(t, treeSnapshot(t, fakeHome), "the API-key path must copy nothing")
}

// TestSeedLiveCredentials_MissingCredentialIsLoud preserves the copiers' own
// "copied 0 files" loudness on the mapping path: a real credential directory
// or file that is not there must produce a NAMED failure, never a silent skip
// that yields a mysteriously unauthenticated run — and must set no env var,
// so a half-mapped child can never happen.
func TestSeedLiveCredentials_MissingCredentialIsLoud(t *testing.T) {
	cases := []struct {
		engine   string
		wantText string // the exact directory the failure must name
	}{
		{"claude", ".claude"},
		{"codex", ".codex"},
		{"opencode", filepath.Join(".local", "share")},
	}
	for _, tc := range cases {
		t.Run(tc.engine, func(t *testing.T) {
			realHome := t.TempDir() // deliberately empty: no credential material
			fakeHome := t.TempDir()
			got, setEnv := recordEnv()
			err := seedLiveCredentials(tc.engine, liveAgents[tc.engine], realHome, fakeHome, setEnv)
			assert.Error(t, err, "an absent real credential directory must fail loudly")
			assert.Contains(t, err.Error(), tc.engine, "the failure must name the engine")
			assert.Contains(t, err.Error(), tc.wantText, "the failure must name the credential that is missing")
			assert.Empty(t, got, "a failed mapping must leave no env var set on the child")
		})
	}
}

// TestMapCredentialHome_RequiredFileMissingNamesIt covers the half
// TestSeedLiveCredentials_MissingCredentialIsLoud cannot: the directory EXISTS
// but the credential file inside it does not — the "logged out, stale dir left
// behind" case, which a mere directory-presence check would wave through.
func TestMapCredentialHome_RequiredFileMissingNamesIt(t *testing.T) {
	realHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(realHome, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := mapCodexCredentials(realHome)
	assert.Error(t, err, "an existing $CODEX_HOME with no auth.json must fail loudly")
	assert.Contains(t, err.Error(), filepath.Join(realHome, ".codex", "auth.json"))
}

// TestMapCredentialHome_NotADirectoryIsRejected pins that a FILE at the
// mapping target is refused. Mapping is a directory contract: bind-mounting or
// pointing at a single file does not survive the atomic temp-file-plus-rename
// that credential writers use.
func TestMapCredentialHome_NotADirectoryIsRejected(t *testing.T) {
	realHome := t.TempDir()
	p := filepath.Join(realHome, ".codex")
	if err := os.WriteFile(p, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := mapCodexCredentials(realHome)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not a directory")
}

// TestSeedLiveCredentials_NoRealHomeIsLoud: reaching the seed with no captured
// real HOME means the gate let through a subscription-path run with nothing to
// map. That must be an error, not a silent no-op.
func TestSeedLiveCredentials_NoRealHomeIsLoud(t *testing.T) {
	got, setEnv := recordEnv()
	err := seedLiveCredentials("codex", liveAgents["codex"], "", t.TempDir(), setEnv)
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
// floor for the policy. claude/codex/opencode are the three engines
// internal/lm/isolation/auth.go's credentialSeedSpecs records as
// HonoursVarForCreds TRUE, so all three must be MAPPED. kiro
// (HonoursVarForCreds FALSE — its subscription auth is a global sqlite no
// HomeVar relocates) cannot be mapped this way; it keeps the copier until
// the human decides, and this pins that so a future edit cannot quietly map
// it at a directory the engine never reads.
func TestLiveAgents_MappableEnginesAreMappedUnmappableOnesAreNot(t *testing.T) {
	mappable := map[string]bool{"claude": true, "codex": true, "opencode": true, "kiro": false}
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

// TestSeedLiveCredentials_UnmappableEngineFallsBackToCopy pins the escalated
// engines' preserved behaviour: kiro still seeds by copy, and sets no env var
// (setting one would silently fork its global credential store).
func TestSeedLiveCredentials_UnmappableEngineFallsBackToCopy(t *testing.T) {
	realHome := t.TempDir()
	kiroDir := filepath.Join(realHome, ".local", "share", "kiro-cli")
	if err := os.MkdirAll(kiroDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kiroDir, "data.sqlite3"), []byte("sqlite-ish"), 0o600); err != nil {
		t.Fatal(err)
	}
	fakeHome := t.TempDir()

	got, setEnv := recordEnv()
	if err := seedLiveCredentials("kiro", liveAgents["kiro"], realHome, fakeHome, setEnv); err != nil {
		t.Fatalf("seedLiveCredentials: %v", err)
	}
	assert.Empty(t, got, "an unmappable engine must set no credential env var")
	assert.Equal(t, map[string]string{filepath.Join(".local", "share", "kiro-cli", "data.sqlite3"): "sqlite-ish"},
		treeSnapshot(t, fakeHome), "kiro keeps its copier until the human decides")
}
