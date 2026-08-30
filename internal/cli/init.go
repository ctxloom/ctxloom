package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/gitignore"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	taskops "github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize a new .ctxloom directory",
	Long: `Initialize a new .ctxloom directory in the current working directory.

This creates a marker directory that ctxloom uses to identify a project root.
All ctxloom data (profiles, bundles, fragments, commands) will be stored here.

If no .ctxloom directory exists when running ctxloom commands, the user home ~/.ctxloom
is used as a fallback.

init scaffolds a LOCAL default coding profile (.ctxloom/profiles/default.yaml,
inheriting the ctxloom-default baseline) and wires the trusted ctxloom-default
remote so its code-review lens profiles are available.

It then installs the dependencies that scaffold declares ('ctxloom deps pull'),
because a declared-but-uninstalled remote parent is SKIPPED at assembly: without
this the project reads as initialized while composing less context than its
configuration says. --no-pull suppresses it; a pull that cannot reach its remote
never rolls the init back — it warns and leaves a usable project.

When run interactively (TTY detected), init will guide you through:
  1. Selecting an AI engine (claude-code, codex, etc.)
  2. Optionally adding a personal ctxloom repository as a remote
  3. Launching your AI for one setup interview: discover and configure
     profiles, then bind agents to them (a coordinator you drive, a
     containerized developer, a cheap finder — plus any other roles)

The working outcome of init is a functioning ctxloom CLI/TUI — ACP editor
integration (either direction: ctxloom serving an editor, or ctxloom
connecting out to an ACP-speaking agent) is optional, separate configuration
via the acp-setup Agent Skill, never a gate on init completing.

Skipped or interrupted the interview? 'ctxloom init prompt' (or ask your
agent to run it) re-enters the companions/profiles/agent-binding half any time.

Examples:
  ctxloom init                     # Interactive setup (if TTY)
  ctxloom init --home              # Initialize in ~/.ctxloom
  ctxloom init --engine codex       # Pre-select engine
  ctxloom init --non-interactive   # Skip all prompts
  ctxloom init --no-pull           # Scaffold without installing dependencies`,
	// init is configured entirely by flags and reads no positional argument,
	// so anything positional is a mistyped subcommand. Without this it was the
	// most destructive instance of the silent-namespace defect groupNode
	// documents: init is RUNNABLE as well as a namespace, so `ctxloom init
	// prmopt` did not print help — it scaffolded .ctxloom, seeded remotes and
	// cloned them, then exited 0, having ignored the argument entirely. Here
	// NoArgs works where it cannot work on a pure group node, precisely
	// because init is runnable and so reaches ValidateArgs at all.
	Args: cobra.NoArgs,
	RunE: runInit,
}

var (
	initHome           bool
	initNonInteractive bool
	initSkipLaunch     bool
	initEngine         string
	initRemotes        []string
	initForge          string
	initNoPull         bool
)

// initPromptCmd is the real home for the setup-interview re-entry pointer:
// 'ctxloom agent setup' printed the whole interview but was misfiled under
// 'agent' (it configures companions, profiles, and agents together, not just
// agents). RunE is shared with the now-Deprecated 'agent setup' alias
// (agent.go's runSetupPromptCmd) — one body, two doors, so they can never
// drift.
var initPromptCmd = &cobra.Command{
	Use:   "prompt",
	Short: "Print ctxloom's setup prompt (companions, profiles, agents) for the LLM to follow",
	Long: `Emit ctxloom's built-in setup prompt: instructions for the LLM to interview
you and configure ctxloom collaboratively — companions (taskloom/ltk),
profiles/content, and agents (engine↔profile bindings). ACP (editor
integration, either direction) is a separate, optional step — see the
acp-setup Agent Skill.

This is the same body 'ctxloom init' hands to your engine at bootstrap and
'/ctxloom-init' loads in any ordinary session — this command is just a
re-entry pointer onto it, for a shell/script that wants the raw prompt text.

Run this (or ask your agent to) any time you want to reconfigure.`,
	Args: cobra.NoArgs,
	RunE: runSetupPromptCmd,
}

