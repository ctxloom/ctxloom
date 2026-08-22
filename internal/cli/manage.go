package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/gitignore"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/projectroot"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// manageCmd is the home for everything that mutates the project harness:
// scaffolding (.ctxloom), hooks/statusline, MCP registration, command files,
// .gitignore, and config. Runtime entrypoints (`mcp serve`) and machine
// callbacks (all consolidated under the hidden `hook` namespace) deliberately
// stay out of this user-facing namespace.
var manageCmd = groupNode(&cobra.Command{
	Use:   "manage",
	Short: "Install and manage ctxloom's project harness",
	Long: `Install, inspect, and remove ctxloom's integration with a project.

Everything that writes to the project harness lives here: the .ctxloom
directory, backend hooks and statusline, MCP server registration, generated
command files, .gitignore, and configuration.

  ctxloom manage install      Scaffold and wire ctxloom into this project
  ctxloom manage uninstall    Remove ctxloom's hooks, MCP entry, and commands
  ctxloom manage check        Show what ctxloom has wired in
  ctxloom manage hooks        Install/uninstall/inspect backend hooks
  ctxloom manage gitignore    Maintain ctxloom's .gitignore entries

MCP registration lives at the top-level 'ctxloom mcp register'/'unregister';
configuration lives at the top-level 'ctxloom config'; the duplicate
'manage init' setup entry point was removed, root 'ctxloom init' is the sole
bootstrap.`,
})

// --- manage install / uninstall / status -----------------------------------

var (
	manageInstallEngine string
	manageInstallPrint  bool
)

var manageInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Scaffold .ctxloom and wire hooks, MCP, gitignore, and config",
	Long: `One-shot, non-interactive setup: scaffold the .ctxloom skeleton (if absent),
exclude ctxloom's private state from git, and apply hooks/MCP/statusline to
every supported backend. Unlike root 'ctxloom init', it never prompts and never
launches an AI, so it is the form to use from a script or CI.`,
	Args: cobra.NoArgs,
	RunE: runManageInstall,
}

var manageUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Remove ctxloom's hooks, statusline, MCP entry, and command files",
	Long: `Strip ctxloom-managed hooks, statusline, MCP servers, and generated command
files from every supported backend. Leaves the .ctxloom directory and its
contents (profiles, bundles, config) untouched.`,
	Args: cobra.NoArgs,
	RunE: runManageUninstall,
}

var manageCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Show what ctxloom has wired into this project",
	Args:  cobra.NoArgs,
	RunE:  runManageCheck,
}

