package operations

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

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
// agree (or only one side exists), as before -> after when they differ. Env
// and headers print one line per name, marked + - ~ when both sides exist.
func writeExecDelta(w io.Writer, d ExecDelta) {
	for _, f := range execFields {
		if f.kv != nil {
			writeKVDelta(w, f.name, d, f.kv)
			continue
		}
		before, after := specText(d.Before, f.text), specText(d.After, f.text)
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

// writeKVDelta prints one map field (env, headers) name by name.
func writeKVDelta(w io.Writer, name string, d ExecDelta, kv func(*ExecSpec) map[string]string) {
	before, after := specKV(d.Before, kv), specKV(d.After, kv)
	keys := unionKeys(before, after)
	sort.Strings(keys)
	if len(keys) == 0 {
		return
	}
	fmt.Fprintf(w, "      %s:\n", name)
	oneSided := d.Before == nil || d.After == nil
	for _, k := range keys {
		b, inBefore := before[k]
		a, inAfter := after[k]
		change, differs := changeOf(inBefore, inAfter, b == a)
		switch {
		case oneSided || !differs:
			fmt.Fprintf(w, "          %s: %s\n", k, presentValue(a, inAfter, b))
		case change == ChangeModified:
			fmt.Fprintf(w, "        ~ %s: %s -> %s\n", k, b, a)
		case change == ChangeAdded:
			fmt.Fprintf(w, "        + %s: %s\n", k, a)
		default:
			fmt.Fprintf(w, "        - %s: %s\n", k, b)
		}
	}
}

// presentValue is after when present, else before.
func presentValue(after string, inAfter bool, before string) string {
	if inAfter {
		return after
	}
	return before
}

// execField is one ExecSpec field in display order: text for a scalar, kv for
// a name -> value map.
type execField struct {
	name string
	text func(*ExecSpec) string
	kv   func(*ExecSpec) map[string]string
}

var execFields = []execField{
	{name: "command", text: func(s *ExecSpec) string { return s.Command }},
	{name: "args", text: func(s *ExecSpec) string { return quoteAll(s.Args) }},
	{name: "env", kv: func(s *ExecSpec) map[string]string { return s.Env }},
	{name: "url", text: func(s *ExecSpec) string { return s.URL }},
	{name: "headers", kv: func(s *ExecSpec) map[string]string { return s.Headers }},
}

func specText(s *ExecSpec, get func(*ExecSpec) string) string {
	if s == nil {
		return ""
	}
	return get(s)
}

func specKV(s *ExecSpec, get func(*ExecSpec) map[string]string) map[string]string {
	if s == nil {
		return nil
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
