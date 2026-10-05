package memory

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/shared/schemaver"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// generationZeroEssence is an essence as written before its front-matter was
// versioned: no schema_version, and the timestamp under distilled_at.
const generationZeroEssence = "---\n" +
	"session_id: old-one\n" +
	"distilled_at: 2024-01-15T10:00:00Z\n" +
	"entry_count: 8\n" +
	"---\n\n" +
	"# Session summary\n\nOld body.\n"

var generationZeroCompactedAt = time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)

// frontMatterOf decodes an essence file's front-matter into a plain map, so a
// test reads the keys actually on disk rather than what a struct maps them to.
func frontMatterOf(t *testing.T, data []byte) map[string]any {
	t.Helper()
	text := string(data)
	require.True(t, strings.HasPrefix(text, "---\n"), "an essence opens with front-matter")
	rest := text[len("---\n"):]
	end := strings.Index(rest, "\n---\n")
	require.GreaterOrEqual(t, end, 0, "the front-matter is terminated")
	var fm map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(rest[:end+1]), &fm))
	return fm
}

func TestLoadCompactedSession_GenerationZeroKeepsItsTimestamp(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "old-one.md"), []byte(generationZeroEssence), 0o644))

	loaded, err := LoadCompactedSession(afero.NewOsFs(), dir, "old-one")
	require.NoError(t, err)

	assert.True(t, loaded.CompactedAt.Equal(generationZeroCompactedAt),
		"distilled_at must migrate to compacted_at with its value intact, got %v", loaded.CompactedAt)
	assert.Equal(t, 8, loaded.SourceEntries)
	assert.Contains(t, loaded.Body, "Old body.")
}

func TestLoadCompactedSession_RefusesANewerGeneration(t *testing.T) {
	dir := t.TempDir()
	newer := "---\n" + schemaver.Key + ": " + strconv.Itoa(essenceKind.Current()+1) + "\n" +
		"session_id: future\ncompacted_at: 2030-01-01T00:00:00Z\n---\n\nbody\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "future.md"), []byte(newer), 0o644))

	_, err := LoadCompactedSession(afero.NewOsFs(), dir, "future")

	require.Error(t, err)
	assert.True(t, errors.Is(err, schemaver.ErrNewer), "a newer essence must be refused as newer, got %v", err)
}

func TestSaveCompacted_WritesTheCurrentGenerationAndCompactedAt(t *testing.T) {
	testsupport.Isolate(t)
	dir := t.TempDir()
	recordOutputDir(t, "fresh-harp")

	c := &Compactor{fs: afero.NewOsFs(), config: CompactionConfig{OutputDir: dir}}
	_, err := c.saveCompacted("fresh", "body", compactedMeta{HarpName: "fresh-harp"})
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(dir, "fresh.md"))
	require.NoError(t, err)
	fm := frontMatterOf(t, data)
	assert.Equal(t, essenceKind.Current(), fm[schemaver.Key], "the writer stamps the current generation")
	assert.Contains(t, fm, compactedAtKey)
	assert.NotContains(t, fm, distilledAtKey)
}

// --write-upgrades persists the migration a load made in memory; without it
// the file on disk is never changed.
func TestLoadCompactedSession_WriteUpgradesPersistsTheMigration(t *testing.T) {
	t.Cleanup(func() { schemaver.BindWriteUpgrades(pflag.NewFlagSet("reset", pflag.ContinueOnError)) })

	for _, persist := range []bool{false, true} {
		dir := t.TempDir()
		path := filepath.Join(dir, "old-one.md")
		require.NoError(t, os.WriteFile(path, []byte(generationZeroEssence), 0o644))
		flags := pflag.NewFlagSet("t", pflag.ContinueOnError)
		schemaver.BindWriteUpgrades(flags)
		require.NoError(t, flags.Set(schemaver.WriteUpgradesFlag, strconv.FormatBool(persist)))

		_, err := LoadCompactedSession(afero.NewOsFs(), dir, "old-one")
		require.NoError(t, err)

		data, err := os.ReadFile(path)
		require.NoError(t, err)
		if !persist {
			assert.Equal(t, generationZeroEssence, string(data), "without --%s the file is never changed", schemaver.WriteUpgradesFlag)
			continue
		}
		fm := frontMatterOf(t, data)
		assert.Equal(t, essenceKind.Current(), fm[schemaver.Key])
		assert.Contains(t, fm, compactedAtKey)
		assert.NotContains(t, fm, distilledAtKey)
		assert.True(t, strings.HasSuffix(string(data), "# Session summary\n\nOld body.\n"), "the body survives the write-back verbatim")
	}
}
