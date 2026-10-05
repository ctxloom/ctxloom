package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/ltk/ir"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// The init token gate's outcomes. Each is returned wrapped under a remedy
// (report.Remediable) naming what the human does next.
var (
	// ErrTokenExportThenRerun: the engine's own setup flow ran on the human's
	// terminal. The token now exists only on their screen; they export it
	// and re-run init.
	ErrTokenExportThenRerun = errors.New("the agent token was created but is not exported in this shell")
	// ErrAgentTokenNotExported: no token, and no terminal to run the
	// engine's setup flow on.
	ErrAgentTokenNotExported = errors.New("the agent token is not exported")
	// ErrTokenSetupUnavailable: the engine's binary, which runs the setup
	// flow, cannot be found.
	ErrTokenSetupUnavailable = errors.New("the engine's token setup cannot be run")
	// ErrTokenSetupFailed: the setup flow ran and did not complete.
	ErrTokenSetupFailed = errors.New("the engine's token setup did not complete")
)

// tokenPlaceholder stands where the token goes in the export line init
// prints. init never has the value: setup-token shows it to the human only.
const tokenPlaceholder = "<the token setup-token showed you>"

// resolveTokenSetupBinary and runTokenSetup are the gate's seams onto the
// engine's real CLI, so no test ever starts a real setup flow.
var (
	resolveTokenSetupBinary = operations.EngineAvailability
	runTokenSetup           = runTokenSetupAttached
)

// errSetupUnderTest: a test binary reached the real setup run. The engine's
// setup flow opens the human's browser on a sign-in page, so from a test
// binary it is refused outright: a test replaces runTokenSetup instead.
var errSetupUnderTest = errors.New("refusing to run an engine's token setup from a test binary")

// runTokenSetupAttached runs the engine's setup flow attached to the human's
// terminal. It refuses inside a test binary.
func runTokenSetupAttached(c *exec.Cmd) error {
	if testing.Testing() {
		return fmt.Errorf("%w: %s", errSetupUnderTest, c.Path)
	}
	return c.Run()
}

// ensureAgentToken makes sure the token every agent authenticates with is
// exported before init probes it. An exported token (by the engine's own
// resolution, as a run makes it) is the whole answer and nothing else
// happens. Without one, on a terminal, the engine's own token flow runs on
// the human's terminal (stdio inherited, nothing read), then the export line
// is printed with a placeholder and init stops with ErrTokenExportThenRerun:
// a child process cannot set its parent shell's environment. Off a terminal
// it refuses with ErrAgentTokenNotExported. ctxloom never sees, captures or
// stores the token.
func ensureAgentToken(ctx context.Context, reg engine.Registry, eng string, interactive bool, shell func(string) (string, bool), out io.Writer) error {
	kind, ok := reg.Lookup(engine.Name(eng))
	if !ok {
		return nil
	}
	a, ok := kind.Home().Auth.Get()
	if !ok {
		return nil
	}
	err := operations.AgentTokenMissing(reg, eng, shell)
	if err == nil {
		return nil
	}
	setup, ok := a.(engine.TokenSetup)
	if !ok {
		return err
	}
	cmdline := strings.Join(append([]string{operations.EngineBinary(reg, eng)}, setup.SetupArgs()...), " ")
	shellPath, _ := shell("SHELL")
	steps := fmt.Sprintf("run `%s`, add `export %s=…` with the token it shows you to %s, then re-run `ctxloom init`",
		cmdline, setup.TokenEnv(), shellProfile(shellPath).file)
	if !interactive {
		// The engine's own fix, verbatim: the wording `ctxloom auth` and
		// `ctxloom run` show for the same missing token.
		fix, _ := clifmt.RemedyOf(err)
		return report.Errorf(fix, "%s: %w (%s)", eng, ErrAgentTokenNotExported, setup.TokenEnv())
	}
	bin, rerr := resolveTokenSetupBinary(reg, eng)
	if rerr != nil {
		return report.Errorf("install "+operations.EngineBinary(reg, eng)+" (or put it on PATH), then re-run `ctxloom init`",
			"%s: %w: %v", eng, ErrTokenSetupUnavailable, rerr)
	}
	_, _ = fmt.Fprintf(out, "\nEvery agent ctxloom launches authenticates with a long-lived token, and %s is not exported.\n"+
		"Running `%s` on your terminal now: sign in through it. ctxloom does not read, capture or store what it prints.\n\n",
		setup.TokenEnv(), cmdline)
	c := exec.CommandContext(ctx, bin, setup.SetupArgs()...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := runTokenSetup(c); err != nil {
		return report.Errorf(steps, "%s: %w: %v", eng, ErrTokenSetupFailed, err)
	}
	_, _ = io.WriteString(out, tokenExportGuidance(setup.TokenEnv(), shellPath))
	return report.Errorf("export "+setup.TokenEnv()+" in this shell (or open a new one after adding it to your profile), then re-run `ctxloom init`",
		"%s: %w", eng, ErrTokenExportThenRerun)
}

// profile is a shell's startup file and its form of an exported variable.
type profile struct {
	file   string
	export func(name, value string) string
}

// shellProfile is the startup file and export syntax for the shell at
// shellPath ($SHELL). An unrecognised shell is told generically rather than
// pointed at a file it does not read.
func shellProfile(shellPath string) profile {
	posix := func(n, v string) string { return "export " + n + "=" + v }
	name := filepath.Base(shellPath)
	switch name {
	case string(ir.ShellZsh):
		return profile{"~/.zshrc", posix}
	case string(ir.ShellBash):
		return profile{"~/.bashrc", posix}
	case "fish":
		return profile{"~/.config/fish/config.fish", func(n, v string) string { return "set -gx " + n + " " + v }}
	}
	return profile{"your shell's startup file", posix}
}

// tokenExportGuidance is what init prints once the engine's setup flow has
// completed: the one line to add to the shell profile, with the placeholder
// standing for the value only the human has.
func tokenExportGuidance(tokenEnv, shellPath string) string {
	p := shellProfile(shellPath)
	return fmt.Sprintf("\nAdd this line to %s, putting in the token setup-token showed you:\n\n    %s\n\n"+
		"ctxloom cannot export it for you: a child process cannot change its parent shell's environment.\n"+
		"Open a new shell (or export it in this one), then re-run `ctxloom init`.\n",
		p.file, p.export(tokenEnv, tokenPlaceholder))
}
