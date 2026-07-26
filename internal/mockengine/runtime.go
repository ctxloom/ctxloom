package mockengine

import (
	"fmt"
	"io"
	"os"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// Runtime is the L2 standard mock runtime: backend-agnostic behaviour wired to
// one resolved EngineCLI surface. It parses nothing itself beyond what L1's
// ParseArgv already did — it reads the prompt off the surface's declared
// delivery channel, runs the shared discovery walk over L1's probes, shapes the
// report, dispatches the prompt sentinels, and renders the outcome on the
// surface's wire format. Adding a backend adds an L1 profile, not code here.
type Runtime struct {
	// CLI is the resolved surface declaration (one of a backend's EngineCLIs).
	CLI agent.EngineCLI
	// Argv is the vendor argv already parsed against CLI's grammar.
	Argv agent.ParsedArgv
	// Res resolves probe roots (cwd/home/env).
	Res Resolver
	// Getenv reads the controlling environment (sentinel knobs, report file).
	Getenv func(string) string
	// LookupEnv is the TWO-VALUE reader used for the declared env-contract
	// observation (os.LookupEnv in production). It exists separately from
	// Getenv because that half of the contract turns on the difference between
	// an unset variable and one set to the empty string, which the one-value
	// form cannot express — and "set to nothing" is exactly what a delivery
	// that ran and passed nothing looks like.
	LookupEnv func(string) (string, bool)
	// Stdin is the child's stdin — the oneshot prompt channel.
	Stdin io.Reader
	// Stdout is the vendor wire output.
	Stdout io.Writer
	// Stderr carries the discovery report (bracketed by the report markers) and
	// any diagnostics.
	Stderr io.Writer
}

// getenv is a nil-safe environment reader.
func (r *Runtime) getenv(k string) string {
	if r.Getenv == nil {
		return os.Getenv(k)
	}
	return r.Getenv(k)
}

// lookupenv is the nil-safe two-value environment reader. When only the
// one-value seam is injected it is derived from that, degrading to "empty
// means unset" rather than reaching past the seam into the real process
// environment.
func (r *Runtime) lookupenv(k string) (string, bool) {
	switch {
	case r.LookupEnv != nil:
		return r.LookupEnv(k)
	case r.Getenv != nil:
		v := r.Getenv(k)
		return v, v != ""
	default:
		return os.LookupEnv(k)
	}
}

// Run executes the mock end to end and returns the process exit code. The
// discovery report is emitted UNCONDITIONALLY — before the sentinel can fail the
// run — because a probe of what context arrived is exactly the evidence a
// failing run most needs.
func (r *Runtime) Run() int {
	prompt := r.readPrompt()

	recs := Walk(r.CLI, r.Argv, r.Res)
	env := ObserveEnv(r.CLI, r.lookupenv)
	report := BuildReport(r.CLI.Engine, string(r.CLI.Surface), recs, env, prompt)
	r.emitReport(report)

	outcome := Dispatch(string(prompt), r.getenv)

	if err := r.render(len(prompt), outcome); err != nil {
		fmt.Fprintf(r.Stderr, "mock-engine: render error: %v\n", err)
		return 1
	}
	return outcome.ExitCode
}

// readPrompt reads the task off the surface's DECLARED delivery channel — stdin
// for a PromptStdin surface (claude oneshot), the trailing argv positional for
// a PromptPositional surface (interactive) — so the mock looks for the prompt
// exactly where L1 says the driver put it.
func (r *Runtime) readPrompt() []byte {
	switch r.CLI.Prompt {
	case agent.PromptStdin:
		if r.Stdin == nil {
			return nil
		}
		b, _ := io.ReadAll(r.Stdin)
		return b
	case agent.PromptPositional:
		if n := len(r.Argv.Positionals); n > 0 {
			return []byte(r.Argv.Positionals[n-1])
		}
		return nil
	default:
		return nil
	}
}

// emitReport writes the report to stderr bracketed by the begin/end markers, and
// additionally to CTXLOOM_MOCK_REPORT_FILE when set.
func (r *Runtime) emitReport(report Report) {
	b, err := report.Marshal()
	if err != nil {
		fmt.Fprintf(r.Stderr, "mock-engine: report marshal error: %v\n", err)
		return
	}
	fmt.Fprintf(r.Stderr, "%s\n%s\n%s\n", ReportBegin, b, ReportEnd)
	if path := r.getenv(EnvReportFile); path != "" {
		if werr := os.WriteFile(path, b, 0o644); werr != nil {
			fmt.Fprintf(r.Stderr, "mock-engine: report file write error: %v\n", werr)
		}
	}
}

// render puts the outcome on the wire in the surface's format. Oneshot dispatches
// to the engine's per-personality wire adapter (renderOneshotWire — claude's
// {result,modelUsage} envelope under SkipSetup, codex's plain text), because the
// oneshot stdout contract is per-engine and not derivable from L1. Interactive —
// deferred in this slice to a plain echo — writes the response text without the
// pty/keystroke/SIGWINCH behaviour a real interactive engine has (see the package
// doc's deferred-surfaces note).
func (r *Runtime) render(promptLen int, outcome Outcome) error {
	switch r.CLI.Surface {
	case agent.CLISurfaceOneshot:
		return renderOneshotWire(r.CLI.Engine, r.Stdout, r.Argv, promptLen, outcome)
	default:
		_, err := io.WriteString(r.Stdout, outcome.Response)
		return err
	}
}
