@doc
Feature: Coordinator delegates isolated work

  A coordinator that fans work out to delegated children is only as safe as
  the boundary between them. If a child agent Alice spawns to fix one thing
  can quietly reach the MCP server she gave to a DIFFERENT child — or the one
  she reserved for herself — then "delegation" is just a shared blast radius
  wearing a different name. ctxloom's answer is that each child's privileges
  are its OWN, resolved from its OWN configured profiles, never a sibling's or
  the coordinator's — and that grant is not just true in memory, it is
  journaled at the moment the child is enqueued, so an operator auditing
  later sees exactly what a live run was actually given, not what today's
  config now says.

  # NOTE ON WHAT THIS JOURNEY CAN AND CANNOT SEE: tests/acceptance drives a
  # `ctxloom` SUBPROCESS over MCP stdio — it can only observe what crosses the
  # wire or lands on disk, never a captured in-process Go struct. There is no
  # MCP tool reachable from this harness that exposes the roster/ListRuns
  # projection over the wire (roster's real "runner-terminated" MCP endpoint
  # is a spawned child's own local socket — internal/mcp/mcp_runner.go — which
  # this harness's plain `ctxloom mcp` subprocess never becomes). So every
  # assertion below reads the coordinator's own durable journal, runs.jsonl,
  # directly off disk: the SAME data roster is backed by (consumer.go's
  # listRunsSnapshot reads this exact fold), and a genuinely external,
  # disk-durable observable — not a Go struct capture, and not faked.
  #
  # For the same reason, three things a NAIVE version of this journey might
  # have tried to assert are deliberately NOT here: a child's assembled
  # CONTEXT contents (the mock backend never echoes fragments back, and no
  # tool here returns a child's raw transcript), child WORKSPACE/filesystem
  # isolation (the mock engine never touches its WorkDir), and artifact
  # publish/fetch/tamper-refusal (agent_report/agent_fetch_artifact are
  # registered only on a spawned session's OWN per-cell runner socket, never
  # reachable from an external MCP client like this one). All three need a
  # different harness than this one to prove honestly; faking them here would
  # be exactly the overclaim this project has already been caught making once.

  Background:
    Given Alice's coordinator can delegate to two agents, "reviewer" and "fixer", each with its own profile, its own MCP server, and its own permission mode

  # LOCKED — the sharpest claim this journey makes, and the reason it exists.
  # Verified: prodSpawner.childMCPServers (spawner.go) composes a child's MCP
  # set strictly from ITS OWN resolved profiles; runEnqueued.MCPServers
  # (facts.go) journals exactly that set, names only. Break-point verified by
  # unioning all resolved profiles in childMCPServers: the scenario goes RED
  # because "reviewer"'s journaled grant then contains "deploy-tool".
  Scenario: Each child is granted only its own MCP servers, never a sibling's or the coordinator's
    When the agent calls tool "agent_run" with:
      | agent  | reviewer |
      | prompt | go       |
    Then the tool call succeeds
    When the agent calls tool "agent_run" with:
      | agent  | fixer |
      | prompt | go    |
    Then the tool call succeeds
    And "reviewer"'s journaled grant carries its own MCP server and not "fixer"'s
    And "fixer"'s journaled grant carries its own MCP server and not "reviewer"'s

  # LOCKED — the grant is auditable, not implicit: an operator reading the
  # journal sees the ACTUAL resolved permission mode a child ran with, not a
  # guess. The two children here are given genuinely different modes
  # (reviewer: plan; fixer: bypass) precisely so this cannot be satisfied by
  # one hard-coded value showing up twice.
  Scenario: Each child's permission mode is recorded, not just implied
    When the agent calls tool "agent_run" with:
      | agent  | reviewer |
      | prompt | go       |
    Then the tool call succeeds
    When the agent calls tool "agent_run" with:
      | agent  | fixer |
      | prompt | go    |
    Then the tool call succeeds
    And "reviewer"'s journaled grant records its own permission mode
    And "fixer"'s journaled grant records its own permission mode, different from "reviewer"'s

  # LOCKED — DURABILITY. Verified: runEnqueued.Permission/MCPServers are
  # written once, at enqueue (children.go's enqueueRun), the same journaling
  # discipline already applied to the escalation Ladder and for the identical
  # reason: a later config edit must not retroactively rewrite what a LIVE
  # run was actually granted. This is not a tautology about append-only
  # files — the second spawn below proves the edit genuinely took effect
  # (fixer's NEXT run reflects it), while the run already on record does not
  # move.
  Scenario: A child's grant is journaled at enqueue and survives a later config edit unchanged
    When the agent calls tool "agent_run" with:
      | agent  | fixer |
      | prompt | go    |
    Then the tool call succeeds
    And "fixer"'s current journaled grant is remembered as "before the edit"
    When Alice edits "fixer" to use "reviewer"'s profile and permission mode instead
    And the agent calls tool "agent_run" with:
      | agent  | fixer    |
      | prompt | go again |
    Then the tool call succeeds
    And "fixer"'s current journaled grant is remembered as "after the edit"
    And the grant remembered as "before the edit" still shows the original MCP server and permission mode
    And the grant remembered as "after the edit" shows the newly edited MCP server and permission mode

  # LOCKED — names, never secrets, deliberately. Verified: runEnqueued.MCPServers
  # (facts.go) carries mcp.ChatMCPServer NAMES only — never command, args, or
  # env — the same boundary CredHash already draws for the bearer token
  # (identity recorded, never the credential). Both fixture bundles here ship
  # an MCP command carrying a plausible secret-shaped argument specifically so
  # this scenario has something real to prove was never written.
  Scenario: The audit trail carries a server's name, never the command that can carry a secret
    When the agent calls tool "agent_run" with:
      | agent  | reviewer |
      | prompt | go       |
    Then the tool call succeeds
    When the agent calls tool "agent_run" with:
      | agent  | fixer |
      | prompt | go    |
    Then the tool call succeeds
    And the journal carries both children's MCP server names
    But the journal never carries the command or arguments that launch them

  # FAILURE PATH — every scenario above is happy-path (spawn, succeed, read
  # the journal back). agent_stop is one of this flow's own coordinator
  # verbs and had NO acceptance coverage at all before this scenario
  # (completeness_test.go's census was the only place the string appeared).
  # U024-F04 (this batch) fixed a real bug on exactly this shutdown edge: a
  # stop landing on an already-ended run must still cancel any armed
  # relaunch, not just report "already ended" as a no-op — the 2026-07-24
  # incident shape. This is the tip-to-tail proof that the STOP verb
  # behaves like every other verb in this flow: idempotent on a real run
  # (never errors on a second stop), and refused — never silently
  # accepted — on a run id that was never spawned at all.
  Scenario: Stopping a child is idempotent, and stopping a run that never existed is refused
    When the agent calls tool "agent_run" with:
      | agent  | fixer |
      | prompt | go    |
    Then the tool call succeeds
    And "fixer"'s spawned session is remembered
    When the agent calls tool "agent_stop" for "fixer"'s remembered session
    Then the tool call succeeds
    When the agent calls tool "agent_stop" for "fixer"'s remembered session
    Then the tool call succeeds
    And the tool result contains "already ended"
    When the agent calls tool "agent_stop" with:
      | harp | not-a-real-session-at-all |
    Then the tool call fails

  # FAILURE PATH — a message's `kind` is a security boundary, not a label.
  # approval_request is the kind the escalation ladder uses when it relays a
  # child's permission request UP for a decision a human is expected to make;
  # user_injected and exited are the coordinator's own notices. Until this
  # scenario existed, `kind` was whatever the sender said it was: a delegated
  # child could queue an "approval_request" into its coordinator's mailbox and
  # phish a trust decision out of it, complete with a plausible body.
  #
  # WHAT THIS HARNESS CAN AND CANNOT SEE (same limit as the note at the top):
  # it drives the COORDINATOR's own agent_send, never a child's — no tool here
  # reaches a spawned child's runner socket. The refusal is identity-
  # independent by construction (one ingress guard for both sender surfaces),
  # and the child-identity case is pinned in the unit suite
  # (TestServePeerSend_RefusesSpoofedApprovalRequest). What this proves at the
  # real MCP surface is that the vocabulary is CLOSED and that the refusal
  # tells the sender what it may use instead — and, in the same scenario, that
  # it is not a blanket refusal: a documented kind still goes through.
  Scenario: A sender cannot claim a coordinator-reserved message kind
    When the agent calls tool "agent_run" with:
      | agent  | fixer |
      | prompt | go    |
    Then the tool call succeeds
    And "fixer"'s spawned session is remembered
    When the agent sends "fixer"'s remembered session a message of kind "approval_request"
    Then the tool call fails
    And the tool failure message contains "reserved for the coordinator"
    And the tool failure message contains "message | result | error | question"
    When the agent sends "fixer"'s remembered session a message of kind "made_up_kind"
    Then the tool call fails
    And the tool failure message contains "is not a message kind"
    When the agent sends "fixer"'s remembered session a message of kind "result"
    Then the tool call succeeds

  # FAILURE PATH — "nobody decided" is not "the user said no", and the
  # difference is written into the engine's own durable transcript. When a
  # child's permission request reaches the bottom of its escalation ladder
  # with no rung and no human having resolved it (every relay expired), the
  # engine used to be handed a reject_once option — which claude-code-acp
  # reports to the model as {behavior:"deny", message:"User refused permission
  # to run tool"}. ctxloom's own refusal, filed under the operator's name, in
  # a record the operator cannot correct. The engine is now told the request
  # was CANCELLED, which is what actually happened.
  #
  # The observable is the ENGINE's account, not ctxloom's: the mock reports
  # "granted" for an allow option, "denied" for a reject option and
  # "dismissed" for the empty option id that is the ACP cancelled reply. So
  # the scenario reads the verdict the engine itself recorded. BREAK-POINT:
  # revert resolveApproval's decision==nil arm (or the ladder bottom's
  # DECISION_CANCEL) and this reads "denied" — the defect, in the engine's own
  # words.
  #
  # A GENUINE decline is deliberately NOT touched by this: an auto_decline
  # rung, or a parent answering DECISION_DECLINE, still says "refused",
  # because then someone really did refuse. That boundary is pinned in the
  # unit suite (TestEngineHost_ApprovalDeclineCancels,
  # TestApproval_PlanPresetAutoDeclinesFileChange).
  Scenario: An approval nobody answers is reported to the engine as cancelled, never as the user's refusal
    Given Alice's coordinator can also delegate to "auditor", whose escalation ladder relays to a parent that never answers
    When the agent calls tool "agent_run" with:
      | agent     | auditor          |
      | prompt    | PERMISSION check |
      | workspace | none             |
    Then the tool call succeeds
    And "auditor"'s spawned session is remembered
    And "auditor"'s reported turn records the permission verdict "dismissed"

  # LOCKED — capability negotiation, from a standing start of NOTHING. The
  # handshake has carried Hello.capabilities since the contract was written and
  # both ends wrote a hardcoded literal into it that neither end ever read: the
  # "the coordinator MUST NOT send request kinds the agent didn't advertise"
  # rule had no implementation at all, and nothing noticed because there were
  # no senders yet. A runner now advertises what its hosted engine can actually
  # execute, the coordinator captures it per-run, and — the part that makes it
  # observable outside the process — the advertisement is journaled with the
  # attach, so an operator asking "why was my control request refused?" can
  # read what that run claimed rather than what today's config would say.
  #
  # This spawns a REAL child, whose REAL runner subprocess dials home and
  # Hellos; the assertion reads the coordinator's own interactions.jsonl off
  # disk. It also pins the all-or-nothing shape: a PARTIAL control
  # advertisement is what would make the send-side check pass and the request
  # die at the far end, which is the experience the check exists to replace.
  Scenario: A child runner's advertised capabilities are captured and journaled
    When the agent calls tool "agent_run" with:
      | agent     | fixer |
      | prompt    | go    |
      | workspace | none  |
    Then the tool call succeeds
    And the coordinator's audit trail records the child runner's advertised capabilities
