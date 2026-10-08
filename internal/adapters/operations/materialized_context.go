package operations

import (
	"fmt"

	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// MaterializedContextFindings is one advisory finding per file under
// projectRoot that holds context an earlier `ctxloom materialize` wrote
// (R5): a session started there may have the engine read that file beside
// the context the session delivers itself. A warning, never a refusal —
// --surface can leave context out of a materialize — and each names how to
// take it back out. `ctxloom run` reports them before it launches.
func MaterializedContextFindings(records delivery.Ownership, projectRoot string) []report.Finding {
	if records == nil || projectRoot == "" {
		return nil
	}
	files, err := delivery.ProjectContextClaims(records, projectRoot)
	if err != nil {
		return nil
	}
	out := make([]report.Finding, 0, len(files))
	for _, f := range files {
		out = append(out, report.Finding{
			Text:   fmt.Sprintf("%s holds context an earlier `ctxloom materialize` wrote into this project; the engine may read it beside this session's own context", f),
			Remedy: "ctxloom materialize --release --surface context --target " + projectRoot,
		})
	}
	return out
}
