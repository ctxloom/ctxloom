package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/gitignore"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/signing/agentkey"
	"github.com/ctxloom/ctxloom/internal/vpio"
	"github.com/ctxloom/ctxloom/internal/vpio/goplugin"
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

When run interactively (TTY detected), init will guide you through:
  1. Selecting an AI engine (claude-code, antigravity, etc.)
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
  ctxloom init --engine antigravity # Pre-select engine
  ctxloom init --non-interactive   # Skip all prompts`,
	RunE: runInit,
}

var (
	initHome           bool
	initNonInteractive bool
	initSkipLaunch     bool
	initEngine         string
	initRemotes        []string
	initForge          string
)

// initPromptCmd is the real home (Decision 6 of the CLI-primary reorg plan)
// for the setup-interview re-entry pointer: 'ctxloom agent setup' printed the
// whole interview but was misfiled under 'agent' (it configures companions,
// profiles, and agents together, not just agents). RunE is shared with the
// now-Deprecated 'agent setup' alias (agent.go's runSetupPromptCmd) — one
// body, two doors, so they can never drift.
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
	bindInitFlags(initCmd)
	initCmd.AddCommand(initPromptCmd)
}

// bindInitFlags binds the init flag set to cmd. Shared by the top-level `init`
// alias and `manage init` so both surfaces accept the same options against the
// same package-level flag vars (a cobra command has exactly one parent, so the
// alias must be a distinct command sharing the operation, not the *cobra.Command).
func bindInitFlags(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&initHome, "home", false, "Initialize in user home directory instead of current directory")
	cmd.Flags().BoolVar(&initNonInteractive, "non-interactive", false, "Skip interactive prompts (use defaults and flags)")
	cmd.Flags().BoolVar(&initSkipLaunch, "skip-launch", false, "Skip auto-launching the AI after init")
	cmd.Flags().StringVar(&initEngine, "engine", "", "Pre-select AI engine (claude-code, antigravity, etc.)")
	cmd.Flags().StringArrayVar(&initRemotes, "remote", nil, "Personal ctxloom repo to add as a trusted remote — its bundle changes apply without review (owner/repo or URL); repeatable")
	cmd.Flags().StringVar(&initForge, "forge", "", "Bind every --remote to this forge (github, git, or a configured forges: label) instead of resolving by URL host")
}

// isInteractiveTerminal returns true if both stdin and stdout are terminals.
func isInteractiveTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

// stdinIsPiped reports whether stdin is NOT a terminal — i.e. content is being
// piped or redirected in. Used to decide when a command should read its task
// from stdin (e.g. `… | ctxloom run --print`).
func stdinIsPiped() bool {
	return !term.IsTerminal(int(os.Stdin.Fd()))
}

// stderrIsTerminal reports whether stderr is a terminal. Checked before
// writing terminal escape sequences to stderr so `2>log` captures don't
// collect raw escape bytes.
func stderrIsTerminal() bool {
	return term.IsTerminal(int(os.Stderr.Fd()))
}

// stdoutIsTerminal reports whether stdout is a terminal. Checked before
// inserting a pager into `session list --full` / `session query --full`
// output so a redirected or piped invocation (`> file`, `| jq`) never gets
// pager control codes mixed in.
func stdoutIsTerminal() bool {
	return term.IsTerminal(int(os.Stdout.Fd()))
}

// initPrompts handles interactive user prompts during init.
type initPrompts struct {
	reader *bufio.Reader
}

func newInitPrompts() *initPrompts {
	return newInitPromptsFrom(os.Stdin)
}

// newInitPromptsFrom is newInitPrompts reading from an arbitrary r instead of
// os.Stdin directly.
func newInitPromptsFrom(r io.Reader) *initPrompts {
	p := &initPrompts{reader: bufio.NewReader(r)}

	// If stdin is a terminal, save state and ensure canonical mode
	// This handles cases where parent process left terminal in raw mode
	if term.IsTerminal(int(os.Stdin.Fd())) {
		oldState, err := term.GetState(int(os.Stdin.Fd()))
		if err == nil {
			// Restore to cooked mode by making raw then restoring
			// This is a workaround since there's no "MakeCooked" function
			_, _ = term.MakeRaw(int(os.Stdin.Fd()))
			if rerr := term.Restore(int(os.Stdin.Fd()), oldState); rerr != nil {
				clidiag.Warn("ctxloom", "failed to restore terminal state: %v", rerr)
			}
		}
	}

	return p
}

// readCleanLine reads a line and strips terminal escape sequences and control chars.
// This handles focus events (^[[I, ^[[O), cursor movements, etc.
func (p *initPrompts) readCleanLine() (string, error) {
	input, err := p.reader.ReadString('\n')
	if err != nil {
		return "", err
	}

	// Strip CSI escape sequences, keep only printable ASCII.
	var clean strings.Builder
	for i := 0; i < len(input); {
		if isCSIStart(input, i) {
			i = skipCSISequence(input, i)
			continue
		}
		if isPrintableASCII(input[i]) {
			clean.WriteByte(input[i])
		}
		i++
	}

	return strings.TrimSpace(clean.String()), nil
}

