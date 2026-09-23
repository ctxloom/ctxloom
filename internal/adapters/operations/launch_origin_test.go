package operations

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/sessionlock"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// The mint is the one place that knows who a session is for, so it is the
// one place that records it: a sweep in a later process decides whether an
// undistilled session may be purged by this stamp alone.
func TestMintIdentity_StampsTheSeedsOrigin(t *testing.T) {
	testsupport.Isolate(t)
	for _, tc := range []struct {
		seed sessions.Seed
		want sessions.Origin
	}{
		{sessions.Seed{ProjectDir: "/proj"}, sessions.OriginSession},
		{sessions.Seed{ProjectDir: "/proj", Depth: 1}, sessions.OriginAgent},
		{sessions.Seed{ProjectDir: "/proj", OneShot: true}, sessions.OriginOneShot},
	} {
		store := sessions.NewMemStore()
		id, err := MintIdentity(store, tc.seed)
		require.NoError(t, err)
		t.Cleanup(func() { sessionlock.Release(id.Harp) })
		got, err := store.Find(id.Harp)
		require.NoError(t, err)
		assert.Equal(t, tc.want, got.Origin)
	}
}
