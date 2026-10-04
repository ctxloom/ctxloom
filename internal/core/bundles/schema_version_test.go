package bundles

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/schemaver"
)

// envelopeKeys decodes an envelope into its top-level keys, so a test can
// ask what a writer put on disk without matching on formatting.
func envelopeKeys(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, yaml.Unmarshal(raw, &m))
	return m
}

func TestParseBundle_KeylessEnvelopeIsGenerationZeroAndGetsTheShapeUpgrades(t *testing.T) {
	keyless := []byte("version: 1.2.0\nprompts:\n  review:\n    content: c\n    llm:\n      claude:\n        model: haiku\n")

	b, err := ParseBundle(keyless)

	require.NoError(t, err)
	require.Contains(t, b.Commands, "review", "generation 0 runs the prompts -> commands step")
	assert.Contains(t, b.Commands["review"].Exports, "claude", "generation 0 runs the llm -> exports step")
	assert.Equal(t, "1.2.0", b.Version, "the author's semver is never touched by a format migration")
}

func TestParseBundle_CurrentEnvelopePassesThrough(t *testing.T) {
	current := []byte(schemaver.Key + ": 1\nversion: 1.2.0\ncommands:\n  review:\n    content: c\n")

	b, err := ParseBundle(current)

	require.NoError(t, err, "the strict decode must accept "+schemaver.Key)
	require.Contains(t, b.Commands, "review")
	assert.Equal(t, "1.2.0", b.Version)
}

// The shape steps run because the generation says so, not because they find
// work: a current envelope still spelling a retired key is refused by the
// strict decode rather than silently migrated.
func TestParseBundle_CurrentEnvelopeDoesNotRunTheGenerationZeroSteps(t *testing.T) {
	stale := []byte(schemaver.Key + ": 1\nversion: 1.2.0\nprompts:\n  review:\n    content: c\n")

	_, err := ParseBundle(stale)

	require.Error(t, err)
}

func TestParseBundle_NewerEnvelopeIsRefusedNamingBothNumbers(t *testing.T) {
	newer := envelopeKind.Current() + 1
	doc := []byte(schemaver.Key + ": " + itoaYAML(t, newer) + "\nversion: 1.2.0\n")

	_, err := ParseBundle(doc)

	require.ErrorIs(t, err, schemaver.ErrNewer)
	var verr *schemaver.VersionError
	require.True(t, errors.As(err, &verr))
	assert.Equal(t, newer, verr.Found)
	assert.Equal(t, envelopeKind.Current(), verr.Current)
}

func TestTreeEnvelope_StampsTheCurrentGenerationAndKeepsTheAuthorsVersion(t *testing.T) {
	raw, err := TreeEnvelope(&Bundle{Version: "1.2.0", Description: "d"})
	require.NoError(t, err)

	keys := envelopeKeys(t, raw)
	assert.Equal(t, envelopeKind.Current(), keys[schemaver.Key])
	assert.Equal(t, "1.2.0", keys["version"])

	back, err := ParseBundle(raw)
	require.NoError(t, err, "what a writer emits must read back")
	assert.Equal(t, "1.2.0", back.Version)
}

// withWriteUpgrades turns the process-wide --write-upgrades switch on for one
// test, the way a root command's flag parse would, and off again after.
func withWriteUpgrades(t *testing.T) {
	t.Helper()
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	schemaver.BindWriteUpgrades(fs)
	require.NoError(t, fs.Set(schemaver.WriteUpgradesFlag, "true"))
	t.Cleanup(func() { schemaver.BindWriteUpgrades(pflag.NewFlagSet("reset", pflag.ContinueOnError)) })
}

// loadProjectTree loads name through the project reader and returns what the
// user was told.
func loadProjectTree(t *testing.T, fsys afero.Fs, name string) string {
	t.Helper()
	var said bytes.Buffer
	t.Cleanup(clidiag.SetSink(&said))
	_, err := NewLoader(NewProjectReader(fsys, []string{"/bundles"}, WithReaderReporter(ledger()))).Load(name)
	require.NoError(t, err)
	return said.String()
}

func TestProjectReader_WriteUpgradesPersistsAnUnsignedEnvelope(t *testing.T) {
	withWriteUpgrades(t)
	mem, dir := localTreeFixture(t, "plain", false)
	envelope := filepath.Join(dir, DirectoryFormManifest)
	before, err := afero.ReadFile(mem, envelope)
	require.NoError(t, err)

	loadProjectTree(t, mem, "plain")

	after, err := afero.ReadFile(mem, envelope)
	require.NoError(t, err)
	keys := envelopeKeys(t, after)
	assert.Equal(t, envelopeKind.Current(), keys[schemaver.Key])
	assert.Equal(t, "2.0.0", keys["version"])
	assert.NotEqual(t, before, after)
	backup, err := afero.Exists(mem, envelope+schemaver.BackupSuffix)
	require.NoError(t, err)
	assert.False(t, backup, "a project bundle tree is version-controlled: git holds the prior bytes, no .bak is left in it")

	loadProjectTree(t, mem, "plain") // the upgraded tree still reads
}

func TestProjectReader_WriteUpgradesSkipsASignedTree(t *testing.T) {
	withWriteUpgrades(t)
	mem, dir := localTreeFixture(t, "sealed", true)
	envelope := filepath.Join(dir, DirectoryFormManifest)
	before, err := afero.ReadFile(mem, envelope)
	require.NoError(t, err)

	said := loadProjectTree(t, mem, "sealed")

	after, err := afero.ReadFile(mem, envelope)
	require.NoError(t, err)
	assert.Equal(t, before, after, "a signed tree's bytes are what its signature covers")
	assert.Contains(t, said, resignToPersist)
	wrote, err := afero.Exists(mem, envelope+schemaver.BackupSuffix)
	require.NoError(t, err)
	assert.False(t, wrote, "nothing is written beside a signed tree")
}

func TestProjectReader_WithoutWriteUpgradesNothingIsWritten(t *testing.T) {
	mem, dir := localTreeFixture(t, "plain", false)
	envelope := filepath.Join(dir, DirectoryFormManifest)
	before, err := afero.ReadFile(mem, envelope)
	require.NoError(t, err)

	loadProjectTree(t, mem, "plain")

	after, err := afero.ReadFile(mem, envelope)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func itoaYAML(t *testing.T, n int) string {
	t.Helper()
	out, err := yaml.Marshal(n)
	require.NoError(t, err)
	return string(bytes.TrimSpace(out))
}