// isCSIStart reports whether a CSI escape (ESC '[') begins at input[i].
func isCSIStart(input string, i int) bool {
	return input[i] == '\x1b' && i+1 < len(input) && input[i+1] == '['
}

// skipCSISequence returns the index just past the CSI sequence starting at i
// (which points at the ESC). CSI sequences end with a final byte 0x40–0x7e.
func skipCSISequence(input string, i int) int {
	i += 2 // past ESC '['
	for i < len(input) {
		c := input[i]
		i++
		if c >= 0x40 && c <= 0x7e {
			break
		}
	}
	return i
}

// isPrintableASCII reports whether b is a printable ASCII byte.
func isPrintableASCII(b byte) bool {
	return b >= 0x20 && b <= 0x7e
}

// primaryEngines are shown first in the selection menu (curated list).
var primaryEngines = []string{"claude-code", "antigravity"}

// getAvailableEngines returns engines filtered by what's actually installed.
// Primary engines come first, then secondary engines, all sorted.
func getAvailableEngines() (primary, secondary []string) {
	primarySet := make(map[string]bool)
	for _, e := range primaryEngines {
		primarySet[e] = true
	}

	// Check which primary engines are available
	for _, name := range primaryEngines {
		if backends.IsAvailable(name) {
			primary = append(primary, name)
		}
	}

	// Get secondary engines (all others except mock)
	for _, name := range backends.List() {
		if name == "mock" || primarySet[name] {
			continue
		}
		if backends.IsAvailable(name) {
			secondary = append(secondary, name)
		}
	}

	// Sort secondary for consistent ordering
	sort.Strings(secondary)
	return primary, secondary
}

// errNoEngines is returned when no AI engines are installed.
var errNoEngines = fmt.Errorf("no AI engines installed")

// promptEngineSelection prompts the user to select an AI engine.
// Returns the selected engine name, or the default if only one is available.
func (p *initPrompts) promptEngineSelection() (string, error) {
	primary, secondary := getAvailableEngines()

	switch len(primary) + len(secondary) {
	case 0:
		return "", errNoEngines
	case 1:
		return selectSoleEngine(primary, secondary), nil
	}

	maxOption := printEngineMenu(primary, secondary)
	return p.readEngineChoice(primary, secondary, maxOption)
}

// selectSoleEngine returns (and announces) the only available engine. Exactly
// one of primary/secondary is non-empty here (the sole-engine switch case), so
// the primary list is consulted before indexing secondary — indexing secondary
// unconditionally panics when the only engine is a primary one.
func selectSoleEngine(primary, secondary []string) string {
	var engine string
	if len(primary) > 0 {
		engine = primary[0]
	} else {
		engine = secondary[0]
	}
	fmt.Printf("\nUsing %s (only available engine)\n", engine)
	return engine
}

// printEngineMenu prints the numbered engine menu (with a "more options" entry
// when secondary engines exist) and returns the highest valid option number.
func printEngineMenu(primary, secondary []string) int {
	fmt.Println("\nSelect your AI engine (press Enter for recommended):")
	for i, engine := range primary {
		label := engine
		if i == 0 {
			label += " (Recommended)"
		}
		fmt.Printf("  %d) %s\n", i+1, label)
	}

	maxOption := len(primary)
	if len(secondary) > 0 {
		fmt.Printf("  %d) more options...\n", len(primary)+1)
		maxOption++
	}
	return maxOption
}

// readEngineChoice loops on input until a valid selection is made: empty picks
// the recommended (first primary), a primary number picks that engine, and the
// "more options" entry shows the full list.
func (p *initPrompts) readEngineChoice(primary, secondary []string, maxOption int) (string, error) {
	for {
		fmt.Print("\n> ")
		input, err := p.readCleanLine()
		if err != nil {
			return "", err
		}

		if input == "" {
			// Enter selects the recommended (first primary). With zero primary
			// engines (only secondaries registered) there is nothing to
			// recommend, so fall through to the full list instead of indexing
			// primary[0] — a latent index-out-of-range panic in init.
			if len(primary) == 0 {
				return p.promptAllEngines(primary, secondary)
			}
			return primary[0], nil
		}

		num, err := strconv.Atoi(input)
		if err != nil || num < 1 || num > maxOption {
			fmt.Printf("Please enter a number between 1 and %d, or press Enter for recommended\n", maxOption)
			continue
		}

		if num <= len(primary) {
			return primary[num-1], nil
		}
		return p.promptAllEngines(primary, secondary)
	}
}

