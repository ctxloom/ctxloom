@doc
Feature: Adopting ctxloom without regret — the first hour, and the exit verified before committing

  Alice has a live repo and five assistants configured five different ways. She
  is willing to try one more tool, on one condition she has learned the hard
  way: she wants to know she can get back out. Adoption is cheap to reverse or
  it is not really a trial.

  So her first hour has two halves. She wires ctxloom in, gets suspicious ten
  minutes later that it did anything at all — and then, before she commits any
  of it, she takes it back out again and checks that what she wrote is still
  hers.

  # Every Then here names something ALICE can see: her assistant receives the
  # project's context, the tool tells her what it wired, her own files survive.
  # The byte-level proofs — which key in which settings file, how a context
  # hook is told apart from a statusline entry, each engine's own config
  # surfaces — belong to cli/manage.feature, which owns that noun.

  Background:
    Given an empty project directory

  # Installing is the subject here, so the command is spelled out. Alice's
  # question ten minutes in is not "did it report success" — that is the thing
  # she is suspicious of — but whether a fresh assistant session actually
  # receives the project's context.
  Scenario: Alice wires ctxloom in and her assistant starts receiving the project's context
    When Alice wires ctxloom into her project:
      """
      ctxloom manage install --engine claude-code
      """
    Then the command succeeds
    And her assistant is wired to receive the project's context at the start of every session
    And ctxloom's own working state is kept out of source control

  # The tool's report must agree with the disk. A status command that says
  # "wired" over a project where nothing was written is worse than no status
  # command, because it answers exactly the question being asked.
  Scenario: What ctxloom reports it wired is what is actually there
    Given ctxloom is already wired into her project
    When Alice asks ctxloom what it has configured:
      """
      ctxloom manage check
      """
    Then the command succeeds
    And ctxloom reports the engine it wired her project for

  # doctor is deterministic — no network, no engine, no credentials — which is
  # what makes it the check Alice can run before she trusts anything else the
  # tool says.
  Scenario: Alice can check the harness deterministically, with no network and no engine
    Given ctxloom is already wired into her project
    When Alice runs the deterministic health check:
      """
      ctxloom doctor
      """
    Then she is shown a health check for every part of the harness

  # The half that decides whether she adopts. Exiting cleanly means two things
  # at once: what ctxloom put in is gone, and what she authored is untouched.
  #
  # The wired state is asserted BEFORE the removal, so the scenario starts from
  # a project that demonstrably has something to remove — otherwise it would
  # pass just as well against an uninstall that does nothing.
  Scenario: Alice verifies she can leave before she commits
    Given ctxloom is already wired into her project
    And her assistant is wired to receive the project's context at the start of every session
    When Alice takes ctxloom back out:
      """
      ctxloom manage uninstall
      """
    Then the command succeeds
    And nothing ctxloom wired into her assistant is left behind
    And the context she authored is still hers
