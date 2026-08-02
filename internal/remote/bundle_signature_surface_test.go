package remote

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/errs"
)

// "This source has no signature surface" and "this bundle has no
// .sig" arrived as the same error, both wrapping errs.ErrRemoteContentNotFound
// and nothing else. The first is a wiring mistake -- the decorator was handed
// an inner source that does not implement BundleSignatureSource, which no
// production wiring does -- and the second is the ordinary, legal way an
// unsigned bundle is signalled. One is a bug in us and one is a fact about the
// content, and they were the same value.
//
// The sentinel is deliberately kept, so no caller's behaviour changes: absence
// still reads as absence, which is what makes an unsigned bundle unsigned
// rather than broken. What is added is a second wrapped sentinel that only the
// capability case carries, so the two can be told apart by anything that wants
// to.
type sigless struct{ BundleByteSource }

func TestCachingBundleReader_MissingSignatureSurfaceIsDistinguishable(t *testing.T) {
	ctx := context.Background()

	t.Run("a source with no signature surface says so", func(t *testing.T) {
		c := NewCachingBundleReader(sigless{})
		_, err := c.ReadBundleSignature(ctx, "acme/tools")
		require.Error(t, err)

		assert.ErrorIs(t, err, ErrNoSignatureSurface,
			"the capability gap must be nameable; nothing in production wiring produces it")
		assert.ErrorIs(t, err, errs.ErrRemoteContentNotFound,
			"and must keep reading as absence, so an unsigned bundle stays unsigned rather than becoming broken")
	})

	t.Run("an ordinary absent .sig does NOT claim a missing surface", func(t *testing.T) {
		inner := &stubSigSource{err: fmt.Errorf("no signature for acme/tools: %w", errs.ErrRemoteContentNotFound)}
		c := NewCachingBundleReader(inner)

		_, err := c.ReadBundleSignature(ctx, "acme/tools")
		require.Error(t, err)
		assert.ErrorIs(t, err, errs.ErrRemoteContentNotFound)
		assert.NotErrorIs(t, err, ErrNoSignatureSurface,
			"a source that HAS the surface and found nothing is reporting content, not a wiring fault")
	})

	t.Run("a real signature still comes back", func(t *testing.T) {
		c := NewCachingBundleReader(&stubSigSource{sig: []byte("-----BEGIN-----")})
		got, err := c.ReadBundleSignature(ctx, "acme/tools")
		require.NoError(t, err)
		assert.Equal(t, []byte("-----BEGIN-----"), got)
	})
}

// stubSigSource is a BundleByteSource that DOES implement the signature
// surface, so it exercises the other side of the capability check.
type stubSigSource struct {
	sig []byte
	err error
}

func (s *stubSigSource) ListBundleNames() []string { return []string{"acme/tools"} }
func (s *stubSigSource) HasBundle(string) bool     { return true }
func (s *stubSigSource) LockEntryFor(string) (LockEntry, bool) {
	return LockEntry{SHA: "deadbeef"}, true
}

func (s *stubSigSource) ReadBundleBytes(context.Context, string) ([]byte, error) {
	return []byte("bundle: yes\n"), nil
}

func (s *stubSigSource) ReadBundleSignature(context.Context, string) ([]byte, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.sig, nil
}
