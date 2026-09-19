package coord

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// summaryFactAt builds one journaled summary fact for the fold-level tests.
func summaryFactAt(harp, runID string, seq uint64, text string) Fact {
	return factAt(factSummary, time.Now(), summaryFact{
		Harp:  harp,
		RunID: runID,
		Seq:   seq,
		Scope: agentcoordpb.Summary_SCOPE_PROGRESS.String(),
		Text:  text,
	})
}

// artifactFactAt builds one journaled artifact manifest fact.
func artifactFactAt(harp, artifactID string, rev uint32, sha string) Fact {
	return factAt(factArtifact, time.Now(), artifactFact{
		Harp:       harp,
		ArtifactID: artifactID,
		Revision:   rev,
		Name:       artifactID + "-r" + string(rune('0'+rev)),
		SHA256:     sha,
	})
}

// latestSummary truncated the roster line with text[:200] — a BYTE
// slice of a UTF-8 string. A report written in any non-ASCII script (or one
// that merely contains an emoji or a typographic dash near the boundary) had
// its final rune cut in half, so the roster rendered a U+FFFD replacement
// character: mojibake in the one line an operator scans to see what an agent
// is doing. The truncation must land on a rune boundary.
func TestLatestSummary_TruncatesOnARuneBoundary(t *testing.T) {
	f := newReportsFold()
	// "日" is 3 bytes, so a 200-BYTE cut lands mid-rune (200 % 3 == 2).
	f.apply(summaryFactAt("child-a", "run-1", 1, strings.Repeat("日", 300)))

	got := f.latestSummary("child-a")

	assert.True(t, utf8.ValidString(got), "the truncated roster line must stay valid UTF-8: %q", got)
	assert.NotContains(t, got, "�",
		"a byte-wise cut split a multi-byte rune and rendered U+FFFD in the roster line")
	assert.Contains(t, got, "…", "an over-long line is still marked as truncated")
}

// TestLatestSummary_ShortAndASCIILinesUnchanged keeps the rune-safe cut from
// changing the ordinary cases: a short line is verbatim, and the first line
// alone is used.
func TestLatestSummary_ShortAndASCIILinesUnchanged(t *testing.T) {
	f := newReportsFold()
	f.apply(summaryFactAt("child-a", "run-1", 1, "all good\nsecond line ignored"))

	assert.Equal(t, "PROGRESS: all good", f.latestSummary("child-a"))
	assert.Empty(t, f.latestSummary("nobody"), "no report, no line")
}

// The artifact arm of reportsFold.apply overwrote
// byID[artifact_id] unconditionally, so a manifest fact carrying a LOWER
// revision than the one already folded replaced it — and recordArtifact trusts
// a producer-supplied non-zero revision without checking it. A replayed or
// out-of-order fact therefore rolled the "latest revision" projection
// BACKWARDS, and every consumer of it (roster, DownloadArtifact resolution)
// then resolved the stale manifest.
//
// The fold's own documented meaning is "each artifact's LATEST revision", so a
// lower revision is never the latest.
func TestReportsFold_ArtifactRevisionNeverGoesBackwards(t *testing.T) {
	f := newReportsFold()
	f.apply(artifactFactAt("child-a", "plan", 2, "sha-two"))
	f.apply(artifactFactAt("child-a", "plan", 1, "sha-one"))

	rec := f.artifacts["child-a"]["plan"]
	assert.Equal(t, uint32(2), rec.Revision,
		"a lower-revision manifest must not displace the latest one")
	assert.Equal(t, "sha-two", rec.SHA256,
		"the whole record must stay on the latest revision, not just its number")

	// A HIGHER revision still advances it — the guard bounds the direction, it
	// does not freeze the projection.
	f.apply(artifactFactAt("child-a", "plan", 3, "sha-three"))
	assert.Equal(t, uint32(3), f.artifacts["child-a"]["plan"].Revision)
	assert.Equal(t, "sha-three", f.artifacts["child-a"]["plan"].SHA256)

	// An equal revision is a redelivery of the same manifest: idempotent, and
	// it must not corrupt the record either.
	f.apply(artifactFactAt("child-a", "plan", 3, "sha-three"))
	assert.Equal(t, uint32(3), f.artifacts["child-a"]["plan"].Revision)
	assert.Equal(t, "sha-three", f.artifacts["child-a"]["plan"].SHA256)
}

// Artifacts ranged over a Go map, so it returned the harp's
// manifests in a different order on every call. Any caller rendering them as a
// list (roster, agent_fetch_artifact's discovery listing) shows a different
// order each time, which reads as churn and makes two listings impossible to
// diff. Ordered by artifact id — a stable, caller-independent key.
func TestArtifacts_ReturnsAStableOrder(t *testing.T) {
	c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)

	ids := []string{"zeta", "mu", "alpha", "omega", "beta", "kappa", "delta", "sigma"}
	for _, id := range ids {
		c.recordArtifact("child-a", &agentcoordpb.ArtifactProduced{
			ArtifactId: id,
			Name:       id,
			Sha256:     []byte(id),
		})
	}

	first := c.Artifacts("child-a")
	require.Len(t, first, len(ids))

	var got []string
	for _, rec := range first {
		got = append(got, rec.ArtifactID)
	}
	want := []string{"alpha", "beta", "delta", "kappa", "mu", "omega", "sigma", "zeta"}
	assert.Equal(t, want, got, "Artifacts must be ordered by artifact id, not by map iteration")

	// Stability is the point: repeated calls agree.
	for range 8 {
		var again []string
		for _, rec := range c.Artifacts("child-a") {
			again = append(again, rec.ArtifactID)
		}
		if !assert.Equal(t, want, again, "two Artifacts calls must return the same order") {
			return
		}
	}
}
