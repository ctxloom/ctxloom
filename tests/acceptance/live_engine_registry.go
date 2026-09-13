// Package acceptance: the live-engine registry.
//
// This file is DELIBERATELY untagged (no `//go:build acceptance`) so its pure
// decision logic — is engine X's binary on PATH, is it authenticated, does
// the require-list floor pass — is reachable by `just test` (plain `go test
// ./...`), not only by the acceptance-tagged suite. Every other file in this
// directory carries the acceptance tag and needs a real built ctxloom binary
// plus fixture plumbing to run at all; this one has no such dependency and
// must stay that way.
//
// Three things live here, all declarative:
//
//  1. liveAgents: one entry per engine the @live suite can drive, describing
//     the BINARY to probe (not necessarily the engine's own name), how to
//     tell INSTALLED apart from AUTHENTICATED, the
//     credential material an isolated run needs, and one cheap pinned model.
//  2. computeLiveEngineReport / formatLiveEngineReport: what actually ran vs.
//     skipped, per engine, WITH THE REASON — printed on every acceptance run
//     (TestAcceptance), not only live ones, so credential expiry shows up as
//     a loud line instead of a silently-lower pass count.
//  3. parseRequiredEngines / checkRequiredEngines: the floor.
//     CTXLOOM_LIVE_REQUIRE naming an engine makes a missing/
//     unauthenticated engine a hard failure instead of a quiet skip — this is
//     what stops a credential expiry from silently deleting live coverage.
package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/config"
)

// realHomeDir is the user's actual home, captured in TestMain (acceptance_test.go)
// before any scenario overrides HOME. Used to locate each engine's real
// credential material (~/.claude, say) for the
// subscription-auth path, and to run each engine's own authentication probe.
var realHomeDir string

// authProbeTimeout bounds every authCheck subprocess (`claude auth status`,
// a local auth-status read). These are meant to be fast, local, non-interactive
// status reads — never a hung prompt and never a paid model call — so a
// generous-but-finite timeout catches a hang without slowing down a normal
// run, which pays this cost on EVERY acceptance run, live or not.
const authProbeTimeout = 8 * time.Second

// liveAgent describes one real backend the @live suite can drive. The same
// distillation/multi-engine scenarios run against each entry via a Scenario
// Outline, so the behavioral assertions stay backend-agnostic while auth,
// binary, and config differ.
type liveAgent struct {
	// binary is the executable actually probed on PATH. NOT necessarily the
	// same as the engine's own name in the Examples table.
	binary string
	// apiKeyEnvs are the env vars whose presence enables the unattended
	// API-key path. They flow to the CLI through the inherited subprocess
	// env, so nothing is copied for this path, and no authCheck subprocess
	// runs — an API key is its own proof of intent to use it.
	apiKeyEnvs []string
	// credDir is the per-agent credential directory under HOME (documentation
	// only — copyCreds below hardcodes its own exact paths).
	credDir string
	// config is the ctxloom config.yaml that points primary+fast at this
	// backend, pinned to ONE CHEAP MODEL where a verified slug exists: live
	// tests prove context DELIVERY, not model quality, and a bigger model
	// proves nothing extra while costing real money on every run.
	config string
	// mapCreds returns the env-var-to-real-directory pointers that make this
	// engine read AND WRITE its REAL credential files from inside an
	// otherwise-isolated run — the MAPPED half of the mapped-or-API-key
	// policy (task erased-collar). Non-nil only for engines whose own
	// config-home var relocates CREDENTIALS (the descriptor's
	// agent.EngineHome.Credentials is Provided: claude's CLAUDE_CONFIG_DIR,
	// say). It NEVER
	// writes, copies, moves
	// or chmods a credential file: it only points at directories, and errors
	// loudly when the real credential material is absent.
	//
	// WHY MAPPING, NOT COPYING (task jovial-employee): a copy into a
	// throwaway HOME is strictly one-way. The engine refreshes inside the
	// copy, the provider ROTATES the refresh token and invalidates the old
	// one SERVER-SIDE, the rotated value dies with the temp dir, and the
	// host's file is left holding a token the provider considers consumed —
	// measured as `401 refresh_token_reused`, which costs the
	// human a manual re-login. A read-only copy does NOT fix this: the
	// damage is the server-side consumption, not the local mutation. A
	// symlink does not fix it either — credential files are written with an
	// atomic temp-file-plus-rename, which replaces the symlink with a
	// regular file and leaves the real credential stale.
	mapCreds func(realHome string) ([]credentialMapping, error)
	// copyCreds is the LEGACY copy path. It copies just the auth files from
	// the real HOME into the isolated one, and errors when it copied zero
	// files — a caller that seeded no credentials must not be
	// indistinguishable from one that seeded correctly.
	//
	// It is NO LONGER how an @live scenario gate seeds an engine that has a
	// mapCreds mapper (seedLiveCredentials prefers mapping, always). It
	// survives for isolation_probe.go, whose census DELIBERATELY builds
	// a stand-in host home to measure what production's own seeding leaks —
	// mapping there would point the measurement at the developer's real
	// directories and destroy the thing being measured.
	copyCreds func(realHome, fakeHome string) error
	// authCheck determines whether the engine is AUTHENTICATED — not merely
	// installed — via the subscription path. Only consulted when apiKeyEnvs
	// is unset and the CTXLOOM_ACCEPTANCE_LIVE opt-in is set (see
	// engineAvailable). Returns ok plus a short, human reason either way; the
	// reason surfaces verbatim in the availability report and in the
	// CTXLOOM_LIVE_REQUIRE floor's failure message.
	authCheck func(realHome string) (ok bool, reason string)
}

