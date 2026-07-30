package cliemit

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// newCmd builds a bare command carrying the flags a real root supplies: a
// persistent --format and (optionally) the --json shorthand a few commands add.
func newCmd(withJSON bool) (*cobra.Command, *bytes.Buffer) {
	c := &cobra.Command{}
	c.Flags().String("format", "text", "")
	if withJSON {
		c.Flags().Bool("json", false, "")
	}
	var buf bytes.Buffer
	c.SetOut(&buf)
	return c, &buf
}

func TestEmit_TextRunsClosure(t *testing.T) {
	c, buf := newCmd(false)
	err := Emit(c, struct {
		Name string `json:"name"`
	}{Name: "x"}, func() error {
		_, e := buf.WriteString("human line\n")
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "human line\n" {
		t.Fatalf("text should run the closure, got %q", got)
	}
}

func TestEmit_JSONRendersData(t *testing.T) {
	c, buf := newCmd(false)
	if err := c.Flags().Set("format", "json"); err != nil {
		t.Fatal(err)
	}
	err := Emit(c, struct {
		Name string `json:"name"`
	}{Name: "x"}, func() error {
		t.Fatal("text closure must not run for json")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("json should be parseable: %v (%q)", err, buf.String())
	}
	if got["name"] != "x" {
		t.Fatalf("got %v", got)
	}
}

func TestResolve_JSONShorthandBeatsFormat(t *testing.T) {
	c, _ := newCmd(true)
	// --format stays at its text default; the set --json shorthand wins.
	if err := c.Flags().Set("json", "true"); err != nil {
		t.Fatal(err)
	}
	f, err := Resolve(c)
	if err != nil {
		t.Fatal(err)
	}
	if f != clifmt.FormatJSON {
		t.Fatalf("a set --json must resolve to json, got %q", f)
	}
}

func TestResolve_UnsetFormatIsText(t *testing.T) {
	c := &cobra.Command{} // no --format flag registered at all
	f, err := Resolve(c)
	if err != nil {
		t.Fatal(err)
	}
	if f != clifmt.FormatText {
		t.Fatalf("an unset --format must resolve to text, got %q", f)
	}
}

func TestResolve_UnknownFormatErrors(t *testing.T) {
	c, _ := newCmd(false)
	if err := c.Flags().Set("format", "xml"); err != nil {
		t.Fatal(err)
	}
	_, err := Resolve(c)
	if !errors.Is(err, clifmt.ErrUnsupportedFormat) {
		t.Fatalf("want ErrUnsupportedFormat, got %v", err)
	}
}

// TestResolve_AcceptsAllFiveEncodings pins Resolve's full accepted vocabulary
// (json/yaml/toml/text/markdown), formerly covered only through
// internal/cli's now-deleted resolveFormat pass-through (U035-F24).
func TestResolve_AcceptsAllFiveEncodings(t *testing.T) {
	for _, want := range []clifmt.Format{
		clifmt.FormatJSON, clifmt.FormatYAML, clifmt.FormatTOML, clifmt.FormatText, clifmt.FormatMarkdown,
	} {
		c, _ := newCmd(false)
		if err := c.Flags().Set("format", string(want)); err != nil {
			t.Fatal(err)
		}
		got, err := Resolve(c)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("want %q, got %q", want, got)
		}
	}
}

// U104-F06 claimed Emit with a nil text closure over an EMPTY SCALAR SLICE
// writes zero bytes and returns nil under text, while --format json writes
// `[]` — so "nothing to show" and "the command produced no output at all"
// are indistinguishable in exactly one format. That premise no longer holds:
// clifmt's renderText prints a "(none)" marker for an empty slice (fixed
// under U154-F02), and Emit routes there whenever text == nil.
//
// This pins the claim at THIS package's seam, which is where the row is
// filed: the nil-closure affordance advertised in Emit's own doc ("a call
// site can route through Emit with zero rendering code") must never deliver a
// silent zero-byte success. It goes red if renderText's empty-slice marker is
// removed again, or if Emit stops routing a nil closure through clifmt.
func TestEmit_NilClosureOverEmptySliceIsNeverZeroBytes(t *testing.T) {
	for _, format := range []clifmt.Format{
		clifmt.FormatText, clifmt.FormatJSON, clifmt.FormatYAML, clifmt.FormatMarkdown,
	} {
		c, buf := newCmd(false)
		if err := c.Flags().Set("format", string(format)); err != nil {
			t.Fatal(err)
		}
		if err := Emit(c, []string{}, nil); err != nil {
			t.Fatalf("Emit(%s): %v", format, err)
		}
		if buf.Len() == 0 {
			t.Fatalf("Emit(%s) over an empty slice wrote zero bytes — indistinguishable from a delivery that never ran", format)
		}
	}
}

// U104-F02 claimed Resolve swallows the one error that separates "the user
// asked for text" from "this command has no --format flag". Measured, the
// error has TWO causes and they are not the same defect:
//
//   - flag ABSENT. Deliberate and documented: an unregistered --format reads
//     as text, which is what lets a command be driven without a root
//     (TestResolve_UnsetFormatIsText, and 30 internal/cli tests besides).
//     Erroring here was tried and rejected — see the commit for the count.
//   - flag registered with the WRONG TYPE. Not an affordance, a wiring bug:
//     the value the user typed cannot be read at all, and text is asserted as
//     if they had asked for it. That must be loud on the first invocation.
func TestResolve_NonStringFormatFlagIsAWiringError(t *testing.T) {
	c := &cobra.Command{Use: "wired-wrong"}
	c.Flags().Bool("format", false, "") // registered, but not a string
	_, err := Resolve(c)
	if err == nil {
		t.Fatal("a --format flag of the wrong type must not silently degrade to text")
	}
	if !strings.Contains(err.Error(), "wired-wrong") {
		t.Fatalf("the error must name the offending command, got %v", err)
	}
}