func init() {
	rootCmd.AddCommand(initCmd)
	initCmd.Flags().BoolVar(&initHome, "home", false, "Initialize in user home directory instead of current directory")
	initCmd.Flags().BoolVar(&initNonInteractive, "non-interactive", false, "Skip interactive prompts (use defaults and flags)")
	initCmd.Flags().BoolVar(&initSkipLaunch, "skip-launch", false, "Skip auto-launching the AI after init")
	initCmd.Flags().StringVar(&initEngine, "engine", "", "Pre-select AI engine (claude-code, codex, etc.)")
	initCmd.Flags().StringArrayVar(&initRemotes, "remote", nil, "Personal ctxloom repo to add as a trusted remote — its bundle changes apply without review (owner/repo or URL); repeatable")
	initCmd.Flags().BoolVar(&initNoPull, "no-pull", false, "Skip the dependency pull init ends with; declared dependencies stay uninstalled until 'ctxloom deps pull' runs")
	initCmd.Flags().StringVar(&initForge, "forge", "", "Bind every --remote to this forge (github, git, or a configured forges: label) instead of resolving by URL host")
	initCmd.AddCommand(initPromptCmd)
}

func runInit(cmd *cobra.Command, args []string) error {
	appDir, err := resolveAppDir(initHome)
	if err != nil {
		return err
	}

	alreadyExists := ctxloomDirExists(appDir)
	if alreadyExists {
		fmt.Printf("ctxloom directory already exists: %s\n", appDir)
	}

	interactive := isInteractiveTerminal() && !initNonInteractive
	selectedEngine := initEngine
	if alreadyExists {
		selectedEngine = engineForExistingDir(selectedEngine, appDir)
		// --remote/--forge were silently ignored here — the only
		// consumer of initRemotes/initForge (addPersonalRemotes) lived
		// exclusively inside setupNewCtxloomDir's fresh-init branch below,
		// so `ctxloom init --remote <repo>` against an existing .ctxloom
		// exited 0, printed "ctxloom directory already exists", and added
		// zero remotes. Honour the flags here too, on a pre-existing dir.
		addPersonalRemotesFn(cmd, appDir, initRemotes, initForge)
		// A re-init installs the declared closure too. A project whose first
		// init ran offline, or a fresh clone of one, has references it can
		// resolve only through a lockfile entry it does not have; re-running
		// init is the obvious thing to reach for, so it repairs that rather
		// than leaving a project that assembles degraded.
		pullSeededDependencies(cmd, appDir)
	} else {
		selectedEngine, err = setupNewCtxloomDir(cmd, appDir, selectedEngine, interactive)
		if err != nil {
			return err
		}
	}

	// --home initializes the fallback ~/.ctxloom, which is not a project and
	// has no identity to mint.
	if !initHome {
		if err := establishProjectIdentity(filepath.Dir(appDir)); err != nil {
			return err
		}
	}

	// Ensure a concrete engine for the launch step (covers existing dirs whose
	// config did not name one).
	primary, _ := getAvailableEngines()
	selectedEngine = pickDefaultEngine(selectedEngine, primary)

	return launchDiscovery(cmd, selectedEngine, appDir, interactive)
}

