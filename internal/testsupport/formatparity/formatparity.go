// Package formatparity is the one --format conformance table every binary in
// the family runs against its own root: the same flag help, the same
// completion, the same default, and the same error tail. Each binary keeps
// its root in its own main package, which no other package can import, so
// the table lives here and each binary's tests call Check; a binary that
// drifts from the family goes red in its own package.
package formatparity

import (
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/pkg/clifmt"
	"github.com/ctxloom/ctxloom/pkg/clifmt/cobrafmt"
)

// Binary is one family binary under the table.
type Binary struct {
	// Prog is the name the binary reports itself by ("<prog>: <msg>").
	Prog string
	// Root returns the binary's root command, as main builds it.
	Root func() *cobra.Command
	// Run runs the binary's real process tail on args and returns its exit
	// status and stderr. Re-exec the test binary as the program where main
	// can be reached that way, so the table checks main's own wiring.
	Run func(t *testing.T, args ...string) (code int, stderr string)
	// FailingArgs is an invocation that fails without touching any store or
	// the network (a missing required argument, say).
	FailingArgs []string
	// Refusal, when set, runs the binary's real process tail on an invocation
	// that is refused naming exactly ONE fix, with extra appended to its argv
	// (the table appends "--format", "json"), and returns its exit status and
	// stderr. Nil for a binary none of whose failures names a fix; NoRefusal
	// then says why, so a gap is a stated fact rather than an omission.
	Refusal func(t *testing.T, extra ...string) (code int, stderr string)
	// NoRefusal is why Refusal is nil.
	NoRefusal string
	// OffTerminalSource is what the binary's --format help names as its
	// configurable off-terminal default (cobrafmt.OffTerminalFrom); empty for
	// a binary whose default off a terminal is always json.
	OffTerminalSource string
}

// Check runs the family table against b.
func Check(t *testing.T, b Binary) {
	t.Helper()
	t.Run("flag", func(t *testing.T) {
		f := b.Root().PersistentFlags().Lookup("format")
		if f == nil {
			t.Fatal("the root registers no persistent --format")
		}
		if want := clifmt.FormatUsageWith(b.OffTerminalSource); f.Usage != want {
			t.Errorf("--format usage = %q, want clifmt.FormatUsageWith(%q) = %q", f.Usage, b.OffTerminalSource, want)
		}
		if f.DefValue != "" {
			t.Errorf("--format defaults to %q; an unset flag must follow the terminal, so the default is empty", f.DefValue)
		}
	})
	t.Run("completion", func(t *testing.T) {
		complete, ok := b.Root().GetFlagCompletionFunc("format")
		if !ok {
			t.Fatal("--format offers no shell completion")
		}
		got, _ := complete(b.Root(), nil, "")
		want := make([]cobra.Completion, 0, len(clifmt.SupportedFormats()))
		for _, f := range clifmt.SupportedFormats() {
			want = append(want, string(f))
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("--format completes to %v, want %v", got, want)
		}
	})
	t.Run("human error line", func(t *testing.T) {
		// Off a terminal the format derives to json, which is not a request:
		// stderr keeps the human line.
		code, stderr := b.Run(t, b.FailingArgs...)
		if code != 1 {
			t.Errorf("exit %d, want 1; stderr %q", code, stderr)
		}
		if !strings.HasPrefix(stderr, b.Prog+": ") || strings.Contains(stderr, "{") {
			t.Errorf("stderr %q, want the human line %q", stderr, b.Prog+": <msg>")
		}
	})
	t.Run("structured error envelope", func(t *testing.T) {
		for _, format := range []string{"json", "yaml", "toml"} {
			code, stderr := b.Run(t, append(append([]string{}, b.FailingArgs...), "--format", format)...)
			if code != 1 || strings.HasPrefix(stderr, b.Prog+": ") || !strings.Contains(stderr, "error") {
				t.Errorf("--format %s: exit %d, stderr %q; want 1 and an error envelope", format, code, stderr)
			}
		}
		_, stderr := b.Run(t, append(append([]string{}, b.FailingArgs...), "--format", "json")...)
		var env clifmt.ErrorEnvelope
		if err := json.Unmarshal([]byte(stderr), &env); err != nil || env.Error == "" {
			t.Errorf("--format json: stderr %q is not an error envelope (%v)", stderr, err)
		}
	})
	t.Run("one fix line per refusal", func(t *testing.T) { checkRefusal(t, b) })
	t.Run("default follows the terminal", func(t *testing.T) {
		for _, tc := range []struct {
			terminal bool
			want     clifmt.Format
		}{{true, clifmt.FormatText}, {false, clifmt.FormatJSON}} {
			restore := cobrafmt.OverrideTerminal(tc.terminal)
			root := b.Root()
			if err := root.ParseFlags(nil); err != nil {
				restore()
				t.Fatal(err)
			}
			got, err := cobrafmt.Resolve(root)
			restore()
			if err != nil || got != tc.want {
				t.Errorf("terminal=%v: unset --format resolves to %q (%v), want %q", tc.terminal, got, err, tc.want)
			}
		}
	})
}

// checkRefusal pins that a refusal naming one fix shows it once: the human
// error report (everything from the "<prog>: " line that is not a streamed
// "<prog>: warning:") carries exactly one "fix:" line, and the json envelope
// carries the fix in remedy and not again inside error.
func checkRefusal(t *testing.T, b Binary) {
	t.Helper()
	if b.Refusal == nil {
		if b.NoRefusal == "" {
			t.Fatal("Binary.Refusal is nil and NoRefusal does not say why")
		}
		t.Skip(b.NoRefusal)
	}
	code, stderr := b.Refusal(t)
	if code == 0 {
		t.Fatalf("the refusal exited 0; stderr %q", stderr)
	}
	report := errorReport(stderr, b.Prog)
	if got := countFixLines(report); got != 1 {
		t.Errorf("the error report carries %d fix: lines, want exactly 1:\n%s", got, report)
	}
	_, stderr = b.Refusal(t, "--format", "json")
	env, err := lastEnvelope(stderr)
	if err != nil {
		t.Fatalf("--format json: stderr %q ends in no error envelope (%v)", stderr, err)
	}
	if env.Remedy == "" || strings.Contains(env.Error, env.Remedy) {
		t.Errorf("--format json: envelope %+v; want the fix in remedy and only there", env)
	}
}

// errorReport is stderr from the process tail's "<prog>: <msg>" line on,
// skipping the streamed "<prog>: warning:" diagnostics before it.
func errorReport(stderr, prog string) string {
	lines := strings.Split(stderr, "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, prog+": ") && !strings.HasPrefix(l, prog+": warning:") {
			return strings.Join(lines[i:], "\n")
		}
	}
	return ""
}

// countFixLines counts the lines of s that are a clifmt.FixLine.
func countFixLines(s string) int {
	n := 0
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimLeft(l, " "), "fix: ") {
			n++
		}
	}
	return n
}

// lastEnvelope decodes stderr as a stream of JSON values and returns the
// last: the envelope follows any streamed JSON Lines warnings.
func lastEnvelope(stderr string) (clifmt.ErrorEnvelope, error) {
	dec := json.NewDecoder(strings.NewReader(stderr))
	var env clifmt.ErrorEnvelope
	for {
		var v clifmt.ErrorEnvelope
		if err := dec.Decode(&v); err != nil {
			if errors.Is(err, io.EOF) && env.Error != "" {
				return env, nil
			}
			if errors.Is(err, io.EOF) {
				return env, errors.New("no error envelope")
			}
			return env, err
		}
		env = v
	}
}
