Feature: Run
  run assembles context from a profile and hands it to the configured LLM. With
  the mock backend the full path is observable: the assembled context reaches the
  model, and the model's reply reaches the user.

  A run is either a SESSION or a single turn, and `--one-shot` is how a caller
  says which. The flag names the mode, not its output: every mode prints, and
  what actually differs is that a one-shot takes one turn and exits.

  # THE MODE HAS ONE SPELLING. A caller reaching for it by a name it does not
  # have must not get a run they did not ask for. An unknown flag is an error,
  # and that error is what makes the real spelling discoverable; an alias
  # quietly accepted alongside it would leave two names for one mode forever,
  # in help, in scripts, and in every future reader's head.
  Scenario: A flag that is not the mode's name is refused rather than guessed at
    Given an initialized ctxloom project
    When I run "ctxloom run --print hello"
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

  # "the output contains dev" was satisfied by the dry-run header, which names
  # the profile whether or not assembly produced anything: a run that loaded
  # zero fragments and assembled an empty context passed. What a dry run is FOR
  # is showing what would be sent, so that is what is asserted.
  Scenario: Dry run assembles context without launching a model
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a fragment "testing" in bundle "demo" exists
    And a profile "dev" with bundle "demo"
    When I run "ctxloom run --dry-run --profile dev hello"
    Then the command succeeds
    And the output contains "dev"
    And the output contains "demo#fragments/testing"
    And the output contains "FRAGMENT-BODY-testing"

  # Asserting only the mock's canned reply proved the reply channel works and
  # nothing about the context: the mock answers "MOCK-REPLY" to an empty prompt
  # just as readily. This asserts what CROSSED THE WIRE, the same way the
  # deny_tools scenario below does.
  Scenario: Run hands the assembled context to the model and returns its reply
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a fragment "testing" in bundle "demo" exists
    And a profile "dev" with bundle "demo"
    And the mock LLM responds "MOCK-REPLY"
    When I run "ctxloom run --one-shot --profile dev unicorn-prompt"
    Then the command succeeds
    And the output contains "MOCK-REPLY"
    And the mock recorded input contains "FRAGMENT-BODY-testing"

  # T2 regression: deny_tools was silently dropped crossing internal/lm/grpc's
  # proto wire (ManagedConfigToProto/managedConfigFromProto). A per-hop test
  # cannot see this class of defect because the proto converter and its mirror
  # struct read as a matched pair; only tracing the payload tip-to-tail (argv ->
  # profile resolution -> proto wire -> the launched backend's Setup) does. This
  # asserts the WIRE PAYLOAD the backend actually received, not just that the
  # command exited 0.
  Scenario: deny_tools configured on a profile reaches the launched backend
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a profile "guarded" with bundle "demo" and deny_tools "WebFetch"
    And the mock LLM responds "MOCK-REPLY"
    When I run "ctxloom run --one-shot --profile guarded unicorn-prompt"
    Then the command succeeds
    And the mock recorded input contains "WebFetch"

  # U082-F03 regression: an explicit -t selection that matches zero fragments
  # previously assembled Context: "" with a nil error and NO warning at all —
  # `ctxloom run -t <tag-that-matches-nothing>` exited 0 having delivered no
  # context whatsoever, indistinguishable from a working, empty-by-design
  # session. No per-hop test caught this: AssembleContext's own unit tests
  # asserted the (correct) empty Context string and stopped there, never
  # asking whether the CALLER could tell that apart from "nothing was asked
  # for". This asserts the CLI's own observable behavior — a non-zero exit
  # naming the tag — not just that assembly itself returned successfully.
  Scenario: An explicit tag selection matching nothing fails loudly, not silently
    Given an initialized ctxloom project
    And a bundle "demo" exists
    And a fragment "testing" in bundle "demo" exists
    When I run "ctxloom run --dry-run -t no-such-tag-anywhere hello"
    Then the command fails
    And the output contains "no-such-tag-anywhere"
