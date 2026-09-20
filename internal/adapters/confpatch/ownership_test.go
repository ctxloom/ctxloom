package confpatch

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/delivery"
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
	require.NoError(t, afero.WriteFile(fs, target, []byte("{\n  \"theirs\": \"kept\"\n}\n"), 0o644))
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
	require.NoError(t, afero.WriteFile(fs, theirs, []byte("hand-written"), 0o644))
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
	require.NoError(t, afero.WriteFile(fs, target, []byte("{\n  \"mine\": \"edited by hand\"\n}\n"), 0o644))
	_, err = rec.Apply(ctx, fs, target, delivery.ProjectWriter, addKey("mine", "2"))
	require.Error(t, err)
	body, err := afero.ReadFile(fs, target)
	require.NoError(t, err)
	require.Contains(t, string(body), "edited by hand")
}
