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
//     cheap pinned model (claude, kiro, codex) or an explicit "no pin, use the
//     engine's own default" (antigravity has no verified cheap-model slug
//     recorded here yet — see the config comment below). codex's authCheck
//     and credential copier are real as of 2026-07-14, but a direct
//     (non-suite) live run found its run-path context delivery broken — see
//     the codex entry's own comment; it is NOT yet a proven context-delivering
//     row.
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
	// isolated one for the subscription path, and errors when it copied
	// zero files — a caller that seeded no credentials must not be
	// indistinguishable from one that seeded correctly (U158-F07). Every
	// registered engine has a non-nil copier today, codex included.
	copyCreds func(realHome, fakeHome string) error
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
// and opencode last — codex now genuinely authenticates on a box with a real
// `codex` on PATH (confirmed 2026-07-14), so it is no longer a
// permanently-unavailable row; opencode joined 2026-07-22 (isolation-probe
// task) once its own local-credential-file authCheck existed. Both stay last
// as the newest/most-recently-wired entries. Kept separate from the map
// because map iteration order is unspecified and this report's whole point
// is to be predictable and diffable across runs.
var liveAgentOrder = []string{"claude", "antigravity", "kiro", "codex", "opencode"}

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
	// Codex CLI (codex) authenticates via `codex login` (ChatGPT subscription
	// OAuth) or an OPENAI_API_KEY/CODEX_API_KEY env var for headless
	// (internal/codex/backend.go:102's own comment names the latter; not
	// live-verified here, so both are offered as candidates rather than
	// asserted). Confirmed live 2026-07-14 on this box: `codex login status`
	// → "Logged in using ChatGPT" (real probe, see authCheckCodex).
	//
	// CODEX'S HOME IS A LANDMINE: codex resolves BOTH its config surface and
	// its ENTIRE runtime state (sessions, memories, logs, goals, model cache,
	// plugins, temp — confirmed 472MB on this box) from the single
	// $CODEX_HOME var (default ~/.codex). The credential is the ONE file
	// auth.json (confirmed: `~/.codex/auth.json`, holds auth_mode +
	// id/access/refresh tokens) — copyCodexCredentials copies only that file,
	// never the tree, the same principle copyClaudeCredentials states
	// explicitly and internal/lm/isolation/auth.go's
	// credentialSeedSpecs["codex"] now applies to a worktree-isolated run's
	// config-home (balmy-comic — a COPY, replacing the former
	// linkUserCodexAuth symlink).
	//
	// PRODUCT BUG FOUND while proving this live (2026-07-14, direct
	// `ctxloom run --print` against a real authenticated codex, NOT via this
	// suite — see the codex-live-proof session): codex's run-path composed
	// context cache file (.ctxloom/cache/context/<hash>.md, written by the
	// RawContext Setup step) carries ONLY companion-contributed fragments
	// (ltk/taskloom docs) and DROPS the active profile's own bundle
	// fragments — confirmed reproducibly: `run --dry-run` correctly shows
	// the profile's fragment in "Assembled Context", but the on-disk cache
	// file codex's SessionStart hook actually reads does not contain it, and
	// its hash is IDENTICAL across two profiles with different fragment
	// content. So a real, authenticated codex run genuinely executes (~9-12s,
	// not a skip) but currently answers questions about context it was never
	// given — bigger than taskloom tiny-ooze (materialize-only gap), since
	// this is the launch/run path. So codex reports AUTHENTICATED here (that
	// axis is real and correct), but is NOT YET a proven context-delivering
	// live row — do not add a J5 @live Examples row for codex until this is
	// fixed, or it would be red (or falsely green on a weakened assertion).
	"codex": {
		binary:     "codex",
		apiKeyEnvs: []string{"OPENAI_API_KEY", "CODEX_API_KEY"},
		credDir:    ".codex",
		config: `version: 4
llm:
  configs:
    codex:
      type: codex
      model: gpt-5.4-mini
  defaults:
    primary: codex
    fast: codex
profiles:
  defaults: []
`,
		copyCreds: copyCodexCredentials,
		authCheck: authCheckCodex,
	},
	// opencode authenticates via `opencode auth login` (interactive, any
	// configured provider) or an API key riding the environment
	// (OPENROUTER_API_KEY — OpenRouter is ctxloom's documented default
	// opencode provider, matching internal/lm/isolation/auth.go's
	// credentialSeedSpecs["opencode"].envTrigger and
	// opencodeAuthEnvVars). Its subscription-shaped credential is NOT an
	// OAuth session at all — confirmed by reading this host's own
	// ~/.local/share/opencode/auth.json (shape only, no values captured):
	// {"openrouter":{"type":"api","key":"<the same OPENROUTER_API_KEY
	// string>"}}. So unlike claude/codex's real OAuth tokens, opencode's
	// "subscription" file is just the API key wrapped in JSON — see
	// website/src/content/docs/security/isolation.md's probe section for
	// why that distinction matters for CI eligibility.
	"opencode": {
		binary:     "opencode",
		apiKeyEnvs: []string{"OPENROUTER_API_KEY"},
		credDir:    filepath.Join(".local", "share", "opencode"),
		config: `version: 4
llm:
  configs:
    opencode:
      type: opencode
      model: openrouter/openai/gpt-oss-20b:free
  defaults:
    primary: opencode
    fast: opencode
profiles:
  defaults: []
`,
		copyCreds: copyOpencodeCredentials,
		authCheck: authCheckOpencode,
	},
}

