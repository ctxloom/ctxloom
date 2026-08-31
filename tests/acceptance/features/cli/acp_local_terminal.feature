@doc
Feature: acp_local_terminal — ctxloom's ACP client role serving terminal/* itself, via tmux

  internal/acp's client role has always DECLINED an engine's terminal/*
  call when no upstream editor advertised the capability to forward it to
  — "ctxloom brokers terminal/* to editors, it never implements one of its
  own." acp_local_terminal (HOME config only — see the Rule below) turns
  on a second path: ctxloom answers terminal/* itself, backed by tmux,
  instead of declining. Off (the default) changes nothing.

  # HOW THIS SCENARIO REACHES A REAL terminal/* CALL. The engine on the
  # OTHER end of ctxloom's client role has to actually issue terminal/*
  # for there to be anything to answer — no test harness in this suite
  # speaks raw ACP JSON-RPC (see acp.feature's own note on why `acp
  # serve`/`acp run` are unreachable directly). The `mock` backend's
  # "TERMINAL" turn (internal/lm/backends/mock_chat.go) exists for exactly
  # this: it raises a scripted create/wait/output/create/kill/release
  # sequence over the SAME forwarding carrier a real engine's terminal/*
  # rides, and reports what it actually observed.
  #
  # The wiring is a LOOPBACK, same shape j002100_delegation.feature already
  # uses for its own ACP-forwarding scenario: an `llm:` label of
  # `type: acp, command: "<this ctxloom binary> acp serve"` makes
  # `ctxloom run --llm <that label>` spawn ctxloom itself as the ACP AGENT
  # (internal/acpagent), which in turn opens ITS OWN engine session using
  # the project's default `mock` backend — a REAL subprocess, a REAL ACP
  # wire in both directions, no test-only backend anywhere in the path
  # that answers terminal/*.
  Background:
    Given an initialized ctxloom project
    And a project wired to drive an engine's terminal/* calls through a scripted mock turn

  Rule: acp_local_terminal is a machine fact, so only HOME config may set it

    Whether THIS box has tmux to serve terminal/* locally is not a fact a
    committed project file can state for every clone — layerscope scopes
    the key ScopeMachine, same as runtime and the isolation_* keys. A
    project file that sets it anyway is dropped with a warning rather than
    silently applied.
    Scenario: A project file cannot turn acp_local_terminal on
      When Alice sets acp_local_terminal in the PROJECT config instead of home
      And I run "ctxloom run --llm forwarding --one-shot TERMINAL"
      Then the command fails
      And the output contains "acp_local_terminal"
      And the output contains "your home config"

  Rule: Off (the default), a probing engine is still declined — nothing about acp_local_terminal changes that

    Scenario: With acp_local_terminal off, terminal/* is declined exactly as before
      When I run "ctxloom run --llm forwarding --one-shot TERMINAL"
      Then the command succeeds
      And the output contains "acp_local_terminal is off"

  Rule: On, an engine's terminal/* calls are answered by a real tmux terminal end to end

    # THE PAYLOAD, NOT THE EXIT CODE. "output=..." is the exact bytes a REAL
    # tmux window printed, read back through terminal/output; "killed=true"
    # is KillTerminal's own outcome, not an assumption that the turn merely
    # completed. A stub that never touched tmux at all could still exit 0 —
    # it could not produce this marker.
    Scenario: A terminal command's real output round-trips, and killing a second terminal actually works
      Given Alice turns acp_local_terminal on in her home config
      When I run "ctxloom run --llm forwarding --one-shot TERMINAL"
      Then the command succeeds
      And the output contains "mock-tmux-marker-7f3a"
      And the output contains "killed=true"

  # acp_local_terminal ON but tmux UNREACHABLE ("fail loud, never silently
  # decline") is NOT exercised here: reliably making tmux unreachable to a
  # subprocess several levels down this suite's real fork/exec chain (the
  # spawned engine, in turn spawning `ctxloom acp serve`, in turn spawning
  # tmux) needs a PATH restrictive enough to hide tmux but permissive enough
  # to keep every OTHER binary that chain needs (sh, git, ctxloom itself)
  # reachable — on this box /bin is a merged-usr symlink to /usr/bin, so
  # there is no directory that has one without the other. That path is
  # covered deterministically instead, with an injected tmuxRunner error, by
  # TestLocalTerminals_Create_TmuxMissing_FailsLoud (internal/acp) and
  # TestChat_LocalTerminal_TmuxMissing_FailsLoud (internal/acp), which pin
  # the exact remedy text this scenario would otherwise have grepped for.
