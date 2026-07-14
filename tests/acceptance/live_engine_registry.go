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
//     the BINARY to probe (not necessarily the engine's own name — kiro's is
//     kiro-cli, antigravity's is agy), how to tell INSTALLED apart from
//     AUTHENTICATED, the credential material an isolated run needs, and one
//     cheap pinned model (claude, kiro) or an explicit "no pin, use the
//     engine's own default" (antigravity has no verified cheap-model slug
//     recorded here yet — see the config comment below).
//  2. computeLiveEngineReport / formatLiveEngineReport: what actually ran vs.
//     skipped, per engine, WITH THE REASON — printed on every acceptance run
//     (TestAcceptance), not only live ones, so credential expiry shows up as
//     a loud line instead of a silently-lower pass count.
//  3. parseRequiredEngines / checkRequiredEngines: the floor.
//     CTXLOOM_LIVE_REQUIRE=claude,kiro,antigravity makes a missing/
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
)

// realHomeDir is the user's actual home, captured in TestMain (acceptance_test.go)
// before any scenario overrides HOME. Used to locate each engine's real
// credential material (~/.claude, ~/.gemini, ~/.local/share/kiro-cli) for the
// subscription-auth path, and to run each engine's own authentication probe.
var realHomeDir string

// authProbeTimeout bounds every authCheck subprocess (`claude auth status`,
// `kiro-cli whoami`). These are meant to be fast, local, non-interactive
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
	// same as the engine's own name in the Examples table — kiro's binary is
	// kiro-cli, antigravity's is agy.
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
	// copyCreds copies just the auth files from the real HOME into the
	// isolated one for the subscription path. nil for engines with no working
	// live path today (codex).
	copyCreds func(realHome, fakeHome string)
	// authCheck determines whether the engine is AUTHENTICATED — not merely
	// installed — via the subscription path. Only consulted when apiKeyEnvs
	// is unset and the CTXLOOM_ACCEPTANCE_LIVE opt-in is set (see
	// engineAvailable). Returns ok plus a short, human reason either way; the
	// reason surfaces verbatim in the availability report and in the
	// CTXLOOM_LIVE_REQUIRE floor's failure message.
	authCheck func(realHome string) (ok bool, reason string)
}

// liveAgentOrder is the availability report's fixed display order, matching
// the Examples tables' own convention (claude, antigravity, kiro) plus codex
// last as the confirmed, permanently-unavailable row. Kept separate from the
// map because map iteration order is unspecified and this report's whole
// point is to be predictable and diffable across runs.
var liveAgentOrder = []string{"claude", "antigravity", "kiro", "codex"}

