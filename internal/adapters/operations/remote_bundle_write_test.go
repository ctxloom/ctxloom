package operations

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
)

// A write aimed at a bundle pinned from a remote is refused as exactly that —
// naming the remote, the pin and the remedy — not reported as "not found" about
// a bundle the same ref just read successfully. Both the canonical ref and the
// short "<remote>/<bundle>" form a user types must reach the refusal.
func TestItemWrite_PinnedRemoteBundleRefusesNamingThePin(t *testing.T) {
	cfg, canonicalRef, shortRef := seedRemoteFragmentFixture(t)

	lock, err := remote.NewLockfileManager(cfg.GetAppPaths()[0]).Load()
	require.NoError(t, err)
	require.Len(t, lock.Bundles, 1)
	var pin remote.LockEntry
	for _, e := range lock.Bundles {
		pin = e
	}

	parsed, err := remote.ParseReference(canonicalRef)
	require.NoError(t, err)

	ctx := context.Background()
	writes := map[string]func(ref string) error{
		"SetBundleMCP": func(ref string) error {
			_, err := SetBundleMCP(ctx, cfg, SetBundleMCPRequest{Bundle: ref, Name: "srv", MCP: BundleMCPInput{Command: "x"}})
			return err
		},
		"AddItem": func(ref string) error {
			_, err := AddItem(ctx, cfg, AddItemRequest{Bundle: ref, Kind: ItemKindFragment, Name: "n", Content: "c"})
			return err
		},
		"UpdateBundle": func(ref string) error {
			_, err := UpdateBundle(ctx, cfg, UpdateBundleRequest{Name: ref, SetDescription: sp("d")})
			return err
		},
	}
	for _, ref := range []string{canonicalRef, shortRef} {
		for op, write := range writes {
			err := write(ref)
			require.Errorf(t, err, "%s %q", op, ref)
			assert.Truef(t, errors.Is(err, ErrPinnedRemoteBundle), "%s %q: want ErrPinnedRemoteBundle, got %v", op, ref, err)
			var perr *PinnedRemoteBundleError
			require.Truef(t, errors.As(err, &perr), "%s %q", op, ref)
			assert.Equal(t, parsed.CanonicalString(), perr.Bundle)
			assert.Equal(t, pin.URL, perr.Remote)
			assert.Equal(t, pin.SHA, perr.Pin)
			assert.NotEmpty(t, perr.Tree)
			for _, want := range []string{pin.URL, pin.SHA, "ctxloom bundle import " + perr.Tree, "ctxloom deps upgrade"} {
				assert.Containsf(t, err.Error(), want, "%s %q: the refusal names %q", op, ref, want)
			}
		}
	}
}

// A write whose bundle the store does not hold must not fall back to "not
// found" when the lockfile that decides whether it is a pinned remote bundle
// could not be read: that verdict was never established, so the read failure
// is the error, carried through for the user to fix.
func TestItemWrite_UnreadableLockfileSurfacesInsteadOfNotFound(t *testing.T) {
	cfg, canonicalRef, _ := seedRemoteFragmentFixture(t)

	// A directory where lock.yaml should be: present, and unreadable as a file.
	lockPath := remote.NewLockfileManager(cfg.GetAppPaths()[0]).Path()
	require.NoError(t, os.Remove(lockPath))
	require.NoError(t, os.Mkdir(lockPath, 0o755))

	_, err := UpdateBundle(context.Background(), cfg, UpdateBundleRequest{Name: canonicalRef, SetDescription: sp("d")})
	require.Error(t, err)
	assert.ErrorIs(t, err, syscall.EISDIR, "the lockfile read failure must be carried, not replaced by not-found")
	assert.NotErrorIs(t, err, ErrPinnedRemoteBundle, "an unread lockfile establishes no pin")
}