// runManageInstall scaffolds and wires ctxloom into the current project. Fault
// tolerant with respect to per-backend hook errors (collected into
// result.Errors and warned, not fatal) so a partial wire still lands the user
// in a working state; NOT fault tolerant with respect to the .gitignore write,
// which now fails loud.
func runManageInstall(cmd *cobra.Command, _ []string) error {
	appDir, err := resolveAppDir(false)
	if err != nil {
		return err
	}
	projectDir := filepath.Dir(appDir)

	engineRequested := cmd.Flags().Changed("engine")
	if err := checkEngineKnown(engineRequested, manageInstallEngine); err != nil {
		return err
	}
	if err := checkInstallEngineApplies(ctxloomDirExists(appDir), engineRequested, manageInstallEngine); err != nil {
		return err
	}

	if manageInstallPrint {
		type manageInstallPlanResult struct {
			CtxloomDir       string `json:"ctxloom_dir"`
			CtxloomDirExists bool   `json:"ctxloom_dir_exists"`
			Engine           string `json:"engine,omitempty"`
			Gitignore        string `json:"gitignore"`
		}
		exists := ctxloomDirExists(appDir)
		plan := manageInstallPlanResult{
			CtxloomDir:       appDir,
			CtxloomDirExists: exists,
			Gitignore:        filepath.Join(projectDir, ".gitignore"),
		}
		if !exists {
			plan.Engine = manageInstallEngine
		}
		return emit(cmd, plan, func() error {
			printInstallPlan(appDir, projectDir)
			return nil
		})
	}

	initialized := false
	if !ctxloomDirExists(appDir) {
		if _, err := operations.InitializeProject(cmd.Context(), operations.InitializeProjectRequest{
			AppDir: appDir,
			Engine: manageInstallEngine,
		}); err != nil {
			return err
		}
		initialized = true
	}

	if _, err := ensureHarnessGitignore(projectDir); err != nil {
		return err
	}

	// ApplyHooks reloads config from disk itself, so this load is
	// not an input to it — it is the early guard + config-warning echo the
	// command owes the user before doing any work.
	if _, err := GetConfig(); err != nil {
		return err
	}
	// An EXPLICIT --engine scopes the hook apply to that one backend — the flag
	// reads like "install for this engine" and used to wire all five
	// regardless (`.opencode/`, `.kiro/`, etc. materializing in a project that
	// uses only one engine). Omitting the flag keeps the prior "all" default:
	// manageInstallEngine always holds a value (its flag default is
	// "claude-code"), so Changed is the only reliable signal that the user
	// actually asked for one engine — see checkInstallEngineApplies above,
	// which gates on the same Changed() check for the same reason.
	hookBackend := "all"
	if cmd.Flags().Changed("engine") {
		hookBackend = manageInstallEngine
	}
	result, err := operations.ApplyHooks(cmd.Context(), operations.ApplyHooksRequest{
		Backend:           hookBackend,
		RegenerateContext: true,
	})
	if err != nil {
		return err
	}
	for _, e := range result.Errors {
		clidiag.Warn("ctxloom", "%s", e)
	}

	type manageInstallResult struct {
		AppDir      string   `json:"app_dir"`
		Initialized bool     `json:"initialized"`
		Gitignore   string   `json:"gitignore"`
		Status      string   `json:"status"`
		Backends    []string `json:"backends"`
		Errors      []string `json:"errors,omitempty"`
	}
	out := manageInstallResult{
		AppDir:      appDir,
		Initialized: initialized,
		Gitignore:   filepath.Join(projectDir, ".gitignore"),
		Status:      result.Status,
		Backends:    result.Backends,
		Errors:      result.Errors,
	}
	return emit(cmd, out, func() error {
		if initialized {
			fmt.Fprintf(cmd.OutOrStdout(), "Initialized ctxloom directory: %s\n", appDir)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Hooks %s for: %v\n", result.Status, result.Backends)
		return nil
	})
}

// checkEngineKnown rejects an explicitly-passed `--engine` that names no
// backend this ctxloom binary knows, BEFORE checkInstallEngineApplies gets a
// chance to refuse for the unrelated reason that .ctxloom already exists. A
// typo'd engine name is the single most likely mistake on migration day, and
// the diagnosis has to be about the ARGUMENT — naming it and listing the
// engines that exist — not about directory state, or a user who mistyped an
// unfamiliar engine name learns nothing about how to fix it.
//
// The roster is backends.List(), not operations.AvailableLLMNames: this flag
// ends up as the TYPE of a real `{type: engine}` LM config entry
// (operations.engineRegistry/fallbackRegistry) when InitializeProject scaffolds
// — the same set InitializeProject itself already enforces via
// backends.Exists — not a resolved LABEL like `agent edit --engine`'s (whose
// membership set, AvailableLLMNames, additionally includes labels a project's
// own config declares, because that command binds to a label the config
// resolves, not a backend type). Checking a project's declared labels here
// would accept names this command cannot actually act on.
//
// It needs no project config to run: the engine roster is a property of the
// BINARY, not of a project, so — unlike checkInstallEngineApplies, which can
// only ask whether .ctxloom exists — this check applies identically whether
// or not .ctxloom exists yet, and runs first for that reason.
func checkEngineKnown(engineRequested bool, engine string) error {
	if !engineRequested || backends.Exists(engine) {
		return nil
	}
	return fmt.Errorf("--engine %q: unknown engine; ctxloom knows: %s", engine, strings.Join(backends.List(), ", "))
}

// checkInstallEngineApplies rejects a `manage install --engine <x>` whose
// engine choice cannot reach anything: the engine is recorded by
// InitializeProject while it scaffolds, so on a project that already has a
// .ctxloom the flag has no effect at all. Re-running install to re-apply hooks
// stays supported — only an EXPLICITLY passed --engine is refused, and only
// when there is nothing left to scaffold. checkEngineKnown runs before this,
// so by the time this fires the engine name itself is already known-good and
// the refusal really is about directory state.
//
// The refusal names where the engine actually lives, because the flag reads like
// the way to change it and silently was not.
func checkInstallEngineApplies(dirExists, engineRequested bool, engine string) error {
	if !dirExists || !engineRequested {
		return nil
	}
	return fmt.Errorf("--engine %s cannot be applied: .ctxloom already exists, and the engine is only recorded while scaffolding it; change the default engine with `ctxloom llm default <name>` (or remove .ctxloom to re-scaffold)", engine)
}

// printInstallPlan lists the steps `manage install` would take without running them.
func printInstallPlan(appDir, projectDir string) {
	fmt.Println("manage install would:")
	if ctxloomDirExists(appDir) {
		fmt.Printf("  - reuse existing .ctxloom: %s\n", appDir)
	} else {
		fmt.Printf("  - scaffold .ctxloom: %s (engine: %s)\n", appDir, manageInstallEngine)
	}
	fmt.Printf("  - update .gitignore: %s\n", filepath.Join(projectDir, ".gitignore"))
	fmt.Println("  - apply hooks, statusline, and MCP registration for all backends")
}

// gitignoreOutcome is what ensureHarnessGitignore actually did to the file:
// the rules it removed and the patterns it appended.
//
// It is measured from the file's BYTES before and after the call, not from what
// the write path decided to do. That distinction is the point. Both halves of
// this command's history are cases of a report that described an intention
// rather than a file: it once printed "Updated <path>" when the write had
// FAILED, and it went on printing it when the write had changed NOTHING —
// including for every project whose blanket `.ctxloom/*` an older binary did
// not recognise, which is how a migration that never ran came to look identical
// to one that succeeded. A summary derived from the bytes cannot drift from
// them.
type gitignoreOutcome struct {
	Retired []string
	Added   []string
}

// changed reports whether the file's rule lines differ from what they were.
func (o gitignoreOutcome) changed() bool { return len(o.Retired) > 0 || len(o.Added) > 0 }

// status is the machine-readable verdict: a caller parsing --format json must be
// able to tell a no-op from a write without diffing the file itself.
func (o gitignoreOutcome) status() string {
	if !o.changed() {
		return "unchanged"
	}
	return "updated"
}

// summary is the human line. It leads with the verb for what happened rather
// than a fixed "Updated", and names the retired rules — a retirement is a
// removal from a user-authored file, so the user is owed the list.
func (o gitignoreOutcome) summary(path string) string {
	retired := fmt.Sprintf("Retired %d superseded %s (%s)",
		len(o.Retired), plural(len(o.Retired), "rule", "rules"), strings.Join(o.Retired, ", "))
	added := fmt.Sprintf("%d %s", len(o.Added), plural(len(o.Added), "pattern", "patterns"))
	switch {
	case len(o.Retired) > 0 && len(o.Added) > 0:
		return fmt.Sprintf("%s and added %s in %s", retired, added, path)
	case len(o.Retired) > 0:
		return fmt.Sprintf("%s in %s", retired, path)
	case len(o.Added) > 0:
		return fmt.Sprintf("Added %s to %s", added, path)
	default:
		return fmt.Sprintf("No change: %s already carries ctxloom's ignore rules", path)
	}
}

// ensureHarnessGitignore excludes ctxloom's private state and transient
// artifacts from git, reporting what it actually did. Returns the write failure
// instead of swallowing it — callers must not report success when the file was
// never updated (`manage gitignore install` used to print "Updated <path>" and
// exit 0 even when the write failed).
func ensureHarnessGitignore(projectDir string) (gitignoreOutcome, error) {
	path := filepath.Join(projectDir, ".gitignore")
	// A pre-read failure is not reported here: an absent .gitignore is the
	// normal first-run case, and any other read problem is the same one
	// gitignore.Ensure is about to hit and report with its own context.
	before, _ := os.ReadFile(path)

	patterns := append(append([]string{}, gitignore.PrivateStatePatterns...), gitignore.TransientArtifactPatterns...)
	if err := gitignore.Ensure(projectDir, gitignore.Comment, patterns...); err != nil {
		return gitignoreOutcome{}, fmt.Errorf("failed to update .gitignore: %w", err)
	}

	// Ensure returned nil, so the file it was asked to maintain must be
	// readable. Failing to read it back is not something to soften into "no
	// change" — that is exactly the claim this function exists to stop being
	// made without evidence.
	after, err := os.ReadFile(path)
	if err != nil {
		return gitignoreOutcome{}, fmt.Errorf("failed to read back %s after updating it: %w", path, err)
	}

	retired, added := ignoreLineDiff(before, after)
	return gitignoreOutcome{Retired: retired, Added: added}, nil
}

// ignoreLineDiff reports the rule lines dropped from before and the ones added
// in after, as multisets so a duplicated line is not silently absorbed.
//
// Comments and blank lines are excluded: only rules affect what git tracks, and
// counting the block header ctxloom appends as an "added pattern" would make
// the count the user is shown wrong by one.
func ignoreLineDiff(before, after []byte) (retired, added []string) {
	beforeLines, afterLines := ignoreRuleLines(before), ignoreRuleLines(after)
	return missingFrom(beforeLines, afterLines), missingFrom(afterLines, beforeLines)
}

// missingFrom returns the members of want that other does not cover, honouring
// multiplicity and preserving want's order.
func missingFrom(want, other []string) []string {
	remaining := make(map[string]int, len(other))
	for _, line := range other {
		remaining[line]++
	}
	var out []string
	for _, line := range want {
		if remaining[line] > 0 {
			remaining[line]--
			continue
		}
		out = append(out, line)
	}
	return out
}

// ignoreRuleLines returns content's trimmed, non-empty, non-comment lines.
func ignoreRuleLines(content []byte) []string {
	var out []string
	for line := range strings.SplitSeq(string(content), "\n") {
		s := strings.TrimSpace(line)
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		out = append(out, s)
	}
	return out
}

func runManageUninstall(cmd *cobra.Command, _ []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return err
	}
	result, err := operations.RemoveHooks(cmd.Context(), cfg, operations.RemoveHooksRequest{Backend: "all"})
	if err != nil {
		return err
	}
	for _, e := range result.Errors {
		clidiag.Warn("ctxloom", "%s", e)
	}

	type manageUninstallResult struct {
		Status   string   `json:"status"`
		Backends []string `json:"backends"`
		Errors   []string `json:"errors,omitempty"`
		Note     string   `json:"note"`
	}
	out := manageUninstallResult{
		Status:   result.Status,
		Backends: result.Backends,
		Errors:   result.Errors,
		Note:     "The .ctxloom directory and its contents were left in place.",
	}
	return emit(cmd, out, func() error {
		fmt.Fprintf(cmd.OutOrStdout(), "Removed ctxloom harness from: %v\n", result.Backends)
		fmt.Fprintln(cmd.OutOrStdout(), out.Note)
		return nil
	})
}

