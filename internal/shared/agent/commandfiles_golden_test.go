package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/ledger"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// oldWriteManagedCommandFiles is a VERBATIM copy of WriteManagedCommandFiles as
// it existed immediately before this commit (Part B3-seam) generalized it into
// a thin adapter over WriteManagedPackageFiles. It exists ONLY so
// TestWriteManagedCommandFiles_GoldenByteIdentical can run the pre-refactor
// algorithm and the post-refactor adapter side by side against the same
// fixture in one test process — a genuine before/after capture without
// depending on a git checkout during the test run. Do not call this from
// production code; it is frozen, dead-by-design test scaffolding.
func oldWriteManagedCommandFiles(fs afero.Fs, dir, manifestName string, cmds []CommandExport, render func(CommandExport) (relPath string, content []byte, err error), opts ...ManagedWriteOption) error {
	o := &managedWriteOptions{}
	for _, opt := range opts {
		opt(o)
	}
	// Frozen scaffolding: the trailing-newline option it once read was retired
	// with the per-engine manifest names, so the pre-refactor byte shape is
	// pinned here instead of taken from live option state.
	const manifestTrailingNewline = false
	manifestPath := filepath.Join(dir, manifestName)

	if data, err := afero.ReadFile(fs, manifestPath); err == nil {
		for _, name := range strings.Split(string(data), "\n") {
			if name = strings.TrimSpace(name); name != "" {
				path, ok := SafeCommandRelPath(dir, name)
				if !ok {
					continue
				}
				_ = fs.Remove(path)
			}
		}
		_ = fs.Remove(manifestPath)
	}

	var written []string
	for _, c := range cmds {
		if !c.Enabled {
			continue
		}
		if _, ok := SafeCommandRelPath(dir, c.Name); !ok {
			continue
		}
		relPath, content, err := render(c)
		if err != nil {
			continue
		}
		path, ok := SafeCommandRelPath(dir, relPath)
		if !ok {
			continue
		}
		if o.dedupHomeDir != "" && filepath.Clean(dir) != filepath.Clean(o.dedupHomeDir) {
			if homePath, ok := SafeCommandRelPath(o.dedupHomeDir, relPath); ok {
				if existing, rerr := afero.ReadFile(fs, homePath); rerr == nil && string(existing) == string(content) {
					continue
				}
			}
		}
		if len(written) == 0 {
			if err := fs.MkdirAll(dir, 0755); err != nil {
				continue
			}
		}
		if parent := filepath.Dir(path); parent != filepath.Clean(dir) {
			if err := fs.MkdirAll(parent, 0755); err != nil {
				continue
			}
		}
		if err := afero.WriteFile(fs, path, content, 0644); err != nil {
			continue
		}
		written = append(written, relPath)
	}

	if len(written) == 0 {
		return nil
	}
	manifest := strings.Join(written, "\n")
	if manifestTrailingNewline {
		manifest += "\n"
	}
	return afero.WriteFile(fs, manifestPath, []byte(manifest), 0644)
}

// goldenRender is the fixture renderer both algorithms run through: a
// dash-flattened nested name plus ".md", content verbatim — matching the shape
// claude's WriteCommandFiles renderer uses in production.
func goldenRender(c CommandExport) (string, []byte, error) {
	filename := strings.ReplaceAll(c.Name, "/", "-") + ".md"
	return filename, []byte(c.Content), nil
}

// listTree walks dir and returns "relpath => content" for every regular file,
// so two materialized trees can be compared byte-for-byte independent of
// walk/map ordering.
func listTree(t *testing.T, fs afero.Fs, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := afero.Walk(fs, dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			return rerr
		}
		data, rerr := afero.ReadFile(fs, path)
		if rerr != nil {
			return rerr
		}
		out[filepath.ToSlash(rel)] = string(data)
		return nil
	})
	require.NoError(t, err)
	return out
}

// TestWriteManagedCommandFiles_GoldenByteIdentical is the B3-seam regression
// guard: WriteManagedCommandFiles, now a thin adapter over
// WriteManagedPackageFiles, must produce a BYTE-IDENTICAL materialized tree
// (every file's path + content, plus the manifest) to the frozen pre-refactor
// algorithm (oldWriteManagedCommandFiles) for the same fixture — a nested
// name, a plain name, a disabled command, and a dedup-eligible duplicate.
// This is the guard called out by skill-command-split.plan.md §3.4: if this
// test cannot stay green, the seam refactor must be escalated, not forced.
func TestWriteManagedCommandFiles_GoldenByteIdentical(t *testing.T) {
	home := "/home/.claude/commands"
	fixtureCommands := []CommandExport{
		{Name: "recover", Content: "Recover the session", Enabled: true},
		{Name: "group/nested", Content: "Nested command body", Enabled: true},
		{Name: "disabled-one", Content: "should not appear", Enabled: false},
		{Name: "shadowed", Content: "SAME AS HOME", Enabled: true}, // dedup-eligible
	}

	// Seed the home dir (both runs dedup against it identically) with a file
	// byte-identical to "shadowed" so the dedup path is exercised on both sides.
	seedHome := func(fs afero.Fs) {
		require.NoError(t, afero.WriteFile(fs, filepath.Join(home, "shadowed.md"), []byte("SAME AS HOME"), 0644))
	}

	oldFS := afero.NewMemMapFs()
	newFS := afero.NewMemMapFs()
	seedHome(oldFS)
	seedHome(newFS)

	oldDir := "/proj/.claude/commands"
	newDir := "/proj/.claude/commands"

	require.NoError(t, oldWriteManagedCommandFiles(oldFS, oldDir, ".ctxloom-manifest", fixtureCommands, goldenRender, WithDedupHomeDir(home)))
	require.NoError(t, WriteManagedCommandFiles(newFS, newDir, fixtureCommands, goldenRender, WithDedupHomeDir(home)))

	// The MARKER is deliberately different now — the pre-refactor writer wrote
	// a per-engine ".ctxloom-manifest" of bare names, the current one writes the
	// shared surface-typed ledger — so it is excluded here. What must NOT have
	// changed is the delivered payload: every command file, at the same path,
	// with the same bytes. That is what this golden capture is actually for.
	oldTree := withoutMarkers(listTree(t, oldFS, oldDir))
	newTree := withoutMarkers(listTree(t, newFS, newDir))
	assert.Equal(t, oldTree, newTree, "the delivered command files (paths + content) must stay byte-identical to the pre-refactor writer")
	require.NotEmpty(t, newTree, "an empty tree would make this comparison vacuous")

	// Sanity: the fixture actually exercised every interesting path (nested,
	// disabled, dedup-skipped), so this golden test isn't accidentally vacuous.
	assert.Contains(t, oldTree, "recover.md")
	assert.Contains(t, oldTree, "group-nested.md")
	assert.NotContains(t, oldTree, "disabled-one.md")
	assert.NotContains(t, oldTree, "shadowed.md", "dedup-skipped against the identical home copy")
}

// withoutMarkers drops manifest/ledger bookkeeping files from a tree listing so
// a payload comparison is not confounded by a bookkeeping format change.
func withoutMarkers(tree map[string]string) map[string]string {
	out := make(map[string]string, len(tree))
	for k, v := range tree {
		base := filepath.Base(k)
		if base == ledger.Name || strings.HasPrefix(base, ".ctxloom-") {
			continue
		}
		out[k] = v
	}
	return out
}
