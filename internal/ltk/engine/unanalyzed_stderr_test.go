package engine

import (
	"strings"
	"testing"
)

// `Output.Stderr` was originally a half-built channel:
// cmd/ltk/evaluate.go consumed it (`if len(out.Stderr) > 0 { os.Stderr.Write(...) }`)
// while no production code ever put a byte in it. The fix made every
// Adapter.Encode emit the unanalyzed note into that channel, so an allow that
// was reached WITHOUT being able to parse the command stops being
// byte-identical to a clean, fully-checked allow.
//
// The fix shipped with no test of its own: deleting the `if resp.Unanalyzed`
// branch from BOTH adapters left every ltk package green (verified by
// removing them and running ./internal/ltk/... ./cmd/ltk/... ./tests/ltk/...
// — all ok). The only other Stderr producer, evaluate.go's "no rules config
// found" notice, is a different code path guarded by `resolved == ""`, so it
// could never catch this. These tests close that hole.
//
// They are table-driven over every registered Adapter deliberately: a future
// engine that implements Encode without the branch is the same defect
// reintroduced, and a per-adapter test would not see it.

// unanalyzedResponse is an allow that was reached without a full parse — the
// exact shape app.decide returns when on_parse_error lets an unparseable
// (or unparseable-nested) command through.
func unanalyzedResponse() Response {
	return Response{
		Allow:      true,
		Unanalyzed: true,
		ParseError: "a nested command (inside a wrapper such as bash -c/eval/cmd /c/pwsh -Command) could not be parsed",
	}
}

// TestEncodeUnanalyzedAllowWritesStderr is the payload assertion: an
// unanalyzed allow must put BYTES on Output.Stderr. Exit code and stdout stay
// exactly as a clean allow leaves them — this is diagnostic only and must
// never change the decision or the host's control flow.
func TestEncodeUnanalyzedAllowWritesStderr(t *testing.T) {
	for _, e := range All() {
		a, ok := e.(Adapter)
		if !ok {
			continue
		}
		t.Run(a.Name(), func(t *testing.T) {
			out, err := a.Encode(unanalyzedResponse())
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			if len(out.Stderr) == 0 {
				t.Fatalf("an unanalyzed allow must write a diagnostic to Output.Stderr, got a zero Output " +
					"— byte-identical to a clean allow")
			}
			// The note must name the cause, or it tells the operator nothing
			// they can act on.
			if !strings.Contains(string(out.Stderr), "could not fully analyze") {
				t.Errorf("stderr note must say the command could not be fully analyzed, got %q", out.Stderr)
			}
			if !strings.Contains(string(out.Stderr), "on_parse_error") {
				t.Errorf("stderr note must name the policy that allowed it, got %q", out.Stderr)
			}
			if !strings.Contains(string(out.Stderr), unanalyzedResponse().ParseError) {
				t.Errorf("stderr note must carry the ParseError detail, got %q", out.Stderr)
			}
			// The decision itself is unchanged: still an allow, so no deny
			// document on stdout and no non-zero exit.
			if len(out.Stdout) != 0 {
				t.Errorf("an allow must not write a decision document to stdout, got %q", out.Stdout)
			}
			if out.ExitCode != 0 {
				t.Errorf("an allow must stay exit 0, got %d", out.ExitCode)
			}
		})
	}
}

// TestEncodeCleanAllowStaysSilent is the other half of the vice. Without it,
// the test above could be satisfied by an adapter that writes the note on
// EVERY allow, which would flood the operator's stderr on every single tool
// call and destroy the signal the note exists to carry.
func TestEncodeCleanAllowStaysSilent(t *testing.T) {
	for _, e := range All() {
		a, ok := e.(Adapter)
		if !ok {
			continue
		}
		t.Run(a.Name(), func(t *testing.T) {
			out, err := a.Encode(Response{Allow: true})
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			if len(out.Stderr) != 0 {
				t.Errorf("a clean, fully-analyzed allow must stay silent, got stderr=%q", out.Stderr)
			}
		})
	}
}

// TestUnanalyzedNoteDegradesWithoutDetail: ParseError is empty on at least one
// construction path (app.go's truncated-depth deny), so the note must still
// render something rather than a dangling "(...)" with nothing in it.
func TestUnanalyzedNoteDegradesWithoutDetail(t *testing.T) {
	note := unanalyzedNote(Response{Allow: true, Unanalyzed: true})
	if !strings.Contains(note, "reason unavailable") {
		t.Errorf("an unanalyzed note with no ParseError must say so explicitly, got %q", note)
	}
}