func runManageCheck(cmd *cobra.Command, _ []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return err
	}
	result, err := operations.HarnessStatus(cmd.Context(), cfg, operations.HarnessStatusRequest{})
	if err != nil {
		return err
	}
	return emit(cmd, result, func() error {
		printHarnessStatus(cmd.OutOrStdout(), result, capabilityLossByAgent(cmd.Context(), cfg))
		return nil
	})
}

// printHarnessStatus renders the wiring report. losses is the CAPABILITY half
// of it (capabilityLossByAgent): what this project's configured agents ask for
// that their engines have no structural place for. It belongs in this report
// rather than in a separate command for the same reason materialize interleaves
// its "NOT carried" lines with its "wrote" lines — every wiring line here is
// true, and a reader who sees only what ctxloom DID wire must not come away
// with that as the whole story.
func printHarnessStatus(w io.Writer, r *operations.HarnessStatusResult, losses []agentCapabilityLoss) {
	fmt.Fprintf(w, "Project: %s\n", r.WorkDir)
	fmt.Fprintf(w, "Statusline (HUD): %v\n\n", r.ManageStatusline)
	for _, b := range r.Backends {
		if !b.SettingsExists {
			fmt.Fprintf(w, "  %s: not configured\n", b.Backend)
			continue
		}
		fmt.Fprintf(w, "  %s: hooks=%v statusline=%v mcp=%v\n", b.Backend, b.HooksPresent, b.StatusLine, b.MCPPresent)
	}
	printSurfaceCurrencies(w, r.Surfaces)
	renderCapabilityLosses(w, losses)
	fmt.Fprintln(w)
	printCompanionStatus(w)
}

