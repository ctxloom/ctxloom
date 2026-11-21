# world: base
# Ported from journeys/j002300_cross_engine_delegation.feature (rev f36e8dfb2)
# NOT ported: a child's report sent to its orchestrator by the child itself
#   (`agent_send` from a child): a session sends only to its own children
#   (the tree's owner rule), so a child reports by ending its turn, which is
#   told to its owner. Every scenario is told again in the daemon's
#   vocabulary (OD-44, amended 2026-11-17). The two @live-only outlines are
#   @pending, as j000400's is: they need a real engine (the live lane).

@doc
Feature: Delegation — each child sees only its own context, and hears from and reports to its orchestrator

  Alice's session hands work to other agents: a "librarian" that knows where
  the team's documents live, and a "cartographer" that knows the codebase's
  layout. Each child works from its own profile, never the orchestrator's or
  its sibling's, and the orchestrator and its children talk as the work goes
  on: the orchestrator sends a child a message (`child_send`, with an
  urgency: `inform` waits for the child's next turn, `wake` starts one,
  `steer` interrupts the turn in flight), each turn a child ends is told to
  the orchestrator without its asking, and a child that cannot start says
  so.

  Every child is a session of its own, which Alice's daemon runs and keeps
  open between tasks. It holds on whichever engine a child runs on, and in
  the project or in a checkout of its own; two children at work at the same
  time never see each other's files.

  Background:
    # substitution: the session is named, and run by her daemon
    Given Alice has a project that has never used ctxloom
    And Alice has exported her assistant's token
    And Alice has set ctxloom up in her project
    And Alice's orchestrator can delegate to two agents, "librarian" and "cartographer", each carrying its own distinct guidance in its own profile
    And Alice's session "amber-quiet-heron" is running

  # Each child, asked what it was told, reports from the guidance in its own
  # profile and not its sibling's.
  Scenario: Two children delegated to the same engine each report guidance found only in their OWN composed profile
    # substitution: delegation is `child_launch` (slice 3), and what a child reports is its turn's reply
    When Alice's orchestrator launches "brisk-copper-moth" on "librarian" to "say what guidance you were given"
    Then "brisk-copper-moth" reports "librarian"'s guidance, not "cartographer"'s
    When Alice's orchestrator launches "quiet-silver-fern" on "cartographer" to "say what guidance you were given"
    Then "quiet-silver-fern" reports "cartographer"'s guidance, not "librarian"'s

  # A child on opencode works from the guidance its session home delivered to
  # it, as a child on Claude Code does.
  # Pending: an opencode child waits on the ACP engine (slice 10d; ACP parked).
  @opencode @pending
  Scenario: A child delegated to an opencode agent runs on opencode and reports what its delivered context says
    # substitution: an agent on the opencode engine, launched by `child_launch`; what the child reports is what Alice sees
    Given Alice's orchestrator can delegate to "qwen-worker", an agent on opencode, carrying its own guidance
    When Alice's orchestrator launches "steady-lunar-pike" on "qwen-worker" to "say what guidance you were given"
    Then "steady-lunar-pike" reports "qwen-worker"'s guidance

  # The orchestrator sends a child a message, and the child's next turn takes
  # it up.
  Scenario: A message the orchestrator sends via child_send reaches its child, verified in the child's own next turn
    # substitution: `agent_send` is `child_send`, with the urgency said (S19)
    Given Alice's orchestrator has launched "brisk-copper-moth" on "librarian", which is open and idle
    When the engine of "amber-quiet-heron" calls "child_send" for "brisk-copper-moth" with wake "J002300-ROUNDTRIP-ECHO-TOKEN-6d2e73"
    Then the tool answered with the child's session as the daemon's read model has it after the message
    And "brisk-copper-moth"'s next turn was "J002300-ROUNDTRIP-ECHO-TOKEN-6d2e73"

  # The orchestrator hands out the work and ends its turn, asking nothing
  # more. The child's turn ends, the orchestrator is woken by being told so,
  # and it starts its next turn with the news, receiving nothing itself.
  Scenario: An idle orchestrator is woken by its child's turn ending, with no receive
    # substitution: the orchestrator is Alice's own terminal session (the terminal wake): the ctxloom MCP server her engine started watches it and posts a wake on her engine's messaging socket, and her next prompt's hook hands the session what woke it; the child's turn's end is sent to her session as a wake
    Given Alice's session "amber-quiet-heron" is idle in her terminal, the ctxloom MCP server its engine started watching it
    When the engine of "amber-quiet-heron" calls "child_launch" for "brisk-copper-moth" with agent "librarian" and task "say what guidance you were given"
    Then "amber-quiet-heron" was woken, and its next turn began with "child brisk-copper-moth finished its turn"
    And "brisk-copper-moth" reports "librarian"'s guidance, not "cartographer"'s

  # A child that cannot start: its orchestrator is told that its run ended
  # without starting, Alice can see why, and the child recorded no turn.
  Scenario: A child that cannot start tells its orchestrator so, and Alice sees why
    # substitution: the orchestrator hears through its owner's notice, the child's run ended launch-failed
    Given the claude program a child runs cannot be found
    When Alice's orchestrator launches "brisk-copper-moth" on "librarian" to "say what guidance you were given"
    Then "amber-quiet-heron" lists "child brisk-copper-moth ended its run 1: launch-failed" as waiting for a turn
    And "brisk-copper-moth" recorded no turn
    When Alice looks at the child:
      """
      ctxloom session show brisk-copper-moth --format json
      """
    Then the command succeeds
    And the output contains "could not start the child's engine"

  # Alice's project works in checkouts. Two children at work at the same
  # time each get a checkout of their own, on a branch of their own; what
  # one writes is never in the other's, nor in Alice's live project.
  Scenario: Two children at work at the same time, each in a checkout of its own, never see each other's files
    # new: concurrent children with differing context, each in its own workspace (a hosted child follows its agent's workspace, s4-Q11)
    Given Alice's project config says "workspace: worktree"
    And Alice's orchestrator has launched "brisk-copper-moth" on "librarian" and "quiet-silver-fern" on "cartographer", both open and idle
    When "brisk-copper-moth" and "quiet-silver-fern" each write "NOTES.md" in turns that run at the same time
    Then "brisk-copper-moth" works in a checkout of its own, on the branch "ctxloom/brisk-copper-moth/librarian"
    And "quiet-silver-fern" works in a checkout of its own, on the branch "ctxloom/quiet-silver-fern/cartographer"
    And each child's "NOTES.md" is in its own checkout alone, and not in its sibling's or in Alice's project

  # On a real engine, the child finds a phrase that exists only in its own
  # profile and gives it back as its reply. The orchestrator then sends the
  # child a fresh value, and the child gives that back too.
  @live-only @every-runtime @delegation @pending
  Scenario Outline: A delegated child on a real <engine> reports back a marker only its own composed context could supply
    # substitution: delegation is `child_launch` and the message `child_send`; the child reports by ending its turn, told to its owner, since a child sends nothing to its owner
    Given a real "<engine>" engine is available for a delegated child carrying marker "<marker>"
    When Alice's orchestrator launches "brisk-copper-moth" on "delegate" to "send back the marker phrase you find in your context"
    Then "amber-quiet-heron" lists "child brisk-copper-moth finished its turn" as waiting for a turn
    And "brisk-copper-moth"'s reply holds "<marker>"
    When Alice's orchestrator sends "brisk-copper-moth" a fresh value and asks for it back
    Then "brisk-copper-moth"'s reply holds that value

    Examples:
      | engine      | marker                                       |
      | claude-code | J002300-DELEGATE-MARKER-CLAUDE-CODE-1d4c07ab |
      | opencode    | J002300-DELEGATE-MARKER-OPENCODE-6e1b3f52    |

  # A child busy with one long turn is steered by its orchestrator: the turn
  # in flight is interrupted, and the child acts on the steer as its next
  # turn, before it finishes the long work.
  @live-only @every-runtime @midturn-mail @pending
  Scenario Outline: A delegated child on a real <engine> in one long multi-tool turn acts on its orchestrator's steer before that work is done
    # substitution: a message for a turn in flight is a `steer` (S19), which interrupts it; the child does not tell its orchestrator it has started
    Given a real "<engine>" engine is available for a mid-turn-mail child
    When Alice's orchestrator launches "brisk-copper-moth" on "delegate" to "do one long task"
    And "brisk-copper-moth" is in the middle of its long turn
    And the engine of "amber-quiet-heron" calls "child_send" for "brisk-copper-moth" with steer "J002300-MIDTURN-CODE-4b9e21"
    Then the child's turn in flight was interrupted, and its next turn was "J002300-MIDTURN-CODE-4b9e21"
    And "brisk-copper-moth"'s reply holds "J002300-MIDTURN-CODE-4b9e21"

    Examples:
      | engine      |
      | claude-code |
