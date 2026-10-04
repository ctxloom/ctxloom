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
//     the BINARY to probe (not necessarily the engine's own name), the
//     credentials a cell that runs that binary directly may take, and one
//     cheap pinned model.
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
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// launchCredentials holds every liveAgents vendorCredEnvs value the suite was
// LAUNCHED with, captured in TestMain (acceptance_test.go) before the ambient
// scrub unsets them (testsupport.EnvKeys lists them, so a developer's exported
// credential cannot leak into a hermetic test). It is the ONLY place an @live
// cell's credential comes from: probeTokenAuth and vendorCredential read it,
// and every live path hands the value to one child's environment explicitly.
// Values are never printed.
var launchCredentials map[string]string

// captureLaunchCredentials reads the vendorCredEnvs of every registered engine
// from the current environment, keeping only the set ones.
func captureLaunchCredentials() map[string]string {
	got := map[string]string{}
	for _, a := range liveAgents {
		for _, k := range a.vendorCredEnvs {
			if v := os.Getenv(k); v != "" {
				got[k] = v
			}
		}
	}
	return got
}

// vendorCredential is the credential a cell that runs a's binary DIRECTLY
// authenticates with: the first of a.vendorCredEnvs captured at launch.
// ok=false means none was, and the cell skips. A cell that runs THROUGH
// ctxloom never asks this — its run takes only what probeTokenAuth names.
//
// It reads the CAPTURE, never the ambient environment: by the time any cell
// runs, TestMain has scrubbed the ambient copy, so an os.Getenv here would
// report "no credential" for a suite launched with one.
func vendorCredential(a liveAgent) (credentialMapping, bool) {
	for _, k := range a.vendorCredEnvs {
		if v := launchCredentials[k]; v != "" {
			return credentialMapping{EnvVar: k, Value: v}, true
		}
	}
	return credentialMapping{}, false
}

// probeAuth is the credential a through-ctxloom cell's run authenticates
// with. launch.RunAuth gives every run ctxloom spawns engine.AuthToken
// whatever the owner's own `auth:` says, and no live fixture declares one for
// the owner's session, so the token is the ONLY credential such a cell can
// use, on every axis.
// The probe hands it over the one way production takes it, on the launching
// environment: a host run receives it by value, and a container run has it
// moved out of the environment into the read-only secret mount
// (launch.Placement.SecretFiles) by isolation's containerPlacement.
type probeAuth struct {
	// Env is what the engine's Auth sets for a token-mode run: the token's
	// variable and its value. Nil when the cell cannot authenticate. Never
	// printed.
	Env map[string]string
	// Reason says why, and is printed: it names variables, never a value.
	Reason string
}

// ok reports whether the cell has a credential to run with.
func (a probeAuth) ok() bool { return len(a.Env) > 0 }

// probeTokenAuth asks backendType's own Auth capability what a run in
// engine.AuthToken mode needs, reading only the credential captured at launch
// (launchCredentials: the ambient copy is scrubbed before any cell runs).
// Asking production rather than re-typing its variable is what stops the
// probe handing a run a credential production would unset — an API key, say.
// When the shell that launches the suite lacks the token, the invocation
// takes it from `zsh -ic`; nothing here reads a file for it.
//
// MEASUREMENT SAFETY, load-bearing for every census this file takes: no
// vendor CLI runs here. A vendor's nominally read-only status command has
// been measured advancing its own credential store's mtime with the size
// unchanged; calling one inside a before/after census window would make the
// probe the source of the very host-state change a cell measures.
func probeTokenAuth(backendType string) probeAuth {
	kind, ok := engines.Registry().Lookup(engine.Name(backendType))
	if !ok {
		return probeAuth{Reason: fmt.Sprintf("unknown engine %q", backendType)}
	}
	auth, ok := kind.Home().Auth.Get()
	if !ok {
		return probeAuth{Reason: fmt.Sprintf("%s declares no auth: %s", backendType, kind.Home().Auth.AbsentReason())}
	}
	creds, err := auth.Credentials(engine.AuthToken, func(name string) (string, bool) {
		v := launchCredentials[name]
		return v, v != ""
	})
	if err != nil {
		reason := err.Error()
		var r report.Remediable
		if errors.As(err, &r) {
			reason += " — " + r.Remedy()
		}
		return probeAuth{Reason: reason}
	}
	return probeAuth{Env: creds.Env, Reason: strings.Join(slices.Sorted(maps.Keys(creds.Env)), ",") + " captured at launch"}
}