// printSurfaceCurrencies renders the DELIVERY half of the wiring report — see
// operations.SurfaceCurrency's doc: whether a materialized native context
// file's content still matches what the project's default profiles currently
// compose. Prints nothing at all when Surfaces is empty (no backend both
// offers a read half and has anything materialized here), matching the "no
// false alarms" rule the field itself already applies — a bare, unlabeled
// section would be worse than no section.
func printSurfaceCurrencies(w io.Writer, surfaces []operations.SurfaceCurrency) {
	if len(surfaces) == 0 {
		return
	}
	fmt.Fprintln(w, "\nMaterialized surfaces:")
	for _, s := range surfaces {
		if s.Detail != "" {
			fmt.Fprintf(w, "  %s (%s): %s — %s\n", s.Route, s.Backend, s.Status, s.Detail)
			continue
		}
		fmt.Fprintf(w, "  %s (%s): %s\n", s.Route, s.Backend, s.Status)
	}
}

// --- manage hooks -----------------------------------------------------------

var manageHooksCmd = groupNode(&cobra.Command{
	Use:   "hooks",
	Short: "Install, uninstall, or inspect ctxloom backend hooks",
})

var (
	manageHooksBackend string
	manageHooksForce   bool
)

var manageHooksInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Apply ctxloom hooks and regenerate context into backend config",
	Args:  cobra.NoArgs,
	RunE:  runManageHooksInstall,
}

