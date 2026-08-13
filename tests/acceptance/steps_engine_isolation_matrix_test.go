//go:build acceptance

package acceptance

import (
	"errors"
	"strings"
	"testing"
)

// The engine × isolation matrix's verdict is the one piece of that floor a
// hermetic test can reach: every cell is @live, so nothing else here ever
// executes matrixAssert, and its whole value is that it distinguishes failure
// SHAPES rather than reporting a bare mismatch. A regression that collapsed
// those branches into one message would still pass every live cell and would
// silently destroy the matrix's diagnostic value — which is the entire point of
// running it.
//
// It also pins the strictness itself. A future "just make the cells pass" edit
// would most naturally strip code fences or accept a prose preamble; the fenced
// and preamble cases below are RED by design and are the executable record of
// that decision.

func matrixCell(nonce, stdout string) *matrixState {
	return &matrixState{
		engine: "claude-code", runtime: "host", workspace: "none",
		nonce: nonce, stdout: stdout,
	}
}

func TestMatrixAssert_ExactObjectPasses(t *testing.T) {
	const nonce = "CTXLOOM-HELLO-abc123"
	if err := matrixAssert(matrixCell(nonce, `{"hello":"CTXLOOM-HELLO-abc123"}`)); err != nil {
		t.Fatalf("exact object must pass, got: %v", err)
	}
}

// Whitespace is the ONLY thing trimmed, and JSON's own insignificant whitespace
// is semantic, not cosmetic: an engine that pretty-prints has still obeyed the
// contract. Everything else an engine might add has not.
func TestMatrixAssert_SurroundingAndInternalWhitespaceIsTolerated(t *testing.T) {
	const nonce = "CTXLOOM-HELLO-abc123"
	if err := matrixAssert(matrixCell(nonce, "\n  { \"hello\" : \"CTXLOOM-HELLO-abc123\" }  \n")); err != nil {
		t.Fatalf("whitespace-only differences must pass, got: %v", err)
	}
}

func TestMatrixAssert_RunFailureIsReportedWithStderr(t *testing.T) {
	m := matrixCell("CTXLOOM-HELLO-abc123", "")
	m.runErr = errors.New("exit status 3")
	m.exitCode = 3
	m.stderr = "ctxloom: isolation finding: container could not start"
	err := matrixAssert(m)
	if err == nil {
		t.Fatal("a failed run must not pass")
	}
	if !strings.Contains(err.Error(), "the run itself failed") ||
		!strings.Contains(err.Error(), "container could not start") {
		t.Fatalf("a failed run must be named as such and carry stderr, got: %v", err)
	}
}

// ctxloom's characteristic bug: exit 0, success-shaped, zero bytes. It must be
// named as a silent no-op, never blurred into "some other mismatch".
func TestMatrixAssert_EmptyStdoutOnCleanExitIsNamedASilentNoOp(t *testing.T) {
	err := matrixAssert(matrixCell("CTXLOOM-HELLO-abc123", "   \n  "))
	if err == nil {
		t.Fatal("empty stdout must not pass")
	}
	if !strings.Contains(err.Error(), "silent no-op") {
		t.Fatalf("empty stdout must be named a silent no-op, got: %v", err)
	}
}

// The strictness decision, made executable: fences are a FINDING, not something
// to strip.
func TestMatrixAssert_MarkdownFencesAreAFormatFailure(t *testing.T) {
	const nonce = "CTXLOOM-HELLO-abc123"
	fenced := "```json\n{\"hello\":\"CTXLOOM-HELLO-abc123\"}\n```"
	err := matrixAssert(matrixCell(nonce, fenced))
	if err == nil {
		t.Fatal("code fences must not pass — loosening this deletes the signal the matrix exists to produce")
	}
	if !strings.Contains(err.Error(), "OUTPUT-FORMAT failure") {
		t.Fatalf("fences must be reported as an output-format failure, got: %v", err)
	}
}

func TestMatrixAssert_PreambleIsAFormatFailure(t *testing.T) {
	const nonce = "CTXLOOM-HELLO-abc123"
	err := matrixAssert(matrixCell(nonce, "Here is your JSON:\n{\"hello\":\"CTXLOOM-HELLO-abc123\"}"))
	if err == nil {
		t.Fatal("a prose preamble must not pass")
	}
	if !strings.Contains(err.Error(), "OUTPUT-FORMAT failure") {
		t.Fatalf("a preamble must be reported as an output-format failure, got: %v", err)
	}
}

// The attribution that makes a red cell actionable: well-formed JSON with no
// trace of the nonce means the engine never saw the composed context — a
// different subsystem from output formatting, and it must say so.
func TestMatrixAssert_WellFormedJSONWithoutTheNonceIsAContextDeliveryFailure(t *testing.T) {
	err := matrixAssert(matrixCell("CTXLOOM-HELLO-abc123", `{"hello":"world"}`))
	if err == nil {
		t.Fatal("a JSON object without the nonce must not pass")
	}
	if !strings.Contains(err.Error(), "CONTEXT-DELIVERY failure") {
		t.Fatalf("a missing nonce must be attributed to context delivery, got: %v", err)
	}
}

func TestMatrixAssert_ExtraKeysAreAShapeFailure(t *testing.T) {
	const nonce = "CTXLOOM-HELLO-abc123"
	err := matrixAssert(matrixCell(nonce, `{"hello":"CTXLOOM-HELLO-abc123","note":"done"}`))
	if err == nil {
		t.Fatal("an extra key must not pass")
	}
	if !strings.Contains(err.Error(), "SHAPE failure") {
		t.Fatalf("an extra key must be reported as a shape failure, got: %v", err)
	}
}

// A JSON array, or any non-object, is a format failure rather than a panic.
func TestMatrixAssert_NonObjectJSONIsAFormatFailure(t *testing.T) {
	err := matrixAssert(matrixCell("CTXLOOM-HELLO-abc123", `["CTXLOOM-HELLO-abc123"]`))
	if err == nil {
		t.Fatal("a JSON array must not pass")
	}
	if !strings.Contains(err.Error(), "OUTPUT-FORMAT failure") {
		t.Fatalf("a non-object must be reported as an output-format failure, got: %v", err)
	}
}

// Every failure names the cell, because this floor's output is read as a
// matrix: a message that does not say which cell produced it is unusable in the
// one context it will ever be read in.
func TestMatrixAssert_EveryFailureNamesItsCell(t *testing.T) {
	m := matrixCell("CTXLOOM-HELLO-abc123", "not json at all")
	m.runtime, m.workspace = "container", "worktree"
	err := matrixAssert(m)
	if err == nil {
		t.Fatal("expected a failure")
	}
	for _, want := range []string{"engine=claude-code", "runtime=container", "workspace=worktree"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("failure must name its cell (%s missing), got: %v", want, err)
		}
	}
}
