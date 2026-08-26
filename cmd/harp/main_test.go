package main

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/shared/harp"
)

// TestNewRootCmd_Flags pins the flag surface carried over from the removed
// `ctxloom harp` subcommand: every short/long name and default must
// match exactly, since this binary is the flag surface's only home now.
func TestNewRootCmd_Flags(t *testing.T) {
	root := newRootCmd()

	cases := []struct {
		name, shorthand, def string
	}{
		{"components", "c", "3"},
		{"separator", "s", "-"},
		{"max-len", "", "0"},
		{"number", "n", "1"},
		{"group", "g", "default"},
	}
	for _, c := range cases {
		f := root.Flags().Lookup(c.name)
		if f == nil {
			t.Fatalf("flag %q not registered", c.name)
		}
		if f.Shorthand != c.shorthand {
			t.Errorf("flag %q shorthand = %q, want %q", c.name, f.Shorthand, c.shorthand)
		}
		if f.DefValue != c.def {
			t.Errorf("flag %q default = %q, want %q", c.name, f.DefValue, c.def)
		}
	}

	if f := root.PersistentFlags().Lookup("format"); f == nil {
		t.Fatal(`persistent flag "format" not registered`)
	} else if f.DefValue != "text" {
		t.Errorf(`"format" default = %q, want "text"`, f.DefValue)
	}
}

// runHarp executes the root command with args, capturing stdout.
func runHarp(t *testing.T, args ...string) string {
	t.Helper()
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		t.Fatalf("harp %s: %v", strings.Join(args, " "), err)
	}
	return out.String()
}

// TestRunGenerate_DefaultTextOneName pins the zero-flag behavior: one name,
// default 3 components (2 separators), newline-terminated.
func TestRunGenerate_DefaultTextOneName(t *testing.T) {
	// --format text is REQUESTED, not assumed. cliemit.Resolve derives the
	// format from stdout when the flag was never set, and a test binary's stdout
	// is never a terminal — so an unset flag reads as json and this test's
	// subject, the TEXT rendering, would never run. The sibling cases below
	// already name their format for the same reason; "Default" here means the
	// zero-flag name shape (one name, three components), not the format.
	out := runHarp(t, "--format", "text")
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d: %q", len(lines), out)
	}
	if got := strings.Count(lines[0], "-"); got != 2 {
		t.Errorf("expected 2 separators (3 components) in %q, got %d", lines[0], got)
	}
}

// TestRunGenerate_Number pins -n/--number: N newline-separated names.
func TestRunGenerate_Number(t *testing.T) {
	out := runHarp(t, "-n", "5", "--format", "text")
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("expected 5 lines, got %d: %q", len(lines), out)
	}
}

// TestRunGenerate_NumberZero pins the historical "n<=0 emits nothing"
// behavior the removed ctxloom harp command had (`for range harpCount`),
// inverted: `harp -n 0` (and every negative count) used to print zero bytes
// and exit 0 — a silent no-op, not a valid "zero names requested" outcome.
// A generator asked to generate something that produces nothing has failed,
// not succeeded, and must say so on stderr with a non-zero exit rather than
// rendering an empty payload. runGenerate now rejects a non-positive
// --number.
func TestRunGenerate_NumberZero(t *testing.T) {
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"-n", "0", "--format", "text"})

	err := root.Execute()
	if err == nil {
		t.Fatal("harp -n 0 must fail loudly (U004-F01), not exit 0 having produced nothing")
	}
	if out.String() != "" && err == nil {
		t.Errorf("expected no rendered payload alongside the error, got %q", out.String())
	}
}

// TestRunGenerate_Components pins -c/--components: total word count.
func TestRunGenerate_Components(t *testing.T) {
	out := runHarp(t, "-c", "5")
	name := strings.TrimRight(out, "\n")
	if got := strings.Count(name, "-") + 1; got != 5 {
		t.Errorf("expected 5 components in %q, got %d", name, got)
	}
}

// TestRunGenerate_Separator pins -s/--separator.
func TestRunGenerate_Separator(t *testing.T) {
	out := runHarp(t, "-s", "_", "-c", "2")
	name := strings.TrimRight(out, "\n")
	if strings.Contains(name, "-") || !strings.Contains(name, "_") {
		t.Errorf("expected '_'-separated name, got %q", name)
	}
}

