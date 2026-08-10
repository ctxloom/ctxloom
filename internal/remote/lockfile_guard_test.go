package remote

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The anti-erasure guard on Save. The lockfile is the sole on-disk record of
// every dependency pin, every user hold (Pinned) and every publisher
// retraction (Retracted); a caller that arrives at Save with an empty set has,
// by construction, nothing to say about the entries already recorded there.
// Writing that empty set is indistinguishable from "erase everything" and is
// exactly how `ctxloom deps upgrade` wiped a whole lockfile while reporting
// "Everything is up to date."
//
// These tests use the REAL OS filesystem (t.TempDir) deliberately: the guard
// reads back what is on disk before writing, and MemMapFs diverges from OsFs on
// exactly that kind of read/stat behaviour.

// populatedLockfile is a two-entry lock carrying both security-relevant flags:
// a user hold and a publisher retraction.
func populatedLockfile() *Lockfile {
	return &Lockfile{
		Version: 1,
		Bundles: map[string]LockEntry{
			"https://github.com/alice/repo@bundles/held": {
				SHA:    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				URL:    "https://github.com/alice/repo",
				Held: true,
			},
			"https://github.com/bob/repo@bundles/withdrawn": {
				SHA:             "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
				URL:             "https://github.com/bob/repo",
				Retracted:       true,
				RetractedReason: "published by mistake",
			},
		},
	}
}

// newGuardManager returns a manager over a fresh real-filesystem base dir.
func newGuardManager(t *testing.T) *LockfileManager {
	t.Helper()
	return NewLockfileManager(filepath.Join(t.TempDir(), ".ctxloom"))
}

// The headline: an empty lockfile must never replace a populated one.
func TestSave_RefusesEmptyOverPopulated(t *testing.T) {
	m := newGuardManager(t)
	require.NoError(t, m.Save(populatedLockfile()))

	before, err := os.ReadFile(m.Path())
	require.NoError(t, err)

	err = m.Save(&Lockfile{Version: 1, Bundles: map[string]LockEntry{}})
	require.Error(t, err, "an empty lockfile must not overwrite a populated one")
	assert.ErrorIs(t, err, ErrLockfileWouldErase)
	assert.Contains(t, err.Error(), "2", "the refusal names how many entries it protected")

	after, err := os.ReadFile(m.Path())
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "the on-disk lockfile is byte-identical after the refusal")
}

// The security-relevant payload specifically: holds and retractions survive.
// A wipe silently un-retracts content the publisher withdrew.
func TestSave_RefusedEmptyWritePreservesPinnedAndRetracted(t *testing.T) {
	m := newGuardManager(t)
	require.NoError(t, m.Save(populatedLockfile()))

	require.Error(t, m.Save(&Lockfile{Version: 1, Bundles: map[string]LockEntry{}}))

	reloaded, err := m.Load()
	require.NoError(t, err)

	held, ok := reloaded.GetEntry(ItemTypeBundle, "https://github.com/alice/repo@bundles/held")
	require.True(t, ok, "the held entry survives")
	assert.True(t, held.Held, "the user's hold survives the refused write")

	withdrawn, ok := reloaded.GetEntry(ItemTypeBundle, "https://github.com/bob/repo@bundles/withdrawn")
	require.True(t, ok, "the retracted entry survives")
	assert.True(t, withdrawn.Retracted, "the publisher's retraction survives the refused write")
	assert.Equal(t, "published by mistake", withdrawn.RetractedReason)
}

// A nil Bundles map is empty too — the guard must not be dodged by shape.
func TestSave_RefusesNilBundlesOverPopulated(t *testing.T) {
	m := newGuardManager(t)
	require.NoError(t, m.Save(populatedLockfile()))

	err := m.Save(&Lockfile{Version: 1})
	assert.ErrorIs(t, err, ErrLockfileWouldErase)
}

