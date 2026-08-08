@doc
Feature: skill — authoring an Agent Skill package, curating it, and shipping it signed

  A "command" is a user-invoked slash template. An Agent Skill is a different
  thing: a directory carrying SKILL.md (frontmatter name + description, an
  instructions body) plus optional bundled scripts and assets, which an engine
  loads on its own by progressive disclosure.

  This is the comprehensive per-noun spec for `ctxloom skill` — authoring a
  package, recording its manifest, listing and showing it, curating which
  skills a profile exports, and round-tripping one through sign/export/import.
  The narrative version is journeys/j6_agent_skill.feature, which asserts what
  a PERSON gets: one authored skill arriving whole in every engine's own skill
  folder.

  # WHERE THE BOUNDARY FALLS, and it is not where it first looks. MATERIALIZING
  # a skill into an engine's native directory is not a `skill` verb at all — it
  # is `profile materialize`, and the per-engine matrix that proves it lives in
  # j6_agent_skill.feature across all five engines. A claude-only copy of that
  # matrix used to sit in this file (D6 in the suite-unification plan); it was
  # strictly weaker than j6's, asserted nothing j6 does not, and is gone. What
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
