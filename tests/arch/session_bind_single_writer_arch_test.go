//go:build arch

// EVERY WRITER OF A HARP'S SESSION BINDING MUST ROUTE THROUGH
// sessions.Manager.BindSession (or sessions.MemStore.BindSession).
//
// exposable-rental unit 3: a live incident displaced harp ugly-icy-squid's
// binding (409a57fc -> a27bcbb4) WITHOUT appending the displaced id to
// Entry.Rotations, which is exactly the lineage-loss bug BindSession's own
// displacement-append (see sessions/index.go's doc comment) exists to
// prevent. Manager.BindSession and MemStore.BindSession are themselves
// unit-pinned (internal/sessions' own tests) to append correctly — the
// open question this gate answers is whether some OTHER code path writes a
// harp's session_id/transcript_path without going through either of them,
// bypassing the append entirely.
//
// A full-tree enumeration at HEAD (`grep -rn '\.BindSession(' --include
// '*.go' . | grep -v _test`) found exactly three non-test call sites, all of
// which resolve to Manager.BindSession or MemStore.BindSession:
//
//   - internal/cli/session_bind.go (bindSessionFromPayload, the SessionStart
//     hook target) -> operations.BindSession -> mgr.BindSession
//   - internal/operations/sessions.go (the BindSession façade itself)
//     -> mgr.BindSession
//   - internal/memory/compactor.go (updateSessionIndex, the compactor's
//     forward-bind backstop) -> mgr.BindSession directly, and ONLY when the
//     entry is not yet bound (entry.SessionID == "") — it can never reach
//     the displacement branch at all, since a bound entry is left untouched.
//
// No fourth writer exists at HEAD: the live incident's clobber is the OLD
// binary's pre-rotation-lineage hook (see steadfast-womankind, a related
// finding from the same sweep, which names the same "pre-rotation-lineage
// binary, no rotation recorded" shape), not a bypass in current code. This
// gate keeps that enumeration true going forward: a new `.BindSession(` call
// site anywhere in the module must be added to bindSessionAllowedCallers (a
// deliberate, reviewable admission) or it fails the build — the same ratchet
// shape write_discipline_test.go uses for iox.
package arch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// bindSessionAllowedCallers are the module-relative files permitted to call
// something named BindSession. Each entry is a deliberate admission with a
// reason — see the package doc above for why each one preserves lineage.
var bindSessionAllowedCallers = map[string]string{
	"internal/cli/session_bind.go":    "the SessionStart hook target; calls operations.BindSession, which calls Manager.BindSession",
	"internal/operations/sessions.go": "the BindSession façade itself, wrapping Manager.BindSession",
	"internal/memory/compactor.go":    "the compactor's forward-bind backstop; only binds an UNBOUND entry (entry.SessionID == \"\"), so it never reaches the displacement branch",
	// Manager's and MemStore's OWN definitions are function DECLARATIONS,
	// not CallExpr — findBindSessionCallers below only matches calls, so
	// internal/sessions/index.go and memstore.go never need an entry here.
}

// findBindSessionCallers walks every non-test .go file under root and
// returns the module-relative files containing at least one CallExpr whose
// selector is named "BindSession" — regardless of receiver, so a future
// writer that names its own method BindSession (rather than routing through
// Manager/MemStore) is still caught by name rather than slipping past a
// receiver-type check this package cannot resolve without go/types.
func findBindSessionCallers(t *testing.T) map[string]bool {
	t.Helper()
	root := moduleRoot(t)
	fset := token.NewFileSet()
	hits := map[string]bool{}

	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if p != root && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") ||
				name == "testdata" || name == "vendor" || name == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if perr != nil {
			t.Errorf("parse %s: %v", p, perr)
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)

		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			var calleeName string
			switch fn := call.Fun.(type) {
			case *ast.SelectorExpr:
				calleeName = fn.Sel.Name
			case *ast.Ident:
				calleeName = fn.Name
			}
			if calleeName == "BindSession" {
				hits[rel] = true
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return hits
}

// TestArch_SessionBindHasNoUnreviewedCaller enumerates every call to
// something named BindSession across the whole module and fails if one turns
// up outside bindSessionAllowedCallers — a new writer of a harp's session
// binding that does not route through sessions.Manager/MemStore is exactly
// the shape of bug exposable-rental unit 3 investigated: a displacement that
// skips Entry.Rotations' lineage-preserving append.
func TestArch_SessionBindHasNoUnreviewedCaller(t *testing.T) {
	hits := findBindSessionCallers(t)
	if len(hits) == 0 {
		t.Fatal("found zero BindSession call sites in the whole module — the scan itself is broken (the known callers, e.g. internal/cli/session_bind.go, must be found), not a sign that no code binds sessions")
	}

	var unreviewed []string
	for file := range hits {
		if _, ok := bindSessionAllowedCallers[file]; !ok {
			unreviewed = append(unreviewed, file)
		}
	}
	sort.Strings(unreviewed)
	for _, file := range unreviewed {
		t.Errorf("%s calls something named BindSession but is not in bindSessionAllowedCallers — "+
			"a session-binding writer outside sessions.Manager/MemStore risks displacing a live "+
			"binding without appending it to Entry.Rotations (exposable-rental unit 3). Route this "+
			"call through sessions.Manager.BindSession (directly or via operations.BindSession), or "+
			"add a reviewed entry here stating why this call site cannot lose lineage.", file)
	}

	// The allowlist itself must stay live: an entry naming a file that no
	// longer calls BindSession at all is a stale admission masking whatever
	// the file does now — the same "allowlist can rot" check
	// write_discipline_test.go runs for its own ratchet.
	for file := range bindSessionAllowedCallers {
		if !hits[file] {
			t.Errorf("bindSessionAllowedCallers lists %q but it no longer calls BindSession — remove the stale entry", file)
		}
	}
}
