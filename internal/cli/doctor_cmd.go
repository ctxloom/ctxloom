package cli

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// doctorDepBinaries are the non-engine binaries ctxloom's own features rely
// on regardless of which engines are configured: ssh/ssh-keygen for signing
// (sheer-spray's prereqs).
var doctorDepBinaries = []string{"ssh", "ssh-keygen"}

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

// doctorHookSurface names each engine's own native hook-configuration file,
// relative to the project root — the same paths j5_multi_engine.feature's
// own Outline asserts hook payload lands in (steps_j5.go's j5AssertHook).
// "Looks installed" here means "the file exists", a cheap, honest,
// deterministic signal — not a proof the hook actually fires.
var doctorHookSurface = map[string]string{
	"claude-code": filepath.Join(".claude", "settings.json"),
	"codex":       filepath.Join(".codex", "config.toml"),
	"kiro":        filepath.Join(".kiro", "agents", "ctxloom.json"),
	"antigravity": filepath.Join(".agents", "hooks.json"),
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

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Run deterministic setup checks (deps, agents, hooks, trust)",
	Long: `Run ctxloom's deterministic setup checks: required binaries on PATH
(signing tools, each configured engine's own client, and a container
runtime), whether every configured agent resolves, whether hooks look
installed for the engines in use, and whether the trust store carries
signers. Each line is prefixed with a DOCTOR-CHECK-* marker — the SAME
vocabulary the "ctxloom-doctor" Agent Skill uses, so a human or an LLM
reading either surface sees one language.

Version currency has no dedicated check here (best-effort, skill-guided):
compare 'ctxloom version' against your remote's newest tag by hand, or ask
an assistant carrying the ctxloom-doctor skill to do it.

Diagnostic only: always exits 0, never blocks or changes anything.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, cfgErr := GetConfig()
		report := DoctorReport{Checks: []DoctorCheck{
			doctorCheckDeps(cfg),
			doctorCheckAgents(cmd.Context(), cfg, cfgErr),
			doctorCheckVersion(),
			doctorCheckHooksTrust(cfg, cfgErr),
		}}
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

// doctorCheckDeps probes PATH for ssh/ssh-keygen (signing), each configured
// engine's native client, and a reachable container runtime.
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
			Detail: "ssh/ssh-keygen, every configured engine's client, and a container runtime are all on PATH"}
	}
	sort.Strings(missing)
	return DoctorCheck{Marker: "DOCTOR-CHECK-DEPS-a1", Status: "warn",
		Detail: "missing: " + strings.Join(missing, ", ")}
}

// doctorCheckAgents resolves every configured agent (profile composition +
// engine/runtime) and reports the first failure, or how many resolved
// cleanly.
func doctorCheckAgents(ctx context.Context, cfg *config.Config, cfgErr error) DoctorCheck {
	if cfgErr != nil {
		return DoctorCheck{Marker: "DOCTOR-CHECK-AGENTS-b2", Status: "warn", Detail: "config did not load: " + cfgErr.Error()}
	}
	if len(cfg.Agents) == 0 {
		return DoctorCheck{Marker: "DOCTOR-CHECK-AGENTS-b2", Status: "info", Detail: "no agents configured"}
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

// doctorCheckHooksTrust reports which configured engines' own native
// hook-configuration file is present (a cheap, honest existence signal, not
// proof the hook fires) and how many signers the trust store carries
// (operations.ListSigners — always includes the embedded root, so a healthy
// store is never reported as empty).
func doctorCheckHooksTrust(cfg *config.Config, cfgErr error) DoctorCheck {
	if cfgErr != nil {
		return DoctorCheck{Marker: "DOCTOR-CHECK-HOOKS-TRUST-d4", Status: "warn", Detail: "config did not load: " + cfgErr.Error()}
	}
	var parts []string
	status := "ok"
	if len(cfg.AppPaths) > 0 {
		root := filepath.Dir(cfg.AppPaths[0])
		var present, absent []string
		for _, engine := range doctorConfiguredEngines(cfg) {
			rel, ok := doctorHookSurface[engine]
			if !ok {
				continue
			}
			if fileExists(filepath.Join(root, rel)) {
				present = append(present, engine)
			} else {
				absent = append(absent, engine)
			}
		}
		switch {
		case len(present) == 0 && len(absent) == 0:
			parts = append(parts, "hooks: no engine with a known hook surface is configured")
		case len(absent) == 0:
			parts = append(parts, "hooks: installed for "+strings.Join(present, ", "))
		default:
			parts = append(parts, "hooks: NOT installed for "+strings.Join(absent, ", ")+" (run `ctxloom manage install`)")
			status = "warn"
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
	rootCmd.AddCommand(doctorCmd)
}
