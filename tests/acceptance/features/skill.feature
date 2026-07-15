@doc
Feature: Agent Skills — SKILL.md packages, distinct from a slash command

  A "command" (ctxloom command) is a user-invoked slash template. A "skill" is
  a different thing an engine loads on its own, by progressive disclosure: a
  directory carrying SKILL.md (frontmatter name+description, an instructions
  body) plus optional bundled scripts/assets. This journey proves the real,
  end-to-end Agent Skill surface: author a package with `ctxloom skill
  create`, sync its manifest, materialize it into an engine's own native
  skills directory (exec bits intact), curate which skills a profile exports,
  and round-trip a package through sign/export/import — verified against a
  trusted publisher, or honestly reported as not verified when the signer
  isn't trusted or the package was tampered with after signing.

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

  Scenario: A curated skill materializes into claude's native Agent Skills directory with its exec bit intact
    Given Alice's project has a directory-form bundle "vault"
    And I run "ctxloom skill create vault reviewer -d SKILL-MARKER-reviewer-9f3c21"
    And Alice adds an executable scripts/run.sh carrying the marker "SCRIPT-MARKER-reviewer-2b7e40" to the skill "vault#skills/reviewer"
    And I run "ctxloom skill sync vault#skills/reviewer"
    And a profile "studio" with bundle "vault"
    And profile "studio" curates skill "vault#skills/reviewer"
    When I run "ctxloom profile materialize studio --target out-claude --backend claude-code"
    Then "out-claude/.claude/skills/reviewer/SKILL.md" carries the marker "SKILL-MARKER-reviewer-9f3c21"
    And "out-claude/.claude/skills/reviewer/scripts/run.sh" is executable
    And "out-claude/.claude/skills/reviewer/scripts/run.sh" carries the marker "SCRIPT-MARKER-reviewer-2b7e40"

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

  Scenario: On kiro, a bundle-authored skill wins over the builtin command of the same name
    Given Alice's project has a directory-form bundle "vault"
    And I run "ctxloom skill create vault recover -d SKILL-MARKER-recover-6f19aa"
    And I run "ctxloom skill sync vault#skills/recover"
    And a profile "studio" with bundle "vault"
    When I run "ctxloom profile materialize studio --target out-kiro --backend kiro"
    Then "out-kiro/.kiro/skills/recover" carries the "recover" skill's content, not the builtin command's prose
    And ctxloom warned that the skill won over the command of the same name

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
