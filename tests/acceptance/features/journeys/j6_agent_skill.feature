@doc
Feature: The ctxloom-doctor Agent Skill reaches every engine's native skill surface

  A "command" is a user-invoked slash template; a skill.feature already
  proves that surface end-to-end for two engines. An "Agent Skill" is a
  different thing an engine loads on its own by progressive disclosure — and
  every one of ctxloom's five engines materializes it the SAME shape,
  directory-form (<name>/SKILL.md + siblings), just at a different path.
  This journey proves that across all five, using a concrete, realistic
  fixture: a "ctxloom-doctor" skill that guides validating a ctxloom setup.

  # PATH CORRECTION vs the command surface (j4_multi_engine.feature's own
  # table): a skill is NOT the same directory a flat command file lands in.
  # Verified against each engine's own skillfiles.go, and — for codex —
  # against a REAL `profile materialize --backend codex` run, not just its
  # source (see steps_j6_doctor.go's engineSkillMDPath doc for the full
  # story):
  #
  #   | engine      | skill surface (via `profile materialize`) | scope       |
  #   |-------------|---------------------------------------------|-------------|
  #   | claude-code | .claude/skills/<name>/SKILL.md              | project     |
  #   | kiro        | .kiro/skills/<name>/SKILL.md                | project     |
  #   | antigravity | .agents/skills/<name>/SKILL.md              | project     |
  #   | opencode    | .opencode/skill/<name>/SKILL.md             | project     |
  #   | codex       | .codex/skills/<name>/SKILL.md               | cell-scoped |
  #
  # codex's OWN skills directory is documented GLOBAL ($CODEX_HOME/skills —
  # internal/codex/skillfiles.go), but that path only fires on the LIVE
  # run/launch path. `profile materialize` binds codex's Skills surface
  # through NewSurfaces' inline closure with no homeOverride, which — just
  # like its Commands surface (j4_multi_engine.feature's own codex row) —
  # cell-scopes under --target instead. A naive Outline redirecting
  # $CODEX_HOME here would assert a file that never gets written.

  Background:
    Given Alice's project has a directory-form bundle "ops"
    And Alice starts a ctxloom-doctor skill in "ops" described as "DOCTOR-SKILL-MARKER-7d4e21"
    And Alice authors the ctxloom-doctor skill's full body in "ops#skills/ctxloom-doctor"
    And Alice adds an executable scripts/run.sh carrying the marker "DOCTOR-SCRIPT-MARKER-9c2f" to the skill "ops#skills/ctxloom-doctor"
    And Alice records the skill's file manifest so tampering would be caught
    And a profile "clinic" with bundle "ops"
    And profile "clinic" curates skill "ops#skills/ctxloom-doctor"

  # LOCKED — the core claim: every engine gets the doctor skill's real body,
  # not just a file with the right name. Each of the four DOCTOR-CHECK-*
  # section markers is asserted independently, so a materializer that
  # truncates or corrupts the body cannot pass by carrying only the
  # frontmatter description.
  Scenario Outline: The ctxloom-doctor skill's full body reaches <engine>'s native skill surface
    When Carol materializes the "clinic" profile for <engine>
    Then the <engine> skill surface carries the doctor skill's marker "DOCTOR-SKILL-MARKER-7d4e21"
    And the <engine> skill surface carries the doctor skill's marker "DOCTOR-CHECK-DEPS-a1"
    And the <engine> skill surface carries the doctor skill's marker "DOCTOR-CHECK-AGENTS-b2"
    And the <engine> skill surface carries the doctor skill's marker "DOCTOR-CHECK-VERSION-c3"
    And the <engine> skill surface carries the doctor skill's marker "DOCTOR-CHECK-HOOKS-TRUST-d4"

    Examples:
      | engine      |
      | claude-code |
      | kiro        |
      | antigravity |
      | opencode    |
      | codex       |

  # Structural invocability, beyond bare file-presence: opencode's skill
  # loader is registered explicitly in opencode.json (skillfiles.go's
  # reconcileSkillsSurface), not merely discovered by convention.
  Scenario: opencode registers the doctor skill's directory in opencode.json
    When Carol materializes the "clinic" profile for opencode
    Then opencode.json registers the skills surface

  # Exec bit survives materialization for a REAL surface (mirrors skill.feature's
  # own claude-code exec-bit scenario), proving the doctor skill's bundled
  # precheck script is actually runnable once it lands.
  Scenario: The doctor skill's script is materialized executable
    When Carol materializes the "clinic" profile for claude-code
    Then "out-claude-code/.claude/skills/ctxloom-doctor/scripts/run.sh" is executable
    And "out-claude-code/.claude/skills/ctxloom-doctor/scripts/run.sh" carries the marker "DOCTOR-SCRIPT-MARKER-9c2f"