// liveVendorEnv is the ENTIRE environment for a cell that runs the claude
// binary directly (no ctxloom in between), built from nothing rather than
// filtered from the harness's: the cell must not inherit a CLAUDE_* knob, a
// ZDOTDIR or a ctxloom session variable from whoever ran the suite. Only PATH
// (to find claude, sh, git and the hook's tools), SHELL, the throwaway HOME and
// CLAUDE_CONFIG_DIR, and the one captured credential cross over.
func liveVendorEnv(cred credentialMapping, home, configDir string) []string {
	shell := "/bin/sh"
	if bash, err := exec.LookPath("bash"); err == nil {
		shell = bash
	}
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"SHELL=" + shell,
		claude.ConfigDirEnv + "=" + configDir,
		cred.EnvVar + "=" + cred.Value,
	}
}

// liveAgent describes one real backend the @live suite can drive. The same
// distillation/multi-engine scenarios run against each entry via a Scenario
// Outline, so the behavioral assertions stay backend-agnostic while auth,
// binary, and config differ.
type liveAgent struct {
	// binary is the executable actually probed on PATH. NOT necessarily the
	// same as the engine's own name in the Examples table.
	binary string
	// vendorCredEnvs are the env vars the vendor binary itself authenticates
	// from, in its preference order, captured at launch. A cell that runs the
	// binary directly takes the first one captured (vendorCredential); a cell
	// that runs through ctxloom takes only the token its engine's Auth names
	// (probeTokenAuth), which must be among these to be captured at all.
	vendorCredEnvs []string
	// credDir is the engine's config directory under HOME: the root the
	// isolation probe censuses for writes leaking out of an isolated run.
	credDir string
	// config is the ctxloom config.yaml that points primary+fast at this
	// backend, pinned to ONE CHEAP MODEL where a verified slug exists: live
	// tests prove context DELIVERY, not model quality, and a bigger model
	// proves nothing extra while costing real money on every run.
	config string
	// engine is the engine config's llm entry types: the key its agent's
	// permissions block is written under.
	engine string
}

// permissionsBlock is an agent binding's permissions block declaring mode
// for engine, indented under an agent entry.
func permissionsBlock(engine, mode string) string {
	return "    permissions:\n      " + engine + ":\n        mode: " + mode + "\n"
}

// liveAgentOrder is the availability report's fixed display order. Kept
// separate from the map because map iteration order is unspecified and this
// report's whole point is to be predictable and diffable across runs.
var liveAgentOrder = []string{"claude"}

// liveClaudeModel is the ONE cheap model every claude @live cell pins, whether
// the cell drives claude through ctxloom (the config below) or invokes the
// vendor binary directly.
const liveClaudeModel = "claude-haiku-4-5-20251001"

