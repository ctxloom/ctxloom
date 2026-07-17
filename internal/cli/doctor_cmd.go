package cli

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// doctorDepBinaries are the non-engine binaries ctxloom's own features rely
// on regardless of which engines are configured: ssh/ssh-keygen for signing
// (sheer-spray's prereqs), and git — worktree isolation shells `git
// worktree` (internal/lm/isolation), remote clone/pull reads/writes real
// git repos (internal/remote/repo_cache.go, internal/git/exec.go), and
// `ctxloom init`/`manage install` themselves clone the default remote. A
// machine without git silently can't do worktrees or pull content; this
// makes that a visible DOCTOR-CHECK-DEPS-a1 warn instead.
var doctorDepBinaries = []string{"ssh", "ssh-keygen", "git"}

// doctorEngineBinaries maps each registered engine backend name to the
// native CLI ctxloom would launch for it, for the DOCTOR-CHECK-DEPS-a1 PATH
// probe. Only backends with a real external client binary are listed.
var doctorEngineBinaries = map[string]string{
	"claude-code": "claude",
	"codex":       "codex",
	"kiro":        "kiro-cli",
	"antigravity": "agy",
	"opencode":    "opencode",
}

// DoctorCheck is one named check's outcome. Marker is the DOCTOR-CHECK-*
// vocabulary this command shares with the "ctxloom-doctor" Agent Skill, so a
// human or an LLM reading either surface sees one language.
type DoctorCheck struct {
	Marker string `json:"marker"`
	Status string `json:"status"` // "ok" | "warn" | "info"
	Detail string `json:"detail"`
}

// DoctorReport is `ctxloom doctor`'s structured result.
type DoctorReport struct {
	Checks []DoctorCheck `json:"checks"`
}

