package mockengine

import (
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// CTXLOOM_MOCK_EXIT_CODE is the mock's fault-INJECTION channel: a test sets it
// to prove ctxloom surfaces a failing engine. A typo in the value used to be
// dropped on the floor, so the injection silently degraded to "success" and the
// test asserting a failure went green having injected nothing.
func TestDispatch_MalformedExitCodeIsLoud(t *testing.T) {
	for _, bad := range []string{"seven", "7x", "1.5", "0x7"} {
		_, err := Dispatch("hello", envMap(map[string]string{EnvExitCode: bad}))
		if err == nil {
			t.Errorf("CTXLOOM_MOCK_EXIT_CODE=%q was accepted; the fault injection silently became a success", bad)
		}
	}
}

// A well-formed value still wins over any sentinel, surrounding whitespace is
// still tolerated, and a BLANK value still means "no override" as it always has
// — the refusal is scoped to a value that was written and cannot be read.
func TestDispatch_WellFormedExitCodeStillWins(t *testing.T) {
	for _, good := range []struct {
		raw  string
		want int
	}{
		{"0", 0}, {"3", 3}, {" 9 ", 9}, {"-1", -1},
		{"", failExitCode},    // blank: no override, the sentinel stands
		{"   ", failExitCode}, // blank after trimming: likewise
	} {
		out, err := Dispatch(SentinelFail, envMap(map[string]string{EnvExitCode: good.raw}))
		if err != nil {
			t.Fatalf("CTXLOOM_MOCK_EXIT_CODE=%q: %v", good.raw, err)
		}
		if out.ExitCode != good.want {
			t.Errorf("CTXLOOM_MOCK_EXIT_CODE=%q gave exit %d, want %d (the env knob must win over the fail sentinel)",
				good.raw, out.ExitCode, good.want)
		}
	}
}

// End to end: the malformed knob fails the run rather than exiting 0.
func TestRuntime_MalformedExitCodeKnobFailsTheRun(t *testing.T) {
	var stdout, stderr strings.Builder
	rt := &Runtime{
		CLI: agent.EngineCLI{
			Engine:  "codex",
			Surface: agent.CLISurfaceOneshot,
			Prompt:  agent.PromptStdin,
		},
		Res:       Resolver{Cwd: t.TempDir(), Home: t.TempDir()},
		LookupEnv: envMap(map[string]string{EnvExitCode: "seven"}),
		Stdin:     strings.NewReader("hello"),
		Stdout:    &stdout,
		Stderr:    &stderr,
	}
	if code := rt.Run(); code == 0 {
		t.Error("a malformed CTXLOOM_MOCK_EXIT_CODE left the run exiting 0")
	}
	if !strings.Contains(stderr.String(), EnvExitCode) {
		t.Errorf("the refusal must name the knob it refused, got:\n%s", stderr.String())
	}
}