func runManageHooksInstall(cmd *cobra.Command, _ []string) error {
	// Guard + config-warning echo only; ApplyHooks reloads from disk
	// itself.
	if _, err := GetConfig(); err != nil {
		return err
	}
	workDir := projectroot.WorkDir()
	result, err := operations.ApplyHooks(cmd.Context(), operations.ApplyHooksRequest{
		Backend:           manageHooksBackend,
		RegenerateContext: true,
		Force:             manageHooksForce,
	})
	if err != nil {
		return err
	}
	for _, e := range result.Errors {
		clidiag.Warn("ctxloom", "%s", e)
	}

	type manageHooksInstallResult struct {
		Status      string   `json:"status"`
		Backends    []string `json:"backends"`
		ProjectRoot string   `json:"project_root"`
		Errors      []string `json:"errors,omitempty"`
	}
	out := manageHooksInstallResult{
		Status:      result.Status,
		Backends:    result.Backends,
		ProjectRoot: workDir,
		Errors:      result.Errors,
	}
	return emit(cmd, out, func() error {
		fmt.Fprintf(cmd.OutOrStdout(), "Hooks %s for: %v (project root: %s)\n", result.Status, result.Backends, workDir)
		return nil
	})
}

var manageHooksUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Remove ctxloom hooks, statusline, MCP entries, and command files",
	Args:  cobra.NoArgs,
	RunE:  runManageHooksUninstall,
}

func runManageHooksUninstall(cmd *cobra.Command, _ []string) error {
	cfg, err := GetConfig()
	if err != nil {
		return err
	}
	result, err := operations.RemoveHooks(cmd.Context(), cfg, operations.RemoveHooksRequest{Backend: manageHooksBackend})
	if err != nil {
		return err
	}
	for _, e := range result.Errors {
		clidiag.Warn("ctxloom", "%s", e)
	}

	type manageHooksUninstallResult struct {
		Status   string   `json:"status"`
		Backends []string `json:"backends"`
		Errors   []string `json:"errors,omitempty"`
	}
	out := manageHooksUninstallResult{Status: result.Status, Backends: result.Backends, Errors: result.Errors}
	return emit(cmd, out, func() error {
		fmt.Fprintf(cmd.OutOrStdout(), "Hooks %s for: %v\n", result.Status, result.Backends)
		return nil
	})
}

var manageHooksCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Show which backends have ctxloom hooks wired in",
	Args:  cobra.NoArgs,
	RunE:  runManageCheck,
}

var (
	manageHooksListEvent    string
	manageHooksListProfiles []string
)

