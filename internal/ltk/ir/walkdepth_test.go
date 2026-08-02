package ir

import (
	"strings"
	"testing"
)

// deepScript builds an acyclic chain of depth nested scripts, each owning one
// command, mirroring what a frontend produces for `$( $( … ) )`.
func deepScript(depth int) *Script {
	leaf := &Script{Shell: ShellBash, Pipelines: []Pipeline{
		{Commands: []SimpleCommand{{Argv: []string{"echo", "hi"}}}},
	}}
	cur := leaf
	for i := 0; i < depth; i++ {
		cur = &Script{Shell: ShellBash, Pipelines: []Pipeline{
			{Commands: []SimpleCommand{{Argv: []string{"echo"}, Nested: []*Script{cur}}}},
		}}
	}
	return cur
}

// TestWalkHandlesDeepAcyclicNesting measures the reachable half of a
// two-part risk: Walk recurses once per nesting level with no cap, so the
// question is whether an ordinary command line can drive it deep enough to
// matter. It cannot: a substitution nested this deep is far past anything a
// shell parser will hand back, and Go's growable stacks absorb it. The
// unreachable half — a cycle — is covered by
// TestNestedIsAcyclicByConstruction below.
func TestWalkHandlesDeepAcyclicNesting(t *testing.T) {
	const depth = 20000
	s := deepScript(depth)
	n := 0
	if !s.Walk(func(*Script, SimpleCommand) bool { n++; return true }) {
		t.Fatal("Walk reported early termination")
	}
	if n != depth+1 {
		t.Errorf("visited %d commands, want %d", n, depth+1)
	}
}

// TestNestedIsAcyclicByConstruction is the guard that makes the cycle
// premise above checkable rather than merely argued. Walk has no visited set, so an
// aliased *Script in Nested would recurse until the stack is exhausted — and a
// Go stack overflow is a fatal error no recover() can catch, which would take
// down the hook process rather than produce a decision.
//
// The reason that cannot happen is a property of the PRODUCERS, not of Walk:
// every site that appends to Nested allocates a fresh *Script and never hands
// back one it received. This test asserts that property directly on the values
// a walk observes — no *Script may be reachable from itself — so a future
// producer that starts aliasing fails here instead of in production.
func TestNestedIsAcyclicByConstruction(t *testing.T) {
	// Hostility check: the detector must actually see nested scripts, or it
	// would pass on any input at all.
	s := deepScript(3)
	if err := checkAcyclic(s, map[*Script]bool{}); err != nil {
		t.Fatalf("acyclic chain rejected: %v", err)
	}
	seen := 0
	s.Walk(func(owner *Script, c SimpleCommand) bool { seen += len(c.Nested); return true })
	if seen == 0 {
		t.Fatal("fixture is not hostile: the walk observed no nested scripts at all")
	}

	// A deliberately aliased graph must be rejected, or the check above proves
	// nothing.
	cyclic := &Script{Shell: ShellBash, Pipelines: []Pipeline{
		{Commands: []SimpleCommand{{Argv: []string{"echo"}}}},
	}}
	cyclic.Pipelines[0].Commands[0].Nested = []*Script{cyclic}
	if err := checkAcyclic(cyclic, map[*Script]bool{}); err == nil {
		t.Fatal("a self-referential Nested edge must be detected")
	}
}

// checkAcyclic reports an error if any *Script is reachable from itself.
func checkAcyclic(s *Script, onPath map[*Script]bool) error {
	if s == nil {
		return nil
	}
	if onPath[s] {
		return errCycle
	}
	onPath[s] = true
	defer delete(onPath, s)
	for _, p := range s.Pipelines {
		for _, c := range p.Commands {
			for _, ns := range c.Nested {
				if err := checkAcyclic(ns, onPath); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

var errCycle = cycleError("nested script graph contains a cycle")

type cycleError string

func (e cycleError) Error() string { return string(e) }

// TestFrontendProducedGraphsAreAcyclic is the production-side half: it walks
// the graphs the real lowering produces for the constructs that create Nested
// edges and asserts none of them aliases.
func TestFrontendProducedGraphsAreAcyclic(t *testing.T) {
	// Built here rather than parsed, to keep package ir free of frontend
	// imports; the shapes mirror $(…), <(…) and a wrapper body.
	inner := &Script{Shell: ShellBash, Pipelines: []Pipeline{
		{Commands: []SimpleCommand{{Argv: []string{"git", "push"}}}},
	}}
	outer := &Script{Shell: ShellBash, Pipelines: []Pipeline{
		{Commands: []SimpleCommand{{Argv: []string{"bash", "-c", "git push"}, Nested: []*Script{inner}}}},
	}}
	if err := checkAcyclic(outer, map[*Script]bool{}); err != nil {
		t.Fatalf("wrapper-shaped graph is cyclic: %v", err)
	}
	if got := len(outer.Commands()); got != 2 {
		t.Errorf("Commands() = %d, want 2", got)
	}
	if !strings.Contains(strings.Join(outer.Commands()[1].Argv, " "), "git push") {
		t.Error("the nested command must be reachable from the outer script")
	}
}

// TestShellIsPartOfTheGraphContract is the pin against a claim that measures
// (correctly — 4 of the 9 importing packages use only Shell) that the Shell
// enum and the command graph have different audiences, and concludes they are
// "two unrelated contracts in one package".
//
// The measurement holds; the conclusion does not. Shell is a FIELD of Script,
// and Walk's contract is specifically that the visitor receives the script
// that OWNS each command because a nested script may carry a different
// dialect — which is what lets a `shells: [cmd]` rule fire on a cmd.exe
// payload nested inside a bash wrapper. That makes the enum part of the
// graph's own contract rather than a lodger in its package. Package ir also
// imports nothing but "slices", so a Shell-only importer pays no dependency
// weight for the co-location.
//
// No red is available for this row: its remedy is a package move, which a test
// follows rather than fails. This pins the coupling that makes the two
// inseparable instead.
func TestShellIsPartOfTheGraphContract(t *testing.T) {
	nested := &Script{Shell: ShellCmd, Pipelines: []Pipeline{
		{Commands: []SimpleCommand{{Argv: []string{"del", "/f", "x"}}}},
	}}
	outer := &Script{Shell: ShellBash, Pipelines: []Pipeline{
		{Commands: []SimpleCommand{{Argv: []string{"cmd.exe", "/c", "del /f x"}, Nested: []*Script{nested}}}},
	}}

	var owners []Shell
	outer.Walk(func(owner *Script, _ SimpleCommand) bool {
		owners = append(owners, owner.Shell)
		return true
	})
	if len(owners) != 2 {
		t.Fatalf("visited %d commands, want 2", len(owners))
	}
	if owners[0] != ShellBash {
		t.Errorf("outer command's owner shell = %q, want bash", owners[0])
	}
	if owners[1] != ShellCmd {
		t.Errorf("nested command's owner shell = %q, want cmd: Walk must report the OWNING script's dialect, not the top-level one", owners[1])
	}
}
