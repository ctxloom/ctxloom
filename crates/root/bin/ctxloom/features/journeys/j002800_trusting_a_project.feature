# world: base
# NOT ported: nothing; the journey is new.

@doc
Feature: A project Alice cloned runs on its own settings only once she trusts it

  Some of what a project's own ctxloom config can say reaches past the
  project: which program runs as her assistant, which credentials file it
  reads, whether it runs in her own home instead of a session home of its
  own. A project she cloned from someone else says those things on its
  author's say-so, not hers.

  So ctxloom holds them back until Alice has looked at them and trusts the
  project. Trusting it is her act, recorded with her name and the time, and
  it covers what she saw: if a later pull changes those settings, they wait
  for her again. She can take her trust back at any time.

  Background:
    # new: trust, from Alice's side.
    Given Alice has exported her assistant's token
    And Alice has cloned a project whose own ctxloom config names the program that runs as her assistant

  # The clone's own assistant program is held back, and ctxloom tells her
  # how to look at it and trust it. She looks, says yes, and her next run
  # uses it.
  Scenario: A cloned project's own settings wait until Alice trusts it, then apply
    # new: trust gates the project-only settings.
    When Alice runs her assistant in the cloned project:
      """
      ctxloom run --one-shot "hello"
      """
    Then the command exits with code 2
    And she is told the project's own "engines.claude-code.command" waits for her trust, and that `ctxloom trust` is how to give it
    When Alice looks at what trusting the project allows, and says yes at her terminal:
      """
      ctxloom trust
      """
    Then the command succeeds
    And the project is listed as trusted by her, just now
    When Alice runs her assistant in the cloned project again:
      """
      ctxloom run --one-shot "hello"
      """
    Then the command succeeds
    And her assistant ran as the program the project names

  # She trusted what she saw. A pull changes it, so it waits for her again.
  Scenario: A change to what Alice trusted waits for her trust again
    # new: trust covers what she saw.
    Given Alice has trusted the cloned project
    And a pull changes the program the project names
    When Alice runs her assistant in the cloned project:
      """
      ctxloom run --one-shot "hello"
      """
    Then the command exits with code 2
    And she is told the project's own settings changed since she trusted them

  # She changes her mind.
  Scenario: Alice takes her trust back, and the project's own settings wait again
    # new: trust --undo.
    Given Alice has trusted the cloned project
    When Alice takes her trust back:
      """
      ctxloom trust --undo --yes
      """
    Then the command succeeds
    When Alice runs her assistant in the cloned project:
      """
      ctxloom run --one-shot "hello"
      """
    Then the command exits with code 2
    And she is told the project's own "engines.claude-code.command" waits for her trust, and that `ctxloom trust` is how to give it
