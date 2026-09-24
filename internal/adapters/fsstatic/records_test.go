package fsstatic

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

func records(t *testing.T) (*Records, afero.Fs, string) {
	t.Helper()
	fs := afero.NewOsFs()
	rec, err := NewRecords(fs, filepath.Join(t.TempDir(), "records"))
	require.NoError(t, err)
	return rec, fs, t.TempDir()
}

// addKey is a build contributing one top-level key to a JSON object.
func addKey(key, value string) delivery.Build {
	return func(current []byte) ([]byte, []string, error) {
		doc := map[string]any{}
		if len(current) > 0 {
			if err := json.Unmarshal(current, &doc); err != nil {
				return nil, nil, err
			}
		}
		doc[key] = value
		out, err := json.MarshalIndent(doc, "", "  ")
		return append(out, '\n'), []string{key}, err
	}
}

var empty delivery.Build = func([]byte) ([]byte, []string, error) { return nil, nil, nil }

func keysOf(t *testing.T, fs afero.Fs, path string) []string {
	t.Helper()
	data, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	doc := map[string]any{}
	require.NoError(t, json.Unmarshal(data, &doc))
	var keys []string
	for k := range doc {
		keys = append(keys, k)
	}
	return keys
}

// TestRecords_TwoWritersOneStructuredFile_EachRemovesOnlyItsOwnEntries: two
// writers add their own keys to the user's JSON file; each reconcile-to-empty
// takes out only that writer's key, and the user's key survives both.
func TestRecords_TwoWritersOneStructuredFile_EachRemovesOnlyItsOwnEntries(t *testing.T) {
	rec, fs, dir := records(t)
	target := filepath.Join(dir, "settings.json")
	testsupport.WriteFileString(t, fs, target, "{\n  \"theirs\": \"kept\"\n}\n", 0o644)
	ctx := context.Background()
	session, project := delivery.SessionWriter("h"), delivery.ProjectWriter

	_, err := rec.Apply(ctx, fs, target, session, addKey("session", "s"))
	require.NoError(t, err)
	_, err = rec.Apply(ctx, fs, target, project, addKey("project", "p"))
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"theirs", "session", "project"}, keysOf(t, fs, target))
	owned, err := rec.Owned(target, session)
	require.NoError(t, err)
	require.Equal(t, []string{"session"}, owned)
	targets, err := rec.Targets(project)
	require.NoError(t, err)
	require.Equal(t, []string{target}, targets)

	_, err = rec.Apply(ctx, fs, target, project, empty)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"theirs", "session"}, keysOf(t, fs, target), "the project writer's key is gone, the session's and the user's stay")
	targets, err = rec.Targets(project)
	require.NoError(t, err)
	require.Empty(t, targets)

	_, err = rec.Apply(ctx, fs, target, session, empty)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"theirs"}, keysOf(t, fs, target), "the user's file is theirs again")
	require.FileExists(t, target)
}

// TestRecords_AReapplyReversesThePreviousContributionFirst: a writer's
// second delivery does not stack on its first — the old key is taken out
// before the new one is added.
func TestRecords_AReapplyReversesThePreviousContributionFirst(t *testing.T) {
	rec, fs, dir := records(t)
	target := filepath.Join(dir, "settings.json")
	ctx := context.Background()
	_, err := rec.Apply(ctx, fs, target, delivery.ProjectWriter, addKey("old", "1"))
	require.NoError(t, err)
	_, err = rec.Apply(ctx, fs, target, delivery.ProjectWriter, addKey("new", "2"))
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"new"}, keysOf(t, fs, target))
}

// TestRecords_ACreatedFileLeavesWithItsLastWriter: a file ctxloom created
// is removed, with its record, when no writer owns anything in it.
func TestRecords_ACreatedFileLeavesWithItsLastWriter(t *testing.T) {
	rec, fs, dir := records(t)
	target := filepath.Join(dir, "settings.json")
	ctx := context.Background()
	_, err := rec.Apply(ctx, fs, target, delivery.SessionWriter("a"), addKey("a", "1"))
	require.NoError(t, err)
	_, err = rec.Apply(ctx, fs, target, delivery.SessionWriter("b"), addKey("b", "2"))
	require.NoError(t, err)
	_, err = rec.Apply(ctx, fs, target, delivery.SessionWriter("a"), empty)
	require.NoError(t, err)
	require.FileExists(t, target, "b still owns an entry")
	_, err = rec.Apply(ctx, fs, target, delivery.SessionWriter("b"), empty)
	require.NoError(t, err)
	require.NoFileExists(t, target)
	require.NoFileExists(t, rec.path(target))
}

