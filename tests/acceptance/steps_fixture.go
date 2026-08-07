//go:build acceptance

package acceptance

import (
	"context"
	"fmt"
	"strings"

	"github.com/cucumber/godog"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

// minimalConfig is the smallest project config the CLI and MCP server need to
// resolve a project root and operate fault-tolerantly. Feature-specific fixtures
// layer bundles, profiles, and LLM config on top via the CLI itself, so the
// fixtures exercise real create paths rather than hand-written YAML.
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
//
// editor.command used to live HERE, pinned to a no-op so `edit` commands run
// non-interactively. It moved to minimalHomeEditorConfig (written via
// writeMinimalConfig): editor.command/args are ScopeMachine
// (internal/config/layerscope) — a binary on THIS box — so a committed
// project-file value no longer survives a real Load.
const minimalConfig = "version: 4\n"

// minimalHomeEditorConfig is minimalConfig's HOME half: the no-op editor
// pin. See minimalConfig's doc and writeMinimalConfig.
const minimalHomeEditorConfig = "version: 4\neditor:\n  command: \"true\"\n"

// writeMinimalConfig writes minimalConfig to the project layer and
// minimalHomeEditorConfig to home — every scenario that used to write
// minimalConfig alone as the whole project config.yaml should call this
// instead, so both halves land where they now belong.
func writeMinimalConfig(env *testenv.TestEnvironment) error {
	if err := env.WriteFile(".ctxloom/config.yaml", minimalConfig); err != nil {
		return err
	}
	return env.WriteHomeFile(".ctxloom/config.yaml", minimalHomeEditorConfig)
}

// markerEditorConfig pins the editor to a command that appends a fixed marker to
// the file it is given, so `edit` round-trips produce an observable change. sh
// receives `-c <script> editor <tmpfile>`, so "$1" is the temp file editInEditor
// writes the content to. The marker is free text, fine for fragment/prompt
// content (it would corrupt profile YAML, so those edits use other paths). See
// minimalConfig for why there is no legacy `profiles: defaults: []` key.
//
// Lives entirely in the HOME layer (see registerFixtureSteps' "marker
// editor" step, which writes minimalConfig to project and THIS to home) —
// editor.command/args are ScopeMachine, so a committed project value would
// not survive a real Load.
const markerEditorConfig = `version: 4
editor:
  command: sh
  args:
    - "-c"
    - 'printf "\nEDITED-BY-TEST\n" >> "$1"'
    - editor
`

// descriptionEditorConfig pins the editor to a command that REWRITES the
// document's `description:` line rather than appending free text, so the
// round-trip is observable on a file whose content must stay valid YAML —
// which is exactly a profile. markerEditorConfig's append would corrupt one
// (a bare scalar after a mapping), which is why `profile edit` had no
// observable effect to assert on and its scenario asserted nothing but the
// exit code.
//
// Lives entirely in the HOME layer — see markerEditorConfig's doc for why.
const descriptionEditorConfig = `version: 4
editor:
  command: sh
  args:
    - "-c"
    - 'sed "s/^description:.*/description: EDITED-BY-TEST/" "$1" > "$1.edited" && mv "$1.edited" "$1"'
    - editor
`

// commandEditorConfig pins the editor to a command that rewrites a `command:`
// line, which is what an MCP server entry is made of. markerEditorConfig's
// append would corrupt the YAML and descriptionEditorConfig rewrites the wrong
// key, so neither can serve `mcp server edit` — the round trip has to leave a
// valid manifest behind or the command reads back garbage.
//
// Lives entirely in the HOME layer — see markerEditorConfig's doc for why.
const commandEditorConfig = `version: 4
editor:
  command: sh
  args:
    - "-c"
    - 'sed "s/^\( *\)command:.*/\1command: EDITED-BY-TEST/" "$1" > "$1.edited" && mv "$1.edited" "$1"'
    - editor
`

// fixtureFragmentBody and fixtureCommandBody are the bodies the fragment and
// command fixtures seed over `create`'s placeholder.
//
// `fragment create` writes "# <name>\n\nAdd content here." — a body whose only
// distinctive token is the item NAME, echoed straight from the argument the
// scenario already passed. Every assertion that looked for that name on a
// show/list/resource/assembly surface was therefore satisfied by the echo
// alone: blanking the stored content left those scenarios green. A marker that
// appears NOWHERE except inside the item's stored bytes can only be asserted
// successfully if the real bytes travelled.
func fixtureFragmentBody(name string) string {
	return fmt.Sprintf("# %s\n\nFRAGMENT-BODY-%s: seeded by the acceptance fixture.\n", name, name)
}

func fixtureCommandBody(name string) string {
	return fmt.Sprintf("# %s\n\nCOMMAND-BODY-%s: seeded by the acceptance fixture.\n", name, name)
}

// seedItemContent replaces one item's `content:` inside a single-file bundle
// YAML. The fixture still CREATES the item through the real CLI path, so
// creation stays exercised end to end; this only overwrites the placeholder
// body, because no CLI or MCP surface can set an item's content at creation
// time (operations.AddItem's Content field has exactly one caller, and it
// hard-codes the placeholder).
func seedItemContent(w *World, bundle, section, name, content string) error {
	rel := ".ctxloom/content/bundles/" + bundle + ".yaml"
	body, err := w.env.ReadFile(rel)
	if err != nil {
		return fmt.Errorf("seed %s %q: read bundle %q: %w", section, name, bundle, err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
		return fmt.Errorf("seed %s %q: parse bundle %q: %w", section, name, bundle, err)
	}
	sec, _ := doc[section].(map[string]any)
	if sec == nil {
		return fmt.Errorf("seed %s %q: bundle %q has no %q section", section, name, bundle, section)
	}
	item, _ := sec[name].(map[string]any)
	if item == nil {
		return fmt.Errorf("seed %s %q: bundle %q has no such entry", section, name, bundle)
	}
	item["content"] = content
	out, err := yaml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("seed %s %q: marshal bundle %q: %w", section, name, bundle, err)
	}
	return w.env.WriteFile(rel, string(out))
}

func registerFixtureSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^an initialized ctxloom project$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := w.env.InitGitRepo(); err != nil {
			return err
		}
		return writeMinimalConfig(w.env)
	})

	// An empty project directory is a git repo with no .ctxloom yet — the
	// starting point for `init`.
	ctx.Step(`^an empty project directory$`, func(c context.Context) error {
		return worldFrom(c).env.InitGitRepo()
	})

	// A project whose editor appends a fixed marker, so an `edit` round-trip is
	// observable (the change lands in the bundle file and across MCP).
	// markerEditorConfig lives in HOME (editor.command/args are ScopeMachine);
	// the project still needs a valid, versioned config.yaml of its own.
	ctx.Step(`^a ctxloom project with a marker editor$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := w.env.InitGitRepo(); err != nil {
			return err
		}
		if err := w.env.WriteFile(".ctxloom/config.yaml", minimalConfig); err != nil {
			return err
		}
		return w.env.WriteHomeFile(".ctxloom/config.yaml", markerEditorConfig)
	})

	// A project whose editor rewrites `description:` in place — the YAML-safe
	// round-trip an `edit` on a structured document (a profile) needs.
	// descriptionEditorConfig lives in HOME for the identical reason.
	ctx.Step(`^a ctxloom project with a command-rewriting editor$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := w.env.InitGitRepo(); err != nil {
			return err
		}
		if err := w.env.WriteFile(".ctxloom/config.yaml", minimalConfig); err != nil {
			return err
		}
		return w.env.WriteHomeFile(".ctxloom/config.yaml", commandEditorConfig)
	})

	ctx.Step(`^a ctxloom project with a description-rewriting editor$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := w.env.InitGitRepo(); err != nil {
			return err
		}
		if err := w.env.WriteFile(".ctxloom/config.yaml", minimalConfig); err != nil {
			return err
		}
		return w.env.WriteHomeFile(".ctxloom/config.yaml", descriptionEditorConfig)
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
		if err := runFixture(c, "fragment", "create", bundle, frag); err != nil {
			return err
		}
		return seedItemContent(worldFrom(c), bundle, "fragments", frag, fixtureFragmentBody(frag))
	})

	ctx.Step(`^a command "([^"]*)" in bundle "([^"]*)" exists$`, func(c context.Context, command, bundle string) error {
		if err := runFixture(c, "command", "create", bundle, command); err != nil {
			return err
		}
		return seedItemContent(worldFrom(c), bundle, "commands", command, fixtureCommandBody(command))
	})

	// A bundle whose only content is the well-known `tooling` command
	// operations.CollectTooling looks for, carrying a caller-chosen marker so a
	// scenario can tell "the trust gate withheld this declaration" apart from
	// "collection returned nothing at all".
	ctx.Step(`^a bundle "([^"]*)" declaring container tooling "([^"]*)"$`, func(c context.Context, name, marker string) error {
		body := fmt.Sprintf("version: 1.0.0\n"+
			"description: declares agent-image tooling\n"+
			"commands:\n"+
			"  tooling:\n"+
			"    description: tools this bundle's content needs in the agent image\n"+
			"    content: |\n"+
			"      %s: install the tools this bundle's content needs.\n", marker)
		return worldFrom(c).env.WriteFile(".ctxloom/content/bundles/"+name+".yaml", body)
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

	// A directory profile carrying deny_tools, written directly (no CLI surface
	// sets deny_tools at creation time). This is the launch-flow's T2 regression
	// fixture: deny_tools/skills were silently dropped crossing
	// internal/lm/grpc's proto wire (fixed at 40b49a7f/48eedc61), and this step
	// plus "the mock recorded input contains" is what lets a scenario prove the
	// field survives ctxloom run end to end, not just the unit-level proto
	// round-trip.
	ctx.Step(`^a profile "([^"]*)" with bundle "([^"]*)" and deny_tools "([^"]*)"$`, func(c context.Context, name, bundle, tool string) error {
		body := "description: acceptance fixture profile\nbundles:\n  - " + bundle + "\ndeny_tools:\n  - " + tool + "\n"
		return worldFrom(c).env.WriteFile(".ctxloom/profiles/"+name+".yaml", body)
	})

	// An INLINE profile, written straight into config.yaml's `profiles:
	// definitions:` map — as opposed to `profile create`, which always writes a
	// directory profile (.ctxloom/profiles/<name>.yaml). `ctxloom config
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
				"commands:\n  demo-skill:\n    description: demo prompt\n    content: |\n      Demo prompt content.\n",
		})
		if err != nil {
			return fmt.Errorf("seed remote: %w", err)
		}
		if w.remoteBare == nil {
			w.remoteBare = map[string]string{}
		}
		w.remoteBare[name] = strings.TrimPrefix(url, "file://")
		_ = w.env.Run("remote", "create", name, url, "--forge", "git")
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
				"commands:\n  demo-skill:\n    description: demo prompt\n    content: |\n      Demo prompt content.\n",
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

	// Asserts against the mock backend's RECORDED SETUP INPUT (what actually
	// crossed the launch wire and reached Setup/Execute), not the CLI's stdout —
	// this is what makes it possible for a scenario to prove a field like
	// deny_tools survived the whole tip-to-tail flow rather than merely that the
	// command exited 0.
	ctx.Step(`^the mock recorded input contains "([^"]*)"$`, func(c context.Context, marker string) error {
		w := worldFrom(c)
		if w.mock == nil {
			return fmt.Errorf(`no mock LLM configured for this scenario (missing a "the mock LLM responds" step)`)
		}
		recorded, err := w.mock.GetRecordedInput()
		if err != nil {
			return fmt.Errorf("read mock recorded input: %w", err)
		}
		if !strings.Contains(recorded, marker) {
			return fmt.Errorf("mock recorded input does not contain %q; recorded:\n%s", marker, recorded)
		}
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
