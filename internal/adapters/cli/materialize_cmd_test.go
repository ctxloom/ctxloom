package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/delivery/deliverytest"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"
)

// matProfile is the project's default profile for these tests: a team
// guardrail hook (the mock carries hooks) and one fragment of context.
const matProfile = `description: "materialize fixture"
bundles:
  - matctx
hooks:
  unified:
    session_start:
      - type: command
        command: echo team-guardrail
`

// matProject scaffolds a mock-bound project, gives its default profile a
// hook, and runs the test from inside it.
func matProject(t *testing.T) string {
	t.Helper()
	root, _ := setupProject(t, "mock")
	path := filepath.Join(bundletree.ProjectProfilesDir(t, filepath.Join(root, ".ctxloom")), "default.yaml")
	testsupport.WriteFileString(t, afero.NewOsFs(), path, matProfile, 0o644)
	bundletree.WriteOS(t, paths.LocalBundlesPathFor(filepath.Join(root, ".ctxloom"), paths.LayoutV2), "matctx",
		"version: 1.0.0\nfragments:\n  rules:\n    content: \"MAT-CONTEXT-MARK\"\n")
	chdir(t, root)
	return root
}

// snapshot is every file under root but the project's own .ctxloom, and
// every ownership record.
func snapshot(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	for _, f := range deliverytest.RelativeFiles(afero.NewOsFs(), root) {
		if !strings.HasPrefix(f, ".ctxloom/") {
			out = append(out, f)
		}
	}
	records, err := paths.HomeRecordsDir()
	require.NoError(t, err)
	for _, f := range deliverytest.RelativeFiles(afero.NewOsFs(), records) {
		out = append(out, "records:"+f)
	}
	return out
}

// TestMaterializeCmd_TheProjectDirectoryNeedsYes (tests 21, 44, 45, 50): with
// no --target, materialize warns naming the directory, the engines and the
// kinds, writes nothing (files and record unchanged), and refuses with the
// typed sentinel; --force does not satisfy it; --yes writes the project root.
func TestMaterializeCmd_TheProjectDirectoryNeedsYes(t *testing.T) {
	root := matProject(t)
	before := snapshot(t, root)
	out, err := runCLIErr(t, "materialize", "--backend", "mock", "--surface", "hooks")
	require.True(t, errors.Is(err, operations.ErrProjectTargetUnconfirmed), "got %v", err)
	assert.Contains(t, out, "WARNING")
	assert.Contains(t, out, root)
	assert.Contains(t, out, "mock")
	assert.Contains(t, out, "hooks")
	assert.Equal(t, before, snapshot(t, root), "nothing written, record unchanged")

	_, err = runCLIErr(t, "materialize", "--backend", "mock", "--surface", "hooks", "--force")
	require.True(t, errors.Is(err, operations.ErrProjectTargetUnconfirmed), "--force is not --yes: %v", err)

	_, err = runCLIErr(t, "materialize", "--backend", "mock", "--surface", "hooks", "--yes")
	require.NoError(t, err)
	assert.NotEqual(t, before, snapshot(t, root), "--yes wrote the project root")
}

// TestMaterializeCmd_AnExplicitTargetNeedsNoYes (test 46): `--target .` is
// the user's own choice: no warning, no --yes.
func TestMaterializeCmd_AnExplicitTargetNeedsNoYes(t *testing.T) {
	root := matProject(t)
	before := snapshot(t, root)
	out, err := runCLIErr(t, "materialize", "--backend", "mock", "--surface", "hooks", "--target", ".")
	require.NoError(t, err)
	assert.NotContains(t, out, "WARNING")
	assert.NotEqual(t, before, snapshot(t, root))
}

// TestMaterializeCmd_DryRunIsNotGated (test 47): a dry run without --target
// prints the plan and the warning, exits zero and writes nothing.
func TestMaterializeCmd_DryRunIsNotGated(t *testing.T) {
	root := matProject(t)
	before := snapshot(t, root)
	out, err := runCLIErr(t, "materialize", "--backend", "mock", "--surface", "hooks", "--dry-run")
	require.NoError(t, err)
	assert.Contains(t, out, "WARNING")
	assert.Contains(t, out, "hooks")
	assert.Equal(t, before, snapshot(t, root))
}

