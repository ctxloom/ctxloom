Feature: Cross-engine delegation — different engines, different context, a real two-way bus

  ctxloom's differentiator is not "an agent can spawn another agent" — every
  competitor with a coordinator loop can do that. It is that the coordinator
  and its children can ride DIFFERENT engines from DIFFERENT vendors, each
  child sees ONLY its own composed profile (not the coordinator's, not a
  sibling's), and the two sides talk to each other over a real, durable
  message bus, not a synchronous return value. j21_delegation.feature proved
  the privilege half of that claim (MCP servers, permission modes, the
  journaled audit trail) with `agent_run` alone. This journey proves the
  other half `agent_run` cannot: that two children genuinely see DIFFERENT
  content (asserted on the payload a child itself emits, never a config
  diff), and that `agent_send`/`agent_recv` carry real words between
  coordinator and child.

  # HARNESS/PRODUCT FINDING — read before the scenarios below, it shapes
  # what each tier can honestly claim. Investigating this journey surfaced a
  # real gap of the same shape J21's own comments already name for
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
  # CORRECTED 2026-08-03 — the paragraph above is right about agent_send and
  # WRONG about the direction as a whole. A chat child indeed cannot decide
  # to call agent_send without a reasoning loop, but that was never the only
  # child->parent path: coord/children.go's bridgeTurnResult queues EVERY
  # child's turn output to its parent's mailbox unconditionally, reasoning or
  # not. So the direction IS hermetically provable, via the coordinator's own
  # agent_recv — see "A delegated child's own turn result reaches the
  # coordinator's mailbox over the bus" below. What had hidden this is that
  # the bridge was silently failing for every scenario in this suite (the
  # empty-coordinator-harp defect, fixed 2026-08-03; see the @live scenario's
  # comment). Only agent_send-by-model-decision needs a real engine.
  #
  # Hermetically, this journey ALSO reads each child's OWN canonical transcript
  # (~/.ctxloom/sessions/<harp>/persist/transcript.jsonl —
  # internal/transcript/record.go's documented, first-party schema, not a
  # scrape) to prove distinct context and the coordinator->child half of the
  # bus: a REAL agent_send call, content verified in the child's own
  # recorded next turn. This is the SAME class of durable, external,
  # disk-backed observable j21_delegation.feature already established for
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
    When the agent calls tool "agent_send" addressed to "librarian"'s session with body "J23-ROUNDTRIP-ECHO-TOKEN-6d2e73"
    Then the tool call succeeds
    And the tool result field "disposition" is set
    And "librarian"'s next reported turn carries "J23-ROUNDTRIP-ECHO-TOKEN-6d2e73"

  # LOCKED — the CHILD->coordinator half of requirement 4, hermetically. The
  # header finding above claims this direction "is provable only against a
  # REAL reasoning engine". That claim is FALSE, and this scenario is the
  # counter-example: a chat child never calls agent_send itself, but
  # coord/children.go's bridgeTurnResult ALREADY queues every child's turn
  # output to its parent's mailbox automatically, so the coordinator's own
  # agent_recv observes the child's words over the real, durable bus with no
  # model reasoning involved. The observable is the mailbox message body —
  # the same payload class the @live tier asserts — never a transcript read
  # and never an in-process struct.
  #
  # BREAK-POINT: this is the regression gate for the empty-coordinator-harp
  # defect (see the @live scenario's comment). Revert
  # internal/cli/selfIdentityFromEnv's minted-harp fallback and this goes red
  # for exactly that reason — agent_recv drains role "" forever while
  # agent_run still reports success.
  Scenario: A delegated child's own turn result reaches the coordinator's mailbox over the bus
    Given Alice's coordinator can delegate to two agents, "librarian" and "cartographer", each carrying its own distinct guidance in its own profile
    When the agent calls tool "agent_run" with:
      | agent  | librarian |
      | prompt | go        |
    Then the tool call succeeds
    And "librarian"'s session harp is remembered
    When the agent calls tool "agent_recv" repeatedly, waiting up to 20s total, until "librarian" reports
    Then the tool call succeeds
    And the received message is from "librarian" and its body carries its own guidance, not "cartographer"'s

  # @live, both requirement 2 (genuine cross-ENGINE, the claim the hermetic
  # tier above explicitly declines — claude-code and codex, the two
  # proven-working pair; the isolation probe just passed both, both axes)
  # and the child->coordinator half of requirement 4 the hermetic tier
  # structurally cannot reach (see the finding above). Each child's marker
  # phrase lives in its OWN materialized profile context, the exact "repeat
  # the marker you can see in your context" technique
  # j4_multi_engine.feature's own @live scenario already verified live
  # against these engines — the difference here is the reply crosses the
  # AGENT-TO-AGENT BUS (agent_send/agent_recv), not a synchronous CLI return
  # value, and each child is instructed, in its OWN turn, to make that call
  # itself — a real model decision, not a scripted echo, which is exactly
  # what the hermetic tier's mock backend cannot supply.
  # SELF-SKIPS LOUDLY: the gate step probes claude AND codex independently
  # and names whichever is missing, and how, before spending a single live
  # turn.
  #
  # @wip — the CLAUDE half of this scenario is now GREEN; a single
  # CODEX-specific vendor gap keeps the whole thing @wip. History and
  # current state, live-verified 2026-07-22 (task woozy-hasty-karma):
  #
  # ORIGINAL finding (icy-value), now FIXED: a live claude-haiku-4-5 child
  # decided to call agent_send, emitted a tool_use for
  # mcp__ctxloom__agent_send, and its PermissionRequest parked forever
  # (90s+, twice) — never resolved despite a `[{"action":"auto_accept"}]`
  # ladder. ROOT CAUSE was NOT the approval ladder (a full-stack
  # reproduction — real internal/acp driver + real ACP subprocess + real
  # gRPC RunChannel + coordinator — resolves it every time; see
  # internal/agentcoord/coord/acp_approval_test.go). It was runner WIRING:
  # internal/cli/llm_serve.go bound the engine host (which unblocks
  # StartRun -> the engine spawn) BEFORE exporting CTXLOOM_MCP_SOCKET, so
  # the child engine could spawn with no reach-back socket; its `ctxloom
  # mcp` shim then ran its LOCAL surface — a second, rogue in-process
  # coordinator — and claude-code-acp's own MCP-tool permission flow stalls
  # on that mis-wired server. FIX: export the socket (and fail loud if it
  # can't be stood up) BEFORE BindHome. With it, the claude child now
  # reports its marker over the real bus — the two claude assertions here
  # pass.
  #
  # SUPERSEDED 2026-08-03 — the "codex-acp drops the stdio server's env"
  # diagnosis recorded here previously is NOT what blocks this scenario, and
  # a live re-run found no evidence for it: no "this session is the
  # coordinator — it has no parent" was raised by either child, and no rogue
  # local coordinator stood up. What was actually broken was
  # ENGINE-INDEPENDENT and had been failing silently in the hermetic suite
  # too: the bare-`ctxloom mcp` coordinator ran with an EMPTY Identity.Harp
  # (internal/cli's selfIdentityFromEnv read CTXLOOM_SESSION_HARP, which
  # `ctxloom run` exports but the .mcp.json entry `manage install` writes
  # does not). The harp IS the coordinator's mailbox address, so with it
  # empty, bridgeTurnResult's mail was refused at queueMailPayloadID's
  # `to == ""` guard and childSend's `parent == ""` arm rejected any
  # agent_send(to:"parent") — while agent_run kept returning success. Fixed
  # 2026-08-03 by minting a harp when no ambient session supplies one;
  # gated hermetically by the bridge scenario above.
  #
  # STATE AFTER THAT FIX, live-verified 2026-08-03 on this host:
  #   - CLAUDE half: GREEN end to end. The claude child decided to call
  #     agent_send, the coordinator's agent_recv returned its OWN marker,
  #     and the round-trip ECHO token came back over the bus. All four
  #     claude assertions pass (12 of 18 steps, up from 4).
  #   - CODEX half: the BUS works — agent_recv really did return a message
  #     from codex-child — but the body is a runner-exit report, not the
  #     marker, because the codex ENGINE could not authenticate:
  #     "Your access token could not be refreshed because your refresh
  #     token was already used" (401 refresh_token_reused). That is a
  #     credential-environment failure on this host, not a ctxloom defect,
  #     and it is the one thing still keeping this scenario @wip.
  # Untag @wip once a codex-child with WORKING credentials returns its own
  # marker phrase here. No product change is known to be required.
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
    When the agent calls tool "agent_send" addressed to "claude-child"'s session with body "Call the MCP tool agent_send with recipient parent and body set to EXACTLY this token, verbatim: J23-LIVE-ECHO-TOKEN-4a6f18. Do this now, then stop."
    Then the tool call succeeds
    When the agent calls tool "agent_recv" repeatedly, waiting up to 120s total, until "claude-child" reports
    Then the tool call succeeds
    And the received message is from "claude-child" and its body contains "J23-LIVE-ECHO-TOKEN-4a6f18"

  # Back to: tests/acceptance/features/j21_delegation.feature (the privilege
  # half of delegation this journey complements).
