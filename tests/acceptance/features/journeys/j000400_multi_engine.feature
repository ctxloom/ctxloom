@doc
Feature: One shared profile, reaching three engines in their own native format

  A team does not standardize on one assistant. Carol's team writes one shared
  profile — a fragment, a command, an MCP server, a hook — once. Alice's teammates
  use claude-code, codex, and kiro, and every one of them needs that
  same profile to reach their own engine, in whatever native shape that engine
  actually reads. ctxloom's job is to be the one place the team's standard is
  authored, and to speak every engine's own dialect on the way out — nobody
  forks the profile per engine, and nobody hand-translates a fragment into
  different config formats.

  The three engines do NOT converge on one shape, and this is not incidental —
  it is the whole point of proving it here rather than asserting it in prose.
  Verified straight from each engine's own surfaces.go (internal/{claude,codex,
  kiro}/surfaces.go):

    | engine      | context lands in                                          | MCP lands in                              | hooks land in                | commands land in                   |
    |-------------|-------------------------------------------------------------|----------------------------------------------|-------------------------------|--------------------------------------|
    | claude-code | CLAUDE.md (managed markers)                                | .mcp.json                                   | .claude/settings.json          | .claude/commands/                    |
    | codex       | AGENTS.md (managed markers, native) + a hook-read cache file | NO native file — folded into config.toml   | $CODEX_HOME/config.toml [hooks] — per-session only | $CODEX_HOME/prompts/ (global) — per-session only |
    | kiro        | .kiro/steering/ctxloom-context.md                          | .kiro/settings/mcp.json                     | .kiro/agents/<name>.json        | .kiro/skills/<name>/SKILL.md         |

  codex's rows name $CODEX_HOME rather than a project-root .codex, and that is
  the second divergence worth stating: codex is the ONE engine with no
  cwd-keyed place for its hooks, MCP servers and prompts at all. They live in
  $CODEX_HOME/config.toml and $CODEX_HOME/prompts, and the only $CODEX_HOME
  ctxloom may write is a PER-SESSION one it creates when an agent launches.
  Alice's real ~/.codex is hers; ctxloom does not write it.

  So `profile materialize --backend codex` is NARROWED rather than gutted, and
  the third scenario below is where that is proven: codex's cwd-keyed surface,
  AGENTS.md, is materialized exactly like everyone else's context, while the
  three home-keyed surfaces are DECLARED as delivered per-session at launch and
  land in the materialized tree nowhere at all. An absence a report states is a
  fact a team can plan around; an absence nothing mentions is the same tree with
  a lie on top of it.

  Proving Alice's bytes were WRITTEN in each engine's own shape is NOT the same
  claim as proving any engine READ them — that is what the second, much smaller
  table below exists to prove, for the engines where it can honestly be proven
  today.

  # THE UNIQUE CLAIM HERE IS THE FAN-OUT, and it is the reason this outline
  # survives alongside four specs that each look stronger than it in isolation.
  # Every individual surface is asserted comprehensively by the noun that owns
  # it — context in cli/fragment.feature, MCP in cli/mcp.feature, hooks in
  # cli/manage.feature, commands in cli/command.feature. What ONE materialize
  # delivering ALL FOUR TOGETHER proves, no per-surface spec can see: a
  # regression that writes three surfaces and silently drops the fourth passes
  # every one of those files and fails only here. Do not fold this into them.
  #
  # LOCKED — materialization only (hermetic, no engine binary required). Every
  # row PARSES the generated file in its own format (JSON, TOML, or plain
  # markdown) and asserts the actual field, never a bare file-exists and never
  # a substring of a key name: a key name is satisfied by the file merely
  # mentioning it, which is true whether or not any content landed.
  #
  # codex's context row is the one to read carefully. Its surface writes
  # AGENTS.md from agent.SurfaceInputs.Context — the assembled string, which
  # materialize populates. It must never be keyed on SurfaceInputs.Fragments:
  # materialize never fills that field, so a fragments-keyed context surface is
  # a silent no-op on this path, exit 0 with nothing written. Adding an engine
  # here is adding a ROW, not new Go.
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

  # codex is the THIRD engine of the fan-out and it gets its own scenario, not
  # a row, because its answer is genuinely different: one surface materializes
  # and three are DECLARED as launch-only. Keeping it as a row would have meant
  # either asserting files the product deliberately does not write, or quietly
  # dropping codex from the journey — and dropping it is how a narrowing
  # becomes a regression nobody notices.
  #
  # ABSENCE OVER THE WHOLE TREE, never "the file I guessed is missing": the
  # interesting failure is a fallback landing somewhere nobody thought to look,
  # which a single-path check cannot see. And the REPORT is asserted alongside,
  # because a narrowing a user is not told about is indistinguishable from a
  # loss.
  Scenario: codex materializes its native context, and declares the three surfaces it delivers at launch instead
    Given Carol's team profile carries a shared fragment, command, MCP server, and hook
    When Alice materializes the team profile for codex
    Then the materialized codex context carries the shared fragment's marker, in its own native shape
    And no codex surface anywhere in the materialized tree carries the shared hook's command
    And no codex surface anywhere in the materialized tree carries the shared MCP server's command
    And no codex surface anywhere in the materialized tree carries the shared command's body
    And the materialize report says codex delivers those surfaces per-session at launch

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

  # LOCKED — @live: claude, kiro, and codex have a working live
  # path today (kiro confirmed live: a logged-in kiro-cli genuinely reads the
  # materialized steering context and echoes the sentinel back — the
  # not-yet-authenticated gap in internal/kiro/chat.go's comment is closed).
  # codex joined this table once 7beee9a routed AGENTS.md through SurfaceFor
  # on the real materialize/run path (not just the dead Deliveries() path) —
  # a logged-in codex CLI genuinely reads the materialized AGENTS.md and
  # echoes the sentinel back. Each present row self-skips without
  # credentials, exactly like J000200's own @live scenario. Adding an engine here
  # is adding a ROW, no new Go and no new steps — proven true a third time by
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
      | Kiro        |
      | Codex       |
