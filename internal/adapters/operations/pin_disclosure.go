package operations

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/collections"
	"github.com/ctxloom/ctxloom/internal/shared/gitutil"
)

// WriteConstraintChanges tells the user, per pin, that the manifest now asks
// for something the pin was not resolved from, and that only `deps upgrade`
// applies it. Pull, init and startup never move an existing pin, so without
// this a constraint edit would look applied when it is not.
func WriteConstraintChanges(w io.Writer, changes []ConstraintChange) {
	for _, c := range changes {
		fmt.Fprintf(w, "  %s: the manifest now asks for %s; the pin stays at %s (resolved from %s).\n",
			c.Identity, constraintLabel(c.Declared), gitutil.ShortSHA(c.SHA), constraintLabel(c.Pinned))
	}
	if len(changes) > 0 {
		fmt.Fprintln(w, "  Only 'ctxloom deps upgrade' moves a pin: run it to see the change, then 'ctxloom deps upgrade --yes' to apply it.")
	}
}

// constraintLabel names an empty constraint for a human.
func constraintLabel(expr string) string {
	if expr == "" {
		return "the default branch"
	}
	return expr
}

// MsgNewPinsHeader introduces the first pins a pull, init or startup created.
const MsgNewPinsHeader = "New pins, with everything each one brings in:"

// WriteNewPins prints the first pins a sync created, under MsgNewPinsHeader.
func WriteNewPins(w io.Writer, changes []PinChange) {
	if len(changes) == 0 {
		return
	}
	fmt.Fprintln(w, MsgNewPinsHeader)
	WritePinChanges(w, changes)
}

// WritePinChanges renders each pin change: a header naming the move, one line
// per item (+ added, - removed, ~ modified) with what hooks and MCP servers
// run, and one line per file with the unified diff of each script.
func WritePinChanges(w io.Writer, changes []PinChange) {
	for _, pc := range changes {
		from, fromSHA := versionLabel(pc.FromVersion), gitutil.ShortSHA(pc.FromSHA)
		if pc.FromSHA == "" {
			from, fromSHA = "first pin", "new"
		}
		fmt.Fprintf(w, "%s  %s -> %s  (%s -> %s)\n", pc.Identity, from, versionLabel(pc.ToVersion), fromSHA, gitutil.ShortSHA(pc.ToSHA))
		for _, it := range pc.Items {
			fmt.Fprintf(w, "  %s %s %s\n", changeMark(it.Change), it.Kind, it.Name)
			if it.Exec != nil {
				writeExecDelta(w, *it.Exec)
			}
		}
		for _, f := range pc.Files {
			fmt.Fprintf(w, "  %s file %s\n", changeMark(f.Change), f.Path)
			for _, line := range difflibLines(f.Diff) {
				fmt.Fprintf(w, "      %s\n", line)
			}
		}
	}
}

func versionLabel(v string) string {
	if v == "" {
		return "unversioned"
	}
	return v
}

func changeMark(c ChangeKind) string {
	switch c {
	case ChangeAdded:
		return "+"
	case ChangeRemoved:
		return "-"
	default:
		return "~"
	}
}

// writeExecDelta prints each exec field either side sets: once when the two
// agree (or only one side exists), as before -> after when they differ.
func writeExecDelta(w io.Writer, d ExecDelta) {
	for _, f := range execFields {
		before, after := f.get(d.Before), f.get(d.After)
		switch {
		case before == "" && after == "":
		case d.Before == nil || d.After == nil || before == after:
			shown := after
			if shown == "" {
				shown = before
			}
			fmt.Fprintf(w, "      %s: %s\n", f.name, shown)
		default:
			fmt.Fprintf(w, "      %s: %s -> %s\n", f.name, noneIfEmpty(before), noneIfEmpty(after))
		}
	}
}

// execFields are ExecSpec's fields in display order, each rendered as text.
var execFields = []struct {
	name string
	get  func(*ExecSpec) string
}{
	{"command", func(s *ExecSpec) string { return specField(s, func(s *ExecSpec) string { return s.Command }) }},
	{"args", func(s *ExecSpec) string { return specField(s, func(s *ExecSpec) string { return quoteAll(s.Args) }) }},
	{"env", func(s *ExecSpec) string { return specField(s, func(s *ExecSpec) string { return pairs(s.Env, "=") }) }},
	{"url", func(s *ExecSpec) string { return specField(s, func(s *ExecSpec) string { return s.URL }) }},
	{"headers", func(s *ExecSpec) string { return specField(s, func(s *ExecSpec) string { return pairs(s.Headers, ": ") }) }},
}

func specField(s *ExecSpec, get func(*ExecSpec) string) string {
	if s == nil {
		return ""
	}
	return get(s)
}

func quoteAll(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = strconv.Quote(a)
	}
	return strings.Join(q, " ")
}

func pairs(m map[string]string, sep string) string {
	out := make([]string, 0, len(m))
	for _, k := range collections.SortedKeys(m) {
		out = append(out, k+sep+m[k])
	}
	return strings.Join(out, ", ")
}

func noneIfEmpty(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

// difflibLines splits a unified diff into lines without a trailing empty one.
func difflibLines(diff string) []string {
	if diff == "" {
		return nil
	}
	return strings.Split(strings.TrimRight(diff, "\n"), "\n")
}
