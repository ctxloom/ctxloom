// Command mockengine is the standalone deterministic stand-in for a real vendor
// coding-agent CLI (see internal/engines/mock/runtime's package doc). It is installed
// UNDER a vendor's name via ctxloom's injection seam — an oneshot config's
// binary_path, or COPY'd over the resolved binary in a fixture image — so its
// own name is deliberately vendor-NEUTRAL: nothing here should read as a real
// tool. It reuses the "mock" root the in-process mock backend already uses, with
// "engine" naming the vendor-CLI role in the architecture vocabulary, so
// mockengine reads clearly as "the fake that impersonates an engine" and is
// distinct from the in-process backend named "mock".
//
// One PERSONALITY per launch, selected by a flag (--<backend>) or the
// MOCKENGINE_PERSONALITY env var (backend registry name), and one SURFACE,
// selected by --surface or MOCKENGINE_SURFACE (oneshot unless said otherwise).
// Everything after the mock's own leading flags is the VENDOR argv, parsed
// against L1's declared grammar for the chosen personality+surface — the mock
// never restates that grammar.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/engines/mock/runtime"
)

// envPersonality selects the personality when no --<backend>/--personality flag is
// present — the clean channel when the mock is installed via a config `env:`
// block and the driver owns the argv.
const envPersonality = "MOCKENGINE_PERSONALITY"

// envSurface selects the surface when no --surface flag is present, for the
// same reason envPersonality exists. Empty means oneshot.
const envSurface = "MOCKENGINE_SURFACE"

func main() {
	// The personalities are the composed engines; compose them first.
	if err := engines.Compose(); err != nil {
		fmt.Fprintf(os.Stderr, "mockengine: %v\n", err)
		os.Exit(2)
	}
	os.Exit(run(os.Args[1:]))
}

// personalityFromFlag resolves a leading `--<engine>` token to the registered
// backend it selects, reporting false for anything else so the caller stops
// consuming and treats the token as vendor argv.
//
// The flag's spelling IS the backend registry's name (--claude-code), and
// membership comes from the registry, so this package names no engine and a
// newly impersonable backend is selectable the same way the existing ones are
// without editing main. A hand-written per-engine flag switch here is the
// second copy of the engine vocabulary that drifts.
func personalityFromFlag(tok string) (string, bool) {
	name, ok := strings.CutPrefix(tok, "--")
	if !ok || name == "" {
		return "", false
	}
	if _, ok := engines.EngineCLIs(name); !ok {
		return "", false
	}
	return name, true
}

// impersonable lists the registered backends that declare an engine CLI — the
// personalities a --<backend> flag can select.
func impersonable() []string {
	return engines.NamesWhere(func(name string, _ engine.Engine) bool {
		_, ok := engines.EngineCLIs(name)
		return ok
	})
}

// surfaceByName resolves the requested surface name against the personality's
// DECLARED surfaces; an empty name means oneshot. The name is matched, never
// converted into agent.CLISurface: membership in that vocabulary is the
// declaration's to assert, and a value no surface declares is refused here
// with the same loudness an undeclared flag gets from ParseArgv.
func surfaceByName(clis []agent.EngineCLI, name string) (agent.EngineCLI, bool) {
	if name == "" {
		return agent.EngineCLIFor(clis, agent.CLISurfaceOneshot)
	}
	for _, cli := range clis {
		if string(cli.Surface) == name {
			return cli, true
		}
	}
	return agent.EngineCLI{}, false
}

// run parses the mock's OWN leading flags, resolves the personality's EngineCLI
// via the backends resolver, parses the remaining vendor argv against L1, and
// runs the L2 runtime. It returns a process exit code.
func run(args []string) int {
	personality := os.Getenv(envPersonality)
	surfaceName := os.Getenv(envSurface)
	vendorArgs := args

	// Consume the mock's own leading flags. They come FIRST because ctxloom
	// prepends a config `args:` block ahead of the engine flags buildArgs emits,
	// so a leading --<backend> survives into argv[0..]. Parsing stops at the first
	// token that is not a mock flag (or at an explicit "--"), and everything
	// after is the vendor argv.
consume:
	for len(vendorArgs) > 0 {
		switch vendorArgs[0] {
		case "--personality":
			if len(vendorArgs) < 2 {
				fmt.Fprintln(os.Stderr, "mock-engine: --personality needs a value")
				return 2
			}
			personality = vendorArgs[1]
			vendorArgs = vendorArgs[2:]
		case "--surface":
			if len(vendorArgs) < 2 {
				fmt.Fprintln(os.Stderr, "mock-engine: --surface needs a value")
				return 2
			}
			surfaceName = vendorArgs[1]
			vendorArgs = vendorArgs[2:]
		case "--":
			vendorArgs = vendorArgs[1:]
			break consume
		default:
			name, ok := personalityFromFlag(vendorArgs[0])
			if !ok {
				break consume
			}
			personality = name
			vendorArgs = vendorArgs[1:]
		}
	}

	if personality == "" {
		// The hint names the flags that actually work: the registry's own
		// names, not a spelling this file guessed.
		fmt.Fprintf(os.Stderr, "mock-engine: no personality selected — pass --<backend> (one of %s) or set %s\n",
			strings.Join(impersonable(), ", "), envPersonality)
		return 2
	}

	clis, ok := engines.EngineCLIs(personality)
	if !ok {
		fmt.Fprintf(os.Stderr, "mock-engine: backend %q declares no engine CLI to impersonate\n", personality)
		return 2
	}
	cli, ok := surfaceByName(clis, surfaceName)
	if !ok {
		fmt.Fprintf(os.Stderr, "mock-engine: %q has no %q surface\n", personality, surfaceName)
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

	// Both roots are REPORTED, not merely used: Resolver.Cwd and Resolver.Home
	// are what ScopeCwd and ScopeHome probes join their relative paths onto,
	// and each lands in the evidence report as rec.Root. An empty root does
	// not disable a probe — filepath.Join("", rel) yields rel — so a home-
	// scoped probe silently re-roots onto the process working directory and
	// reports the project's own .claude/CLAUDE.md as the one it found in
	// $HOME. A fake whose whole product is a faithful observation report must
	// refuse rather than answer a different question, and an unset HOME is
	// ordinary here: this binary runs inside container fixtures under a
	// vendor's name, and ctxloom rewrites HOME deliberately to isolate engines.
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mock-engine: cannot determine the working directory, which every cwd-scoped probe resolves against: %v\n", err)
		return 2
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mock-engine: cannot determine the home directory, which every home-scoped probe resolves against: %v\n", err)
		return 2
	}

	rt := &runtime.Runtime{
		CLI:  cli,
		Argv: parsed,
		Res: runtime.Resolver{
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
	if cli.Surface == agent.CLISurfaceInteractive {
		rt.Resize = resizeNotifications(os.Stdout)
	}
	return rt.Run()
}
