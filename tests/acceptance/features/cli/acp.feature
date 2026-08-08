@doc
Feature: acp — the editor door, and the entries an editor is told to paste

  `ctxloom acp` is the noun that owns the Agent Client Protocol surface: the
  two directions ctxloom can speak it in, and the agent-server entries a user
  copies into their editor's configuration to open the door at all.

  This is the comprehensive per-noun spec. The narrative version — Dana, who
  lives in her editor and will not adopt a terminal workflow — is
  journeys/j5_editor.feature, which asserts what a PERSON sees.

  # WHAT THIS SPEC DOES NOT REACH, said plainly rather than left to be
  # discovered. `acp serve` (ctxloom serving the protocol on stdio for an
  # editor to connect INTO) and `acp client` (ctxloom connecting OUT to a
  # third-party ACP agent) are both on completeness_test.go's
  # allowlisted-uncovered list and STAY there. Driving either needs an ACP
  # client harness speaking JSON-RPC over stdio, which does not exist in
  # testenv — that is its own build, tracked by taskloom agile-satin, not
  # something to fold in here quietly. Everything below is `acp list` and the
  # bare group node.

  Background:
    Given an initialized ctxloom project

  Rule: The listing always advertises ctxloom itself, plus one entry per binding

    An editor needs a command to run. `acp list` answers with the entries to
    paste, and there is always at least one: ctxloom's own `acp serve`. Each
    agent binding the project defines advertises as its OWN entry, because an
    editor picks an agent by picking an entry.

    # A project with no bindings is the state every project starts in, and the
    # listing has to be USEFUL there rather than empty — so it advertises the
    # base entry and says how to get more. Asserting the guidance text is what
    # stops this from being satisfied by a bare "entries (1):" header.
    Scenario: A project with no agent bindings still advertises ctxloom itself
      When Alice asks which entries her editor should be given:
        """
        ctxloom acp list
        """
      Then the command succeeds
      And the output contains "ACP agent-server entries (1):"
      And the output contains "No agent bindings defined"
      And the output contains ".ctxloom/agents/"

    # The claim is that a binding BECOMES an entry — name, engine and curated
    # profiles all carried through. Each is asserted separately: a listing that
    # printed the name and dropped the engine would leave a user pasting an
    # entry that binds nothing, and would still satisfy a name-only assertion.
    Scenario: Each agent binding advertises as its own entry, carrying its engine and profiles
      Given a bundle "house" exists
      And a profile "editorial" with bundle "house"
      When Alice defines an agent bound to that profile:
        """
        ctxloom agent create scribe --engine claude-code --profiles editorial
        """
      And Alice asks which entries her editor should be given:
        """
        ctxloom acp list
        """
      Then the command succeeds
      And the output contains "ACP agent-server entries (2):"
      And the output contains "scribe"
      And the output contains "engine: claude-code"
      And the output contains "profiles: editorial"

  Rule: What gets pasted has to survive being pasted

    The listing ends in a block a user copies into an editor's JSON settings.
    Prose that describes the entries contains the same words and is a
    different artifact — and the difference only shows up when the paste makes
    an editor's config stop loading.

    # PARSEABILITY, NOT SUBSTRING. This asserts the block round-trips through a
    # JSON decoder. A renderer still naming every command and no longer
    # emitting JSON passes any grep and fails here, which is the whole point:
    # the same assertion lives in the journey for what Dana sees, and here for
    # the shape a machine consumes.
    # The fixture defines a binding ON PURPOSE. With only ctxloom's own base
    # entry the count assertion below is satisfied by any block carrying one
    # object, so a renderer that pasted just the first entry would pass —
    # measured, by mutating exactly that.
    Scenario: The pasteable block is valid JSON carrying every advertised entry
      Given a bundle "house" exists
      And a profile "editorial" with bundle "house"
      And Alice defines an agent bound to that profile:
        """
        ctxloom agent create scribe --engine claude-code --profiles editorial
        """
      When Alice asks which entries her editor should be given:
        """
        ctxloom acp list
        """
      Then the command succeeds
      And the pasteable editor block parses as JSON and names every advertised entry

    # The one capability warning ctxloom gives BEFORE a user commits to a
    # binding. It is unconditional on purpose — the point is telling someone
    # while they are choosing, not diagnosing it afterward — so it must appear
    # even in a project that has defined no generic-acp binding at all, which
    # is exactly the fixture here.
    Scenario: The listing warns what a generic ACP binding will not inherit
      When Alice asks which entries her editor should be given:
        """
        ctxloom acp list
        """
      Then the command succeeds
      And the output contains "no hooks fire and no session history is captured"

  Rule: The group node refuses what it cannot honour

    `ctxloom acp` on its own is a group, not a runnable command. A flag it
    accepts and discards teaches a user their invocation was right when it did
    nothing — the defect shape this project is named for.

    Scenario: A flag the bare command cannot honour is refused, not swallowed
      When Alice tries to bind an agent on the bare editor command:
        """
        ctxloom acp --agent scribe
        """
      Then the command fails
      And the output contains "unknown flag"
