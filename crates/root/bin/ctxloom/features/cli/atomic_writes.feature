# world: base
# NOT ported: nothing; the write rules are new.

@doc
Feature: writes — every change to ctxloom's own trees is whole or absent, and a killed write converges on retry

  The daemon may be killed at any instant, so no write may leave a tree
  half done. Each write to ctxloom's OWN trees (bundles, skills, profiles,
  config, sessions) is ONE atomic operation: one file replaced (written
  under a temporary name, then renamed over), one new directory (built under
  a temporary name outside the trees ctxloom scans, then renamed in without
  replacing anything), or one move. A retry is then either a duplicate of
  the write (its request id) or a clean first apply.

  Where one operation cannot do it, a write is a RETRYABLE ATOMIC SEQUENCE:
  ordered atomic steps, each of which sees when it is already done, so the
  caller retries the whole sequence after a failure and it converges. It is
  one mechanism, used by:

    session edit <harp> --name <new>   the session's directory (its lock inside
                                       it, `sessions/<harp>/session.lock`) in
                                       one rename, then its output folder
    session remove <harp>              the directory renamed to trash, then the
                                       purge, then the trash removed
    materialize, doctor --fix,         other tools' files: each step ordered so a
    manage uninstall, bundle move      re-run converges (materialize writes its
                                       record first)

  AN EDIT IS ONE FILE. Inside an existing directory, a command changes one
  file: `bundle edit` adds or removes one item per call; `fragment edit`
  and `command edit` write the `.md` alone; distilling, or removing the
  distilled form, is its own command.

  A SKILL IS ONE DIRECTORY. Its metadata lives inside it
  (`skills/<name>/.meta.yaml`), and an MCP server's in its one
  `mcp/<name>.yaml`, so each is staged, renamed in and renamed to trash
  whole.

  A DISTILLED FORM NAMES ITS SOURCE. Each `.distilled.md` records the
  SHA-256 of the `.md` it was distilled from. Every time assembly would use
  it, the current `.md` is hashed and compared: only a match uses the
  distilled form; a mismatch, or no recorded digest, serves the original
  and says the distilled form is stale (as do `fragment show` and doctor).
  No earlier verdict is trusted.

  The existing contracts these change when they are built: run.feature's
  and session_remove.feature's session lock beside the directory, which
  moves inside it; the skill and MCP server layouts in mcp.feature and
  deps.feature; content_distill.feature's distilled files, which gain the
  digest; and bundle.feature's `bundle edit` with several items at once.

  Rule: A write to ctxloom's own trees is whole or absent

    Scenario Outline: A write killed in the middle leaves the tree as it was, and its retry applies it whole
      # new: one atomic operation per write.
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And the daemon will be killed outright in the middle of the write
      When I run '<write>'
      Then the command succeeds
      And the change is in the tree whole, once, and every bundle still reads

      Examples:
        | write                                                         |
        | ctxloom bundle create notes --description "my notes"          |
        | ctxloom fragment create demo#fragments/rules --body "be kind" |
        | ctxloom skill create demo#skills/review                       |
        | ctxloom config set engines.claude-code.model opus             |

    Scenario: A new directory is never renamed over one that appeared meanwhile
      # new: staged, then renamed in without replacing.
      Given an initialized ctxloom project
      And another process creates the bundle "notes" while its creation is staged
      When I run 'ctxloom bundle create notes --description "my notes"'
      Then the command exits with code 2
      And the other process's "notes" is untouched, and nothing staged is left behind

    Scenario: A skill is one directory, its metadata inside it
      # new: sidecars inside.
      Given an initialized ctxloom project
      And a bundle "demo" exists
      When I run "ctxloom skill create demo#skills/review"
      Then the command succeeds
      And the skill "review" is the one directory "skills/review" of the bundle "demo", holding its ".meta.yaml"

  Rule: An edit inside an existing directory changes one file

    Scenario: A bundle edit that names two items is refused
      # new: one change per call.
      Given an initialized ctxloom project
      And a bundle "demo" exists
      When I run "ctxloom bundle edit demo --add-fragment a --add-fragment b"
      Then the command exits with code 2
      And the output contains "one change per call"

    Scenario: A fragment edit writes its source alone, and leaves its distilled form stale
      # new: the edit writes the .md only.
      Given an initialized ctxloom project
      And a fragment "rules" in bundle "demo" with a fresh distilled form
      When I run 'ctxloom fragment edit demo#fragments/rules --body "be kinder"'
      Then the command succeeds
      And only "fragments/rules.md" of the bundle "demo" changed
      And "ctxloom fragment show demo#fragments/rules" says its distilled form is stale

  Rule: A distilled form is used only while it matches its source

    Scenario: After its source is edited, assembly serves the original and says the distilled form is stale
      # new: the digest is checked at every use.
      Given an initialized ctxloom project
      And a fragment "rules" in bundle "demo" with a fresh distilled form
      And a profile "dev" with bundle "demo"
      And the source of "rules" is edited by hand
      When I run "ctxloom context assemble --profile dev --format json"
      Then the command succeeds
      And the assembled context holds the original text of "rules", not its distilled form
      And the output reports "notices[0].code" as "library-application-stale-distilled"

    Scenario: A distilled form that records no digest is stale
      # new: an older distilled file.
      Given an initialized ctxloom project
      And a fragment "rules" in bundle "demo" whose distilled form records no source digest
      And a profile "dev" with bundle "demo"
      When I run "ctxloom context assemble --profile dev --format json"
      Then the command succeeds
      And the assembled context holds the original text of "rules", not its distilled form

    Scenario: Distilling again records the source's digest, and assembly uses the distilled form
      # new: distill is its own write.
      Given an initialized ctxloom project
      And a fragment "rules" in bundle "demo" whose distilled form records no source digest
      And a profile "dev" with bundle "demo"
      When I run "ctxloom fragment distill demo#fragments/rules"
      And I run "ctxloom context assemble --profile dev --format json"
      Then the command succeeds
      And the assembled context holds the distilled form of "rules"

  Rule: A multi-step write is a retryable atomic sequence, and a retry converges

    Scenario: A rename moves the session's directory with its lock in one step, then its output folder
      # new: the lock inside the directory.
      Given an initialized ctxloom project
      And a finished session "amber-swift-owl" with a transcript
      When I run "ctxloom session edit amber-swift-owl --name quiet-amber-fox"
      Then the command succeeds
      And the session's directory, its lock inside it, and its output folder are under "quiet-amber-fox"

    Scenario Outline: A sequence killed between its steps converges when the command is run again
      # new: the crash-mid-sequence retry.
      Given an initialized ctxloom project
      And a finished session "amber-swift-owl" with a transcript
      And the daemon will be killed outright <between>
      When I run '<command>'
      And I run '<command>'
      Then the command succeeds
      And <outcome>

      Examples:
        | between                                          | command                                                    | outcome                                                                         |
        | after the session's directory moved, before its output folder | ctxloom session edit amber-swift-owl --name quiet-amber-fox | the directory, lock and output folder are under "quiet-amber-fox", and its name is claimed once |
        | after the session's directory moved to trash, before the purge | ctxloom session remove amber-swift-owl --yes --even-if-live | nothing lists "amber-swift-owl", its streams are purged and no trash is left    |

    Scenario Outline: Another tool's files are written in steps that converge, whichever step is killed
      # new: the foreign-file writers.
      Given an initialized ctxloom project
      And the daemon will be killed outright after step <step> of '<command>'
      When I run '<command>'
      And I run '<command>'
      Then the command succeeds
      And the files it writes are as one uninterrupted run would leave them

      Examples:
        | command                                | step |
        | ctxloom materialize --to out --yes     | 1    |
        | ctxloom materialize --to out --yes     | 2    |
        | ctxloom doctor --fix                   | 1    |
        | ctxloom manage uninstall --yes         | 1    |
