@doc
Feature: acp — the editor door, the agent ctxloom talks out to, and the entries an editor is told to paste

  `ctxloom acp` is the noun that owns the Agent Client Protocol surface: the
  two directions ctxloom can speak it in, and the agent-server entries a user
  copies into their editor's configuration to open the door at all.

  The surfaces this file covers are `ctxloom acp` (the bare group node),
  `ctxloom acp list` (the pasteable entries), `ctxloom acp serve` (ctxloom
  serving the protocol so an editor connects IN) and `ctxloom acp run`
  (ctxloom connecting OUT to an ACP-speaking agent — a session, or a single
  turn with --one-shot).

  This is the comprehensive per-noun spec. The narrative version — Dana, who
  lives in her editor and will not adopt a terminal workflow — is
  journeys/j000500_editor.feature, which asserts what a PERSON sees.

  # WHAT THIS SPEC DOES NOT REACH, said plainly rather than left to be
  # discovered. `acp serve` (ctxloom serving the protocol on stdio for an
  # editor to connect INTO) and `acp run` (ctxloom connecting OUT to a
  # third-party ACP agent) are both on completeness_test.go's
  # allowlisted-uncovered list and STAY there. Driving either needs an ACP
  # client harness speaking JSON-RPC over stdio, which does not exist in
  # testenv — that is its own build, tracked by taskloom agile-satin, not
  # something to fold in here quietly. Everything below is `acp list`, the
  # bare group node, and `acp serve`'s pre-flight --workspace/--agent
  # validation (Rule below) — the one piece of `acp serve` that resolves
  # and exits before the JSON-RPC loop would need a real client on the other
  # end: the refusal returns immediately, and the accepted case reaches
  # Serve() only to see stdin at EOF (no client attached) and exit.

  Background:
    Given an initialized ctxloom project

  Rule: The listing always advertises ctxloom itself, plus one entry per binding

    An editor needs a command to run. `acp list` answers with the entries to
    paste, and there is always at least one: ctxloom's own `acp serve`. Each
    agent binding the project defines advertises as its OWN entry, because an
    editor picks an agent by picking an entry.

    # ONE BODY, ONCE PER ENCODING. Off a terminal ctxloom derives JSON, so the
    # no-flag row is what an editor's own tooling receives; the explicit rows
    # prove a typed --format wins in both directions. The structured rows
    # address the entry BY PATH, which a listing that printed a header over an
    # empty array cannot satisfy.
    #
    # The count claim rides the text column: the payload is a bare array with
    # no length field, so each row states the fact its own encoding spells —
    # the header's "(1)" in prose, the entry's identity at $.0 in JSON.
    Scenario Outline: A project with no agent bindings still advertises ctxloom itself
      When Alice asks which entries her editor should be given:
        """
        ctxloom acp list <flags>
        """
      Then the command succeeds
      And the output reports "$.0.name" as "<the sole entry>"
      And the output reports "$.0.args" containing "<opens the door>"

      Examples: no --format at all takes the derived default off a terminal; an explicit one wins in both directions
        | flags         | the sole entry                | opens the door |
        |               | ctxloom                       | serve          |
        | --format json | ctxloom                       | serve          |
        | --format text | ACP agent-server entries (1): | acp serve      |

    # The guidance is a HUMAN-RENDERER concern and is pinned in the encoding
    # that has it. `acp list`'s structured payload is a list of entries with no
    # field for advice, so there is no path a JSON row could address — tabling
    # this would mean asserting nothing on two rows out of three.
    Scenario: The empty listing says how to get more entries
      When Alice asks which entries her editor should be given:
        """
        ctxloom acp list --format text
        """
      Then the command succeeds
      And the output contains "No agent bindings defined"
      And the output contains "'agents:' in .ctxloom/config.yaml"

    # The claim is that a binding BECOMES an entry — name, engine and curated
    # profiles all carried through. Each is asserted separately: a listing that
    # printed the name and dropped the engine would leave a user pasting an
    # entry that binds nothing, and would still satisfy a name-only assertion.
    Scenario Outline: Each agent binding advertises as its own entry, carrying its engine and profiles
      Given a bundle "house" exists
      And a profile "editorial" with bundle "house"
      When Alice defines an agent bound to that profile:
        """
        ctxloom agent create scribe --llm claude-code --profiles editorial
        """
      And Alice asks which entries her editor should be given:
        """
        ctxloom acp list <flags>
        """
      Then the command succeeds
      And the output reports "$.1.name" as "<a second entry appears>"
      And the output reports "$.1.agent" as "<names the binding>"
      And the output reports "$.1.llm" as "<carries its engine>"
      And the output reports "$.1.profiles" containing "<carries its profiles>"

      Examples: no --format at all takes the derived default off a terminal; an explicit one wins in both directions
        | flags         | a second entry appears        | names the binding | carries its engine | carries its profiles |
        |               | ctxloom: scribe               | scribe            | claude-code        | editorial            |
        | --format json | ctxloom: scribe               | scribe            | claude-code        | editorial            |
        | --format text | ACP agent-server entries (2): | scribe            | llm: claude-code   | profiles: editorial  |

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
    #
    # NOT TABLED BY FORMAT, and it is the one scenario here that cannot be:
    # the subject is the pasteable block EMBEDDED IN THE PROSE and its
    # agreement with the prose header's own count. `--format json` emits the
    # entries as the whole payload with no header and no embedded block, so
    # there is no second artifact to disagree with — the claim does not exist
    # in that encoding rather than being spelled differently there.
    Scenario: The pasteable block is valid JSON carrying every advertised entry
      Given a bundle "house" exists
      And a profile "editorial" with bundle "house"
      And Alice defines an agent bound to that profile:
        """
        ctxloom agent create scribe --llm claude-code --profiles editorial
        """
      When Alice asks which entries her editor should be given:
        """
        ctxloom acp list --format text
        """
      Then the command succeeds
      And the pasteable editor block parses as JSON and names every advertised entry

    # The one capability warning ctxloom gives BEFORE a user commits to a
    # binding. It is unconditional on purpose — the point is telling someone
    # while they are choosing, not diagnosing it afterward — so it must appear
    # even in a project that has defined no generic-acp binding at all, which
    # is exactly the fixture here.
    #
    # Pinned to the human rendering for the same reason as the guidance above:
    # the entry payload carries no field for a caveat, so a JSON row would
    # assert nothing.
    Scenario: The listing warns what a generic ACP binding will not inherit
      When Alice asks which entries her editor should be given:
        """
        ctxloom acp list --format text
        """
      Then the command succeeds
      And the output contains "no hooks fire and no session history is captured"


  # DRIVING AN ACP AGENT OUT HAS TWO FORMS, AND THEY ARE THE SAME TWO `run` HAS.
  #
  # Bare `acp run --llm <label>` opens a SESSION with the configured
  # ACP-speaking agent: one typed line is one turn, and Ctrl-D ends it. A
  # prompt on the command line opens that session with the first turn.
  # `--one-shot` drives a SINGLE turn instead — prompt in, response out, exit
  # — and reads its prompt from piped stdin when the command line carries
  # none. That is the same word, for the same mode, as `ctxloom run
  # --one-shot`: the two verbs mean the same thing and differ only in which
  # engine surface they drive, so a caller who learned the rule on one carries
  # it to the other.
  #
  # No scenario here drives either form, and the omission is deliberate rather
  # than an oversight. `acp run` is on completeness_test.go's
  # allowlisted-uncovered list precisely because this harness cannot reach its
  # behaviour at all (see the note at the top of this file): both forms end at
  # a live ACP agent on the other side of the plugin door. A scenario whose
  # whole content is a flag-validation error would CREDIT the leaf to the
  # coverage gate while proving nothing about what it does — the vacuous
  # coverage that gate exists to refuse. What keeps the two forms from
  # collapsing into each other is pinned in internal/cli instead, by counting
  # the TURNS each one takes and which engine door it took them through
  # (TestRunACPRun_TakesTheFormTheInvocationAsked,
  # TestRunACPSession_TakesEveryTypedTurnUntilStdinEnds,
  # TestRunACPOneshot_TakesExactlyOneTurnAndExits), until the ACP client
  # harness tracked by taskloom agile-satin makes the leaf itself reachable.

  Rule: --workspace on acp serve means nothing without --agent, so it is refused

    acpWorkspaceAxis silently returns the empty axis whenever no agent is
    named — worktree isolation is scoped to a specific agent entry, not the
    plain `acp serve` a bare editor connection uses. Left unchecked, a user
    who passes --workspace worktree with no --agent gets NO isolation and is
    told nothing, which is the worst failure direction for an isolation flag.
    So --workspace without --agent is refused outright, before the protocol
    loop even opens. The positive case is pinned in the same fixture: paired
    with --agent, the identical --workspace value is accepted rather than
    refused for some unrelated reason — the only way the refusal above can be
    trusted to be ABOUT the missing --agent.
    Scenario: --workspace requires --agent, and is accepted once one is named
      Given an initialized ctxloom project
      And a profile "dev" exists
      And I run "ctxloom agent create developer --llm claude-code --profiles dev"
      When I run "ctxloom acp serve --workspace worktree"
      Then the command fails
      And the output contains "--workspace requires --agent"
      When I run "ctxloom acp serve --workspace worktree --agent developer"
      Then the command succeeds

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