// establishProjectIdentity mints or adopts projectDir's project identity,
// writing the <projectDir>/.ctxloom/project-id marker and registering the
// project. It is idempotent: a project that already has an identity resolves
// to the same id and nothing changes.
//
// This is what makes `ctxloom init` the FOLLOWABLE remedy the rest of the
// codebase advertises. Both projectroot.TaskStoreRoot and worktreeSignpost
// (internal/config) tell a user to run init in a linked worktree to make it a
// deliberately separate project, and TaskStoreRoot's opt-out reads the
// project-id marker — the one piece of .ctxloom that is gitignored
// (gitignore.PrivateStatePatterns) and so cannot arrive with a checkout.
// Init left no marker at all, so following that advice changed nothing.
//
// It deliberately runs on a PRE-EXISTING .ctxloom as well as a fresh one,
// because the remedy's own case is the "already exists" one: .ctxloom is
// committed, so a linked worktree always arrives already carrying a complete
// one, and an init there would otherwise take the early branch and mint
// nothing.
//
// A failure here is fatal rather than a warning, against this file's usual
// warn-and-continue convention for post-scaffold steps. Identity is not a
// post-scaffold nicety: an init that reports success while leaving the
// project unidentified is the silent no-op that sends the user back to the
// same advice that just failed them.
func establishProjectIdentity(projectDir string) error {
	id, warning, err := taskops.ResolveProjectIdentity(projectDir)
	if err != nil {
		return fmt.Errorf("establish project identity for %s: %w", projectDir, err)
	}
	if warning != "" {
		clidiag.Warn("ctxloom", "%s", warning)
	}
	fmt.Printf("Project identity: %s\n", id)
	return nil
}

// resolveAppDir returns the .ctxloom directory to operate on: under the user's
// home when home is true, else under the current working directory.
func resolveAppDir(home bool) (string, error) {
	if home {
		dir, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("failed to get home directory: %w", err)
		}
		return filepath.Join(dir, config.AppDirName), nil
	}
	pwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get current directory: %w", err)
	}
	return filepath.Join(pwd, config.AppDirName), nil
}

// ctxloomDirExists reports whether appDir already exists as a directory.
func ctxloomDirExists(appDir string) bool {
	info, err := os.Stat(appDir)
	return err == nil && info.IsDir()
}

// engineForExistingDir resolves which engine a RE-INIT (a .ctxloom that
// already exists) targets. An explicit selection wins: --engine is a flag typed
// on THIS invocation, so a value stored in the config may not override it —
// doing so read the flag and then discarded it, with nothing said. Otherwise the
// engine recorded in the existing config applies, and if that is unreadable or
// names none the choice falls through to pickDefaultEngine.
func engineForExistingDir(selected, appDir string) string {
	if selected != "" {
		return selected
	}
	if cfg, err := config.Load(config.WithAppDir(appDir)); err == nil {
		return cfg.GetDefaultLLM()
	}
	return ""
}

// setupNewCtxloomDir performs first-time setup for a non-existent .ctxloom dir:
// resolve the engine (with interactive prompts), write the skeleton, register
// personal/discovery remotes, apply hooks, and update .gitignore. Returns the
// resolved engine. Per CLAUDE.md fault tolerance, post-scaffold steps warn and
// continue; only directory/config creation failures are fatal.
func setupNewCtxloomDir(cmd *cobra.Command, appDir, selectedEngine string, interactive bool) (string, error) {
	engine, personalRepos, dirtyTreeHandler, dirtyTreeCommitAck, err := resolveSetupEngine(selectedEngine, interactive)
	if err != nil {
		return "", err
	}

	if err := writeInitialConfig(appDir, engine, dirtyTreeHandler, dirtyTreeCommitAck); err != nil {
		return "", err
	}
	fmt.Printf("Initialized ctxloom directory: %s\n", appDir)
	fmt.Printf("Default AI engine: %s\n", engine)
	fmt.Println("Seeded remote \"ctxloom-default\" (official curated repo). Its bundles are signed")
	fmt.Println("by ctxloom's publishing key, which this binary trusts, so they need no review.")

	// Targeted system-dependency gate, right after the marker dir/minimal
	// config and BEFORE the clone two lines down: git is a hard prerequisite
	// of THIS call (cloneConfiguredRemotes shells out to it next), so a
	// missing git fails loud here, with a named fix, instead of surfacing as
	// a raw git error out of the clone machinery. Runs on both the
	// interactive and --non-interactive paths (both reach this same call) —
	// a scripted init still needs git to clone.
	if err := checkSystemDeps(engine); err != nil {
		return "", err
	}

	// Remotes from --remote flags are added alongside any the interactive prompt
	// collected, so a fully non-interactive run can still register personal repos.
	addPersonalRemotesFn(cmd, appDir, append(append([]string{}, initRemotes...), personalRepos...), initForge)
	cloneConfiguredRemotes(cmd, appDir)
	pullSeededDependencies(cmd, appDir)
	applyInitHooks(cmd, appDir)

	// Exclude ctxloom's private working state from version control.
	if err := gitignore.Ensure(filepath.Dir(appDir), gitignore.Comment, gitignore.PrivateStatePatterns...); err != nil {
		clidiag.Warn("ctxloom", "failed to update .gitignore: %v", err)
	}

	return engine, nil
}

