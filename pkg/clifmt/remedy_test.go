package clifmt

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// fixed is a minimal Remedier error, standing in for any caller's
// remediable type: clifmt recognises it structurally, never by import.
type fixed struct {
	msg, fix string
	err      error
}

func (f fixed) Error() string  { return f.msg }
func (f fixed) Remedy() string { return f.fix }
func (f fixed) Unwrap() error  { return f.err }

const testFix = "run the thing"

func TestRemedyOf_FindsFixThroughWrapChain(t *testing.T) {
	err := fmt.Errorf("outer: %w", fmt.Errorf("mid: %w", fixed{msg: "inner", fix: testFix}))
	got, ok := RemedyOf(err)
	if !ok || got != testFix {
		t.Fatalf("RemedyOf = %q, %v; want %q, true", got, ok, testFix)
	}
}

func TestRemedyOf_EmptyRemedyDoesNotHideCause(t *testing.T) {
	err := fixed{msg: "outer", err: fixed{msg: "inner", fix: testFix}}
	if got, _ := RemedyOf(err); got != testFix {
		t.Fatalf("RemedyOf = %q; want the cause's %q", got, testFix)
	}
}

func TestRemedyOf_OutermostWins(t *testing.T) {
	err := fixed{msg: "outer", fix: "outer fix", err: fixed{msg: "inner", fix: testFix}}
	if got, _ := RemedyOf(err); got != "outer fix" {
		t.Fatalf("RemedyOf = %q; want the outermost fix", got)
	}
}

func TestRemedyOf_WalksJoinedErrors(t *testing.T) {
	err := errors.Join(errors.New("plain"), fmt.Errorf("w: %w", fixed{msg: "x", fix: testFix}))
	if got, ok := RemedyOf(err); !ok || got != testFix {
		t.Fatalf("RemedyOf(join) = %q, %v; want %q", got, ok, testFix)
	}
}

func TestRemedyOf_NoneFound(t *testing.T) {
	if got, ok := RemedyOf(fmt.Errorf("a: %w", errors.New("b"))); ok || got != "" {
		t.Fatalf("RemedyOf = %q, %v; want empty, false", got, ok)
	}
	if _, ok := RemedyOf(nil); ok {
		t.Fatal("RemedyOf(nil) reported a remedy")
	}
}

func TestFixLine(t *testing.T) {
	if got := FixLine("  ", ""); got != "" {
		t.Errorf("FixLine with no remedy = %q; want empty", got)
	}
	if got, want := FixLine("  ", testFix), "\n  fix: "+testFix; got != want {
		t.Errorf("FixLine = %q; want %q", got, want)
	}
}

// TestRenderError_FillsRemedyInJSON pins that a fix raised behind a %w wrap
// reaches the machine-readable envelope under the "remedy" key.
func TestRenderError_FillsRemedyInJSON(t *testing.T) {
	var buf bytes.Buffer
	err := fmt.Errorf("ctx: %w", fixed{msg: "boom", fix: testFix})
	if rerr := RenderError(&buf, err, FormatJSON); rerr != nil {
		t.Fatalf("RenderError: %v", rerr)
	}
	var env ErrorEnvelope
	if jerr := json.Unmarshal(buf.Bytes(), &env); jerr != nil {
		t.Fatalf("decode %q: %v", buf.String(), jerr)
	}
	if env.Remedy != testFix || env.Error != "ctx: boom" {
		t.Fatalf("envelope = %+v; want remedy %q", env, testFix)
	}
}

// TestRenderError_TextFixMatchesFixLine binds the reflective `label:"fix"`
// text line to FixLine, the one human form every listing uses.
func TestRenderError_TextFixMatchesFixLine(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderError(&buf, fixed{msg: "boom", fix: testFix}, FormatText); err != nil {
		t.Fatalf("RenderError: %v", err)
	}
	if !strings.Contains(buf.String(), FixLine("", testFix)) {
		t.Fatalf("text %q does not contain FixLine %q", buf.String(), FixLine("", testFix))
	}
}

func TestRenderError_NoRemedyOmitsKey(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderError(&buf, errors.New("boom"), FormatJSON); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), `"remedy"`) {
		t.Fatalf("remedy key present without a remedy: %s", buf.String())
	}
}

func TestEncodeWarning_CarriesRemedy(t *testing.T) {
	var buf bytes.Buffer
	if err := EncodeWarning(&buf, WarningEnvelope{Prog: "p", Warning: "w", Remedy: testFix}); err != nil {
		t.Fatal(err)
	}
	var env WarningEnvelope
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Remedy != testFix {
		t.Fatalf("remedy = %q; want %q", env.Remedy, testFix)
	}
}
