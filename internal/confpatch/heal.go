package confpatch

import (
	"reflect"
	"strings"

	hew "github.com/benjaminabbitt/hew/go"
	yamlv3 "gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/core/agent"
)

// ownedCandidate is one pointer a caller believes it manages, and — when it has
// a record of the fact — the value it wrote there.
type ownedCandidate struct {
	Pointer string
	// Recorded is what ctxloom wrote at Pointer, from its own record. Nil when
	// the caller has no record to appeal to (see WithOwnedPaths), which leaves
	// executable identity as the only proof available.
	Recorded *yamlv3.Node
}

// healOwnedDrift rebuilds the RESTORED document when the recorded reversal no
// longer applies, for the single case where refusing is the wrong answer.
//
// Apply's refusal exists because a reversal that will not fit means the USER
// edited the region ctxloom manages. That premise fails in one specific way:
// ctxloom writes the running binary's own absolute path into the entry (see
// selfexec.Path), so a second ctxloom — the copy on PATH versus one built in a
// working tree — writes a DIFFERENT value into its own entry. Neither wrote a
// record the other can reverse, and the loser reads the winner's entry as a
// foreign edit and refuses. Every backend then fails to configure, because one
// wedged target takes the whole apply down.
//
// So this asks a narrower question than "which entries in this file are
// ctxloom's?" — the question the package doc rejects, because a foreign file
// cannot answer it. It asks only: AT THE PATHS THIS RECORD SAYS CTXLOOM
// CREATED, is what sits there still recognizably ctxloom's own? The record
// supplies the paths from ctxloom's own memory; ownership is then a bounded
// check on those paths alone. A path holding anything else means a real user
// edit, and the caller refuses exactly as before.
//
// It returns the restored bytes, the paths it took back out (for the caller's
// warning), and whether the heal applies at all.
func healOwnedDrift(binding hew.Binding, format hew.FormatID, target string, before []byte, prev Record, owner string) ([]byte, []string, bool) {
	created := createdPaths(prev, target)
	if len(created) == 0 {
		return nil, nil, false
	}
	out, removed, unowned, err := ownedRemovals(binding, format, target, before, created, owner)
	if err != nil || len(unowned) > 0 {
		// A path ctxloom created now holds something it cannot prove is its
		// own: that IS the user edit the refusal exists for.
		return nil, nil, false
	}
	if len(removed) == 0 {
		return before, nil, true
	}
	return out, removed, true
}

// ownedRemovals removes, from doc, each pointer whose CURRENT content the
// writer (owner, an executable basename) can prove is its own, and reports both
// what it took out and which pointers held something else.
//
// Splitting "what is mine here?" from "what should I do about it?" is what lets
// the two callers differ where they must: reversing a recorded application
// REFUSES when a path is not ctxloom's (the user edited it), while re-applying
// over an entry with no record simply LEAVES it and overwrites in place, which
// is what ctxloom has always done to a name it manages.
func ownedRemovals(binding hew.Binding, format hew.FormatID, target string, doc []byte, candidates []ownedCandidate, owner string) (out []byte, removed, unowned []string, err error) {
	cur, err := binding.Document(target, doc)
	if err != nil {
		return nil, nil, nil, err
	}
	d, err := hew.OpenBytes(target, doc, hew.As(format))
	if err != nil {
		return nil, nil, nil, err
	}
	for _, c := range candidates {
		ptr := c.Pointer
		node, present := nodeAt(cur.Root(), ptr)
		if !present {
			// Already gone: nothing of ctxloom's left to take out here.
			continue
		}
		if !ctxloomWrote(node, c.Recorded) && !ownedBy(node, owner) {
			unowned = append(unowned, ptr)
			continue
		}
		p, perr := hew.ParsePathIn(format, ptr)
		if perr != nil {
			return nil, nil, nil, perr
		}
		d.AtPath(p).Remove()
		removed = append(removed, ptr)
	}
	if len(removed) == 0 {
		return doc, nil, unowned, nil
	}
	out, err = d.Bytes()
	if err != nil {
		return nil, nil, nil, err
	}
	return out, removed, unowned, nil
}