// The legitimate empty case: a genuinely empty project must still be able to
// write an empty lockfile. Replacing a data-loss bug with a usability one is
// not a fix.
func TestSave_AllowsEmptyWhenNothingIsRecorded(t *testing.T) {
	// No lockfile on disk at all.
	m := newGuardManager(t)
	require.NoError(t, m.Save(&Lockfile{Version: 1, Bundles: map[string]LockEntry{}}),
		"an empty lockfile over no lockfile is a legitimate write")
	loaded, err := m.Load()
	require.NoError(t, err)
	assert.True(t, loaded.IsEmpty())

	// An already-empty lockfile on disk.
	require.NoError(t, m.Save(&Lockfile{Version: 1, Bundles: map[string]LockEntry{}}),
		"an empty lockfile over an empty lockfile is a legitimate write")

	// A zero-byte file is blank, not corrupt.
	blank := newGuardManager(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(blank.Path()), 0o755))
	require.NoError(t, os.WriteFile(blank.Path(), nil, 0o644))
	require.NoError(t, blank.Save(&Lockfile{Version: 1, Bundles: map[string]LockEntry{}}),
		"a zero-byte lockfile records nothing, so an empty write erases nothing")
}

// The ordinary write path is untouched: populated over populated, populated
// over empty, and first-ever write all still work.
func TestSave_AllowsPopulatedWrites(t *testing.T) {
	m := newGuardManager(t)
	require.NoError(t, m.Save(&Lockfile{Version: 1, Bundles: map[string]LockEntry{}}))
	require.NoError(t, m.Save(populatedLockfile()), "populated over empty")

	grown := populatedLockfile()
	grown.AddEntry(ItemTypeBundle, "https://github.com/carol/repo@bundles/new", LockEntry{SHA: "cccccccc"})
	require.NoError(t, m.Save(grown), "populated over populated")

	loaded, err := m.Load()
	require.NoError(t, err)
	assert.Equal(t, 3, loaded.Count())
}

// A corrupt lock.yaml must not be silently replaced. Its holds and
// retractions cannot be read, so nothing can carry them forward — any write
// over it destroys state nobody can account for.
func TestSave_RefusesOverwritingCorruptLockfile(t *testing.T) {
	m := newGuardManager(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(m.Path()), 0o755))
	corrupt := []byte("bundles:\n  - this is not: a mapping\n   broken: [unclosed\n")
	require.NoError(t, os.WriteFile(m.Path(), corrupt, 0o644))

	err := m.Save(populatedLockfile())
	require.Error(t, err, "an unparseable lockfile must not be silently overwritten")
	assert.ErrorIs(t, err, ErrLockfileUnreadable)
	assert.Contains(t, err.Error(), m.Path(), "the refusal names the file to fix")

	after, err := os.ReadFile(m.Path())
	require.NoError(t, err)
	assert.Equal(t, string(corrupt), string(after), "the corrupt file is left exactly as found")
}

// Even an explicit erasure never overwrites a corrupt lockfile: AllowEmpty is
// a statement about the CALLER's intent to empty, not a licence to destroy
// state that could not be read.
func TestSave_AllowEmptyStillRefusesCorruptLockfile(t *testing.T) {
	m := newGuardManager(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(m.Path()), 0o755))
	require.NoError(t, os.WriteFile(m.Path(), []byte("\tnot: [yaml\n"), 0o644))

	err := m.Save(&Lockfile{Version: 1, Bundles: map[string]LockEntry{}}, AllowEmpty())
	assert.ErrorIs(t, err, ErrLockfileUnreadable)
}

// The escape hatch: a caller that emptied the lock DELIBERATELY, entry by
// entry (deps check --cleanup pruning the last item), says so and writes.
func TestSave_AllowEmptyPermitsDeliberateErasure(t *testing.T) {
	m := newGuardManager(t)
	require.NoError(t, m.Save(populatedLockfile()))

	require.NoError(t, m.Save(&Lockfile{Version: 1, Bundles: map[string]LockEntry{}}, AllowEmpty()),
		"an explicitly-intended erasure is permitted")

	loaded, err := m.Load()
	require.NoError(t, err)
	assert.True(t, loaded.IsEmpty(), "the deliberate erasure landed")
}

// Errors sentinels must be distinguishable so callers can report the right fix.
func TestLockfileGuardErrorsAreDistinct(t *testing.T) {
	assert.False(t, errors.Is(ErrLockfileWouldErase, ErrLockfileUnreadable))
	assert.False(t, errors.Is(ErrLockfileUnreadable, ErrLockfileWouldErase))
}
