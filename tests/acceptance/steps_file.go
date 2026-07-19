//go:build acceptance

package acceptance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"
	"gopkg.in/yaml.v3"
)

func registerFileSteps(ctx *godog.ScenarioContext) {
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
// from a created bundle file, used by @live distill assertions. Bundles live at
// .ctxloom/content/bundles/<bundle>.yaml.
func readBundleFragment(w *World, bundle, fragment string) (content, distilled string, err error) {
	rel := filepath.Join(".ctxloom", "content", "bundles", bundle+".yaml")
	body, err := os.ReadFile(filepath.Join(w.env.ProjectDir, rel))
	if err != nil {
		return "", "", fmt.Errorf("read bundle %q: %w", bundle, err)
	}
	var doc struct {
		Fragments map[string]struct {
			Content   string `yaml:"content"`
			Distilled string `yaml:"distilled"`
		} `yaml:"fragments"`
	}
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return "", "", fmt.Errorf("parse bundle %q: %w", bundle, err)
	}
	f, ok := doc.Fragments[fragment]
	if !ok {
		return "", "", fmt.Errorf("fragment %q not in bundle %q", fragment, bundle)
	}
	return f.Content, f.Distilled, nil
}
