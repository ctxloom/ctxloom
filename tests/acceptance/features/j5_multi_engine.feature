@doc
Feature: One shared profile, reaching four engines in their own native format

  A team does not standardize on one assistant. Carol's team writes one shared
  profile — a fragment, a skill, an MCP server, a hook — once. Alice's teammates
  use claude-code, codex, kiro, and antigravity, and every one of them needs that
  same profile to reach their own engine, in whatever native shape that engine
  actually reads. ctxloom's job is to be the one place the team's standard is
  authored, and to speak every engine's own dialect on the way out — nobody
  forks the profile per engine, and nobody hand-translates a fragment into four
  different config formats.

  The four engines do NOT converge on one shape, and this is not incidental —
  it is the whole point of proving it here rather than asserting it in prose.
  Verified straight from each engine's own surfaces.go (internal/{claude,codex,
  kiro,antigravity}/surfaces.go):

    | engine      | context lands in                          | MCP lands in                              | hooks land in                | skills land in                     |
    |-------------|--------------------------------------------|--------------------------------------------|-------------------------------|--------------------------------------|
    | claude-code | CLAUDE.md                                  | .mcp.json                                   | .claude/settings.json          | .claude/commands/                    |
    | codex       | NO native file — a hook reads the cache file | NO native file — folded into config.toml   | .codex/config.toml [hooks]      | $CODEX_HOME/prompts/ (global)         |
    | kiro        | .kiro/steering/ctxloom-context.md          | .kiro/settings/mcp.json                     | .kiro/agents/<name>.json        | .kiro/skills/<name>/SKILL.md         |
    | antigravity | .agents/AGENTS.md                          | .agents/mcp_config.json                     | .agents/hooks.json (separate)   | .agents/skills/<name>.md             |

  codex is the honest gap this feature exists to show, not to hide: it has NO
  native context file and NO native MCP file at all — both fold into
  .codex/config.toml, the same file that carries its hooks. Proving Alice's
  bytes were WRITTEN in each engine's own shape is NOT the same claim as proving
  any engine READ them — that is what the second, much smaller table below
  exists to prove, for the two engines where it can honestly be proven today.

  # LOCKED — materialization only (hermetic, no engine binary required). Every
  # row PARSES the generated file in its own format (JSON, TOML, or plain
  # markdown) and asserts the actual field — never a bare file-exists or a
  # substring-of-a-key-name (the exact vacuousness `manage.feature`'s ".mcp.json"
  # assertion still carries, which this outline does not repeat). codex is
  # deliberately NOT a row here — see the @wip scenario below for why.
  Scenario Outline: The same profile materializes into each engine's own native surfaces
    Given Carol's team profile carries a shared fragment, skill, MCP server, and hook
    When Alice materializes the team profile for <engine>
    Then the materialized <engine> context carries the shared fragment's marker, in its own native shape
    And the materialized <engine> MCP configuration carries the shared server's command, in its own native shape
    And the materialized <engine> hook configuration carries the shared hook's command, in its own native shape
    And the materialized <engine> skill file carries the shared skill's body, in its own native shape

    Examples:
      | engine      |
      | claude-code |
      | kiro        |
      | antigravity |

  # @wip — a PRODUCT GAP this journey found, not a harness limitation (the same
  # kind of finding J1-J4 already surfaced, per the suite's own convention: see
  # `manage apply` vs `materialize` in internal/operations/hooks.go regenerateContext
  # vs internal/operations/profile_materialize.go's BuildSurfaces call). codex's
  # context reaches an engine ONLY through a hook that reads a cache file
  # (agent.WriteContextFile, keyed off agent.SurfaceInputs.Fragments) — never a
  # native per-project file. `ctxloom manage hooks install` populates Fragments
  # (via its own separate regenerateContext call) and the cache file lands
  # correctly. `profile materialize` never populates Fragments at all, so
  # codex's context surface is unconditionally a no-op there — the ONLY one of
  # the four engines whose materialized target gets NO context, silently. Its
  # MCP, hook, and skill surfaces are unaffected (they don't depend on
  # Fragments) and materialize correctly, proven below. Excluded from the
  # default green run until fixed. NOT the same claim as "codex has no native
  # context format" (true and fine, see the table above) — this is "codex's
  # only delivery mechanism silently does not fire from this CLI path."
  @wip
  Scenario: codex materializes its MCP, hook, and skill surfaces, but not its context — a confirmed product gap
    Given Carol's team profile carries a shared fragment, skill, MCP server, and hook
    When Alice materializes the team profile for codex
    Then the materialized codex MCP configuration carries the shared server's command, in its own native shape
    And the materialized codex hook configuration carries the shared hook's command, in its own native shape
    And the materialized codex skill file carries the shared skill's body, in its own native shape
    But the materialized codex context is not delivered at all, a known product gap

  # LOCKED — @live: claude, antigravity, and kiro have a working live path
  # today (kiro confirmed live: a logged-in kiro-cli genuinely reads the
  # materialized steering context and echoes the sentinel back — the
  # not-yet-authenticated gap in internal/kiro/chat.go's comment is closed).
  # codex has no binary on any dev host and is implemented against docs, never
  # run (internal/codex/settings.go:7-29) — absent from this table on purpose,
  # not skipped, never written. Each present row self-skips without
  # credentials, exactly like J1's own @live scenario. Adding an engine here is
  # adding a ROW, no new Go and no new steps — proven true a third time by
  # kiro's row below.
  @live
  Scenario Outline: A real engine actually receives the shared context and can use it
    Given a real <engine> agent is available
    And Carol's team profile carries a fragment with a sentinel marker
    When Alice asks her <engine> assistant to repeat the sentinel it can see
    Then its reply contains the sentinel marker

    Examples:
      | engine      |
      | Claude      |
      | Antigravity |
      | Kiro        |
