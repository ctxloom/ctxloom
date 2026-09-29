Feature: attach — refuses rather than guessing which pane is yours

  `ctxloom attach <harp>` is meant to put your terminal on a run's live tmux
  pane. It is HIDDEN, and it refuses every invocation: a pane's window name is
  minted at random per run and journaled nowhere, so the one fact attach needs
  cannot be looked up yet.

  The refusal is the behaviour worth pinning. `tmux attach -t` against a wrong
  but EXISTING target succeeds and shows you a different run's pane with no
  error anywhere — so an attach that guessed would look like it worked. When
  the window is journaled and attach gains a success path, this file gains the
  scenario for it and the refusal below has to go.

  Scenario: Attaching to a run is refused, naming the missing fact rather than guessing
    Given an initialized ctxloom project
    When I run "ctxloom attach brisk-copper-moth"
    Then the command fails
    And the output contains "brisk-copper-moth"
    And the output contains "not available yet"
    And the output contains "pane window is not recorded"