// doctorDepsOnlyFlag backs --deps: scopes the report to ONLY the DEPS-a1
// system-binary probe (git/ssh/ssh-keygen/container runtime/each configured
// engine's client) — the machine-level question that's true-or-false
// regardless of whether a project has been set up yet. init's PRIME and the
// setup skill's phase 1 run in THIS mode: full `doctor` on a brand-new,
// never-set-up project is a wall of expected-missing state (no agents, no
// profiles, no hooks wired) that would needlessly alarm a user at the very
// start of the setup that's about to configure those things.
var doctorDepsOnlyFlag bool

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Run deterministic setup checks (deps, agents, hooks, MCP, companions, trust)",
	Long: `Run ctxloom's deterministic setup checks — this IS the init-as-skill setup
skill's Phase 6 postcondition check (init-as-skill.plan.md §8.2): the
.ctxloom marker + config validity; required binaries on PATH (signing tools,
git, each configured engine's own client, and a container runtime); whether every
configured agent resolves (profile composition + engine/runtime) and the
roster is non-empty; the seeded dependency lockfile parses and a real context
assembly succeeds; hooks AND MCP registration per configured backend; the
trust store's signers; and companion detection + loadout probing
(taskloom/ltk/...). Each line is prefixed with a DOCTOR-CHECK-* marker — the
SAME vocabulary the "ctxloom-doctor" Agent Skill uses, so a human or an LLM
reading either surface sees one language.

Version currency has no dedicated check here (best-effort, skill-guided):
compare 'ctxloom version' against your remote's newest tag by hand, or ask
an assistant carrying the ctxloom-doctor skill to do it.

Deliberately does NOT parse any third-party ACP client config (Zed settings,
Nori's config.toml, VSCode acp-client, Toad, ...): client verification is
that config's own AGENT's re-read + live connect, never this command's job —
ctxloom stays unbound to any one frontend (init-as-skill.plan.md §6).

--deps scopes the report to ONLY the system-binary dependency probe (git,
ssh/ssh-keygen, a container runtime, and any already-configured engine's
client) — no agents/profiles/hooks/trust checks, so it reads clean on a
project that hasn't been set up yet. This is the mode init's PRIME and the
setup skill's phase 1 use, before there's anything else to check.

Diagnostic only: always exits 0, never blocks or changes anything. A "warn"
status IS this command's fail-loud signal — read the report, don't grep the
exit code.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		cfg, cfgErr := GetConfig()
		var checks []DoctorCheck
		if doctorDepsOnlyFlag {
			checks = []DoctorCheck{doctorCheckDeps(cfg)}
		} else {
			checks = []DoctorCheck{
				doctorCheckSetupMarker(cfg, cfgErr),
				doctorCheckDeps(cfg),
				doctorCheckAgents(ctx, cfg, cfgErr),
				doctorCheckVersion(),
				doctorCheckHooksTrust(ctx, cfg, cfgErr),
				doctorCheckSetupLockAndAssembly(ctx, cfg, cfgErr),
				doctorCheckSetupCompanions(cfg, cfgErr),
				doctorCheckSetupAuthPing(),
			}
		}
		report := DoctorReport{Checks: checks}
		return emit(cmd, report, func() error { return renderDoctorReport(cmd.OutOrStdout(), report) })
	},
}

// doctorConfiguredEngines returns the sorted, de-duplicated set of registered
// backend names (claude-code/codex/...) every configured agent's Engine
// label resolves to. nil cfg (config failed to load) yields none — the
// deps check then reports only the engine-independent binaries.
func doctorConfiguredEngines(cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	set := map[string]bool{}
	for _, a := range cfg.Agents {
		backend, _ := operations.ResolveBackend(cfg, a.Engine)
		if backend != "" {
			set[backend] = true
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// doctorCheckDeps probes PATH for ssh/ssh-keygen (signing), git (worktree
// isolation + remote clone/pull + init/manage install's own clone), each
// configured engine's native client, and a reachable container runtime.
func doctorCheckDeps(cfg *config.Config) DoctorCheck {
	var missing []string
	for _, bin := range doctorDepBinaries {
		if _, err := exec.LookPath(bin); err != nil {
			missing = append(missing, bin)
		}
	}
	for _, engine := range doctorConfiguredEngines(cfg) {
		bin, ok := doctorEngineBinaries[engine]
		if !ok {
			continue
		}
		if _, err := exec.LookPath(bin); err != nil {
			missing = append(missing, fmt.Sprintf("%s (%s)", bin, engine))
		}
	}
	if !(isolation.Docker{}.Available() || isolation.Podman{}.Available()) {
		missing = append(missing, "docker/podman (container runtime)")
	}
	if len(missing) == 0 {
		return DoctorCheck{Marker: "DOCTOR-CHECK-DEPS-a1", Status: "ok",
			Detail: "ssh/ssh-keygen, git, every configured engine's client, and a container runtime are all on PATH"}
	}
	sort.Strings(missing)
	return DoctorCheck{Marker: "DOCTOR-CHECK-DEPS-a1", Status: "warn",
		Detail: "missing: " + strings.Join(missing, ", ")}
}

// doctorCheckAgents resolves every configured agent (profile composition +
// engine/runtime) and reports the first failure, or how many resolved
// cleanly. An empty roster is a WARN, not a neutral fact: init-as-skill's
// setup postcondition (§8.2) requires "agents non-empty with resolvable
// profiles", and doctor IS that postcondition check now — a management-only
// project with genuinely zero agents is rare enough that silently calling it
// "info" would hide the far more common case, an interrupted/incomplete
// setup.
func doctorCheckAgents(ctx context.Context, cfg *config.Config, cfgErr error) DoctorCheck {
	if cfgErr != nil {
		return DoctorCheck{Marker: "DOCTOR-CHECK-AGENTS-b2", Status: "warn", Detail: "config did not load: " + cfgErr.Error()}
	}
	if len(cfg.Agents) == 0 {
		return DoctorCheck{Marker: "DOCTOR-CHECK-AGENTS-b2", Status: "warn",
			Detail: "no agents configured (run `/ctxloom-init` phase 5, or `ctxloom agent set <name> ...`)"}
	}
	names := make([]string, 0, len(cfg.Agents))
	for name := range cfg.Agents {
		names = append(names, name)
	}
	sort.Strings(names)
	var failed []string
	for _, name := range names {
		if _, err := operations.ResolveAgent(ctx, cfg, name, ""); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", name, err))
		}
	}
	if len(failed) == 0 {
		return DoctorCheck{Marker: "DOCTOR-CHECK-AGENTS-b2", Status: "ok",
			Detail: fmt.Sprintf("%d agent(s) resolve cleanly: %s", len(names), strings.Join(names, ", "))}
	}
	return DoctorCheck{Marker: "DOCTOR-CHECK-AGENTS-b2", Status: "warn", Detail: strings.Join(failed, "; ")}
}

// doctorCheckVersion is deliberately best-effort/skill-guided: there is no
// update-check infrastructure (internal/cli/version.go is a bare print), so
// this reports the running version and defers comparison to the
// ctxloom-doctor skill (or a human) rather than faking a currency verdict.
func doctorCheckVersion() DoctorCheck {
	return DoctorCheck{Marker: "DOCTOR-CHECK-VERSION-c3", Status: "info",
		Detail: fmt.Sprintf("running %s; comparing against the newest remote tag is best-effort/skill-guided (no --check-version yet)", Version)}
}

// doctorCheckHooksTrust cross-references doctorConfiguredEngines (every
// backend a configured agent resolves to) against operations.HarnessStatus —
// the SAME read `ctxloom manage status`/`ctxloom manage hooks status` already
// expose — reporting hooks AND MCP registration per backend (a real
// read, not a bare file-existence guess), plus how many signers the trust
// store carries (operations.ListSigners — always includes the embedded root,
// so a healthy store is never reported as empty).
func doctorCheckHooksTrust(ctx context.Context, cfg *config.Config, cfgErr error) DoctorCheck {
	if cfgErr != nil {
		return DoctorCheck{Marker: "DOCTOR-CHECK-HOOKS-TRUST-d4", Status: "warn", Detail: "config did not load: " + cfgErr.Error()}
	}
	var parts []string
	status := "ok"
	configured := doctorConfiguredEngines(cfg)
	switch {
	case len(configured) == 0:
		parts = append(parts, "hooks/MCP: no engine is configured to check")
	default:
		result, err := operations.HarnessStatus(ctx, cfg, operations.HarnessStatusRequest{})
		if err != nil {
			parts = append(parts, "hooks/MCP: "+err.Error())
			status = "warn"
		} else {
			byBackend := make(map[string]operations.BackendWiring, len(result.Backends))
			for _, b := range result.Backends {
				byBackend[b.Backend] = b
			}
			var present, missing []string
			for _, name := range configured {
				b, ok := byBackend[name]
				if !ok || !b.SettingsExists || !b.HooksPresent {
					missing = append(missing, name)
					continue
				}
				present = append(present, name)
			}
			sort.Strings(present)
			sort.Strings(missing)
			if len(missing) > 0 {
				parts = append(parts, "hooks/MCP NOT registered for: "+strings.Join(missing, ", ")+" (run `ctxloom manage install`)")
				status = "warn"
			} else {
				parts = append(parts, "hooks/MCP registered for: "+strings.Join(present, ", "))
			}
		}
	}
	signers, err := operations.ListSigners(cfg, nil)
	if err != nil {
		parts = append(parts, "trust store: "+err.Error())
	} else {
		active := 0
		for _, s := range signers {
			if !s.Suppressed {
				active++
			}
		}
		parts = append(parts, fmt.Sprintf("trust store: %d active signer(s)", active))
	}
	return DoctorCheck{Marker: "DOCTOR-CHECK-HOOKS-TRUST-d4", Status: status, Detail: strings.Join(parts, "; ")}
}

// ===== init-as-skill Phase 6 postcondition checks (plan.md §8.2) =====
//
// These compose the SAME operations/config entry points every other command
// already uses (config.Config, operations.AssembleContext, config.
// DiscoverCompanions/ProbeCompanions/ProbeCompanionLoadouts) rather than
// re-implementing any of their logic — this file's job is to CALL them and
// translate the result into a DoctorCheck, never to re-derive what "locked",
// "resolves", or "registered" means. Deliberately absent: anything that
// opens a third-party client's own config file (Zed settings.json, Nori's
// config.toml, ...) — see doctorCmd's Long text and init-as-skill.plan.md
// §6/§8.2. The remaining two §8.2 items — agents non-empty/resolvable and
// hooks/MCP registered per backend — are folded directly into
// doctorCheckAgents and doctorCheckHooksTrust above rather than duplicated
// here: doctor's OWN pre-existing checks already covered that ground, they
// just needed a stricter (WARN, not INFO) reading of the empty/missing case.

// doctorAppDir mirrors operations.getBaseDir's fallback (unexported there,
// remotes.go): the .ctxloom directory config.Load already resolved, or ""
// when it found none (callers use this to short-circuit rather than probe a
// directory that was never located).
func doctorAppDir(cfg *config.Config) string {
	if cfg != nil && len(cfg.AppPaths) > 0 {
		return cfg.AppPaths[0]
	}
	return ""
}

// doctorCheckSetupMarker verifies the .ctxloom marker directory config.Load
// already resolved (cfg.AppPaths, config.go:137) is present and the project
// config loaded without a hard error — the ground-floor precondition every
// other check in this report assumes. Read-only: it inspects config.Load's
// ALREADY-resolved record instead of re-globbing the filesystem for .ctxloom.
func doctorCheckSetupMarker(cfg *config.Config, cfgErr error) DoctorCheck {
	const marker = "DOCTOR-CHECK-SETUP-MARKER-e5"
	if cfgErr != nil {
		return DoctorCheck{Marker: marker, Status: "warn", Detail: "config did not load: " + cfgErr.Error()}
	}
	appDir := doctorAppDir(cfg)
	if appDir == "" {
		return DoctorCheck{Marker: marker, Status: "warn",
			Detail: "no .ctxloom marker directory found (run `ctxloom manage install` or `ctxloom init`)"}
	}
	return DoctorCheck{Marker: marker, Status: "ok", Detail: "marker present, config valid: " + appDir}
}

// doctorCheckSetupLockAndAssembly verifies the two "seeded deps are actually
// usable" postconditions the setup skill's phase 4/5 promise: the lockfile
// (remote.LockfileManager — the SAME reader `ctxloom sync`/`lock` use) parses
// without error, and a real context assembly (operations.AssembleContext —
// the SAME entry point `ctxloom run`'s configured-default path uses) succeeds
// end to end. AssembleContext exercises the trust gate, companion-loadout
// seeding, and fragment/profile resolution for real; none of that is
// reimplemented here.
func doctorCheckSetupLockAndAssembly(ctx context.Context, cfg *config.Config, cfgErr error) DoctorCheck {
	const marker = "DOCTOR-CHECK-SETUP-DEPS-h8"
	if cfgErr != nil {
		return DoctorCheck{Marker: marker, Status: "warn", Detail: "config did not load: " + cfgErr.Error()}
	}
	var parts []string
	status := "ok"
	if appDir := doctorAppDir(cfg); appDir == "" {
		parts = append(parts, "lockfile: no .ctxloom directory to check")
		status = "warn"
	} else {
		lf, err := remote.NewLockfileManager(appDir).Load()
		if err != nil {
			parts = append(parts, "lockfile: "+err.Error())
			status = "warn"
		} else {
			parts = append(parts, fmt.Sprintf("lockfile: %d entries parse cleanly", len(lf.AllEntries())))
		}
	}
	if _, err := operations.AssembleContext(ctx, cfg, operations.AssembleContextRequest{}); err != nil {
		parts = append(parts, "context assembly: "+err.Error())
		status = "warn"
	} else {
		parts = append(parts, "context assembly: succeeds for the configured default profile(s)")
	}
	return DoctorCheck{Marker: marker, Status: status, Detail: strings.Join(parts, "; ")}
}

// doctorCheckSetupCompanions reports companion discovery + loadout probing —
// the SAME two-stage protocol (config.DiscoverCompanions, config.
// ProbeCompanions, config.ProbeCompanionLoadouts) AssembleContext's
// SeededBundleLoader already runs internally for a real session. Reporting
// only: a project with no companions installed is not misconfigured (they
// are optional add-ons), so this is never a "warn"; it respects
// --no-companions/CTXLOOM_NO_COMPANIONS (config.CompanionsDisabled) by
// reporting that instead of executing companion binaries a second time.
func doctorCheckSetupCompanions(cfg *config.Config, cfgErr error) DoctorCheck {
	const marker = "DOCTOR-CHECK-SETUP-COMPANIONS-i9"
	if cfgErr != nil {
		return DoctorCheck{Marker: marker, Status: "warn", Detail: "config did not load: " + cfgErr.Error()}
	}
	if config.CompanionsDisabled() {
		return DoctorCheck{Marker: marker, Status: "info", Detail: "companion probing disabled (--no-companions)"}
	}
	bins := config.DiscoverCompanions()
	if len(bins) == 0 {
		return DoctorCheck{Marker: marker, Status: "info", Detail: "no companions discovered"}
	}
	var present []string
	for _, st := range config.ProbeCompanions() {
		if st.Path != "" {
			present = append(present, st.Bin)
		}
	}
	loadouts := config.ProbeCompanionLoadouts(cfg.TrustRoot())
	presentDetail := "(none on PATH)"
	if len(present) > 0 {
		presentDetail = strings.Join(present, ", ")
	}
	return DoctorCheck{Marker: marker, Status: "ok", Detail: fmt.Sprintf(
		"discovered: %s; on PATH: %s; loadout verified: %d",
		strings.Join(bins, ", "), presentDetail, len(loadouts))}
}

// doctorCheckSetupAuthPing is a placeholder. init-as-skill's USER RULING (a)
// wants a deterministic auth ping BEFORE the raw-CLI vendor TUI launches, but
// no such surface exists anywhere in this codebase yet (grepped: no
// AuthPing/auth-ping symbol) — that is a different slice's work (init-as-
// skill.plan.md §10④, init bootstrap rework), not this one's. Reported as
// "info" so the gap is VISIBLE in the postcondition report instead of
// silently missing.
func doctorCheckSetupAuthPing() DoctorCheck {
	return DoctorCheck{Marker: "DOCTOR-CHECK-SETUP-AUTHPING-j0", Status: "info",
		Detail: "no auth-ping surface exists in this build yet (deferred; verify by launching the engine's own CLI)"}
}

// renderDoctorReport writes the human-readable check list, one
// "DOCTOR-CHECK-* [status] detail" line per check, in the fixed order the
// checks were run.
func renderDoctorReport(out io.Writer, report DoctorReport) error {
	w := iox.NewErrWriter(out)
	w.Println("ctxloom doctor")
	for _, c := range report.Checks {
		w.Printf("  %s [%s] %s\n", c.Marker, c.Status, c.Detail)
	}
	return w.Err()
}

func init() {
	doctorCmd.Flags().BoolVar(&doctorDepsOnlyFlag, "deps", false,
		"check ONLY system-binary dependencies (git/ssh/ssh-keygen/container runtime/configured engines' clients) — skips agents/profiles/hooks/trust, for use before a project has been set up")
	rootCmd.AddCommand(doctorCmd)
}