// liveAgents maps the lowercased scenario token ("claude", "antigravity",
// "kiro", "codex") to its backend wiring.
var liveAgents = map[string]liveAgent{
	"claude": {
		binary:     "claude",
		apiKeyEnvs: []string{"ANTHROPIC_API_KEY"},
		credDir:    ".claude",
		config: `version: 4
llm:
  configs:
    claude:
      type: claude-code
      model: claude-haiku-4-5-20251001
  defaults:
    primary: claude
    fast: claude
profiles:
  defaults: []
`,
		copyCreds: copyClaudeCredentials,
		authCheck: authCheckClaude,
	},
	// Antigravity CLI (agy) authenticates via OAuth only — there is no
	// API-key env path — and stores its auth under ~/.gemini (shared with the
	// retired Gemini CLI's directory layout). Model pinned to "Gemini 3.5
	// Flash (Low)": verified live against a real, authenticated `agy` on
	// 2026-07-14 (`agy --model "Gemini 3.5 Flash (Low)" -p "..."` replied
	// correctly) — agy validates --model against its OWN `agy models` display
	// names verbatim (confirmed: a slug like "gemini-3.5-flash" is REJECTED
	// with "not recognized as a known model"), so this exact string, spaces
	// and parens included, is required.
	"antigravity": {
		binary:  "agy",
		credDir: ".gemini",
		config: `version: 4
llm:
  configs:
    antigravity:
      type: antigravity
      model: "Gemini 3.5 Flash (Low)"
  defaults:
    primary: antigravity
    fast: antigravity
profiles:
  defaults: []
`,
		copyCreds: copyAntigravityCredentials,
		authCheck: authCheckAntigravity,
	},
	// Kiro CLI (kiro-cli) authenticates via `kiro-cli login` (OAuth: GitHub/
	// Google/Builder ID social login) or KIRO_API_KEY for headless. Confirmed
	// live: the subscription credential is NOT under ~/.kiro at all (that tree
	// holds only agents/settings/skills/steering/sessions) — it lives in a
	// single sqlite3 database at ~/.local/share/kiro-cli/data.sqlite3 (table
	// auth_kv, key "kirocli:social:token" for social login), which the CLI
	// resolves via the standard XDG data dir. A cheap model keeps paid calls
	// inexpensive.
	"kiro": {
		binary:     "kiro-cli",
		apiKeyEnvs: []string{"KIRO_API_KEY"},
		credDir:    filepath.Join(".local", "share", "kiro-cli"),
		config: `version: 4
llm:
  configs:
    kiro:
      type: kiro
      model: qwen3-coder-next
  defaults:
    primary: kiro
    fast: kiro
profiles:
  defaults: []
`,
		copyCreds: copyKiroCredentials,
		authCheck: authCheckKiro,
	},
	// codex has NO binary on any dev host today (confirmed 2026-07-14) and its
	// settings support is implemented against docs, never actually run
	// (internal/codex/settings.go). It stays declared-but-unavailable: a real
	// row in this registry (so the availability report always names it,
	// rather than pretending only three engines exist), but with no config,
	// no credential copier, and an authCheck that only matters if a future
	// dev host ever puts a `codex` binary on PATH before this comment is
	// updated with a real probe.
	"codex": {
		binary:    "codex",
		authCheck: authCheckCodex,
	},
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
//	live engines: claude ✓ · antigravity ✓ · kiro ✓ · codex ✗ (binary not found)
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

// parseRequiredEngines splits CTXLOOM_LIVE_REQUIRE ("claude,kiro,antigravity")
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
// away) because steps_j1_setup.go's own @live scenario predates this
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

// authCheckKiro runs `kiro-cli whoami`, a local, non-interactive status read
// (confirmed: well under a second, no network stall observed) that exits
// nonzero when not logged in — a genuine authentication probe, unlike the
// file-presence heuristic authCheckAntigravity is stuck with below.
func authCheckKiro(realHome string) (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), authProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "kiro-cli", "whoami")
	cmd.Env = append(os.Environ(), "HOME="+realHome)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Sprintf("`kiro-cli whoami` failed: %v (output: %s)", err, strings.TrimSpace(string(out)))
	}
	return true, strings.TrimSpace(string(out))
}

// authCheckAntigravity checks for agy's OAuth credential file.
//
// ESCALATE (installed vs. authenticated, the exact distinction whose absence
// hid kiro's own breakage for weeks): agy's own --help-all exposes NO
// auth-status/whoami-equivalent subcommand, so unlike claude and kiro above,
// this is a FILE-PRESENCE heuristic, not a verified login check. An
// oauth_creds.json that exists but holds an expired or revoked token would
// still probe as "authenticated" here, and this probe cannot tell the
// difference. If agy ever grows a real status subcommand, replace this.
func authCheckAntigravity(realHome string) (bool, string) {
	for _, sub := range []string{".gemini", filepath.Join(".gemini", "antigravity-cli")} {
		p := filepath.Join(realHome, sub, "oauth_creds.json")
		if _, err := os.Stat(p); err == nil {
			return true, fmt.Sprintf("oauth credential file found (%s) — file-presence heuristic only, not a verified login check (agy has no auth-status subcommand)", p)
		}
	}
	return false, "no oauth_creds.json found under ~/.gemini or ~/.gemini/antigravity-cli"
}

