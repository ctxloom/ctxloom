//go:build acceptance

package acceptance

import (
	"context"
	"fmt"
	"strings"

	"github.com/cucumber/godog"
)

// minimalConfig is the smallest project config the CLI and MCP server need to
// resolve a project root and operate fault-tolerantly. Feature-specific fixtures
// layer bundles, profiles, and LLM config on top via the CLI itself, so the
// fixtures exercise real create paths rather than hand-written YAML. The editor
// is pinned to a no-op so `edit` commands run non-interactively.
const minimalConfig = "version: 3\neditor:\n  command: \"true\"\nprofiles:\n  defaults: []\n"

// markerEditorConfig pins the editor to a command that appends a fixed marker to
// the file it is given, so `edit` round-trips produce an observable change. sh
// receives `-c <script> editor <tmpfile>`, so "$1" is the temp file editInEditor
// writes the content to. The marker is free text, fine for fragment/prompt
// content (it would corrupt profile YAML, so those edits use other paths).
const markerEditorConfig = `version: 3
editor:
  command: sh
  args:
    - "-c"
    - 'printf "\nEDITED-BY-TEST\n" >> "$1"'
    - editor
profiles:
  defaults: []
`

func registerFixtureSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^an initialized ctxloom project$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := w.env.InitGitRepo(); err != nil {
			return err
		}
		return w.env.WriteFile(".ctxloom/config.yaml", minimalConfig)
	})

	// An empty project directory is a git repo with no .ctxloom yet — the
	// starting point for `init`.
	ctx.Step(`^an empty project directory$`, func(c context.Context) error {
		return worldFrom(c).env.InitGitRepo()
	})

	// A project whose editor appends a fixed marker, so an `edit` round-trip is
	// observable (the change lands in the bundle file and across MCP).
	ctx.Step(`^a ctxloom project with a marker editor$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := w.env.InitGitRepo(); err != nil {
			return err
		}
		return w.env.WriteFile(".ctxloom/config.yaml", markerEditorConfig)
	})

	// A malformed config exercises fault tolerance: ctxloom must warn and fall
	// back to defaults rather than crash.
	ctx.Step(`^a malformed ctxloom config$`, func(c context.Context) error {
		return worldFrom(c).env.WriteFile(".ctxloom/config.yaml", "::: not: [valid yaml\n\tbroken")
	})

	// A profile whose bundle does not exist, written directly so creation-time
	// validation doesn't reject it — the point is that USING it degrades.
	ctx.Step(`^a profile "([^"]*)" referencing a missing bundle$`, func(c context.Context, name string) error {
		body := "description: references a missing bundle\nbundles:\n  - does-not-exist\n"
		return worldFrom(c).env.WriteFile(".ctxloom/profiles/"+name+".yaml", body)
	})

	ctx.Step(`^a bundle "([^"]*)" exists$`, func(c context.Context, name string) error {
		return runFixture(c, "bundle", "create", name, "-d", "acceptance fixture bundle")
	})

	ctx.Step(`^a fragment "([^"]*)" in bundle "([^"]*)" exists$`, func(c context.Context, frag, bundle string) error {
		return runFixture(c, "fragment", "create", bundle, frag)
	})

	ctx.Step(`^a prompt "([^"]*)" in bundle "([^"]*)" exists$`, func(c context.Context, prompt, bundle string) error {
		return runFixture(c, "prompt", "create", bundle, prompt)
	})

	// A profile requires at least one bundle or parent. The fixture creates a
	// dedicated base bundle so the profile is self-contained.
	ctx.Step(`^a profile "([^"]*)" exists$`, func(c context.Context, name string) error {
		if err := runFixture(c, "bundle", "create", name+"-base", "-d", "profile base"); err != nil {
			return err
		}
		return runFixture(c, "profile", "create", name, "-b", name+"-base", "-d", "acceptance fixture profile")
	})

	ctx.Step(`^a profile "([^"]*)" with bundle "([^"]*)"$`, func(c context.Context, name, bundle string) error {
		return runFixture(c, "profile", "create", name, "-b", bundle, "-d", "acceptance fixture profile")
	})

	// A recorded session seeds the home session index so session list/show/
	// rename/forget can be exercised without launching a backend. project_dir is
	// the live project so current-project listing finds it too.
	ctx.Step(`^a recorded session "([^"]*)"$`, func(c context.Context, harp string) error {
		w := worldFrom(c)
		entry := fmt.Sprintf("sessions:\n"+
			"  - harp_name: %s\n"+
			"    session_id: seeded-%s\n"+
			"    backend: claude-code\n"+
			"    project_dir: %s\n"+
			"    started_at: 2026-01-01T00:00:00Z\n"+
			"    transcript_path: \"\"\n"+
			"    summary: seeded acceptance session\n", harp, harp, w.env.ProjectDir)
		if err := w.env.WriteHomeFile(".ctxloom/sessions/index.yaml", entry); err != nil {
			return err
		}
		essence := fmt.Sprintf("---\nharp_name: %s\ndistilled_at: 2026-01-01T00:00:00Z\n---\n\nSeeded essence for %s.\n", harp, harp)
		return w.env.WriteHomeFile(".ctxloom/sessions/"+harp+"/essence.md", essence)
	})

	// A git remote serving a ctxloom layout over file://, registered with the
	// generic git forge. Exercises the real clone/fetch path hermetically.
	ctx.Step(`^a git remote "([^"]*)" serving a ctxloom bundle$`, func(c context.Context, name string) error {
		w := worldFrom(c)
		url, err := w.env.SeedRemote(map[string]string{
			"ctxloom/bundles/demo.yaml": "version: 1.0.0\n" +
				"author: test\n" +
				"description: Demo bundle\n" +
				"fragments:\n  demo-frag:\n    tags: [demo]\n    content: |\n      Demo fragment content.\n" +
				"prompts:\n  demo-prompt:\n    description: demo prompt\n    content: |\n      Demo prompt content.\n",
			"ctxloom/profiles/base.yaml": "description: Base profile\nbundles:\n  - demo\n",
		})
		if err != nil {
			return fmt.Errorf("seed remote: %w", err)
		}
		if w.remoteBare == nil {
			w.remoteBare = map[string]string{}
		}
		w.remoteBare[name] = strings.TrimPrefix(url, "file://")
		_ = w.env.Run("remote", "add", name, url, "--forge", "git")
		if w.env.LastExitCode() != 0 {
			return fmt.Errorf("remote add failed: %s", w.env.LastOutput())
		}
		return nil
	})

	// Advance a seeded remote with a second commit that changes the demo bundle,
	// so the next `remote sync` detects the change and stages it for review.
	ctx.Step(`^the remote "([^"]*)" advances its bundle$`, func(c context.Context, name string) error {
		w := worldFrom(c)
		bare := w.remoteBare[name]
		if bare == "" {
			return fmt.Errorf("remote %q was not seeded", name)
		}
		return w.env.AdvanceRemote(bare, map[string]string{
			"ctxloom/bundles/demo.yaml": "version: 2.0.0\n" +
				"author: test\n" +
				"description: Demo bundle v2\n" +
				"fragments:\n  demo-frag:\n    tags: [demo]\n    content: |\n      Demo fragment content, version two.\n",
		})
	})

	ctx.Step(`^the mock LLM responds "([^"]*)"$`, func(c context.Context, response string) error {
		w := worldFrom(c)
		mock, err := w.env.SetupMockLM()
		if err != nil {
			return fmt.Errorf("setup mock LLM: %w", err)
		}
		if err := mock.SetResponse(response); err != nil {
			return fmt.Errorf("set mock response: %w", err)
		}
		w.mock = mock
		return nil
	})
}

// runFixture executes a setup command and fails the scenario if it does not
// succeed — a broken fixture must not masquerade as a passing scenario.
func runFixture(c context.Context, args ...string) error {
	w := worldFrom(c)
	_ = w.env.Run(args...)
	if code := w.env.LastExitCode(); code != 0 {
		return fmt.Errorf("fixture %v failed (exit %d): %s", args, code, w.env.LastOutput())
	}
	return nil
}
