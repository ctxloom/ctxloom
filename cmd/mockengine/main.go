// Command mockengine is the standalone deterministic stand-in for a real vendor
// coding-agent CLI (see internal/mockengine's package doc). It is installed
// UNDER a vendor's name via ctxloom's injection seam — an oneshot config's
// binary_path, or COPY'd over the resolved binary in a fixture image — so its
// own name is deliberately vendor-NEUTRAL: nothing here should read as a real
// tool. It reuses the "mock" root the in-process mock backend already uses, with
// "engine" naming the vendor-CLI role in the architecture vocabulary, so
// mockengine reads clearly as "the fake that impersonates an engine" and is
// distinct from the in-process backend named "mock".
//
// One PERSONALITY per launch, selected by a flag (--claude) or the
// MOCKENGINE_PERSONALITY env var (backend registry name). Everything after the
// mock's own leading flags is the VENDOR argv, parsed against L1's declared
// grammar for the chosen personality+surface — the mock never restates that
// grammar.
package main

import (
	"fmt"
	"os"

	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/mockengine"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// EnvPersonality selects the personality when no --claude/--personality flag is
// present — the clean channel when the mock is installed via a config `env:`
// block and the driver owns the argv.
const EnvPersonality = "MOCKENGINE_PERSONALITY"

func main() { os.Exit(run(os.Args[1:])) }

// run parses the mock's OWN leading flags, resolves the personality's EngineCLI
// via the backends resolver, parses the remaining vendor argv against L1, and
// runs the L2 runtime. It returns a process exit code.
func run(args []string) int {
	personality := os.Getenv(EnvPersonality)
	surface := agent.CLISurfaceOneshot // this slice: oneshot is the built surface
	vendorArgs := args

	// Consume the mock's own leading flags. They come FIRST because ctxloom
	// prepends a config `args:` block ahead of the engine flags buildArgs emits,
	// so a leading --claude survives into argv[0..]. Parsing stops at the first
	// token that is not a mock flag (or at an explicit "--"), and everything
	// after is the vendor argv.
consume:
	for len(vendorArgs) > 0 {
		switch vendorArgs[0] {
		case "--claude", "--claude-code":
			personality = "claude-code"
			vendorArgs = vendorArgs[1:]
		case "--codex":
			personality = "codex"
			vendorArgs = vendorArgs[1:]
		case "--personality":
			if len(vendorArgs) < 2 {
				fmt.Fprintln(os.Stderr, "mock-engine: --personality needs a value")
				return 2
			}
			personality = vendorArgs[1]
			vendorArgs = vendorArgs[2:]
		case "--":
			vendorArgs = vendorArgs[1:]
			break consume
		default:
			break consume
		}
	}

	if personality == "" {
		fmt.Fprintf(os.Stderr, "mock-engine: no personality selected — pass --claude or set %s\n", EnvPersonality)
		return 2
	}

	clis, ok := backends.EngineCLIsFor(personality)
	if !ok {
		fmt.Fprintf(os.Stderr, "mock-engine: backend %q declares no engine CLI to impersonate\n", personality)
		return 2
	}
	cli, ok := agent.EngineCLIFor(clis, surface)
	if !ok {
		fmt.Fprintf(os.Stderr, "mock-engine: %q has no %q surface\n", personality, surface)
		return 2
	}

	// Parse the vendor argv against L1's grammar. An undeclared flag is a LOUD
	// error here, exactly as the driver's own anti-drift test treats it: a fake
	// that silently tolerated an unknown flag would be one drift away from
	// passing while the real spawn broke.
	parsed, err := cli.ParseArgv(vendorArgs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mock-engine: vendor argv did not parse against the %s/%s grammar: %v\n",
			cli.Engine, cli.Surface, err)
		return 2
	}

	cwd, _ := os.Getwd()
	home, _ := os.UserHomeDir()

	rt := &mockengine.Runtime{
		CLI:  cli,
		Argv: parsed,
		Res: mockengine.Resolver{
			Cwd:    cwd,
			Home:   home,
			Getenv: os.Getenv,
		},
		Getenv: os.Getenv,
		// The two-value form is passed EXPLICITLY: the env-contract observation
		// turns on unset-versus-set-to-empty, and deriving presence from
		// os.Getenv alone would report a variable ctxloom set to nothing as one
		// it never set.
		LookupEnv: os.LookupEnv,
		Stdin:     os.Stdin,
		Stdout:    os.Stdout,
		Stderr:    os.Stderr,
	}
	return rt.Run()
}
