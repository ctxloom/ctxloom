@doc
Feature: skill — authoring an Agent Skill package, curating it, and shipping it signed

  A "command" is a user-invoked slash template. An Agent Skill is a different
  thing: a directory carrying SKILL.md (frontmatter name + description, an
  instructions body) plus optional bundled scripts and assets, which an engine
  loads on its own by progressive disclosure.

  This is the comprehensive per-noun spec for `ctxloom skill` — authoring a
  package, recording its manifest, listing and showing it, curating which
  skills a profile exports, and round-tripping one through sign/export/import.
  The narrative version is journeys/j000600_agent_skill.feature, which asserts what
  a PERSON gets: one authored skill arriving whole in every engine's own skill
  folder.

  # WHERE THE BOUNDARY FALLS, and it is not where it first looks. MATERIALIZING
  # a skill into an engine's native directory is not a `skill` verb at all — it
  # is `profile materialize`, and the per-engine matrix that proves it lives in
  # j000600_agent_skill.feature across all five engines. A claude-only copy of that
  # matrix used to sit in this file (D6 in the suite-unification plan); it was
  # strictly weaker than j000600's, asserted nothing j000600 does not, and is gone. What
  # remains here is what the noun itself owns.

  Rule: Authoring a package records what is actually on disk

    `skill create` scaffolds a package and `skill sync` records the per-file
    manifest — sha256 and POSIX mode — into bundle.yaml. Listing and showing a
    skill must reflect that real tree rather than a name someone declared.


    Scenario: Alice authors a skill package and its manifest, listing, and show all reflect the real tree
      Given Alice's project has a directory-form bundle "vault"
      When I run "ctxloom skill create vault reviewer -d SKILL-MARKER-reviewer-9f3c21"
      And Alice adds an executable scripts/run.sh carrying the marker "SCRIPT-MARKER-reviewer-2b7e40" to the skill "vault#skills/reviewer"
      And I run "ctxloom skill sync vault#skills/reviewer"
      Then the bundle manifest for "vault#skills/reviewer" records real hashes and modes for "SKILL.md" and "scripts/run.sh"
      When I run "ctxloom skill list"
      Then the skill list output includes "reviewer" from bundle "vault"
      When I run "ctxloom skill show vault#skills/reviewer"
      Then the skill show output carries the frontmatter description "SKILL-MARKER-reviewer-9f3c21"
      When the agent reads resource "ctxloom://skills"
      Then the skill resource contains "reviewer"
      When the agent reads resource "ctxloom://skills/reviewer"
      Then the skill resource contains "SKILL-MARKER-reviewer-9f3c21"

  Rule: A skill package can be created and removed

    Before `skill remove`, a skill package could be created and never
    removed at all. Bare invocation reports what would be removed and
    removes nothing; --yes applies it — the same posture every other
    `remove` leaf in this CLI shares.


    Scenario: Removing a skill drops both its bundle.yaml registration and its on-disk directory
      Given Alice's project has a directory-form bundle "vault"
      And I run "ctxloom skill create vault reviewer -d SKILL-MARKER-reviewer-9f3c21"
      When I run "ctxloom skill remove vault#skills/reviewer"
      Then the command succeeds
      And the output contains "Nothing was removed"
      And the output contains "--yes"
      When I run "ctxloom skill list"
      Then the skill list output includes "reviewer" from bundle "vault"
      When I run "ctxloom skill remove vault#skills/reviewer --yes"
      Then the command succeeds
      And the output contains "Removed skill"
      And the file ".ctxloom/content/bundles/vault/skills/reviewer" does not exist
      When I run "ctxloom skill list"
      Then the output does not contain "reviewer"

    Scenario: Removing a skill nobody created is refused, not silently reported as done
      Given Alice's project has a directory-form bundle "vault"
      When I run "ctxloom skill remove vault#skills/never-created --yes"
      Then the command fails

  Rule: A profile exports only the skills it curates

    Shipping a skill in a bundle does not push it at every project. Curation is
    the choice, so a bundle can offer more than any one profile takes.


    Scenario: Profile skill curation exports only the curated skill, not the bundle's other one
      Given Alice's project has a directory-form bundle "vault"
      And I run "ctxloom skill create vault reviewer -d SKILL-MARKER-reviewer-9f3c21"
      And I run "ctxloom skill sync vault#skills/reviewer"
      And I run "ctxloom skill create vault planner -d SKILL-MARKER-planner-1a2b3c"
      And I run "ctxloom skill sync vault#skills/planner"
      And a profile "studio" with bundle "vault"
      And profile "studio" curates skill "vault#skills/reviewer"
      When I run "ctxloom profile materialize studio --target out-curated --backend claude-code"
      Then "out-curated/.claude/skills/reviewer/SKILL.md" carries the marker "SKILL-MARKER-reviewer-9f3c21"
      And "out-curated/.claude/skills/planner" does not exist

  Rule: A bundle-authored skill outranks a builtin of the same name

    Names collide. When they do, what the project authored wins over what
    ctxloom ships, or a team cannot override guidance they disagree with.


    Scenario: On kiro, a bundle-authored skill wins over the builtin command of the same name
      Given Alice's project has a directory-form bundle "vault"
      And I run "ctxloom skill create vault discover -d SKILL-MARKER-discover-6f19aa"
      And I run "ctxloom skill sync vault#skills/discover"
      And a profile "studio" with bundle "vault"
      When I run "ctxloom profile materialize studio --target out-kiro --backend kiro"
      Then "out-kiro/.kiro/skills/discover" carries the "discover" skill's content, not the builtin command's prose
      And ctxloom warned that the skill won over the command of the same name

  Rule: A package's signature is reported honestly, and its bytes always land

    Import reports what it could verify — trusted, untrusted, or tampered — and
    never silently upgrades one to another. The files land byte-for-byte
    regardless, so verification is a REPORT about provenance rather than a
    filter that quietly drops content.


    Scenario Outline: A skill's signature is reported honestly on import, and its files always land byte-for-byte
      Given Alice's project has a directory-form bundle "vault"
      And I run "ctxloom skill create vault reviewer -d SKILL-MARKER-reviewer-9f3c21"
      And Alice adds an executable scripts/run.sh carrying the marker "SCRIPT-MARKER-reviewer-2b7e40" to the skill "vault#skills/reviewer"
      And I run "ctxloom skill sync vault#skills/reviewer"
      And a directory-form bundle "landed" exists
      When I run "ctxloom skill export vault#skills/reviewer -o reviewer.zip"
      And <signer>'s key signs the skill "vault#skills/reviewer" over its current manifest, into "reviewer.zip.sig"
      And Trent is a trusted publisher for this project
      And I run "ctxloom skill import reviewer.zip --bundle landed --sig reviewer.zip.sig --format json"
      Then the import reports the signature as <outcome>
      And the imported files under "landed#skills/reviewer" match the originally-authored "vault#skills/reviewer", byte for byte

      Examples:
        | signer  | outcome    |
        | Trent   | verified   |
        | Mallory | unverified |


    Scenario: A skill package tampered with after signing fails verification even though it was legitimately signed
      Given Alice's project has a directory-form bundle "vault"
      And I run "ctxloom skill create vault reviewer -d SKILL-MARKER-reviewer-9f3c21"
      And Alice adds an executable scripts/run.sh carrying the marker "SCRIPT-MARKER-reviewer-2b7e40" to the skill "vault#skills/reviewer"
      And I run "ctxloom skill sync vault#skills/reviewer"
      And Trent's key signs the skill "vault#skills/reviewer" over its current manifest, into "reviewer-tampered.zip.sig"
      And Trent is a trusted publisher for this project
      And the skill "vault#skills/reviewer"'s SKILL.md is modified after signing
      And a directory-form bundle "landed-tampered" exists
      When I run "ctxloom skill export vault#skills/reviewer -o reviewer-tampered.zip"
      And I run "ctxloom skill import reviewer-tampered.zip --bundle landed-tampered --sig reviewer-tampered.zip.sig --format json"
      Then the import reports the signature as unverified

  Rule: A curated skill lands in every engine's own skill folder

    Where a skill LANDS is skill-specific knowledge — each engine names
    the folder differently and one of them is not even pluralised — so it
    belongs to this noun, even though the command that triggers it is
    `profile materialize`. The trigger is not the subject.

    # PATH CORRECTION vs the command surface (j000400_multi_engine.feature's own
    # table): a skill is NOT the same directory a flat command file lands in.
    # Verified against each engine's own skillfiles.go, and — for codex —
    # against a REAL `profile materialize --backend codex` run, not just its
    # source (see steps_j000600_doctor.go's engineSkillMDPath doc for the full
    # story):
    #
    #   | engine      | skill surface (via `profile materialize`) | scope       |
    #   |-------------|---------------------------------------------|-------------|
    #   | claude-code | .claude/skills/<name>/SKILL.md              | project     |
    #   | kiro        | .kiro/skills/<name>/SKILL.md                | project     |
    #   | opencode    | .opencode/skill/<name>/SKILL.md             | project     |
    #   | codex       | .codex/skills/<name>/SKILL.md               | cell-scoped |
    #
    # codex's OWN skills directory is documented GLOBAL ($CODEX_HOME/skills —
    # internal/codex/skillfiles.go), but that path only fires on the LIVE
    # run/launch path. `profile materialize` binds codex's Skills surface
    # through NewSurfaces' inline closure with no homeOverride, which — just
    # like its Commands surface (j000400_multi_engine.feature's own codex row) —
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
        | opencode    |
        | codex       |

    # Structural invocability, beyond bare file-presence: opencode's skill
    # loader is registered explicitly in opencode.json (skillfiles.go's
    # reconcileSkillsSurface), not merely discovered by convention.
    Scenario: opencode registers the doctor skill's directory in opencode.json
      When Carol materializes the "clinic" profile for opencode
      Then opencode.json registers the skills surface

    # Exec bit survives materialization for a REAL surface, proving the doctor
    # skill's bundled precheck script is actually runnable once it lands. This is
    # now the ONLY place that claim is made: cli/skill.feature carried a
    # claude-only copy of it, strictly weaker than this file's five-engine
    # matrix, and it was folded away rather than kept in both.
    Scenario: The doctor skill's script is materialized executable
      When Carol materializes the "clinic" profile for claude-code
      Then "out-claude-code/.claude/skills/ctxloom-doctor/scripts/run.sh" is executable
      And "out-claude-code/.claude/skills/ctxloom-doctor/scripts/run.sh" carries the marker "DOCTOR-SCRIPT-MARKER-9c2f"
