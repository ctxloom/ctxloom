@doc
Feature: fragment — reusable context units, and the engine surface each one reaches

  A "fragment" is context an assistant just HAS, unasked — as opposed to a
  command (something a person or an assistant explicitly invokes by name) or
  a skill (something an engine discovers and loads on its own by progressive
  disclosure). `ctxloom fragment` is the noun that owns creating, reading,
  searching, and deleting these context units inside a bundle, and it is also
  the noun that owns proving a fragment actually LANDS: every engine reads its
  context from a different file, in a different shape, and a team that
  authors guidance once must see it reach each engine's own surface without
  anyone hand-translating it.

  This is the comprehensive per-noun spec: what the noun DOES, leaf by leaf,
  including the engine-surface matrix that only matters to a machine parsing
  a generated file. The narrative version — Carol's team writing one shared
  profile once and it reaching claude-code, codex, and kiro in
  their own native format — is journeys/j000400_multi_engine.feature, which
  asserts what a PERSON sees; MCP, hooks, and commands are that same
  journey's other three surfaces, and live in cli/mcp.feature,
  cli/manage.feature, and cli/command.feature respectively — this file owns
  only the context surface.

  Rule: A fragment authored once reaches every axis: manifest, listing, and agent resource

    # Every literal on the three axes below is "testing" — the argument the
    # command line just passed. It is the YAML key in the manifest, the name
    # in the listing, and the name in the resource, so a create that stored
    # ZERO BYTES of content left all three green (Content: "" in
    # cli.createItem): this project's characteristic silent no-op, on its
    # primary authoring verb. "Add content here." is the placeholder body
    # create actually writes, and it appears nowhere else in a freshly
    # created project.
    Scenario: Alice creates a fragment and it lands in the manifest, the listing, and the agent's resource
      Given an initialized ctxloom project
      And a bundle "demo" exists
      When Alice adds a fragment to the team's shared guidance:
        """
        ctxloom fragment create demo testing
        """
      Then the command succeeds
      And the file ".ctxloom/content/bundles/demo.yaml" contains "testing"
      And the file ".ctxloom/content/bundles/demo.yaml" contains "Add content here."
      When I run "ctxloom fragment list"
      Then the output contains "testing"
      When the agent reads resource "ctxloom://fragments"
      Then the resource contains "testing"

  Rule: A fragment's content reaches a person and an agent byte for byte, and is findable by name

    # The MIME type is a static field on the envelope: every MCP resource
    # returning an EMPTY body left this green on the type alone. The body is
    # the resource.
    Scenario: Reading a single fragment over MCP
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a fragment "testing" in bundle "demo" exists
      When the agent reads resource "ctxloom://fragments/testing"
      Then the resource MIME type is "text/markdown"
      And the resource contains "FRAGMENT-BODY-testing"

    Scenario: Search finds a fragment by name
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a fragment "testing" in bundle "demo" exists
      When I run "ctxloom search --local testing"
      Then the command succeeds
      And the output contains "testing"

  Rule: Removing a fragment removes it from the manifest and from every listing

    # Bare `remove` is a preview: it must leave the manifest untouched. A
    # guard that quietly destroyed anyway would still pass a scenario that
    # only checked exit code — the manifest-still-contains check is what
    # actually catches that.
    Scenario: Bare fragment remove reports and destroys nothing
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a fragment "testing" in bundle "demo" exists
      When I run "ctxloom fragment remove demo#fragments/testing"
      Then the command succeeds
      And the output contains "Nothing was removed"
      And the output contains "--yes"
      And the file ".ctxloom/content/bundles/demo.yaml" contains "FRAGMENT-BODY-testing"

    # Both halves are asserted on payload, separately: a remove that only
    # trimmed the cached listing (leaving the YAML entry behind) and a remove
    # that only trimmed the YAML (leaving a stale listing or a stale MCP
    # resource) would each satisfy exactly one of these three checks alone.
    Scenario: Removing a fragment removes it from the manifest, the listing, and the agent's resource
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a fragment "testing" in bundle "demo" exists
      When Alice removes it:
        """
        ctxloom fragment remove demo#fragments/testing --yes
        """
      Then the command succeeds
      And the file ".ctxloom/content/bundles/demo.yaml" does not contain "FRAGMENT-BODY-testing"
      When I run "ctxloom fragment list"
      Then the output does not contain "testing"
      When the agent reads resource "ctxloom://fragments"
      Then the resource does not contain "testing"

  Rule: A fragment authored once reaches every engine's own native context surface

    # The team writes guidance in one place; each engine reads it from the
    # file that engine actually looks at, without anyone hand-translating it.
    # Each row PARSES the generated file in its own format (plain markdown
    # for every engine here) and asserts the actual marker content lands
    # inside it — never a bare file-exists. codex is the interesting row:
    # AGENTS.md is its NATIVE route (managed-section markers, exactly like
    # claude's CLAUDE.md), but codex ALSO keeps a
    # second, separate context route — a content-addressed cache file a
    # SessionStart hook reads at run time, carrying a per-invocation content
    # hash the AGENTS.md route cannot provide. That second route is keyed on
    # resolved Fragment objects, which `profile materialize` never populates
    # (it only ever has the assembled context STRING) — so it is exercised by
    # the live run/launch path, not by materialization, and has no assertion
    # here; AGENTS.md is the surface a materialized profile actually proves.
    Scenario Outline: The same fragment materializes into each engine's own native context file
      Given Carol's team profile carries a shared fragment, command, MCP server, and hook
      When Alice materializes the team profile for <engine>
      Then the materialized <engine> context carries the shared fragment's marker, in its own native shape

      Examples:
        | engine      |
        | claude-code |
        | kiro        |
        | codex       |

  Rule: Materializing a profile never destroys a team's hand-authored context file

    # Regression coverage for a P0 data-loss defect: materializing a profile
    # for claude-code/codex must never destroy a team's hand-authored
    # CLAUDE.md / AGENTS.md — content outside ctxloom's managed markers must
    # survive byte-for-byte, and ctxloom's own content must still land
    # alongside it. Gutting the marker-merge core
    # (agent.WriteManagedContext, internal/shared/agent/managedcontext.go)
    # down to a bare whole-file write makes this fail for exactly that
    # reason: the hand-authored line is GONE, not merely unasserted.
    Scenario Outline: A hand-authored context file survives materialization byte-for-byte
      Given Carol's team profile carries a shared fragment, command, MCP server, and hook
      And Alice's team already hand-authored <file> for <engine> with their own conventions
      When Alice materializes the team profile for <engine>
      Then <file> still carries Alice's hand-authored conventions, byte-for-byte
      And the materialized <engine> context carries the shared fragment's marker, in its own native shape

      Examples:
        | engine      | file      |
        | claude-code | CLAUDE.md |
        | codex       | AGENTS.md |
