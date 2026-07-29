package cliemit

import (
	"bytes"
	"encoding/json"
	"errors"
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
