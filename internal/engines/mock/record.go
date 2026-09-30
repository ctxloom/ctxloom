package mock

import (
	"fmt"
	"os"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/shared/containerprobe"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// The mock's TEST CONTROL knobs, read off the engine's env (the exec's env,
// then the process's). They are how a hermetic test scripts the engine and
// reads back what reached it.
const (
	// EnvResponse replaces the echo. SET to "" is a deliberate empty reply.
	EnvResponse = "CTXLOOM_MOCK_RESPONSE"
	// EnvRecordFile names the file the turn's evidence is written to.
	EnvRecordFile = "CTXLOOM_MOCK_RECORD_FILE"
	// EnvFailPrefix marks the reply as the failure outcome (FailPrefix).
	EnvFailPrefix = "CTXLOOM_MOCK_FAIL_PREFIX"
	// EnvExitCode is the process's exit code; non-zero fails the turn.
	EnvExitCode = "CTXLOOM_MOCK_EXIT_CODE"
)

// FailPrefix marks a response as the failure outcome WITHOUT discarding the
// evidence of what the engine actually observed. It is deliberately
// additive: a failure that replaced the response with a constant would
// render identically whether or not ctxloom delivered anything, and the
// mock's class gate (mock/runtime's arch test) forbids exactly that — "a
// limb that renders identically either way is not evidence". Prefixing
// instead of replacing is what lets a NEGATIVE scenario assert positively:
// the run can only produce "FAIL" followed by the observed context if the
// engine was actually reached and the value actually flowed, where asserting
// the ABSENCE of something is satisfied just as well by an engine that never
// launched.
const FailPrefix = "FAIL"

// LookupEnv reads a knob: the exact key on env, then the lowercase the
// config parser may produce, then the process environment. Two-valued
// because "set to empty" and "unset" must be distinguishable —
// CTXLOOM_MOCK_RESPONSE="" is a request for an EMPTY engine reply, and a
// zero-byte reply is the exact shape this project's characteristic bug
// produces (exit 0, a success message, nothing written); a mock that
// cannot be asked for one cannot be used to prove ctxloom surfaces it.
func LookupEnv(env map[string]string, key string) (string, bool) {
	if env != nil {
		if v, ok := env[key]; ok {
			return v, true
		}
		if v, ok := env[strings.ToLower(key)]; ok {
			return v, true
		}
	}
	return os.LookupEnv(key)
}

// Env is LookupEnv's one-value form.
func Env(env map[string]string, key string) string {
	v, _ := LookupEnv(env, key)
	return v
}

// Record is what a record is written FROM: the mode, the working directory,
// the env the engine saw, the context and prompt as delivered, and the
// evidence of the delivered settings (the deny list) and skills. Every arm
// of the mock leaves the same evidence, because a scenario asserting WHERE
// an engine ran, or WHAT reached it, must not first have to know which arm
// the run took.
type Record struct {
	Mode          int32
	WorkDir       string
	Env           map[string]string
	Context       string
	Prompt        string
	FragmentCount int
	DenyTools     []string
	Skills        []string
	// HomeEnvKeys are the engine home-relocation variables to echo when
	// set, so a test can prove what config-home env the engine received.
	HomeEnvKeys []string
	// Posture is the permission posture a structured turn ran at; nil for
	// a run that has none (the mock binary's own launches).
	Posture *engine.TurnPosture
}

// WriteRecord renders one record to file. WHERE THE ENGINE RAN is recorded
// in two independent signals, because neither is sufficient alone:
// container_markers is a heuristic that reads TRUE on both sides when the
// test harness itself runs inside a devcontainer; hostname breaks the tie —
// a container gets its own UTS namespace regardless of how paths are
// mapped, so it never matches the launching process's hostname. A write
// failure is an error, not a warning: reporting success with no record file
// lets a later assertion silently read a STALE record from a previous run.
func WriteRecord(file string, in Record) error {
	if file == "" {
		return nil
	}
	var input strings.Builder
	input.WriteString("=== Arguments ===\n")
	_, _ = fmt.Fprintf(&input, "mode=%d\n", in.Mode)
	_, _ = fmt.Fprintf(&input, "fragments=%d\n", in.FragmentCount)
	if cwd, err := os.Getwd(); err == nil {
		_, _ = fmt.Fprintf(&input, "cwd=%s\n", cwd)
	} else {
		_, _ = fmt.Fprintf(&input, "cwd=<error: %v>\n", err)
	}
	_, _ = fmt.Fprintf(&input, "workdir=%s\n", in.WorkDir)
	if host, err := os.Hostname(); err == nil {
		_, _ = fmt.Fprintf(&input, "hostname=%s\n", host)
	} else {
		_, _ = fmt.Fprintf(&input, "hostname=<error: %v>\n", err)
	}
	_, _ = fmt.Fprintf(&input, "container_markers=%s\n", strings.Join(containerprobe.Markers(), ","))
	input.WriteString("=== Env ===\n")
	for _, key := range in.HomeEnvKeys {
		if v := Env(in.Env, key); v != "" {
			_, _ = fmt.Fprintf(&input, "%s=%s\n", key, v)
		}
	}
	// Recorded even when empty, so a scenario asserting ABSENCE is possible.
	input.WriteString("=== DenyTools ===\n")
	for _, t := range in.DenyTools {
		_, _ = fmt.Fprintf(&input, "%s\n", t)
	}
	input.WriteString("=== Skills ===\n")
	for _, s := range in.Skills {
		_, _ = fmt.Fprintf(&input, "%s\n", s)
	}
	if p := in.Posture; p != nil {
		input.WriteString("=== Posture ===\n")
		_, _ = fmt.Fprintf(&input, "mode=%s\n", postureMode(*p))
		for _, g := range p.Grants {
			_, _ = fmt.Fprintf(&input, "grant=%s\n", g)
		}
		trust := "untrusted"
		if p.Trust == engine.TrustTrusted {
			trust = "trusted"
		}
		_, _ = fmt.Fprintf(&input, "trust=%s\n", trust)
	}
	input.WriteString("=== Context ===\n")
	input.WriteString(in.Context)
	input.WriteString("\n=== Prompt ===\n")
	input.WriteString(in.Prompt)
	input.WriteString("\n")
	if err := iox.WriteFileAtomic(file, []byte(input.String()), 0o644); err != nil {
		return fmt.Errorf("failed to write mock record file %q: %w", file, err)
	}
	return nil
}

// Response is the reply: the custom response when SET (an empty one is an
// empty reply), else the default echo of mode/fragments/context/prompt
// (plus a distilled marker for distill or compress contexts). failPrefix
// prepends FailPrefix to WHATEVER is produced, custom or echo — orthogonal
// to the response knob on purpose: that knob REPLACES the response, so a
// test using it to signal failure can only assert a literal it wrote
// itself, which proves nothing about what reached the engine.
func Response(custom string, hasCustom bool, contextStr, prompt string, mode int32, fragmentCount int, failPrefix bool) string {
	var response strings.Builder
	if failPrefix {
		response.WriteString(FailPrefix + "\n")
	}
	if hasCustom {
		response.WriteString(custom)
		return response.String()
	}
	_, _ = fmt.Fprintf(&response, "[mock] mode=%d\n", mode)
	_, _ = fmt.Fprintf(&response, "[mock] fragments=%d\n", fragmentCount)
	if contextStr != "" {
		_, _ = fmt.Fprintf(&response, "[mock] context_length=%d\n", len(contextStr))
		// The CONTENT, not just its length: a cross-engine delegation
		// journey proves guidance present in one child's context and absent
		// from a sibling's through each child's OWN output, which a length
		// number cannot carry and verbatim text can.
		_, _ = fmt.Fprintf(&response, "[mock] context=%s\n", contextStr)
	}
	if prompt != "" {
		_, _ = fmt.Fprintf(&response, "[mock] prompt=%s\n", prompt)
	}
	if strings.Contains(contextStr, "distill") || strings.Contains(contextStr, "compress") {
		response.WriteString("[mock] distilled=Compressed content for testing\n")
	}
	return response.String()
}
