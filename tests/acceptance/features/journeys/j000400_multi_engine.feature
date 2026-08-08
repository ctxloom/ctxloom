@doc
Feature: One shared profile, reaching four engines in their own native format

  A team does not standardize on one assistant. Carol's team writes one shared
  profile — a fragment, a command, an MCP server, a hook — once. Alice's teammates
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

    | engine      | context lands in                                          | MCP lands in                              | hooks land in                | commands land in                   |
    |-------------|-------------------------------------------------------------|----------------------------------------------|-------------------------------|--------------------------------------|
    | claude-code | CLAUDE.md (managed markers)                                | .mcp.json                                   | .claude/settings.json          | .claude/commands/                    |
    | codex       | AGENTS.md (managed markers, native) + a hook-read cache file | NO native file — folded into config.toml   | .codex/config.toml [hooks]      | $CODEX_HOME/prompts/ (global)         |
    | kiro        | .kiro/steering/ctxloom-context.md                          | .kiro/settings/mcp.json                     | .kiro/agents/<name>.json        | .kiro/skills/<name>/SKILL.md         |
    | antigravity | .agents/AGENTS.md (managed markers)                        | .agents/mcp_config.json                     | .agents/hooks.json (separate)   | .agents/skills/<name>/SKILL.md       |

  codex still has NO native MCP file of its own — MCP folds into
  .codex/config.toml, the same file that carries its hooks. Its context surface
  is the one that used to be the honest gap this feature existed to show: codex
  now writes AGENTS.md natively too (managed-section markers, taskloom
  lanky-plop/tiny-ooze), alongside the SessionStart-hook cache file the live
  run/launch path still needs for its per-invocation content hash. Proving
  Alice's bytes were WRITTEN in each engine's own shape is NOT the same claim as
  proving any engine READ them — that is what the second, much smaller table
  below exists to prove, for the engines where it can honestly be proven today.

  # LOCKED — materialization only (hermetic, no engine binary required). Every
  # row PARSES the generated file in its own format (JSON, TOML, or plain
  # markdown) and asserts the actual field — never a bare file-exists or a
  # substring-of-a-key-name (the exact vacuousness `manage.feature`'s ".mcp.json"
  # assertion still carries, which this outline does not repeat). codex is now a
  # FOURTH ROW: `profile materialize` used to leave codex's context surface a
  # silent no-op (it was keyed on agent.SurfaceInputs.Fragments, which
  # materialize never populates); codex's context surface now ALSO writes
  # AGENTS.md from agent.SurfaceInputs.Context (the assembled string materialize
  # DOES populate), so this row proves the gap the table used to document is
  # closed — the Outline's own design (add an engine, add a row) proves it a
  # fourth time.
  Scenario Outline: The same profile materializes into each engine's own native surfaces
    Given Carol's team profile carries a shared fragment, command, MCP server, and hook
    When Alice materializes the team profile for <engine>
    Then the materialized <engine> context carries the shared fragment's marker, in its own native shape
    And the materialized <engine> MCP configuration carries the shared server's command, in its own native shape
    And the materialized <engine> hook configuration carries the shared hook's command, in its own native shape
    And the materialized <engine> command file carries the shared command's body, in its own native shape

    Examples:
      | engine      |
      | claude-code |
      | kiro        |
      | antigravity |
      | codex       |

  # Regression coverage for taskloom lanky-plop (P0 data loss): materializing a
  # profile for claude-code/codex must never destroy a team's hand-authored
  # CLAUDE.md / AGENTS.md — content outside ctxloom's managed markers must
  # survive byte-for-byte, and ctxloom's own content must still land alongside
  # it. BREAK-POINT VERIFIED: reverting the marker-merge core
  # (agent.WriteManagedContext, internal/shared/agent/managedcontext.go) back
  # to a bare whole-file write makes this scenario fail for exactly that
  # reason — the hand-authored line is gone, not merely unasserted.
  Scenario Outline: A hand-authored context file survives materialization byte-for-byte
    Given Carol's team profile carries a shared fragment, command, MCP server, and hook
    And Alice's team already hand-authored <file> for <engine> with their own conventions
    When Alice materializes the team profile for <engine>
    Then <file> still carries Alice's hand-authored conventions, byte-for-byte
    And the materialized <engine> context carries the shared fragment's marker, in its own native shape

    Examples:
      | engine      | file      |
      | claude-code | CLAUDE.md |
      | codex       | AGENTS.md |

  # LOCKED — @live: claude, antigravity, kiro, and codex have a working live
  # path today (kiro confirmed live: a logged-in kiro-cli genuinely reads the
  # materialized steering context and echoes the sentinel back — the
  # not-yet-authenticated gap in internal/kiro/chat.go's comment is closed).
  # codex joined this table once 7beee9a routed AGENTS.md through SurfaceFor
  # on the real materialize/run path (not just the dead Deliveries() path) —
  # a logged-in codex CLI genuinely reads the materialized AGENTS.md and
  # echoes the sentinel back. Each present row self-skips without
  # credentials, exactly like J000200's own @live scenario. Adding an engine here
  # is adding a ROW, no new Go and no new steps — proven true a fourth time by
  # codex's row below.
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
      | Codex       |
