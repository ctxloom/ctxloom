package operations

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/companions"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// allowWorld is a HOME under a temp root (so the allow store is this test's)
// and one fake companion binary.
func allowWorld(t *testing.T, body string) (bin string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	bin = filepath.Join(t.TempDir(), "ltk")
	require.NoError(t, os.WriteFile(bin, []byte(body), 0o755)) //nolint:gosec // a fake companion must be executable
	return bin
}

func allowStoreRecords(t *testing.T) []companions.CompanionKey {
	t.Helper()
	store, err := companions.NewAllowStore(safefs.New())
	require.NoError(t, err)
	recs, err := store.List()
	require.NoError(t, err)
	out := make([]companions.CompanionKey, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.Key)
	}
	return out
}

// TestAllowCompanion_PreviewWritesNothing: without Apply the result names the
// path and hash an allow would record, and the store file is never created.
func TestAllowCompanion_PreviewWritesNothing(t *testing.T) {
	bin := allowWorld(t, "#!/bin/sh\n")

	res, err := AllowCompanion(context.Background(), safefs.New(), CompanionAllowRequest{PathOrName: bin})
	require.NoError(t, err)
	assert.False(t, res.Applied)
	assert.Equal(t, bin, res.Key.Path)
	assert.Len(t, res.Key.SHA256, 64)
	assert.Nil(t, res.Previous)
	wire, err := json.Marshal(res)
	require.NoError(t, err)
	assert.JSONEq(t, `{"key":{"bin":"ltk","path":"`+bin+`","sha256":"`+res.Key.SHA256+`"},"applied":false}`, string(wire),
		"--format json carries the same lower-case keys as every other payload")

	storePath, err := paths.HomeCompanionAllowPath()
	require.NoError(t, err)
	assert.NoFileExists(t, storePath, "a preview must not write the store")
}

// TestAllowCompanion_ApplyRecordsPathAndHash: with Apply the record is
// written, and admission then admits exactly that binary.
func TestAllowCompanion_ApplyRecordsPathAndHash(t *testing.T) {
	bin := allowWorld(t, "#!/bin/sh\n")

	res, err := AllowCompanion(context.Background(), safefs.New(), CompanionAllowRequest{PathOrName: bin, Apply: true})
	require.NoError(t, err)
	assert.True(t, res.Applied)
	assert.Equal(t, []companions.CompanionKey{res.Key}, allowStoreRecords(t))
}

// TestAllowCompanion_RebuiltBinaryDisclosesThePreviousHash: re-allowing a path
// whose bytes changed names the hash on record, and applying it replaces the
// record rather than adding a second one for the same path.
func TestAllowCompanion_RebuiltBinaryDisclosesThePreviousHash(t *testing.T) {
	bin := allowWorld(t, "#!/bin/sh\necho one\n")
	first, err := AllowCompanion(context.Background(), safefs.New(), CompanionAllowRequest{PathOrName: bin, Apply: true})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\necho two\n"), 0o755)) //nolint:gosec // a fake companion

	preview, err := AllowCompanion(context.Background(), safefs.New(), CompanionAllowRequest{PathOrName: bin})
	require.NoError(t, err)
	require.NotNil(t, preview.Previous)
	assert.Equal(t, first.Key.SHA256, preview.Previous.SHA256)
	assert.NotEqual(t, first.Key.SHA256, preview.Key.SHA256)

	applied, err := AllowCompanion(context.Background(), safefs.New(), CompanionAllowRequest{PathOrName: bin, Apply: true})
	require.NoError(t, err)
	assert.Equal(t, []companions.CompanionKey{applied.Key}, allowStoreRecords(t), "one record per path")
}

// TestAllowCompanion_UnresolvableTargetFails: a path that is not there cannot
// be hashed, so there is nothing to allow.
func TestAllowCompanion_UnresolvableTargetFails(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, err := AllowCompanion(context.Background(), safefs.New(),
		CompanionAllowRequest{PathOrName: filepath.Join(t.TempDir(), "missing")})
	require.Error(t, err)
}

// TestForgetCompanion_PreviewThenApply: forget previews the records it would
// drop without dropping them; with apply they are gone.
func TestForgetCompanion_PreviewThenApply(t *testing.T) {
	bin := allowWorld(t, "#!/bin/sh\n")
	allowed, err := AllowCompanion(context.Background(), safefs.New(), CompanionAllowRequest{PathOrName: bin, Apply: true})
	require.NoError(t, err)

	preview, err := ForgetCompanion(context.Background(), safefs.New(), bin, false)
	require.NoError(t, err)
	assert.False(t, preview.Applied)
	assert.Equal(t, []companions.CompanionKey{allowed.Key}, preview.Records)
	assert.Len(t, allowStoreRecords(t), 1, "a preview must not forget anything")

	done, err := ForgetCompanion(context.Background(), safefs.New(), bin, true)
	require.NoError(t, err)
	assert.True(t, done.Applied)
	assert.Empty(t, allowStoreRecords(t))
}

// TestForgetCompanion_ByNameMatchesTheRecordedName: a bare name forgets the
// records allowed under that name, even once the binary itself is gone.
func TestForgetCompanion_ByNameMatchesTheRecordedName(t *testing.T) {
	bin := allowWorld(t, "#!/bin/sh\n")
	_, err := AllowCompanion(context.Background(), safefs.New(), CompanionAllowRequest{PathOrName: bin, Apply: true})
	require.NoError(t, err)
	require.NoError(t, os.Remove(bin))

	done, err := ForgetCompanion(context.Background(), safefs.New(), "ltk", true)
	require.NoError(t, err)
	assert.Len(t, done.Records, 1)
	assert.Empty(t, allowStoreRecords(t))
}

// TestForgetCompanion_NothingRecordedIsNotAllowed: forgetting what was never
// allowed is the caller's mistake to see, not a silent success.
func TestForgetCompanion_NothingRecordedIsNotAllowed(t *testing.T) {
	bin := allowWorld(t, "#!/bin/sh\n")
	_, err := ForgetCompanion(context.Background(), safefs.New(), bin, true)
	require.ErrorIs(t, err, ErrCompanionNotAllowed)
}
