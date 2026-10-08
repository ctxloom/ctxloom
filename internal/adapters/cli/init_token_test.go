package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// tokenGateFake records what the init token gate asked of its two seams: the
// engine's binary resolution and the setup run. Neither ever reaches a real
// engine CLI.
type tokenGateFake struct {
	binary     string
	resolveErr error
	runErr     error
	resolved   int
	ran        []*exec.Cmd
	// outAtRun is how much guidance had been written when the setup ran: the
	// explanation must come BEFORE the human is handed to claude.
	outAtRun int
}

func installTokenGateFake(t *testing.T, f *tokenGateFake, out *bytes.Buffer) {
	t.Helper()
	origResolve, origRun := resolveTokenSetupBinary, runTokenSetup
	resolveTokenSetupBinary = func(engine.Registry, string) (string, error) {
		f.resolved++
		return f.binary, f.resolveErr
	}
	runTokenSetup = func(c *exec.Cmd) error {
		f.ran = append(f.ran, c)
		if out != nil {
			f.outAtRun = out.Len()
		}
		return f.runErr
	}
	t.Cleanup(func() { resolveTokenSetupBinary, runTokenSetup = origResolve, origRun })
}

// envOf is an os.LookupEnv over a map.
func envOf(kv map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := kv[k]; return v, ok }
}

const tokenGateEngine = "claude-code"

func remedyOf(t *testing.T, err error) string {
	t.Helper()
	var r report.Remediable
	require.True(t, errors.As(err, &r), "the refusal carries a remedy: %v", err)
	return r.Remedy()
}

// A token already exported is the whole answer: init does not explain, does
// not resolve claude, and does not run setup-token. Checked on a terminal,
// where the bootstrap would otherwise run, so the pin cannot pass vacuously.
func TestEnsureAgentToken_TokenSet_SkipsTheBootstrap(t *testing.T) {
	f := &tokenGateFake{binary: "/fake/bin/claude"}
	var out bytes.Buffer
	installTokenGateFake(t, f, &out)

	err := ensureAgentToken(context.Background(), engines.Registry(), tokenGateEngine, true,
		envOf(map[string]string{claude.OAuthTokenEnv: "fixture-not-a-token", "SHELL": "/bin/zsh"}), &out)
	require.NoError(t, err)
	assert.Empty(t, f.ran, "setup-token must not run when the token is set")
	assert.Zero(t, f.resolved, "claude need not even be resolved when the token is set")
	assert.Empty(t, out.String(), "nothing to explain when the token is set")
}

// On a terminal with no token: claude's own setup-token runs on the human's
// own terminal (stdin, stdout and stderr inherited, nothing captured), then
// init prints the line to add to the shell profile with a PLACEHOLDER, and
// stops with the typed sentinel whose remedy is to export and re-run init.
func TestEnsureAgentToken_TerminalNoToken_RunsSetupThenGuides(t *testing.T) {
	f := &tokenGateFake{binary: "/fake/bin/claude"}
	var out bytes.Buffer
	installTokenGateFake(t, f, &out)

	err := ensureAgentToken(context.Background(), engines.Registry(), tokenGateEngine, true,
		envOf(map[string]string{"SHELL": "/usr/bin/zsh"}), &out)

	require.ErrorIs(t, err, ErrTokenExportThenRerun)
	require.Len(t, f.ran, 1, "setup-token runs exactly once")
	c := f.ran[0]
	assert.Equal(t, "/fake/bin/claude", c.Path, "the engine's own resolved binary runs")
	assert.Equal(t, []string{"/fake/bin/claude", "setup-token"}, c.Args)
	assert.Same(t, os.Stdin, c.Stdin, "stdin is the human's own terminal")
	assert.Same(t, os.Stdout, c.Stdout, "stdout is the human's own terminal: ctxloom reads nothing claude prints")
	assert.Same(t, os.Stderr, c.Stderr, "stderr is the human's own terminal")
	assert.Positive(t, f.outAtRun, "the human is told what is about to happen before claude takes the terminal")

	guidance := out.String()
	assert.Contains(t, guidance, "export "+claude.OAuthTokenEnv+"="+tokenPlaceholder+"\n",
		"the export line carries a placeholder, never a value")
	assert.Contains(t, guidance, "~/.zshrc", "the profile file for the detected shell")
	assert.Contains(t, guidance, "parent shell", "say plainly why ctxloom cannot export it itself")

	remedy := remedyOf(t, err)
	assert.Contains(t, remedy, claude.OAuthTokenEnv)
	assert.Contains(t, remedy, "re-run `ctxloom init`")
}

