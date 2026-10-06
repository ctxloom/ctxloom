package bundles

import (
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// ledger is the real rendering sink, for a test that asserts on the strictness
// ledger or the clidiag stream exactly as the binary's user would see them.
func ledger() report.Sink { return strictness.Sink("ctxloom") }

// admitAllPipe wraps a reader in a pipeline with no link policy — the right
// shape for a test exercising resolution and form selection.
func admitAllPipe(l *Loader, preferDistilled bool) *Pipeline {
	return NewPipeline(l, LinksUnchecked(), preferDistilled)
}