// liveAgentOrder is the availability report's fixed display order. Kept
// separate from the map because map iteration order is unspecified and this
// report's whole point is to be predictable and diffable across runs.
var liveAgentOrder = []string{"claude"}

// liveAgents maps the lowercased scenario token ("claude") to its
// backend wiring.
var liveAgents = map[string]liveAgent{
	"claude": {
		binary:     "claude",
		apiKeyEnvs: []string{"ANTHROPIC_API_KEY"},
		credDir:    ".claude",
		config: fmt.Sprintf("version: %d\n", config.CurrentConfigVersion) + `llm:
  configs:
    claude:
      type: claude-code
      model: claude-haiku-4-5-20251001
  defaults:
    primary: claude
    fast: claude
`,
		mapCreds:  mapClaudeCredentials,
		copyCreds: copyClaudeCredentials,
		authCheck: authCheckClaude,
	},
}

// backendTypeToLiveKey maps a REGISTERED backend type name (the config
// `llm.configs.*.type` value, and the identifier
// tests/acceptance/steps_j002200_isolation_matrix.go's spy fixture and the
// isolation-probe feature both use: "claude-code")
// onto this registry's own liveAgents map key. Every name is
// identical except claude-code -> claude, a historical mismatch (the @live
// Examples tables predate the isolation matrix and used the short form).
// Extracted here — not duplicated — so both the hermetic j002200 matrix's engine
// vocabulary and the live isolation probe's agree on one mapping.
func backendTypeToLiveKey(backendType string) string {
	if backendType == "claude-code" {
		return "claude"
	}
	return backendType
}

// matchedEnv returns the first non-empty env var among names, or "".
func matchedEnv(names []string) string {
	for _, n := range names {
		if os.Getenv(n) != "" {
			return n
		}
	}
	return ""
}

// envSet reports whether any of the named env vars is non-empty.
func envSet(names []string) bool {
	return matchedEnv(names) != ""
}

// engineStatus is one row of the availability report: whether this engine
// will actually run in this suite, and why not when it will not.
type engineStatus struct {
	name      string
	available bool
	reason    string
}

