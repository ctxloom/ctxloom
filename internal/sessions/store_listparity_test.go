package sessions

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestListForProject_EnrichmentParityAcrossStores is the parity test across the
// two implementations of the list path, written BEFORE collapsing their
// duplicated activity-sort comparator.
//
// The comparator itself is copy-pasted verbatim, so it cannot diverge. The
// enrichment wrapped around it can, and does: *Manager runs
// fillTranscriptByLocation, MemStore does not. A session whose bound vendor
// transcript was pruned but whose transcript is discoverable BY LOCATION (the
// containerized case -- the engine's store root is bind-mounted into the harp
// dir) therefore resolves through the real store and not through the fake that
// every other package's tests run against. That divergence is the defect the
// duplication was hiding.
func TestListForProject_EnrichmentParityAcrossStores(t *testing.T) {
	adapters := []struct {
		name string
		make func(t *testing.T) Store
	}{
		{"MemStore", func(*testing.T) Store { return NewMemStore() }},
		{"Manager", func(t *testing.T) Store {
			m, err := Open(filepath.Join(t.TempDir(), "index.yaml"))
			require.NoError(t, err)
			return m
		}},
	}

	for _, a := range adapters {
		t.Run(a.name, func(t *testing.T) {
			testsupport.Isolate(t)
			s := a.make(t)

			e, err := s.AssignHarp("/proj", "claude")
			require.NoError(t, err)

			// Bind a vendor transcript, then delete it: the binding is stale.
			pruned := filepath.Join(t.TempDir(), "vendor.jsonl")
			require.NoError(t, os.WriteFile(pruned, []byte("{}\n"), 0o644))
			require.NoError(t, s.BindSession(e.HarpName, "sess-1", pruned))
			require.NoError(t, os.Remove(pruned))

			// But the transcript IS discoverable by location.
			store, err := paths.HarpTranscriptStoreDir(e.HarpName)
			require.NoError(t, err)
			located := filepath.Join(store, "enc", "abc.jsonl")
			require.NoError(t, os.MkdirAll(filepath.Dir(located), 0o755))
			require.NoError(t, os.WriteFile(located, []byte("{}\n"), 0o644))

			// The fixture must be hostile from the package's own vantage point:
			// the stale binding must really be stale, and the located file must
			// really be locatable, or the assertion below proves nothing.
			_, statErr := os.Stat(pruned)
			require.ErrorIs(t, statErr, os.ErrNotExist, "the bound transcript must genuinely be gone")
			gotPath, ok := LocateTranscript(e.HarpName)
			require.True(t, ok, "the fixture transcript must be locatable")
			require.Equal(t, located, gotPath)

			rows, err := s.ListForProject("/proj")
			require.NoError(t, err)
			require.Len(t, rows, 1)
			require.Equal(t, located, rows[0].TranscriptPath,
				"%s must resolve a stale binding by location, like its sibling store", a.name)
		})
	}
}
