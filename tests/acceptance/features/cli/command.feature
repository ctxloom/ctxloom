@doc
Feature: command — authoring reusable prompt templates for AI coding assistants

  A "command" is a user-invoked slash template: something a person or an
  assistant explicitly runs by name, as opposed to a fragment (context an
  assistant just has, unasked) or a skill (something an engine discovers and
  loads on its own by progressive disclosure). `ctxloom command` is the noun
  that owns creating, reading, editing, distilling, and deleting these
  templates inside a bundle.

  This is the comprehensive per-noun spec: what the noun DOES, leaf by leaf,
  including the refusals and the shapes that only matter to a machine. The
  narrative version — Carol authoring a team standard once and it reaching
  every teammate's assistant untouched — is
  journeys/j000700_team_authoring.feature, which asserts what a PERSON sees.

  # WHERE THE BOUNDARY FALLS, and it is not where it first looks. WHICH
  # engine-native file a materialized command lands in (.claude/commands/,
  # .codex/prompts/, a SKILL.md package for the engines with no slash-command
  # idiom of their own) is per-engine knowledge this noun owns by SUBJECT even
  # though the command that writes it is `profile materialize` — but that
  # five-engine matrix already lives in journeys/j000400_multi_engine.feature,
  # and cli/manage.feature's own "shipped commands in its own idiom" scenario
  # covers the built-in discover command landing correctly across every
  # engine. A claude-only copy of that matrix here would be strictly weaker
  # than either and is deliberately not built — see the pairing brief's
  # decision 7a.

  Rule: Creating a command writes real content, and it reaches every surface

    # Same hazard as fragment.feature's create scenario: "review" is the
    # argument the command line passed, and it is the YAML key, the listed
    # name, and the resource entry alike — so `Content: ""` in cli.createItem
    # stored no body at all and every axis stayed green regardless.
    # "Add content here." is the placeholder body create actually writes, and
    # it is the only string here that proves real bytes landed rather than a
    # name being echoed back three times.
    Scenario: Alice creates a command and it is listed and exposed to the agent
      Given an initialized ctxloom project
      And a bundle "demo" exists
      When Alice creates a command for her team:
        """
        ctxloom command create demo review
        """
      Then the command succeeds
      And the file ".ctxloom/content/bundles/demo.yaml" contains "review"
      And the file ".ctxloom/content/bundles/demo.yaml" contains "Add content here."
      When I run "ctxloom command list"
      Then the output contains "review"
      When the agent reads resource "ctxloom://commands"
      Then the resource contains "review"

  Rule: A command's content reaches a person and an agent byte for byte

    # `show` prints the item's name as a header, echoed straight from the
    # argument the scenario already passed — so a body of zero bytes would
    # still satisfy "the output contains review" on the name alone. The
    # fixture's marker exists nowhere in a fresh project except inside the
    # command's own stored bytes, so only the real content can satisfy it.
    Scenario: Showing a command's content over the CLI
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a command "review" in bundle "demo" exists
      When Alice reads it herself:
        """
        ctxloom command show demo#commands/review
        """
      Then the command succeeds
      And the output contains "review"
      And the output contains "COMMAND-BODY-review"

    # The MIME type is a static field on the envelope: every MCP resource
    # returning an EMPTY body left this green on the type alone. The body is
    # the resource.
    Scenario: Reading a single command over MCP
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a command "review" in bundle "demo" exists
      When the agent reads resource "ctxloom://commands/review"
      Then the resource MIME type is "text/markdown"
      And the resource contains "COMMAND-BODY-review"

  Rule: Deleting a command removes it from the manifest and from every listing

    # Both halves are asserted on payload, separately: a delete that only
    # trimmed the cached listing (leaving the YAML entry behind) and a delete
    # that only trimmed the YAML (leaving a stale listing) would each satisfy
    # exactly one of these three checks alone.
    Scenario: Deleting a command removes it from the manifest, the listing, and the agent's resource
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a command "review" in bundle "demo" exists
      When Alice removes it:
        """
        ctxloom command delete demo#commands/review
        """
      Then the command succeeds
      And the file ".ctxloom/content/bundles/demo.yaml" does not contain "COMMAND-BODY-review"
      When I run "ctxloom command list"
      Then the output does not contain "review"
      When the agent reads resource "ctxloom://commands"
      Then the resource does not contain "review"

  Rule: --no-distill controls whether an edit gets re-distilled, honestly

    # --no-distill is a per-invocation opt-out, distinct from the item's own
    # persistent no_distill field: it skips calling the LLM for THIS edit and
    # leaves the distilled form EMPTY rather than stale (SetItemContent clears
    # Distilled/DistilledBy/ContentHash the instant raw content changes,
    # before it even decides whether to redistill). A warning is the only
    # place that fact is visible to the author — a silent skip would read
    # identically to "already up to date and nothing to do".
    Scenario: Editing with --no-distill warns instead of leaving a stale distillation unmentioned
      Given a ctxloom project with a marker editor
      And a bundle "demo" exists
      And a command "review" in bundle "demo" exists
      When Alice edits it without redistilling:
        """
        ctxloom command edit demo#commands/review --no-distill
        """
      Then the command succeeds
      And the output contains "distilled form not refreshed (--no-distill)"
      And the file ".ctxloom/content/bundles/demo.yaml" contains "EDITED-BY-TEST"

    # editInEditor returns whatever bytes are on disk with no length check, so
    # a crashed editor, a truncated write, or a wrapper script that never
    # actually saved used to replace the command's ENTIRE body with nothing
    # and still print "Updated command ...". An intentional blank is not a
    # thing anyone does through this path; losing a command's content to a
    # wrapper that silently failed very much is.
    Scenario: An editor that returns nothing is refused rather than emptying the command
      Given a ctxloom project with an editor that empties the file
      And a bundle "demo" exists
      And a command "review" in bundle "demo" exists
      When she edits it and the editor comes back empty:
        """
        ctxloom command edit demo#commands/review
        """
      Then the command fails
      And the output contains "refusing to overwrite"
      And the file ".ctxloom/content/bundles/demo.yaml" contains "COMMAND-BODY-review"

  Rule: A command already marked no_distill is reported, not silently skipped

    # no_distill is checked BEFORE the distiller-availability check, so this
    # answer is the same whether or not an LLM is even configured — the
    # author's own declaration wins first, and distill says so rather than
    # exiting 0 with no explanation of which of the two reasons applied.
    Scenario: Distilling a command marked no_distill reports why it did nothing
      Given an initialized ctxloom project
      And the project already has the file ".ctxloom/content/bundles/demo.yaml":
        """
        version: "1.0.0"
        commands:
          review:
            no_distill: true
            content: "Review guidance that is never auto-summarized."
        """
      When Alice tries to distill it anyway:
        """
        ctxloom command distill demo#commands/review
        """
      Then the command succeeds
      And the output contains "is marked as no_distill"

  Rule: A --bundle filter that names nothing is refused, not answered empty

    # The UNFILTERED listing is non-empty whenever any command exists
    # anywhere, so a --bundle typo used to fall through to "Commands (0):" —
    # a message indistinguishable from a bundle that legitimately holds none.
    Scenario: Listing commands with a misspelled --bundle is refused, not answered empty
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a command "review" in bundle "demo" exists
      When I run "ctxloom command list --bundle nosuchbundle"
      Then the command fails
      And the output contains "no bundle named"
      And the output contains "nosuchbundle"
