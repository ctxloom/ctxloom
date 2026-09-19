// The hand-off half of `ctxloom init`: the setup prompt every door emits, the
// engine auth liveness probe that gates the launch, and the raw CLI/TUI
// discovery session itself.

package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/vpio"
	"github.com/ctxloom/ctxloom/internal/vpio/goplugin"
)

// discoverySessionPrompt returns the ONE prompt every setup door emits:
// ctxloom's built-in five-phase setup body (ctxloomInitPrompt, see agent.go) —
// orient+scan, companions, profiles+content, agents, close — so content
// selection and agent binding happen in a single continuous conversation (no
// mid-session prompt fetch). Resolves through ResolveSetupPrompt so every bundle- or companion-shipped
// `agent-setup` command is composed in identically for the init discovery
// launch, for `ctxloom init prompt`, and for the `/ctxloom-init` slash
// command. A nil config degrades to the built-in text alone (CLAUDE.md fault
// tolerance) — which is also the path a config LOAD FAILURE takes, since
// GetConfig returns a nil config on error.
func discoverySessionPrompt(cfg *config.Config) string {
	if cfg == nil {
		return ctxloomInitPrompt
	}
	return operations.ResolveSetupPrompt(cfg, ctxloomInitPrompt)
}

// discoveryRunRequest builds the wire request for the init discovery launch —
// the interactive setup session `ctxloom init` hands off to. Extracted from
// launchEngineWithPrompt so the request's payload (prompt, mode, and above all
// its permission posture) is assertable without spawning an engine subprocess,
// exactly as the auth ping's posture is pinned through operations.RunOneshot.
//
// The posture is PermissionDefault unless THIS PROJECT DIRECTORY declared its
// own, and either way it is SAID. The pinned default is the engine's OWN normal
// in-tool approval prompting, not another bypass: this session runs entirely
// inside the vendor's raw CLI/TUI (init's whole reason for launching it this
// way), so the vendor TUI's native edit-approval prompts ARE the consent
// surface for whatever the setup skill's tool calls (incl. client-config
// writes, §6) attempt — exactly the right gate, and the same one the user gets
// in every other engine session.
//
// It is spelled out rather than left at the zero value. Both reach the same
// posture (agent.WireMode maps "" and "default" alike to PermissionDefault),
// so nothing about an undeclared launch changes — but an unset field is a
// posture NOBODY DECLARED, indistinguishable from a caller that never
// considered the question, and it is the same silent fall-through the auth
// ping's explicit bypass closed on init's other launch site.
//
// EXACTLY ONE resolution rung runs here, and the asymmetry with `ctxloom run`
// (resolvePermissionMode: flag > agent binding > llm label > project default >
// backend built-in, which is BYPASS for claude-code on the host) is the point.
// The discovery launch consults the PROJECT DEFAULT and nothing else — not the
// label, not a binding, not the host stopgap. A setup session is not the place
// to inherit a host-wide bypass, nor a posture attached to some engine label
// the human has not yet chosen; but a human who wrote `permissions:` into THIS
// directory's config has decided, for this directory, what an agent here may
// do, and honouring that is not inheritance. Pinning `default` over their own
// declaration would also make init lie: setup would prompt for everything and
// the very next `ctxloom run` in the same directory would not.
// harp is this run's session name, minted by the caller via
// operations.AssignSession before this function runs (the same primitive
// `ctxloom run`'s openSession uses) — never "" on a real launch. It rides on
// Options.Env under agent.SessionHarpEnv, the SAME env key every other
// consumer of session identity already reads (BaseBackend.run stamps
// LaunchSpec.Harp from exactly this key; internal/lm/grpc/chat.go's transcript
// capture reads it the same way), so this is the existing wire path, not a
// new one. Without it, panelaunch.runInteractiveInPane's pane-hosted launch
// refuses outright ("interactive launch requires a named session") because a
// tmux pane is addressed by harp and an empty one addresses none.
func discoveryRunRequest(cfg *config.Config, workDir, harp string) *pb.RunStart {
	posture, _ := discoveryPermissionMode(cfg)
	return &pb.RunStart{
		Prompt: &pb.Fragment{Content: discoverySessionPrompt(cfg)},
		Options: &pb.RunOptions{
			WorkDir:        workDir,
			Mode:           pb.ExecutionMode_INTERACTIVE,
			PermissionMode: posture.String(),
			Env:            map[string]string{agent.SessionHarpEnv: harp},
		},
	}
}