// manageHooksListCmd is `list` from the canonical verb spine (enumerate the
// noun's instances — docs/cli-surface-recommendation.md §3), on the `manage
// hooks` noun that already exists.
//
// It is a NEW LEAF rather than an extension of `manage hooks check` on purpose,
// having checked what check does: check answers "which BACKENDS have ctxloom
// wired in" — a question about installation, whose result is one row per backend
// and no hooks at all. This answers "which HOOKS run, and in what order" — one
// row per hook and no backends. Folding them together would give one command two
// unrelated answers and one output shape that suits neither, and would take
// `check`'s existing JSON contract with it.
var manageHooksListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the hooks that will fire, per event, in their resolved order",
	Long: `List the hooks that will actually fire, in the order they will fire in.

Hooks merge across every source — your config, your profiles, ctxloom's builtins,
companion tools, and each bundle your profiles reference — by pure APPEND, and a
bundle's own 'order:' sequences only its own hooks within an event. The result is
emergent: no single file states it. This is where you read it.

Each hook is reported with where it came from — your config, which profile, or
which bundle, builtin or companion — so a sequence you dislike points at one
place you can go and change.`,
	Args: cobra.NoArgs,
	RunE: runManageHooksList,
}

func runManageHooksList(cmd *cobra.Command, _ []string) error {
	if _, err := GetConfig(); err != nil {
		return err
	}
	result, err := operations.ResolveHooks(cmd.Context(), operations.ResolveHooksRequest{
		Event:    manageHooksListEvent,
		Profiles: manageHooksListProfiles,
		WorkDir:  projectroot.WorkDir(),
	})
	if err != nil {
		return err
	}
	return emit(cmd, result, func() error { return renderResolvedHooks(cmd.OutOrStdout(), result) })
}

// renderResolvedHooks writes the human form. An event with NO hooks is printed
// with "(none)" rather than skipped: silence would be indistinguishable from
// ctxloom having failed to look, and "nothing runs here" is a real answer someone
// asked for.
func renderResolvedHooks(w io.Writer, result *operations.ResolveHooksResult) error {
	for _, ev := range result.Events {
		fmt.Fprintf(w, "%s\n", ev.Event)
		if len(ev.Hooks) == 0 {
			fmt.Fprintln(w, "  (none)")
			continue
		}
		for _, h := range ev.Hooks {
			fmt.Fprintf(w, "  %d.%s %-24s [%s]\n", h.Position, resolvedHookMoved(h), resolvedHookLabel(h), resolvedHookOrigin(h))
		}
	}
	for _, bn := range result.BackendNative {
		fmt.Fprintf(w, "%s (%s native)\n", bn.Event, bn.Backend)
		for _, h := range bn.Hooks {
			fmt.Fprintf(w, "  %d.%s %-24s [%s]\n", h.Position, resolvedHookMoved(h), resolvedHookLabel(h), resolvedHookOrigin(h))
		}
	}
	return nil
}

// resolvedHookMoved marks a hook whose position was CHOSEN rather than merely
// emergent, naming where the merge had put it. Empty — not "(declared 3)" on
// every row — when nothing moved the hook, which is every row until something
// reorders: a marker on everything marks nothing.
func resolvedHookMoved(h operations.ResolvedHook) string {
	if h.Declared == 0 || h.Declared == h.Position {
		return ""
	}
	return fmt.Sprintf(" (declared %d)", h.Declared)
}

// resolvedHookLabel names a hook by what it DOES. A hook with neither a command
// nor a prompt is reported as its type rather than as blank space — a blank row
// reads as a rendering bug and hides a hook that is genuinely there.
func resolvedHookLabel(h operations.ResolvedHook) string {
	switch {
	case h.Command != "":
		return h.Command
	case h.Prompt != "":
		return h.Prompt
	case h.Type != "":
		return "(" + h.Type + ")"
	default:
		return "(hook)"
	}
}

func resolvedHookOrigin(h operations.ResolvedHook) string {
	if h.Source != "" {
		return h.SourceKind + " " + h.Source
	}
	return h.SourceKind
}

// --- manage statusline ------------------------------------------------------

var manageStatuslineCmd = groupNode(&cobra.Command{
	Use:   "statusline",
	Short: "Enable or disable ctxloom's HUD statusline",
	Long: `Control whether ctxloom installs and maintains its HUD statusline.

Disable it to keep your own (or no) statusline — ctxloom will stop managing the
statusline and clear any it previously installed. The change takes effect on the
next 'ctxloom manage hooks install' or 'ctxloom run'.`,
})

var manageStatuslineInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Let ctxloom manage the HUD statusline (default)",
	Args:  cobra.NoArgs,
	RunE:  runManageStatuslineInstall,
}

var manageStatuslineUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Stop managing the HUD statusline; keep your own",
	Args:  cobra.NoArgs,
	RunE:  runManageStatuslineUninstall,
}

func runManageStatuslineInstall(cmd *cobra.Command, _ []string) error {
	return setStatusline(cmd, true)
}

func runManageStatuslineUninstall(cmd *cobra.Command, _ []string) error {
	return setStatusline(cmd, false)
}

// setStatusline persists the statusline preference and renders the resulting
// state through emit() (text/json/yaml/toml/markdown).
func setStatusline(cmd *cobra.Command, enabled bool) error {
	if _, err := GetConfig(); err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	result, err := operations.SetStatusline(cmd.Context(), config.NewManager(), operations.SetStatuslineRequest{Enabled: enabled})
	if err != nil {
		return err
	}
	state := "disabled"
	if result.Statusline {
		state = "enabled"
	}

	type manageStatuslineResult struct {
		Status     string `json:"status"`
		Statusline bool   `json:"statusline"`
	}
	out := manageStatuslineResult{Status: state, Statusline: result.Statusline}
	return emit(cmd, out, func() error {
		fmt.Fprintf(cmd.OutOrStdout(), "ctxloom HUD statusline: %s\n", state)
		fmt.Fprintln(cmd.OutOrStdout(), "Run 'ctxloom run' or 'ctxloom manage hooks install' to apply the change.")
		return nil
	})
}

// --- manage gitignore --------------------------------------------------------

var manageGitignoreCmd = groupNode(&cobra.Command{
	Use:   "gitignore",
	Short: "Maintain ctxloom's .gitignore entries",
})

var manageGitignoreInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Add ctxloom's private-state and transient-artifact ignores",
	Args:  cobra.NoArgs,
	RunE:  runManageGitignoreInstall,
}

func runManageGitignoreInstall(cmd *cobra.Command, _ []string) error {
	appDir, err := resolveAppDir(false)
	if err != nil {
		return err
	}
	projectDir := filepath.Dir(appDir)
	outcome, err := ensureHarnessGitignore(projectDir)
	if err != nil {
		return err
	}
	path := filepath.Join(projectDir, ".gitignore")

	type manageGitignoreInstallResult struct {
		Status  string   `json:"status"`
		Path    string   `json:"path"`
		Retired []string `json:"retired,omitempty"`
		Added   []string `json:"added,omitempty"`
	}
	out := manageGitignoreInstallResult{
		Status:  outcome.status(),
		Path:    path,
		Retired: outcome.Retired,
		Added:   outcome.Added,
	}
	return emit(cmd, out, func() error {
		fmt.Fprintln(cmd.OutOrStdout(), outcome.summary(path))
		return nil
	})
}

// --- manage commit ---------------------------------------------------

// manageCommitCmd is the scriptable counterpart to `ctxloom init`'s
// dirty-tree interview question: the ONLY other place allowed to write
// paths.DirtyTreeCommitAckPath, since the record is no longer a hand-editable
// config.yaml key (config-layer-scope design doc, "Consent leaves the
// chain"). Both writers exist because the record must be settable without
// re-running init on an already-initialized project.
var manageCommitCmd = groupNode(&cobra.Command{
	Use:   "commit",
	Short: "Trust or untrust ctxloom to auto-commit a dirty tree on your behalf",
	Long: `Trust or untrust ctxloom, for THIS checkout, to auto-commit its uncommitted
changes onto its current branch when dirty_tree_handler is "commit", so a
delegated agent_run child (which only ever sees committed state in its own
worktree) can see them.

This is deliberately NOT a config.yaml key: the config chain has three
channels an agent can reach (a home file, an environment variable, an argv),
and this decision must come from a human, once, through one of exactly two
surfaces — this command, or the dirty-tree question in 'ctxloom init'.

An absent decision is untrusted, and the spawn is refused rather than
committing on your behalf.`,
})

var manageCommitTrustCmd = &cobra.Command{
	Use:   "trust",
	Short: "Trust ctxloom to auto-commit this checkout's dirty tree",
	Args:  cobra.NoArgs,
	RunE:  runManageCommitTrust,
}

