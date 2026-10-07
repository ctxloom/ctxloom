package fsstatic_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/fsstatic"
	"github.com/ctxloom/ctxloom/internal/core/composite/compositetest"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// The declaration contract (present.Delivered): Files and Claims together are
// what the writer claims after the run, whether or not the approach wrote a
// byte this run. Each test drives the mock's context kind into a project root
// through a context approach whose delivery it scripts (contextBy).

// declaredFixture is a project root, the production record over a real
// filesystem, and the file the scripted approach delivers.
type declaredFixture struct {
	fs      afero.Fs
	rec     *fsstatic.Records
	project string
	path    string
}

func newDeclaredFixture(t *testing.T) declaredFixture {
	t.Helper()
	fs := afero.NewOsFs()
	rec, err := fsstatic.NewRecords(fs, filepath.Join(t.TempDir(), "records"))
	require.NoError(t, err)
	project := t.TempDir()
	return declaredFixture{fs: fs, rec: rec, project: project, path: filepath.Join(project, "owned.txt")}
}

// deliver runs one delivery whose context approach is deliver.
func (f declaredFixture) deliver(t *testing.T, deliver func(fs afero.Fs) (present.Delivered, error)) error {
	t.Helper()
	root := mock.New().Root()
	root.Context = contextBy{ContextApproach: root.Context, deliver: deliver}
	return deliverMock(t, f.fs, f.rec, root, f.project, delivery.ProjectWriter, false)
}

// writeAndDeclare writes each path whole and declares it.
func writeAndDeclare(paths ...string) func(fs afero.Fs) (present.Delivered, error) {
	return func(fs afero.Fs) (present.Delivered, error) {
		for _, p := range paths {
			if err := safefs.WriteFile(fs, p, []byte("owned "+filepath.Base(p)+"\n"), 0o644); err != nil {
				return present.Delivered{}, err
			}
		}
		return present.Delivered{Files: paths}, nil
	}
}

// declareOnly declares each path without writing it.
func declareOnly(paths ...string) func(fs afero.Fs) (present.Delivered, error) {
	return func(afero.Fs) (present.Delivered, error) { return present.Delivered{Files: paths}, nil }
}

func (f declaredFixture) claimed(t *testing.T) []string {
	t.Helper()
	targets, err := f.rec.Targets(delivery.ProjectWriter)
	require.NoError(t, err)
	return targets
}

// TestDeliver_RefusesAWrittenFileItDoesNotDeclare: a file written under a
// target root and absent from Files fails the delivery, naming the approach
// and the path, and lands nothing.
func TestDeliver_RefusesAWrittenFileItDoesNotDeclare(t *testing.T) {
	f := newDeclaredFixture(t)
	err := f.deliver(t, func(fs afero.Fs) (present.Delivered, error) {
		return present.Delivered{}, safefs.WriteFile(fs, f.path, []byte("undeclared\n"), 0o644)
	})
	require.ErrorIs(t, err, fsstatic.ErrUndeclaredWrite)
	require.ErrorContains(t, err, f.path)
	require.ErrorContains(t, err, mock.New().Root().Context.Name())
	require.NoFileExists(t, f.path)
	require.Empty(t, f.claimed(t))
}

// TestDeliver_KeepsADeclaredFileItDidNotWriteThisRun: a file the writer
// claimed whole earlier, declared again but not written, keeps its claim and
// stands as it stood — not rewritten (os.SameFile), and still live.
func TestDeliver_KeepsADeclaredFileItDidNotWriteThisRun(t *testing.T) {
	f := newDeclaredFixture(t)
	require.NoError(t, f.deliver(t, writeAndDeclare(f.path)))
	before, err := os.Stat(f.path)
	require.NoError(t, err)
	require.Equal(t, []string{f.path}, f.claimed(t))

	for run := 2; run <= 3; run++ {
		require.NoError(t, f.deliver(t, declareOnly(f.path)), "run %d", run)
		after, err := os.Stat(f.path)
		require.NoError(t, err, "run %d: the declared file was removed", run)
		require.True(t, os.SameFile(before, after), "run %d: a declared, unwritten file was rewritten", run)
		require.Equal(t, []string{f.path}, f.claimed(t), "run %d: the claim must be kept", run)
		states, err := f.rec.Paths(f.fs, f.path)
		require.NoError(t, err)
		require.Len(t, states, 1)
		require.Equal(t, "", states[0].Pointer, "run %d: a whole-file claim", run)
		require.True(t, states[0].Live, "run %d: the file holds the claimed bytes", run)
	}
}

// TestDeliver_RefusesADeclaredUnwrittenFileItNeverClaimed: declaring a file
// the writer holds no claim on, without writing it, fails — even with a file
// standing there, which is the user's, and is left alone.
func TestDeliver_RefusesADeclaredUnwrittenFileItNeverClaimed(t *testing.T) {
	f := newDeclaredFixture(t)
	require.NoError(t, os.WriteFile(f.path, []byte("theirs\n"), 0o644))
	err := f.deliver(t, declareOnly(f.path))
	require.ErrorIs(t, err, fsstatic.ErrDeclaredUnclaimed)
	require.ErrorContains(t, err, f.path)
	require.ErrorContains(t, err, mock.New().Root().Context.Name())
	got, rerr := os.ReadFile(f.path)
	require.NoError(t, rerr)
	require.Equal(t, "theirs\n", string(got))
	require.Empty(t, f.claimed(t))
}

