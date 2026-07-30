package mockengine

import (
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// Runtime already guards a nil Stdin (readPrompt answers "no prompt arrived"),
// and already defaults a nil Getenv to the real process reader. Stdout and
// Stderr had neither treatment, so a partially constructed Runtime took a nil
// dereference inside fmt.Fprintf rather than degrading (U079-F18).
//
// The policy is the one getenv already sets: an absent injection means "use the
// process's own facility". Discarding would be worse than the panic — the
// instrument's entire output is evidence, and silently swallowing it is the
// silent no-op this package exists to catch.
func TestRuntime_NilWritersDoNotPanic(t *testing.T) {
	cli := agent.EngineCLI{
		Engine:  "codex",
		Surface: agent.CLISurfaceOneshot,
		Prompt:  agent.PromptStdin,
	}
	rt := &Runtime{
		CLI:    cli,
		Res:    Resolver{Cwd: t.TempDir(), Home: t.TempDir()},
		Getenv: func(string) string { return "" },
		Stdin:  strings.NewReader("hello"),
		// Stdout and Stderr deliberately left nil.
	}

	var code int
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Run panicked on a Runtime with nil writers: %v", r)
			}
		}()
		code = rt.Run()
	}()

	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
}

// The accessors must not invent a writer when one WAS injected: the report and
// the wire output still land where the caller put them.
func TestRuntime_InjectedWritersAreUsed(t *testing.T) {
	var stdout, stderr strings.Builder
	rt := &Runtime{
		CLI: agent.EngineCLI{
			Engine:  "codex",
			Surface: agent.CLISurfaceOneshot,
			Prompt:  agent.PromptStdin,
		},
		Res:    Resolver{Cwd: t.TempDir(), Home: t.TempDir()},
		Getenv: func(string) string { return "" },
		Stdin:  strings.NewReader("hello"),
		Stdout: &stdout,
		Stderr: &stderr,
	}
	if code := rt.Run(); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(stderr.String(), ReportBegin) {
		t.Error("the discovery report did not reach the injected Stderr")
	}
	if stdout.Len() == 0 {
		t.Error("the wire output did not reach the injected Stdout")
	}
}