// TestMaterializeCmd_ReleaseIsGatedToo (test 48).
func TestMaterializeCmd_ReleaseIsGatedToo(t *testing.T) {
	root := matProject(t)
	before := snapshot(t, root)
	_, err := runCLIErr(t, "materialize", "--backend", "mock", "--surface", "hooks", "--yes")
	require.NoError(t, err)
	written := snapshot(t, root)

	_, err = runCLIErr(t, "materialize", "--backend", "mock", "--release")
	require.True(t, errors.Is(err, operations.ErrProjectTargetUnconfirmed), "got %v", err)
	assert.Equal(t, written, snapshot(t, root))

	_, err = runCLIErr(t, "materialize", "--backend", "mock", "--release", "--yes")
	require.NoError(t, err)
	var after []string
	for _, f := range snapshot(t, root) {
		if !strings.HasPrefix(f, "records:") {
			after = append(after, f)
		}
	}
	var want []string
	for _, f := range before {
		if !strings.HasPrefix(f, "records:") {
			want = append(want, f)
		}
	}
	assert.Equal(t, want, after, "the release took out what --yes wrote")
}

// TestMaterializeCmd_JSONAbortsTheSameWay (test 49, the --format json half;
// the command never prompts, so there is no TTY branch to differ).
func TestMaterializeCmd_JSONAbortsTheSameWay(t *testing.T) {
	matProject(t)
	_, err := runCLIErr(t, "materialize", "--backend", "mock", "--format", "json")
	require.True(t, errors.Is(err, operations.ErrProjectTargetUnconfirmed), "got %v", err)
}

// TestMaterializeCmd_YesDoesNotOverrideTheGlobalScope (test 50's other
// half): --yes confirms the project directory, never an engine's
// user-global scope.
func TestMaterializeCmd_YesDoesNotOverrideTheGlobalScope(t *testing.T) {
	matProject(t)
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	_, err = runCLIErr(t, "materialize", "--target", home, "--backend", "claude-code", "--yes", "--dry-run")
	require.ErrorContains(t, err, "refusing")
}

// TestMaterializeCmd_RefusesIncoherentFlags (test 31).
func TestMaterializeCmd_RefusesIncoherentFlags(t *testing.T) {
	matProject(t)
	target := t.TempDir()
	for name, args := range map[string][]string{
		"unknown kind":            {"--surface", "widgets"},
		"unknown mechanism":       {"--surface", "context=hook"},
		"one kind two ways":       {"--surface", "context", "--surface", "context=file:x.md"},
		"release with profiles":   {"default", "--release"},
		"release with a dest":     {"--release", "--surface", "context=file:x.md"},
		"release with diff":       {"--release", "--diff", "x"},
		"diff with two engines":   {"--diff", "x", "--backend", "mock", "--backend", "claude-code"},
		"diff without context":    {"--diff", "x", "--surface", "hooks"},
		"dest off context":        {"--surface", "mcp=file:x.json"},
		"dest outside the target": {"--surface", "context=file:../x.md"},
	} {
		_, err := runCLIErr(t, append([]string{"materialize", "--target", target, "--backend", "mock"}, args...)...)
		require.Error(t, err, name)
	}
}

// TestAgentSet_ASurfaceTakesNoDestination (test 32).
func TestAgentSet_ASurfaceTakesNoDestination(t *testing.T) {
	_, err := surfacesFromFlag([]string{"context=file:docs/AGENTS.md"})
	require.ErrorContains(t, err, "no destination")
	got, err := surfacesFromFlag([]string{"context=file"})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"context": "file"}, got)
}

// TestMaterializeCmd_JSONShape (test 33): the --format json payload is
// MaterializeResult, golden with the temp target normalised.
func TestMaterializeCmd_JSONShape(t *testing.T) {
	golden := filepath.Join(pkgSourceDir(t), "testdata", "materialize_json.golden")
	matProject(t)
	target := t.TempDir()
	out, err := runCLIErr(t, "materialize", "--target", target, "--backend", "mock", "--surface", "hooks", "--surface", "skills", "--format", "json")
	require.NoError(t, err)
	resolved, err := filepath.EvalSymlinks(target)
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &doc), out)
	got, err := json.MarshalIndent(doc, "", "  ")
	require.NoError(t, err)
	normal := strings.ReplaceAll(string(got), resolved, "<target>")
	normal = regexp.MustCompile(`"profiles": \[[^\]]*\]`).ReplaceAllString(normal, `"profiles": ["<default>"]`)
	if os.Getenv("UPDATE_GOLDEN") != "" {
		testsupport.WriteFileString(t, afero.NewOsFs(), golden, normal+"\n", 0o644)
	}
	want, err := os.ReadFile(golden)
	require.NoError(t, err)
	assert.Equal(t, string(want), normal+"\n")
}

