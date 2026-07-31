package shell_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/ctxloom/ctxloom/internal/ltk/frontend/shell"
	"github.com/ctxloom/ctxloom/internal/ltk/ir"
)

// These characterize U072-F05 and U071-F04, which are the two halves of one
// property: the IR has no way to say a word could not be resolved, so a
// partially-erased command is indistinguishable from a shorter one that was
// typed in full — on the ordinary expansion path (U072-F05) and on the
// expansion-error fallback path (U071-F04) alike.
//
// The erasure itself is deliberate and documented on the package: values ltk
// cannot know expand to empty "and are simply not matched", which fails open
// on purpose for a cooperative redirect. What is NOT decided is whether the
// erasure should leave a trace a caller can act on. Adding one means a new IR
// field plus a policy for it, and this package twice records the opposite
// house rule in code — Raw and a speculative wrapper shell parameter were both
// deleted for having no consumer, each with a "re-add it once a real consumer
// lands" note. So these pins record the present contract; they go red if a
// trace is added, which is exactly when the prose here needs revisiting.

func parseBash(t *testing.T, src string) *ir.Script {
	t.Helper()
	s, err := shell.New().Parse(context.Background(), ir.ShellBash, src)
	if err != nil {
		t.Fatalf("Parse(%q): %v", src, err)
	}
	return s
}

// TestUnresolvableWordIsErasedWithoutATrace pins the core of U072-F05: an
// unset variable in program position does not merely fail to resolve, it
// vanishes, and the argument after it becomes argv[0]. The resulting command
// is byte-for-byte what the shorter command line would have produced, so no
// consumer downstream can tell the two apart.
func TestUnresolvableWordIsErasedWithoutATrace(t *testing.T) {
	erased := parseBash(t, `$LTK_DEFINITELY_UNSET push --force`).Commands()
	typed := parseBash(t, `push --force`).Commands()

	if len(erased) != 1 || len(typed) != 1 {
		t.Fatalf("shape: erased=%d typed=%d", len(erased), len(typed))
	}
	// Hostility check: the fixture must actually have lost a word, or the
	// equality below is trivially true for the wrong reason.
	if erased[0].Program() != "push" {
		t.Fatalf("fixture is not hostile: program = %q, want the erased word to have shifted push into argv[0]", erased[0].Program())
	}
	if !reflect.DeepEqual(erased[0], typed[0]) {
		t.Errorf("erased %+v differs from typed %+v; the IR now carries a trace of the unresolved word", erased[0], typed[0])
	}
}

// TestDegradedFallbackArgvIsUnmarked pins the U071-F04 half: when expansion
// ERRORS the lowerer silently substitutes a literal-text guess, and the
// command it produces is likewise indistinguishable from a resolved one.
func TestDegradedFallbackArgvIsUnmarked(t *testing.T) {
	requireExpansionFails(t, degradingCommand)

	guessed := parseBash(t, degradingCommand).Commands()
	if len(guessed) != 1 {
		t.Fatalf("commands = %d, want 1", len(guessed))
	}
	if got, want := guessed[0].Argv, []string{"git", "push", "--force"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("fixture is not hostile: argv = %v, want %v (the fallback path must have run)", got, want)
	}
	resolved := parseBash(t, `git push --force`).Commands()[0]
	if !reflect.DeepEqual(guessed[0], resolved) {
		t.Errorf("guessed %+v differs from resolved %+v; the IR now marks a degraded argv", guessed[0], resolved)
	}
}