// backendTypeToLiveKey maps a REGISTERED backend type name (the config
// `llm.configs.*.type` value, and the identifier
// tests/acceptance/steps_j9_isolation_matrix.go's spy fixture and the
// isolation-probe feature both use: "claude-code"/"codex"/"kiro"/"opencode"/
// "antigravity") onto this registry's own liveAgents map key. Every name is
// identical except claude-code -> claude, a historical mismatch (the @live
// Examples tables predate the isolation matrix and used the short form).
// Extracted here — not duplicated — so both the hermetic j9 matrix's engine
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
// local-credential-file heuristic authCheckAntigravity is stuck with below
// (agy exposes no equivalent status subcommand at all).
//
// CAVEAT — this probe is NOT side-effect-free: measured 2026-07-22
// (reproduced independently), a bare `kiro-cli whoami` with no login/logout
// involved advances ~/.local/share/kiro-cli/data.sqlite3's mtime while its
// size stays unchanged. That means calling this authCheck INSIDE a
// before/after credential-store census (the isolation probe's technique,
// tests/acceptance/isolation_probe.go) would make the check itself the
// source of the host-state change a "kiro leaks" cell reports — indistinguishable
// from a real leak. The isolation probe's own availability checks
// deliberately never shell out to this function for that reason (file
// presence and environment variables only); if a genuinely read-only kiro
// auth check is ever needed inside a measurement window, this one is not it.
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