// TestDeliver_RefusesADeclaredUnwrittenFileThatIsGone: the writer's earlier
// claim names a file no longer on disk; declaring it without writing fails,
// rather than keeping a claim on nothing.
func TestDeliver_RefusesADeclaredUnwrittenFileThatIsGone(t *testing.T) {
	f := newDeclaredFixture(t)
	require.NoError(t, f.deliver(t, writeAndDeclare(f.path)))
	require.NoError(t, os.Remove(f.path))
	err := f.deliver(t, declareOnly(f.path))
	require.ErrorIs(t, err, fsstatic.ErrDeclaredMissing)
	require.ErrorContains(t, err, f.path)
	require.ErrorContains(t, err, mock.New().Root().Context.Name())
}

// TestDeliver_ReleasesAFileItNoLongerDeclares: a file claimed earlier and
// absent from this run's declaration is released, and so removed.
func TestDeliver_ReleasesAFileItNoLongerDeclares(t *testing.T) {
	f := newDeclaredFixture(t)
	other := filepath.Join(f.project, "dropped.txt")
	require.NoError(t, f.deliver(t, writeAndDeclare(f.path, other)))
	require.FileExists(t, other)
	require.NoError(t, f.deliver(t, declareOnly(f.path)))
	require.NoFileExists(t, other)
	require.FileExists(t, f.path)
	require.Equal(t, []string{f.path}, f.claimed(t))
}

// TestDeliver_RefusesAPathBothDeclaredAndClaimed: a file is owned whole or
// claimed into, never both by one delivery.
func TestDeliver_RefusesAPathBothDeclaredAndClaimed(t *testing.T) {
	f := newDeclaredFixture(t)
	err := f.deliver(t, func(fs afero.Fs) (present.Delivered, error) {
		if err := safefs.WriteFile(fs, f.path, []byte("whole\n"), 0o644); err != nil {
			return present.Delivered{}, err
		}
		return present.Delivered{Files: []string{f.path},
			Claims: map[string][]present.Claim{f.path: {{Pointer: present.AppendedSection, Value: []byte("section")}}}}, nil
	})
	require.ErrorIs(t, err, fsstatic.ErrDeclaredAndClaimed)
	require.ErrorContains(t, err, f.path)
	require.NoFileExists(t, f.path)
}

// legacyMarker is the sidecar the managed-file writer used to keep beside
// each managed directory. Nothing writes it now; the record still claims it
// whole in every project delivered to before.
const legacyMarker = ".ctxloom-managed"

// TestDeliver_RemovesALegacyManagedMarkerTheRecordClaims: a project delivered
// to before carries the old sidecar marker in its commands directory, claimed
// whole by the writer because the old writer wrote it through the overlay.
// Nothing declares it any more, so the next delivery's release removes it,
// and the commands themselves stand.
func TestDeliver_RemovesALegacyManagedMarkerTheRecordClaims(t *testing.T) {
	fs := afero.NewOsFs()
	rec, err := fsstatic.NewRecords(fs, filepath.Join(t.TempDir(), "records"))
	require.NoError(t, err)
	project := t.TempDir()
	commands := filepath.Join(project, claude.ConfigDirName, claude.CommandsDirName)
	marker := filepath.Join(commands, legacyMarker)
	seed := safefs.NewBatch(fs, func(_ string, fn func() error) error { return fn() })
	require.NoError(t, rec.In(seed).Stage(marker, delivery.ProjectWriter, []present.Claim{{Value: []byte("go.md\tcommands\n")}}))
	_, err = seed.Commit()
	require.NoError(t, err)
	require.FileExists(t, marker, "precondition: the record's claim landed the marker")

	eng, err := claude.Build()
	require.NoError(t, err)
	pkg := compositetest.Fixture(t, compositetest.WithCommand("go", "go now"))
	items := pkg.EngineItems(eng.Root().Name)
	exports, err := eng.Exports(items)
	require.NoError(t, err)
	pref := delivery.Preference{Root: map[present.Kind]present.RootKind{present.Commands: present.RootProjectRoot}}
	plan, err := delivery.Route(items, eng.Root(), pref, present.Paths{ProjectRoot: present.Root{Host: project, Engine: project}})
	require.NoError(t, err)
	target := delivery.Target{Root: present.ProjectOnHost(project), Ownership: rec, Writer: delivery.ProjectWriter}
	_, err = fsstatic.New(safefs.NewMem(fs)).Deliver(context.Background(), delivery.Loadout{Plan: plan, Package: pkg, Exports: exports}, eng.Root(), target)
	require.NoError(t, err)

	require.NoFileExists(t, marker, "the legacy marker is released and removed")
	require.FileExists(t, filepath.Join(commands, "go.md"))
	claimed, err := rec.Targets(delivery.ProjectWriter)
	require.NoError(t, err)
	require.Equal(t, []string{filepath.Join(commands, "go.md")}, claimed)
}
