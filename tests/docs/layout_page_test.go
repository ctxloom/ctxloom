//go:build arch

package docs

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/gitignore"
	"github.com/ctxloom/ctxloom/internal/paths"
	tasksp "github.com/ctxloom/ctxloom/internal/shared/tasks/paths"
)

// docsLayoutPage is the user-facing account of the .ctxloom layout.
const docsLayoutPage = "layout.md"

// pagePathRe pulls every project-rooted .ctxloom path out of the page. The
// character class deliberately admits `<` and `>` so placeholder segments
// (`<harp>`, `<engine-leaf>`) are captured rather than truncating the match
// silently, and `*` so the lock glob in the quoted .gitignore block is caught.
var pagePathRe = regexp.MustCompile(`\.ctxloom/[A-Za-z0-9_.*<>/-]+`)

// homePagePathRe is pagePathRe's HOME-root twin: it requires the leading `~/`
// pagePathRe's own loop deliberately skips (see the "A ~ immediately before"
// comment below), so the two regexes partition every .ctxloom path on the
// page between them rather than double-matching.
var homePagePathRe = regexp.MustCompile(`~/\.ctxloom/[A-Za-z0-9_.*<>/-]+`)

// TestArch_LayoutPageNamesOnlyRealPaths keeps docs/layout.md from describing a
// directory ctxloom does not write.
//
// The failure this prevents is specific and has happened to this repo's own
// ignore list: `.ctxloom/pieces/` and `.ctxloom/ephemeral/` were carried for
// releases as patterns for features that were never built, and anyone auditing
// "what does ctxloom keep out of git" read them as a tier that exists. A page
// written for users is a louder version of the same claim, and it drifts the
// same way — by outliving a path, not by inventing one.
//
// The declarative sources are the authority: paths.Layout (every path this
// tree's writers produce, classified) and gitignore.PrivateStatePatterns (what
// ctxloom writes into a project's .gitignore). A path on the page passes when
// it IS one of those, or is nested under one — `.ctxloom/content/bundles` is
// covered by the `.ctxloom/content` row, `.ctxloom/state/<harp>/home` by
// `.ctxloom/state/`, which is exactly how the blanket rules are meant to work.
//
// Home-rooted paths (`~/.ctxloom/...`) are a different root with a different
// lifecycle; TestArch_LayoutPageNamesOnlyRealHomePaths below is their mirror
// of this same check, against paths.Layout()'s RootHome rows instead.
func TestArch_LayoutPageNamesOnlyRealPaths(t *testing.T) {
	body := readRepoDoc(t, docsLayoutPage)
	known := declaredProjectPaths()

	var checked int
	for _, m := range pagePathRe.FindAllStringIndex(body, -1) {
		start, end := m[0], m[1]
		// A `~` immediately before the match makes this the HOME root.
		if start > 0 && (body[start-1] == '~' || body[start-1] == '/') {
			continue
		}
		path := strings.TrimSuffix(body[start:end], "/")
		checked++
		if !coveredBy(path, known) {
			t.Errorf("docs/%s names %q, which is neither in paths.Layout() nor covered by gitignore.PrivateStatePatterns — the page is describing a path nothing writes, or a real path lost its declaration",
				docsLayoutPage, path)
		}
	}

	// An empty-input guard: a regex that stopped matching would otherwise make
	// every assertion above vacuous and this test green forever.
	if checked < 8 {
		t.Fatalf("only %d .ctxloom paths were extracted from docs/%s — the page or the extractor changed shape, and the check above is now vacuous",
			checked, docsLayoutPage)
	}
}

