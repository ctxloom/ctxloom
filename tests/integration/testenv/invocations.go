package testenv

import (
	"sync"

	"github.com/ctxloom/ctxloom/internal/cli"
)

// Invocation is one CLI argv this suite actually executed, as opposed to one
// merely NAMED in a feature file's prose or a step's source. See
// RecordedInvocations.
type Invocation struct {
	Leaf string   // "ctxloom manage install"
	Args []string // full argv passed to Run/Command, so "--engine codex"
	// variants of the same leaf stay distinct from one another
}

// invocationsMu guards invocations. The acceptance suite runs its scenarios
// sequentially (godog's default Concurrency, unset here — see
// acceptance_test.go), so there is no real contention today; the mutex is
// cheap insurance against that ever changing silently.
var (
	invocationsMu sync.Mutex
	invocations   []Invocation
)

// RecordedInvocations returns every argv this suite actually executed, so
// coverage credit follows execution: a @wip or excluded scenario credits
// nothing, and a substring can never satisfy a leaf it did not run.
//
// Populated from two kinds of call site. Run and RunWithStdin record
// themselves — they own a command's whole Start-through-Wait lifecycle, so
// recording inside them is unambiguously honest. The few callers that build
// via Command instead (which only CONSTRUCTS an *exec.Cmd and leaves
// starting it to the caller) call RecordCommandStart once they have actually
// started the process — see that method's doc comment for why recording is
// not automatic inside Command itself.
func RecordedInvocations() []Invocation {
	invocationsMu.Lock()
	defer invocationsMu.Unlock()
	return append([]Invocation(nil), invocations...)
}

// recordInvocation appends one genuinely-started CLI invocation to the
// package-wide recorder. Package-wide, not per-TestEnvironment, because the
// acceptance suite constructs a fresh TestEnvironment per scenario (or per
// journey) — a completeness gate that only saw the LAST environment's
// invocations would report almost everything as uncovered.
func recordInvocation(args []string) {
	inv := Invocation{
		Leaf: leafForArgs(args),
		Args: append([]string(nil), args...),
	}
	invocationsMu.Lock()
	invocations = append(invocations, inv)
	invocationsMu.Unlock()
}

// leafForArgs resolves argv to its cobra leaf command path (e.g. "ctxloom
// manage install") using the REAL command tree (cli.GetRootCmd()), the same
// tree completeness_test.go's own leafCommands() census walks.
//
// This is deliberately NOT a hand-rolled heuristic (e.g. "leading tokens
// that don't start with '-'"): a positional argument that happens not to
// start with '-' — a bundle ref in "ctxloom bundle sign my-bundle", say — is
// indistinguishable from a subcommand name under that rule, so a heuristic
// would misresolve the leaf. cobra's own Find walks the tree the same way
// real command dispatch does, so it does not have that failure mode.
func leafForArgs(args []string) string {
	root := cli.GetRootCmd()
	cmd, _, err := root.Find(args)
	if err != nil || cmd == nil {
		return ""
	}
	return cmd.CommandPath()
}

// RecordCommandStart records args as a genuinely-started invocation, for the
// handful of call sites that build their *exec.Cmd via Command rather than
// calling Run/RunWithStdin directly (isolation_probe.go's two live-agent
// probes, steps_j000700_team.go's runBob).
//
// THE DESIGN POINT: Command only BUILDS an *exec.Cmd — the caller decides
// whether, when, and how to start it (some override cmd.Dir, wire cmd.Stdout
// to their own buffers, wrap it in a timeout, etc.), and may end up never
// starting it at all. Recording automatically inside Command would credit a
// command that was merely constructed, which is exactly the vacuous-coverage
// shape this recorder exists to replace. So recording is NOT automatic here:
// each of the three current call sites calls RecordCommandStart itself,
// immediately once it knows the process has actually been started (right
// after the runWithTimeout/cmd.Run() call that starts it returns) — the same
// point in the command's lifecycle Run/RunWithStdin record at internally.
func (e *TestEnvironment) RecordCommandStart(args []string) {
	recordInvocation(args)
}