// promptAllEngines shows all available engines.
func (p *initPrompts) promptAllEngines(primary, secondary []string) (string, error) {
	allEngines := append(primary, secondary...)

	fmt.Println("\nAll installed engines:")
	for i, engine := range allEngines {
		fmt.Printf("  %d) %s\n", i+1, engine)
	}

	for {
		fmt.Print("\n> ")
		input, err := p.readCleanLine()
		if err != nil {
			return "", err
		}

		if input == "" {
			continue
		}

		num, err := strconv.Atoi(input)
		if err != nil || num < 1 || num > len(allEngines) {
			fmt.Printf("Please enter a number between 1 and %d\n", len(allEngines))
			continue
		}

		return allEngines[num-1], nil
	}
}

// promptPersonalRepos optionally asks for one or more personal ctxloom repos.
// Returns the repos in entry order; an empty slice if the user has none.
// The trust consequence is stated BEFORE entry, not after: adding a repo here
// marks it trusted, and the user must know that while deciding what to type.
func (p *initPrompts) promptPersonalRepos() ([]string, error) {
	fmt.Print("\nDo you have any personal ctxloom repositories? (y/N): ")
	input, err := p.readCleanLine()
	if err != nil {
		return nil, err
	}

	input = strings.ToLower(input)
	if input != "y" && input != "yes" {
		return nil, nil
	}

	fmt.Println("Repos you add here are addresses only — their content takes the review path")
	fmt.Println("('ctxloom review') until you sign your bundles and trust your own signing key.")
	fmt.Println("Enter GitHub repos (e.g., 'myuser/ctxloom-profiles'), one per line. Blank line when done.")
	var repos []string
	for {
		fmt.Printf("  repo %d (blank to finish): ", len(repos)+1)
		repo, err := p.readCleanLine()
		if err != nil {
			return repos, err
		}
		if repo == "" {
			break
		}
		repos = append(repos, repo)
	}

	return repos, nil
}

// dirtyTreeHandlerOption is one menu entry promptDirtyTreeHandler presents:
// value is the literal config value written to dirty_tree_handler, label is
// the full line shown for it (already including its trailing consequence
// text, so the loop below only needs to number and print each one).
type dirtyTreeHandlerOption struct {
	value, label string
}

// dirtyTreeHandlerOptions is promptDirtyTreeHandler's menu, in display order.
// Index 0 ("commit") is both the built-in default (operations package's
// defaultDirtyTreeHandler) and what a bare Enter picks, mirroring
// promptEngineSelection's "Enter for recommended" convention. Wording must
// stay in lockstep with the real refusal/warning text a mismatched choice
// would later hit — see internal/operations/delegate.go's
// dirtyTreeFailError/commitDirtyTree/handleDirtyParentTree "stale" branch.
var dirtyTreeHandlerOptions = []dirtyTreeHandlerOption{
	{
		value: "commit",
		label: "commit — ctxloom may commit your uncommitted changes onto your current branch on your behalf, so the child can see them (each such commit is announced when it happens) (Recommended)",
	},
	{
		value: "copy",
		label: "copy — reproduce your uncommitted changes inside the child's own worktree as uncommitted WIP; nothing on your branch is ever touched",
	},
	{
		value: "stale",
		label: "stale — the child sees only your last commit; your uncommitted work stays invisible to it",
	},
	{
		value: "fail",
		label: "fail — refuse the delegation instead of guessing",
	},
}

// promptDirtyTreeHandler asks the interview's ONE dirty-tree question and
// returns both the dirty_tree_handler value and the dirty_tree_commit_ack it
// implies. This is deliberately a single decision, not two: "may ctxloom
// commit your uncommitted work on your behalf" and "what should happen when
// your tree is dirty" are the SAME choice among commit/copy/stale/fail, and
// asking both would just rubber-stamp the first with the second (see this
// task's brief). ack is true if and only if the chosen handler is "commit" —
// every other handler never mutates the user's repo and needs no
// acknowledgement (config.Config.dirtyTreeCommitAck's doc).
//
// This runs entirely in THIS process, reading raw keystrokes off the user's
// own terminal via readCleanLine — never through an engine session or any
// agent-mediated surface — which is what keeps the resulting ack
// unambiguously human-set, the same property promptPersonalRepos' trust
// consequence relies on.
func (p *initPrompts) promptDirtyTreeHandler() (handler string, ack bool, err error) {
	fmt.Println("\nDelegated agents you launch with agent_run run in an isolated worktree,")
	fmt.Println("which by default can only see your COMMITTED work — nothing you haven't")
	fmt.Println("committed yet. What should happen when your tree has uncommitted changes")
	fmt.Println("at delegation time?")
	fmt.Println()
	for i, opt := range dirtyTreeHandlerOptions {
		fmt.Printf("  %d) %s\n", i+1, opt.label)
	}

	for {
		fmt.Print("\n> (1-4, Enter for recommended): ")
		input, err := p.readCleanLine()
		if err != nil {
			return "", false, err
		}

		var opt dirtyTreeHandlerOption
		if input == "" {
			opt = dirtyTreeHandlerOptions[0]
		} else {
			num, convErr := strconv.Atoi(input)
			if convErr != nil || num < 1 || num > len(dirtyTreeHandlerOptions) {
				fmt.Printf("Please enter a number between 1 and %d, or press Enter for recommended\n", len(dirtyTreeHandlerOptions))
				continue
			}
			opt = dirtyTreeHandlerOptions[num-1]
		}
		return opt.value, opt.value == "commit", nil
	}
}