var manageCommitUntrustCmd = &cobra.Command{
	Use:   "untrust",
	Short: "Withdraw that trust for this checkout",
	Args:  cobra.NoArgs,
	RunE:  runManageCommitUntrust,
}

// runManageCommitTrust and runManageCommitUntrust are named rather than inline
// closures over the bool. A RunE written as a func literal in a package-level
// var initializer compiles into `init.funcN`, which carries no trace of the
// command it belongs to — so every tool that identifies a leaf by its RunE
// symbol loses these two, and loses them SILENTLY. `go tool covdata func` does
// not emit closures at all, so they simply do not appear in a coverage report
// rather than appearing as uncovered.
func runManageCommitTrust(cmd *cobra.Command, _ []string) error {
	return runManageDirtyTreeAck(cmd, true)
}

func runManageCommitUntrust(cmd *cobra.Command, _ []string) error {
	return runManageDirtyTreeAck(cmd, false)
}

// manageDirtyTreeAckResult is the emit()-friendly payload both grant/revoke
// render — a bool an automation script can act on, plus a human summary.
type manageDirtyTreeAckResult struct {
	Status       string `json:"status"`
	Acknowledged bool   `json:"acknowledged"`
}

func runManageDirtyTreeAck(cmd *cobra.Command, ack bool) error {
	cfg, err := GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	if err := config.SetDirtyTreeCommitAck(cfg.FS(), cfg.GetAppDir(), ack); err != nil {
		return fmt.Errorf("failed to record dirty-tree-commit acknowledgement: %w", err)
	}
	state := "granted"
	if !ack {
		state = "revoked"
	}
	out := manageDirtyTreeAckResult{Status: state, Acknowledged: ack}
	return emit(cmd, out, func() error {
		fmt.Fprintf(cmd.OutOrStdout(), "dirty-tree-commit acknowledgement: %s\n", state)
		return nil
	})
}

func init() {
	rootCmd.AddCommand(manageCmd)

	// Orchestrators.
	manageCmd.AddCommand(manageInstallCmd)
	manageCmd.AddCommand(manageUninstallCmd)
	manageCmd.AddCommand(manageCheckCmd)
	manageInstallCmd.Flags().StringVar(&manageInstallEngine, "engine", "claude-code", "AI engine to record when scaffolding")
	manageInstallCmd.Flags().BoolVar(&manageInstallPrint, "print", false, "Print the steps that would run, without executing")

	// hooks.
	manageCmd.AddCommand(manageHooksCmd)
	manageHooksCmd.AddCommand(manageHooksInstallCmd)
	manageHooksCmd.AddCommand(manageHooksUninstallCmd)
	manageHooksCmd.AddCommand(manageHooksCheckCmd)
	manageHooksCmd.AddCommand(manageHooksListCmd)
	manageHooksListCmd.Flags().StringVar(&manageHooksListEvent, "event", "",
		"Report only this lifecycle event (pre_tool, post_tool, session_start, session_end, pre_shell, post_file_edit, turn_end)")
	manageHooksListCmd.Flags().StringSliceVar(&manageHooksListProfiles, "profile", nil,
		"Resolve against these profiles instead of the configured defaults (repeatable)")
	manageHooksInstallCmd.Flags().BoolVar(&manageHooksForce, "force", false, "Proceed even if the resolved project directory would write Claude Code's user-global settings (not inside a project / $HOME)")
	for _, c := range []*cobra.Command{manageHooksInstallCmd, manageHooksUninstallCmd} {
		c.Flags().StringVar(&manageHooksBackend, "backend", "all", "Backend to target (claude-code, codex, or all)")
	}

	// statusline opt-out.
	manageCmd.AddCommand(manageStatuslineCmd)
	manageStatuslineCmd.AddCommand(manageStatuslineInstallCmd)
	manageStatuslineCmd.AddCommand(manageStatuslineUninstallCmd)

	// gitignore.
	manageCmd.AddCommand(manageGitignoreCmd)
	manageGitignoreCmd.AddCommand(manageGitignoreInstallCmd)

	// dirty-tree-commit acknowledgement.
	manageCmd.AddCommand(manageCommitCmd)
	manageCommitCmd.AddCommand(manageCommitTrustCmd)
	manageCommitCmd.AddCommand(manageCommitUntrustCmd)
}