// authCheckAntigravity inspects agy's file-based OAuth token file CONTENT
// (token.access_token / token.refresh_token / token.expiry), not merely the
// file's presence.
//
// WRONG FILE, CORRECTED (fatal-amino, 2026-07-22): this used to read
// ~/.gemini/oauth_creds.json — a guess that turned out to be leftover state
// from the retired standalone Gemini CLI (same shared ~/.gemini directory
// layout), not an antigravity credential at all. The live-investigated,
// CONFIRMED file is ~/.gemini/antigravity-cli/antigravity-oauth-token — the
// same path internal/lm/isolation/auth.go's resolveAntigravityContainerAuth
// now seeds — with the shape {"auth_method": "...", "token": {"access_token",
// "refresh_token", "token_type", "expiry"}} (expiry an RFC3339 STRING, unlike
// oauth_creds.json's unix-millis expiry_date INT — a second reason the two
// were never interchangeable).
//
// ESCALATE, STILL (installed vs. authenticated, the exact distinction whose
// absence hid kiro's own breakage for weeks): confirmed 2026-07-15, agy 1.1.2
// exposes NO auth-status/whoami-equivalent subcommand — `agy auth`, `agy
// whoami`, `agy status`, `agy login`, `agy account`, `agy session` and `agy
// config` all silently fall through to the top-level `--help` text (none is
// a real subcommand registered with the CLI), and the two subcommands that
// DO exist and looked promising, `agy models` and `agy agent`, are static/
// local: verified they print the identical list under a fresh, empty HOME
// with no credential file at all as under the real authenticated HOME, so
// neither carries any auth signal. There is no local, free, non-interactive
// command surface to probe, unlike claude/kiro/codex above.
//
// So this remains a LOCAL CREDENTIAL FILE heuristic, not a verified login
// check — but it now parses the file (matching authCheckClaude's JSON-field
// style) instead of just calling os.Stat: a refresh_token present, or an
// access_token whose expiry has not passed, is treated as evidence of a live
// credential; an expired access_token with no refresh_token, or a file that
// fails to parse, is treated as evidence against one. This is strictly more
// accurate than plain presence for the "stale/revoked token still on disk"
// case a pure file-presence check cannot distinguish.
//
// KNOWN, MEASURED LIMITATION THIS CANNOT FIX (2026-07-14 investigation, task
// vast-rut, predating the file-name correction above but unaffected by it):
// this file's existence is neither necessary nor sufficient evidence of the
// TRUE authentication condition on a HOST run. A FRESH HOME containing no
// file-based token anywhere still let a real `agy -p` call authenticate and
// correctly answer a prompt on a real host — with no API-key env var set and
// the D-Bus/keyring session severed by env alone (the socket itself is
// UID-addressed and does not need the env var — see
// internal/lm/isolation/curatedhome.go's package doc). That is expected and
// does not contradict this file's role: this token file is agy's fallback
// for when NO keyring is reachable AT ALL (a real container, not merely an
// env var cleared on a host), which is exactly the case the container axis's
// probeContainerAuthAvailable / resolveAntigravityContainerAuth exercise. A
// "false" from this function does NOT reliably mean agy is unauthenticated
// on a HOST run (the keyring may still cover it there); a "true" is the
// best available local, free, non-interactive signal for the FILE-BASED
// fallback specifically. Deliberately NOT "fixed" by shelling out to a real
// `agy -p <prompt>` call instead: that would make this probe a paid,
// possibly network-stalling model call on every opted-in acceptance run,
// breaking the same "never a paid model call" contract authProbeTimeout's
// doc comment states for every authCheck* in this file. If agy ever grows a
// real status subcommand, replace this outright.
func authCheckAntigravity(realHome string) (bool, string) {
	type fileToken struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		Expiry       string `json:"expiry"` // RFC3339; empty if absent
	}
	type tokenFile struct {
		AuthMethod string    `json:"auth_method"`
		Token      fileToken `json:"token"`
	}
	const limitation = "local credential-file heuristic only, not a verified login check (agy has no auth-status subcommand) — see authCheckAntigravity doc comment for its known limits"
	p := filepath.Join(realHome, ".gemini", "antigravity-cli", "antigravity-oauth-token")
	data, err := os.ReadFile(p)
	if err != nil {
		return false, fmt.Sprintf("no %s found — NOTE: this is not reliable evidence of unauthenticated on a HOST run (the OS keyring may still cover it there); see authCheckAntigravity doc comment", p)
	}
	var tf tokenFile
	if jerr := json.Unmarshal(data, &tf); jerr != nil {
		return false, fmt.Sprintf("%s exists but is not valid JSON: %v", p, jerr)
	}
	if tf.Token.RefreshToken != "" {
		// A refresh token lets the client mint a fresh access token past
		// expiry, so its presence is the stronger liveness signal — avoids
		// flip-flopping this probe false purely because a short-lived access
		// token aged out between two runs.
		return true, fmt.Sprintf("%s: refresh_token present (%s)", p, limitation)
	}
	if tf.Token.AccessToken == "" {
		return false, fmt.Sprintf("%s exists but has no access_token or refresh_token", p)
	}
	if tf.Token.Expiry != "" {
		expiry, perr := time.Parse(time.RFC3339, tf.Token.Expiry)
		if perr != nil {
			return true, fmt.Sprintf("%s: access_token present, expiry %q unparseable as RFC3339 (%s)", p, tf.Token.Expiry, limitation)
		}
		if time.Now().After(expiry) {
			return false, fmt.Sprintf("%s: access_token expired %s ago, no refresh_token", p, time.Since(expiry).Round(time.Second))
		}
		return true, fmt.Sprintf("%s: access_token valid until %s (%s)", p, expiry.Format(time.RFC3339), limitation)
	}
	// access_token present with no expiry field to check against.
	return true, fmt.Sprintf("%s: access_token present, no expiry field to verify (%s)", p, limitation)
}