// engineAvailable is the single decision the availability report, the
// CTXLOOM_LIVE_REQUIRE floor, and every @live step's gate all share — they
// can never disagree about whether an engine will run, because they all call
// this. optIn is the CTXLOOM_ACCEPTANCE_LIVE=1 (or CTXLOOM_LIVE_REQUIRE-implied,
// see resolveOptIn) opt-in for the subscription credential path, which copies
// local credentials and makes real, paid calls.
func engineAvailable(a liveAgent, realHome string, optIn bool) (bool, string) {
	if a.binary == "" {
		return false, "no binary configured for this engine"
	}
	if _, err := exec.LookPath(a.binary); err != nil {
		return false, fmt.Sprintf("binary %q not found on PATH", a.binary)
	}
	if k := matchedEnv(a.apiKeyEnvs); k != "" {
		return true, fmt.Sprintf("%s set", k)
	}
	if a.authCheck == nil {
		return false, "no authentication probe configured for this engine"
	}
	if !optIn {
		return false, "installed, but CTXLOOM_ACCEPTANCE_LIVE=1 not set (subscription credential path is opt-in)"
	}
	if realHome == "" {
		return false, "no real HOME captured to probe subscription credentials against"
	}
	return a.authCheck(realHome)
}

// probeEngine wraps engineAvailable with the engine's name, for the ordered
// report below.
func probeEngine(name string, a liveAgent, realHome string, optIn bool) engineStatus {
	ok, reason := engineAvailable(a, realHome, optIn)
	return engineStatus{name: name, available: ok, reason: reason}
}

// resolveOptIn is the single place that decides whether the subscription
// credential path is opted into. CTXLOOM_ACCEPTANCE_LIVE=1 is the direct
// opt-in a dev sets on a workstation. CTXLOOM_LIVE_REQUIRE implies the same
// opt-in: a require-list only makes sense if the subscription probe actually
// runs, and a caller that sets CTXLOOM_LIVE_REQUIRE but forgets
// CTXLOOM_ACCEPTANCE_LIVE should get a real "not authenticated" failure, not
// a confusing "opt-in not set" one for a flag it never knew to set.
func resolveOptIn() bool {
	if os.Getenv("CTXLOOM_ACCEPTANCE_LIVE") == "1" {
		return true
	}
	return len(parseRequiredEngines(os.Getenv("CTXLOOM_LIVE_REQUIRE"))) > 0
}

// computeLiveEngineReport probes every registered engine, in liveAgentOrder.
func computeLiveEngineReport(realHome string, optIn bool) []engineStatus {
	report := make([]engineStatus, 0, len(liveAgentOrder))
	for _, name := range liveAgentOrder {
		a, ok := liveAgents[name]
		if !ok {
			report = append(report, engineStatus{name: name, available: false, reason: "not registered"})
			continue
		}
		report = append(report, probeEngine(name, a, realHome, optIn))
	}
	return report
}

// formatLiveEngineReport renders the loud, one-line availability table, e.g.:
//
//	live engines: claude ✓
//
// A skip is never silent: every unavailable engine carries its reason inline,
// right next to the ones that ran.
func formatLiveEngineReport(report []engineStatus) string {
	parts := make([]string, 0, len(report))
	for _, s := range report {
		if s.available {
			parts = append(parts, fmt.Sprintf("%s ✓", s.name))
		} else {
			parts = append(parts, fmt.Sprintf("%s ✗ (%s)", s.name, s.reason))
		}
	}
	return "live engines: " + strings.Join(parts, " · ")
}

