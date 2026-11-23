# world: base
# NOT ported: nothing; the verb is new (step 10e Stage 2a), and Go had no daemon.
# DRAFT for the owner's review: spec text is his.

@doc
Feature: session restart — the daemon restarts a child's engine and resumes its conversation

  Surface covered here:

    ctxloom session restart <harp> [--note <text>]

  THE SESSION DECIDES. `session restart` asks the session, as the user, to
  restart its engine (the session's own `RequestRestart`, reason `operator`,
  sent through the daemon's command coordinator like `session stop` and
  `session close`). The session records `RestartOwed` for the incarnation
  that runs, once: a request while one is owed records nothing, and the same
  request replayed is one. The daemon that hosts the engine acts on what the
  session recorded.

  AT THE END OF THE TURN. The daemon never cuts a turn for a restart. While
  a turn is in flight the child is parked: a steer still reaches the turn, an
  inform or a wake waits (it is delivered into the resumed conversation). At
  the turn's end, or when the engine exits on its own, the daemon ends the
  engine, ends its incarnation `restarted`, and the session's next
  incarnation, cause `restart`, is hosted again resuming the engine's own
  conversation, in the same session home. A restart is never counted against
  the re-host budget.

  ONLY A CHILD. A terminal session's engine belongs to its own `ctxloom run`
  process, not to the daemon, so it is refused.

  Rule: A restart ends the engine and hosts the child again, resuming its conversation

    Scenario: An idle child is restarted at once
      # new: session restart.
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" that is open and idle
      When I run "ctxloom session restart brisk-copper-moth"
      Then the command succeeds
      And "brisk-copper-moth" runs again as its session's next incarnation, in its own conversation
      And its incarnation was recorded with the cause "restart"

  Rule: Only a session the daemon hosts is restarted

    Scenario: A terminal session is refused
      # new: session restart.
      Given an initialized ctxloom project
      And a session "amber-quiet-heron" that is still running
      When I run "ctxloom session restart amber-quiet-heron"
      Then the command exits with code 48
      And the refusal is "daemon-api-primary-not-hosted"

    Scenario: A session nobody recorded is refused
      # new: session restart.
      Given an initialized ctxloom project
      When I run "ctxloom session restart nobody-ever-ran"
      Then the command exits with code 48
