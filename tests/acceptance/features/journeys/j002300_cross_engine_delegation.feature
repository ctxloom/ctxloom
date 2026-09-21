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

  # WHAT THE HERMETIC TIER READS, and who writes it. Both observables below
  # are produced by the child's OWN runner process, never by the coordinator:
  # each child's canonical transcript
  # (~/.ctxloom/sessions/<harp>/persist/transcript.jsonl —
  # internal/adapters/transcript/record.go's documented, first-party schema, not a
  # scrape) proves distinct context and the coordinator->child half of the
  # bus (a REAL agent_send call, content verified in the child's own recorded
  # next turn); the coordinator's own mailbox, read through agent_recv, proves
  # the child->coordinator half through the runner's automatic turn report
  # (coord.EngineHost, spoolturnresult.go) — no model reasoning involved. Only
  # agent_send-by-model-decision needs a real engine, and that is the @live
  # tier's job. The @negative-probe scenario withholds the runner and shows
  # neither observable survives its absence.
  #
  # A finding surfaced live-verifying the @live scenario below, first
  # recorded here as "a real permission-ladder gap". It was not one: the root
  # cause turned out to be runner WIRING, and the last thing keeping that
  # scenario red after the fix was a consumed refresh token on the host.
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
  @reach-back @R1
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

  # LOCKED — the CHILD->coordinator half of requirement 4, hermetically. A
  # chat child never calls agent_send itself, but its runner writes every
  # turn's output to the parent's mailbox as the automatic turn report
  # (coord.EngineHost's ReportTurnResult, spoolturnresult.go), so the
  # coordinator's own agent_recv observes the child's words over the real,
  # durable bus with no model reasoning involved. The observable is the
  # mailbox message body — the same payload class the @live tier asserts —
  # never a transcript read and never an in-process struct.
  #
  # BREAK-POINT: this is the regression gate for the empty-coordinator-harp
  # defect (see the @live scenario's comment). Revert
  # selfIdentityFromEnv's minted-harp fallback and this goes red
  # for exactly that reason — agent_recv drains role "" forever while
  # agent_run still reports success.
  @reach-back @R2
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

  # LOCKED — the CHILD->coordinator half of requirement 4 WITHOUT A RECEIVE.
  # @R2 above proves the child's turn result reaches the owner's spool; this
  # proves the owner is HANDED it at its next turn by the turn-start hook
  # (`ctxloom hook mail-drain`), the one reader of the owner's in/ — no
  # agent_recv, nothing parked. The hook is invoked here exactly as an engine
  # invokes it: a subprocess with the owner's harp in its environment and the
  # engine's turn-start payload on stdin; what it writes to stdout is what the
  # engine injects as that turn's context. Delivery is proven on the PAYLOAD
  # (the child's own guidance, under the coordinator's provenance header
  # naming the child's harp), on DISK (the file has moved to in/consumed/ and
  # nothing is left pending), and by a second turn-start that finds nothing —
  # once per delivery, which a receive loop never had.
  #
  # The owner's harp is pinned by the scenario BEFORE the coordinator starts:
  # the stdio coordinator takes its own identity from CTXLOOM_SESSION_HARP
  # (selfIdentityFromEnv), and that identity IS the spool the hook reads.
  #
  # CONTAINER AXIS EXCLUDED, stated rather than discovered: a containerized
  # claude never receives ctxloom's hooks at all (pulmonary-eternity), so this
  # delivery is inert for a container-hosted coordinator until that lands. The
  # coordinator here is a host process, which is the axis this claim covers.
  @reach-back @R2
  Scenario: A delegated child's report reaches the coordinator's next turn through the turn-start hook, with no receive
    Given Alice's coordinator can delegate to two agents, "librarian" and "cartographer", each carrying its own distinct guidance in its own profile
    And the session harp is "quiet-copper-heron"
    When the agent calls tool "agent_run" with:
      | agent  | librarian |
      | prompt | go        |
    Then the tool call succeeds
    And "librarian"'s session harp is remembered
    And the coordinator's own spool holds "librarian"'s report within 20s
    When I run "ctxloom hook mail-drain" with input:
      """
      {"session_id":"vendor-session-1","hook_event_name":"UserPromptSubmit","prompt":"what did the child say?"}
      """
    Then the command succeeds
    And the drained turn context carries "librarian"'s report with its own guidance, not "cartographer"'s
    And the coordinator's own spool shows "librarian"'s report consumed, with nothing pending
    When I run "ctxloom hook mail-drain" with input:
      """
      {"session_id":"vendor-session-1","hook_event_name":"UserPromptSubmit","prompt":"and now?"}
      """
    Then the command succeeds
    And the hook writes nothing to stdout

  # THE NEGATIVE PROBE for the two hermetic bus scenarios above. Both are
  # green only because a REAL runner process stands for the child: the
  # coordinator self-execs one per delegated run, and it is that process — not
  # the coordinator — that opens the child's transcript and writes the
  # automatic turn report the coordinator's mailbox receives
  # (coord.EngineHost, spoolturnresult.go). A green run cannot show that
  # dependency on its own: a shim answering in-process for the runner would
  # produce the same bytes. So this scenario WITHHOLDS the runner and asserts
  # that the same agent_run, on the same fixture, then yields NEITHER
  # observable — the mailbox carries the launch failure with the withheld
  # runner's own dying words in the body and no result at all, and the child's
  # transcript never gains a turn.
  #
  # HOW THE RUNNER IS WITHHELD, with no product seam: the coordinator is
  # started from a copy of the binary that is unlinked once its MCP handshake
  # completes, so its self-lookup (selfexec.Path) takes its documented
  # upgrade-in-place fallback — a PATH lookup — and PATH is led by a decoy
  # `ctxloom` that prints a marker and exits non-zero. Everything else about
  # the fixture is R1/R2's.
  #
  # FORCED, NOT AWAITED: the failure notice is queued by the run's terminal,
  # after which nothing can write that child's transcript, so "recorded no
  # turn" reads a settled state rather than racing one.
  @negative-probe
  Scenario: With the runner withheld, the same delegation yields a launch failure in the coordinator's mailbox and no recorded turn
    Given Alice's coordinator can delegate to two agents, "librarian" and "cartographer", each carrying its own distinct guidance in its own profile
    And the coordinator's runner is withheld
    When the agent calls tool "agent_run" with:
      | agent  | librarian |
      | prompt | go        |
    Then the tool call succeeds
    And "librarian"'s session harp is remembered
    When the agent calls tool "agent_recv" repeatedly, waiting up to 20s total, until "librarian" reports
    Then the tool call succeeds
    And the received message from "librarian" is the withheld runner's launch failure, and no result carrying its guidance arrived
    And "librarian" recorded no turn

  # @live, both requirement 2 (genuine cross-ENGINE, the claim the hermetic
  # tier above explicitly declines — the
  # proven-working pair; the isolation probe just passed both, both axes)
  # and the one part of requirement 4 the hermetic tier cannot supply: a
  # child that DECIDES to call agent_send itself. Each child's marker
  # phrase lives in its OWN materialized profile context, the exact "repeat
  # the marker you can see in your context" technique
  # j000400_multi_engine.feature's own @live scenario already verified live
  # against these engines — the difference here is the reply crosses the
  # AGENT-TO-AGENT BUS (agent_send/agent_recv), not a synchronous CLI return
  # value, and each child is instructed, in its OWN turn, to make that call
  # itself — a real model decision, not a scripted echo, which is exactly
  # what the hermetic tier's mock backend cannot supply.
  # SELF-SKIPS LOUDLY: the gate step probes each engine independently
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
  # reproduction resolved it every time). It was runner WIRING:
  # internal/adapters/cli/llm_serve.go bound the engine host (which unblocks
  # StartRun -> the engine spawn) BEFORE exporting CTXLOOM_MCP_SOCKET, so
  # the child engine could spawn with no reach-back socket; its `ctxloom
  # mcp` shim then ran its LOCAL surface — a second, rogue in-process
  # coordinator — and the child engine's own MCP-tool permission flow stalls
  # on that mis-wired server. FIX: export the socket (and fail loud if it
  # can't be stood up) BEFORE BindHome. With it, the claude child now
  # reports its marker over the real bus — the two claude assertions here
  # pass.
  #
  # AN EMPTY COORDINATOR HARP SILENTLY EATS MAIL, and it is
  # ENGINE-INDEPENDENT: the bare-`ctxloom mcp` coordinator can run with an
  # EMPTY Identity.Harp
  # (internal/adapters/cli's selfIdentityFromEnv read CTXLOOM_SESSION_HARP, which
  # `ctxloom run` exports but the .mcp.json entry `manage install` writes
  # does not). The harp IS the coordinator's mailbox address, so with it
  # empty, bridgeTurnResult's mail is refused at queueMailPayloadID's
  # `to == ""` guard and childSend's `parent == ""` arm rejects any
  # agent_send(to:"parent") — while agent_run keeps returning success. A harp
  # is minted when no ambient session supplies one; gated hermetically by the
  # bridge scenario above.
  #
  # STATE AFTER THAT FIX, live-verified 2026-08-03 on this host:
  #   - CLAUDE half: GREEN end to end. The claude child decided to call
  #     agent_send, the coordinator's agent_recv returned its OWN marker,
  #     and the round-trip ECHO token came back over the bus. All four
  #     claude assertions pass (12 of 18 steps, up from 4).
  #   - CODEX half: the BUS works — agent_recv really did return a message
  #     from the second child — but the body is a runner-exit report, not the
  #     marker, because that ENGINE could not authenticate:
  #     "Your access token could not be refreshed because your refresh
  #     token was already used" (401 refresh_token_reused).
  #
  # RESOLVED 2026-08-12 — and it was never a ctxloom defect, exactly as the
  # entry above judged. The host's own engine invocation, with no ctxloom in
  # the picture, failed with the identical 401; a human re-authenticated; this
  # scenario then passed unchanged, no product change of any kind. Read a
  # future red here with that precedent in hand: a runner-exit body carrying a
  # 401 means re-authenticate the engine, and only a body that is neither the
  # marker nor a credential error is evidence against ctxloom. The per-engine
  # floor below now guards each engine of that pair separately, so a repeat of
  # this failure names ONE engine instead of taking the pair down together.
  # PARKED — this scenario needs TWO engines that can each ACT as an agent:
  # read the context they were given and call agent_send back. Only one such
  # engine ships. The registered double cannot stand in — it reports which
  # surfaces it received, it does not reason over them — so repointing the
  # second child at it would assert a reply nothing can produce.
  #
  # What is NOT in doubt is the machinery: per-child profile isolation and the
  # two-way bus are exercised by the hermetic rows below. What goes unproven
  # meanwhile is the differently-vendored half, which is the headline claim.
  #
  # UNTAG WHEN: a second engine that can act as an agent is available.
  @live @wip
  Scenario: A coordinator delegates the same kind of task to two real, differently-vendored engines, and each proves it saw its own context over the real bus
    Given real "claude" and a second agent-capable engine are both available for cross-engine delegation
    When the agent calls tool "agent_run" with:
      | agent  | claude-child |
      | prompt | Look at the additional context available to you in this session (not this message) for the one distinctive marker phrase it contains. Call the MCP tool agent_send with to="parent" and body set to EXACTLY that marker phrase, verbatim and in full, nothing else. Do this now. |
    Then the tool call succeeds
    And "claude-child"'s session harp is remembered
    When the agent calls tool "agent_recv" repeatedly, waiting up to 120s total, until "claude-child" reports
    Then the tool call succeeds
    And the received message is from "claude-child" and its body carries its own guidance, not "second-child"'s
    When the agent calls tool "agent_run" with:
      | agent  | second-child |
      | prompt | Look at the additional context available to you in this session (not this message) for the one distinctive marker phrase it contains. Call the MCP tool agent_send with to="parent" and body set to EXACTLY that marker phrase, verbatim and in full, nothing else. Do this now. |
    Then the tool call succeeds
    And "second-child"'s session harp is remembered
    When the agent calls tool "agent_recv" repeatedly, waiting up to 120s total, until "second-child" reports
    Then the tool call succeeds
    And the received message is from "second-child" and its body carries its own guidance, not "claude-child"'s
    When the agent calls tool "agent_send" addressed to "claude-child"'s session with body "Call the MCP tool agent_send with recipient parent and body set to EXACTLY this token, verbatim: J002300-LIVE-ECHO-TOKEN-4a6f18. Do this now, then stop."
    Then the tool call succeeds
    When the agent calls tool "agent_recv" repeatedly, waiting up to 120s total, until "claude-child" reports
    Then the tool call succeeds
    And the received message is from "claude-child" and its body contains "J002300-LIVE-ECHO-TOKEN-4a6f18"

  # THE PER-ENGINE FLOOR — one live row per engine ctxloom 0.7 can delegate to.
  #
  # WHY IT EXISTS. Every scenario above proves delegation against either the
  # mock (hermetic) or a real engine PAIR (@live). Neither answers the
  # question an operator actually asks before trusting `agent_run` on their own
  # box: "does a delegated child on MY engine really launch, really receive its
  # composed context, and really get a word back to its coordinator?" Until
  # this outline existed, two of the three 0.7 engines had never had a full
  # live delegation round trip verified AT ALL — that child path was
  # migrated onto the StartRun/runner model (see coord.viaStartRunBackends)
  # with no live proof behind it. A per-engine
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
  # the run's terminal notice reaches the same mailbox — so
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
  # P6 — THE STEER ECHO. Capability-probe ladder rung p6-steer-echo
  # (tests/acceptance/capability_probe_registry.go), living here rather than in a
  # file of its own because it extends this journey's machinery rather than
  # duplicating it: the gate, the harp-remembering step and the payload-draining
  # agent_recv above are all reused verbatim, and the LOCKED scenarios are left
  # exactly as they are.
  #
  # WHAT IT ADDS TO THE FLOOR ABOVE. The per-engine floor proves the
  # CHILD -> COORDINATOR direction on all three engines: a marker that exists
  # only in the child's own composed context reaches the coordinator's mailbox.
  # The other direction — the coordinator reaching INTO a live session
  # mid-flight, and the child acting on what it was handed — was proven for
  # claude-code alone, by the J002300-LIVE-ECHO-TOKEN step of the @live
  # cross-engine scenario above. Capability-inventory row 13 records the
  # removed engines as claimed-and-unproven for exactly that half. These three rows
  # are that gap.
  #
  # THE CHANNEL IS THE BUS MESSAGE BODY, AND ONLY THAT. The value the child must
  # produce is a harp minted per cell at fixture time (probeHarps.Mint) and
  # handed to it in ONE place: the body of an agent_send made AFTER it is
  # already running. It is not in the composed context, not in the spawn prompt,
  # not in the environment. So a child that returns it has necessarily received
  # a mid-session message — which is the whole claim — and no amount of prompt
  # compliance can fake it.
  #
  # WHY EACH ROW STILL PLANTS A CONTEXT MARKER. The wake-up half (the marker
  # column, the same technique the floor above uses) is not redundant: it proves
  # the child launched, received its context and can reach its coordinator
  # BEFORE a steer is spent, so a red on the echo cannot be confused with a
  # child that never woke up. The two values are deliberately different, and the
  # verdict function's own unit tests include the false-green where a child
  # repeats its context marker instead of the steer (probe_p6_steer_echo_test.go,
  # TestP6AssertEcho_ForeignChannelCannotFalseGreenIt).
  #
  # THE SPOOL ASSERTION IS THE MAIL PLANE'S BEHAVIOURAL PROOF IN THIS SUITE.
  # The file spool is the only carrier, so the final step asserts, on PAYLOAD
  # BYTES, that the coordinator's steer exists as a FILE in the child's own
  # spool in plane. "The echo came back" is not enough on its own: a carrier
  # that delivered by some path nobody meant, while writing nothing, is this
  # project's characteristic bug wearing the spool's clothes.
  #
  # MEASURED 2026-08-13, EVERY ROW GREEN. The
  # claude-code row re-proves what the LOCKED scenario above already proves,
  # against a coordinator->child mid-session steer that had been claimed and
  # never demonstrated (capability inventory row 13 read "marker only; no
  # mid-session steer"). Each child returned the minted harp as
  # its whole message body. Each row's assertion was then mutated on the
  # ASSERTION SIDE ONLY — the verdict made to look for harp+"-MUTANT" while the
  # fixture still sent the real one — and each went RED with a BUS-DELIVERY
  # shape, so no row is passing because the check cannot fail.
  #
  # WHAT THE SPOOL CENSUS ACTUALLY SHOWED, on every one of the three: three
  # message files, and BOTH directions on disk.
  #
  #   in/consumed   1: <ts>.00000001.coord.md            <- the coordinator's steer
  #   out/consumed  2: <ts>.00000001.<childharp>.md      <- the child's wake-up reply
  #                    <ts>.00000002.<childharp>.md      <- the child's echo
  #
  # Two things worth reading off that. First, the steer file is in CONSUMED, not
  # in/: the rename IS the child runner's acknowledgement, so the full
  # at-least-once cycle completed rather than a file merely being dropped in a
  # directory. Second, the OUT plane carried the child's traffic too, harp and
  # all — so the cutover is running in both directions here, not only the one
  # this step asserts. That is recorded rather than asserted on purpose: P6's
  # claim is the coordinator's steer, and a cell must not silently become the
  # regression gate for a neighbouring subsystem's scope. If child->parent file
  # delivery is meant to be guaranteed, that belongs in its own assertion.
  #
  # TIMING, for whoever tunes the budget: an echo turn has landed as much as 79
  # seconds after the steer on a slow engine. Do not shorten 240s on the
  # strength of a fast one.
  #
  # ONE ROW AT A TIME, two paid turns each (the wake-up and the steer). Address
  # exactly one cell with the registry's own tag expression:
  #   ACCEPTANCE_TAGS="@live && @probe-p6-steer-echo && @claude-code && @host && @ws-none"
  # (probeCell.TagExpression renders it; the `just capability-probe` wrapper is
  # slice S10's, not this one's).
  @live @probe-p6-steer-echo
  Scenario Outline: A delegated child on a real <engine> echoes back a harp the coordinator steered into its live session
    Given a real "<engine>" engine on runtime "<runtime>" with workspace "<workspace>" is available for a steer-echo child carrying wake marker "<marker>"
    When the agent calls tool "agent_run" with:
      | agent  | delegate |
      | prompt | Look at the additional context available to you in this session (not this message) for the one distinctive marker phrase it contains. Call the MCP tool agent_send with to="parent" and body set to EXACTLY that marker phrase, verbatim and in full, nothing else. Do this now. |
    Then the tool call succeeds
    And "delegate"'s session harp is remembered
    When the agent calls tool "agent_recv" repeatedly, waiting up to 240s total, until "delegate" reports a body containing "<marker>"
    Then the tool call succeeds
    When the agent calls tool "agent_send" addressed to "delegate"'s session carrying this cell's minted steer harp
    Then the tool call succeeds
    When the agent calls tool "agent_recv" repeatedly, waiting up to 240s total, until "delegate" echoes this cell's minted steer harp
    Then the tool call succeeds
    And the coordinator's steer is on disk in "delegate"'s own spool, in a file carrying that harp

    @claude-code @host @ws-none
    Examples:
      | engine      | runtime | workspace | marker                                |
      | claude-code | host    | none      | P6-WAKE-MARKER-CLAUDE-CODE-5b1e07c4   |

    # THE ISOLATED CELL. Every row above runs host/none — isolated on NEITHER
    # axis — so they prove the round trip works, not that it survives the
    # isolation boundary. This row is the same eleven steps with the child's
    # PROCESS in a rootless container and its FILES in a worktree: the two axes
    # are independent (see j002200_isolation.feature's header), and answering
    # "does delegation still work when isolated" needs both set at once, not
    # either alone.
    #
    # It is NOT a relabelling of the rows above. steps_p6_steer_echo.go refuses
    # any pair p6BuildableCells does not declare, so this row runs a genuinely
    # different fixture — `runtime: container-rootless` on the agent binding,
    # `workspace: worktree` at the top level, and the fixture COMMITTED, because
    # a worktree spawn refuses a dirty checkout.
    #
    # container-rootful is absent rather than declared-and-skipped: no box this
    # suite has run on has had a reachable rootful daemon, and a row that can
    # only ever skip looks like coverage.
    @claude-code @container-rootless @ws-worktree @reach-back @R4
    Examples:
      | engine      | runtime            | workspace | marker                                    |
      | claude-code | container-rootless | worktree  | P6-WAKE-MARKER-CLAUDE-CODE-CTRWT-9d4f21ab |

    # THE TWO MIXED CORNERS, completing the matrix. host/none and
    # container-rootless/worktree prove the bus with BOTH boundaries off and
    # BOTH on; these prove it with exactly one present. The axes are
    # independent, so a bus that survives the combination could still fail on a
    # single boundary — worktree alone relocates the child's files and its
    # config home, container alone relocates its process and its credentials,
    # and each could break the mail plane on its own.
    @claude-code @host @ws-worktree
    Examples:
      | engine      | runtime | workspace | marker                                   |
      | claude-code | host    | worktree  | P6-WAKE-MARKER-CLAUDE-CODE-HOSTWT-3e7c15 |

    @claude-code @container-rootless @ws-none @reach-back @R3
    Examples:
      | engine      | runtime            | workspace | marker                                   |
      | claude-code | container-rootless | none      | P6-WAKE-MARKER-CLAUDE-CODE-CTRNONE-b82a4 |

  # Back to: tests/acceptance/features/journeys/j002100_delegation.feature (the privilege
  # half of delegation this journey complements).
