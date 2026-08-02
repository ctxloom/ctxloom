package mockengine

import (
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// envMap builds a two-value reader over a fixed map, so a test can express
// "set to the empty string" as distinct from "unset" — the distinction the
// tests below turn on.
func envMap(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

// ctxloom's characteristic bug is exit 0 with a success message and zero bytes
// delivered. CTXLOOM_MOCK_RESPONSE is the knob that would let a test prove
// ctxloom SURFACES a zero-byte engine reply rather than papering over it — and
// it was unreachable, because the one-value read could not tell
// CTXLOOM_MOCK_RESPONSE="" from an unset variable.
func TestDispatch_EmptyResponseCanBeRequested(t *testing.T) {
	out, err := Dispatch("hello", envMap(map[string]string{EnvResponse: ""}))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if out.Response != "" {
		t.Errorf("Response = %q, want the empty reply that was explicitly requested", out.Response)
	}
	if out.ExitCode != 0 {
		t.Errorf("an empty response is a successful run, got exit %d", out.ExitCode)
	}
}

// An UNSET variable must still leave the default (or the sentinel's) reply
// alone — otherwise every run would deliver nothing.
func TestDispatch_UnsetResponseLeavesTheDefault(t *testing.T) {
	out, err := Dispatch("hello", envMap(map[string]string{}))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if out.Response == "" {
		t.Error("an unset CTXLOOM_MOCK_RESPONSE must not be read as a request for an empty reply")
	}

	echoed, err := Dispatch(SentinelEcho+"token-42\n", envMap(map[string]string{}))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if !strings.Contains(echoed.Response, "token-42") {
		t.Errorf("the echo sentinel was lost: %q", echoed.Response)
	}
}

// End to end on the wire: codex's oneshot adapter streams the response straight
// through, so an explicitly empty reply must reach stdout as zero bytes — the
// shape a test needs in order to assert ctxloom does not silently substitute
// something for it.
func TestRuntime_EmptyResponseReachesTheWireAsZeroBytes(t *testing.T) {
	var stdout, stderr strings.Builder
	rt := &Runtime{
		CLI: agent.EngineCLI{
			Engine:  "codex",
			Surface: agent.CLISurfaceOneshot,
			Prompt:  agent.PromptStdin,
		},
		Res:       Resolver{Cwd: t.TempDir(), Home: t.TempDir()},
		LookupEnv: envMap(map[string]string{EnvResponse: ""}),
		Stdin:     strings.NewReader("hello"),
		Stdout:    &stdout,
		Stderr:    &stderr,
	}
	if code := rt.Run(); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want zero bytes", stdout.String())
	}
	// The evidence channel is untouched: a zero-byte REPLY is not a zero-byte RUN.
	if !strings.Contains(stderr.String(), ReportBegin) {
		t.Error("the discovery report went missing on an empty-response run")
	}
}