// TestRunGenerate_MaxLen pins --max-len.
func TestRunGenerate_MaxLen(t *testing.T) {
	out := runHarp(t, "--max-len", "4", "-c", "2", "-s", "-", "--format", "text")
	for _, word := range strings.Split(strings.TrimRight(out, "\n"), "-") {
		if len(word) > 4 {
			t.Errorf("word %q exceeds --max-len 4", word)
		}
	}
}

// TestRunGenerate_Group pins -g/--group against the "long" word list, whose
// words routinely exceed the "default" group's 5-char cap — a group switch
// is otherwise unobservable from the CLI's output alone.
func TestRunGenerate_Group(t *testing.T) {
	sawLong := false
	for range 50 {
		out := runHarp(t, "-g", "long", "-c", "2", "-n", "10")
		for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
			for _, word := range strings.Split(line, "-") {
				if len(word) > 5 {
					sawLong = true
				}
			}
		}
		if sawLong {
			break
		}
	}
	if !sawLong {
		t.Error(`-g long: never saw a word >5 chars across 500 draws (group flag not honored?)`)
	}
}

// TestRunGenerate_FormatJSON pins the "json = array of names" contract.
func TestRunGenerate_FormatJSON(t *testing.T) {
	out := runHarp(t, "-n", "3", "--format", "json")
	var names []string
	if err := json.Unmarshal([]byte(out), &names); err != nil {
		t.Fatalf("invalid JSON array: %v\noutput: %s", err, out)
	}
	if len(names) != 3 {
		t.Fatalf("expected 3 names, got %d", len(names))
	}
}

// TestRunGenerate_FormatYAML pins the yaml encoding of the same bare list.
func TestRunGenerate_FormatYAML(t *testing.T) {
	out := runHarp(t, "-n", "2", "--format", "yaml")
	var names []string
	if err := yaml.Unmarshal([]byte(out), &names); err != nil {
		t.Fatalf("invalid YAML: %v\noutput: %s", err, out)
	}
	if len(names) != 2 {
		t.Fatalf("expected 2 names, got %d", len(names))
	}
}

// TestRunGenerate_FormatTOML pins the TOML wrapping clifmt applies to a
// top-level list Result (documents must be a table at the root, so the
// slice lands under an "items" key for TOML specifically).
func TestRunGenerate_FormatTOML(t *testing.T) {
	out := runHarp(t, "-n", "2", "--format", "toml")
	if !strings.Contains(out, "items") {
		t.Errorf("expected TOML output wrapped under \"items\", got %q", out)
	}
}

// TestRunGenerate_FormatMarkdown pins the bullet-list rendering of a
// top-level scalar slice.
func TestRunGenerate_FormatMarkdown(t *testing.T) {
	out := runHarp(t, "-n", "2", "--format", "markdown")
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 bullet lines, got %d: %q", len(lines), out)
	}
	for _, l := range lines {
		if !strings.HasPrefix(l, "- ") {
			t.Errorf("expected markdown bullet, got %q", l)
		}
	}
}

// TestRunGenerate_UnsupportedFormat pins that an unknown --format is a hard
// error, not a silent text fallback (clifmt's own contract).
func TestRunGenerate_UnsupportedFormat(t *testing.T) {
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--format", "xml"})
	if err := root.Execute(); err == nil {
		t.Fatal("expected an error for --format xml, got nil")
	}
}

// TestNewVersionCmd pins `harp version`'s text and json shapes.
func TestNewVersionCmd(t *testing.T) {
	text := runHarp(t, "version", "--format", "text")
	if strings.TrimSpace(text) != Version {
		t.Errorf("version text = %q, want %q", strings.TrimSpace(text), Version)
	}

	jsonOut := runHarp(t, "version", "--format", "json")
	var info struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &info); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %s", err, jsonOut)
	}
	if info.Name != progName {
		t.Errorf("version json name = %q, want %q", info.Name, progName)
	}
	if info.Version != Version {
		t.Errorf("version json version = %q, want %q", info.Version, Version)
	}
}

// TestNewRootCmd_CommandSurface pins the command tree shape.
func TestNewRootCmd_CommandSurface(t *testing.T) {
	root := newRootCmd()
	if root.Use != progName {
		t.Errorf("root Use = %q, want %q", root.Use, progName)
	}
	if root.Short == "" {
		t.Error("root has no Short")
	}
	got := map[string]bool{}
	for _, c := range root.Commands() {
		got[c.Name()] = true
	}
	if !got["version"] {
		t.Error(`"version" subcommand missing from root tree`)
	}
}

