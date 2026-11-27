# world: base
# Ported from cli/coordination_contract.feature (rev f36e8dfb2)
# Ported from journeys/j002100_delegation.feature (rev f36e8dfb2) (the last Rule)
#   rewritten for the daemon's tools (owner, 2026-11-18, "Go-era MCP
#   specs") as DRAFT text, accepted under the standing ruling and reviewed
#   in full before release. Go's `agent_send` is `child_send`, its message
#   `kind` is the message's urgency, and Go's `agent_stop` is `child_close`
#   (S46).
# NOT ported: "agent_send advertises the kind field, not a kind convention
#   buried in structured" (S46 and S19: a message is its text and its
#   urgency; there is no `kind` and no `structured`, ruling 101); the
#   coordinator-reserved kinds of "A sender cannot claim a
#   coordinator-reserved message kind" (no kinds: the daemon's notices to an
#   owner are its own facts, never a message a sender writes); "the surface
#   states the rejection behaviour" (its vocabulary is the schema's enum and
#   its requirement the schema's `required`, both kept below; no tool text
#   says a word outside it is refused, a text the owner has not written);
#   agent_stop's "already ended" (S46 has no stop tool: a run's stop is
#   `session stop`, the human's, j002100).

Feature: The coordination tools advertise a closed vocabulary, and refuse what is not theirs to accept

  An orchestrating agent knows what it may ask of a child only because
  ctxloom's MCP server tells it. A message to a child is `child_send`'s
  text and its urgency: inform (read at the child's next turn), wake (start
  a turn) or steer (interrupt the turn in flight). The urgency is a closed
  vocabulary, enumerated in the tool's own schema, and a word outside it is
  refused before the daemon is asked, so nothing is sent.

  These scenarios keep it closed: a widened vocabulary, or an urgency let
  through, comes back red here.

  Rule: The tools advertise the vocabulary a sender may use

    # The vocabulary is CLOSED, and closed means enumerated: a word not on
    # this list cannot be named at all, and one added without a decision
    # shows up here rather than in a model's context.
    Scenario: child_send advertises exactly the three urgencies, and requires one
      # substitution: Go's "the advertised vocabulary is exactly the declared one", and the REQUIRED of "the surface states the rejection behaviour": the kind enum is child_send's urgency enum (S46, S19)
      Given an initialized ctxloom project
      And a session "amber-quiet-heron" that is still running
      When its engine reads the "child_send" tool's input contract
      Then its "urgency" argument's vocabulary is exactly:
        | inform |
        | wake   |
        | steer  |
      And it requires the arguments "child", "urgency" and "text"

    # Some harnesses surface the description and elide the argument docs, so
    # the description names each urgency and what it does.
    Scenario: The tool description itself names each urgency
      # substitution: Go's "the tool description itself states the vocabulary is closed": the description names the three urgencies, the owner's approved tool text (2026-11-14)
      Given an initialized ctxloom project
      And a session "amber-quiet-heron" that is still running
      When its engine reads the "child_send" tool's description
      Then it says "inform (read at its next turn)"
      And it says "wake (start a turn)"
      And it says "steer (interrupt its turn)"

  Rule: The coordinator's verbs refuse what is not theirs to accept

    # A word outside the vocabulary is refused at the tool, naming the word,
    # and nothing reaches the child; a word of the vocabulary still goes
    # through.
    Scenario: An urgency outside the vocabulary is refused, naming it, and nothing is sent
      # substitution: Go's "A sender cannot claim a coordinator-reserved message kind", less its reserved kinds (none exist): a word outside the vocabulary is refused naming it, and a word of it goes through; the child is the daemon's (step 9), not agent_run's
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" that is open and idle
      When the engine of "amber-quiet-heron" calls "child_send" for "brisk-copper-moth" with the urgency "made_up_urgency" and "check the tests"
      Then the tool failed with "mcp-server-urgency-invalid", naming "made_up_urgency"
      And no message "check the tests" was sent to "brisk-copper-moth"
      When the engine of "amber-quiet-heron" calls "child_send" for "brisk-copper-moth" with wake "check the tests"
      Then the tool answered with the child's session as the daemon's read model has it after the message
      And the message was sent once, under the id the server chose

    # child_close is idempotent: a second close answers the child, closed,
    # and records nothing. A child that was never launched is refused, never
    # silently accepted.
    Scenario: Closing a child is idempotent, and closing a child that never existed is refused
      # substitution: Go's "Stopping a child is idempotent, and stopping a run that never existed is refused": S46 has no stop tool, and child_close is the tool whose second call changes nothing
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" that is open and idle
      When the engine of "amber-quiet-heron" calls "child_close" for "brisk-copper-moth"
      Then the tool answered with the child's session, closed
      When the engine of "amber-quiet-heron" calls "child_close" for "brisk-copper-moth"
      Then the tool answered with the child's session, closed
      And "brisk-copper-moth" was closed once
      When the engine of "amber-quiet-heron" calls "child_close" for "calm-hollow-reed"
      Then the tool failed with "daemon-api-contract-session-not-found", naming "calm-hollow-reed"

    # Only a session's owner may send it a message. A session that is not
    # its owner (here, the child's own child) is refused, and
    # nothing reaches the child.
    # DRAFT (2026-11-19): added with the sessions-not-owner scenario the
    # night report tracked; the owner reviews it with the rest of the file.
    Scenario: A session that is not a child's owner cannot send it a message
      # substitution: no Go counterpart; the daemon's ownership rule (sessions-not-owner) seen through child_send
      Given an initialized ctxloom project
      And a child "brisk-copper-moth" of "amber-quiet-heron" that is open and idle
      And a child "olive-quiet-finch" of "brisk-copper-moth" that is open and idle
      When the engine of "olive-quiet-finch" calls "child_send" for "brisk-copper-moth" with wake "check the tests"
      Then the tool failed with "sessions-not-owner", naming "only its owner may do that"
      # DRAFT (2026-11-19): the refusal names the requester by its stable id
      # (it once read "session  is not this session's owner", the coordinator
      # naming a refusal's type alone).
      And the tool's refusal names the stable id of "olive-quiet-finch" as the requester
      And no message "check the tests" was sent to "brisk-copper-moth"
