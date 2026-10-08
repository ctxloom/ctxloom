package cobrafmt

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/pkg/clifmt"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// AddFlag registers ONE persistent --format: usage clifmt.FormatUsage, an
// empty default (an unset flag follows the terminal), and completion that
// offers exactly clifmt.SupportedFormats.
func TestAddFlag(t *testing.T) {
	root := &cobra.Command{Use: "prog"}
	AddFlag(root)
	f := root.PersistentFlags().Lookup("format")
	if f == nil {
		t.Fatal("no persistent --format")
	}
	if f.Value.Type() != "string" || f.DefValue != "" {
		t.Errorf("--format is %s defaulting to %q, want a string defaulting to \"\"", f.Value.Type(), f.DefValue)
	}
	if f.Usage != clifmt.FormatUsage() {
		t.Errorf("usage = %q, want clifmt.FormatUsage()", f.Usage)
	}
	complete, ok := root.GetFlagCompletionFunc("format")
	if !ok {
		t.Fatal("--format has no completion")
	}
	got, directive := complete(root, nil, "")
	var want []cobra.Completion
	for _, f := range clifmt.SupportedFormats() {
		want = append(want, string(f))
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("completion = %v, want %v", got, want)
	}
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("completion directive = %v, want NoFileComp", directive)
	}
}

type codeErr struct {
	code int
	msg  string
}

func (e codeErr) Error() string { return e.msg }
func (e codeErr) ExitCode() int { return e.code }

type fixErr struct{ msg, fix string }

func (e fixErr) Error() string  { return e.msg }
func (e fixErr) Remedy() string { return e.fix }

// execTree runs `prog run <args>` whose RunE returns err, through Execute.
func execTree(t *testing.T, err error, args ...string) (int, string, string) {
	t.Helper()
	root := &cobra.Command{Use: "prog", SilenceUsage: true}
	AddFlag(root)
	root.AddCommand(&cobra.Command{Use: "run", RunE: func(*cobra.Command, []string) error { return err }})
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(append([]string{"run"}, args...))
	code := Execute(root, "prog", &stderr)
	return code, stdout.String(), stderr.String()
}

func TestExecute(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		args       []string
		wantCode   int
		wantStderr string
	}{
		{"success", nil, nil, 0, ""},
		{"exit status is silent", clifmt.ExitStatus{Code: 3}, nil, 3, ""},
		{"wrapped exit status is silent", fmt.Errorf("engine: %w", clifmt.ExitStatus{Code: 7}), nil, 7, ""},
		{"exit coder is reported", codeErr{2, "refused"}, nil, 2, "prog: refused\n"},
		{"plain error", errors.New("boom"), nil, 1, "prog: boom\n"},
		{"fix line", fmt.Errorf("ctx: %w", fixErr{"broken", "run fix"}), nil, 1, "prog: ctx: broken\n  fix: run fix\n"},
		{"explicit text is the human line", errors.New("boom"), []string{"--format", "text"}, 1, "prog: boom\n"},
		{"explicit markdown is the human line", errors.New("boom"), []string{"--format", "markdown"}, 1, "prog: boom\n"},
		{"a format that will not parse keeps the error", errors.New("boom"), []string{"--format", "xml"}, 1, "prog: boom\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := execTree(t, tc.err, tc.args...)
			if code != tc.wantCode || stderr != tc.wantStderr || stdout != "" {
				t.Errorf("code %d, stderr %q, stdout %q; want %d, %q, nothing", code, stderr, stdout, tc.wantCode, tc.wantStderr)
			}
		})
	}
}

// An explicit structured format turns the failure into a parseable envelope
// on stderr; a format merely derived from a pipe does not.
func TestExecute_StructuredOnlyWhenExplicit(t *testing.T) {
	for _, f := range []string{"json", "yaml", "toml"} {
		code, _, stderr := execTree(t, fixErr{"broken", "run fix"}, "--format", f)
		if code != 1 || !strings.Contains(stderr, "broken") || !strings.Contains(stderr, "run fix") || strings.HasPrefix(stderr, "prog:") {
			t.Errorf("--format %s: code %d, stderr %q; want an envelope", f, code, stderr)
		}
	}
	_, _, stderr := execTree(t, fixErr{"broken", "run fix"}, "--format", "json")
	var env clifmt.ErrorEnvelope
	if err := json.Unmarshal([]byte(stderr), &env); err != nil || env.Error != "broken" || env.Remedy != "run fix" {
		t.Errorf("json envelope %q: %+v, %v", stderr, env, err)
	}

	t.Cleanup(OverrideTerminal(false)) // a pipe: the format derives to json
	if _, _, stderr := execTree(t, errors.New("boom")); stderr != "prog: boom\n" {
		t.Errorf("a derived format restructured stderr: %q", stderr)
	}
}