// discoveryPermissionMode is the discovery launch's one-rung resolution: this
// project's declared default posture, else the pinned PermissionDefault.
//
// Nil-safe on purpose, and unparseable-safe for the same reason: GetConfig
// hands back a nil config on a load failure (launchEngineWithPrompt is
// explicitly best-effort about that), and config.GetPermissions is a raw
// hand-editable spelling that nothing validates on the way in. Both degrade to
// the pinned default, which is the narrow end — a misspelling must never widen
// a setup session.
// The declared bool is the caller's way to tell "this project chose default"
// apart from "this project chose nothing" — two inputs that resolve to the same
// posture but are opposite answers to whether the human has been asked yet.
// printDiscoveryPostureHint turns on exactly that distinction.
func discoveryPermissionMode(cfg *config.Config) (mode agent.PermissionMode, declared bool) {
	if cfg == nil {
		return agent.PermissionDefault, false
	}
	if m, ok := agent.ParsePermissionMode(cfg.GetPermissions()); ok {
		return m, true
	}
	return agent.PermissionDefault, false
}

// printDiscoveryPostureHint tells the user, at the moment init hands off, that
// a project-scoped default posture exists and how to set it — but only when
// this launch is running at the PINNED DEFAULT, i.e. when they have not set
// one. A capability nobody is told about is a capability nobody has, and this
// handoff is the one moment in the product where a human is already being
// walked through configuring this specific directory.
//
// It names the KEY, the FILE, and the values, because all three are needed to
// act on it and because WHICH file is the entire restriction: the same line in
// ~/.ctxloom/config.yaml is dropped with a warning and never applied (see
// config.Config.permissions / layerscope). Silent once a posture is declared —
// repeating instructions for something already done is noise.
//
// Prints to stdout via fmt.Println, following printReentryHint below: this is
// part of the handoff narration a human is reading, not a diagnostic.
func printDiscoveryPostureHint(cfg *config.Config) {
	if _, declared := discoveryPermissionMode(cfg); declared {
		return
	}
	fmt.Printf("Tip: set `permissions: <%s>` in this project's .ctxloom/config.yaml\n",
		strings.Join(agent.PermissionModeNames(), "|"))
	fmt.Println("to choose the default posture agents start at HERE (this directory only; a home config is ignored).")
}