// discoverySessionPrompt returns the ONE prompt the init discovery session
// receives: ctxloom's built-in five-phase setup body (ctxloomInitPrompt, see
// agent.go) — orient+scan, companions, profiles+content, agents, close — so
// content selection and agent binding happen in a single continuous
// conversation (no mid-session prompt fetch). ACP is deliberately not one of
// the five phases (it is optional, handed off to the acp-setup skill), so
// this discovery session reaches a working CLI/TUI outcome without ever
// gating on it. Resolves through ResolveSetupPrompt so every bundle- or
// companion-shipped `agent-setup` command is composed in at init exactly as
// it is for `ctxloom init prompt` and for the `/ctxloom-init` slash command;
// a nil config degrades to the built-in text alone (CLAUDE.md fault
// tolerance).
func discoverySessionPrompt(cfg *config.Config) string {
	if cfg == nil {
		return ctxloomInitPrompt
	}
	return operations.ResolveSetupPrompt(cfg, ctxloomInitPrompt)
}

// launchEngineWithPrompt starts the engine's own raw CLI/TUI with the merged
// setup-skill prompt (pty passthrough — the vendor's real interactive binary
// on this terminal, exactly as `ctxloom run`'s interactive path). Errors
// (failed launch, errored session) are returned for the caller to degrade on
// — a session ending badly (an interrupted setup, a crashed engine) warns and
// init still exits cleanly; there is no relaunch loop or review offer to gate
// on a clean return anymore (both deleted — init hands off once and is done).
func launchEngineWithPrompt(ctx context.Context, engine, workDir string) error {
	client, err := pb.NewSelfInvokingClientForLabel(engine, "", 0)
	if err != nil {
		return fmt.Errorf("failed to launch %s: %w", engine, err)
	}
	defer client.Kill()

	// Config is best-effort here: it only feeds the bundle-override lookup for
	// the agent-setup half, and init just wrote it — but a load failure must
	// not sink the discovery launch.
	var cfg *config.Config
	if c, cerr := GetConfig(); cerr == nil {
		cfg = c
	}

	// No PermissionMode is set: the zero value rides the wire as "" and
	// resolves (agent.WireMode) to PermissionDefault — the engine's OWN normal
	// in-tool approval prompting, not another bypass. This session runs
	// entirely inside the vendor's raw CLI/TUI (init's whole reason for
	// launching it this way), so the vendor TUI's native edit-approval prompts
	// ARE the consent surface for whatever the setup skill's tool calls (incl.
	// client-config writes, §6) attempt — exactly the right gate, and the same
	// one the user gets in every other engine session.
	req := &pb.RunStart{
		Prompt: &pb.Fragment{Content: discoverySessionPrompt(cfg)},
		Options: &pb.RunOptions{
			WorkDir: workDir,
			Mode:    pb.ExecutionMode_INTERACTIVE,
		},
	}

	// The discovery session is interactive, so the frontend must own the
	// terminal exactly as `ctxloom run` does: raw-mode keystrokes and resize
	// events are pumped over the bidi Run stream into the agent's pty (the
	// plugin subprocess never inherits our terminal). Off a TTY this degrades
	// to a non-interactive stream — warn and continue (CLAUDE.md).
	stdin, resize, restoreTerm := interactiveTerminal(ctx)
	defer restoreTerm()
	if stdin == nil {
		clidiag.Warn("ctxloom", "stdin is not a terminal; discovery session will not accept input")
	}

	// Restore the terminal before dying on an interrupt delivered from
	// outside (in raw mode a user's ^C is just bytes forwarded to the agent,
	// not a SIGINT to us). restoreTerm is idempotent, so this races safely
	// with the deferred and inline calls.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	go func() {
		<-sigCh
		restoreTerm()
		// Re-raise signal for default handling
		signal.Stop(sigCh)
		p, _ := os.FindProcess(os.Getpid())
		_ = p.Signal(os.Interrupt)
	}()
	defer signal.Stop(sigCh)

	// Run the discovery session over the vpio seam (internal/vpio) — the same
	// go-plugin-wrapping goplugin.Launcher `ctxloom run`'s interactive path
	// uses, so both callers share one transport implementation.
	session, err := goplugin.NewLauncher(client, req).Start(ctx, vpio.ProcessSpec{
		Stdin:  stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	})
	if err != nil {
		return fmt.Errorf("AI session failed to start: %w", err)
	}
	pumpResize(session, resize)
	_, err = session.Wait()
	restoreTerm()
	if err != nil {
		return fmt.Errorf("AI session ended: %w", err)
	}

	return nil
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
		selectedEngine = engineFromExistingConfig(selectedEngine)
		// U036-F04: --remote/--forge were silently ignored here — the only
		// consumer of initRemotes/initForge (addPersonalRemotes) lived
		// exclusively inside setupNewCtxloomDir's fresh-init branch below,
		// so `ctxloom init --remote <repo>` against an existing .ctxloom
		// exited 0, printed "ctxloom directory already exists", and added
		// zero remotes. Honour the flags here too, on a pre-existing dir.
		addPersonalRemotesFn(cmd, initRemotes, initForge)
	} else {
		selectedEngine, err = setupNewCtxloomDir(cmd, appDir, selectedEngine, interactive)
		if err != nil {
			return err
		}
	}

	// Ensure a concrete engine for the launch step (covers existing dirs whose
	// config did not name one).
	primary, _ := getAvailableEngines()
	selectedEngine = pickDefaultEngine(selectedEngine, primary)

	return launchDiscovery(cmd, selectedEngine, appDir, interactive)
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

