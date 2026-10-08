package delivery_test

import (
	"errors"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// stageClaim commits writer's appended-section claim on path in rec, writing
// the file on fs.
func stageClaim(t *testing.T, fs afero.Fs, rec delivery.Ownership, path string, w delivery.Writer) {
	t.Helper()
	b := safefs.NewBatch(fs, func(_ string, fn func() error) error { return fn() })
	require.NoError(t, rec.In(b).Stage(path, w, []present.Claim{{Pointer: present.AppendedSection, Value: []byte("ctx-" + string(w))}}))
	_, err := b.Commit()
	require.NoError(t, err)
}

// TestProjectClaims_NamesOnlyLivePlacesAProjectWriterHolds: a settings
// status sees the places a project writer (legacy tag or per-kind family)
// claims AND the file still holds; a session's claim, or a project claim the
// file no longer holds, is not one.
func TestProjectClaims_NamesOnlyLivePlacesAProjectWriterHolds(t *testing.T) {
	fs := afero.NewMemMapFs()
	rec := newRecord(t, fs)
	claims := delivery.ProjectClaims(fs, rec)

	stageClaim(t, fs, rec, "/p/session.md", delivery.SessionWriter("brisk-otter"))
	got, err := claims("/p/session.md")
	require.NoError(t, err)
	require.Empty(t, got, "a live claim no project writer holds is not a project claim")

	stageClaim(t, fs, rec, "/p/AGENTS.md", delivery.ProjectWriterFor("mock").Of(present.Context))
	got, err = claims("/p/AGENTS.md")
	require.NoError(t, err)
	require.Equal(t, []string{present.AppendedSection}, got)

	testsupport.WriteFileString(t, fs, "/p/AGENTS.md", "the user rewrote this\n", 0o644)
	got, err = claims("/p/AGENTS.md")
	require.NoError(t, err)
	require.Empty(t, got, "a project claim the file no longer holds is not live")
}

// failingOwnership is a record whose every read fails.
type failingOwnership struct {
	delivery.Ownership
	writers []delivery.Writer
}

var errRecord = errors.New("record unreadable")

func (failingOwnership) Paths(afero.Fs, string) ([]delivery.PathState, error) { return nil, errRecord }
func (f failingOwnership) Writers() ([]delivery.Writer, error) {
	if f.writers == nil {
		return nil, errRecord
	}
	return f.writers, nil
}
func (failingOwnership) Targets(delivery.Writer) ([]string, error) { return nil, errRecord }

// TestProjectClaims_AnUnreadableRecordIsAnError: a record that cannot be
// read is reported, never read as "nothing claimed".
func TestProjectClaims_AnUnreadableRecordIsAnError(t *testing.T) {
	_, err := delivery.ProjectClaims(afero.NewMemMapFs(), failingOwnership{})("/p/x")
	require.ErrorIs(t, err, errRecord)

	_, err = delivery.ProjectContextClaims(failingOwnership{}, "/p")
	require.ErrorIs(t, err, errRecord, "the writer list unreadable")
	_, err = delivery.ProjectContextClaims(failingOwnership{writers: []delivery.Writer{delivery.ProjectWriterFor("mock").Of(present.Context)}}, "/p")
	require.ErrorIs(t, err, errRecord, "a context writer's targets unreadable")
}

// TestProjectContextClaims_CountsOnlyAtRestContextUnderTheProject (R5):
// the files under the project an at-rest context writer claims, sorted and
// once each; not the legacy bare project tag (it names no kind), not another
// kind's project writer, not a session, not a file outside the project.
func TestProjectContextClaims_CountsOnlyAtRestContextUnderTheProject(t *testing.T) {
	fs := afero.NewMemMapFs()
	rec := newRecord(t, fs)
	mockCtx, claudeCtx := delivery.ProjectWriterFor("mock").Of(present.Context), delivery.ProjectWriterFor("claude-code").Of(present.Context)

	// The bare tag sorts before every family writer: skipping it must not
	// end the walk.
	stageClaim(t, fs, rec, "/p/LEGACY.md", delivery.ProjectWriter)
	stageClaim(t, fs, rec, "/p/settings.json", delivery.ProjectWriterFor("mock").Of(present.Settings))
	stageClaim(t, fs, rec, "/p/SESSION.md", delivery.SessionWriter("brisk-otter"))
	stageClaim(t, fs, rec, "/elsewhere/AGENTS.md", mockCtx)
	stageClaim(t, fs, rec, "/p/docs/AGENTS.md", mockCtx)
	stageClaim(t, fs, rec, "/p/docs/AGENTS.md", claudeCtx)
	stageClaim(t, fs, rec, "/p/CLAUDE.md", claudeCtx)

	got, err := delivery.ProjectContextClaims(rec, "/p")
	require.NoError(t, err)
	require.Equal(t, []string{"/p/CLAUDE.md", "/p/docs/AGENTS.md"}, got)
}