// resolveSetupEngine decides which engine to install with. It warns (but does
// not fail) when no engines are detected, runs the interactive engine/repo/
// dirty-tree-handler prompts when applicable, and finally falls back to the
// first available primary engine. Returns errNoEngines only when the
// interactive selection reports none installed. dirtyTreeHandler/
// dirtyTreeCommitAck stay at their zero values ("", false) whenever the
// prompts don't run (an --engine flag given, or a non-interactive init) — the
// same as if the question had never been asked.
func resolveSetupEngine(selected string, interactive bool) (engine string, repos []string, dirtyTreeHandler string, dirtyTreeCommitAck bool, err error) {
	if selected == "" && noEnginesInstalled() {
		warnNoEnginesDetected()
		selected = "claude-code"
	}

	if interactive && selected == "" {
		selected, repos, dirtyTreeHandler, dirtyTreeCommitAck, err = promptForEngineAndRepos()
		if err != nil {
			return "", nil, "", false, err
		}
	}

	primary, _ := getAvailableEngines()
	return pickDefaultEngine(selected, primary), repos, dirtyTreeHandler, dirtyTreeCommitAck, nil
}

// writeInitialConfig delegates project bootstrap (the .ctxloom skeleton +
// config.yaml + default remotes.yaml) to the operations core.
//
// It EXPLICITLY invalidates the ambient config memo on success, rather than
// relying on the memo's own stat-based self-correction (config.Load's mtime
// +size check). init's own post-scaffold steps (addPersonalRemotes,
// cloneConfiguredRemotes, pullSeededDependencies, applyInitHooks) read the
// config back appDir-SCOPED, which is never served from the memo at all — but
// the AMBIENT readers that follow in the same process are (GetConfig in
// launchDiscovery and launchEngineWithPrompt), and a stat check has only
// mtime+size granularity to key on: theoretically indistinguishable from the
// pre-write state on a filesystem coarse enough, or if a PRIOR config.Load in
// this same init run (e.g. engineForExistingDir probing for a pre-existing
// config before this write happens) already memoized a "missing" stamp whose
// invalidation this write's own stat SHOULD, but need not provably, trigger.
// Invalidate() removes that dependency: the very next Load anywhere in the
// process re-reads from disk unconditionally.
func writeInitialConfig(appDir, engine, dirtyTreeHandler string, dirtyTreeCommitAck bool) error {
	_, err := operations.InitializeProject(context.Background(), operations.InitializeProjectRequest{
		AppDir:             appDir,
		Engine:             engine,
		DirtyTreeHandler:   dirtyTreeHandler,
		DirtyTreeCommitAck: dirtyTreeCommitAck,
	})
	if err == nil {
		config.Invalidate()
	}
	return err
}

// personalRemoteRequests builds the AddRemote requests for the user's personal
// repos. The first is named "personal"; subsequent ones get "personal-2",
// "personal-3", … so each is a distinct, addressable remote. A non-empty forge
// binds every remote to that forge (github, git, or a configured label) instead
// of letting resolution fall back to URL-host matching.
//
// A remote is no longer trusted on add (spec §11): trusting content is now
// keyed to a publisher KEY, not to the repo it came from. To auto-trust your own
// personal repo's content, sign its bundles with `ctxloom sign` and trust your
// key with `ctxloom signer add`. Until you do, its content takes the review
// path, which is exactly right for content nobody has vouched for.
func personalRemoteRequests(repos []string, forge string) []operations.AddRemoteRequest {
	reqs := make([]operations.AddRemoteRequest, 0, len(repos))
	for i, repo := range repos {
		name := "personal"
		if i > 0 {
			name = fmt.Sprintf("personal-%d", i+1)
		}
		reqs = append(reqs, operations.AddRemoteRequest{Name: name, URL: repo, Forge: forge})
	}
	return reqs
}

