//go:build acceptance

package acceptance

import (
	"reflect"
	"testing"
)

// TestCtxloomArgs_QuotedArgumentSurvivesAsOneWord pins that ctxloomArgs
// used to split on bare whitespace (strings.Fields), so the acceptance suite
// structurally could not express a quoted argument, an argument containing
// spaces, or a flag whose value is a sentence — exactly the argv shapes
// where a variadic-flag defect lives. A feature-file line like
// `ctxloom run --one-shot 'write a file'` must produce ["run", "--one-shot",
// "write a file"], a single trailing argument, not four separate words.
func TestCtxloomArgs_QuotedArgumentSurvivesAsOneWord(t *testing.T) {
	got, err := ctxloomArgs(`ctxloom run --one-shot 'write a file'`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"run", "--one-shot", "write a file"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ctxloomArgs = %#v, want %#v", got, want)
	}
}

// TestCtxloomArgs_DoubleQuotedArgumentSurvivesAsOneWord covers the
// double-quote form with an escaped inner quote.
func TestCtxloomArgs_DoubleQuotedArgumentSurvivesAsOneWord(t *testing.T) {
	got, err := ctxloomArgs(`ctxloom run --one-shot "say \"hi\" now"`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"run", "--one-shot", `say "hi" now`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ctxloomArgs = %#v, want %#v", got, want)
	}
}

// TestCtxloomArgs_UnquotedStillSplitsOnWhitespace is the ordinary case,
// unchanged by the fix.
func TestCtxloomArgs_UnquotedStillSplitsOnWhitespace(t *testing.T) {
	got, err := ctxloomArgs("ctxloom run --agent reviewer")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"run", "--agent", "reviewer"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ctxloomArgs = %#v, want %#v", got, want)
	}
}
