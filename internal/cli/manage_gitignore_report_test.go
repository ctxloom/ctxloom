package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/gitignore"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// runGitignoreInstall executes `manage gitignore install` with the given extra
// args and returns its combined output, requiring a clean exit.
func runGitignoreInstall(t *testing.T, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs(append([]string{"manage", "gitignore", "install"}, args...))
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
	})
	require.NoError(t, rootCmd.Execute())
	return out.String()
}

// TestManageGitignoreInstall_NoChangeDoesNotClaimUpdate is the reporting
// regression. `manage gitignore install` printed "Updated <path>" and reported
// status "updated" UNCONDITIONALLY — on a file it had not touched by a single
// byte just as loudly as on one it had rewritten.
//
// That is what let the retirement defect hide. A project whose blanket rule the
// binary did not recognise got the same "Updated" as one that migrated cleanly,
// so the only way to discover nothing had happened was to run `git diff`
// afterwards and think to be suspicious. A command that describes the file it
// actually wrote makes a silent no-op unmissable at the point it happens.
func TestManageGitignoreInstall_NoChangeDoesNotClaimUpdate(t *testing.T) {
	testsupport.ProjectDir(t)

	// --format is explicit: it is a persistent flag on the shared rootCmd, so a
	// value another test in this package set would otherwise stick.
	first := runGitignoreInstall(t, "--format", formatText)
	require.Contains(t, first, "Wrote",
		"the first run writes ctxloom's own nested .gitignore and must say so")

	second := runGitignoreInstall(t, "--format", formatText)
	require.NotContains(t, second, "Updated",
		"the second run changes nothing, so it must not claim an update")
	require.Contains(t, strings.ToLower(second), "no change",
		"a run that wrote nothing must say it wrote nothing")
}

// TestManageGitignoreInstall_NoChangeJSONStatus pins the machine-readable half:
// a caller parsing --format json must be able to tell a no-op from a write.
func TestManageGitignoreInstall_NoChangeJSONStatus(t *testing.T) {
	testsupport.ProjectDir(t)

	first := runGitignoreInstallJSON(t)
	require.Equal(t, "updated", first["status"])

	second := runGitignoreInstallJSON(t)
	require.Equal(t, "unchanged", second["status"],
		"a byte-identical file must not be reported as updated")
	require.Empty(t, second["added"], "nothing was added")
	require.Empty(t, second["retired"], "nothing was retired")
}

// runGitignoreInstallJSON runs the command with --format json and decodes it.
func runGitignoreInstallJSON(t *testing.T, args ...string) map[string]any {
	t.Helper()
	out := runGitignoreInstall(t, append(args, "--format", "json")...)
	require.Truef(t, json.Valid([]byte(out)), "invalid JSON:\n%s", out)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &payload))
	return payload
}

// TestManageGitignoreInstall_ReportsPreExistingRootRulesAsRedundant pins the
// migration decision for a project an OLDER ctxloom already wrote to. Those
// root rules are now duplicated by .ctxloom/.gitignore, and the choice made
// here is to NAME them and leave them: deleting lines from a file ctxloom does
// not own is not a write it gets to make unasked. Reporting is what lets the
// user delete them deliberately.
//
// The rules must be NAMED rather than counted — a user cannot act on a number.
func TestManageGitignoreInstall_ReportsPreExistingRootRulesAsRedundant(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	path := filepath.Join(dir, ".gitignore")
	stale := "# ctxloom private working state\n" +
		strings.Join(gitignore.PrivateStatePatterns, "\n") + "\nnode_modules/\n"
	require.NoError(t, os.WriteFile(path, []byte(stale), 0o644))

	payload := runGitignoreInstallJSON(t)

	redundant := payload["redundant"]
	require.Len(t, redundant, len(gitignore.PrivateStatePatterns),
		"every stale root rule must be reported, not just detected")
	for _, p := range gitignore.PrivateStatePatterns {
		require.Contains(t, redundant, p, "the redundant rule %s must be named", p)
	}

	// Reported, NOT removed. This is the half that pins the decision rather
	// than the detection.
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	for _, p := range gitignore.PrivateStatePatterns {
		require.Contains(t, ignoreRules(string(after)), p,
			"ctxloom must not delete %s from a file it does not own", p)
	}
	require.Contains(t, ignoreRules(string(after)), "node_modules/", "and an unrelated rule is untouched")
}

// TestManageGitignoreInstall_ReportsRetiredBlanket pins the migration case in
// the exact shape that produced the original report: a .gitignore that already
// carries every pattern the command would append, plus a blanket `.ctxloom/*`.
// Nothing is added, so retirement is the only thing that happens — and if the
// command cannot say so, a user has no way to distinguish a completed migration
// from one that never ran.
func TestManageGitignoreInstall_ReportsRetiredBlanket(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	patterns := append([]string{}, gitignore.PrivateStatePatterns...)
	content := "# Local config\n.ctxloom/*\n!.ctxloom/plans/\n" + strings.Join(patterns, "\n") + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(content), 0o644))

	payload := runGitignoreInstallJSON(t)
	require.Equal(t, "updated", payload["status"])
	require.Equal(t, []any{".ctxloom/*"}, payload["retired"],
		"the blanket rule that was removed must be named")
	require.Empty(t, payload["added"], "every pattern was already present")

	after, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	require.NoError(t, err)
	require.NotContains(t, string(after), "\n.ctxloom/*\n", "the blanket really is gone")
	require.Contains(t, string(after), "!.ctxloom/plans/", "the user's re-include really did survive")
}

// ignoreRules returns content's non-empty, non-comment lines, trimmed — the
// test's own reading of the file, independent of the production helper.
func ignoreRules(content string) []any {
	var out []any
	for _, line := range strings.Split(content, "\n") {
		s := strings.TrimSpace(line)
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		out = append(out, s)
	}
	return out
}
