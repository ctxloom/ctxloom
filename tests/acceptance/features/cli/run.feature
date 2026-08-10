@doc
Feature: run — assembling a project's context and handing it to an engine

  `run` is the core operation, and the only one whose whole job is to put
  something in front of a model. It resolves which context this invocation
  gets — a profile, loose fragments, a tag selection, or a named agent binding
  — assembles it, and launches the configured engine with that context and a
  prompt.

  Surfaces covered here:

    ctxloom run [prompt...]              (interactive: a session)
    ctxloom run --one-shot [prompt...]   (one turn, then exit)
    ctxloom run --dry-run                (render what would be sent; send nothing)
    ctxloom run --profile/-p <name>
    ctxloom run --tag/-t <tag>
    ctxloom run --command/-r <name>      (a saved command as the prompt)
    ctxloom run --agent <name>

  A RUN IS EITHER A SESSION OR A SINGLE TURN, and `--one-shot` is how a caller
  says which. The flag names the MODE, not its output: every mode prints, and
  what actually differs is that a one-shot takes one turn and exits. Bare
  `ctxloom run` is the session — interactive, terminal-owning, and therefore
  not specifiable here; the pty-driven versions live in the journeys
  (j000200_setup.feature drives a real interactive launch over a pty).

  EVERY SCENARIO HERE DRIVES THE MOCK ENGINE, never a real one. That is not a
  compromise — it is what makes the full path OBSERVABLE. The mock records the
  setup payload that actually crossed the launch wire, so a scenario can assert
  what the model RECEIVED rather than that the command exited 0. `the mock
  recorded input contains` is the strongest assertion available on this
  surface, and the reason the deny_tools regression below is catchable at all:
  a per-hop test cannot see a field silently dropped crossing the proto wire,
  because the converter and its mirror struct read as a matched pair.

  Rule: The mode has one spelling, and asking for two at once is a refusal

    A caller reaching for a mode by a name it does not have must not get a run
    they did not ask for. An unknown flag is an error, and that error is what
    makes the real spelling discoverable; an alias quietly accepted alongside
    it would leave two names for one mode forever — in help, in scripts, and in
    every future reader's head.

    Scenario: A flag that is not the mode's name is refused rather than guessed at
      Given an initialized ctxloom project
      When Alice reaches for the mode by a name it does not have:
        """
        ctxloom run --print hello
        """
      Then the command fails
      And the output contains "unknown flag"

    # The two flags select DIFFERENT execution modes, so an invocation naming
    # both is a caller who has not decided. Resolving that by picking one
    # silently is the shape where an invocation reads as precise and runs as
    # something else.
    Scenario: Asking for both execution modes at once is refused
      Given an initialized ctxloom project
      When I run "ctxloom run --one-shot --structured hello"
      Then the command fails
      And the output contains "one-shot"

    # `--agent` names a BINDING that already carries its own composed profiles,
    # engine and runtime, so combining it with the flags that assemble context
    # by hand is a caller asking for two different answers to the same
    # question.
    Scenario: An agent binding and a hand-assembled profile cannot both decide the context
      Given an initialized ctxloom project
      And a profile "dev" exists
      When Alice names both a binding and a profile:
        """
        ctxloom run --agent developer --profile dev hello
        """
      Then the command fails
      And the output contains "none of the others can be"

  Rule: A one-shot takes exactly one turn, and refuses to take an empty one

    Scenario: Run hands the assembled context to the model and returns its reply
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a fragment "testing" in bundle "demo" exists
      And a profile "dev" with bundle "demo"
      And the mock LLM responds "MOCK-REPLY"
      When Alice asks a question with her project's context attached:
        """
        ctxloom run --one-shot --profile dev unicorn-prompt
        """
      Then the command succeeds
      And the output contains "MOCK-REPLY"
      And the mock recorded input contains "FRAGMENT-BODY-testing"

    # THE CHARACTERISTIC BUG, in its purest form. A one-shot gets exactly one
    # turn, so an empty prompt asks nothing at all: the run would exit 0, print
    # a reply to a question nobody posed, and `ctxloom run --one-shot > out.txt`
    # would leave a file whose emptiness looks like the model's answer. It is a
    # refusal instead, and the message names every door a prompt could have
    # arrived through so the caller knows which one they forgot.
    Scenario: A one-shot with no prompt from any door refuses to run
      Given an initialized ctxloom project
      And the mock LLM responds "MOCK-REPLY-NEVER-ASKED"
      When Alice starts a one-shot without asking anything:
        """
        ctxloom run --one-shot
        """
      Then the command fails
      And the output contains "nothing to run"
      And the output does not contain "MOCK-REPLY-NEVER-ASKED"

    # ONE-SHOT AS A UNIVERSAL REDUCER: with no prompt on the command line, a
    # one-shot reads it from piped stdin, so anything a shell can produce can
    # be summarized, classified or rewritten with the project's own context
    # attached. The assertion is on what CROSSED THE WIRE — the piped text
    # reaching the model — because stdout carrying the mock's canned reply
    # would look identical whether the pipe was read or silently discarded.
    Scenario: A one-shot reads its prompt from piped input
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a fragment "testing" in bundle "demo" exists
      And a profile "dev" with bundle "demo"
      And the mock LLM responds "MOCK-REPLY"
      When I run "ctxloom run --one-shot --profile dev" with input:
        """
        PIPED-PROMPT-summarize-this
        """
      Then the command succeeds
      And the output contains "MOCK-REPLY"
      And the mock recorded input contains "PIPED-PROMPT-summarize-this"

    # A saved command is content, stored in a bundle and resolved by name, so
    # `-r` is how a team's reviewed prompt becomes the thing that runs. The
    # BODY has to reach the model: a run that resolved the command, dropped its
    # content and launched with an empty prompt would still exit 0.
    Scenario: A saved command becomes the prompt
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a command "review" in bundle "demo" exists
      And a profile "dev" with bundle "demo"
      And the mock LLM responds "MOCK-REPLY"
      When Alice runs the review prompt her team saved:
        """
        ctxloom run --one-shot --profile dev -r review
        """
      Then the command succeeds
      And the output contains "MOCK-REPLY"
      And the mock recorded input contains "COMMAND-BODY-review"

  Rule: A dry run renders what would be sent, and sends nothing

    `--dry-run` is the inspection surface: it stops before anything stateful or
    interactive happens — no session-index write, no coordinator, no task
    seeding, no isolation, no engine launch — and prints the composed context.

    # "the output contains dev" was satisfied by the dry-run header, which
    # names the profile whether or not assembly produced anything: a run that
    # loaded zero fragments and assembled an empty context passed. What a dry
    # run is FOR is showing what would be sent, so the fragment's REF and its
    # BODY are both asserted.
    #
    # The mock is configured and its reply must NOT appear. That is the half
    # that says "sends nothing": without it, a dry run that rendered the
    # context AND launched the engine anyway would pass every other assertion
    # here.
    Scenario: A dry run shows the composed context without launching a model
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a fragment "testing" in bundle "demo" exists
      And a profile "dev" with bundle "demo"
      And the mock LLM responds "MOCK-REPLY-NEVER-SENT"
      When Alice checks what would be sent before sending it:
        """
        ctxloom run --dry-run --profile dev hello
        """
      Then the command succeeds
      And the output contains "dev"
      And the output contains "demo#fragments/testing"
      And the output contains "FRAGMENT-BODY-testing"
      And the output does not contain "MOCK-REPLY-NEVER-SENT"

    # U082-F03 regression: an explicit -t selection matching zero fragments
    # previously assembled Context: "" with a nil error and NO warning at all —
    # `ctxloom run -t <tag-that-matches-nothing>` exited 0 having delivered no
    # context whatsoever, indistinguishable from a working, empty-by-design
    # session. No per-hop test caught it: AssembleContext's own unit tests
    # asserted the (correct) empty Context string and stopped there, never
    # asking whether the CALLER could tell that apart from "nothing was asked
    # for". This asserts the CLI's own observable behavior — a non-zero exit
    # naming the tag — not just that assembly returned successfully.
    Scenario: An explicit tag selection matching nothing fails loudly, not silently
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a fragment "testing" in bundle "demo" exists
      When Alice selects by a tag nothing carries:
        """
        ctxloom run --dry-run -t no-such-tag-anywhere hello
        """
      Then the command fails
      And the output contains "no-such-tag-anywhere"

  Rule: What the profile declares reaches the launched backend

    A profile carries more than fragments: tool denials, permission posture,
    the engine it prefers. None of that is worth anything if it is lost between
    the command line and the process that runs.

    # T2 regression: deny_tools was silently dropped crossing internal/lm/grpc's
    # proto wire (ManagedConfigToProto/managedConfigFromProto). A per-hop test
    # cannot see this class of defect because the proto converter and its
    # mirror struct read as a matched pair; only tracing the payload
    # tip-to-tail (argv -> profile resolution -> proto wire -> the launched
    # backend's Setup) does. This asserts the WIRE PAYLOAD the backend actually
    # received, not just that the command exited 0.
    Scenario: A tool denial configured on a profile reaches the launched backend
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And a profile "guarded" with bundle "demo" and deny_tools "WebFetch"
      And the mock LLM responds "MOCK-REPLY"
      When Alice runs under a profile that forbids a tool:
        """
        ctxloom run --one-shot --profile guarded unicorn-prompt
        """
      Then the command succeeds
      And the mock recorded input contains "WebFetch"
