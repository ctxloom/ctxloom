Feature: Cross-engine delegation — different engines, different context, a real two-way bus

  ctxloom's differentiator is not "an agent can spawn another agent" — every
  competitor with a coordinator loop can do that. It is that the coordinator
  and its children can ride DIFFERENT engines from DIFFERENT vendors, each
  child sees ONLY its own composed profile (not the coordinator's, not a
  sibling's), and the two sides talk to each other over a real, durable
  message bus, not a synchronous return value. j002100_delegation.feature proved
  the privilege half of that claim (MCP servers, permission modes, the
  journaled audit trail) with `agent_run` alone. This journey proves the
  other half `agent_run` cannot: that two children genuinely see DIFFERENT
  content (asserted on the payload a child itself emits, never a config
  diff), and that `agent_send`/`agent_recv` carry real words between
  coordinator and child.

  # HARNESS/PRODUCT FINDING — read before the scenarios below, it shapes
  # what each tier can honestly claim. Investigating this journey surfaced a
  # real gap of the same shape J002100's own comments already name for
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
  # disk-backed observable j002100_delegation.feature already established for
  # runs.jsonl — never an in-process Go struct, never faked.
  #
  # A SECOND finding surfaced live-verifying the @live scenario below, first
  # recorded here as "a real permission-ladder gap". It was not one: the root
  # cause turned out to be runner WIRING, and the last thing keeping that
  # scenario red after the fix was a consumed codex refresh token on the host.
  # Both are resolved and both are kept, in full, in that scenario's own
  # comment — the misdiagnosis included, because it is the reason the
  # per-engine floor at the bottom of this file exists at all.

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
    When the agent calls tool "agent_send" addressed to "librarian"'s session with body "J002300-ROUNDTRIP-ECHO-TOKEN-6d2e73"
    Then the tool call succeeds
    And the tool result field "disposition" is set
    And "librarian"'s next reported turn carries "J002300-ROUNDTRIP-ECHO-TOKEN-6d2e73"

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
  # j000400_multi_engine.feature's own @live scenario already verified live
  # against these engines — the difference here is the reply crosses the
  # AGENT-TO-AGENT BUS (agent_send/agent_recv), not a synchronous CLI return
  # value, and each child is instructed, in its OWN turn, to make that call
  # itself — a real model decision, not a scripted echo, which is exactly
  # what the hermetic tier's mock backend cannot supply.
  # SELF-SKIPS LOUDLY: the gate step probes claude AND codex independently
  # and names whichever is missing, and how, before spending a single live
  # turn.
  #
  # GREEN END TO END, live-verified 2026-08-12: 18 of 18 steps, both children
  # returned their OWN marker over the bus and the round-trip ECHO token came
  # back. It spent months @wip, and the history below is kept in full because
  # each entry names a real defect this scenario caught — and the last one is
  # the reminder that a red @live row is not automatically a product bug.
  # History, live-verified 2026-07-22 (task woozy-hasty-karma):
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
  #     token was already used" (401 refresh_token_reused).
  #
  # RESOLVED 2026-08-12 — and it was never a ctxloom defect, exactly as the
  # entry above judged. The host's own `codex exec`, with no ctxloom in the
  # picture, failed with the identical 401; a human ran `codex login`; this
  # scenario then passed unchanged, no product change of any kind. Read a
  # future red here with that precedent in hand: a runner-exit body carrying a
  # 401 means re-authenticate the engine, and only a body that is neither the
  # marker nor a credential error is evidence against ctxloom. The per-engine
  # floor below now guards each engine of that pair separately, so a repeat of
  # this failure names ONE engine instead of taking the pair down together.
  @live
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
    When the agent calls tool "agent_send" addressed to "claude-child"'s session with body "Call the MCP tool agent_send with recipient parent and body set to EXACTLY this token, verbatim: J002300-LIVE-ECHO-TOKEN-4a6f18. Do this now, then stop."
    Then the tool call succeeds
    When the agent calls tool "agent_recv" repeatedly, waiting up to 120s total, until "claude-child" reports
    Then the tool call succeeds
    And the received message is from "claude-child" and its body contains "J002300-LIVE-ECHO-TOKEN-4a6f18"

  # THE PER-ENGINE FLOOR — one live row per engine ctxloom 0.7 can delegate to.
  #
  # WHY IT EXISTS. Every scenario above proves delegation against either the
  # mock (hermetic) or the claude/codex PAIR (@live). Neither answers the
  # question an operator actually asks before trusting `agent_run` on their own
  # box: "does a delegated child on MY engine really launch, really receive its
  # composed context, and really get a word back to its coordinator?" Until
  # this outline existed, three of the four 0.7 engines had never had a full
  # live delegation round trip verified AT ALL — opencode's child path was
  # migrated onto the StartRun/runner model (coord.viaStartRunBackends now
  # carries claude-code, codex, kiro, acp and opencode) with no live proof
  # behind it, and kiro's was proven only at the isolation layer. A per-engine
  # matrix, in the suite's own live lane, is the difference between "the code
  # path exists" and "the engine came back".
  #
  # WHAT EACH ROW PROVES, AND WHY IT CANNOT BE FAKED. The marker phrase exists
  # in exactly ONE place: a fragment in the child's OWN bundle, written fresh
  # by the gate step into this scenario's isolated project. It is NOT in the
  # prompt (read it — the prompt only says "the one distinctive marker phrase
  # in your context"), so an engine that echoes what it was sent cannot
  # produce it. It reaches the coordinator only if (a) the child process
  # really launched on that engine, (b) ctxloom really delivered the composed
  # profile context into its first turn, (c) the engine really reasoned over
  # that context, and (d) the child really reached back through its forwarder
  # MCP server to call agent_send(to:"parent"). The assertion is on the BODY
  # BYTES that arrive in the coordinator's mailbox, never on an exit code and
  # never on agent_run's own success — which this journey's own history
  # already showed is worth nothing on its own (agent_run kept returning
  # success for weeks while the empty-coordinator-harp defect silently ate
  # every reply). A child that launches and dies still delivers a message —
  # bridgeTurnResult queues its runner-exit report to the same mailbox — so
  # "a message arrived from the child" is deliberately NOT the assertion; the
  # marker in the body is.
  #
  # GATING. Each row probes ITS OWN engine through the same
  # live_engine_registry.go decision every other @live step uses, and a row
  # whose engine is missing or unauthenticated skips with the engine and the
  # reason printed by name — never silently. CTXLOOM_LIVE_REQUIRE turns any
  # named engine's skip into a hard failure (checkRequiredEngines), which is
  # how a credential expiry is stopped from quietly deleting a row.
  #
  # ONE ROW AT A TIME. Each Examples block carries its own @<engine> tag —
  # the addressing mechanism isolation_probe.feature established, and used
  # here for the same reason: `just live-delegation <engine>` runs exactly one
  # engine's row, which is what a live, paid, minutes-long turn wants. Tags
  # attach to an Examples: block, not to a row inside one, so each engine gets
  # its own single-row block.
  @live @delegation
  Scenario Outline: A delegated child on a real <engine> reports back a marker only its own composed context could supply
    Given a real "<engine>" engine is available for a delegated child carrying marker "<marker>"
    When the agent calls tool "agent_run" with:
      | agent  | delegate |
      | prompt | Look at the additional context available to you in this session (not this message) for the one distinctive marker phrase it contains. Call the MCP tool agent_send with to="parent" and body set to EXACTLY that marker phrase, verbatim and in full, nothing else. Do this now. |
    Then the tool call succeeds
    And "delegate"'s session harp is remembered
    When the agent calls tool "agent_recv" repeatedly, waiting up to 240s total, until "delegate" reports a body containing "<marker>"
    Then the tool call succeeds

    @claude-code
    Examples:
      | engine      | marker                                       |
      | claude-code | J002300-DELEGATE-MARKER-CLAUDE-CODE-1d4c07ab |

    # GREEN, but it took a human to get here, and the detour is worth keeping:
    # this row's FIRST live run was red, and not for any reason in this repo.
    # The child launched, the runner dialled home, and a message really did
    # reach the coordinator's mailbox from the child's harp — the delegation
    # path worked — but the body was a runner-exit report carrying codex's own
    # 401 refresh_token_reused. Confirmed HOST-side, independent of ctxloom, by
    # running `codex exec` with no ctxloom in the picture: identical 401. A
    # human ran `codex login`, and the row returned its own marker unchanged.
    #
    # THE PROBE GAP THAT DETOUR EXPOSED IS STILL REAL (live_engine_registry.go):
    # the availability report said `codex ✓` throughout, because
    # authCheckCodex's `codex login status` is a LOCAL read of auth.json that
    # never attempts a refresh — INSTALLED and AUTHENTICATED are distinguished,
    # AUTHENTICATED and STILL-VALID are not. So a consumed refresh token
    # surfaces here as a loud RED row rather than a named skip. That is the
    # honest failure shape (a skip would be worse), and there is no cheap fix:
    # the only probe that would know is one that performs a refresh, which is
    # what consumes the token. If this row ever goes red again with a 401 in the
    # body, read it as "re-run `codex login`", not as a delegation regression.
    @codex
    Examples:
      | engine | marker                                 |
      | codex  | J002300-DELEGATE-MARKER-CODEX-8b3f52cd |

    @kiro
    Examples:
      | engine | marker                                |
      | kiro   | J002300-DELEGATE-MARKER-KIRO-2e9a16ef |

    # @wip — A REAL PRODUCT GAP in opencode's delegated-child path, measured
    # 2026-08-12 and deliberately NOT routed around. opencode child delegation
    # was migrated onto the StartRun/runner model in the spool cutover's S3b
    # slice (coord.viaStartRunBackends["opencode"] == true) and a full live
    # round trip had never been run behind it. It does not work: in 240s,
    # ZERO messages reached the coordinator's mailbox from the child's harp —
    # not the child's own agent_send, not coord/children.go's bridgeTurnResult
    # copy of its turn output, and not even a runner-exit report. Contrast the
    # codex row above, which delivered a runner-exit report through that exact
    # machinery, so the mailbox path itself is not the suspect.
    #
    # THE ENGINE IS NOT THE SUSPECT EITHER — both halves were isolated:
    #   - `opencode run --model openrouter/openai/gpt-oss-20b:free` answers
    #     normally on this host (credentials, provider and pinned model fine);
    #   - `ctxloom run --agent delegate --one-shot` against THIS row's exact
    #     agent/profile/bundle fixture returns the marker verbatim, so ctxloom's
    #     opencode engine wiring AND its composed-context delivery are fine on
    #     the run path.
    # What is unproven is opencode as a DELEGATED CHILD specifically. Untag when
    # a live opencode child returns its own marker here; until then this row is
    # the standing, addressable proof that it does not.
    @opencode @wip
    Examples:
      | engine   | marker                                    |
      | opencode | J002300-DELEGATE-MARKER-OPENCODE-7f05b391 |

  # Back to: tests/acceptance/features/j002100_delegation.feature (the privilege
  # half of delegation this journey complements).
