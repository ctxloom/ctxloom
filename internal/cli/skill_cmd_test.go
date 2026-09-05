package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/paths"
)

func skillListCmdWithOutput() (*cobra.Command, *bytes.Buffer) {
	cmd := &cobra.Command{}
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	return cmd, buf
}

// TestPrintSkillList_GenuinelyEmpty is the case where no skills exist at all
// (no --bundle filter, or a filter that also matches the true empty state):
// the create-one hint is the right guidance.
func TestPrintSkillList_GenuinelyEmpty(t *testing.T) {
	cmd, buf := skillListCmdWithOutput()
	err := printSkillList(cmd, nil, "", 0)
	assert.NoError(t, err)
	out := buf.String()
	assert.Contains(t, out, "No skills found.")
	assert.Contains(t, out, "Create one with: ctxloom skill create <bundle> <name>")
}

// TestPrintSkillList_BundleFilterExcludesEverything pins a fix: --bundle
// naming a bundle that has no skills (or a typo) used to print the plain
// "No skills found." + create-a-skill hint even when skills exist in OTHER
// bundles — misleading, since skills do exist, just not under this filter.
// The message must name the filtered bundle and point at the unfiltered
// listing instead of suggesting nothing exists.
func TestPrintSkillList_BundleFilterExcludesEverything(t *testing.T) {
	cmd, buf := skillListCmdWithOutput()
	err := printSkillList(cmd, nil, "nonexistent-bundle", 3)
	assert.NoError(t, err)
	out := buf.String()
	assert.Contains(t, out, `No skills found in bundle "nonexistent-bundle".`)
	assert.Contains(t, out, "Skills exist in other bundles")
	assert.NotContains(t, out, "Create one with:",
		"the create-a-skill hint is wrong here — skills already exist, just not in this bundle")
}

// TestPrintSkillList_NonEmptyRendersEntries is the non-empty control case,
// pinning the existing grouped-by-bundle rendering survives the signature
// change unchanged.
func TestPrintSkillList_NonEmptyRendersEntries(t *testing.T) {
	cmd, buf := skillListCmdWithOutput()
	entries := []operations.SkillEntry{
		{Name: "code-reviewer", Source: "core"},
	}
	err := printSkillList(cmd, entries, "", 1)
	assert.NoError(t, err)
	out := buf.String()
	assert.Contains(t, out, "Skills (1):")
	assert.Contains(t, out, "core:")
	assert.Contains(t, out, "code-reviewer")
}

// `skill sync <bundle>#skills/` names a skill and then names nothing. Widening
// that to a whole-bundle sync rewrites the files: manifest of every skill the
// bundle ships — manifests the user never named, and whose rewrite is exactly
// what re-arms (or silently re-baselines) the install-time tamper check. The
// separator is an explicit narrowing request, so an empty name after it is a
// typo to refuse, never a broader default to assume.
func TestSkillSyncTarget(t *testing.T) {
	t.Run("bare bundle name selects every skill", func(t *testing.T) {
		b, n, err := skillSyncTarget("my-bundle")
		require.NoError(t, err)
		assert.Equal(t, "my-bundle", b)
		assert.Empty(t, n, "a bare bundle name is the documented whole-bundle sync")
	})

	t.Run("qualified ref selects exactly one skill", func(t *testing.T) {
		b, n, err := skillSyncTarget("my-bundle#skills/code-reviewer")
		require.NoError(t, err)
		assert.Equal(t, "my-bundle", b)
		assert.Equal(t, "code-reviewer", n)
	})

	for _, arg := range []string{"my-bundle#skills/", "my-bundle#skills/   "} {
		t.Run("empty name after the separator is refused: "+arg, func(t *testing.T) {
			_, _, err := skillSyncTarget(arg)
			require.Error(t, err, "an empty name after #skills/ must not widen to a whole-bundle sync")
			assert.Contains(t, err.Error(), "#skills/")
		})
	}
}

