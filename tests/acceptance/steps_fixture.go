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
//
// Deliberately carries NO `profiles:` key: the old `profiles.defaults` (a bare
// seq) is the pre-v6 schema defaultAgentUpgrade migrates on load into a
// synthesized `agents.default` + `default_agent: default` (profiles.defaults was
// RETIRED — the default context is now whatever the always-bound default AGENT
// composes). A fixture carrying the legacy key would silently gain that
// synthesized agent the moment any command calls cfg.Save() — polluting
// `agent list` with an unwanted "default" entry scenarios never asked for, and
// tripping the schema-validation fatal finding `ctxloom run`'s strict startup
// gate enforces on a not-yet-upgraded file. Scenarios that need a default agent
// set one explicitly via `ctxloom agent default <name>`.
const minimalConfig = "version: 4\neditor:\n  command: \"true\"\n"

// markerEditorConfig pins the editor to a command that appends a fixed marker to
// the file it is given, so `edit` round-trips produce an observable change. sh
// receives `-c <script> editor <tmpfile>`, so "$1" is the temp file editInEditor
// writes the content to. The marker is free text, fine for fragment/prompt
// content (it would corrupt profile YAML, so those edits use other paths). See
// minimalConfig for why there is no legacy `profiles: defaults: []` key.
const markerEditorConfig = `version: 4
editor:
  command: sh
  args:
    - "-c"
    - 'printf "\nEDITED-BY-TEST\n" >> "$1"'
    - editor
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

	ctx.Step(`^a skill "([^"]*)" in bundle "([^"]*)" exists$`, func(c context.Context, skill, bundle string) error {
		return runFixture(c, "skill", "create", bundle, skill)
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

	// An INLINE profile, written straight into config.yaml's `profiles:
	// definitions:` map — as opposed to `profile create`, which always writes a
	// directory profile (.ctxloom/profiles/<name>.yaml). `ctxloom manage config
	// get profiles` only ever reflects this inline map (cfg.Profiles.Definitions):
	// there is no CLI surface that populates it, so a scenario asserting on that
	// section must seed it directly. Appends to the existing config.yaml — safe
	// because minimalConfig/markerEditorConfig carry no `profiles:` key of
	// their own.
	ctx.Step(`^a profile "([^"]*)" is defined inline in config with bundle "([^"]*)"$`, func(c context.Context, name, bundle string) error {
		w := worldFrom(c)
		existing, err := w.env.ReadFile(".ctxloom/config.yaml")
		if err != nil {
			return err
		}
		body := existing + fmt.Sprintf("profiles:\n  definitions:\n    %s:\n      bundles:\n        - %s\n", name, bundle)
		return w.env.WriteFile(".ctxloom/config.yaml", body)
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
			".ctxloom/content/bundles/demo.yaml": "version: 1.0.0\n" +
				"author: test\n" +
				"description: Demo bundle\n" +
				"fragments:\n  demo-frag:\n    tags: [demo]\n    content: |\n      Demo fragment content.\n" +
				"skills:\n  demo-skill:\n    description: demo prompt\n    content: |\n      Demo prompt content.\n",
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
			".ctxloom/content/bundles/demo.yaml": "version: 2.0.0\n" +
				"author: test\n" +
				"description: Demo bundle v2\n" +
				"fragments:\n  demo-frag:\n    tags: [demo]\n    content: |\n      Demo fragment content, version two.\n",
		})
	})

	// Publishes a new commit upstream with the named fragment's content replaced
	// by an arbitrary, caller-chosen string — the payload-testing counterpart to
	// "advances its bundle" above (which only ever moves to one fixed string).
	// Letting the scenario pick unrelated marker text on each side avoids any
	// accidental substring relationship between the "before" and "after"
	// content, which would make a "does not contain the old content" assertion
	// meaningless.
	ctx.Step(`^the remote "([^"]*)" changes fragment "([^"]*)" to "([^"]*)"$`, func(c context.Context, name, frag, content string) error {
		w := worldFrom(c)
		bare := w.remoteBare[name]
		if bare == "" {
			return fmt.Errorf("remote %q was not seeded", name)
		}
		return w.env.AdvanceRemote(bare, map[string]string{
			".ctxloom/content/bundles/demo.yaml": "version: 1.1.0\n" +
				"author: test\n" +
				"description: Demo bundle\n" +
				"fragments:\n  " + frag + ":\n    tags: [demo]\n    content: |\n      " + content + "\n" +
				"skills:\n  demo-skill:\n    description: demo prompt\n    content: |\n      Demo prompt content.\n",
		})
	})

	// Forces the project's local cache clone for a seeded remote back to its
	// very first commit — a stronger, deliberate version of the staleness a
	// clone's checked-out HEAD always carries by construction (fetch advances
	// refs/remotes/origin/* but never fast-forwards the local branch). Pins the
	// invariant that content is served by resolving refs/remotes/origin/<branch>,
	// never the clone's checked-out working tree/HEAD.
	ctx.Step(`^the remote "([^"]*)"'s cached clone is forced back to its first commit$`, func(c context.Context, name string) error {
		w := worldFrom(c)
		bare := w.remoteBare[name]
		if bare == "" {
			return fmt.Errorf("remote %q was not seeded", name)
		}
		clone, err := w.env.FindRepoCacheClone()
		if err != nil {
			return fmt.Errorf("locate cached clone for %q: %w", name, err)
		}
		return w.env.ResetCachedCloneToFirstCommit(clone)
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