// The guidance is printed only once setup-token has completed: a failed or
// abandoned setup is reported as that, typed, with no export line implying a
// token exists.
func TestEnsureAgentToken_SetupFails_NoExportLine(t *testing.T) {
	f := &tokenGateFake{binary: "/fake/bin/claude", runErr: errors.New("exit status 1")}
	var out bytes.Buffer
	installTokenGateFake(t, f, &out)

	err := ensureAgentToken(context.Background(), engines.Registry(), tokenGateEngine, true,
		envOf(map[string]string{"SHELL": "/bin/bash"}), &out)
	require.ErrorIs(t, err, ErrTokenSetupFailed)
	assert.NotErrorIs(t, err, ErrTokenExportThenRerun)
	assert.NotContains(t, out.String(), "export "+claude.OAuthTokenEnv)
	assert.Contains(t, remedyOf(t, err), "claude setup-token")
}

// Off a terminal there is no one to hand claude's flow to: no prompt, no
// setup-token, and not even a binary lookup — a typed refusal whose remedy is
// the same steps for the human to take.
func TestEnsureAgentToken_NoTerminalNoToken_TypedRefusal(t *testing.T) {
	f := &tokenGateFake{binary: "/fake/bin/claude"}
	var out bytes.Buffer
	installTokenGateFake(t, f, &out)

	err := ensureAgentToken(context.Background(), engines.Registry(), tokenGateEngine, false,
		envOf(map[string]string{"SHELL": "/bin/zsh"}), &out)
	require.ErrorIs(t, err, ErrAgentTokenNotExported)
	assert.Empty(t, f.ran, "setup-token never runs off a terminal")
	assert.Zero(t, f.resolved)
	assert.Empty(t, out.String(), "no prompt off a terminal")
	remedy := remedyOf(t, err)
	assert.Contains(t, remedy, "claude setup-token")
	assert.Contains(t, remedy, claude.OAuthTokenEnv)
	assert.Equal(t, remedyOf(t, operations.AgentTokenMissing(engines.Registry(), tokenGateEngine, envOf(nil))), remedy,
		"init's fix is the engine's own wording, the one `ctxloom auth` and `ctxloom run` show")
}

// claude not installed: a typed refusal naming the binary, and nothing run.
func TestEnsureAgentToken_TerminalNoBinary_TypedNamingIt(t *testing.T) {
	f := &tokenGateFake{resolveErr: errors.New(`"claude" not found on PATH`)}
	var out bytes.Buffer
	installTokenGateFake(t, f, &out)

	err := ensureAgentToken(context.Background(), engines.Registry(), tokenGateEngine, true,
		envOf(map[string]string{"SHELL": "/bin/zsh"}), &out)
	require.ErrorIs(t, err, ErrTokenSetupUnavailable)
	assert.Contains(t, err.Error(), "claude")
	assert.Empty(t, f.ran)
}

// An engine that declares no auth (the mock) has no token to create.
func TestEnsureAgentToken_EngineWithoutAuth_NothingToDo(t *testing.T) {
	f := &tokenGateFake{binary: "/fake/bin/mock"}
	installTokenGateFake(t, f, nil)
	require.NoError(t, ensureAgentToken(context.Background(), engines.Registry(), "mock", true, envOf(nil), &bytes.Buffer{}))
	assert.Empty(t, f.ran)
}

// The profile named follows $SHELL; fish has its own syntax, and an unknown
// shell is told generically rather than pointed at a wrong file.
func TestTokenExportGuidance_FollowsTheShell(t *testing.T) {
	for _, tc := range []struct{ shell, file, line string }{
		{"/bin/zsh", "~/.zshrc", "export X=" + tokenPlaceholder},
		{"/usr/local/bin/bash", "~/.bashrc", "export X=" + tokenPlaceholder},
		{"/usr/bin/fish", "~/.config/fish/config.fish", "set -gx X " + tokenPlaceholder},
		{"", "your shell's startup file", "export X=" + tokenPlaceholder},
		{"/bin/tcsh", "your shell's startup file", "export X=" + tokenPlaceholder},
	} {
		t.Run(tc.shell, func(t *testing.T) {
			g := tokenExportGuidance("X", tc.shell)
			assert.Contains(t, g, tc.file)
			assert.Contains(t, g, "    "+tc.line+"\n")
		})
	}
}