// addPersonalRemotes registers the user's personal repos in the .ctxloom at
// appDir — the one THIS init targets, never whatever .ctxloom an ambient
// discovery walk would find from the cwd (they differ under `init --home` inside
// a project, or with CTXLOOM_ROOT set elsewhere). Failures warn and continue (an
// unknown --forge label rolls that single remote back).
// addPersonalRemotesFn is a package var seam over addPersonalRemotes: tests
// stub it to verify runInit's/setupNewCtxloomDir's branching reaches it (with
// the right args) without exercising the real remote-add machinery (registry
// + network fetch/clone). Defaults to the real function.
var addPersonalRemotesFn = addPersonalRemotes

func addPersonalRemotes(cmd *cobra.Command, appDir string, repos []string, forge string) {
	if len(repos) == 0 {
		return
	}
	cfg, loadErr := config.Load(config.WithAppDir(appDir))
	if loadErr != nil {
		clidiag.Warn("ctxloom", "failed to load config for remote: %v", loadErr)
		return
	}
	for _, req := range personalRemoteRequests(repos, forge) {
		if _, addErr := operations.AddRemote(cmd.Context(), cfg, req); addErr != nil {
			clidiag.Warn("ctxloom", "failed to add remote %q (%s): %v", req.Name, req.URL, addErr)
		} else {
			fmt.Printf("Added remote %q: %s (content takes the review path — 'ctxloom review')\n", req.Name, req.URL)
		}
	}
}

// cloneConfiguredRemotes eagerly clones every remote configured in the .ctxloom
// at appDir (see addPersonalRemotes on why the dir is passed, not discovered) so
// discovery (search_library, browse) can read them offline. Fault-tolerant:
// per-remote failures warn and continue.
func cloneConfiguredRemotes(cmd *cobra.Command, appDir string) {
	cfg, loadErr := config.Load(config.WithAppDir(appDir))
	if loadErr != nil {
		clidiag.Warn("ctxloom", "failed to load config for cloning remotes: %v", loadErr)
		return
	}
	cloneRes, cloneErr := operations.EnsureRemoteClones(cmd.Context(), cfg)
	if cloneErr != nil {
		clidiag.Warn("ctxloom", "failed to clone remotes: %v", cloneErr)
		return
	}
	if len(cloneRes.Cloned) > 0 {
		fmt.Printf("Cloned remotes for discovery: %s\n", strings.Join(cloneRes.Cloned, ", "))
	}
}

