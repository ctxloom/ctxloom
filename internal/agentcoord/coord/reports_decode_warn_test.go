package coord

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// Both arms of reportsFold.apply discarded an undecodable fact with
// a bare `if fact.decode(&p) != nil { return }` — no warning, no counter, no
// trace. A corrupt or truncated line in the reports journal therefore looks
// EXACTLY like an agent that never filed a report: the roster shows the
// previous summary, `LatestReport` shows the previous summary, and nothing
// anywhere says a durable record was skipped. The fold must still continue (a
// fold may not fail on its own store's history) — but it must say so.
func TestReportsFold_UndecodableFactWarns(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind string
		want string
	}{
		{name: "summary", kind: factSummary, want: "report.summary"},
		{name: "artifact", kind: factArtifact, want: "report.artifact"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			restore := clidiag.SetSink(&buf)
			defer restore()

			f := newReportsFold()
			// A payload of the wrong JSON shape is what a truncated or
			// partially-written journal line decodes to.
			f.apply(Fact{Kind: tc.kind, At: time.Now(), Data: []byte(`"not-an-object"`)})

			assert.Contains(t, buf.String(), tc.want,
				"an undecodable %s fact must name the kind it dropped", tc.kind)
			assert.Contains(t, buf.String(), "warning:",
				"the drop must reach the diagnostic channel, not vanish")
		})
	}
}

// TestReportsFold_DecodableFactIsSilent keeps the new warning from firing on
// the ordinary path — a warning on every good fact would bury the one that
// matters (and replay applies every fact in the journal).
func TestReportsFold_DecodableFactIsSilent(t *testing.T) {
	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	f := newReportsFold()
	f.apply(summaryFactAt("child-a", "run-1", 1, "fine"))
	f.apply(artifactFactAt("child-a", "plan", 1, "sha-one"))

	assert.Empty(t, buf.String(), "a well-formed fact must not warn")
	assert.Equal(t, "PROGRESS: fine", f.latestSummary("child-a"))
}
