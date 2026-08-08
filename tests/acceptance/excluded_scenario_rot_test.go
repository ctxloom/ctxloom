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
// written AHEAD of the surface it describes — j13_closeout's `session distill
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
	files, err := filepath.Glob(filepath.Join(featuresDir(t), "*.feature"))
	if err != nil {
		t.Fatalf("glob features: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no feature files found — a silent pass here would prove nothing")
	}

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
	t.Logf("resolved %d command invocation(s) across excluded scenarios", checked)
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
