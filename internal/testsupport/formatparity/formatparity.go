// Package formatparity is the one --format conformance table every binary in
// the family runs against its own root: the same flag help, the same
// completion, the same default, and the same error tail. Each binary keeps
// its root in its own main package, which no other package can import, so
// the table lives here and each binary's tests call Check; a binary that
// drifts from the family goes red in its own package.
package formatparity

import (
	"encoding/json"
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
}

// Check runs the family table against b.
func Check(t *testing.T, b Binary) {
	t.Helper()
	t.Run("flag", func(t *testing.T) {
		f := b.Root().PersistentFlags().Lookup("format")
		if f == nil {
			t.Fatal("the root registers no persistent --format")
		}
		if f.Usage != clifmt.FormatUsage() {
			t.Errorf("--format usage = %q, want clifmt.FormatUsage() = %q", f.Usage, clifmt.FormatUsage())
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