// launchEngineWithPrompt starts the engine's own raw CLI/TUI with the merged
// setup-skill prompt (pty passthrough — the vendor's real interactive binary
// on this terminal, exactly as `ctxloom run`'s interactive path). Errors
// (a harp that could not be minted, a failed launch, an errored session) are
// returned to the caller, which reports them through strictness and refuses
// by default rather than swallowing them — there is no relaunch loop or
// review offer to gate on a clean return anymore (both deleted — init hands
// off once and is done).
func launchEngineWithPrompt(ctx context.Context, engine, workDir, harp string) error {
	client, err := pb.NewSelfInvokingClientForLabel(engine, "", 0)
	if err != nil {
		return fmt.Errorf("failed to launch %s: %w", engine, err)
	}
	defer client.Kill()

	// Config is best-effort here: it only feeds the bundle-override lookup for
	// the agent-setup half, and init just wrote it — but a load failure must
	// not sink the discovery launch. GetConfig returns a nil config on error,
	// which discoverySessionPrompt already degrades to the built-in body for.
	cfg, _ := GetConfig()

	req := discoveryRunRequest(cfg, workDir, harp)

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

// authPingTask is the smallest possible prompt sent to probe the selected
// engine's auth before init hands off to its raw CLI/TUI — just enough to
// prove a live, authenticated round trip happened.
const authPingTask = "Reply with exactly: ok"

// engineAuthFixHint names the fix for a failed auth probe, read off the
// engine's OWN credential declaration (backends.CredentialSeedFor): the
// login command that makes its credential file exist, and the env var that
// carries usable auth instead. An engine that declares no seedable
// credential — or is not registered at all — gets a generic but actionable
// fix rather than a blank, since the probe still failed.
func engineAuthFixHint(engine string) string {
	seed, ok := backends.CredentialSeedFor(engine).Get()
	if !ok {
		return "authenticate the engine (subscription login or its API-key env var) and try again"
	}
	fix := fmt.Sprintf("run `%s`", seed.LoginHint)
	if seed.EnvTrigger != "" {
		fix += fmt.Sprintf(" (or set %s)", seed.EnvTrigger)
	}
	return fix
}

// authPingFactory is a test seam: nil self-invokes the compiled-in backend
// (operations.RunOneshot's normal path); tests inject a stub pb.ClientFactory
// so no real engine binary or credential is required to exercise the gate.
var authPingFactory pb.ClientFactory

// pingEngineAuth probes the selected engine with the smallest possible
// oneshot run BEFORE init hands off to its raw CLI/TUI (§12 Q1, user-approved:
// "a dead first session inside a vendor TUI is invisible failure; catch it in
// code"). It reuses operations.RunOneshot — the SAME oneshot Execute surface
// `ctxloom run --print` and delegated agent_run children already ride — rather than a bespoke
// engine-ping path: no explicit profile (falls back to the configured
// defaults, which is a fault-tolerant no-op if none resolve) and the smallest
// possible task. Any failure (missing binary, no credentials, a dead
// subscription token, a real backend error) fails loud, naming the fix for
// THIS engine; auth itself stays ambient — this is a liveness gate, not a
// login flow.
func pingEngineAuth(ctx context.Context, cfg *config.Config, engine, workDir, harp string) error {
	_, err := operations.RunOneshot(ctx, cfg, operations.RunOneshotRequest{
		Task:    authPingTask,
		LLM:     engine,
		WorkDir: workDir,
		// The probe runs INSIDE the setup session, under the harp init already
		// minted, on the same env key every other consumer of session identity
		// reads. A run whose surfaces land in a shared cwd derives its private
		// scratch from this harp, and derives nothing without one: harpless, the
		// probe refuses (ErrSharedScratchNoHarp) rather than writing the
		// session's private files somewhere world-readable.
		Harp: harp,
		// The ping is a throwaway auth-liveness probe with a fixed trivial
		// prompt — it wants no permission gating at all, regardless of
		// whatever posture the chosen engine's llm label declares (or
		// doesn't). That intent used to ride silently on
		// effectiveMemberPermission's now-removed floor (an unset posture
		// ran at bypass with no one saying so); the floor's removal requires
		// every caller of an ask-capable posture to say what it wants out
		// loud, so this one does.
		Permissions: agent.PermissionBypass.String(),
		Factory:     authPingFactory,
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
// dead vendor-TUI session. A session that fails to launch, fails to start, or
// ends in error (an unminted harp, an interrupted setup, a crashed engine) is
// reported through strictness and refuses by default too — the same
// refuse-unless-degraded posture as the ping, not a separate swallow — so
// init's own working outcome not happening is never mistaken for success.
// There is no relaunch loop and no review offer afterward (both deleted:
// re-entry is `/ctxloom-init` or `ctxloom init prompt`, and review is the
// setup skill's own phase 3d/5).
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
	// Mint this session's harp via the SAME primitive `ctxloom run`'s openSession
	// uses (operations.AssignSession), BEFORE the probe rather than inside the
	// launch below. init and run then differ in what they run, not in whether a
	// session exists: the probe and the setup session are two turns of ONE named
	// session, exactly as a run's turns are.
	//
	// Minting it later is what broke the probe. Setup runs on every turn, so a
	// harpless probe reached sharedScratchDir and was refused
	// (ErrSharedScratchNoHarp) — correctly, since a shared-cwd delivery derives
	// its private scratch from the harp and must never fall back to a
	// world-readable location.
	//
	// This does NOT degrade to an unnamed session on failure: a pane-hosted
	// launch refuses an empty harp anyway ("interactive launch requires a named
	// session"), so continuing would defer the same failure with a worse
	// diagnosis.
	entry, err := operations.AssignSession(cmd.Context(), workDir, engine)
	if err != nil {
		return fmt.Errorf("failed to name the setup session: %w", err)
	}

	if err := pingEngineAuth(cmd.Context(), cfg, engine, workDir, entry.HarpName); err != nil {
		return err
	}

	fmt.Printf("\nLaunching %s for setup...\n", engine)
	fmt.Println("(Exit the session when done)")
	// Said HERE, immediately before the session that the posture governs, rather
	// than buried in the reentry hint after it: this is the moment the pinned
	// default is about to bite, and the moment the human is already deciding how
	// this directory should be set up.
	printDiscoveryPostureHint(cfg)
	fmt.Println()

	if launchErr := launchEngineWithPromptFn(cmd.Context(), engine, workDir, entry.HarpName); launchErr != nil {
		// A setup session that failed to launch (or start) is init's own
		// working outcome not happening, so it must not exit clean — that
		// is the exit-0-success-message-nothing-happened shape CLAUDE.md
		// singles out. Reported through strictness rather than a bespoke
		// degraded check here: FailOnce streams the warning in BOTH modes
		// (so --degraded still tells the user what did not happen), and
		// FindingsError is what turns it fatal in strict mode and not under
		// --degraded, matching every other refuse-by-default choke in this
		// codebase without this call site deciding fatality itself.
		mark := strictness.Checkpoint()
		defer strictness.Close(mark)
		strictness.FailOnce(strictness.ClassConfig,
			"check the engine's auth/config, then retry `ctxloom init`, or run `ctxloom init prompt` to reconfigure without relaunching",
			"the setup session failed to launch: %v", launchErr)
		return strictness.FindingsError(mark)
	}

	printReentryHint()
	return nil
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