// authCheckCodex always reports unavailable: codex has no binary on any dev
// host today, so engineAvailable never actually reaches this (the
// binary-not-found check above short-circuits first). It exists so the
// registry entry is complete and self-documenting rather than silently
// nil, and as a real answer if a future dev host ever does have a `codex`
// binary on PATH before anyone gets around to writing a genuine probe.
func authCheckCodex(string) (bool, string) {
	return false, "codex has no live authentication probe implemented (declared but unavailable)"
}

// copyClaudeCredentials copies just the auth-relevant files from the real
// ~/.claude into the isolated home, best effort — never the whole tree (which
// holds caches, history, and backups).
func copyClaudeCredentials(realHome, fakeHome string) {
	srcDir := filepath.Join(realHome, ".claude")
	dstDir := filepath.Join(fakeHome, ".claude")
	_ = os.MkdirAll(dstDir, 0o755)
	for _, name := range []string{".credentials.json", "settings.json", "config.json"} {
		data, err := os.ReadFile(filepath.Join(srcDir, name))
		if err != nil {
			continue
		}
		_ = os.WriteFile(filepath.Join(dstDir, name), data, 0o600)
	}
	// ~/.claude.json holds onboarding state; copying it stops the CLI from
	// dropping into an interactive first-run flow under the isolated HOME.
	if data, err := os.ReadFile(filepath.Join(realHome, ".claude.json")); err == nil {
		_ = os.WriteFile(filepath.Join(fakeHome, ".claude.json"), data, 0o600)
	}
}

// copyAntigravityCredentials copies just the auth-relevant files for
// Antigravity CLI (agy) into the isolated home, best effort — never the whole
// tree (which holds the brain conversation store and caches). agy keeps its
// OAuth state under ~/.gemini and ~/.gemini/antigravity-cli: oauth_creds.json
// and google_accounts.json carry the subscription login; installation_id and
// settings.json keep the CLI out of its interactive first-run flow.
func copyAntigravityCredentials(realHome, fakeHome string) {
	for _, sub := range []string{".gemini", filepath.Join(".gemini", "antigravity-cli")} {
		srcDir := filepath.Join(realHome, sub)
		dstDir := filepath.Join(fakeHome, sub)
		_ = os.MkdirAll(dstDir, 0o755)
		for _, name := range []string{"oauth_creds.json", "google_accounts.json", "settings.json", "installation_id", "user_id"} {
			data, err := os.ReadFile(filepath.Join(srcDir, name))
			if err != nil {
				continue
			}
			_ = os.WriteFile(filepath.Join(dstDir, name), data, 0o600)
		}
	}
}

// copyKiroCredentials copies the ONE file kiro-cli's subscription auth lives
// in: ~/.local/share/kiro-cli/data.sqlite3, a sqlite3 database that mixes the
// auth token (table auth_kv) with conversation/telemetry state — confirmed
// live against an authenticated `kiro-cli login`. There is no separate
// credential-only file to extract (unlike claude/antigravity's small JSON
// sidecars): the whole opaque db is the smallest unit that carries the token,
// so the isolated run inherits harmless local conversation/telemetry rows
// alongside it. Nothing under ~/.kiro (agents/settings/skills/steering/
// sessions — all project- or workspace-scoped, never auth) is touched.
func copyKiroCredentials(realHome, fakeHome string) {
	srcDir := filepath.Join(realHome, ".local", "share", "kiro-cli")
	dstDir := filepath.Join(fakeHome, ".local", "share", "kiro-cli")
	_ = os.MkdirAll(dstDir, 0o755)
	data, err := os.ReadFile(filepath.Join(srcDir, "data.sqlite3"))
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dstDir, "data.sqlite3"), data, 0o600)
}
