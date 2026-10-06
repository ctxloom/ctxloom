package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/configload"
	"github.com/ctxloom/ctxloom/internal/adapters/gitignore"
	"github.com/ctxloom/ctxloom/internal/adapters/hostpty"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/core/config"
	enginepkg "github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	taskops "github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// initCmd sets up a project. Why each step is there:
//   - It installs the scaffold's dependencies because a declared but
//     uninstalled remote parent is SKIPPED at assembly: without the pull the
//     project reads as initialized while composing less context than its
//     configuration says. A pull that cannot reach its remote never rolls the
//     init back.
//   - It writes no engine file into the project: the setup interview, like
//     every 'ctxloom run', carries ctxloom's hooks and MCP server in its own
//     session home, and an engine launched directly in the tree gets neither.
//   - The token flow runs the engine's own 'claude setup-token' attached to the
//     terminal and reads nothing it prints: ctxloom never captures or stores
//     the token (see authLong). Off a terminal it exits 0 because the project
//     is already set up; only the interview was not launched.
//   - 'auth: login' is not pre-checked beyond its store being on this host; the
//     engine reports a login it does not accept when the session starts.
var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize a new .ctxloom directory",
	Long: `Set up ctxloom in the current directory: create the .ctxloom directory that
marks the project root, scaffold a local "default" profile on the
ctxloom-default baseline, register the ctxloom-default remote, and install the
dependencies the scaffold declares (--no-pull skips that). Outside any project,
ctxloom falls back to ~/.ctxloom.

On a terminal, init asks which engine to use and whether to add a personal
repository, takes the remaining defaults without asking (printing one line
for each and how to change it), then launches your engine for one setup
interview that configures profiles and binds agents. 'ctxloom init prompt'
re-enters that interview later.

Agents authenticate with a token you mint once. If none is exported, init runs
the engine's own flow (claude: 'claude setup-token') and shows the line to add
to your shell profile; export it and re-run init. Off a terminal, or with
--non-interactive, it warns and exits 0 without launching the interview;
--skip-launch launches nothing.

init writes 'auth: token', so your own sessions use that same token; 'auth:
login' (your own engine login, host only) is opt-in.`,
	Example: `  ctxloom init                     # Interactive setup (if TTY)
  ctxloom init --home              # Initialize in ~/.ctxloom
  ctxloom init --engine claude-code # Pre-select engine
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

// initPromptCmd is the setup-interview re-entry pointer. It lives under
// 'init', not 'agent', because the interview configures companions, profiles
// and agents together, not just agents.
var initPromptCmd = &cobra.Command{
	Use:   "prompt",
	Short: "Print ctxloom's setup prompt (companions, profiles, agents) for the LLM to follow",
	Long: `Emit ctxloom's built-in setup prompt: instructions for the LLM to interview
you and configure ctxloom collaboratively — companions (taskloom/ltk),
profiles/content, and agents (engine↔profile bindings).

This is the same body 'ctxloom init' hands to your engine at bootstrap and
'/ctxloom-init' loads in any ordinary session — this command is just a
re-entry pointer onto it, for a shell/script that wants the raw prompt text.