// Execute owns reporting, so cobra's own "Error:" line never doubles it.
func TestExecute_SilencesCobrasOwnReport(t *testing.T) {
	root := &cobra.Command{Use: "prog", RunE: func(*cobra.Command, []string) error { return errors.New("boom") }}
	var stderr bytes.Buffer
	root.SetErr(&stderr)
	root.SetOut(io.Discard)
	root.SetArgs(nil)
	Execute(root, "prog", &stderr)
	if stderr.String() != "prog: boom\n" {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestApplyDiagnostics(t *testing.T) {
	t.Cleanup(func() { clidiag.SetStructured(false) })
	cases := map[string]bool{"json": true, "yaml": true, "toml": true, "text": false, "markdown": false, "xml": false}
	for f, structured := range cases {
		var buf bytes.Buffer
		root := &cobra.Command{Use: "prog", PersistentPreRun: func(cmd *cobra.Command, _ []string) { ApplyDiagnostics(cmd) }}
		AddFlag(root)
		root.AddCommand(&cobra.Command{Use: "run", Run: func(*cobra.Command, []string) { clidiag.Fwarn(&buf, "prog", "w") }})
		root.SetArgs([]string{"run", "--format", f})
		clidiag.SetStructured(!structured)
		if err := root.Execute(); err != nil {
			t.Fatal(err)
		}
		if got := strings.HasPrefix(buf.String(), "{"); got != structured {
			t.Errorf("--format %s: warning %q, want structured=%v", f, buf.String(), structured)
		}
	}
}

func TestEmitVersion(t *testing.T) {
	for f, want := range map[string]string{"text": "1.2.3\n", "json": "{\n  \"name\": \"prog\",\n  \"version\": \"1.2.3\"\n}\n"} {
		c, buf := newCmd(false)
		if err := c.Flags().Set("format", f); err != nil {
			t.Fatal(err)
		}
		if err := EmitVersion(c, "prog", "1.2.3"); err != nil {
			t.Fatal(err)
		}
		if buf.String() != want {
			t.Errorf("%s: %q, want %q", f, buf.String(), want)
		}
	}
}

type viewed struct {
	N int `json:"n"`
}

// A Printer installed on a tree reaches every Emit in it, and only in it.
func TestWithPrinter(t *testing.T) {
	p, err := clifmt.New(clifmt.ViewFor(func(*clifmt.ViewCtx, viewed) (clifmt.Doc, error) {
		return clifmt.Doc{clifmt.Para("viewed")}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	tree := func() (*cobra.Command, *cobra.Command, *bytes.Buffer) {
		root := &cobra.Command{Use: "prog"}
		AddFlag(root)
		leaf := &cobra.Command{Use: "leaf"}
		root.AddCommand(leaf)
		var buf bytes.Buffer
		leaf.SetOut(&buf)
		if err := root.PersistentFlags().Set("format", "text"); err != nil {
			t.Fatal(err)
		}
		return root, leaf, &buf
	}
	root, leaf, buf := tree()
	WithPrinter(root, p)
	if err := Emit(leaf, viewed{N: 1}); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "viewed\n" {
		t.Errorf("with the Printer: %q", buf.String())
	}
	_, other, otherBuf := tree()
	if err := Emit(other, viewed{N: 1}); err != nil {
		t.Fatal(err)
	}
	if otherBuf.String() != "N: 1\n" {
		t.Errorf("another tree picked up the Printer: %q", otherBuf.String())
	}
}

// The Printer reaches a command's Emit when the tree is actually executed,
// not only when a command is emitted from directly.
func TestWithPrinter_ThroughExecute(t *testing.T) {
	p, err := clifmt.New(clifmt.ViewFor(func(*clifmt.ViewCtx, viewed) (clifmt.Doc, error) {
		return clifmt.Doc{clifmt.Para("viewed")}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	root := &cobra.Command{Use: "prog"}
	AddFlag(root)
	root.AddCommand(&cobra.Command{Use: "run", RunE: func(cmd *cobra.Command, _ []string) error {
		return Emit(cmd, viewed{N: 1})
	}})
	WithPrinter(root, p)
	var stdout bytes.Buffer
	root.SetOut(&stdout)
	root.SetArgs([]string{"run", "--format", "text"})
	if code := Execute(root, "prog", io.Discard); code != 0 || stdout.String() != "viewed\n" {
		t.Errorf("code %d, stdout %q; want 0, %q", code, stdout.String(), "viewed\n")
	}
}
