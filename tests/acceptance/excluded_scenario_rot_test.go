//go:build acceptance

package acceptance

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/cli"
)

// runStepCommand matches the command a `When I run "ctxloom ..."` step drives.
var runStepCommand = regexp.MustCompile(`I run "ctxloom ([^"]+)"`)

// excludedScenarioTags are the tags that keep a scenario out of the default
// run. It is the same set completeness_test.go's excludedCorpusTags names, for
// the same reason, and the two are asserted equal below so they cannot drift.
var excludedScenarioTags = []string{"@wip", "@live", "@future", "@network", "@container"}

// TestExcludedScenarios_InvokeCommandsThatStillExist closes the blind spot that
// let a whole feature file rot unnoticed.
//
// A scenario tagged @wip/@live/@future/@network never runs in the default
// suite, so nothing tells it when the command it drives is DELETED. The suite
// stays green while containing a test for a command that no longer exists, and
// the rot is invisible precisely because the test is excluded — the exclusion
// that makes it cheap also makes it silent.
//
// That is not hypothetical. init.feature drove `ctxloom manage init` behind a
// @network tag; `manage init` was deleted outright, and
// internal/cli/manage_test.go asserted its absence. The repo therefore held one
// test asserting the command was gone and another asserting it worked, green,
// for as long as both existed. It would have failed instantly had it ever run.
//
// This resolves each excluded scenario's command against the REAL cobra tree,
// so a deletion breaks the scenario that depends on it the same day.
//
// WHAT THIS DELIBERATELY DOES NOT CHECK: flags. A @wip scenario is often
// written AHEAD of the surface it describes — j001300_closeout's `session distill
// --skill/--to-bundle` rows are exactly that, tracked separately, and failing
// them here would punish the legitimate use of @wip. A missing COMMAND is
// different in kind: you cannot write a scenario ahead of a command and have it
// name a path that already resolves, so a path that stops resolving is a
// deletion, not an anticipation.
func TestExcludedScenarios_InvokeCommandsThatStillExist(t *testing.T) {
	// Drift guard: this test's premise is that it covers exactly the scenarios
	// the default run skips. If the two lists diverge, either this checks
	// scenarios that do run (harmless but misleading) or misses ones that do
	// not (the hole reopens).
	got := append([]string{}, excludedScenarioTags...)
	want := append([]string{}, excludedCorpusTags...)
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("excludedScenarioTags %v must equal completeness_test.go's excludedCorpusTags %v", got, want)
	}

	root := cli.GetRootCmd()
	files := excludedScenarioFeatureFiles(t)

	checked := 0
	for _, path := range files {
		for _, inv := range excludedInvocations(t, path) {
			checked++
			if err := resolves(root, inv.args); err != nil {
				t.Errorf("%s\n  scenario: %s\n  runs:     ctxloom %s\n  %v\n"+
					"  This scenario is excluded from the default run, so nothing else will ever tell you.",
					filepath.Base(path), inv.scenario, inv.raw, err)
			}
		}
	}
	// A zero-invocation run would pass vacuously, and this file's whole value is
	// that it looks at scenarios nothing else looks at.
	if checked == 0 {
		t.Fatal("found no commands in excluded scenarios — the parse is broken, not the suite clean")
	}
	t.Logf("resolved %d command invocation(s) across excluded scenarios in %d feature file(s)", checked, len(files))
}

type invocation struct {
	scenario string
	raw      string
	args     []string
}

// excludedInvocations returns every `ctxloom ...` invocation inside a scenario
// that the default run skips. Tags are inherited from the Feature line exactly
// as godog treats them, mirroring runnableFeatureText's line-based approach and
// for the same stated reason: a second Gherkin parser here would be a second
// model of the tag rules, free to drift from the one the suite actually uses.
func excludedInvocations(t *testing.T, path string) []invocation {
	t.Helper()
	var out []invocation
	var fileTags, pending []string
	scenario := ""
	excluded := false

	for _, line := range strings.Split(string(readOrFail(t, path)), "\n") {
		s := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(s, "@"):
			pending = append(pending, strings.Fields(s)...)
			continue
		case strings.HasPrefix(s, "Feature:"):
			fileTags = append(fileTags, pending...)
			pending = nil
			continue
		case strings.HasPrefix(s, "Scenario"), strings.HasPrefix(s, "Background"):
			excluded = anyExcluded(append(append([]string{}, fileTags...), pending...))
			scenario = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(s, "Scenario Outline:"), "Scenario:"))
			pending = nil
			continue
		}
		if !excluded {
			continue
		}
		if m := runStepCommand.FindStringSubmatch(s); m != nil {
			out = append(out, invocation{scenario: scenario, raw: m[1], args: commandPath(m[1])})
		}
	}
	return out
}

