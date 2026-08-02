package profiles

import (
	"bytes"
	"errors"
	"os"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/upgrade"
)

// TestLoad_RemoteSchemeRefsReportNoLockfileEntry pins the SEAM above the
// scheme test Load uses to tell a remote profile reference from a local
// profile name: every canonical URL scheme must produce the "no lockfile
// entry" diagnosis (a remote ref can never resolve from disk), and a bare
// local name must not. The scheme list itself is remote.IsCanonicalRef's;
// this pin is what keeps a second, drifting copy of it out of this package.
func TestLoad_RemoteSchemeRefsReportNoLockfileEntry(t *testing.T) {
	loader := NewLoader([]string{"/profiles"}, WithFS(afero.NewMemMapFs()))

	for _, ref := range []string{
		"https://github.com/owner/repo@bundles/b",
		"http://example.com/owner/repo@bundles/b",
		"git@github.com:owner/repo.git@bundles/b",
		"file:///tmp/repo@bundles/b",
	} {
		_, err := loader.Load(ref)
		require.Error(t, err, "ref %q", ref)
		require.ErrorIs(t, err, errs.ErrProfileNotFound, "ref %q", ref)
		assert.Contains(t, err.Error(), "no lockfile entry", "ref %q", ref)
	}

	// A bare local name must NOT take the remote arm: it is looked up on disk
	// and reports a plain not-found.
	_, err := loader.Load("go-developer")
	require.ErrorIs(t, err, errs.ErrProfileNotFound)
	assert.NotContains(t, err.Error(), "no lockfile entry")
}