// TestRunGenerate_RejectsAdvertisedRangeViolations pins that the flag help
// advertises "number of words (2-16)" and enumerates the word-list groups, and
// the CLI must ENFORCE what it advertises. The library it calls deliberately
// clamps instead (harp.Options.normalize, pinned by
// TestGenerateName_ComponentsClamped and TestGenerateName_UnknownGroupFallsBack
// — that is its documented zero-value contract and it is not this binary's to
// change), so a CLI that hands the value straight through turns a typo into a
// plausible wrong answer at exit 0: `harp -c 1` prints a THREE-word name,
// `harp -c 99` prints sixteen, `harp -g typo` quietly draws from "default".
//
// Same shape and same layer as the `--number` guard above: the enforcement
// belongs where the promise is made.
func TestRunGenerate_RejectsAdvertisedRangeViolations(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"components below the advertised floor", []string{"-c", "1"}, "--components"},
		{"components above the advertised ceiling", []string{"-c", "17"}, "--components"},
		{"group outside the advertised set", []string{"-g", "no-such-group"}, "--group"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := newRootCmd()
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)
			root.SetArgs(tc.args)

			err := root.Execute()
			if err == nil {
				t.Fatalf("harp %s must fail loudly, not silently clamp; it printed %q", strings.Join(tc.args, " "), out.String())
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q should name the offending flag %q", err, tc.want)
			}
			if out.String() != "" {
				t.Errorf("a refused invocation must render no names, got %q", out.String())
			}
		})
	}
}

// TestRunGenerate_AcceptsTheWholeAdvertisedRange is the other half: enforcement
// must not narrow what the help promises. Both endpoints and every advertised
// group have to work.
func TestRunGenerate_AcceptsTheWholeAdvertisedRange(t *testing.T) {
	for _, n := range []int{2, 16} {
		name := strings.TrimRight(runHarp(t, "-c", strconv.Itoa(n)), "\n")
		if got := strings.Count(name, "-") + 1; got != n {
			t.Errorf("-c %d produced %d components (%q)", n, got, name)
		}
	}
	for _, g := range harp.Groups() {
		if out := runHarp(t, "-g", g); strings.TrimSpace(out) == "" {
			t.Errorf("advertised group %q produced no name", g)
		}
	}
}

// TestRunGenerate_RejectsAnEmptySeparator is the remaining limb of the same
// principle TestRunGenerate_RejectsAdvertisedRangeViolations enforces for
// --components and --group: the library CLAMPS what it cannot use rather than
// rejecting it (harp.Options.normalize rewrites an empty Separator to "-"),
// so a value this binary cannot actually deliver has to be refused HERE or it
// comes back as a plausible wrong answer at exit 0.
//
// `harp -s ""` asks for no separator and used to get dash-separated names.
// Honouring it is not available to this layer: harp.Options cannot express
// "explicitly empty" — the struct's zero value already means "use the
// default" — and cobra's Changed() knowledge stops at this boundary. So the
// honest response is to say so, not to substitute a different separator.
func TestRunGenerate_RejectsAnEmptySeparator(t *testing.T) {
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"-s", ""})

	err := root.Execute()
	if err == nil {
		t.Fatalf("harp -s \"\" must be refused, not silently rendered with the default separator; output: %q", out.String())
	}
	if !strings.Contains(err.Error(), "--separator") {
		t.Errorf("the error must name the flag it is about, got %q", err)
	}
	if strings.Contains(out.String(), "-") && strings.TrimSpace(out.String()) != "" {
		t.Errorf("nothing may be generated once the flag is refused, got %q", out.String())
	}
}

// TestRunGenerate_AcceptsAnExplicitNonEmptySeparator keeps the guard above
// from becoming a blanket rejection of --separator.
func TestRunGenerate_AcceptsAnExplicitNonEmptySeparator(t *testing.T) {
	name := strings.TrimRight(runHarp(t, "-s", "_"), "\n")
	if strings.Contains(name, "-") {
		t.Errorf("-s _ must not produce dashes, got %q", name)
	}
	if strings.Count(name, "_") != 2 {
		t.Errorf("-s _ must join the default 3 components with underscores, got %q", name)
	}
}
