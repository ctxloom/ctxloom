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
  diff), and that `agent_send`/`agent_recv` carry real words between
  coordinator and child.

  # HARNESS/PRODUCT FINDING — read before the scenarios below, it shapes
  # what each tier can honestly claim. Investigating this journey surfaced a
  # real gap of the same shape J6's own comments already name for
  # roster/agent_report: coord/children.go's automatic "oneshot turn ->
  # parent mailbox" bridge (onTurnBoundary -> queueMail) fires ONLY when the
  # spawned backend does NOT implement agent.StructuredChat
  # (operations/delegate.go's PrepareAgentChat). Every backend registered
  # today — mock included (internal/lm/backends/mock_chat.go) — DOES
  # implement StructuredChat, so that branch's own doc comment already says
  # so: "today, no production backend; only test doubles". In production, a
  # delegated child's ONLY way to report to its coordinator is to decide,
  # inside its OWN reasoning loop, to call `agent_send(to: "parent", ...)`
  # through its forwarder MCP server — nothing bridges a chat child's output
  # automatically. A scripted, non-reasoning backend (mock's Chat() only
  # emits ChatEvents; it is not an MCP client and has no path to invoke the
  # coordinator's own agent_send tool) structurally cannot do that.
  #
  # So: the child->coordinator direction of the bus is provable only against
  # a REAL reasoning engine — the @live scenario below. Hermetically, this
  # journey instead reads each child's OWN canonical transcript
  # (~/.ctxloom/sessions/<harp>/persist/transcript.jsonl —
  # internal/transcript/record.go's documented, first-party schema, not a
  # scrape) to prove distinct context and the coordinator->child half of the
  # bus: a REAL agent_send call, content verified in the child's own
  # recorded next turn. This is the SAME class of durable, external,
  # disk-backed observable j6_delegation.feature already established for
  # runs.jsonl — never an in-process Go struct, never faked.
  #
  # A SECOND, more serious finding surfaced live-verifying the @live
  # scenario below — a real permission-ladder gap that blocks it from going
  # green today (not a test bug; see that scenario's own @wip comment for
  # the full evidence).

  # LOCKED — requirement 3 (distinct context): each child's OWN reported
  # turn is read straight off its canonical transcript, never off an
  # in-process struct and never off the sibling's. BREAK-POINT: if a future
  # change made the mock's composed fragments leak across agents (e.g.
  # resolving from the caller's profile instead of the callee's),
  # "librarian"'s reported turn would start carrying "cartographer"'s
  # guidance and this scenario goes red for exactly that reason.
  Scenario: Two children delegated to the same engine each report guidance found only in their OWN composed profile
    Given Alice's coordinator can delegate to two agents, "librarian" and "cartographer", each carrying its own distinct guidance in its own profile
    When the agent calls tool "agent_run" with:
      | agent  | librarian |
      | prompt | go        |
    Then the tool call succeeds
    And "librarian"'s session harp is remembered
    And "librarian"'s reported turn carries its own guidance, not "cartographer"'s
    When the agent calls tool "agent_run" with:
      | agent  | cartographer |
      | prompt | go           |
    Then the tool call succeeds
    And "cartographer"'s session harp is remembered
    And "cartographer"'s reported turn carries its own guidance, not "librarian"'s

  # LOCKED — the coordinator->child half of requirement 4, on real
  # infrastructure: a genuine `agent_send` MCP tool call, addressed by the
  # child's own runtime-minted harp, delivered as its next turn
  # (coordinator.go's peerSend/driveQueued), with the exact sent content
  # verified in what the child itself reported next. The ECHO token exists
  # only in this scenario's Gherkin text, so its appearance in the child's
  # next reported turn cannot be anything but that specific agent_send
  # having reached that specific child session.
  Scenario: A message the coordinator sends via agent_send reaches its child, verified in the child's own next reported turn
    Given Alice's coordinator can delegate to two agents, "librarian" and "cartographer", each carrying its own distinct guidance in its own profile
    When the agent calls tool "agent_run" with:
      | agent  | librarian |
      | prompt | go        |
    Then the tool call succeeds
    And "librarian"'s session harp is remembered
    And "librarian"'s reported turn carries its own guidance, not "cartographer"'s
    When the agent calls tool "agent_send" addressed to "librarian"'s session with body "J17-ROUNDTRIP-ECHO-TOKEN-6d2e73"
    Then the tool call succeeds
    And the tool result field "disposition" is set
    And "librarian"'s next reported turn carries "J17-ROUNDTRIP-ECHO-TOKEN-6d2e73"

  # @live, both requirement 2 (genuine cross-ENGINE, the claim the hermetic
  # tier above explicitly declines — claude-code and codex, the two
  # proven-working pair; the isolation probe just passed both, both axes)
  # and the child->coordinator half of requirement 4 the hermetic tier
  # structurally cannot reach (see the finding above). Each child's marker
  # phrase lives in its OWN materialized profile context, the exact "repeat
  # the marker you can see in your context" technique
  # j5_multi_engine.feature's own @live scenario already verified live
  # against these engines — the difference here is the reply crosses the
  # AGENT-TO-AGENT BUS (agent_send/agent_recv), not a synchronous CLI return
  # value, and each child is instructed, in its OWN turn, to make that call
  # itself — a real model decision, not a scripted echo, which is exactly
  # what the hermetic tier's mock backend cannot supply.
  # SELF-SKIPS LOUDLY: the gate step probes claude AND codex independently
  # and names whichever is missing, and how, before spending a single live
  # turn.
  #
  # @wip — A SECOND, MORE SERIOUS HARNESS/PRODUCT FINDING, live-verified
  # 2026-07-22 (task woozy-hasty-karma): this scenario is CORRECTLY
  # designed but does not go green today, and the reason is a real product
  # gap, not a test bug — reported per this task's own instructions, not
  # routed around. Live evidence (a real, authenticated claude-haiku-4-5
  # child, twice): the child's own reasoning correctly finds its marker and
  # decides to call agent_send (its "thinking" transcript entries show
  # exactly that plan), then emits a tool_use for
  # mcp__ctxloom__agent_send — and the runner immediately surfaces a
  # PermissionRequest for that call (transcript kind:"permission",
  # tool_name mcp__ctxloom__agent_send) that is NEVER resolved: the
  # child's turn parks indefinitely (90s+ observed, both times) with no
  # further transcript entry. This agent's own journaled run.enqueued fact
  # (runs.jsonl) carries `"ladder":[{"action":"auto_accept"}]` for its
  # "bypass" permission — an UNRESTRICTED rung (ladder.go's LadderRung.Kinds
  # doc: "empty matches every [kind]"), which per coord/approval.go's
  # serveApproval should auto-grant ANY ApprovalKind, including the
  # TOOL_USE kind an MCP tool call carries, with NO relay and NO wait. It
  # does not happen in practice for a live, ACP-driven, StartRun-migrated
  # claude-code child. Whether the gap is in the runner's ACP
  # session/request_permission -> agentcoord ApprovalRequest bridge, or
  # elsewhere in that path, is NOT diagnosed here — that would be a
  # production-code change, explicitly out of scope for this task. Filed
  # as a real, evidenced, HIGH-SEVERITY finding: "bypass" is agent_run's
  # own documented headless-safe permission enum, yet a live delegated
  # child following its own reasoning to use an MCP tool hangs forever
  # under it — the identical mechanism (ACP + StartRun) every real backend
  # (claude/codex/kiro/acp) uses, so this is not expected to be
  # claude-specific; not independently re-verified against codex to avoid
  # spending a second live call proving the same already-evidenced gap.
  # Untag @wip (and drop this note) once the ladder is confirmed to
  # actually resolve a live child's MCP-tool-call permission request.
  @live @wip
  Scenario: A coordinator delegates the same kind of task to two real, differently-vendored engines, and each proves it saw its own context over the real bus
    Given real "claude" and "codex" engines are both available for cross-engine delegation
    When the agent calls tool "agent_run" with:
      | agent  | claude-child |
      | prompt | Look at the additional context available to you in this session (not this message) for the one distinctive marker phrase it contains. Call the MCP tool agent_send with to="parent" and body set to EXACTLY that marker phrase, verbatim and in full, nothing else. Do this now. |
    Then the tool call succeeds
    And "claude-child"'s session harp is remembered
    When the agent calls tool "agent_recv" repeatedly, waiting up to 120s total, until "claude-child" reports
    Then the tool call succeeds
    And the received message is from "claude-child" and its body carries its own guidance, not "codex-child"'s
    When the agent calls tool "agent_run" with:
      | agent  | codex-child |
      | prompt | Look at the additional context available to you in this session (not this message) for the one distinctive marker phrase it contains. Call the MCP tool agent_send with to="parent" and body set to EXACTLY that marker phrase, verbatim and in full, nothing else. Do this now. |
    Then the tool call succeeds
    And "codex-child"'s session harp is remembered
    When the agent calls tool "agent_recv" repeatedly, waiting up to 120s total, until "codex-child" reports
    Then the tool call succeeds
    And the received message is from "codex-child" and its body carries its own guidance, not "claude-child"'s
    When the agent calls tool "agent_send" addressed to "claude-child"'s session with body "Call the MCP tool agent_send with recipient parent and body set to EXACTLY this token, verbatim: J17-LIVE-ECHO-TOKEN-4a6f18. Do this now, then stop."
    Then the tool call succeeds
    When the agent calls tool "agent_recv" repeatedly, waiting up to 120s total, until "claude-child" reports
    Then the tool call succeeds
    And the received message is from "claude-child" and its body contains "J17-LIVE-ECHO-TOKEN-4a6f18"

  # Back to: tests/acceptance/features/j6_delegation.feature (the privilege
  # half of delegation this journey complements).