// TestRecords_AnOpaqueFileIsOwnedWhole: a file hew cannot read (markdown)
// is the writer's whole contribution; reconcile-to-empty restores what
// stood there before, or removes a file that did not exist.
func TestRecords_AnOpaqueFileIsOwnedWhole(t *testing.T) {
	rec, fs, dir := records(t)
	ctx := context.Background()
	whole := func(text string) delivery.Build {
		return func([]byte) ([]byte, []string, error) { return []byte(text), []string{"CONTEXT.md"}, nil }
	}

	created := filepath.Join(dir, "CONTEXT.md")
	_, err := rec.Apply(ctx, fs, created, delivery.ProjectWriter, whole("managed"))
	require.NoError(t, err)
	_, err = rec.Apply(ctx, fs, created, delivery.ProjectWriter, empty)
	require.NoError(t, err)
	require.NoFileExists(t, created)

	theirs := filepath.Join(dir, "THEIRS.md")
	testsupport.WriteFileString(t, fs, theirs, "hand-written", 0o644)
	_, err = rec.Apply(ctx, fs, theirs, delivery.ProjectWriter, whole("managed"))
	require.NoError(t, err)
	body, err := afero.ReadFile(fs, theirs)
	require.NoError(t, err)
	require.Equal(t, "managed", string(body))
	_, err = rec.Apply(ctx, fs, theirs, delivery.ProjectWriter, empty)
	require.NoError(t, err)
	body, err = afero.ReadFile(fs, theirs)
	require.NoError(t, err)
	require.Equal(t, "hand-written", string(body))
}

// TestRecords_DriftIsRefused_NotClobbered: a structured file edited in the
// region a writer manages refuses the writer's next apply.
func TestRecords_DriftIsRefused_NotClobbered(t *testing.T) {
	rec, fs, dir := records(t)
	target := filepath.Join(dir, "settings.json")
	ctx := context.Background()
	_, err := rec.Apply(ctx, fs, target, delivery.ProjectWriter, addKey("mine", "1"))
	require.NoError(t, err)
	testsupport.WriteFileString(t, fs, target, "{\n  \"mine\": \"edited by hand\"\n}\n", 0o644)
	_, err = rec.Apply(ctx, fs, target, delivery.ProjectWriter, addKey("mine", "2"))
	require.Error(t, err)
	body, err := afero.ReadFile(fs, target)
	require.NoError(t, err)
	require.Contains(t, string(body), "edited by hand")
}

// TestRecords_ACreatedYAMLFileIsOwnedWhole: a format with no empty document
// to diff from (YAML) leaves a created file owned whole — a reapply
// rewrites it from scratch and it leaves with its writer.
func TestRecords_ACreatedYAMLFileIsOwnedWhole(t *testing.T) {
	rec, fs, dir := records(t)
	target := filepath.Join(dir, "state.yaml")
	ctx := context.Background()
	whole := func(text string) delivery.Build {
		return func([]byte) ([]byte, []string, error) { return []byte(text), []string{"state.yaml"}, nil }
	}
	_, err := rec.Apply(ctx, fs, target, delivery.ProjectWriter, whole("a: 1\n"))
	require.NoError(t, err)
	_, err = rec.Apply(ctx, fs, target, delivery.ProjectWriter, whole("b: 2\n"))
	require.NoError(t, err)
	body, err := afero.ReadFile(fs, target)
	require.NoError(t, err)
	require.Equal(t, "b: 2\n", string(body))
	_, err = rec.Apply(ctx, fs, target, delivery.ProjectWriter, empty)
	require.NoError(t, err)
	require.NoFileExists(t, target)
}

// TestNewRecords_TightensAnExistingRecordDir: the records directory also holds
// undo records that keep the previous value of the key they undo, so it is
// owner-only. Opening the store tightens a looser existing directory on the
// REAL filesystem, before any delivery runs: an approach writing its record
// through fsstatic's copy-on-write overlay cannot chmod a directory that lives
// in the overlay's base. Opening creates nothing — `manage check` opens it too.
func TestNewRecords_TightensAnExistingRecordDir(t *testing.T) {
	fs := afero.NewOsFs()
	dir := filepath.Join(t.TempDir(), "records")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.Chmod(dir, 0o755))

	_, err := NewRecords(fs, dir)
	require.NoError(t, err)

	info, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm(), "an existing records directory must be tightened to owner-only")
}

func TestNewRecords_CreatesNoDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "records")
	_, err := NewRecords(afero.NewOsFs(), dir)
	require.NoError(t, err)
	_, err = os.Stat(dir)
	require.True(t, os.IsNotExist(err), "opening the store must not create its directory")
}

// TestWriteThrough_CreatesAMissingDirectoryOwnerOnly: what an approach writes
// outside the target is its own state (claude's undo record), so a directory
// writeThrough has to create for it is owner-only.
func TestWriteThrough_CreatesAMissingDirectoryOwnerOnly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "records")
	path := filepath.Join(dir, "x.hew-record.yaml")

	require.NoError(t, writeThrough(afero.NewOsFs(), path, []byte("x"), 0o600))

	info, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}