// createdPaths lists the pointers the record says ctxloom ADDED to target,
// each carrying the value it wrote there.
//
// Only additions are healable: a `replace` overwrote a value that was already
// the user's, and restoring it needs the recorded pre-image the reversal
// carries — which is precisely what no longer applies.
func createdPaths(prev Record, target string) []ownedCandidate {
	var out []ownedCandidate
	for _, t := range prev.Targets {
		if t.Target != target {
			continue
		}
		for i, op := range t.Transforms {
			if op.Op != "add" || op.Path == "" {
				continue
			}
			out = append(out, ownedCandidate{Pointer: op.Path, Recorded: &t.Transforms[i].Value})
		}
	}
	return out
}

// nodeAt walks an RFC 6901 pointer down from root through map members. It
// deliberately handles only the map spine: every path this heals is a named
// entry ctxloom added to a named container, and a pointer into a sequence
// cannot be shown to be ctxloom's by name.
func nodeAt(root hew.Node, pointer string) (hew.Node, bool) {
	if root == nil {
		return nil, false
	}
	node := root
	for _, seg := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		if seg == "" {
			continue
		}
		if node.Kind() != hew.KindMap {
			return nil, false
		}
		next, ok := node.Member(unescapePointer(seg))
		if !ok {
			return nil, false
		}
		node = next
	}
	return node, true
}

// unescapePointer decodes RFC 6901's two escapes, ~1 for "/" and ~0 for "~",
// in that order (reversing them would turn "~01" into "/" instead of "~1").
func unescapePointer(seg string) string {
	return strings.ReplaceAll(strings.ReplaceAll(seg, "~1", "/"), "~0", "~")
}

// ownedBy reports whether node is an entry the writer whose executable basename
// is owner wrote: an object whose `command` invokes that binary, or the command
// string itself.
//
// It proves ownership from the EXECUTABLE the entry runs, not from the entry's
// name. A name proves nothing — a user may keep a "ctxloom" key pointing at
// their own wrapper, and taking that out would be the clobber this whole
// package exists to prevent.
func ownedBy(node hew.Node, owner string) bool {
	switch node.Kind() {
	case hew.KindScalar:
		return agent.IsManaged(scalarString(node), owner)
	case hew.KindMap:
		cmd, ok := node.Member("command")
		if !ok || cmd.Kind() != hew.KindScalar {
			return false
		}
		return agent.IsManaged(scalarString(cmd), owner)
	default:
		return false
	}
}

func scalarString(node hew.Node) string {
	var s string
	if err := node.Value().Decode(&s); err != nil {
		return ""
	}
	return s
}

// ctxloomWrote reports whether node still holds exactly what the record says
// ctxloom put there.
//
// This is the FIRST and strongest ownership proof, and it exists because
// identity alone was too narrow: ctxloom writes entries that run OTHER
// programs — a bundle's MCP server invoking npx, or a companion binary — and
// those are as much ctxloom's writes as the one invoking ctxloom itself.
// Requiring every managed entry to run ctxloom made a single npx server block
// the heal for the whole file, which is a refusal over an entry nobody had
// touched.
//
// Untouched-since-written is the honest test for those: if the bytes still
// match the record, no user edit is at stake and taking the entry back out
// restores exactly the state the record describes. A nil Recorded means the
// caller has no record to appeal to, which is not ownership — identity decides
// those.
func ctxloomWrote(node hew.Node, recorded *yamlv3.Node) bool {
	if recorded == nil || recorded.IsZero() {
		return false
	}
	var want, got any
	if err := recorded.Decode(&want); err != nil {
		return false
	}
	if err := node.Value().Decode(&got); err != nil {
		return false
	}
	return reflect.DeepEqual(want, got)
}