func anyExcluded(tags []string) bool {
	for _, tag := range tags {
		for _, ex := range excludedScenarioTags {
			if tag == ex || strings.HasPrefix(tag, ex+".") {
				return true
			}
		}
	}
	return false
}

// commandPath returns the leading non-flag words of an invocation: the
// subcommand path, stopping at the first flag or argument-bearing token. A
// Scenario Outline placeholder (<engine>) ends the path too — its value is not
// known here, and guessing one would invent a command nobody wrote.
func commandPath(cmdline string) []string {
	var out []string
	for _, w := range strings.Fields(cmdline) {
		if strings.HasPrefix(w, "-") || strings.HasPrefix(w, "<") {
			break
		}
		out = append(out, w)
	}
	return out
}

// resolves walks the cobra tree along args, consuming as many leading words as
// name a real subcommand. It stops at the first word that is not a subcommand
// (a positional argument, e.g. a harp name) rather than failing, because a
// scenario legitimately passes arguments — only a word that should have been a
// command and is not gets reported.
func resolves(root *cobra.Command, args []string) error {
	if len(args) == 0 {
		return nil
	}
	cur := root
	for i, word := range args {
		next := findChild(cur, word)
		if next == nil {
			if i == 0 {
				return errUnknown("ctxloom has no command " + word)
			}
			// The discriminator is whether cur HAS subcommands, NOT whether it
			// is runnable. Two earlier attempts got this wrong and both let the
			// rotted `manage init` scenario pass:
			//   - "cur.Args != nil -> positional" : `manage` carries an Args
			//     validator, so the dead word read as an argument.
			//   - "!cur.Runnable() -> subcommand" : `manage` IS runnable, it
			//     prints its own help, so the check never fired.
			// A command that owns subcommands is a namespace; a word after it
			// that matches none of them was meant to be one, and is now gone.
			// A leaf owns no subcommands, so its trailing words are genuine
			// positional arguments (a harp name, a selector) and are left alone.
			if hasSubcommands(cur) {
				return errUnknown(cur.CommandPath() + " has no subcommand " + word +
					" (known: " + strings.Join(subcommandNames(cur), ", ") + ")")
			}
			return nil
		}
		cur = next
	}
	return nil
}

func findChild(c *cobra.Command, name string) *cobra.Command {
	for _, ch := range c.Commands() {
		if ch.Name() == name {
			return ch
		}
		for _, alias := range ch.Aliases {
			if alias == name {
				return ch
			}
		}
	}
	return nil
}

type errUnknown string

func (e errUnknown) Error() string { return string(e) }

// excludedScenarioFeatureFiles is the SUBJECT of the rot gate: every feature
// file in the corpus, found by walking rather than globbing.
//
// It is a named symbol rather than an inline call because two things must agree
// about what the gate looks at — the gate itself and
// TestExcludedScenarioGate_VisitsEveryFeatureFileIncludingSubdirectories, which
// proves the set is complete. With the discovery inlined, that proof would be
// about a helper the gate was free to stop using, and a regression to a flat
// glob would pass it.
//
// A flat filepath.Glob("features/*.feature") is what this replaces, and the
// replacement is the whole point of the fix. features/ is being split into
// journeys/ and cli/; the glob had already gone blind to 39 of 71 files while
// staying green, because its only vacuity guard asked whether ANY file was
// found and the remaining root files answered yes. Coverage decayed with every
// slice of the split and nothing could say so. Walking is what completeness_test.go's
// loadCorpus and feature_parse_test.go's TestFeatureFilesParse already do, for
// this same reason; this gate was the one that was missed.
func excludedScenarioFeatureFiles(t *testing.T) []string {
	t.Helper()
	return featureFilesOrFail(t, featuresDir(t))
}

