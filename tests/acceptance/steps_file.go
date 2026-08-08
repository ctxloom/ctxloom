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

	"github.com/cucumber/godog"
	"gopkg.in/yaml.v3"
)

func registerFileSteps(ctx *godog.ScenarioContext) {
	// Seeds a file the user is taken to have authored by hand, so a scenario can
	// assert what a ctxloom rewrite does to content ctxloom did not write.
	ctx.Step(`^the project already has the file "([^"]*)":$`, func(c context.Context, rel string, body *godog.DocString) error {
		return worldFrom(c).env.WriteFile(rel, body.Content)
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
	// scenario means, matches whatever else happens to contain it — j19's
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
	// first (J16 worktree-task-store journey's critical payload assertion —
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
		now, err := snapshotProjectTree(w.env.ProjectDir)
		if err != nil {
			return fmt.Errorf("re-read project tree: %w", err)
		}
		var added, removed, modified []string
		for rel, sum := range now {
			switch before, ok := w.projectTree[rel]; {
			case !ok:
				added = append(added, rel)
			case before != sum:
				modified = append(modified, rel)
			}
		}
		for rel := range w.projectTree {
			if _, ok := now[rel]; !ok {
				removed = append(removed, rel)
			}
		}
		if len(added)+len(removed)+len(modified) == 0 {
			return nil
		}
		slices.Sort(added)
		slices.Sort(removed)
		slices.Sort(modified)
		return fmt.Errorf("the project tree changed: added %v, removed %v, modified %v", added, removed, modified)
	})
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
	return nil
}

// readBundleFragment returns the original and distilled content of a fragment
// from a created bundle file, used by the distill assertions. Bundles live at
// .ctxloom/content/bundles/<bundle>.yaml.
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

// readBundleItem reads one named item out of a bundle manifest's section.
// Section-agnostic on purpose: fragments and commands carry the same
// content/distilled pair, and two near-identical readers would drift the first
// time the manifest shape changed under one of them.
func readBundleItem(w *World, section, bundle, name string) (content, distilled string, err error) {
	rel := filepath.Join(".ctxloom", "content", "bundles", bundle+".yaml")
	body, err := os.ReadFile(filepath.Join(w.env.ProjectDir, rel))
	if err != nil {
		return "", "", fmt.Errorf("read bundle %q: %w", bundle, err)
	}
	type item struct {
		Content   string `yaml:"content"`
		Distilled string `yaml:"distilled"`
	}
	// Not map[string]map[string]item over the whole document: a manifest's
	// top-level also carries scalars (version, description), which that shape
	// fails to unmarshal.
	var doc struct {
		Fragments map[string]item `yaml:"fragments"`
		Commands  map[string]item `yaml:"commands"`
	}
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return "", "", fmt.Errorf("parse bundle %q: %w", bundle, err)
	}
	sections := map[string]map[string]item{"fragments": doc.Fragments, "commands": doc.Commands}
	items, known := sections[section]
	if !known {
		return "", "", fmt.Errorf("bundle section %q is not one this reader knows (fragments, commands)", section)
	}
	it, ok := items[name]
	if !ok {
		return "", "", fmt.Errorf("%s %q not in bundle %q's %s", strings.TrimSuffix(section, "s"), name, bundle, section)
	}
	return it.Content, it.Distilled, nil
}