// engineFromExistingConfig returns the engine recorded in an existing config,
// falling back to current when the config is unreadable or names none.
func engineFromExistingConfig(current string) string {
	if cfg, err := config.Load(); err == nil {
		if backend := cfg.GetDefaultLLM(); backend != "" {
			return backend
		}
	}
	return current
}

// pickDefaultEngine resolves the engine to use: an explicit selection wins;
// otherwise the first available primary engine; otherwise "claude-code" so init
// never dead-ends without an engine.
func pickDefaultEngine(selected string, primary []string) string {
	if selected != "" {
		return selected
	}
	if len(primary) > 0 {
		return primary[0]
	}
	return "claude-code"
}

// checkSystemDeps is PRIME's targeted, deterministic system-dependency gate —
// NOT `ctxloom doctor` (which comprehensively checks engines/agents/hooks/
// trust; overkill and partly irrelevant this early). This just asks "does the
// machine have what THIS call is about to need."
//
// git is a HARD BLOCK: cloneConfiguredRemotes (called right after this,
// within the same setupNewCtxloomDir call) shells out to git to clone the
// seeded remote, and worktree isolation shells out to it later still — a
// git-less machine cannot complete init's own deterministic steps. Failing
// loud here, with a named fix, beats letting the clone step surface a raw
// "executable file not found" error with no guidance.
//
// ssh-keygen and a container runtime (needed later for containerized
// agents) are INFORMATIONAL ONLY: nothing PRIME itself does needs them yet,
// so their absence surfaces as a warning, not a block. ssh-keygen is NOT a
// signing dependency — an earlier version of this warning wrongly implied
// it was; ctxloom's signing is pure Go over the ssh-agent protocol
// (internal/signing/agentkey/agentkey.go, internal/signing/sign.go — never
// shells out to ssh-keygen) — it is only useful, by hand, to GENERATE a new
// key if you don't already have one (`ssh-keygen -t ed25519-sk`).
//
// engine is the resolved backend this init is configuring (e.g.
// "claude-code", "codex") — used only to know which ACP adapter, if any,
// warnIfACPAdapterMissing should check for.
//
// A sibling slice adds git to `ctxloom doctor`'s own comprehensive dependency
// check on a separate, unmerged branch; the couple of lines of overlap
// between that comprehensive report and this narrow up-front gate are
// intentional (they serve different moments — doctor is a health report,
// this is a "can PRIME even proceed" gate), not something to fold into a
// shared helper here.
func checkSystemDeps(engine string) error {
	if _, err := exec.LookPath("git"); err != nil {
		return fmt.Errorf("git is required (ctxloom is about to clone/pull remote content, and worktree isolation shells out to it later) but was not found on PATH — install it (e.g. `apt install git`, `brew install git`, `winget install Git.Git`) and re-run `ctxloom init`")
	}

	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		clidiag.Warn("ctxloom", "ssh-keygen not found on PATH — recommended, not required (`ctxloom sign` itself is pure Go and never execs ssh-keygen): it's the tool you'd run by hand to generate a new SSH key if you don't already have one to sign with (`ssh-keygen -t ed25519-sk`)")
	}
	warnIfNoSignKey()
	warnIfGitIdentityMissing()
	warnIfACPAdapterMissing(engine)
	if !(isolation.Docker{}.Available()) && !(isolation.Podman{}.Available()) {
		clidiag.Warn("ctxloom", "no container runtime detected (docker/podman) — you'll need one later to run containerized agents")
	}
	return nil
}

