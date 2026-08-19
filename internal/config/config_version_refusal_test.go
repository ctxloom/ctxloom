package config

import (
	"fmt"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// loadRefusalFindings loads body as the project config and returns the findings
// the load raised, with the strictness gate isolated to this test.
func loadRefusalFindings(t *testing.T, body string) []strictness.Finding {
	t.Helper()
	strictness.Reset()
	strictness.SetDegraded(false)
	t.Cleanup(func() { strictness.Reset() })

	fs := afero.NewMemMapFs()
	appDir := "/project/" + paths.AppDirName
	require.NoError(t, fs.MkdirAll(appDir, 0o755))
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(appDir), []byte(body), 0o644))

	mark := strictness.Checkpoint()
	_, err := Load(WithFS(fs), WithAppDir(appDir))
	require.NoError(t, err, "the refusal is a FINDING, not a load error: the gate decides")
	return strictness.Since(mark)
}

// A config older than the current schema is refused rather than repaired. There
// are no in-place upgraders any more, so a document at an older version would
// otherwise be read as though its retired key names still meant something.
//
// The message must name three things, because a user who cannot act on it is
// worse off than one who got a parse error: WHICH file, WHAT version it claims,
// and what this build requires.
func TestLoad_OlderThanCurrentVersion_IsRefusedWithAnActionableFinding(t *testing.T) {
	found := loadRefusalFindings(t, "version: 5\nllm:\n  defaults:\n    primary: claude-code\n")

	require.Len(t, found, 1, "exactly one finding: %+v", found)
	assert.Equal(t, strictness.ClassMigration, found[0].Class)
	assert.Contains(t, found[0].Message, "version: 5", "the finding must quote the version the file declares")
	assert.Contains(t, found[0].Message, fmt.Sprintf("%d", CurrentConfigVersion), "and the version this build requires")
	assert.Contains(t, found[0].Message, paths.ConfigPath("/project/"+paths.AppDirName), "and name the file")
	assert.Contains(t, found[0].FixIt, "ctxloom init", "the remedy is re-scaffolding, and the finding must say so")
}

// A document with NO `version:` key is the pre-versioning generation — older
// than every numbered schema, not exempt from the check. Treating absent as
// current is the silent-acceptance direction, and it would let the oldest
// configs in the world through the one gate built to stop them.
func TestLoad_UnversionedConfig_IsRefusedAsPreVersioning(t *testing.T) {
	found := loadRefusalFindings(t, "llm:\n  defaults:\n    primary: claude-code\n")

	require.Len(t, found, 1, "exactly one finding: %+v", found)
	assert.Equal(t, strictness.ClassMigration, found[0].Class)
	assert.Contains(t, found[0].Message, "pre-versioning",
		"an unversioned document must be named for what it is, not reported as `version: 0`")
}

// The current version is accepted silently. Without this, a refusal that fired
// on EVERY config would pass both tests above while breaking every user.
func TestLoad_CurrentVersion_RaisesNoFinding(t *testing.T) {
	found := loadRefusalFindings(t,
		fmt.Sprintf("version: %d\nllm:\n  defaults:\n    primary: claude-code\n", CurrentConfigVersion))

	assert.Empty(t, found, "a current config must raise nothing: %+v", found)
}