// TestArch_LayoutPageNamesOnlyRealHomePaths is TestArch_LayoutPageNamesOnly
// RealPaths' mirror for the `~/.ctxloom/...` paths that test deliberately
// skips: added by C13 (fs-consolidation plan) once paths.Layout() gained a
// Root discriminator and could finally enumerate the home-rooted stores
// (sessions, approvals, signers, trigger cache, coord, companion consent).
// Before that, docs/layout.md's home-tree table was prose no declarative
// source could check — this closes the same drift hole the project-side test
// already closes for the project tree.
func TestArch_LayoutPageNamesOnlyRealHomePaths(t *testing.T) {
	body := readRepoDoc(t, docsLayoutPage)
	known := declaredHomePaths()

	var checked int
	for _, m := range homePagePathRe.FindAllStringIndex(body, -1) {
		start, end := m[0], m[1]
		match := strings.TrimSuffix(body[start:end], "/")
		path := strings.TrimPrefix(match, "~/")
		checked++
		if !coveredBy(path, known) {
			t.Errorf("docs/%s names %q, which is neither a RootHome row in paths.Layout() nor the documented tasks/paths exception — the page is describing a home path nothing writes, or a real one lost its declaration",
				docsLayoutPage, match)
		}
	}

	// The same empty-input guard TestArch_LayoutPageNamesOnlyRealPaths uses,
	// scaled to how many home paths the page is expected to name today (the
	// home-tree table alone names 7 stores).
	if checked < 7 {
		t.Fatalf("only %d ~/.ctxloom paths were extracted from docs/%s — the page or the extractor changed shape, and the check above is now vacuous",
			checked, docsLayoutPage)
	}
}

// declaredProjectPaths is the union of the two declarative sources, each
// normalized to a slash-separated path with no trailing separator. Filtered
// to RootProject: a RootHome row can share Rel text with a RootProject one
// (".ctxloom/sessions" names both the project's distilled-history row and
// the home sessions store), and mixing them here would let a HOME-only path
// pass this, the PROJECT-path check, by accident.
func declaredProjectPaths() []string {
	var out []string
	for _, e := range paths.Layout() {
		if e.Root != paths.RootProject {
			continue
		}
		out = append(out, filepath.ToSlash(e.Rel))
	}
	for _, p := range gitignore.PrivateStatePatterns {
		out = append(out, strings.TrimSuffix(p, "/"))
	}
	return out
}

// declaredHomePaths is declaredProjectPaths' RootHome twin, plus two
// documented exceptions the page names but paths.Layout() deliberately does
// NOT carry a row for:
//   - `~/.ctxloom/tasks` is taskloom's own log store (internal/shared/tasks/
//     paths.HomeTasksDir), a sibling vocabulary that aliases
//     internal/paths.AppDirName without folding into it — see
//     docs/architecture/core/paths.md's "Where documented and real behavior
//     diverge" note on tasks/paths.IndexFileName for the same boundary drawn
//     the other way. Referenced via the constants, not a hand-rolled literal,
//     so a rename there cannot silently desync this allowlist.
//   - `~/.ctxloom/logs/ctxloom.log` (paths.HomeLogFilePath) is diagnostic
//     output every ctxloom process writes at startup, not state whose absence
//     doctor's local-tier check would ever report — see Presence's doc in
//     internal/paths for why it has no Layout row at all.
func declaredHomePaths() []string {
	out := []string{
		filepath.ToSlash(filepath.Join(tasksp.AppDirName, tasksp.TasksDir)),
		filepath.ToSlash(filepath.Join(paths.AppDirName, paths.LogsDir, paths.LogFileName)),
	}
	for _, e := range paths.Layout() {
		if e.Root != paths.RootHome {
			continue
		}
		out = append(out, filepath.ToSlash(e.Rel))
	}
	return out
}

// coveredBy reports whether path is one of the declared paths or sits beneath
// one. A declared pattern containing a glob (`.ctxloom/*.lock`) is matched with
// filepath.Match, which is what git itself does with that line.
func coveredBy(path string, known []string) bool {
	for _, k := range known {
		if path == k || strings.HasPrefix(path, k+"/") {
			return true
		}
		if strings.ContainsAny(k, "*?[") {
			if ok, err := filepath.Match(k, path); err == nil && ok {
				return true
			}
		}
	}
	return false
}

// readRepoDoc reads docs/<rel> by walking up from the test's working directory
// to the module root, so the gate does not care where it is run from.
func readRepoDoc(t *testing.T, rel string) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			body, readErr := os.ReadFile(filepath.Join(dir, "docs", rel))
			if readErr != nil {
				t.Fatalf("read docs/%s: %v", rel, readErr)
			}
			return string(body)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find the module root (no go.mod above the working directory)")
		}
		dir = parent
	}
}
