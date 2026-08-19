package archlint

import (
	"go/ast"

	"golang.org/x/tools/go/analysis"
)

// bindSessionAllowedCallers are the module-relative files permitted to call
// something named BindSession. Each entry is a deliberate admission.
//
// Manager's and MemStore's own definitions are function DECLARATIONS, not
// calls, so internal/sessions never needs an entry.
var bindSessionAllowedCallers = map[string]string{
	"internal/cli/session_bind.go":    "the SessionStart hook target; calls operations.BindSession, which calls Manager.BindSession",
	"internal/operations/sessions.go": "the BindSession façade itself, wrapping Manager.BindSession",
	"internal/memory/compactor.go":    "the compactor's forward-bind backstop; only binds an UNBOUND entry (entry.SessionID == \"\"), so it never reaches the displacement branch",
}

// SessionBindAnalyzer enforces that every writer of a harp's session binding
// routes through sessions.Manager.BindSession or sessions.MemStore.BindSession.
//
// Those two are the only implementations that append a displaced session id to
// Entry.Rotations. A writer that sets session_id or transcript_path by another
// path silently drops the lineage, and the loss is only visible much later,
// when a rotation cannot be traced back.
//
// The rule is a ratchet over call sites: a new file calling BindSession must
// be admitted here with a reason, and an entry that no longer calls it is
// reported so the admission cannot outlive what it admitted.
var SessionBindAnalyzer = &analysis.Analyzer{
	Name: "archsessionbind",
	Doc:  "session bindings must be written only through sessions.Manager/MemStore BindSession",
	Run:  runSessionBind,
}

func runSessionBind(pass *analysis.Pass) (any, error) {
	if SkipPass(pass) {
		return nil, nil
	}
	dir := PkgDir(pass)
	if dir == "" {
		return nil, nil
	}
	for _, f := range ProdFiles(pass) {
		rel := FileRel(pass, f)
		_, admitted := bindSessionAllowedCallers[rel]
		calls := false
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || CalleeName(call) != "BindSession" {
				return true
			}
			calls = true
			if admitted {
				return true
			}
			pass.Reportf(call.Pos(),
				"%s calls something named BindSession but is not in bindSessionAllowedCallers — a "+
					"session-binding writer outside sessions.Manager/MemStore risks displacing a live "+
					"binding without appending it to Entry.Rotations. Route this call through "+
					"sessions.Manager.BindSession (directly or via operations.BindSession), or add a "+
					"reviewed entry to internal/archlint/sessionbind.go stating why this call site "+
					"cannot lose lineage.", rel)
			return true
		})
		if admitted && !calls && allowlistLivenessEnabled() {
			pass.Reportf(f.Package,
				"bindSessionAllowedCallers lists %q but it no longer calls BindSession — remove the "+
					"stale entry", rel)
		}
	}
	return nil, nil
}