// TestExcludedScenarioGate_VisitsEveryFeatureFileIncludingSubdirectories proves
// the rot gate's subject is the WHOLE corpus.
//
// The assertion is deliberately a SET, not a count and not existence. Existence
// ("some files were found") is satisfied by the very bug this fixes — the flat
// glob found 32 files and reported success. A bare count would pass on any 71
// files, including 71 wrong ones. Set equality against an independent
// enumeration is the only form that a blind discovery cannot satisfy.
//
// The enumeration below is written with os.ReadDir recursion rather than
// filepath.WalkDir on purpose: reusing WalkDir here would compare the helper
// against itself, and a walk that skipped a directory would be invisible in
// both halves.
//
// The nested-file guard is what keeps this test honest OVER TIME. Set equality
// alone would go quiet the day the corpus happened to be flat, and a flat
// corpus is exactly the condition under which a flat glob is indistinguishable
// from a walk. Requiring subdirectory files makes the test fail loudly if it
// ever loses its ability to detect the bug, instead of passing for the wrong
// reason.
func TestExcludedScenarioGate_VisitsEveryFeatureFileIncludingSubdirectories(t *testing.T) {
	dir := featuresDir(t)
	got := excludedScenarioFeatureFiles(t)
	want := everyFeatureFileUnder(t, dir)

	if diff := setDifference(want, got); len(diff) > 0 {
		t.Errorf("the rot gate does not see %d of %d feature file(s): %v\n"+
			"  A file the gate never opens cannot fail it, so every excluded scenario in these is unguarded.",
			len(diff), len(want), diff)
	}
	if diff := setDifference(got, want); len(diff) > 0 {
		t.Errorf("the rot gate reports %d file(s) that are not in the corpus: %v", len(diff), diff)
	}

	nested := 0
	for _, path := range want {
		if filepath.Dir(path) != dir {
			nested++
		}
	}
	if nested == 0 {
		t.Fatalf("every one of the %d feature file(s) sits at the root of %s, so a flat glob "+
			"and a recursive walk return the same set and this test can no longer tell them apart. "+
			"It is asserting nothing until the corpus has subdirectories again.", len(want), dir)
	}
	t.Logf("gate sees %d feature file(s): %d at the root of %s, %d in subdirectories",
		len(got), len(want)-nested, dir, nested)
}

// everyFeatureFileUnder enumerates .feature files under dir recursively using
// os.ReadDir, independently of the filepath.WalkDir the gate's own discovery
// uses. Sorted, so a comparison against it is order-stable.
func everyFeatureFileUnder(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	var descend func(string)
	descend = func(d string) {
		entries, err := os.ReadDir(d)
		if err != nil {
			t.Fatalf("read dir %s: %v", d, err)
		}
		for _, e := range entries {
			path := filepath.Join(d, e.Name())
			switch {
			case e.IsDir():
				descend(path)
			case strings.HasSuffix(e.Name(), ".feature"):
				out = append(out, path)
			}
		}
	}
	descend(dir)
	if len(out) == 0 {
		t.Fatalf("no .feature files under %s — the corpus cannot be empty, so this enumeration is broken", dir)
	}
	sort.Strings(out)
	return out
}

// setDifference returns the members of a that are absent from b.
func setDifference(a, b []string) []string {
	inB := make(map[string]bool, len(b))
	for _, s := range b {
		inB[s] = true
	}
	var out []string
	for _, s := range a {
		if !inB[s] {
			out = append(out, s)
		}
	}
	return out
}

// featuresDir resolves the features directory from this test file's own
// location, so it does not depend on the working directory the suite runs in.
func featuresDir(t *testing.T) string {
	t.Helper()
	dir := "features"
	if _, err := os.Stat(dir); err == nil {
		return dir
	}
	t.Fatalf("features directory not found from %s", mustGetwd(t))
	return ""
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return wd
}

// hasSubcommands reports whether c owns any visible subcommand — i.e. whether
// it is a namespace rather than a leaf.
func hasSubcommands(c *cobra.Command) bool {
	for _, ch := range c.Commands() {
		if !ch.Hidden && ch.Name() != "help" {
			return true
		}
	}
	return false
}

// subcommandNames lists c's visible subcommands, so a failure names what the
// scenario could have meant instead of only what it cannot have.
func subcommandNames(c *cobra.Command) []string {
	var out []string
	for _, ch := range c.Commands() {
		if !ch.Hidden && ch.Name() != "help" {
			out = append(out, ch.Name())
		}
	}
	sort.Strings(out)
	return out
}
