package memory

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/shared/schemaver"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// unversionedEssence is an essence written before its front-matter was
// versioned: no schema_version, and the timestamp under distilled_at.
const unversionedEssence = "---\n" +
	"session_id: old-one\n" +
	"distilled_at: 2024-01-15T10:00:00Z\n" +
	"entry_count: 8\n" +
	"---\n\n" +
	"# Session summary\n\nOld body.\n"

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

// An essence older than the current generation is refused, not guessed at:
// one that declares none, and one that declares generation 1.
func TestLoadCompactedSession_RefusesAnOlderGeneration(t *testing.T) {
	for name, body := range map[string]string{
		"unversioned":  unversionedEssence,
		"generation 1": strings.Replace(unversionedEssence, "---\n", "---\n"+schemaver.Key+": 1\n", 1),
		"empty block":  "---\n\n---\nbody\n",
	} {
		t.Run(name, func(t *testing.T) {
			fsys := afero.NewMemMapFs()
			testsupport.WriteFile(t, fsys, "/s/old-one.md", []byte(body), 0o644)
			_, err := LoadCompactedSession(fsys, "/s", "old-one")
			require.ErrorIs(t, err, schemaver.ErrTooOld)
		})
	}
}

// Reading an essence already at the current generation never writes, even
// under --write-upgrades: there is nothing to persist.
func TestLoadCompactedSession_WriteUpgradesLeavesCurrentEssenceUntouched(t *testing.T) {
	t.Cleanup(func() { schemaver.BindWriteUpgrades(pflag.NewFlagSet("reset", pflag.ContinueOnError)) })
	flags := pflag.NewFlagSet("t", pflag.ContinueOnError)
	schemaver.BindWriteUpgrades(flags)
	require.NoError(t, flags.Set(schemaver.WriteUpgradesFlag, "true"))

	current := "---\n" + schemaver.Key + ": " + strconv.Itoa(essenceKind.Current()) + "\n" +
		"session_id: now\ncompacted_at: 2024-01-15T10:00:00Z\n---\n\nbody\n"
	base := afero.NewMemMapFs()
	testsupport.WriteFile(t, base, "/s/now.md", []byte(current), 0o644)

	loaded, err := LoadCompactedSession(afero.NewReadOnlyFs(base), "/s", "now")
	require.NoError(t, err, "a read with nothing to migrate must not attempt a write")
	assert.Equal(t, "body\n", loaded.Body)
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
	assert.Contains(t, fm, "compacted_at")
}
