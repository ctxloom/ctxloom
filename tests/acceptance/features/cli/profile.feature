@doc
Feature: profile — the composition that decides what an agent actually receives

  Covers: `ctxloom profile list` (and its `ls` alias), `profile show`, `profile
  create`, `profile edit`, `profile modify` (the attach/detach and
  include/exclude surface), `profile remove` (and its `rm`/`del` aliases),
  `profile export`, `profile import`, `profile materialize`, and the bare
  `ctxloom profile` form.

  A profile is a COMPOSITION. It owns no content of its own: it names bundles
  to draw from, parents to inherit from, and a mask of what to leave out, and
  the result is the context one agent starts with. `.ctxloom/profiles/<name>.yaml`
  is the whole of it — which is why every scenario below judges a profile by
  what it ASSEMBLES, not by what its file says it wants.

  THERE IS NO `profile attach` OR `profile include`. Composition is expressed
  as flags on two commands, and the vocabulary is worth learning once:

  | intent                      | how it is spelled                                   |
  | attach a bundle             | `profile create -b <bundle>` / `modify --add-bundle`|
  | detach a bundle             | `profile modify --remove-bundle <bundle>`           |
  | attach a parent             | `profile create --parent <p>` / `modify --add-parent`|
  | detach a parent             | `profile modify --remove-parent <p>`                |
  | mask inherited content out  | `profile modify --exclude-fragment` / `--exclude-mcp`|
  | stop masking it out         | `profile modify --include-fragment` / `--include-mcp`|

  The mask exists because a profile cannot DELETE what it inherits — the
  bundle belongs to whoever published it. `--exclude-*` is a veto applied at
  assembly, and `--include-*` lifts that veto; neither touches the bundle. So
  the only honest test of either is the assembled surface, and that is what
  every scenario in the mask rule reads.

  EXPORT AND MATERIALIZE ARE NOT THE SAME VERB, and confusing them is the
  everyday mistake this noun invites. `export` publishes the DEFINITION — the
  profile YAML, a few lines naming bundles — so another project can import it
  and resolve it against its own installation. `materialize` writes the
  ASSEMBLED SURFACE — CLAUDE.md, .mcp.json, .claude/commands — so a plain
  `claude`, a CI job, or a human can launch into that context with ctxloom out
  of the loop entirely. One is portable and inert; the other is local and
  ready to run. The scenario that pins the difference writes both from one
  profile and reads each other's payload out of neither.

  Which ENGINE each materialized surface is native to, and the per-engine
  matrix that proves one profile reaches all of them, is cli/fragment.feature
  and journeys/j000400_multi_engine.feature. This file owns the composition
  itself.

  Rule: A created profile is a real file, and every read surface agrees with it

    Scenario: Creating a profile writes its definition and it is listed back
      Given an initialized ctxloom project
      And a bundle "demo" exists
      When Alice names the context she wants to work in:
        """
        ctxloom profile create dev -b demo -d day-to-day-work
        """
      Then the command succeeds
      And the file ".ctxloom/profiles/dev.yaml" exists
      # The composition, not just the name: a create that wrote a profile with
      # an empty bundles list would satisfy a name check and assemble nothing.
      And the file ".ctxloom/profiles/dev.yaml" contains "- demo"
      And the file ".ctxloom/profiles/dev.yaml" contains "day-to-day-work"
      When I run "ctxloom profile list"
      Then the command succeeds
      And the output contains "Profiles (1):"
      And the output contains "dev"
      And the output contains "day-to-day-work"
      When the agent reads resource "ctxloom://profiles"
      Then the resource contains "dev"

    # A profile with nothing to draw on has nothing to compose, so creation
    # refuses rather than writing a file that would assemble to nothing later
    # — the silent no-op this project is prone to, moved forward to the one
    # moment a person can still fix it.
    Scenario: A profile that names neither a bundle nor a parent is refused
      Given an initialized ctxloom project
      And a bundle "demo" exists
      # The composed form first, so "no file was written" below is read in a
      # fixture where a create demonstrably DOES write one.
      When I run "ctxloom profile create dev -b demo"
      Then the command succeeds
      And the file ".ctxloom/profiles/dev.yaml" exists
      When I run "ctxloom profile create hollow"
      Then the command fails
      And the output contains "at least one parent (--parent) or bundle (-b) is required"
      And the file ".ctxloom/profiles/hollow.yaml" does not exist

    Scenario: Showing a profile reads its composition back
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a profile "dev" with bundle "demo"
      When Alice checks what the profile is composed of:
        """
        ctxloom profile show dev
        """
      Then the command succeeds
      And the output contains "Profile: dev"
      And the output contains "Bundles:"
      And the output contains "- demo"

    Scenario: Reading a single profile over MCP
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a profile "dev" with bundle "demo"
      When the agent reads resource "ctxloom://profiles/dev"
      Then the resource contains "demo"

    # The bare noun answers rather than teaches, through the same seam
    # `ctxloom remote` and `ctxloom deps` use. The `ls` alias is driven in the
    # same fixture, because an alias that reached a different path would be a
    # second listing nobody is testing.
    Scenario: Bare profile lists the profiles, and so does ls
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a profile "dev" with bundle "demo"
      When I run "ctxloom profile"
      Then the command succeeds
      And the output contains "dev"
      And the output does not contain "Available Commands:"
      When I run "ctxloom profile ls"
      Then the command succeeds
      And the output contains "dev"

  Rule: Composition is by attachment, and detaching takes the content back out

    A bundle or a parent is attached by name and detached by the same name.
    Neither change is observable in the file alone — a profile that lists a
    bundle it cannot resolve looks identical on disk to one that works — so
    every attachment here is read out of the ASSEMBLED context.

    # Both directions in one fixture, and each half read on payload. The
    # second bundle is what makes the detachment falsifiable: a
    # `--remove-bundle` that emptied the profile outright would satisfy the
    # "gone" assertion just as well as a correct one does.
    Scenario: A bundle is attached to a profile and detached again, and the assembled context follows
      Given an initialized ctxloom project
      And a bundle "house-style" exists
      And a fragment "house-rules" in bundle "house-style" exists
      And a bundle "demo" exists
      And a fragment "testing" in bundle "demo" exists
      And a profile "dev" with bundle "demo"
      When Alice attaches the team's house style to her profile:
        """
        ctxloom profile modify dev --add-bundle house-style
        """
      Then the command succeeds
      When I run "ctxloom profile materialize dev --target attached"
      Then the command succeeds
      And the file "attached/CLAUDE.md" contains "FRAGMENT-BODY-house-rules"
      And the file "attached/CLAUDE.md" contains "FRAGMENT-BODY-testing"
      When Alice detaches it again:
        """
        ctxloom profile modify dev --remove-bundle house-style
        """
      Then the command succeeds
      When I run "ctxloom profile materialize dev --target detached"
      Then the command succeeds
      And the file "detached/CLAUDE.md" does not contain "FRAGMENT-BODY-house-rules"
      And the file "detached/CLAUDE.md" contains "FRAGMENT-BODY-testing"

    # Inheritance is the other axis, and the one a team uses to keep a shared
    # base in one place. The child's OWN bundle is asserted throughout, so
    # "the parent's content is gone" is distinguishable from "the profile
    # stopped resolving at all" — which is what a broken detach looks like
    # from the outside.
    Scenario: A parent profile's content is inherited, and detaching the parent takes it away
      Given an initialized ctxloom project
      And a bundle "house-style" exists
      And a fragment "house-rules" in bundle "house-style" exists
      And a profile "base" with bundle "house-style"
      And a bundle "demo" exists
      And a fragment "testing" in bundle "demo" exists
      When Alice composes her profile on top of the team's base:
        """
        ctxloom profile create dev --parent base -b demo -d day-to-day-work
        """
      Then the command succeeds
      When I run "ctxloom profile show dev"
      Then the command succeeds
      And the output contains "Parents:"
      And the output contains "- base"
      When I run "ctxloom profile materialize dev --target inherited"
      Then the command succeeds
      And the file "inherited/CLAUDE.md" contains "FRAGMENT-BODY-house-rules"
      And the file "inherited/CLAUDE.md" contains "FRAGMENT-BODY-testing"
      When Alice stops inheriting from the base:
        """
        ctxloom profile modify dev --remove-parent base
        """
      Then the command succeeds
      When I run "ctxloom profile materialize dev --target standalone"
      Then the command succeeds
      And the file "standalone/CLAUDE.md" does not contain "FRAGMENT-BODY-house-rules"
      And the file "standalone/CLAUDE.md" contains "FRAGMENT-BODY-testing"

    # A parent that does not exist is a composition that can never assemble.
    # Refusing at creation is friction up front; accepting it would defer the
    # failure to every launch afterwards, where the person who typed the name
    # is no longer looking.
    Scenario: Inheriting from a profile that does not exist is refused at creation
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a profile "base" with bundle "demo"
      # A real parent composes, so the refusal below is about the NAME rather
      # than about `--parent` never having worked in this fixture.
      When I run "ctxloom profile create good --parent base -b demo"
      Then the command succeeds
      And the file ".ctxloom/profiles/good.yaml" exists
      When I run "ctxloom profile create dev --parent no-such-base -b demo"
      Then the command fails
      And the file ".ctxloom/profiles/dev.yaml" does not exist

    # `modify` with nothing to do must not read as a successful change.
    Scenario: A modify that was told to change nothing says so
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a profile "dev" with bundle "demo"
      When I run "ctxloom profile modify dev"
      Then the command succeeds
      And the output contains "No changes made."
      And the file ".ctxloom/profiles/dev.yaml" contains "- demo"

  Rule: The mask hides inherited content without deleting it

    A profile cannot remove an item from a bundle it does not own, so
    `--exclude-fragment` and `--exclude-mcp` are a VETO applied while the
    context is assembled. `--include-*` lifts the veto. The bundle is untouched
    throughout — which is why both scenarios end by lifting the mask and
    finding the content still there to come back.

    Scenario: A fragment is masked out of the assembled context and let back in
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a fragment "testing" in bundle "demo" exists
      And a profile "dev" with bundle "demo"
      # Established first: the fragment really does reach the surface, so the
      # absence asserted next is a mask working rather than a fixture that
      # never delivered anything.
      When I run "ctxloom profile materialize dev --target before"
      Then the command succeeds
      And the file "before/CLAUDE.md" contains "FRAGMENT-BODY-testing"
      When Alice masks the inherited fragment out of her own context:
        """
        ctxloom profile modify dev --exclude-fragment testing
        """
      Then the command succeeds
      And the file ".ctxloom/profiles/dev.yaml" contains "exclude_fragments"
      When I run "ctxloom profile materialize dev --target masked"
      Then the command succeeds
      And the file "masked/CLAUDE.md" does not contain "FRAGMENT-BODY-testing"
      # The rest of the bundle still arrives: the veto took one item, not the
      # attachment.
      And the file "masked/CLAUDE.md" contains "Add your content here."
      # And the bundle was never edited — lifting the mask brings the same
      # bytes straight back, which it could not do if the exclusion had
      # touched the manifest.
      When Alice lifts the mask:
        """
        ctxloom profile modify dev --include-fragment testing
        """
      Then the command succeeds
      When I run "ctxloom profile materialize dev --target restored"
      Then the command succeeds
      And the file "restored/CLAUDE.md" contains "FRAGMENT-BODY-testing"

    # The same veto over an EXECUTABLE surface, which is where it matters most:
    # an MCP server a profile excludes must not be launched for that profile.
    # Two servers are declared so the masked run still has a registry to read —
    # "the excluded server is absent" is asserted alongside "the other one is
    # still there", which a materialize that wrote no registry at all could
    # not satisfy.
    Scenario: An MCP server is masked out of the materialized registry and let back in
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And I run "ctxloom bundle edit demo --add-mcp tree-sitter --add-mcp thinker"
      And a profile "dev" with bundle "demo"
      When I run "ctxloom profile materialize dev --target before"
      Then the command succeeds
      And the file "before/.mcp.json" registers an MCP server named "tree-sitter"
      And the file "before/.mcp.json" registers an MCP server named "thinker"
      When Alice vetoes one of the servers her bundle ships:
        """
        ctxloom profile modify dev --exclude-mcp tree-sitter
        """
      Then the command succeeds
      When I run "ctxloom profile materialize dev --target masked"
      Then the command succeeds
      And the file "masked/.mcp.json" registers no MCP server named "tree-sitter"
      And the file "masked/.mcp.json" registers an MCP server named "thinker"
      When Alice lifts the veto:
        """
        ctxloom profile modify dev --include-mcp tree-sitter
        """
      Then the command succeeds
      When I run "ctxloom profile materialize dev --target restored"
      Then the command succeeds
      And the file "restored/.mcp.json" registers an MCP server named "tree-sitter"

  Rule: A profile is edited by flag or by editor, and both land in the file

    # A profile is a structured document, so the append-a-line marker editor
    # the item nouns use would leave invalid YAML behind — which is why this
    # scenario once had no observable effect to assert and settled for the exit
    # code, passing even against an edit path that returned immediately and did
    # nothing. The description-rewriting editor keeps the document valid, so
    # the round-trip is assertable on the file AND on the read-back surface.
    Scenario: Editing a profile in the configured editor round-trips into the file
      Given a ctxloom project with a description-rewriting editor
      And a bundle "demo" exists
      And a profile "dev" with bundle "demo"
      When Alice opens the profile in her editor:
        """
        ctxloom profile edit dev
        """
      Then the command succeeds
      And the output contains "Updated profile"
      And the file ".ctxloom/profiles/dev.yaml" contains "EDITED-BY-TEST"
      When I run "ctxloom profile show dev"
      Then the command succeeds
      And the output contains "EDITED-BY-TEST"

    Scenario: The description can be set by flag instead
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a profile "dev" with bundle "demo"
      And the file ".ctxloom/profiles/dev.yaml" contains "acceptance fixture profile"
      When Alice re-describes the profile without opening an editor:
        """
        ctxloom profile modify dev -d updated-desc
        """
      Then the command succeeds
      And the file ".ctxloom/profiles/dev.yaml" contains "updated-desc"
      And the file ".ctxloom/profiles/dev.yaml" does not contain "acceptance fixture profile"
      When I run "ctxloom profile show dev"
      Then the output contains "updated-desc"

  Rule: Export publishes the definition; materialize writes the runnable surface

    # Same shape as the bundle round-trip: a profile export that dropped the
    # bundles: list still imported as a profile named dev and still listed.
    # That list is the profile's entire content, so both ends assert it.
    #
    # `profile remove` is asserted on its EFFECT here (the file is gone) before
    # the re-import, not just on exit code: without that assertion this
    # scenario would pass just as happily against a `remove` that reported and
    # destroyed nothing, since the re-import would recreate the file either
    # way.
    Scenario: Export, remove, and re-import a profile
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a profile "dev" with bundle "demo"
      When Alice exports the profile definition to share it:
        """
        ctxloom profile export dev pexport
        """
      Then the command succeeds
      And the file "pexport/dev.yaml" contains "- demo"
      When I run "ctxloom profile remove dev --yes"
      Then the command succeeds
      And the file ".ctxloom/profiles/dev.yaml" does not exist
      When Alice imports it back:
        """
        ctxloom profile import pexport/dev.yaml -f
        """
      Then the command succeeds
      When I run "ctxloom profile list"
      Then the output contains "dev"
      When I run "ctxloom profile show dev"
      Then the command succeeds
      And the output contains "Bundles:"
      And the output contains "- demo"

    # The clobber guard, proven by what SURVIVES the refusal rather than by the
    # error text: a guard that printed the message and overwrote anyway would
    # pass an exit-code check and still have eaten the local edit. The forced
    # retry afterwards proves the guard was the only thing in the way.
    Scenario: Importing over an existing profile refuses, and the local edit survives
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a profile "dev" with bundle "demo"
      And I run "ctxloom profile export dev pexport"
      And I run "ctxloom profile modify dev -d LOCAL-EDIT-MARKER"
      When Alice imports the shared copy over her own:
        """
        ctxloom profile import pexport/dev.yaml
        """
      Then the command fails
      And the output contains "profile already exists"
      And the output contains "use --force to overwrite"
      And the file ".ctxloom/profiles/dev.yaml" contains "LOCAL-EDIT-MARKER"
      When Alice decides she meant it:
        """
        ctxloom profile import pexport/dev.yaml --force
        """
      Then the command succeeds
      And the file ".ctxloom/profiles/dev.yaml" does not contain "LOCAL-EDIT-MARKER"

    # THE DISTINCTION THE WHOLE RULE IS NAMED FOR, asserted by writing both
    # from one profile and reading each other's payload out of neither. An
    # export that quietly assembled, or a materialize that quietly copied the
    # definition, would each satisfy a file-exists check on both destinations
    # and be entirely wrong.
    Scenario: Export carries the definition and materialize carries the assembled context
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a fragment "testing" in bundle "demo" exists
      And a profile "dev" with bundle "demo"
      When Alice publishes the definition and stands up a runnable surface:
        """
        ctxloom profile export dev definition
        ctxloom profile materialize dev --target surface
        """
      Then the command succeeds
      # The definition NAMES the bundle — the `bundles:` key and the entry
      # under it — and carries none of its content.
      And the file "definition/dev.yaml" contains "bundles:"
      And the file "definition/dev.yaml" contains "- demo"
      And the file "definition/dev.yaml" does not contain "FRAGMENT-BODY-testing"
      # The surface carries the assembled content and is NOT the definition:
      # the fragment's body reaches the engine's own context file, the
      # bundle's command reaches the engine's own command directory, and the
      # `bundles:` key that identifies a definition appears nowhere in it.
      And the file "surface/CLAUDE.md" contains "FRAGMENT-BODY-testing"
      And the file "surface/.claude/commands/demo-example.md" contains "Example prompt content. Describe what this prompt does."
      And the file "surface/CLAUDE.md" does not contain "bundles:"

  Rule: Removal reports first and destroys only when told to

    # Bare `remove` is a preview: it must name what it would destroy AND leave
    # the profile in place. A guard that quietly destroyed anyway would still
    # pass a scenario that only checked exit code or the report text — the
    # file-exists check and the follow-up listing are what actually catch that.
    Scenario: Bare profile remove reports what would go and destroys nothing
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a profile "dev" with bundle "demo"
      When Alice asks what removing the profile would cost:
        """
        ctxloom profile remove dev
        """
      Then the command succeeds
      And the output contains "Would remove profile"
      And the output contains "1 bundle(s)"
      And the output contains "Nothing was removed. Re-run with --yes to apply:"
      And the output contains "ctxloom profile remove dev --yes"
      And the file ".ctxloom/profiles/dev.yaml" exists
      When I run "ctxloom profile list"
      Then the output contains "dev"

    Scenario: Removing a profile with --yes takes the file and the listing entry
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a profile "dev" with bundle "demo"
      When Alice applies the removal:
        """
        ctxloom profile remove dev --yes
        """
      Then the command succeeds
      And the file ".ctxloom/profiles/dev.yaml" does not exist
      When I run "ctxloom profile list"
      Then the output contains "No profiles defined."

    # The aliases are not decoration: `rm` is what a person's fingers type, and
    # an alias that reached a different code path — a different default, a
    # missing preview — would be a destroyer with no guard on it.
    Scenario: The rm alias previews and destroys exactly as remove does
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a profile "dev" with bundle "demo"
      When I run "ctxloom profile rm dev"
      Then the command succeeds
      And the output contains "Nothing was removed"
      And the file ".ctxloom/profiles/dev.yaml" exists
      When I run "ctxloom profile del dev --yes"
      Then the command succeeds
      And the file ".ctxloom/profiles/dev.yaml" does not exist