Run this (or ask your agent to) any time you want to reconfigure.`,
	Example: `  ctxloom init prompt`,
	Args:    cobra.NoArgs,
	RunE:    runSetupPromptCmd,
}

func init() {
	rootCmd.AddCommand(initCmd)
	initCmd.Flags().BoolVar(&initHome, "home", false, "Initialize in user home directory instead of current directory")
	initCmd.Flags().BoolVar(&initNonInteractive, "non-interactive", false, "Skip interactive prompts (use defaults and flags)")
	initCmd.Flags().BoolVar(&initSkipLaunch, "skip-launch", false, "Skip auto-launching the AI after init")
	// Usage is completed by applyEngineNamedHelp once the registry is composed;
	// init() runs before that, when the engine list is still empty.
	initCmd.Flags().StringVar(&initEngine, "engine", "", "Pre-select AI engine")
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
	if err := pinAppDir(cmd, appDir); err != nil {
		return err
	}

	alreadyExists := initializedProjectExists(afero.NewOsFs(), appDir)
	if alreadyExists {
		fmt.Printf(initAlreadyExistsFormat, appDir)
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
		// A project initialized before the approvals store was provisioned
		// withholds everything until it is: re-running init is the remedy the
		// trust finding and doctor both name.
		if err := operations.ProvisionApprovalsStore(nil, appDir); err != nil {
			return err
		}
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
// (internal/core/config) tell a user to run init in a linked worktree to make it a
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

// initAlreadyExistsFormat is init's notice for a project it is re-running
// over; it takes the project's .ctxloom path.
const initAlreadyExistsFormat = "ctxloom directory already exists: %s\n"

// initializedProjectExists reports whether appDir is an initialized project:
// a .ctxloom carrying its config file. A .ctxloom without one is what an
// abandoned or partial setup leaves (a marker, a state dir), and init
// scaffolds it like a fresh project rather than treating it as one to keep.
func initializedProjectExists(fsys afero.Fs, appDir string) bool {
	if !ctxloomDirExists(fsys, appDir) {
		return false
	}
	_, err := fsys.Stat(paths.ConfigPath(appDir))
	return err == nil
}

// ctxloomDirExists reports whether appDir already exists as a directory.
func ctxloomDirExists(fsys afero.Fs, appDir string) bool {
	info, err := fsys.Stat(appDir)
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
	if cfg, err := GetConfig(); err == nil {
		return cfg.GetDefaultLLM()
	}
	return ""
}

// ctxloomDefaultTrusted reports whether THIS machine actually trusts
// ctxloom's own embedded publishing key for the publish namespace — the key
// that signs every bundle the "ctxloom-default" remote serves. It reads the
// live trust root (Config.TrustRoot) rather than asserting trust
// unconditionally: a human can locally distrust an embedded principal
// (`ctxloom signer untrust <principal>`, writing
// ~/.ctxloom/distrusted_signers or its project equivalent), and init used to
// claim "this binary trusts" the seeded remote regardless, then in the same
// run print a dozen "withheld: signed by a key this machine does not trust"
// warnings when that remote's content was actually admitted.
//
// A nil cfg (the config init just wrote could not be read back) answers
// false: the honest answer when trust cannot be established is "do not
// claim it," not "assume the common case."
func ctxloomDefaultTrusted(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	root := cfg.Trust().Root()
	now := time.Now()
	for _, e := range configload.EmbeddedSigners().Entries() {
		if root.TrustedForNamespace(e.PublicKey, signing.NamespacePublish, now).Trusted {
			return true
		}
	}
	return false
}

// setupNewCtxloomDir performs first-time setup for a non-existent .ctxloom dir:
// resolve the engine (with interactive prompts), write the skeleton, register
// personal/discovery remotes, pull the seeded dependencies, and write the
// nested .gitignore. It writes NO engine file: the runtime surfaces reach an
// engine through a `ctxloom run` session's own home (launchDiscovery's
// interview session included), never through the project tree. Returns the
// resolved engine. Per CLAUDE.md fault tolerance, post-scaffold steps warn
// and continue; only directory/config creation failures are fatal.
func setupNewCtxloomDir(cmd *cobra.Command, appDir, selectedEngine string, interactive bool) (string, error) {
	engine, personalRepos, dirtyTreeHandler, headlessPermissions, err := resolveSetupEngine(selectedEngine, interactive)
	if err != nil {
		return "", err
	}

	if err := writeInitialConfig(appDir, engine, dirtyTreeHandler, headlessPermissions); err != nil {
		return "", err
	}
	fmt.Printf("Initialized ctxloom directory: %s\n", appDir)
	fmt.Printf("Default AI engine: %s\n", engine)
	fmt.Println("Seeded remote \"ctxloom-default\" (official curated repo).")
	trustCfg, trustCfgErr := GetConfig()
	if trustCfgErr != nil {
		trustCfg = nil
	}
	if ctxloomDefaultTrusted(trustCfg) {
		fmt.Println("Its bundles are signed by ctxloom's publishing key, which this binary trusts, so they need no review.")
	} else {
		fmt.Println("Its bundles are signed by ctxloom's publishing key, which this machine does not trust — they will await `ctxloom review`.")
	}

	// Targeted system-dependency gate, right after the marker dir/minimal
	// config and BEFORE the clone two lines down: git is a hard prerequisite
	// of THIS call (cloneConfiguredRemotes shells out to it next), so a
	// missing git fails loud here, with a named fix, instead of surfacing as
	// a raw git error out of the clone machinery. Runs on both the
	// interactive and --non-interactive paths (both reach this same call) —
	// a scripted init still needs git to clone.
	if err := checkSystemDeps(); err != nil {
		return "", err
	}

	// Remotes from --remote flags are added alongside any the interactive prompt
	// collected, so a fully non-interactive run can still register personal repos.
	addPersonalRemotesFn(cmd, appDir, append(append([]string{}, initRemotes...), personalRepos...), initForge)
	cloneConfiguredRemotesFn(cmd, appDir)
	pullSeededDependencies(cmd, appDir)

	// Exclude ctxloom's private working state from version control, in the
	// nested .ctxloom/.gitignore ctxloom owns rather than by appending to the
	// project's own root file. The nested file is meant to be COMMITTED: that
	// is what carries the rules into clones and linked worktrees.
	if _, err := gitignore.EnsureNested(afero.NewOsFs(), filepath.Dir(appDir)); err != nil {
		clidiag.Warn("ctxloom", "failed to write %s: %v", gitignore.NestedGitignorePath(filepath.Dir(appDir)), err)
	}

	return engine, nil
}

// resolveSetupEngine decides which engine to install with. It warns (but does
// not fail) when no engines are detected, runs the interactive engine/repo
// prompts when applicable and takes the interview's defaults
// (takeInterviewDefaults), and finally falls back to the first available
// primary engine. Returns errNoEngines only when the interactive selection
// reports none installed. dirtyTreeHandler/headlessPermissions stay at their
// zero values whenever the interview doesn't run (an --engine flag given, or
// a non-interactive init).
func resolveSetupEngine(selected string, interactive bool) (engine string, repos []string, dirtyTreeHandler, headlessPermissions string, err error) {
	if selected == "" && noEnginesInstalled() {
		warnNoEnginesDetected()
		selected = operations.DefaultEngineName(App().Engines())
	}

	if interactive && selected == "" {
		selected, repos, err = promptForEngineAndRepos()
		if err != nil {
			return "", nil, "", "", err
		}
		dirtyTreeHandler, headlessPermissions = takeInterviewDefaults(os.Stdout)
	}

	primary, _ := getAvailableEngines()
	return pickDefaultEngine(selected, primary), repos, dirtyTreeHandler, headlessPermissions, nil
}

// writeInitialConfig delegates project bootstrap (the .ctxloom skeleton +
// config.yaml + default remotes.yaml) to the operations core, then publishes
// the generation that holds the scaffold — the one Reload after a scaffold.
// Every post-scaffold step (addPersonalRemotes,
// cloneConfiguredRemotes, pullSeededDependencies, applyInitHooks) and the
// discovery launch read that generation; nothing in this process observes
// the pre-scaffold state again.
func writeInitialConfig(appDir, engine, dirtyTreeHandler, headlessPermissions string) error {
	_, err := operations.InitializeProject(context.Background(), App().Engines(), operations.InitializeProjectRequest{
		AppDir:              appDir,
		Engine:              engine,
		DirtyTreeHandler:    dirtyTreeHandler,
		HeadlessPermissions: headlessPermissions,
	})
	if err != nil {
		return err
	}
	// The scaffold is written: the generation init's discovery launch
	// resolves against is the one that holds it.
	_, err = App().Reload(context.Background())
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
// key with `ctxloom signer trust`. Until you do, its content takes the review
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
	cfg, loadErr := GetConfig()
	if loadErr != nil {
		clidiag.Warn("ctxloom", "failed to load config for remote: %v", loadErr)
		return
	}
	for _, req := range personalRemoteRequests(repos, forge) {
		res, addErr := operations.AddRemote(cmd.Context(), cfg, req)
		if addErr != nil {
			clidiag.Warn("ctxloom", "failed to add remote %q (%s): %v", req.Name, req.URL, addErr)
		} else {
			if res.Warning != "" {
				clidiag.Warn("ctxloom", "%s", res.Warning)
			}
			fmt.Printf("Added remote %q: %s (content takes the review path — 'ctxloom review')\n", req.Name, req.URL)
		}
	}
}

// cloneConfiguredRemotesFn is a package var seam over cloneConfiguredRemotes:
// a fresh init clones the seeded default remote, which is the one step of
// the fresh branch that reaches the network, so tests of that branch stub it
// out. Defaults to the real function.
var cloneConfiguredRemotesFn = cloneConfiguredRemotes

// cloneConfiguredRemotes eagerly clones every remote configured in the .ctxloom
// at appDir (see addPersonalRemotes on why the dir is passed, not discovered) so
// discovery (search_library, browse) can read them offline. Fault-tolerant:
// per-remote failures warn and continue.
func cloneConfiguredRemotes(cmd *cobra.Command, appDir string) {
	cfg, loadErr := GetConfig()
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
// It runs before launchDiscovery because the interview session composes
// from the installed bundles: a launch over a closure that is not there yet
// carries nothing the pulled content ships.
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
	result, syncErr := operations.SyncDependencies(cmd.Context(), App(), operations.SyncDependenciesRequest{
		Lock:       true,
		ApplyHooks: false, // applyInitHooks runs right after
	})
	if syncErr != nil {
		warnDependencyPullFailed(syncErr)
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
		warnDependencyPullFailed(resultErr)
		return
	}
	if result.Installed > 0 {
		fmt.Printf("Pulled %d seeded dependencies\n", result.Installed)
	}
}

// warnDependencyPullFailed reports a dependency pull that did not complete
// during init. It names three things a bare error cannot: that the init itself
// stands (nothing is rolled back), that the consequence is uninstalled
// dependencies which assembly will silently skip, and the fix — the one err
// names (a misspelt reference, a retired-format pin), else the pull that
// finishes the job once the remote is reachable.
func warnDependencyPullFailed(err error) {
	remedy, ok := clifmt.RemedyOf(err)
	if !ok {
		remedy = remedyRetryPull
	}
	clidiag.WarnRemedy("ctxloom", remedy,
		"the dependency pull did not complete: %v\n"+
			"  Everything init wrote is kept — the project is initialized and usable.\n"+
			"  Its remote dependencies are NOT installed, so context assembly will skip\n"+
			"  them (`ctxloom doctor` reports this).",
		err)
}

// remedyRetryPull is init's fix for a pull whose failure names none of its own.
const remedyRetryPull = "ctxloom deps pull, once the remote is reachable"

// The setup launch: the auth probe and the discovery session are TWO
// launches with TWO minted identities through the one resolver — a
// throwaway one-shot that proves a live, authenticated round trip, then the
// interactive session with the setup skill in context. They differ in what
// they ask for, not in how they are resolved.

func discoverySessionPrompt(cfg *config.Config) string {
	if cfg == nil {
		return ctxloomInitPrompt
	}
	return operations.ResolveSetupPrompt(cfg, ctxloomInitPrompt)
}

// discoveryPosture is the setup session's mode: the one that asks the human.
const discoveryPosture = "default"

// authPingTask is the smallest possible prompt sent to probe the selected
// engine's auth before init hands off to its raw CLI/TUI — just enough to
// prove a live, authenticated round trip happened.
const authPingTask = "Reply with exactly: ok"

// engineAuthFixHint names the fix for a failed auth probe. An engine that
// declares auth (Engine.Home().Auth) gets pointed at the token the probe, a
// one-shot, reads from the environment, which `ctxloom auth status` names; one
// that declares none — or is not registered at all — gets a generic but
// actionable fix rather than a blank, since the probe still failed.
func engineAuthFixHint(reg enginepkg.Registry, name string) string {
	kind, ok := reg.Lookup(enginepkg.Name(name))
	if ok {
		_, ok = kind.Home().Auth.Get()
	}
	if !ok {
		return "authenticate the engine (subscription login or its API-key env var) and try again"
	}
	return fmt.Sprintf("export the token every agent authenticates with — `ctxloom auth status` shows, for %s, which variable it is read from and whether it is set — and try again", name)
}

// authPingHosts is a test seam: nil runs the probe on the command's
// internal coordinator (internalRunHosts); tests inject a RunHosts double so
// no real engine binary or credential is required to exercise the gate.
var authPingHosts operations.RunHosts

func pingHosts() operations.RunHosts {
	if authPingHosts != nil {
		return authPingHosts
	}
	return internalRunHosts()
}

// pingEngineAuth probes the selected engine with the smallest possible
// one-shot BEFORE init hands off to its raw CLI/TUI ("a dead first session
// inside a vendor TUI is invisible failure; catch it in code"). It is an
// internal one-shot of its own — its own harp, ended when it answers — on
// the engine init selected, at bypass: a throwaway liveness probe with a
// fixed trivial prompt wants no permission gating, whatever posture the
// engine's label declares, and says so out loud. Being a one-shot it
// authenticates as every agent does, with the token (launch.RunAuth): the
// credential every run ctxloom spawns needs. Any failure (missing binary, no
// token, a dead subscription token, a real backend error) fails loud, naming
// the fix for THIS engine; this is a liveness gate, not a login flow.
func pingEngineAuth(ctx context.Context, deps launch.Deps, engine, workDir string) error {
	src := operations.InternalSource(engine, "", workDir)
	src.Permission = permissionBypass
	probe, err := operations.StartOneShot(ctx, deps, pingHosts(), sessions.Seed{ProjectDir: workDir}, src)
	if err != nil {
		return probeFailure(engine, probeFailedToStart, err)
	}
	defer probe.End()
	if _, err := probe.Turn(ctx, authPingTask); err != nil {
		return probeFailure(engine, probeDidNotAnswer, err)
	}
	return nil
}

// The probe's two failure points, named for WHAT FAILED rather than for auth.
// Both are liveness verdicts: the probe catches a missing binary, a rejected
// flag, a config file the engine refuses to start against, or an unreachable
// backend just as readily as a dead credential.
const (
	probeFailedToStart = "the startup probe could not be started"
	probeDidNotAnswer  = "the startup probe did not answer"
	// probeAuthGuess frames the login hint as a CANDIDATE, not a verdict.
	// It is a constant so a test can pin the framing: an unconditional
	// "authentication failed" is the defect this message exists to avoid,
	// and nothing else would catch its return.
	probeAuthGuess = "if it is not authenticated"
)

// probeFailure reports a failed liveness probe: what failed, the engine's own
// error, and authentication as ONE CANDIDATE CAUSE rather than the diagnosis.
//
// Reporting every failure as "auth check failed" with only the login hint is
// a diagnosis pointing AWAY from the defect whenever auth is not the defect —
// and auth is usually not the defect: a probe killed by an argv the engine
// rejected would send the user to re-run a login that was already good while
// the real refusal went unmentioned. The engine's own error is the
// load-bearing part of this message; the hint is a guess and is phrased as one.
func probeFailure(engine, what string, err error) error {
	return fmt.Errorf("%s isn't ready to launch: %s (%v) — %s, %s; if it is, the engine refused this launch for some other reason and `ctxloom doctor` runs the full check",
		engine, what, err, probeAuthGuess, engineAuthFixHint(App().Engines(), engine))
}

// launchEngineWithPrompt starts the engine's own raw CLI/TUI on the resolved
// discovery launch: an owner-owned INTERACTIVE run on the command's internal
// coordinator, its runner on the pty this process holds and pumps the
// terminal onto — exactly `ctxloom run`'s interactive path (ptyStarter,
// driveOwnedInteractive) over a runState this launch fills. Errors are
// returned to the caller, which reports them through strictness and refuses
// by default rather than swallowing them.
func launchEngineWithPrompt(ctx context.Context, deps launch.Deps, workDir string, l launch.Launch) error {
	env, ok := operations.EnvironmentOf(l.Cell)
	if !ok {
		return errors.New("the discovery launch's cell carries no environment")
	}
	c, err := internalCoordinator(workDir, l.Identity.Harp)
	if err != nil {
		return fmt.Errorf("failed to launch %s: %w", l.Engine, err)
	}
	st := &runState{
		ctx:          ctx,
		cfg:          deps.Snapshot.Config,
		workDir:      workDir,
		launch:       l,
		activeHarp:   l.Identity.Harp,
		backendName:  string(l.Engine),
		label:        l.Label.Label,
		labelModel:   l.Label.Model,
		env:          env,
		sessionCoord: c,
	}
	st.launch.Env = stampTerminalEnv(st.launch.Env)
	sess, err := startOwnedRun(ctx, c, ownedRunLaunch{Launch: st.launch, Rebind: endpointRebinder(deps)}, st.ptyStarter(hostpty.Start))
	if err != nil {
		if st.pty != nil {
			st.pty.Kill()
		}
		return fmt.Errorf("AI session failed to start: %w", err)
	}
	st.ownedRun = sess
	defer sess.cancel()
	defer st.pty.Kill()
	if err := st.driveOwnedInteractive(); err != nil {
		return fmt.Errorf("AI session ended: %w", err)
	}
	return nil
}

// initLaunchDeps composes the resolver's ports for the setup launches: the
// process's own (App), or — a test seam — stateless doubles.
var initLaunchDeps = func(ctx context.Context) (launch.Deps, error) { return App().LaunchDeps(ctx) }

// launchEngineWithPromptFn is a package var seam over launchEngineWithPrompt:
// tests stub it to verify launchDiscovery's branching (the ping gates the
// launch; a successful ping proceeds to it) without spawning a real engine
// subprocess. Defaults to the real function.
var launchEngineWithPromptFn = launchEngineWithPrompt

// initLaunchSkippedNoTokenFormat is init's warning when setup succeeded but the
// setup interview could not launch for want of an agent token; it takes the
// token refusal.
const initLaunchSkippedNoTokenFormat = "the project is set up, but the setup interview was not launched: %v — once the token is exported, re-run `ctxloom init` to launch it"

// launchDiscovery runs no engine at all under --skip-launch. Otherwise it
// first makes sure the agent token is exported (ensureAgentToken: the
// engine's own setup flow on a terminal, a typed refusal off one), and only
// then, on a terminal, pings the selected engine's auth, resolves the discovery session and
// launches it with the setup skill in context via the engine's own raw
// CLI/TUI. The two are separate launches with separate identities: the
// probe's session ends when it answers; the discovery session is the one
// the human works in. A failed ping fails init loud rather than dropping the
// user into a dead vendor-TUI session. A session that fails to resolve,
// launch or ends in error is reported through strictness and refuses by
// default too, so init's own working outcome not happening is never
// mistaken for success.
func launchDiscovery(cmd *cobra.Command, engine, appDir string, interactive bool) error {
	if initSkipLaunch {
		return nil
	}
	if err := ensureAgentToken(cmd.Context(), App().Engines(), engine, interactive, os.LookupEnv, cmd.OutOrStdout()); err != nil {
		// Off a terminal the project is already set up; only the interview's
		// launch is skipped for want of a token. That is a success with a
		// warning naming the token's fix, not a failure.
		if errors.Is(err, ErrAgentTokenNotExported) {
			fix, _ := clifmt.RemedyOf(err)
			clidiag.WarnRemedy("ctxloom", fix, initLaunchSkippedNoTokenFormat, err)
			return nil
		}
		return err
	}
	if !interactive {
		return nil
	}

	workDir := filepath.Dir(appDir)
	deps, err := initLaunchDeps(cmd.Context())
	if err != nil {
		return fmt.Errorf("setup launch: %w", err)
	}
	cfg := deps.Snapshot.Config

	if err := pingEngineAuth(cmd.Context(), deps, engine, workDir); err != nil {
		return err
	}

	fmt.Printf("\nLaunching %s for setup...\n", engine)
	fmt.Println("(Exit the session when done)")
	// Said HERE, immediately before the session that the posture governs,
	// rather than buried in the reentry hint after it: this is the moment

	// The discovery launch pins the engine's default mode — the human
	// answers each gated call — over whatever a label the human has not yet
	// chosen declares: a setup session is not the place to inherit one. It
	// rides the flag, which the engine reads over every declaration.
	src := operations.InternalSource(engine, "", workDir)
	src.Mode = enginepkg.Interactive
	src.Prompt = discoverySessionPrompt(cfg)
	src.Permission = discoveryPosture
	l, err := operations.StartRun(cmd.Context(), deps, sessions.Seed{ProjectDir: workDir}, src)
	if err != nil {
		return reportSetupLaunchFailure(err)
	}
	defer func() {
		_ = launch.Discard(context.Background(), l)
		if eerr := operations.EndSession(l.Identity.Harp, time.Now()); eerr != nil {
			clidiag.Warn("ctxloom", "setup session %s: end: %v", l.Identity.Harp, eerr)
		}
	}()

	if launchErr := launchEngineWithPromptFn(cmd.Context(), deps, workDir, l); launchErr != nil {
		return reportSetupLaunchFailure(launchErr)
	}

	printReentryHint()
	return nil
}

// setupLaunchRemedy is the fix for a setup launch whose error names none of
// its own; a failure that does name one (a delivery refusal, a remediable
// isolation fault) is more specific and wins.
const setupLaunchRemedy = "check the engine's auth/config, then retry `ctxloom init`, or run `ctxloom init prompt` to reconfigure without relaunching"

// reportSetupLaunchFailure reports a setup session that failed to resolve or
// launch: init's own working outcome not happening must not exit clean.
// Reported through strictness rather than a bespoke degraded check here:
// FailOnce streams the warning in BOTH modes (so --degraded still tells the
// user what did not happen), and FindingsError is what turns it fatal in
// strict mode and not under --degraded, matching every other
// refuse-by-default choke without this call site deciding fatality itself.
func reportSetupLaunchFailure(err error) error {
	mark := strictness.Checkpoint()
	defer strictness.Close(mark)
	remedy, ok := clifmt.RemedyOf(err)
	if !ok {
		remedy = setupLaunchRemedy
	}
	strictness.FailOnce(report.KindConfig, remedy, "the setup session failed to launch: %v", err)
	return App().Strictness.FindingsError(mark)
}

// printReentryHint tells the user how to reach ctxloom once the raw-CLI setup
// session has ended: `ctxloom run` (the CLI/TUI) is the primary, working
// outcome of init. Reconfigure any time via `/ctxloom-init` (from any
// session) or `ctxloom init prompt`. Printed once, after the session — init
// then returns and the process exits; there is no relaunch loop.
func printReentryHint() {
	fmt.Println("\nSetup session ended. `ctxloom run` is the primary way to reach ctxloom from here.")
	fmt.Println("Run `/ctxloom-init` from any session (or `ctxloom init prompt`) to reconfigure any time.")
}
