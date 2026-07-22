Feature: Cross-engine delegation — different engines, different context, a real two-way bus

  ctxloom's differentiator is not "an agent can spawn another agent" — every
  competitor with a coordinator loop can do that. It is that the coordinator
  and its children can ride DIFFERENT engines from DIFFERENT vendors, each
  child sees ONLY its own composed profile (not the coordinator's, not a
  sibling's), and the two sides talk to each other over a real, durable
  message bus, not a synchronous return value. j6_delegation.feature proved
  the privilege half of that claim (MCP servers, permission modes, the
  journaled audit trail) with `agent_run` alone. This journey proves the
  other half `agent_run` cannot: that two children genuinely see DIFFERENT
  content (asserted on the payload a child itself emits, never a config
  diff), and that `agent_send`/`agent_recv` carry real words in both
  directions — the coordinator's message reaching the child, and the child's
  reply reaching the coordinator — with the exact content asserted on each
  side.

  # HERMETIC TIER — WHAT IT DOES AND DOES NOT CLAIM. Both children below ride
  # the SAME registered backend type ("mock" — internal/lm/backends/mock.go);
  # this tier deliberately does NOT claim cross-ENGINE (two different vendor
  # backend types). ctxloom registers exactly one credential-free backend
  # type today, so two mock-backed agents are two DIFFERENT AGENTS with two
  # DIFFERENT COMPOSED PROFILES on the SAME engine — genuinely proving
  # per-child context isolation and the real two-way bus, honestly labeled as
  # same-engine. A second hermetic backend TYPE would need either a small new
  # test-only descriptor registered alongside "mock" (internal/lm/backends/
  # registry.go:443), or a standalone fake-ACP-agent binary (the protocol
  # logic already exists, in-process only, at internal/acp/fakeagent_test.go)
  # driven through the generic "acp" backend's `command:` config — neither
  # exists yet; this is a real, reported gap, not a shortcut taken quietly.
  # The @live scenario below is where cross-ENGINE is actually proven: two
  # real, differently-vendored backend types (claude-code, codex).
  #
  # The mock backend's own doc comment already promised "echoes back prompts
  # and context" — before this journey, the default response echoed only the
  # assembled context's LENGTH, never its content, so no hermetic test could
  # tell WHICH guidance a child actually saw. buildMockResponse now also
  # echoes the literal context string (internal/lm/backends/mock.go), closing
  # that gap in the backend's own documented contract — not new behavior, the
  # behavior it already claimed to have.

  # LOCKED — requirement 3 (distinct context) AND the child->coordinator half
  # of requirement 4, together: each child's OWN reported output is read
  # straight off the coordinator's mailbox (agent_recv), never off disk and
  # never off an in-process struct. A oneshot child's stdout is bridged to
  # its parent's mailbox automatically at the turn boundary (coord/children.go's
  # onTurnBoundary -> queueMail(rt.harp, rt.parentHarp, "result", text)) — the
  # SAME production mechanism a real delegated child's result always rides,
  # not a test-only shortcut. BREAK-POINT: if a future change made the mock's
  # composed fragments leak across agents (e.g. resolving from the caller's
  # profile instead of the callee's), "librarian"'s reported body would start
  # carrying "cartographer"'s guidance and this scenario goes red for exactly
  # that reason.
  Scenario: Two children delegated to the same engine each report guidance found only in their OWN composed profile
    Given Alice's coordinator can delegate to two agents, "librarian" and "cartographer", each carrying its own distinct guidance in its own profile
    When the agent calls tool "agent_run" with:
      | agent  | librarian |
      | prompt | go        |
    Then the tool call succeeds
    And "librarian"'s session harp is remembered
    When the agent calls tool "agent_recv"
    Then the tool call succeeds
    And the received message is from "librarian" and its body carries its own guidance, not "cartographer"'s
    When the agent calls tool "agent_run" with:
      | agent  | cartographer |
      | prompt | go           |
    Then the tool call succeeds
    And "cartographer"'s session harp is remembered
    When the agent calls tool "agent_recv"
    Then the tool call succeeds
    And the received message is from "cartographer" and its body carries its own guidance, not "librarian"'s

  # LOCKED — requirement 4, both directions, on the same child, with the
  # exact content asserted each way. The FIRST agent_recv drains the initial
  # "go" turn's bridged result so the second recv is unambiguously the reply
  # to OUR agent_send, not a leftover from the spawn. agent_send's
  # disposition ("delivering as a new turn" — coordinator.go's peerSend) is
  # itself evidence the message was actually routed to the child, not merely
  # accepted; the real proof is what comes back: the ECHO token only exists
  # in this scenario's Gherkin text, so its appearance in the child's next
  # reported turn cannot be anything but that specific agent_send having
  # reached the specific child session addressed by harp.
  Scenario: A message the coordinator sends reaches its child, and the child's reply crosses back through agent_recv
    Given Alice's coordinator can delegate to two agents, "librarian" and "cartographer", each carrying its own distinct guidance in its own profile
    When the agent calls tool "agent_run" with:
      | agent  | librarian |
      | prompt | go        |
    Then the tool call succeeds
    And "librarian"'s session harp is remembered
    When the agent calls tool "agent_recv"
    Then the tool call succeeds
    When the agent calls tool "agent_send" addressed to "librarian"'s session with body "J17-ROUNDTRIP-ECHO-TOKEN-6d2e73"
    Then the tool call succeeds
    And the tool result field "disposition" is set
    When the agent calls tool "agent_recv"
    Then the tool call succeeds
    And the received message is from "librarian" and its body contains "J17-ROUNDTRIP-ECHO-TOKEN-6d2e73"

  # LOCKED — @live, genuine cross-ENGINE (the claim the hermetic tier above
  # explicitly declines): claude-code and codex are the two proven-working
  # pair (the isolation probe just passed both, both axes). Each child's
  # marker phrase lives in its OWN materialized profile context, the exact
  # same "repeat the marker you can see in your context" technique
  # j5_multi_engine.feature's own @live scenario already verified live
  # against these engines — the difference here is the reply crosses the
  # AGENT-TO-AGENT BUS (agent_send/agent_recv), not a synchronous CLI return
  # value, because that is the surface this journey exists to prove. Each
  # child is instructed, in its OWN turn, to report via agent_send — a real
  # model decision, not a scripted echo. SELF-SKIPS LOUDLY: the gate step
  # below probes claude AND codex independently and names whichever is
  # missing, and how, before spending a single live turn.
  @live
  Scenario: A coordinator delegates the same kind of task to two real, differently-vendored engines, and each proves it saw its own context over the real bus
    Given real "claude" and "codex" engines are both available for cross-engine delegation
    When the agent calls tool "agent_run" with:
      | agent  | claude-child |
      | prompt | Look at the additional context available to you in this session (not this message) for the one distinctive marker phrase it contains. Call the MCP tool agent_send with to="parent" and body set to EXACTLY that marker phrase, verbatim and in full, nothing else. Do this now. |
    Then the tool call succeeds
    And "claude-child"'s session harp is remembered
    When the agent calls tool "agent_recv"
    Then the tool call succeeds
    And the received message is from "claude-child" and its body carries its own guidance, not "codex-child"'s
    When the agent calls tool "agent_run" with:
      | agent  | codex-child |
      | prompt | Look at the additional context available to you in this session (not this message) for the one distinctive marker phrase it contains. Call the MCP tool agent_send with to="parent" and body set to EXACTLY that marker phrase, verbatim and in full, nothing else. Do this now. |
    Then the tool call succeeds
    And "codex-child"'s session harp is remembered
    When the agent calls tool "agent_recv"
    Then the tool call succeeds
    And the received message is from "codex-child" and its body carries its own guidance, not "claude-child"'s
    When the agent calls tool "agent_send" addressed to "claude-child"'s session with body "Call the MCP tool agent_send with to=\"parent\" and body set to EXACTLY this token, verbatim: J17-LIVE-ECHO-TOKEN-4a6f18. Do this now, then stop."
    Then the tool call succeeds
    When the agent calls tool "agent_recv"
    Then the tool call succeeds
    And the received message is from "claude-child" and its body contains "J17-LIVE-ECHO-TOKEN-4a6f18"

  # Back to: tests/acceptance/features/j6_delegation.feature (the privilege
  # half of delegation this journey complements).