// pullSeededDependencies pulls and locks the remote dependencies the config at
// appDir references (see addPersonalRemotes on why the dir is passed, not
// discovered) — most importantly the seeded default profile, which resolves
// only through a lockfile entry. Without it a fresh project is degraded rather
// than merely unfinished: assembly SKIPS the uninstalled reference, so the very
// first `ctxloom run` after init composes less context than the configuration
// says it should, and `ctxloom doctor` reports the skip.
//
// It runs before applyInitHooks because hooks are materialized from the
// installed bundles: hooks applied over a closure that is not there yet
// register nothing the pulled content ships.
//
// --no-pull suppresses it, and the pull is the ONLY thing that flag decides; it
// configures this invocation and is never bridged to a config key.
//
// Fault-tolerant, and deliberately so: a pull needs a reachable remote, and a
// user who is offline, proxied, or pointed at an unreachable address must still
// end up with a usable initialized project. Every failure keeps what init wrote
// and reports what is missing (warnDependencyPullFailed) instead of failing the
// command.
func pullSeededDependencies(cmd *cobra.Command, appDir string) {
	if initNoPull {
		fmt.Println("Skipped the dependency pull (--no-pull). Any remote dependencies this project")
		fmt.Println("references are NOT installed, so context assembly will skip them until you run:")
		fmt.Println("  ctxloom deps pull")
		return
	}
	cfg, err := config.Load(config.WithAppDir(appDir))
	if err != nil {
		warnDependencyPullFailed("failed to load config for dependency pull: " + err.Error())
		return
	}
	result, syncErr := operations.SyncDependencies(cmd.Context(), cfg, operations.SyncDependenciesRequest{
		Lock:       true,
		ApplyHooks: false, // applyInitHooks runs right after
	})
	if syncErr != nil {
		warnDependencyPullFailed(syncErr.Error())
		return
	}
	// A sync returns a NIL ERROR for a run in which individual references
	// failed to fetch — the per-item outcomes live in the result, and an
	// offline init produces exactly that shape: nil error, zero installed,
	// every reference failed. Reporting only on Installed printed nothing at
	// all in that case, which is this project's characteristic defect (exit 0,
	// no complaint, zero bytes fetched). pullResultErr is the same verdict
	// `ctxloom deps pull` exits on, so init and the command it stands in for
	// can never disagree about what counts as a failed pull.
	if resultErr := pullResultErr(result); resultErr != nil {
		warnDependencyPullFailed(resultErr.Error())
		return
	}
	if result.Installed > 0 {
		fmt.Printf("Pulled %d seeded dependencies\n", result.Installed)
	}
}

// warnDependencyPullFailed reports a dependency pull that did not complete
// during init. It names three things a bare error cannot: that the init itself
// stands (nothing is rolled back), that the consequence is uninstalled
// dependencies which assembly will silently skip, and the one command that
// finishes the job once the remote is reachable.
func warnDependencyPullFailed(reason string) {
	clidiag.Warn("ctxloom",
		"the dependency pull did not complete: %s\n"+
			"  Everything init wrote is kept — the project is initialized and usable.\n"+
			"  Its remote dependencies are NOT installed, so context assembly will skip\n"+
			"  them (`ctxloom doctor` reports this). Once the remote is reachable, run:\n"+
			"    ctxloom deps pull",
		reason)
}

// applyHooksFn is a package var seam over operations.ApplyHooks: tests stub it
// to drive applyInitHooks' reporting branches (most importantly the empty
// backend list, which is a failure to report rather than a success line to
// print) without writing real backend settings files. Defaults to the real
// function.
var applyHooksFn = operations.ApplyHooks

// applyInitHooks registers the ctxloom MCP server with every backend, from the
// config at appDir (see addPersonalRemotes on why the dir is passed, not
// discovered). Failures
// warn and continue (fault tolerant). An apply that touched NO backend is
// reported as the failure it is: the engine settings surfaces are what make
// ctxloom reachable from a session at all, so "applied to nothing" must never
// render as a success line with an empty payload.
func applyInitHooks(cmd *cobra.Command, appDir string) {
	// ApplyHooks used to take a *config.Config and never read it, so
	// the appDir-scoped config init had just built was silently discarded and
	// ApplyHooks re-discovered one by walking up from cwd — the wrong project
	// whenever `init` targeted a directory other than the working one.
	// ConfigLoader is the seam ApplyHooks actually honours, so the appDir goes
	// there. A load failure now surfaces through applyErr below.
	result, applyErr := applyHooksFn(context.Background(), operations.ApplyHooksRequest{
		Backend:           "all",
		RegenerateContext: false,
		ConfigLoader: func() (*config.Config, error) {
			return config.Load(config.WithAppDir(appDir))
		},
	})
	if applyErr != nil {
		clidiag.Warn("ctxloom", "failed to apply hooks: %v", applyErr)
		return
	}
	if result == nil || len(result.Backends) == 0 {
		clidiag.Warn("ctxloom", "applied hooks to no backends — no engine settings were written, so ctxloom's MCP server and context hook are not registered anywhere; install an engine and re-run `ctxloom manage hooks install`")
		return
	}
	fmt.Printf("Applied hooks for: %v\n", result.Backends)
}