// launchDiscovery wires the gate: with a token the existing probe still runs
// (now proving the credential every agent uses); without one on a terminal
// the probe does not run and the sentinel comes back; --skip-launch runs no
// engine at all, setup-token included.
func TestLaunchDiscovery_TokenGate(t *testing.T) {
	newCmd := func() *cobra.Command { c := &cobra.Command{}; c.SetContext(context.Background()); return c }
	cfg := func() *config.Config { return config.NewFixture(config.Fixture{AppPaths: []string{t.TempDir()}}) }

	t.Run("token set: the probe runs", func(t *testing.T) {
		t.Setenv(claude.OAuthTokenEnv, "fixture-not-a-token")
		f := &tokenGateFake{binary: "/fake/bin/claude"}
		installTokenGateFake(t, f, nil)
		testLaunchDeps(t, cfg())
		stub := &stubRunHost{}
		stubPingHosts(t, stub)
		orig := launchEngineWithPromptFn
		launchEngineWithPromptFn = func(context.Context, launch.Deps, string, launch.Launch) error { return nil }
		t.Cleanup(func() { launchEngineWithPromptFn = orig })

		_ = captureStdout(t, func() { require.NoError(t, launchDiscovery(newCmd(), tokenGateEngine, t.TempDir()+"/.ctxloom", true)) })
		assert.NotNil(t, stub.gotLaunch, "the token probe ran")
		assert.Empty(t, f.ran)
	})

	t.Run("no token on a terminal: no probe, the sentinel", func(t *testing.T) {
		t.Setenv(claude.OAuthTokenEnv, "")
		f := &tokenGateFake{binary: "/fake/bin/claude"}
		installTokenGateFake(t, f, nil)
		testLaunchDeps(t, cfg())
		stub := &stubRunHost{}
		stubPingHosts(t, stub)

		var err error
		_ = captureStdout(t, func() { err = launchDiscovery(newCmd(), tokenGateEngine, t.TempDir()+"/.ctxloom", true) })
		require.ErrorIs(t, err, ErrTokenExportThenRerun)
		assert.Nil(t, stub.gotLaunch, "no probe runs before the token exists")
		assert.Len(t, f.ran, 1)
	})

	t.Run("no token off a terminal: setup stands, the launch is skipped with a warning", func(t *testing.T) {
		t.Setenv(claude.OAuthTokenEnv, "")
		f := &tokenGateFake{binary: "/fake/bin/claude"}
		installTokenGateFake(t, f, nil)
		var warned bytes.Buffer
		t.Cleanup(clidiag.SetSink(&warned))
		require.NoError(t, launchDiscovery(newCmd(), tokenGateEngine, t.TempDir()+"/.ctxloom", false))
		assert.Empty(t, f.ran)
		assert.Contains(t, warned.String(), ErrAgentTokenNotExported.Error(), "the skipped launch is warned")
	})

	t.Run("--skip-launch: no engine runs, setup-token included", func(t *testing.T) {
		t.Setenv(claude.OAuthTokenEnv, "")
		f := &tokenGateFake{binary: "/fake/bin/claude"}
		installTokenGateFake(t, f, nil)
		orig := initSkipLaunch
		initSkipLaunch = true
		t.Cleanup(func() { initSkipLaunch = orig })
		require.NoError(t, launchDiscovery(newCmd(), tokenGateEngine, t.TempDir()+"/.ctxloom", true))
		assert.Empty(t, f.ran)
		assert.Zero(t, f.resolved)
	})
}

// A FOUND BUG, pinned: a test that reached launchDiscovery on a terminal with
// no token ran the REAL `claude setup-token`, which opened the owner's browser
// on a login page each time. The production invoker therefore refuses to run
// anything from a test binary. /bin/true stands for the installed binary so a
// missing guard runs nothing that matters.
func TestRunTokenSetupAttached_RefusesInATestBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/true is a unix path")
	}
	err := runTokenSetupAttached(exec.Command("/bin/true", "setup-token"))
	require.ErrorIs(t, err, errSetupUnderTest)
}

// withAgentToken exports a fixture agent token for one test, putting it on
// the "token set" side of init's token gate. The test binary's sandbox scrubs
// the variable, so a test that reaches launchDiscovery sets it itself.
func withAgentToken(t *testing.T) {
	t.Helper()
	t.Setenv(claude.OAuthTokenEnv, "cli-test-fixture-not-a-token")
}
