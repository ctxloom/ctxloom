@doc
Feature: Dana in her editor

  Dana lives in Zed and is not going to adopt a terminal workflow because a
  context tool would prefer it. This is not stubbornness; it is most of the
  market. If ctxloom only works for people who run their assistant in a
  terminal, then "one signed context tree driving all of them" quietly means
  "all of the ones I drive the way I like".

  The editor door is `ctxloom acp serve`: the editor spawns it over stdio and
  talks the Agent Client Protocol, and the promise is that it is the SAME door —
  same binding, same trust gates, same capture — as a terminal run. `acp list`
  prints the blocks she pastes into her editor's config to make that happen,
  and `acp client` drives one headless turn outward at any ACP-speaking agent.

  # NOTE ON WHAT THIS FEATURE CAN AND CANNOT SEE — the honest limit, stated
  # first, because it governs what is here and what deliberately is not.
  #
  # Most of Dana's journey needs a LIVE EDITOR speaking ACP over stdio to a
  # long-running serve process. That is not hermetically assertable in this
  # harness, and writing scenarios that pass without it would prove nothing
  # about whether Zed can actually drive ctxloom. Those rows are recorded as
  # unverifiable here and left to a manual verify, rather than faked into
  # green. The whole surface is EXPERIMENTAL and the live-editor verify remains
  # open.
  #
  # What IS assertable is the CONFIGURATION EDGE — the part Dana touches before
  # any editor is involved, and, as it turns out, the part where the surface's
  # real problems live: whether the block she pastes is pasteable, whether a
  # flag she passes is honoured or silently dropped, and whether she is told
  # what this door does not carry.
  #
  # NOT WRITTEN HERE, and why:
  #   - door equivalence under a real editor (same context, trust gates and tee
  #     as a terminal run) — needs a live ACP client; a Go-level test of the
  #     same name exists, and this journey does not restate it;
  #   - `acp client` driving a real outbound turn — needs a live ACP agent;
  #   - the antigravity row, which is prose-degraded by construction
  #     (STRUCTURAL loss of tool events and permission forwarding), so there is
  #     nothing to assert but the loss itself.
  #
  # NOTE ON TAGS. Each scenario carries its own note: an untagged one records
  # the mutation that proves it can still fail, a @wip one the product surface
  # that does not exist yet.

  Background:
    Given Dana lives in her editor and will not adopt a terminal workflow

  # The first thing she ever does, and the first thing that can fail. "Prints
  # agent blocks to paste into Zed's config" is a payload claim, not a
  # formatting preference: the block has to name the command the editor will
  # execute AND parse as the JSON an editor config is written in. A prose
  # listing that describes the agent contains the same words and is a different
  # artifact — and the difference only shows up when Dana pastes it and her
  # editor's config file stops loading.
  #
  # UNTAGGED: it passes as written, and the parseability half is what earns it.
  # Changing zedAgentServersBlock's `"%s": %s` to `"%s" -> %s` — a block that
  # still contains every substring a reader would grep for, still names the
  # command, and is no longer JSON — turns this red on the Unmarshal, which is
  # exactly the artifact Dana's editor would reject.
  Scenario: The block she is told to paste is one she can actually paste
    When I run "ctxloom acp list"
    Then the listing is something she can actually paste into her editor's config
    And the listing names the agent she would bind to

  # A FILED DEFECT (task broken-sage) at the surface where it does the most
  # damage. The bare `ctxloom acp` parent command registers --agent, --llm,
  # --profile and --workspace, and then discards them.
  #
  # A flag that is REJECTED teaches the user their command was wrong. A flag
  # that is ACCEPTED and ignored teaches them it was right. Dana binds an
  # agent, sees exit 0, and gets a session carrying none of that agent's
  # context — no error, no warning, nothing to search for. She will conclude
  # ctxloom does not deliver context to editors, and she will be reasoning
  # correctly from everything she can observe.
  #
  # UNTAG WHEN: bare `ctxloom acp` either honours the flags it registers or
  # refuses the invocation. Expected RED.
  @wip
  Scenario: A flag she passes is honoured or refused, never quietly dropped
    When I run "ctxloom acp --agent dev"
    Then ctxloom refuses rather than accepting flags it will discard

  # THE DIFFERENTIATOR'S HOLE, and the most important scenario in this file.
  # A generic ACP agent onboarded as configuration inherits no materialized
  # native surface (P1 is ABSENT BY DESIGN for generic acp), no hooks, and no
  # history — which is to say none of the three things that distinguish ctxloom
  # from pointing the editor straight at the vendor.
  #
  # That may well be the correct engineering outcome; the protocol carries what
  # it carries. What is not correct is that nobody is told. Dana configures an
  # ACP agent, everything reports success, and she gets a door that looks
  # equivalent to the terminal one and is not. The same silence appears in U3,
  # where a new hire's opencode-using deskmate silently receives the team
  # bundle without the team's guardrails, and it is the same missing
  # capability: a per-engine report of what this binding cannot carry.
  #
  # UNTAG WHEN: onboarding or listing a generic ACP agent reports what that
  # binding does not inherit. Expected RED.
  @wip
  Scenario: She is told what the editor door does not carry
    When I run "ctxloom acp list"
    Then ctxloom says what a bare ACP agent will not inherit
