package mockengine

import (
	"fmt"
	"strconv"
	"strings"
)

// Sentinels are deterministic control markers a test embeds in the prompt to
// steer the mock's outcome. They match on a SUBSTRING of the incoming prompt,
// never equality: ctxloom PREPENDS composed context to the task, so the bytes
// the engine receives are never exactly the marker — an equals check would
// never fire. This mirrors internal/lm/backends/mock.go's env-driven control
// knobs, extended to prompt-embedded markers so a single-shot spawn a test does
// not control the env of can still be steered.
const (
	// SentinelFail makes the run exit nonzero — the fault path, so a test can
	// prove ctxloom surfaces a failing engine rather than swallowing it.
	SentinelFail = "CTXLOOM_MOCK_FAIL"
	// SentinelEcho makes the response echo the text that follows the marker on
	// the same line, so a test can round-trip a known token through the engine.
	SentinelEcho = "CTXLOOM_MOCK_ECHO:"

	// failExitCode is the nonzero code SentinelFail yields.
	failExitCode = 7
)

// Env knobs, mirroring internal/lm/backends/mock.go, for tests that DO control
// the child's environment.
const (
	// EnvExitCode overrides the exit code (wins over any sentinel).
	EnvExitCode = "CTXLOOM_MOCK_EXIT_CODE"
	// EnvResponse overrides the response text (wins over any sentinel).
	EnvResponse = "CTXLOOM_MOCK_RESPONSE"
	// EnvReportFile, when set, writes the discovery report JSON to that path in
	// addition to stderr — a robust channel when stdout/stderr are noisy or when
	// the report must survive a mounted-workspace boundary.
	EnvReportFile = "CTXLOOM_MOCK_REPORT_FILE"
)

// Outcome is a sentinel dispatch result: the exit code and the response text
// (the "result" the surface adapter renders onto the wire).
type Outcome struct {
	ExitCode int
	Response string
}

// Dispatch resolves a prompt (and the controlling environment) to an Outcome.
// Prompt-embedded sentinels decide first; explicit env knobs override, because a
// test that owns the child's env is making a deliberate, unambiguous request.
//
// lookup is the TWO-VALUE environment reader, for the same reason ObserveEnv
// takes one: an override SET TO THE EMPTY STRING is a request for an empty
// engine reply, and the one-value form cannot tell that from an unset variable.
// A zero-byte reply is the shape ctxloom's characteristic bug produces, so the
// knob that lets a test prove ctxloom surfaces one rather than papering over it
// must be expressible.
//
// A knob that is set but UNREADABLE is an error, not a default. EnvExitCode is
// the fault-INJECTION channel — a test sets it to prove ctxloom surfaces a
// failing engine — so a typo silently degrading to "success" would make the
// test that was asserting a failure go green having injected nothing.
func Dispatch(prompt string, lookup func(string) (string, bool)) (Outcome, error) {
	if lookup == nil {
		lookup = func(string) (string, bool) { return "", false }
	}

	out := Outcome{ExitCode: 0, Response: "mock-engine: ok"}

	switch {
	case strings.Contains(prompt, SentinelFail):
		out.ExitCode = failExitCode
		out.Response = "mock-engine: fail sentinel matched"
	case strings.Contains(prompt, SentinelEcho):
		out.Response = "mock-engine: " + echoPayload(prompt)
	}

	if v, ok := lookup(EnvResponse); ok {
		out.Response = v
	}
	if v, ok := lookup(EnvExitCode); ok && strings.TrimSpace(v) != "" {
		code, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return out, fmt.Errorf("%s=%q is not an integer exit code", EnvExitCode, v)
		}
		out.ExitCode = code
	}
	return out, nil
}

// echoPayload returns the rest of the line following the first SentinelEcho.
func echoPayload(prompt string) string {
	i := strings.Index(prompt, SentinelEcho)
	rest := prompt[i+len(SentinelEcho):]
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		rest = rest[:nl]
	}
	return strings.TrimSpace(rest)
}