// warnIfNoSignKey is checkSystemDeps' companion to the ssh-keygen PATH probe
// above: even with ssh-keygen present, a resolvable signing IDENTITY is
// needed both to approve reviewed content (`ctxloom review` countersigns an
// approval with it — spec §9.5, and review is a normal part of ordinary
// setup, not a publishing-only step) and to publish or sign your own content
// (`ctxloom sign`). It runs the SAME resolver both of those use
// (internal/signing/agentkey.Discoverer.Discover — see review.go's
// resolveReviewSigner and sign.go's runSign) and reuses
// signKeyResolutionDetail (doctor_cmd.go) so this warn says the exact same
// thing `ctxloom doctor --deps`'s DOCTOR-CHECK-SIGNKEY-k1 check reports —
// one resolver, one message, two surfaces. explicit is always "" here: a
// brand-new init has no sign.key configured yet, so this checks the
// zero-config chain (git config user.signingkey, then ssh-agent's sole
// identity) exactly as `ctxloom review`/`ctxloom sign` would try it today.
// Informational only, like ssh-keygen/container-runtime above — never blocks
// init: a project that only ever consumes already-trusted/embedded content
// has nothing to approve and genuinely needs no key.
func warnIfNoSignKey() {
	ok, detail := signKeyResolutionDetail(context.Background(), agentkey.NewDiscoverer(), "")
	if !ok {
		clidiag.Warn("ctxloom", "%s", detail)
	}
}

// warnIfGitIdentityMissing is checkSystemDeps' companion probe for git's
// commit identity (user.name/user.email), same shape and posture as
// warnIfNoSignKey above: informational only, reusing the SAME shared
// gitIdentityDetail (doctor_cmd.go) and the SAME `git config --get` reader
// (internal/signing/agentkey.Discoverer.GitConfig, defaulted by
// agentkey.NewDiscoverer()) that DOCTOR-CHECK-GITIDENT-l2 uses, so this warn
// says the exact same thing `ctxloom doctor --deps` reports. Agents ctxloom
// launches commit their own work inside isolated worktrees (internal/lm/
// isolation/worktree.go's teardown), so an incomplete identity here can
// surface later as a failed or mis-attributed commit deep inside a run —
// surfacing it at init time, before that happens, beats discovering it then.
func warnIfGitIdentityMissing() {
	ok, detail := gitIdentityDetail(context.Background(), agentkey.NewDiscoverer().GitConfig)
	if !ok {
		clidiag.Warn("ctxloom", "%s", detail)
	}
}