// TestMaterializeCmd_DiffComposesThroughTheWritersConsumer (test 30): the
// file `--surface context` just wrote diffs as identical.
func TestMaterializeCmd_DiffComposesThroughTheWritersConsumer(t *testing.T) {
	matProject(t)
	target := t.TempDir()
	_, err := runCLIErr(t, "materialize", "--target", target, "--backend", "mock", "--surface", "context")
	require.NoError(t, err)
	written := filepath.Join(target, mock.ContextFileName)
	require.FileExists(t, written)
	out, err := runCLIErr(t, "materialize", "--backend", "mock", "--surface", "context", "--diff", written, "--format", "json")
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &doc), out)
	assert.Equal(t, true, doc["identical"], out)
}

// TestMaterializeCmd_DiffShowsWhatDiffers: against a file holding text the
// materialized context does not, --diff reports a difference as a unified
// diff from the file to the default agent's context.
func TestMaterializeCmd_DiffShowsWhatDiffers(t *testing.T) {
	matProject(t)
	theirs := filepath.Join(t.TempDir(), "CONTEXT.md")
	testsupport.WriteFileString(t, afero.NewOsFs(), theirs, "LOCAL-ONLY-LINE\n", 0o644)
	out, err := runCLIErr(t, "materialize", "--backend", "mock", "--diff", theirs, "--format", "json")
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &doc), out)
	assert.Equal(t, false, doc["identical"], out)
	diff, _ := doc["diff"].(string)
	assert.Contains(t, diff, "--- "+theirs)
	assert.Contains(t, diff, "+++ the default agent (materialized here)")
	assert.Contains(t, diff, "-LOCAL-ONLY-LINE")
}

// TestRenderMaterialize_TheTextReport: the heading's verb follows the
// status (an applied run reads "Materialized"); profiles, the context file
// and a premise withhold's carrying surface appear only when there is one.
func TestRenderMaterialize_TheTextReport(t *testing.T) {
	render := func(res *operations.MaterializeResult) string {
		var b strings.Builder
		require.NoError(t, renderMaterialize(&b, res))
		return b.String()
	}
	full := render(&operations.MaterializeResult{
		Target: "/out", Status: operations.MaterializeApplied, Created: true, Profiles: []string{"go-dev", "review"},
		Engines: []operations.EngineOutcome{{
			Engine: "mock", Wrote: []string{"context"}, Released: []string{"mcp"}, ContextFile: "/out/docs/AGENTS.md",
			NotCarried: []agent.SurfaceLoss{{Surface: "hooks", Reason: "mock has no hook mechanism"}},
			WithheldByPremise: []operations.PremiseWithhold{
				{Name: "b/f/release", Premise: "cutting a release", Delivered: "skills"},
				{Name: "b/f/deploy", Premise: "deploying"},
			},
			Skipped: []string{"bad name"}, Warnings: []string{"slow disk"},
		}},
		Warnings: []string{"heads up"},
	})
	assert.Equal(t, `Materialized → /out (applied)
  created /out
  profiles: go-dev, review
mock
  wrote context
  released mcp
  context file /out/docs/AGENTS.md
  NOT carried: hooks — mock has no hook mechanism
  withheld by premise: b/f/release (cutting a release) → skills
  withheld by premise: b/f/deploy (deploying)
  skipped: bad name
  warning: slow disk
warning: heads up
`, full)

	bare := render(&operations.MaterializeResult{Target: "/out", Status: operations.MaterializeReleased, Engines: []operations.EngineOutcome{{Engine: "mock"}}})
	assert.Equal(t, "Released → /out (released)\nmock\n", bare, "no profiles, no context file: no lines for them")
	assert.True(t, strings.HasPrefix(render(&operations.MaterializeResult{Target: "/out", Status: operations.MaterializePlanned}), "Would materialize → /out"))
}