// TestExists_ReportsPresenceNotLoadability pins that Exists answers "is there a
// profile under this name", not "does it currently parse". Answering the second
// question is destructive: CreateProfile treats Exists==false as "the name is
// free" and Save then writes over whatever is actually on disk, so a profile
// with a YAML syntax error was silently replaced by a brand-new one and the
// user's file was gone.
func TestExists_ReportsPresenceNotLoadability(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/profiles/broken.yaml", []byte("bundles: [unclosed\n"), 0o644))
	require.NoError(t, afero.WriteFile(fs, "/profiles/good.yaml", []byte("bundles:\n  - go-development\n"), 0o644))
	loader := NewLoader([]string{"/profiles"}, WithFS(fs))

	// The broken profile genuinely does not load...
	_, err := loader.Load("broken")
	require.Error(t, err)
	require.NotErrorIs(t, err, errs.ErrProfileNotFound, "a malformed profile is present, not absent")

	// ...but it is unmistakably there.
	assert.True(t, loader.Exists("broken"), "a present-but-malformed profile must report as existing")
	assert.True(t, loader.Exists("good"))

	// Names with no file behind them stay false, including the shapes Load
	// refuses outright.
	assert.False(t, loader.Exists("absent"))
	assert.False(t, loader.Exists("../escape"))
	assert.False(t, loader.Exists("some/bundle#profiles/p"))
	assert.False(t, loader.Exists("https://github.com/owner/repo@bundles/b"))
}

// TestExists_SeededProfile pins that a seeded (bundle-shipped) profile exists
// through the same accessor, since it has no file behind it at all.
func TestExists_SeededProfile(t *testing.T) {
	loader, p, _ := seedTestProfile(t)
	assert.True(t, loader.Exists(p.Name))
}

// faultyFs injects per-path Stat and Open failures into an afero filesystem, so
// a test can exercise the I/O error arms List takes on a directory it cannot
// interrogate. MemMapFs alone can only ever report "not found".
type faultyFs struct {
	afero.Fs
	statErr map[string]error
	openErr map[string]error
}

func (f *faultyFs) Stat(name string) (os.FileInfo, error) {
	if err, ok := f.statErr[name]; ok {
		return nil, err
	}
	return f.Fs.Stat(name)
}

func (f *faultyFs) Open(name string) (afero.File, error) {
	if err, ok := f.openErr[name]; ok {
		return nil, err
	}
	return f.Fs.Open(name)
}

// TestList_WarnsWhenAProfileDirectoryCannotBeRead pins that a profile directory
// List cannot interrogate is REPORTED, not silently dropped. An unreadable dir
// used to `continue` with the error discarded, so `ctxloom profile list`
// printed an empty, error-free list for a machine whose profiles were all
// present but unreachable — the shape of failure this project keeps producing.
func TestList_WarnsWhenAProfileDirectoryCannotBeRead(t *testing.T) {
	base := afero.NewMemMapFs()
	require.NoError(t, base.MkdirAll("/profiles", 0o755))
	fs := &faultyFs{Fs: base, statErr: map[string]error{"/profiles": errors.New("permission denied")}}

	var warnings bytes.Buffer
	restore := clidiag.SetSink(&warnings)
	defer restore()

	loader := NewLoader([]string{"/profiles"}, WithFS(fs))
	list, err := loader.List()
	require.NoError(t, err, "List still degrades rather than failing the whole command")
	assert.Empty(t, list)
	assert.Contains(t, warnings.String(), "/profiles",
		"the unreadable profiles directory must be named on stderr")
}

// TestList_WarnsWhenASubdirectoryCannotBeWalked is the same invariant one level
// down: a subdirectory whose entries cannot be read is skipped, and saying so is
// the difference between "you have no profiles" and "I could not look".
func TestList_WarnsWhenASubdirectoryCannotBeWalked(t *testing.T) {
	base := afero.NewMemMapFs()
	require.NoError(t, base.MkdirAll("/profiles/team", 0o755))
	require.NoError(t, afero.WriteFile(base, "/profiles/solo.yaml", []byte("bundles:\n  - go\n"), 0o644))
	require.NoError(t, afero.WriteFile(base, "/profiles/team/shared.yaml", []byte("bundles:\n  - go\n"), 0o644))
	fs := &faultyFs{Fs: base, openErr: map[string]error{"/profiles/team": errors.New("permission denied")}}

	var warnings bytes.Buffer
	restore := clidiag.SetSink(&warnings)
	defer restore()

	loader := NewLoader([]string{"/profiles"}, WithFS(fs))
	list, err := loader.List()
	require.NoError(t, err)

	names := make([]string, 0, len(list))
	for _, p := range list {
		names = append(names, p.Name)
	}
	assert.Equal(t, []string{"solo"}, names, "the readable profile is still listed")
	assert.Contains(t, warnings.String(), "/profiles/team",
		"the unwalkable subdirectory must be named on stderr")
}

// TestList_NamesAreDirRelativeAndNeverEmpty pins the invariant behind the
// discarded filepath.Rel error in List: every listed profile carries the name
// derived from its path relative to the directory it was found in, and a name is
// never empty. An empty Name would sort first and address nothing.
func TestList_NamesAreDirRelativeAndNeverEmpty(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll("/profiles/team", 0o755))
	require.NoError(t, afero.WriteFile(fs, "/profiles/solo.yaml", []byte("bundles:\n  - go\n"), 0o644))
	require.NoError(t, afero.WriteFile(fs, "/profiles/team/shared.yml", []byte("bundles:\n  - go\n"), 0o644))

	loader := NewLoader([]string{"/profiles"}, WithFS(fs))
	list, err := loader.List()
	require.NoError(t, err)

	names := make([]string, 0, len(list))
	for _, p := range list {
		require.NotEmpty(t, p.Name, "profile at %s got an empty name", p.Path)
		names = append(names, p.Name)
	}
	assert.Equal(t, []string{"solo", "team/shared"}, names)
}

// TestCommitUpgrade_RefusesNothingToWrite asserts the PAYLOAD: a commit that
// carries no bytes must not report success, and must not touch the file. The
// signature invites both mistakes -- CommitUpgrade(nil) returned nil for a write
// that never happened, and a zero-length Data was written verbatim, truncating
// the user's profile to nothing while reporting success.
func TestCommitUpgrade_RefusesNothingToWrite(t *testing.T) {
	const authored = "bundles:\n  - go-development\n"

	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/profiles/p.yaml", []byte(authored), 0o644))
	loader := NewLoader([]string{"/profiles"}, WithFS(fs))

	require.Error(t, loader.CommitUpgrade(nil), "a nil pending upgrade is not a successful write")

	require.Error(t, loader.CommitUpgrade(&upgrade.Pending{Path: "/profiles/p.yaml"}),
		"an empty upgrade payload is not a successful write")

	after, err := afero.ReadFile(fs, "/profiles/p.yaml")
	require.NoError(t, err)
	assert.Equal(t, authored, string(after), "the profile must be byte-identical after a refused commit")
}
