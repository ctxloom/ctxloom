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

// withTTY overrides isInteractiveTerminal for the duration of one test,
// restoring the original (production) implementation on cleanup — the same
// seam technique internal/cli's terminal predicates use, since a test
// binary's own stdout is never a real terminal.
func withTTY(t *testing.T, interactive bool) {
	t.Helper()
	orig := isInteractiveTerminal
	isInteractiveTerminal = func() bool { return interactive }
	t.Cleanup(func() { isInteractiveTerminal = orig })
}

func TestEmit_TextRunsClosure(t *testing.T) {
	c, buf := newCmd(false)
	// Pin format explicitly: this test is about Emit's text/data dispatch,
	// not about the TTY-driven default (covered separately below), and a
	// test binary's stdout is never a terminal.
	if err := c.Flags().Set("format", "text"); err != nil {
		t.Fatal(err)
	}
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
// internal/cli's now-deleted resolveFormat pass-through.
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

// It used to be the case that Emit with a nil text closure over an EMPTY
// SCALAR SLICE wrote zero bytes and returned nil under text, while --format
// json wrote `[]` — so "nothing to show" and "the command produced no output
// at all" were indistinguishable in exactly one format. That is fixed now:
// clifmt's renderText prints a "(none)" marker for an empty slice, and
// Emit routes there whenever text == nil.
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

// It might seem like Resolve swallows the one error that separates "the user
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

// Resolve reads cmd.Flags(), and cobra merges a PARENT's persistent
// flags into a child's flag set during ParseFlags, inside Execute. So Resolve
// is only correct once execution has begun; called earlier against a
// subcommand it cannot see the root's --format and answers text. That
// ordering requirement is now stated on Resolve's doc, and this pins it so the
// prose stays true: both halves are measured, not asserted.
func TestResolve_OnlySeesInheritedFormatOnceExecutionHasBegun(t *testing.T) {
	newTree := func(runE func(*cobra.Command, []string) error) (*cobra.Command, *cobra.Command) {
		root := &cobra.Command{Use: "root", SilenceUsage: true, SilenceErrors: true}
		root.PersistentFlags().String("format", "text", "")
		sub := &cobra.Command{Use: "sub", RunE: runE}
		root.AddCommand(sub)
		root.SetArgs([]string{"sub", "--format", "json"})
		return root, sub
	}

	// BEFORE Execute: the persistent flag has not been merged into sub's own
	// flag set, so there is no --format to read and text is the answer.
	_, sub := newTree(func(*cobra.Command, []string) error { return nil })
	early, err := Resolve(sub)
	if err != nil {
		t.Fatal(err)
	}
	if early != clifmt.FormatText {
		t.Fatalf("pre-parse Resolve should answer text, got %q", early)
	}

	// INSIDE RunE: the merge has happened and the user's value is visible.
	var inside clifmt.Format
	root, _ := newTree(func(c *cobra.Command, _ []string) error {
		var rerr error
		inside, rerr = Resolve(c)
		return rerr
	})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if inside != clifmt.FormatJSON {
		t.Fatalf("Resolve inside RunE must see the inherited --format, got %q", inside)
	}
}

// This package called itself "the cross-binary --format output
// filter" while implementing only the SUCCESS half — clifmt.RenderError had
// exactly one call site in the whole tree, inside internal/cli's Execute, so
// the error half lived in one binary and not in the package that claims it.
// EmitError is that missing half. It must answer the same four questions Emit
// does, plus one Emit never faces: a --format that will not parse must not
// cost the caller the ORIGINAL error.
func TestEmitError_RendersInTheSelectedFormat(t *testing.T) {
	t.Run("text keeps the human line", func(t *testing.T) {
		c, _ := newCmd(false)
		// Pin format explicitly — see TestEmit_TextRunsClosure's comment.
		if err := c.Flags().Set("format", "text"); err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if err := EmitError(&buf, c, errors.New("boom")); err != nil {
			t.Fatal(err)
		}
		if got := buf.String(); got != "Error: boom\n" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("json gets a parseable envelope", func(t *testing.T) {
		c, _ := newCmd(false)
		if err := c.Flags().Set("format", "json"); err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if err := EmitError(&buf, c, errors.New("boom")); err != nil {
			t.Fatal(err)
		}
		var got map[string]string
		if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
			t.Fatalf("a caller that asked for json must get parseable bytes: %v (%q)", err, buf.String())
		}
		if got["error"] != "boom" {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("an unparseable --format must not lose the original error", func(t *testing.T) {
		c, _ := newCmd(false)
		if err := c.Flags().Set("format", "xml"); err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if err := EmitError(&buf, c, errors.New("boom")); err != nil {
			t.Fatal(err)
		}
		if got := buf.String(); got != "Error: boom\n" {
			t.Fatalf("the original failure must survive a bad --format, got %q", got)
		}
	})

	t.Run("writes to the writer it is given, never stdout", func(t *testing.T) {
		c, out := newCmd(false)
		var buf bytes.Buffer
		if err := EmitError(&buf, c, errors.New("boom")); err != nil {
			t.Fatal(err)
		}
		if out.Len() != 0 {
			t.Fatalf("a failure must not land on stdout, got %q", out.String())
		}
	})
}

// TestResolve_DefaultFollowsTTY pins both arms of the TTY-driven default an
// UNSET --format now resolves to: json off a terminal (the fix — a piped or
// scripted invocation used to silently get a human rendering it could
// misparse), text ON one (unchanged — a human at a terminal keeps today's
// rendering). A test proving only the json half would let the human-facing
// arm regress unnoticed, which is exactly the gap this task calls out.
func TestResolve_DefaultFollowsTTY(t *testing.T) {
	t.Run("non-terminal stdout defaults to json", func(t *testing.T) {
		withTTY(t, false)
		c, _ := newCmd(false) // --format never Set: this is the "left at default" case
		got, err := Resolve(c)
		if err != nil {
			t.Fatal(err)
		}
		if got != clifmt.FormatJSON {
			t.Fatalf("a non-TTY invocation with --format left unset must default to json, got %q", got)
		}
	})

	t.Run("terminal stdout keeps text", func(t *testing.T) {
		withTTY(t, true)
		c, _ := newCmd(false)
		got, err := Resolve(c)
		if err != nil {
			t.Fatal(err)
		}
		if got != clifmt.FormatText {
			t.Fatalf("a TTY-attached invocation with --format left unset must default to text, got %q", got)
		}
	})
}

// TestResolve_ExplicitFormatBeatsTTYDefaultBothWays pins the DoD requirement
// that an explicit --format always wins over the TTY-driven default,
// regardless of which direction it disagrees with: --format text into a
// non-terminal still renders text, and --format json at a terminal still
// renders json.
func TestResolve_ExplicitFormatBeatsTTYDefaultBothWays(t *testing.T) {
	t.Run("--format text survives a non-terminal", func(t *testing.T) {
		withTTY(t, false)
		c, _ := newCmd(false)
		if err := c.Flags().Set("format", "text"); err != nil {
			t.Fatal(err)
		}
		got, err := Resolve(c)
		if err != nil {
			t.Fatal(err)
		}
		if got != clifmt.FormatText {
			t.Fatalf("an explicit --format text must survive a non-TTY default of json, got %q", got)
		}
	})

	t.Run("--format json survives a terminal", func(t *testing.T) {
		withTTY(t, true)
		c, _ := newCmd(false)
		if err := c.Flags().Set("format", "json"); err != nil {
			t.Fatal(err)
		}
		got, err := Resolve(c)
		if err != nil {
			t.Fatal(err)
		}
		if got != clifmt.FormatJSON {
			t.Fatalf("an explicit --format json must survive a TTY default of text, got %q", got)
		}
	})
}
