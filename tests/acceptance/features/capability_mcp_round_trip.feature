@live
Feature: Capability probe P2 — an arbitrary MCP server's tool, actually called

  ctxloom tells every engine it drives about the MCP servers a project declares.
  All four engines claim to accept them, and each has its own native place to put
  them: claude gets a --mcp-config file, codex a [mcp_servers] table in its
  config.toml, kiro .kiro/settings/mcp.json, opencode an `mcp` block inside
  opencode.json. Until this probe, none of that was ever demonstrated end to end.
  What HAD been demonstrated is narrower than it looks: j002300's delegated
  children each called mcp__ctxloom__agent_send, which is ctxloom's OWN
  auto-registered forwarder, over a socket the coordinator stood up, on host/none
  alone. A server a USER declares — the thing every `mcp.servers:` block in every
  config.yaml out there is — had never been shown to reach an engine at all, let
  alone to be called.

  This feature is that missing proof, one cell per engine.

  THE NONCE IS THE ROUND TRIP. Each cell mints a fresh harp and writes it into
  exactly one place: a small stdio MCP server the fixture generates, which serves
  a single tool, get_nonce, returning that harp. The harp is NOT in the agent's
  composed context, NOT in the prompt and NOT in the environment — the cell scans
  the surfaces it authored before spending a turn and refuses to run if the value
  is in one. The fixture server lives INSIDE the workspace, so an engine that
  goes looking CAN read the nonce file; that is deliberate, and it is safe for
  the reason the next paragraph gives.

  AND ECHOING IT BACK IS NOT ENOUGH — WHICH IS THE WHOLE DEFENCE. A nonce probe
  that only matched strings would be satisfied by an engine that found the value
  some other way, and this one is deliberately not: the fixture server records
  every tools/call it serves, and the verdict REQUIRES one. Reading the nonce
  file cannot forge that record. This is measured rather than argued — P3's codex
  cell of 2026-08-13 grepped its harp out of the fixture's own script, answered
  correctly, and red anyway — and it is why the fixture could be moved into the
  workspace to reach the container axis at all.

  A run whose output is perfect but whose tool was never called goes red, on the
  tool path, with its output printed beside the finding. The two halves are never
  allowed to substitute for each other — that
  is what stops this probe from being a tautology, and it is checked by a
  hermetic test that plants the harp in composed context and demands a red.

  THE FAILURE SHAPES ARE THE FINDING, as everywhere on this ladder. A red names
  which of these it found: the run failed outright; exit 0 with empty stdout (the
  silent no-op); an OUTPUT-FORMAT failure (fences, preamble); an MCP-DELIVERY
  failure — itself split between "the fixture server never started", which blames
  ctxloom's registration or the engine's launcher, and "the server started and
  get_nonce was never called", which blames neither and describes the model; or a
  SHAPE/VALUE failure once the round trip demonstrably happened. Do not loosen any
  of them. An engine that must be tolerated gets its own tagged Examples block
  with its own measured evidence, where a reader can see it.

  REGISTRATION GOES THROUGH CONFIG, NOT THROUGH AN ENGINE FILE. The fixture
  writes `mcp.servers` into the project's config.yaml and stops there; ctxloom's
  own delivery carries it the rest of the way to each engine's native surface. A
  cell that wrote the engine's file directly would prove an engine can read a
  file we wrote — which nobody doubted — instead of proving ctxloom delivers.
  Config rather than a bundle for a second reason: a bundle's MCP servers pass
  through the executable trust gate, and a withheld server would red as an
  MCP-delivery failure that is really a trust decision.

  THE CONTAINER AXIS, AND THE TWO WRONG REASONS IT WAITED BEHIND. claude-code
  now runs container-rootless on both workspace settings. Getting here needed no
  production change at all — only the fixture moving INTO the workspace and its
  registration naming a WORKSPACE-RELATIVE path, which resolves identically
  whether the cell runs the project directory (bind-mounted at the same absolute
  path) or a per-agent checkout. Both earlier blockers were recorded here
  authoritatively and both were wrong: until 2026-08-25 this said MCP reach-back
  was undesigned, which misattributed a COORDINATOR-bus finding to a fixture that
  never used the bus; it was then replaced by a call for a probe-only bind-mount
  seam, which mistook defence in depth for the mechanism. codex, kiro and
  opencode stay host-only by SCOPE — 0.7.0 propagates claude onto this axis.
  Every cell here self-skips LOUDLY, naming the engine and the reason: an absent
  or unauthenticated engine. There is no interpreter to miss any more — the
  fixture server is a Go binary the fixture builds (cmd/probe-mcp-server), so a
  build failure errors rather than skipping.

  Scenario Outline: A <engine> run obtains the nonce only by calling the fixture MCP server's tool
    Given the MCP round-trip probe targets "<engine>" under runtime "<runtime>" and workspace "<workspace>"
    When it asks the engine to call the fixture MCP tool in one turn
    Then the engine's output is exactly the nonce the MCP tool returned

    @probe-p2-mcp-round-trip @claude-code @host @ws-none
    Examples:
      | engine      | runtime | workspace |
      | claude-code | host    | none      |

    # @wip — RED, measured TWICE on 2026-08-26, and the finding is narrow: the
    # server STARTS IN THE CONTAINER and is never called.
    #
    #   start request(initialize) request(notifications/initialized)
    #   request(tools/list)
    #
    # So ctxloom's delivery is PROVEN on this axis — the bundle's mcp: block
    # reached a containerized claude, which spawned the server in-container,
    # completed the handshake and enumerated the tool. Then no tools/call. The
    # identical fixture on host/none calls it reliably the same day, so this is
    # neither the fixture nor the prompt. It is the same shape as kiro's host
    # row: registration and discovery work, invocation does not.
    #
    # These two rows must be read as a PAIR: P6's host/worktree cell failed
    # where both-off and both-on passed, because the two axes resolve a
    # credential by different mechanisms.
    #
    # Untag when a containerized claude calls a registered MCP tool it listed.
    @probe-p2-mcp-round-trip @claude-code @container-rootless @ws-none @wip
    Examples:
      | engine      | runtime            | workspace |
      | claude-code | container-rootless | none      |

    # @wip — RED identically to the row above (measured 2026-08-26), so the
    # mixed corner adds no separate defect. It DID prove the evidence path: the
    # fixture reaches the per-agent checkout because it is COMMITTED, and
    # probeCellRunDir resolved that checkout and read the call log out of it —
    # which works here because the server's writes leave the tree dirty and the
    # WIP-safe teardown spares it. P3's worktree row shows the other side.
    @probe-p2-mcp-round-trip @claude-code @container-rootless @ws-worktree @wip
    Examples:
      | engine      | runtime            | workspace |
      | claude-code | container-rootless | worktree  |

    @probe-p2-mcp-round-trip @codex @host @ws-none
    Examples:
      | engine | runtime | workspace |
      | codex  | host    | none      |

    # @wip — RED, measured twice on 2026-08-13, and the finding is narrow and
    # real: kiro REGISTERS and DISCOVERS the server, and never calls it.
    #
    # The fixture server's own call log is what makes that sayable. It read:
    #
    #   start request(initialize) request(notifications/initialized)
    #   request(tools/list) eof
    #
    # so ctxloom wrote .kiro/settings/mcp.json, kiro spawned the server, finished
    # the handshake, and asked for the tool list — every step up to invocation.
    # Then nothing. kiro's stderr looped for six minutes on
    #
    #   Tool validation failed: No tool with "dummy" is found
    #
    # and the model's eventual answer was the literal string
    # {"nonce":"non-verbatim placeholder - tool not found"} — kiro reporting that
    # no such tool existed, moments after enumerating it.
    #
    # This is NOT the kiro finding P0 already carries. That one is ctxloom's
    # interactive `> ` prompt echo leaking ANSI decoration into a non-interactive
    # capture, and it reds the output-format check. It masked this finding on the
    # first attempt, which is why mcpProbeAssert now asks "was the tool called"
    # BEFORE "is the output well formed": the tool-call fact is a property of the
    # server's records, not of the stdout's shape, and it is what this probe is
    # about. Fixing the decoration defect will not turn this cell green.
    #
    # Untag when a kiro run calls a registered MCP tool it has already listed.
    @probe-p2-mcp-round-trip @kiro @host @ws-none @wip
    Examples:
      | engine | runtime | workspace |
      | kiro   | host    | none      |

    @probe-p2-mcp-round-trip @opencode @host @ws-none
    Examples:
      | engine   | runtime | workspace |
      | opencode | host    | none      |
