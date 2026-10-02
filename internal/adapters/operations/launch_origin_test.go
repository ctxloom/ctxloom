package operations

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/composite/compositetest"
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
		id, err := MintIdentity(store, tc.seed, compositetest.Trust())
		require.NoError(t, err)
		t.Cleanup(func() { sessionlock.Release(id.Harp) })
		got, err := store.Find(id.Harp)
		require.NoError(t, err)
		assert.Equal(t, tc.want, got.Origin)
	}
}

// session.yaml records whether the run that minted the session waived
// signature verification — read off the generation's Trust, the same value
// that decided what the session was delivered, never a caller's separate bool.
func TestMintIdentity_StampsTheTrustsSignatureCheckPosture(t *testing.T) {
	testsupport.Isolate(t)
	root, records, retraction := compositetest.Ports()
	waived, err := composite.NewTrust(root, records, retraction, composite.WithoutSignatureCheck())
	require.NoError(t, err)
	for _, tc := range []struct {
		name string
		tr   composite.Trust
		want bool
	}{
		{"enforced", compositetest.Trust(), false},
		{"waived", waived, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := sessions.NewMemStore()
			id, err := MintIdentity(store, sessions.Seed{ProjectDir: "/proj"}, tc.tr)
			require.NoError(t, err)
			t.Cleanup(func() { sessionlock.Release(id.Harp) })
			got, err := store.Find(id.Harp)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got.SigCheckDisabled)
		})
	}
}