// Every command RunE must hand the operations layer the context COBRA owns —
// cmd.Context() — never a fresh context.Background(). Root wires the
// signal-cancelled context into that one value, so a RunE that substitutes its
// own detaches its work from Ctrl-C and from any deadline a parent (the MCP
// server, an ACP session, a test) set. `skill` was the last group doing this:
// six of its seven subcommands opened their own root context.
//
// A behavioural pin is not available here and the reason matters: every
// operations/skills.go entry point takes `_ context.Context` and DISCARDS it,
// so no cancellation is observable through this seam today in either
// direction. The invariant worth maintaining is therefore the wiring itself —
// once the operations layer starts honoring its ctx, this file must already be
// passing the right one.
func TestSkillCommands_UseCobraContextNotBackground(t *testing.T) {
	// Absolute, from this package's compiled-in source path — not a bare
	// "skill_cmd.go" relative read: TestMain sandboxes the test binary into a
	// temp cwd (so nothing here can reach the real ~/.ctxloom), where a
	// relative read fails outright.
	src, err := os.ReadFile(filepath.Join(pkgSourceDir(t), "skill_cmd.go"))
	require.NoError(t, err)
	var offending []string
	for i, line := range strings.Split(string(src), "\n") {
		if strings.Contains(line, "context.Background()") {
			offending = append(offending, fmt.Sprintf("skill_cmd.go:%d: %s", i+1, strings.TrimSpace(line)))
		}
	}
	assert.Empty(t, offending,
		"skill subcommands must pass cmd.Context() to the operations layer, not a detached root context")
}

// TestSkillSyncTarget_RefusesAnotherKindsSelector pins the guard the collapse
// onto bundles.ParseItemAsk adds: a selector naming a kind other than skills
// used to fall through to the bare-bundle form, because the old splitter cut
// on the literal "#skills/" and nothing else. That widened one mistyped ref
// into a rewrite of every skill manifest in the bundle — the exact
// re-baselining of the install-time tamper check the narrowing rule exists to
// prevent.
func TestSkillSyncTarget_RefusesAnotherKindsSelector(t *testing.T) {
	for _, arg := range []string{"my-bundle#fragments/x", "my-bundle#mcp/pg", "my-bundle#commands/review"} {
		b, n, err := skillSyncTarget(arg)
		require.Error(t, err, "%q selects something other than a skill and must not widen to a whole-bundle sync", arg)
		assert.Empty(t, b, arg)
		assert.Empty(t, n, arg)
	}
}

// dirFormBundle creates an empty directory-form bundle — the shape skills
// require (a single-file bundle cannot hold a skill package).
func dirFormBundle(t *testing.T, appDir, name string) {
	t.Helper()
	dir := filepath.Join(paths.LocalBundlesPath(appDir), name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bundle.yaml"), []byte("version: \"1.0\"\n"), 0o644))
}

// TestRunSkillImport_AdvisesAReviewCommandTheCLIAccepts closes an honesty gap:
// the import footer printed "run: ctxloom review <bundle>", and reviewCmd is
// declared cobra.NoArgs — so the one command the tool told the user to run was
// rejected by the tool that told them to run it.
//
// The assertion deliberately does NOT match the footer's wording. It takes the
// advice the command actually printed, splits off whatever it advised passing
// to `ctxloom review`, and hands that argv to reviewCmd's OWN argument
// validator. That keeps the two in agreement no matter how either is reworded,
// and it fails the moment the bundle name is appended back on.
func TestRunSkillImport_AdvisesAReviewCommandTheCLIAccepts(t *testing.T) {
	root := agentProject(t, "version: 6\n")
	appDir := filepath.Join(root, ".ctxloom")
	cfg, err := GetConfig()
	require.NoError(t, err)
	dirFormBundle(t, appDir, "src")
	dirFormBundle(t, appDir, "dst")

	_, err = operations.CreateSkill(context.Background(), cfg, operations.CreateSkillRequest{
		Bundle: "src", Name: "reviewer", Description: "Reviews Go diffs.",
	})
	require.NoError(t, err)
	_, err = operations.SyncSkill(context.Background(), cfg, operations.SyncSkillRequest{Bundle: "src", Name: "reviewer"})
	require.NoError(t, err)

	zipPath := filepath.Join(t.TempDir(), "reviewer.zip")
	_, err = operations.ExportSkill(context.Background(), cfg, operations.ExportSkillRequest{
		Bundle: "src", Name: "reviewer", OutPath: zipPath,
	})
	require.NoError(t, err)

	skillImportBundle = "dst"
	t.Cleanup(func() { skillImportBundle = "" })

	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	require.NoError(t, runSkillImport(cmd, []string{zipPath}))

	const advised = "ctxloom review"
	printed := out.String()
	require.Contains(t, printed, advised, "the import must still point the user at review")
	idx := strings.Index(printed, advised)
	rest, _, _ := strings.Cut(printed[idx+len(advised):], "\n")

	assert.NoError(t, reviewCmd.Args(reviewCmd, strings.Fields(rest)),
		"`%s%s` is what the import told the user to run; `ctxloom review` must accept it", advised, rest)
}
