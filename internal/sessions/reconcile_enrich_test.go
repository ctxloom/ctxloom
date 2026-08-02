package sessions

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestReconcile_SurvivorsCarryCanonicalTranscriptPath pins that Reconcile
// returns entries enriched the same way every other entry-returning method
// does.
//
// Reconcile judges a COPY it enriches, which is what stops a
// recoverable session being reaped -- but it used to hand back the RAW
// survivors, so a caller taking Reconcile's return saw
// CanonicalTranscriptPath == "" for a session that plainly has one. That is
// not cosmetic: Entry.SourceStale prefers the canonical transcript precisely
// because comparing the stamped size against the legacy engine file compares
// two different sources and the staleness badge lies. An entry-returning method
// whose entries mean something different from every sibling's is a trap set for
// the next caller.
func TestReconcile_SurvivorsCarryCanonicalTranscriptPath(t *testing.T) {
	testsupport.Isolate(t)

	m, err := Open(filepath.Join(t.TempDir(), "index.yaml"))
	require.NoError(t, err)
	e, err := m.AssignHarp("/proj", "claude")
	require.NoError(t, err)

	canonical, err := paths.ResolveHarpCanonicalTranscriptPath(e.HarpName)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(canonical), 0o755))
	require.NoError(t, os.WriteFile(canonical, []byte("{}\n"), 0o644))

	// The fixture must be hostile from the package's own vantage point: if the
	// canonical transcript is not discoverable at all, every assertion below is
	// vacuous. Find is the reference enrichment.
	ref, err := m.Find(e.HarpName)
	require.NoError(t, err)
	require.NotNil(t, ref)
	require.Equal(t, canonical, ref.CanonicalTranscriptPath,
		"the fixture's canonical transcript must be discoverable, or this test proves nothing")

	survivors, err := m.Reconcile(func(Entry) bool { return false })
	require.NoError(t, err)
	require.Len(t, survivors, 1)
	require.Equal(t, canonical, survivors[0].CanonicalTranscriptPath,
		"Reconcile's survivors must be enriched like Find's and ListForProject's")
}

// TestReconcile_DoesNotPersistTheComputedCanonicalPath is the other half: the
// enrichment is computed-on-read, so it must not reach index.yaml. The fill
// happens after the save decision for exactly this reason.
func TestReconcile_DoesNotPersistTheComputedCanonicalPath(t *testing.T) {
	testsupport.Isolate(t)

	path := filepath.Join(t.TempDir(), "index.yaml")
	m, err := Open(path)
	require.NoError(t, err)
	e, err := m.AssignHarp("/proj", "claude")
	require.NoError(t, err)

	canonical, err := paths.ResolveHarpCanonicalTranscriptPath(e.HarpName)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(canonical), 0o755))
	require.NoError(t, os.WriteFile(canonical, []byte("{}\n"), 0o644))

	// Force a save by making one entry dead, so the persisted bytes are the
	// ones Reconcile itself wrote rather than AssignHarp's.
	dead, err := m.AssignHarp("/proj", "claude")
	require.NoError(t, err)
	survivors, err := m.Reconcile(func(x Entry) bool { return x.HarpName == dead.HarpName })
	require.NoError(t, err)
	require.Len(t, survivors, 1)

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "canonical_transcript_path",
		"the computed canonical path must never be persisted to the index")
	require.NotContains(t, string(raw), canonical,
		"the computed canonical path must never be persisted to the index")
}