// warnIfACPAdapterMissing is checkSystemDeps' companion probe for the ACP
// adapter binary (claude-code-acp/codex-acp) the resolved engine's
// HOST-runtime structured chat needs (see DOCTOR-CHECK-ACPADAPTER-m3,
// doctor_cmd.go, for the full rationale) — reusing the SAME shared
// acpAdapterDetail (doctor_cmd.go) so this warn says the exact same thing
// `ctxloom doctor --deps` reports. Informational only, like the other warns
// beside it: the raw-CLI bootstrap interview this gate protects never
// touches structured chat, so a missing adapter here is a heads-up for
// LATER (agent_run cross-engine delegation, `ctxloom acp client`), not a
// block on init completing now — nothing about ACP (server or client) is
// ever a hard requirement for init; see the acp-setup skill for that
// separate, optional configuration — and it's a non-issue entirely for an
// agent that ends up configured with runtime: container, whose image
// carries its own adapter (the detail text says so).
func warnIfACPAdapterMissing(engine string) {
	ok, detail := acpAdapterDetail([]string{engine})
	if !ok {
		clidiag.Warn("ctxloom", "%s", detail)
	}
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
	addPersonalRemotesFn(cmd, append(append([]string{}, initRemotes...), personalRepos...), initForge)
	cloneConfiguredRemotes(cmd)
	pullSeededDependencies(cmd)
	applyInitHooks(cmd)

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

// noEnginesInstalled reports whether neither a primary nor a secondary engine
// is available.
func noEnginesInstalled() bool {
	primary, secondary := getAvailableEngines()
	return len(primary) == 0 && len(secondary) == 0
}

// warnNoEnginesDetected prints install guidance to stderr. Init continues with a
// placeholder engine (fault tolerant).
func warnNoEnginesDetected() {
	clidiag.Warn("ctxloom", "no AI engines detected")
	fmt.Fprintln(os.Stderr, "Install one of the following to use ctxloom:")
	fmt.Fprintln(os.Stderr, "  claude-code:  npm install -g @anthropic-ai/claude-code")
	fmt.Fprintln(os.Stderr, "  antigravity:  curl -fsSL https://antigravity.google/cli/install.sh | bash")
	fmt.Fprintln(os.Stderr, "  codex:        npm install -g @openai/codex")
	fmt.Fprintln(os.Stderr, "")
}

// promptForEngineAndRepos runs the interactive engine selection, optional
// personal-repo, and dirty-tree-handler prompts. errNoEngines propagates (the
// prompt already explained it); other prompt failures warn and fall back
// rather than aborting init. A failed dirty-tree prompt falls back to
// ""/false — the same silent-default shape init had before this question
// existed (built-in "commit" default, unacknowledged).
func promptForEngineAndRepos() (engine string, repos []string, dirtyTreeHandler string, dirtyTreeCommitAck bool, err error) {
	prompts := newInitPrompts()

	engine, err = prompts.promptEngineSelection()
	if err != nil {
		if err == errNoEngines {
			return "", nil, "", false, err
		}
		clidiag.Warn("ctxloom", "failed to read engine selection: %v", err)
		engine = "claude-code"
	}

	repos, repoErr := prompts.promptPersonalRepos()
	if repoErr != nil {
		clidiag.Warn("ctxloom", "failed to read repo selection: %v", repoErr)
		repos = nil
	}

	dirtyTreeHandler, dirtyTreeCommitAck, dtErr := prompts.promptDirtyTreeHandler()
	if dtErr != nil {
		clidiag.Warn("ctxloom", "failed to read dirty-tree handler selection: %v", dtErr)
		dirtyTreeHandler, dirtyTreeCommitAck = "", false
	}

	return engine, repos, dirtyTreeHandler, dirtyTreeCommitAck, nil
}

// writeInitialConfig delegates project bootstrap (the .ctxloom skeleton +
// config.yaml + default remotes.yaml) to the operations core.
//
// It EXPLICITLY invalidates the ambient config memo on success, rather than
// relying on the memo's own stat-based self-correction (config.Load's mtime
// +size check): the rest of init (addPersonalRemotes, cloneConfiguredRemotes,
// pullSeededDependencies, applyInitHooks) all read the config right back via
// the bare, memoized config.Load() in the SAME process, and a stat check has
// only mtime+size granularity to key on — theoretically indistinguishable
// from the pre-write state on a filesystem coarse enough, or if a PRIOR
// config.Load() in this same init run (e.g. engineFromExistingConfig probing
// for a pre-existing config before this write happens) already memoized a
// "missing" stamp whose invalidation this write's own stat SHOULD, but need
// not provably, trigger. Invalidate() removes that dependency: the very next
// Load anywhere in the process re-reads from disk unconditionally.
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

// addPersonalRemotes registers the user's personal repos. Failures warn and
// continue (an unknown --forge label rolls that single remote back).
// addPersonalRemotesFn is a package var seam over addPersonalRemotes: tests
// stub it to verify runInit's/setupNewCtxloomDir's branching reaches it (with
// the right args) without exercising the real remote-add machinery (registry
// + network fetch/clone). Defaults to the real function.
var addPersonalRemotesFn = addPersonalRemotes

func addPersonalRemotes(cmd *cobra.Command, repos []string, forge string) {
	if len(repos) == 0 {
		return
	}
	cfg, loadErr := config.Load()
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

// cloneConfiguredRemotes eagerly clones every configured remote so discovery
// (search_library, browse) can read them offline. Fault-tolerant: per-remote
// failures warn and continue.
func cloneConfiguredRemotes(cmd *cobra.Command) {
	cfg, loadErr := config.Load()
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

// pullSeededDependencies pulls and locks the remote dependencies the fresh
// config references — most importantly the seeded default profile, which
// resolves only through a lockfile entry. Without this step the very first
// `ctxloom run` after init fails to assemble. Fault-tolerant: failures warn
// and continue (offline init still completes; run degrades per assembly).
func pullSeededDependencies(cmd *cobra.Command) {
	cfg, err := config.Load()
	if err != nil {
		clidiag.Warn("ctxloom", "failed to load config for dependency pull: %v", err)
		return
	}
	result, syncErr := operations.SyncDependencies(cmd.Context(), cfg, operations.SyncDependenciesRequest{
		Lock:       true,
		ApplyHooks: false, // applyInitHooks runs right after
	})
	if syncErr != nil {
		clidiag.Warn("ctxloom", "failed to pull seeded dependencies: %v", syncErr)
		return
	}
	if result.Installed > 0 {
		fmt.Printf("Pulled %d seeded dependencies\n", result.Installed)
	}
}

// applyInitHooks registers the ctxloom MCP server with every backend. Failures
// warn and continue (fault tolerant).
func applyInitHooks(cmd *cobra.Command) {
	cfg, err := config.Load()
	if err != nil {
		clidiag.Warn("ctxloom", "failed to load config: %v", err)
		return
	}
	result, applyErr := operations.ApplyHooks(context.Background(), cfg, operations.ApplyHooksRequest{
		Backend:           "all",
		RegenerateContext: false,
	})
	if applyErr != nil {
		clidiag.Warn("ctxloom", "failed to apply hooks: %v", applyErr)
		return
	}
	fmt.Printf("Applied hooks for: %v\n", result.Backends)
}

// authPingTask is the smallest possible prompt sent to probe the selected
// engine's auth before init hands off to its raw CLI/TUI — just enough to
// prove a live, authenticated round trip happened.
const authPingTask = "Reply with exactly: ok"

// engineAuthFix names, per engine, the fix for a failed auth probe: the
// subscription-login and API-key-env paths each backend actually offers
// (internal/lm/isolation/auth.go's envTrigger constants are the verified
// source for the env var names). Keyed by backend name (backends.List()); an
// engine this map doesn't (yet) name gets engineAuthFixHint's generic
// fallback rather than blocking on a missing entry.
var engineAuthFix = map[string]string{
	"claude-code": "run `claude login` (or set ANTHROPIC_API_KEY)",
	"antigravity": "run `agy` and complete its sign-in flow (OAuth only — no API-key path)",
	"codex":       "run `codex login` (or set OPENAI_API_KEY)",
	"kiro":        "run `kiro-cli login` (or set KIRO_API_KEY)",
	"opencode":    "authenticate opencode (see its `auth` subcommand) or set OPENROUTER_API_KEY",
}

// engineAuthFixHint returns engine's named fix, or a generic fallback for an
// engine not (yet) in engineAuthFix.
func engineAuthFixHint(engine string) string {
	if fix, ok := engineAuthFix[engine]; ok {
		return fix
	}
	return "authenticate the engine (subscription login or its API-key env var) and try again"
}

// authPingFactory is a test seam: nil self-invokes the compiled-in backend
// (operations.RunOneshot's normal path); tests inject a stub pb.ClientFactory
// so no real engine binary or credential is required to exercise the gate.
var authPingFactory pb.ClientFactory

// pingEngineAuth probes the selected engine with the smallest possible
// oneshot run BEFORE init hands off to its raw CLI/TUI (§12 Q1, user-approved:
// "a dead first session inside a vendor TUI is invisible failure; catch it in
// code"). It reuses operations.RunOneshot — the SAME oneshot Execute surface
// `ctxloom run --print` and map/weave already ride — rather than a bespoke
// engine-ping path: no explicit profile (falls back to the configured
// defaults, which is a fault-tolerant no-op if none resolve) and the smallest
// possible task. Any failure (missing binary, no credentials, a dead
// subscription token, a real backend error) fails loud, naming the fix for
// THIS engine; auth itself stays ambient — this is a liveness gate, not a
// login flow.
func pingEngineAuth(ctx context.Context, cfg *config.Config, engine, workDir string) error {
	_, err := operations.RunOneshot(ctx, cfg, operations.RunOneshotRequest{
		Task:    authPingTask,
		LLM:     engine,
		WorkDir: workDir,
		Factory: authPingFactory,
	})
	if err != nil {
		return fmt.Errorf("%s isn't ready to launch (auth check failed: %v) — %s", engine, err, engineAuthFixHint(engine))
	}
	return nil
}

// launchEngineWithPromptFn is a package var seam over launchEngineWithPrompt:
// tests stub it to verify launchDiscovery's branching (the ping gates the
// launch; a successful ping proceeds to it) without spawning a real engine
// subprocess. Defaults to the real function.
var launchEngineWithPromptFn = launchEngineWithPrompt

// launchDiscovery pings the selected engine's auth, then — unless
// --skip-launch or non-interactive — launches it with the setup skill in
// context via its own raw CLI/TUI (launchEngineWithPrompt). A failed ping
// fails init loud (returned as an error) rather than dropping the user into a
// dead vendor-TUI session; a session that starts but ends in error (an
// interrupted setup, a crashed engine) only warns — init still hands off and
// exits cleanly. There is no relaunch loop and no review offer afterward
// (both deleted: re-entry is `/ctxloom-init` or `ctxloom init prompt`, and
// review is the setup skill's own phase 3d/5).
func launchDiscovery(cmd *cobra.Command, engine, appDir string, interactive bool) error {
	if !interactive || initSkipLaunch {
		return nil
	}

	workDir := filepath.Dir(appDir)

	// Config is best-effort here (same pattern as launchEngineWithPrompt): a
	// load failure must not block the ping — RunOneshot degrades a nil config
	// to no configured-defaults profile, which is itself fault-tolerant.
	var cfg *config.Config
	if c, cerr := GetConfig(); cerr == nil {
		cfg = c
	}
	if err := pingEngineAuth(cmd.Context(), cfg, engine, workDir); err != nil {
		return err
	}

	fmt.Printf("\nLaunching %s for setup...\n", engine)
	fmt.Println("(Exit the session when done)")
	fmt.Println()

	if launchErr := launchEngineWithPromptFn(cmd.Context(), engine, workDir); launchErr != nil {
		clidiag.Warn("ctxloom", "%v", launchErr)
		return nil
	}

	printReentryHint()
	return nil
}

// printReentryHint tells the user how to reach ctxloom once the raw-CLI setup
// session has ended: `ctxloom run` (the CLI/TUI) is the primary, working
// outcome of init — no ACP client is required. Reconfigure any time via
// `/ctxloom-init` (from any session) or `ctxloom init prompt`; add optional
// ACP editor integration any time via the acp-setup skill. Printed once,
// after the session — init then returns and the process exits; there is no
// relaunch loop.
func printReentryHint() {
	fmt.Println("\nSetup session ended. `ctxloom run` is the primary way to reach ctxloom from here.")
	fmt.Println("Want an editor's AI panel too (Zed, VSCode, ...)? Invoke the acp-setup skill any time.")
	fmt.Println("Run `/ctxloom-init` from any session (or `ctxloom init prompt`) to reconfigure any time.")
}