// liveAgents maps the lowercased scenario token ("claude") to its
// backend wiring.
var liveAgents = map[string]liveAgent{
	"claude": {
		binary:         "claude",
		vendorCredEnvs: []string{claude.OAuthTokenEnv, claude.APIKeyEnv},
		credDir:        ".claude",
		engine:         "claude-code",
		config: fmt.Sprintf("schema_version: %d\n", config.CurrentConfigVersion) + `llm:
  configs:
    claude:
      type: claude-code
      model: ` + liveClaudeModel + `
  defaults:
    primary: claude
    fast: claude
`,
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

// engineStatus is one row of the availability report: whether this engine
// will actually run in this suite, and why not when it will not.
type engineStatus struct {
	name      string
	available bool
	reason    string
}

// engineAvailable is the single decision the availability report, the
// CTXLOOM_LIVE_REQUIRE floor, and every through-ctxloom @live step's gate all
// share — they can never disagree about whether an engine will run, because
// they all call this. A run ctxloom launches authenticates with its engine's
// token and nothing else (launch.RunAuth), so the token captured at launch is
// the whole question; no engine binary is run to answer it.
func engineAvailable(a liveAgent) (bool, string) {
	if ok, reason := binaryAvailable(a); !ok {
		return false, reason
	}
	auth := probeTokenAuth(a.engine)
	return auth.ok(), auth.Reason
}

// binaryAvailable: is a's binary configured and on PATH.
func binaryAvailable(a liveAgent) (bool, string) {
	if a.binary == "" {
		return false, "no binary configured for this engine"
	}
	if _, err := exec.LookPath(a.binary); err != nil {
		return false, fmt.Sprintf("binary %q not found on PATH", a.binary)
	}
	return true, ""
}

// probeEngine wraps engineAvailable with the engine's name, for the ordered
// report below.
func probeEngine(name string, a liveAgent) engineStatus {
	ok, reason := engineAvailable(a)
	return engineStatus{name: name, available: ok, reason: reason}
}

// directEngineStatus is engineAvailable for a cell that runs a's binary
// DIRECTLY, in a throwaway HOME and config dir: it takes what the binary
// itself accepts (vendorCredential), never a login in the real home.
func directEngineStatus(name string, a liveAgent) engineStatus {
	if ok, reason := binaryAvailable(a); !ok {
		return engineStatus{name: name, reason: reason}
	}
	cred, ok := vendorCredential(a)
	if !ok {
		return engineStatus{name: name, reason: fmt.Sprintf(
			"none of %v was exported at launch — run `claude setup-token` and export %s", a.vendorCredEnvs, claude.OAuthTokenEnv)}
	}
	return engineStatus{name: name, available: true, reason: cred.EnvVar + " captured at launch"}
}

// computeLiveEngineReport probes every registered engine, in liveAgentOrder.
func computeLiveEngineReport() []engineStatus {
	report := make([]engineStatus, 0, len(liveAgentOrder))
	for _, name := range liveAgentOrder {
		a, ok := liveAgents[name]
		if !ok {
			report = append(report, engineStatus{name: name, available: false, reason: "not registered"})
			continue
		}
		report = append(report, probeEngine(name, a))
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

// credentialMapping is one env var set on the child process so the engine
// authenticates. Nothing is copied and nothing is written.
type credentialMapping struct {
	EnvVar string
	Value  string
}

// seedLiveCredentials is THE single door every through-ctxloom @live scenario
// gate goes through to make a real engine authenticate from inside its
// isolated HOME: the engine's token-mode credential, captured at launch, set
// on the child. Nothing is copied, mapped or read from the real home, and an
// API key captured beside the token is not handed over — the run would unset
// it.
//
// setEnv is the per-scenario door through testenv's ambient-session scrub
// (TestEnvironment.SetChildEnv); it is a parameter rather than a direct call
// so this decision stays unit-testable without any acceptance fixture, and so
// a test can assert the env var that was ACTUALLY set and its value.
//
// Without the token it is a loud error naming the fix, not a silent skip:
// reaching here means the gate's engineAvailable already passed.
func seedLiveCredentials(a liveAgent, setEnv func(key, value string)) error {
	auth := probeTokenAuth(a.engine)
	if !auth.ok() {
		return fmt.Errorf("seed %s credentials: %s", a.engine, auth.Reason)
	}
	for _, k := range slices.Sorted(maps.Keys(auth.Env)) {
		setEnv(k, auth.Env[k])
	}
	return nil
}