// parseRequiredEngines splits CTXLOOM_LIVE_REQUIRE ("claude,...")
// into lowercased, trimmed, non-empty engine names. Empty/unset returns nil —
// the floor is off by default (a dev box runs whatever is available).
func parseRequiredEngines(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(raw, ",") {
		p = strings.ToLower(strings.TrimSpace(p))
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// checkRequiredEngines is THE FLOOR: if required names any engine that is not
// available in report, it returns an error naming exactly which and why —
// nil otherwise (including when required is empty, the default/unset case).
func checkRequiredEngines(report []engineStatus, required []string) error {
	if len(required) == 0 {
		return nil
	}
	byName := make(map[string]engineStatus, len(report))
	for _, s := range report {
		byName[s.name] = s
	}
	var missing []string
	for _, name := range required {
		s, ok := byName[name]
		switch {
		case !ok:
			missing = append(missing, fmt.Sprintf("%s (not a known live engine)", name))
		case !s.available:
			missing = append(missing, fmt.Sprintf("%s (%s)", name, s.reason))
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("CTXLOOM_LIVE_REQUIRE floor failed — required engine(s) unavailable: %s", strings.Join(missing, "; "))
}

// liveAgentAvailable reports whether the named real agent can be reached.
// Kept as a single-argument, bool-returning function (rather than folded
// away) because steps_j000200_setup.go's own @live scenario predates this
// registry and calls it directly with the liveAgents["claude"] entry it
// looked up itself. It delegates to the exact same engineAvailable decision
// the report and the require-list floor use, so every caller agrees on
// whether an engine will run.
func liveAgentAvailable(a liveAgent) bool {
	ok, _ := engineAvailable(a, realHomeDir, resolveOptIn())
	return ok
}

// authCheckClaude runs `claude auth status`, a local, non-interactive JSON
// status read (confirmed: no network stall observed, completes in well under
// a second), and reports whether it says loggedIn.
func authCheckClaude(realHome string) (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), authProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "claude", "auth", "status")
	cmd.Env = append(os.Environ(), "HOME="+realHome)
	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Sprintf("`claude auth status` failed: %v", err)
	}
	var status struct {
		LoggedIn bool `json:"loggedIn"`
	}
	if jerr := json.Unmarshal(out, &status); jerr != nil {
		return false, fmt.Sprintf("`claude auth status` returned unparseable output: %v", jerr)
	}
	if !status.LoggedIn {
		return false, "`claude auth status` reports not logged in"
	}
	return true, "claude auth status: logged in"
}

// credentialMapping is one env-var-to-directory pointer: setting EnvVar to Dir
// on the child process makes the engine resolve its credential material from
// Dir, the REAL host directory, rather than from the scenario's isolated HOME.
// Nothing is copied and nothing is written by ctxloom — the engine reads and
// writes the real file itself, so a provider-side rotation lands on the host
// and is simply picked up next time.
type credentialMapping struct {
	EnvVar string
	Dir    string
}

// mapCredentialHome builds the single mapping for an engine whose config-home
// env var relocates credentials, and FAILS LOUD when the real credential
// material is not there: dir must exist and be a directory, and every
// required file (relative to dir) must exist. This is the mapping-path
// equivalent of the copiers' "copied 0 files" error — a caller that mapped an
// engine at nothing must not be indistinguishable from one that mapped it
// correctly, or the run comes back mysteriously unauthenticated.
//
// It only ever calls os.Stat. It never reads a credential's contents, and
// never creates, writes, moves, chmods or removes anything.
func mapCredentialHome(engine, envVar, dir string, required ...string) ([]credentialMapping, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("map %s credentials: %s would point at %s, which is not there: %w", engine, envVar, dir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("map %s credentials: %s would point at %s, which is not a directory", engine, envVar, dir)
	}
	for _, rel := range required {
		p := filepath.Join(dir, rel)
		if _, serr := os.Stat(p); serr != nil {
			return nil, fmt.Errorf("map %s credentials: required credential file %s is missing: %w", engine, p, serr)
		}
	}
	return []credentialMapping{{EnvVar: envVar, Dir: dir}}, nil
}

// mapClaudeCredentials points CLAUDE_CONFIG_DIR at the REAL ~/.claude.
// claude's descriptor declares its home var relocates both config AND
// credentials (agent.EngineHome.Credentials) and marks .credentials.json the
// one REQUIRED source file, so that file's absence is
// the loud failure here too.
//
// COST, ACCEPTED AND STATED (erased-collar's "decide per engine and say where
// it lands"): the whole config-home is mapped, so claude's run state written
// under it during a live scenario — history/projects, and the .claude.json it
// auto-creates inside CLAUDE_CONFIG_DIR when the var is set — lands in the
// human's real ~/.claude rather than in a temp dir. That is the price of the
// engine writing a rotated token where the host can see it.
func mapClaudeCredentials(realHome string) ([]credentialMapping, error) {
	return mapCredentialHome("claude", "CLAUDE_CONFIG_DIR", filepath.Join(realHome, ".claude"), ".credentials.json")
}

// seedLiveCredentials is THE single door every @live scenario gate goes
// through to make a real engine authenticate from inside an isolated run, and
// the enforcement point for erased-collar's policy: credentials reach a live
// run exactly one of two ways — an API-key env var, or MAPPED — and never as
// a copy.
//
// setEnv is the per-scenario door through testenv's ambient-session scrub
// (TestEnvironment.SetChildEnv); it is a parameter rather than a direct call
// so this decision stays unit-testable without any acceptance fixture, and so
// a test can assert the env var that was ACTUALLY set and its value.
//
// Order, and why:
//  1. apiKeyEnvs set → nothing is copied and nothing is mapped. The key rides
//     the inherited env, and nothing rotates.
//  2. no real HOME captured → a loud error, not a silent skip. Reaching here
//     means engineAvailable already passed, and it only passes the
//     subscription path with a real HOME in hand.
//  3. mapCreds set → map, and set every returned var.
//  4. otherwise copyCreds → the legacy copy, for the engines that cannot be
//     mapped (see the copyCreds field doc).
//  5. neither → a loud error rather than an unauthenticated run.
func seedLiveCredentials(name string, a liveAgent, realHome, fakeHome string, setEnv func(key, value string)) error {
	if envSet(a.apiKeyEnvs) {
		return nil
	}
	if realHome == "" {
		return fmt.Errorf("seed %s credentials: no real HOME captured to map credentials from", name)
	}
	if a.mapCreds != nil {
		mappings, err := a.mapCreds(realHome)
		if err != nil {
			return err
		}
		if len(mappings) == 0 {
			return fmt.Errorf("seed %s credentials: mapper returned no env-var mappings", name)
		}
		for _, m := range mappings {
			setEnv(m.EnvVar, m.Dir)
		}
		return nil
	}
	if a.copyCreds != nil {
		return a.copyCreds(realHome, fakeHome)
	}
	return fmt.Errorf("seed %s credentials: engine has neither a credential mapping nor a copier configured", name)
}

// copyOneCredFile reads name from srcDir and, if present, writes it to
// dstDir (creating dstDir first) under the same name. Reports whether it
// copied anything, and the first write/mkdir error encountered (a missing
// source is never an error here — the caller decides whether "copied
// nothing at all" is fatal).
func copyOneCredFile(srcDir, dstDir, name string) (copied bool, err error) {
	data, rerr := os.ReadFile(filepath.Join(srcDir, name))
	if rerr != nil {
		return false, nil
	}
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return false, fmt.Errorf("mkdir %s: %w", dstDir, err)
	}
	if err := os.WriteFile(filepath.Join(dstDir, name), data, 0o600); err != nil {
		return false, fmt.Errorf("write %s: %w", filepath.Join(dstDir, name), err)
	}
	return true, nil
}

// copyClaudeCredentials copies just the auth-relevant files from the real
// ~/.claude into the isolated home, best effort — never the whole tree (which
// holds caches, history, and backups). Errors when it copied zero files.
func copyClaudeCredentials(realHome, fakeHome string) error {
	srcDir := filepath.Join(realHome, ".claude")
	dstDir := filepath.Join(fakeHome, ".claude")
	copiedAny := false
	for _, name := range []string{".credentials.json", "settings.json", "config.json"} {
		copied, err := copyOneCredFile(srcDir, dstDir, name)
		if err != nil {
			return fmt.Errorf("copy claude credentials: %w", err)
		}
		copiedAny = copiedAny || copied
	}
	// ~/.claude.json holds onboarding state; copying it stops the CLI from
	// dropping into an interactive first-run flow under the isolated HOME.
	copied, err := copyOneCredFile(realHome, fakeHome, ".claude.json")
	if err != nil {
		return fmt.Errorf("copy claude credentials: %w", err)
	}
	copiedAny = copiedAny || copied
	if !copiedAny {
		return fmt.Errorf("copy claude credentials: copied 0 files from %s or %s/.claude.json", srcDir, realHome)
	}
	return nil
}
