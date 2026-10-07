//go:build acceptance

package acceptance

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/tests/integration/testenv"
	"github.com/cucumber/godog"
	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"
)

func registerFileSteps(ctx *godog.ScenarioContext) {
	// Seeds a file the user is taken to have authored by hand, so a scenario can
	// assert what a ctxloom rewrite does to content ctxloom did not write.
	ctx.Step(`^the project already has the file "([^"]*)":$`, func(c context.Context, rel string, body *godog.DocString) error {
		return worldFrom(c).env.WriteFile(rel, body.Content)
	})

	// Seeds an authored bundle stated as one YAML document; it lands as the
	// tree ctxloom reads (testenv.WriteBundleTree), each item in its own file.
	ctx.Step(`^the project already has the bundle "([^"]*)":$`, func(c context.Context, name string, body *godog.DocString) error {
		w := worldFrom(c)
		return testenv.WriteBundleTree(w.env.ProjectDir, name, body.Content)
	})

	// The HOME-layer twin of the step above. A scenario that needs to prove a
	// claim about CONFIG SCOPE has to place the same bytes at each layer in
	// turn, and only the project half could be written until now.
	ctx.Step(`^the home already has the file "([^"]*)":$`, func(c context.Context, rel string, body *godog.DocString) error {
		return worldFrom(c).env.WriteHomeFile(rel, body.Content)
	})

	// Owner access, not an exact mode: the umask decides the group and other
	// bits, but a directory ctxloom creates that its own user cannot list or
	// enter is broken whatever the umask.
	ctx.Step(`^the (project|home) directory "([^"]*)" is readable, writable and searchable by its owner$`, func(c context.Context, layer, rel string) error {
		w := worldFrom(c)
		root := w.env.ProjectDir
		if layer == "home" {
			root = w.env.HomeDir
		}
		info, err := os.Stat(filepath.Join(root, rel))
		if err != nil {
			return fmt.Errorf("stat %s directory %q: %w", layer, rel, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("%s path %q is not a directory", layer, rel)
		}
		if perm := info.Mode().Perm(); perm&0o700 != 0o700 {
			return fmt.Errorf("%s directory %q has mode %#o; its owner lacks rwx", layer, rel, perm)
		}
		return nil
	})

	// A machine ctxloom has never run on: the harness provisions the home
	// layer up front, so a scenario about ctxloom creating it removes it.
	ctx.Step(`^the home has no "([^"]*)" directory yet$`, func(c context.Context, rel string) error {
		return os.RemoveAll(filepath.Join(worldFrom(c).env.HomeDir, rel))
	})

	ctx.Step(`^the file "([^"]*)" exists$`, func(c context.Context, rel string) error {
		w := worldFrom(c)
		if !w.env.FileExists(rel) {
			return fmt.Errorf("project file %q does not exist", rel)
		}
		return nil
	})

	ctx.Step(`^the file "([^"]*)" does not exist$`, func(c context.Context, rel string) error {
		w := worldFrom(c)
		if w.env.FileExists(rel) {
			return fmt.Errorf("project file %q unexpectedly exists", rel)
		}
		return nil
	})

	ctx.Step(`^the file "([^"]*)" contains "([^"]*)"$`, func(c context.Context, rel, want string) error {
		return fileContains(c, false, rel, want)
	})

	// Regex counterpart to `contains`, and the reason it exists: substring
	// containment cannot express STRUCTURE, so assertions on generated JSON
	// were being written as quote-free fragments chosen to dodge quoting
	// ("ctxloom-auto", "${CLAUDE_PROJECT_DIR}") instead of `"command": ...`.
	// A fragment picked for what it can express, rather than for what the
	// scenario means, matches whatever else happens to contain it — j000100's
	// `contains "ctxloom hook"` was satisfied by the statusLine command and
	// survived deleting the SessionStart hook it named.
	//
	// The pattern capture is `(.*)` and NOT the `([^"]*)` used by every other
	// step in this file, deliberately: a `[^"]*` capture cannot carry a double
	// quote, which is precisely the character these assertions need. The step
	// text is a whole line, so anchoring the closing quote at `$` keeps the
	// greedy capture unambiguous. Write literal quotes in the pattern as \"
	// (Go's regexp reads it as a plain quote) to keep the Gherkin readable.
	ctx.Step(`^the file "([^"]*)" matches "(.*)"$`, func(c context.Context, rel, pattern string) error {
		w := worldFrom(c)
		re, err := regexp.Compile(pattern)
		if err != nil {
			return fmt.Errorf("invalid regexp %q: %w", pattern, err)
		}
		body, err := w.env.ReadFile(rel)
		if err != nil {
			return fmt.Errorf("read file %q: %w", rel, err)
		}
		if !re.MatchString(body) {
			return fmt.Errorf("file %q does not match /%s/; content:\n%s", rel, pattern, body)
		}
		return nil
	})

	ctx.Step(`^the file "([^"]*)" does not contain "([^"]*)"$`, func(c context.Context, rel, unwanted string) error {
		w := worldFrom(c)
		body, err := w.env.ReadFile(rel)
		if err != nil {
			return fmt.Errorf("read file %q: %w", rel, err)
		}
		if strings.Contains(body, unwanted) {
			return fmt.Errorf("file %q unexpectedly contains %q; content:\n%s", rel, unwanted, body)
		}
		return nil
	})

	ctx.Step(`^the file "([^"]*)" contains "([^"]*)" exactly (\d+) times$`, func(c context.Context, rel, want string, n int) error {
		w := worldFrom(c)
		body, err := w.env.ReadFile(rel)
		if err != nil {
			return fmt.Errorf("read file %q: %w", rel, err)
		}
		if got := strings.Count(body, want); got != n {
			return fmt.Errorf("file %q contains %q %d times, want %d; content:\n%s", rel, want, got, n, body)
		}
		return nil
	})

	ctx.Step(`^the file "([^"]*)" is valid YAML$`, func(c context.Context, rel string) error {
		w := worldFrom(c)
		body, err := w.env.ReadFile(rel)
		if err != nil {
			return fmt.Errorf("read %q: %w", rel, err)
		}
		var out any
		if err := yaml.Unmarshal([]byte(body), &out); err != nil {
			return fmt.Errorf("file %q is not valid YAML: %w", rel, err)
		}
		return nil
	})

	ctx.Step(`^the home file "([^"]*)" exists$`, func(c context.Context, rel string) error {
		w := worldFrom(c)
		if !w.env.HomeFileExists(rel) {
			return fmt.Errorf("home file %q does not exist", rel)
		}
		return nil
	})

	ctx.Step(`^the home file "([^"]*)" contains "([^"]*)"$`, func(c context.Context, rel, want string) error {
		return fileContains(c, true, rel, want)
	})

	// The home counterpart of `the file X does not contain Y`, and the
	// per-machine half of every "this landed in the OTHER store" assertion.
	// It deliberately READS the file
	// rather than checking it away: `the home file X does not exist` passes
	// against a harness that could never see a home file at all, so a scenario
	// that has just written something ELSE into that same file and then
	// asserts this is checking a live absence, not a missing fixture.
	ctx.Step(`^the home file "([^"]*)" does not contain "([^"]*)"$`, func(c context.Context, rel, unwanted string) error {
		w := worldFrom(c)
		body, err := w.env.ReadHomeFile(rel)
		if err != nil {
			return fmt.Errorf("read home file %q: %w", rel, err)
		}
		if strings.Contains(body, unwanted) {
			return fmt.Errorf("home file %q unexpectedly contains %q; content:\n%s", rel, unwanted, body)
		}
		return nil
	})

	ctx.Step(`^a home file matching "([^"]*)" exists$`, func(c context.Context, glob string) error {
		w := worldFrom(c)
		matches, err := filepath.Glob(filepath.Join(w.env.HomeDir, glob))
		if err != nil {
			return fmt.Errorf("bad glob %q: %w", glob, err)
		}
		if len(matches) == 0 {
			return fmt.Errorf("no home file matches %q", glob)
		}
		return nil
	})

	// Exact-count variant of the above: "exists" only proves presence, which
	// cannot catch a SECOND store silently getting minted alongside the
	// first (J002600 worktree-task-store journey's critical payload assertion —
	// a redirect that quietly does nothing would still leave a home file
	// matching the glob, just an extra one).
	ctx.Step(`^exactly (\d+) home files? match(?:es)? "([^"]*)"$`, func(c context.Context, n int, glob string) error {
		w := worldFrom(c)
		matches, err := filepath.Glob(filepath.Join(w.env.HomeDir, glob))
		if err != nil {
			return fmt.Errorf("bad glob %q: %w", glob, err)
		}
		if len(matches) != n {
			return fmt.Errorf("glob %q matched %d home files, want exactly %d: %v", glob, len(matches), n, matches)
		}
		return nil
	})

	// The read-only claim, made checkable. A command documented as a
	// DIAGNOSIS must leave the project exactly as it found it, and no
	// assertion on that command's own stdout can see it writing a file
	// somewhere else. Snapshot every byte under the project, then compare.
	ctx.Step(`^I record the project tree$`, func(c context.Context) error {
		w := worldFrom(c)
		tree, err := snapshotProjectTree(w.env.ProjectDir)
		if err != nil {
			return fmt.Errorf("record project tree: %w", err)
		}
		w.projectTree = tree
		return nil
	})

	ctx.Step(`^the project tree is unchanged$`, func(c context.Context) error {
		w := worldFrom(c)
		// Zero-length guard, the same one steps_skill.go's byte comparison
		// carries: comparing two empty trees is trivially unchanged, so an
		// unrecorded (or empty) snapshot must fail loudly instead of
		// certifying anything.
		if len(w.projectTree) == 0 {
			return fmt.Errorf(`the recorded project tree is EMPTY — comparing nothing to nothing is trivially "unchanged"; the "I record the project tree" step must run first, against a project that has files`)
		}
		d, err := diffProjectTree(w.env.ProjectDir, w.projectTree)
		if err != nil {
			return err
		}
		if d.empty() {
			return nil
		}
		return fmt.Errorf("the project tree changed: %s", d)
	})

	// The exact form of the control: the command wrote, and what it wrote is
	// ONE file the scenario names — so a control that also leaked anything
	// else into the project fails here rather than passing as "it moved".
	ctx.Step(`^the project tree changed only by adding "([^"]*)"$`, func(c context.Context, rel string) error {
		w := worldFrom(c)
		if len(w.projectTree) == 0 {
			return fmt.Errorf(`the recorded project tree is EMPTY — every file would count as added; the "I record the project tree" step must run first, against a project that has files`)
		}
		d, err := diffProjectTree(w.env.ProjectDir, w.projectTree)
		if err != nil {
			return err
		}
		if len(d.added) == 1 && d.added[0] == filepath.FromSlash(rel) && len(d.removed)+len(d.modified) == 0 {
			return nil
		}
		return fmt.Errorf("the project tree should have gained only %q: %s", rel, d)
	})

	// The CONTROL half of "the project tree is unchanged", and the reason that
	// step can be believed. An unchanged tree is the assertion this project's
	// characteristic bug satisfies for free: a command that exits 0 having done
	// nothing at all leaves the tree pristine, and so does a command correctly
	// declining to write. Nothing in the negative claim alone tells the two
	// apart.
	//
	// So a scenario asserting a no-write must also run the SAME command without
	// whatever suppressed the write, from the SAME recorded snapshot, and land
	// here. This step failing means the control wrote nothing either — the
	// fixture never had anything to suppress, and the paired negative proved
	// nothing.
	ctx.Step(`^the project tree has changed$`, func(c context.Context) error {
		w := worldFrom(c)
		// Same zero-length guard as its sibling, for the mirrored reason:
		// against an unrecorded snapshot every file in the project reads as
		// "added", so this would pass without the command doing anything.
		if len(w.projectTree) == 0 {
			return fmt.Errorf(`the recorded project tree is EMPTY — every file would count as added, so this passes without the command writing anything; the "I record the project tree" step must run first, against a project that has files`)
		}
		d, err := diffProjectTree(w.env.ProjectDir, w.projectTree)
		if err != nil {
			return err
		}
		if !d.empty() {
			return nil
		}
		return fmt.Errorf("the project tree is UNCHANGED across %d files — this step is the control for a paired no-write assertion, so the command was expected to write; if it wrote nothing, the negative half of that pair proves nothing", len(w.projectTree))
	})
}

// treeDiff is a project tree's movement against a recorded snapshot, each
// list sorted.
type treeDiff struct{ added, removed, modified []string }

func (d treeDiff) empty() bool { return len(d.added)+len(d.removed)+len(d.modified) == 0 }

func (d treeDiff) String() string {
	return fmt.Sprintf("added %v, removed %v, modified %v", d.added, d.removed, d.modified)
}

// diffProjectTree re-snapshots root and compares it with recorded.
func diffProjectTree(root string, recorded map[string]string) (treeDiff, error) {
	now, err := snapshotProjectTree(root)
	if err != nil {
		return treeDiff{}, fmt.Errorf("re-read project tree: %w", err)
	}
	var d treeDiff
	for rel, sum := range now {
		switch before, ok := recorded[rel]; {
		case !ok:
			d.added = append(d.added, rel)
		case before != sum:
			d.modified = append(d.modified, rel)
		}
	}
	for rel := range recorded {
		if _, ok := now[rel]; !ok {
			d.removed = append(d.removed, rel)
		}
	}
	slices.Sort(d.added)
	slices.Sort(d.removed)
	slices.Sort(d.modified)
	return d, nil
}

// snapshotProjectTree maps every file under root to a digest of its content.
// .git is skipped: its internal churn belongs to git, not to the command under
// test.
func snapshotProjectTree(root string) (map[string]string, error) {
	tree := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		if d.IsDir() {
			if rel == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		body, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		tree[rel] = fmt.Sprintf("%x", sha256.Sum256(body))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return tree, nil
}

func fileContains(c context.Context, home bool, rel, want string) error {
	w := worldFrom(c)
	var (
		body string
		err  error
		kind string
	)
	if home {
		body, err = w.env.ReadHomeFile(rel)
		kind = "home file"
	} else {
		body, err = w.env.ReadFile(rel)
		kind = "file"
	}
	if err != nil {
		return fmt.Errorf("read %s %q: %w", kind, rel, err)
	}
	if !strings.Contains(body, want) {
		return fmt.Errorf("%s %q does not contain %q; content:\n%s", kind, rel, want, body)
	}
	// j000400Excerpt matches one line at a time, so a multi-line want is
	// located by its first line.
	marker, _, _ := strings.Cut(want, "\n")
	w.docStepMaterialized = rel + ":\n" + j000400Excerpt(body, marker, 2)
	return nil
}

// readBundleFragment returns the original and distilled content of a fragment
// from a created bundle file, used by the distill assertions.
func readBundleFragment(w *World, bundle, fragment string) (content, distilled string, err error) {
	return readBundleItem(w, "fragments", bundle, fragment)
}

// readBundleCommand is readBundleFragment's counterpart for the commands
// section. Both kinds distill through the same seam, so a scenario that can
// only read one of them cannot tell "distillation works" from "distillation
// works for fragments" — which is exactly the regression `bundle distill`
// (every item at once) has to be able to fail on.
func readBundleCommand(w *World, bundle, command string) (content, distilled string, err error) {
	return readBundleItem(w, "commands", bundle, command)
}

// readAuthoredBundle reads the authored bundle name back through the
// production tree reader, so an assertion sees exactly what ctxloom sees.
func readAuthoredBundle(w *World, name string) (*bundles.Bundle, error) {
	manifest := filepath.Join(w.env.ProjectDir, filepath.FromSlash(bundleFilePath(name)))
	b, err := bundles.ReadTreeAt(context.Background(), afero.NewOsFs(), manifest)
	if err != nil {
		return nil, fmt.Errorf("read bundle %q: %w", name, err)
	}
	return b, nil
}

// readBundleItem reads one named item out of an authored bundle's section.
// Section-agnostic on purpose: fragments and commands carry the same
// content/distilled pair (bundles.ItemBody).
func readBundleItem(w *World, section, bundle, name string) (content, distilled string, err error) {
	b, err := readAuthoredBundle(w, bundle)
	if err != nil {
		return "", "", err
	}
	var body bundles.ItemBody
	var ok bool
	switch section {
	case "fragments":
		var f bundles.BundleFragment
		f, ok = b.Fragments[name]
		body = f.ItemBody
	case "commands":
		var cmd bundles.BundleCommand
		cmd, ok = b.Commands[name]
		body = cmd.ItemBody
	default:
		return "", "", fmt.Errorf("bundle section %q is not one this reader knows (fragments, commands)", section)
	}
	if !ok {
		return "", "", fmt.Errorf("%s %q not in bundle %q's %s", strings.TrimSuffix(section, "s"), name, bundle, section)
	}
	return body.Content, body.Distilled, nil
}
