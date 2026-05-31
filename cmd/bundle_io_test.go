// Tests for the file-IO helpers extracted from runBundleCreate /
// runBundleImport / runBundleExport. The cobra wrappers stay thin
// (load config + delegate); the testable surface is the validation,
// overwrite-guard, and dest-path-dispatch logic these helpers carry.
package cmd

import (
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// writeBundleSkeleton
// =============================================================================

func TestWriteBundleSkeleton_CreatesFileWithExpectedContent(t *testing.T) {
	fs := afero.NewMemMapFs()
	path, err := writeBundleSkeleton(fs, "/.ctxloom/bundles", "my-bundle", "Standard dev")
	require.NoError(t, err)
	assert.Equal(t, "/.ctxloom/bundles/my-bundle.yaml", path)

	data, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	body := string(data)

	// The skeleton must seed a default version + the user-supplied
	// description, plus the two example items so the user has a
	// working starting point.
	assert.Contains(t, body, "version: 1.0.0")
	assert.Contains(t, body, "description: Standard dev")
	assert.Contains(t, body, "example:")
	assert.Contains(t, body, "Example Fragment")
	assert.Contains(t, body, "Example prompt content")
}

func TestWriteBundleSkeleton_EmptyDescription(t *testing.T) {
	fs := afero.NewMemMapFs()
	path, err := writeBundleSkeleton(fs, "/b", "x", "")
	require.NoError(t, err)
	data, _ := afero.ReadFile(fs, path)
	// The top-level Bundle.Description field is omitempty; with empty
	// input it must not surface as a top-level `description:` line.
	// (The nested example prompt has its own description, which we don't
	// want to confuse with — top-level keys are unindented.)
	assert.NotContains(t, string(data), "\ndescription:")
	assert.NotRegexp(t, `^description:`, string(data))
}

func TestWriteBundleSkeleton_RefusesOverwrite(t *testing.T) {
	fs := afero.NewMemMapFs()
	_, err := writeBundleSkeleton(fs, "/b", "x", "")
	require.NoError(t, err)

	_, err = writeBundleSkeleton(fs, "/b", "x", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestWriteBundleSkeleton_CreatesParentDir(t *testing.T) {
	// Parent dir doesn't exist yet → MkdirAll must land it.
	fs := afero.NewMemMapFs()
	path, err := writeBundleSkeleton(fs, "/nested/deeper/bundles", "x", "")
	require.NoError(t, err)
	exists, _ := afero.Exists(fs, path)
	assert.True(t, exists)
}

// =============================================================================
// importBundleFile
// =============================================================================

const validBundleYAML = `version: "1.0.0"
description: "Imported bundle"
fragments:
  example:
    content: "Hello"
prompts:
  greet:
    content: "Say hi"
mcp:
  fs:
    command: "mcp-fs"
`

func TestImportBundleFile_HappyPath(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/src/my-bundle.yaml", []byte(validBundleYAML), 0644))

	destPath, bundle, err := importBundleFile(fs, "/src/my-bundle.yaml", "/dst/bundles", false)
	require.NoError(t, err)
	assert.Equal(t, "/dst/bundles/my-bundle.yaml", destPath)

	// Parsed bundle is returned so the caller can build a summary line.
	require.NotNil(t, bundle)
	assert.Equal(t, "1.0.0", bundle.Version)
	assert.Len(t, bundle.Fragments, 1)
	assert.Len(t, bundle.Prompts, 1)
	assert.Len(t, bundle.MCP, 1)

	// File was actually written byte-for-byte.
	data, err := afero.ReadFile(fs, destPath)
	require.NoError(t, err)
	assert.Equal(t, validBundleYAML, string(data))
}

func TestImportBundleFile_MissingSource(t *testing.T) {
	fs := afero.NewMemMapFs()
	_, _, err := importBundleFile(fs, "/no/such/file.yaml", "/dst", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read source file")
}

func TestImportBundleFile_MalformedYAML(t *testing.T) {
	// A file that exists but doesn't parse as a bundle must error before
	// any write happens.
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/src/bad.yaml", []byte("not: [valid: bundle"), 0644))

	_, _, err := importBundleFile(fs, "/src/bad.yaml", "/dst", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid bundle file")

	// Confirm no destination was created — bundleDir creation only
	// happens AFTER validation.
	exists, _ := afero.Exists(fs, "/dst")
	assert.False(t, exists, "validation failure must not leave a partial destination")
}

func TestImportBundleFile_RefusesOverwriteWithoutForce(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/src/x.yaml", []byte(validBundleYAML), 0644))
	require.NoError(t, afero.WriteFile(fs, "/dst/x.yaml", []byte("preexisting"), 0644))

	_, _, err := importBundleFile(fs, "/src/x.yaml", "/dst", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
	assert.Contains(t, err.Error(), "--force")

	// Existing file must not have been overwritten.
	data, _ := afero.ReadFile(fs, "/dst/x.yaml")
	assert.Equal(t, "preexisting", string(data))
}

func TestImportBundleFile_ForceOverwrites(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/src/x.yaml", []byte(validBundleYAML), 0644))
	require.NoError(t, afero.WriteFile(fs, "/dst/x.yaml", []byte("preexisting"), 0644))

	destPath, _, err := importBundleFile(fs, "/src/x.yaml", "/dst", true)
	require.NoError(t, err)

	data, _ := afero.ReadFile(fs, destPath)
	assert.Equal(t, validBundleYAML, string(data), "force=true must replace the existing file")
}

func TestImportBundleFile_FilenamePreserved(t *testing.T) {
	// The destination filename is filepath.Base of the source — useful
	// because the import preserves the bundle's identity across paths.
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/long/path/to/special-name.yaml",
		[]byte(validBundleYAML), 0644))

	destPath, _, err := importBundleFile(fs, "/long/path/to/special-name.yaml", "/dst", false)
	require.NoError(t, err)
	assert.Equal(t, "/dst/special-name.yaml", destPath)
}

// =============================================================================
// exportBundleFile
// =============================================================================

func TestExportBundleFile_OutputFileDispatch(t *testing.T) {
	// -o <file> dispatches to a specific output path; the parent dir
	// is created automatically.
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/src/bundle.yaml", []byte("content"), 0644))

	destPath, err := exportBundleFile(fs, "/src/bundle.yaml", "/exports/new/dir/special.yaml", "")
	require.NoError(t, err)
	assert.Equal(t, "/exports/new/dir/special.yaml", destPath)

	data, _ := afero.ReadFile(fs, destPath)
	assert.Equal(t, "content", string(data))
}

func TestExportBundleFile_DestDirDispatch(t *testing.T) {
	// <dest-dir> positional dispatches to <dir>/<basename(src)>.
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/src/my-bundle.yaml", []byte("content"), 0644))

	destPath, err := exportBundleFile(fs, "/src/my-bundle.yaml", "", "/exports")
	require.NoError(t, err)
	assert.Equal(t, "/exports/my-bundle.yaml", destPath)
}

func TestExportBundleFile_PrefersOutputFileOverDestDir(t *testing.T) {
	// If both are non-empty (would be a flag-misuse), the switch picks
	// outputFile. Document the precedence so a future refactor doesn't
	// silently flip it.
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/src/x.yaml", []byte("c"), 0644))

	destPath, err := exportBundleFile(fs, "/src/x.yaml", "/o/specific.yaml", "/d/should-not-use")
	require.NoError(t, err)
	assert.Equal(t, "/o/specific.yaml", destPath, "outputFile must win over destDir")

	exists, _ := afero.Exists(fs, "/d")
	assert.False(t, exists, "destDir must not be created when outputFile wins")
}

func TestExportBundleFile_MissingArgsError(t *testing.T) {
	// Neither outputFile nor destDir → error before any write.
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/src/x.yaml", []byte("c"), 0644))

	_, err := exportBundleFile(fs, "/src/x.yaml", "", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "either -o")
}

func TestExportBundleFile_MissingSource(t *testing.T) {
	fs := afero.NewMemMapFs()
	_, err := exportBundleFile(fs, "/no/such/file.yaml", "", "/dst")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read bundle")
}

func TestExportBundleFile_OutputFileWithoutParentDir(t *testing.T) {
	// outputFile = "just-a-filename.yaml" (no directory component).
	// filepath.Dir returns ".", which the helper skips — write should
	// just land at the relative path in the fs root.
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/src/x.yaml", []byte("c"), 0644))

	destPath, err := exportBundleFile(fs, "/src/x.yaml", "out.yaml", "")
	require.NoError(t, err)
	assert.Equal(t, "out.yaml", destPath)
	data, _ := afero.ReadFile(fs, "out.yaml")
	assert.Equal(t, "c", string(data))
}

// Sanity: round-trip a real bundle through import+export round.
func TestImportExportRoundTrip(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/originals/round.yaml", []byte(validBundleYAML), 0644))

	importedPath, _, err := importBundleFile(fs, "/originals/round.yaml", "/local/bundles", false)
	require.NoError(t, err)

	exportPath, err := exportBundleFile(fs, importedPath, "", "/published")
	require.NoError(t, err)
	assert.Equal(t, "/published/round.yaml", exportPath)

	roundTripped, err := afero.ReadFile(fs, exportPath)
	require.NoError(t, err)
	assert.Equal(t, validBundleYAML, string(roundTripped), "round trip must preserve bytes exactly")

	// The destination filename mirrors the original — verify here so a
	// future change that re-derives filenames trips this assertion.
	assert.Equal(t, "round.yaml", filepath.Base(exportPath))
}