// authCheckCodex runs `codex login status`, a local, non-interactive status
// read confirmed live 2026-07-14 (~0.1s, no network stall observed): exit 0
// with "Logged in using ChatGPT" on the subscription path, exit 1 with "Not
// logged in" otherwise — a genuine authenticated/not-authenticated probe, the
// same INSTALLED-vs-AUTHENTICATED distinction authCheckClaude/authCheckKiro
// make (and whose absence hid kiro's own breakage for months), replacing the
// old hardcoded "codex has no live authentication probe implemented" stub
// that reported unavailable regardless of reality.
func authCheckCodex(realHome string) (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), authProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "codex", "login", "status")
	cmd.Env = append(os.Environ(), "HOME="+realHome)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		return false, fmt.Sprintf("`codex login status` reports not logged in: %s", text)
	}
	return true, fmt.Sprintf("codex login status: %s", text)
}

// authCheckOpencode is a LOCAL CREDENTIAL FILE heuristic, the same shape as
// authCheckAntigravity: opencode 1.18.1 exposes no auth-status/whoami
// subcommand of its own (`opencode auth list` prints configured providers,
// not a login/expiry verdict), so this parses
// ~/.local/share/opencode/auth.json (matching auth.go's
// credentialSeedSpecs["opencode"] destination) and treats a non-empty
// top-level object as evidence of a configured provider credential. Unlike
// claude/codex's real OAuth tokens, this file typically holds a plain API
// key (see the liveAgents["opencode"] entry's own doc) — so this is
// "some provider is configured", not "a subscription is logged in"; there is
// no expiry to check. NEVER logs the file's contents.
func authCheckOpencode(realHome string) (bool, string) {
	p := filepath.Join(realHome, ".local", "share", "opencode", "auth.json")
	data, err := os.ReadFile(p)
	if err != nil {
		return false, fmt.Sprintf("%s not found or unreadable: %v — NOTE: local credential-file heuristic only, opencode has no auth-status subcommand", p, err)
	}
	var providers map[string]json.RawMessage
	if jerr := json.Unmarshal(data, &providers); jerr != nil {
		return false, fmt.Sprintf("%s exists but is not a valid JSON object: %v", p, jerr)
	}
	if len(providers) == 0 {
		return false, fmt.Sprintf("%s exists but configures no provider", p)
	}
	names := make([]string, 0, len(providers))
	for k := range providers {
		names = append(names, k)
	}
	return true, fmt.Sprintf("%s: provider(s) configured: %s (local credential-file heuristic only, not a verified login check)", p, strings.Join(names, ","))
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

// copyOpencodeCredentials copies opencode's auth material into the isolated
// home: auth.json (the credential that matters) plus mcp-auth.json when
// present (per-MCP-server OAuth tokens, optional — internal/lm/isolation/
// auth.go's credentialSeedSpecs["opencode"] treats it the same way). Both
// live under ~/.local/share/opencode, matching opencode's own XDG_DATA_HOME
// resolution (live-verified against opencode 1.18.1, same auth.go doc).
// Errors when it copied zero files: a caller that seeded no credentials must
// not be indistinguishable from one that seeded correctly (U158-F07).
func copyOpencodeCredentials(realHome, fakeHome string) error {
	srcDir := filepath.Join(realHome, ".local", "share", "opencode")
	dstDir := filepath.Join(fakeHome, ".local", "share", "opencode")
	copiedAny := false
	for _, name := range []string{"auth.json", "mcp-auth.json"} {
		copied, err := copyOneCredFile(srcDir, dstDir, name)
		if err != nil {
			return fmt.Errorf("copy opencode credentials: %w", err)
		}
		copiedAny = copiedAny || copied
	}
	if !copiedAny {
		return fmt.Errorf("copy opencode credentials: copied 0 files from %s (checked auth.json, mcp-auth.json)", srcDir)
	}
	return nil
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

// copyAntigravityCredentials copies just the auth-relevant files for
// Antigravity CLI (agy) into the isolated home, best effort — never the whole
// tree (which holds the brain conversation store and caches). agy keeps its
// file-based OAuth fallback token at
// ~/.gemini/antigravity-cli/antigravity-oauth-token — CONFIRMED 2026-07-22
// (fatal-amino) as the real credential agy's own file-based fallback reads/
// writes when no OS keyring is reachable; google_accounts.json and
// installation_id/settings.json keep the CLI out of its interactive
// first-run flow. oauth_creds.json is DELIBERATELY not copied here: it was an
// earlier wrong-file guess (corrected auth.go's resolveAntigravityContainerAuth
// doc) — on this project's own dev host it turned out to be leftover state
// from the retired standalone Gemini CLI (same shared ~/.gemini directory
// layout), not an antigravity credential at all, and copying it would make
// this probe's auth-path decision (probeDecideAuthPath) report a false
// "seeded" path off a file production's resolver never reads.
func copyAntigravityCredentials(realHome, fakeHome string) error {
	copiedAny := false
	for _, sub := range []string{".gemini", filepath.Join(".gemini", "antigravity-cli")} {
		srcDir := filepath.Join(realHome, sub)
		dstDir := filepath.Join(fakeHome, sub)
		for _, name := range []string{"antigravity-oauth-token", "google_accounts.json", "settings.json", "installation_id", "user_id"} {
			copied, err := copyOneCredFile(srcDir, dstDir, name)
			if err != nil {
				return fmt.Errorf("copy antigravity credentials: %w", err)
			}
			copiedAny = copiedAny || copied
		}
	}
	if !copiedAny {
		return fmt.Errorf("copy antigravity credentials: copied 0 files under %s/.gemini", realHome)
	}
	return nil
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
func copyKiroCredentials(realHome, fakeHome string) error {
	srcDir := filepath.Join(realHome, ".local", "share", "kiro-cli")
	dstDir := filepath.Join(fakeHome, ".local", "share", "kiro-cli")
	copied, err := copyOneCredFile(srcDir, dstDir, "data.sqlite3")
	if err != nil {
		return fmt.Errorf("copy kiro credentials: %w", err)
	}
	if !copied {
		return fmt.Errorf("copy kiro credentials: copied 0 files from %s", srcDir)
	}
	return nil
}

// copyCodexCredentials copies the ONE file codex's subscription auth lives
// in: ~/.codex/auth.json (auth_mode + id/access/refresh tokens; confirmed
// live against a real `codex login status` → "Logged in using ChatGPT" on
// 2026-07-14) — never the rest of ~/.codex, which on this box holds 472MB of
// sessions, memories, logs, goals, a model-list cache, and plugins, all
// mixed with config under the SAME $CODEX_HOME codex uses for its credential
// lookup (confirmed: pointing CODEX_HOME at a project dir once made codex
// dump 91MB of sqlite/temp state there). This is the identical "never the
// whole tree" principle copyClaudeCredentials states explicitly, and the
// identical file internal/lm/isolation/auth.go's credentialSeedSpecs["codex"]
// COPIES (balmy-comic — replacing the former linkUserCodexAuth symlink) into
// a worktree-isolated run's config-home — applied here to the isolated test
// HOME instead. codexHome() (internal/codex/commandfiles.go) resolves
// $CODEX_HOME if set, else $HOME/.codex, so once the acceptance harness
// overrides HOME to the isolated fakeHome, writing auth.json under
// fakeHome/.codex is exactly where codex (and ctxloom's own credential-seed
// copy, when a real isolated run follows) will find it.
func copyCodexCredentials(realHome, fakeHome string) error {
	srcDir := filepath.Join(realHome, ".codex")
	dstDir := filepath.Join(fakeHome, ".codex")
	copied, err := copyOneCredFile(srcDir, dstDir, "auth.json")
	if err != nil {
		return fmt.Errorf("copy codex credentials: %w", err)
	}
	if !copied {
		return fmt.Errorf("copy codex credentials: copied 0 files from %s", srcDir)
	}
	return nil
}
