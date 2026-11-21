# world: base
# Ported from journeys/j002100_delegation.feature (rev f36e8dfb2)
# NOT ported: Go's two hold scenarios, the rate-limit hold and the refused
#   credential's hold, with `@durable-holds` (OD-45, owner 2026-11-13: holds
#   are dropped; a rate-limited child fails and the human resumes it, which
#   the scenario that replaces them tells); `session pause` and `session ask`
#   (the daemon has no pause, and `session send` replaces the ask, S19); the
#   reserved-kind refusal and agent_stop's idempotence, which are
#   cli/coordination_contract.feature's (rewritten for S46, 2026-11-18: no
#   kind is reserved, there being no kinds; an urgency outside child_send's
#   vocabulary is refused, and child_close is idempotent). Every scenario is
#   told again in the daemon's vocabulary (OD-44, amended 2026-11-17).

@doc
Feature: Orchestrator delegates isolated work

  Alice runs a session that hands work out to other agents: a "reviewer" to
  read the change and a "fixer" to repair what the reviewer finds. Each is an
  agent of her project's config, with a profile of its own, an MCP server of
  its own and claude permissions of its own (the agent's
  `engines.claude-code.permissions`, which ctxloom passes to claude as they
  are written). She trusts the arrangement only if each child gets what she
  gave it and nothing more: the fixer must not reach the reviewer's tools,
  and neither may reach the ones she kept for her own session.

  Her session delegates through ctxloom's MCP server, which every session's
  engine is given: `child_launch` starts a child, a session of its own that
  her daemon runs and keeps open between tasks; `child_send` gives it more
  work, with an urgency; `child_clear` starts its conversation again;
  `child_close` ends it for good. Alice can do each herself from any
  terminal, as `session launch`, `session send`, `session clear` and
  `session close`. Each turn a child ends is told to the session that owns
  it, so the orchestrator hears back without asking.

  So Alice watches. She sees what each child was given, she sees the
  orchestrator ask a child a question and hear its answer later, she sees a
  child that ran into its rate limit wait for her and not go again on its
  own, and she resumes it. If her own session dies and she resumes it, her
  children are still open and still hers. She stops a child's run when it is
  going the wrong way, and when the work is done she closes her session and
  its children close with it.

  Background:
    # substitution: the agents are written in the config, each with its own claude permissions document (rulings 133-139), and the orchestrator is Alice's own session on the agent "lead"
    Given Alice has a project that has never used ctxloom
    And Alice has exported her assistant's token
    And Alice has set ctxloom up in her project
    And Alice's project defines her own agent "lead" and two agents to delegate to, each on a profile of its own that carries an MCP server of its own, and each with claude permissions of its own:
      """
      default_agent: lead
      agents:
        lead:
          engine: claude-code
          profiles: [leading]     # carries the MCP server "lead-tools"
        reviewer:
          engine: claude-code
          profiles: [reviewing]   # carries the MCP server "review-tools"
          engines:
            claude-code:
              permissions: {defaultMode: plan}
        fixer:
          engine: claude-code
          profiles: [fixing]      # carries the MCP server "fix-tools"
          engines:
            claude-code:
              permissions: {defaultMode: acceptEdits, allow: ["Bash(cargo test)"]}
      """
    And Alice's session "amber-quiet-heron" is running

  # Alice's orchestrator hands out both pieces of work. Each child's session
  # home carries ctxloom's own MCP server and its own agent's, never its
  # sibling's and never the one Alice kept for her session, and its claude is
  # given its own agent's permissions, exactly as written.
  Scenario: Each child is given only its own MCP servers and permissions, never a sibling's or the orchestrator's
    # substitution: delegation is `child_launch` (slice 3); what a child was granted is what its session home carries
    When Alice's orchestrator launches "brisk-copper-moth" on "reviewer" to "review the change", and "quiet-silver-fern" on "fixer" to "fix what the review finds"
    Then "brisk-copper-moth" is listed as a child of "amber-quiet-heron", run by her daemon
    And "quiet-silver-fern" is listed as a child of "amber-quiet-heron", run by her daemon
    And "brisk-copper-moth"'s session home registers the MCP servers "ctxloom" and "review-tools", and neither "fix-tools" nor "lead-tools"
    And "quiet-silver-fern"'s session home registers the MCP servers "ctxloom" and "fix-tools", and neither "review-tools" nor "lead-tools"
    And "brisk-copper-moth"'s claude was given the permissions {"defaultMode":"plan"}
    And "quiet-silver-fern"'s claude was given the permissions {"defaultMode":"acceptEdits","allow":["Bash(cargo test)"]}
    When Alice looks at her session:
      """
      ctxloom session show amber-quiet-heron --format json
      """
    Then the command succeeds
    And the output reports "children" having "2" entries

  # The orchestrator asks the fixer which way to go and carries on at once.
  # The fixer's answer is its next turn; when that turn ends, the
  # orchestrator is told, and reads the answer from the fixer's transcript.
  Scenario: A message to a child returns at once, and the child's answer reaches the orchestrator when its turn ends
    # substitution: `session ask` is `child_send` with an urgency (S19); the answer is the child's turn, its end told to the owner, and read through `transcript_show`
    Given Alice's orchestrator has launched "quiet-silver-fern" on "fixer", which is idle and will answer its next message "the reversible one"
    When the engine of "amber-quiet-heron" calls "child_send" for "quiet-silver-fern" with wake "which migration path?"
    Then the tool answered with the child's session as the daemon's read model has it after the message
    And "quiet-silver-fern"'s next turn was "which migration path?"
    And "amber-quiet-heron" lists "child quiet-silver-fern finished its turn" as waiting for a turn
    When the engine of "amber-quiet-heron" calls "transcript_show" for "quiet-silver-fern"
    Then the tool answered with the transcript of "quiet-silver-fern", holding "which migration path?" and then "the reversible one"

  # The reviewer runs into its rate limit. Nothing holds it or tries again on
  # its own: its turn fails, its orchestrator is told, and it waits, open,
  # for Alice. Once she sees why, she resumes it herself.
  Scenario: A child that runs into its rate limit fails its turn, is not tried again on its own, and Alice resumes it
    # new: holds are dropped (OD-45): a rate-limited child fails and the human resumes it
    Given Alice's orchestrator has launched "brisk-copper-moth" on "reviewer", which is idle and whose next turn runs into its rate limit
    When the engine of "amber-quiet-heron" calls "child_send" for "brisk-copper-moth" with wake "review the migration"
    Then "amber-quiet-heron" lists "child brisk-copper-moth failed its turn" as waiting for a turn
    When Alice looks at the child:
      """
      ctxloom session show brisk-copper-moth --format json
      """
    Then the command succeeds
    And the output contains "rate-limited"
    And once its rate limit has reset, "brisk-copper-moth" has still run no turn on its own
    When Alice resumes the child:
      """
      ctxloom session send brisk-copper-moth wake "carry on with the review"
      """
    Then the command succeeds
    And "brisk-copper-moth"'s next turn was "carry on with the review"

  # Alice's session dies while her children are open. She resumes it, and
  # her children are where she left them: open, hers, and ready for more.
  Scenario: Alice resumes her session after it died, and her children are still open and hers
    # substitution: no holds (OD-45); what outlives the session is its children, open until closed, and `run --session` resumes it (ruling 115)
    Given Alice's orchestrator has launched "brisk-copper-moth" on "reviewer" and "quiet-silver-fern" on "fixer", both open and idle
    When Alice's session dies and she resumes it:
      """
      ctxloom run --session amber-quiet-heron
      """
    Then "brisk-copper-moth" is listed as a child of "amber-quiet-heron", run by her daemon
    And "quiet-silver-fern" is listed as a child of "amber-quiet-heron", run by her daemon
    When the engine of "amber-quiet-heron" calls "child_send" for "quiet-silver-fern" with wake "now fix the finding"
    Then "quiet-silver-fern"'s next turn was "now fix the finding"
    And "amber-quiet-heron" lists "child quiet-silver-fern finished its turn" as waiting for a turn

  # Alice gives the fixer the reviewer's profile too. A fixer launched after
  # that is given both MCP servers; the record of what the first fixer was
  # launched with still shows what it was actually given.
  Scenario: What a child was launched with stays on its record, and a later edit of its agent changes only the next child's
    # substitution: delegation is `child_launch`; the profiles are `agent edit --profiles` (there is no `--add-profile`), and permissions are written by hand (ruling 138)
    Given Alice's orchestrator has launched "quiet-silver-fern" on "fixer", which is open and idle
    When Alice gives "fixer" the reviewer's profile as well:
      """
      ctxloom agent edit fixer --profiles fixing,reviewing
      """
    Then the command succeeds
    When Alice's orchestrator launches "steady-lunar-pike" on "fixer" to "fix the next finding"
    Then the record of what "quiet-silver-fern" was launched with names the MCP servers "ctxloom" and "fix-tools", and not "review-tools"
    And the record of what "steady-lunar-pike" was launched with names the MCP servers "ctxloom", "fix-tools" and "review-tools"
    And neither record holds the command that starts an MCP server

  # The fixer is going the wrong way. Alice stops its run: its assistant
  # exits, its orchestrator is told, and the fixer stays open for its next
  # task, since only a close means no more work.
  Scenario: Alice stops a child's run, and the child stays open for its next task
    # substitution: stop is the run's stop, and close is the only "no more work" (OD-57)
    Given Alice's orchestrator has launched "quiet-silver-fern" on "fixer", which is in the middle of a turn
    When Alice stops the child's run:
      """
      ctxloom session stop quiet-silver-fern
      """
    Then the command succeeds
    And "quiet-silver-fern"'s assistant was asked to stop, and has exited
    And "amber-quiet-heron" lists "child quiet-silver-fern ended its run" as waiting for a turn
    When Alice gives the child its next task:
      """
      ctxloom session send quiet-silver-fern wake "fix it the reversible way"
      """
    Then the command succeeds
    And "quiet-silver-fern"'s next turn was "fix it the reversible way"

  # The work is done. Alice closes her session, and every child it still
  # holds is closed with it: their assistants exit, and nothing they were
  # launched with is left at rest.
  Scenario: Closing Alice's session closes every child it still holds
    # new: the close cascades (S19)
    Given Alice's orchestrator has launched "brisk-copper-moth" on "reviewer" and "quiet-silver-fern" on "fixer", both open and idle
    When Alice closes her session:
      """
      ctxloom session close amber-quiet-heron
      """
    Then the command succeeds
    And "brisk-copper-moth" is listed as closed, and its assistant has exited
    And "quiet-silver-fern" is listed as closed, and its assistant has exited
    And nothing "brisk-copper-moth" was launched with is left at rest
    And nothing "quiet-silver-fern" was launched with is left at rest
