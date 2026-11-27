# world: base
# NOT ported: nothing; the write rules are new.

@doc
Feature: writes — every change to ctxloom's own trees is whole or absent, and a killed write converges on retry

  The daemon may be killed at any instant, so no write may leave a tree
  half done. Each write to ctxloom's OWN trees (bundles, skills, profiles,
  config, sessions) is ONE atomic operation: one file replaced (written
  under a temporary name, then renamed over), one new directory (built under
  the tree's `.ctxloom/state/staging/`, then renamed in without replacing
  anything), one path renamed into the trash, or one move. Nothing is ever
  staged beside its target: git would see it (`??`), and the next read
  would record it.

  A WRITE IS INTENDED FIRST. The daemon observes the tree (as every read
  does, cli/library_changes.feature), plans, and records the write's INTENT
  under the request id the command chose: the paths it will change and
  git's object ids for what it will write. Only then does it carry the
  operation out, look at the paths again, and record the step done. So a
  daemon killed at any point leaves either the tree as it was with the
  intent pending, or the change made with its completion owed; the
  command's one retry (cli/daemon.feature) is a duplicate of the intent and
  finishes it, and the change is recorded as ctxloom's, never as an outside
  one. A write planned against a path whose record has moved on since (its
  etag: the sequence of the last event that named the path) is refused as
  stale and writes nothing.

  Where one operation cannot do it, a write is a RETRYABLE ATOMIC SEQUENCE:
  ordered atomic steps, listed in its intent, each of which sees when it is
  already done, so the caller retries the whole sequence after a failure and
  it converges at the first step not done. It is one mechanism, used by:

    session remove <harp>              the session's directory (its lock inside
                                       it, `sessions/<harp>/session.lock`)
                                       renamed to trash, then the purge, then
                                       the trash removed
    bundle import --force              the old bundle renamed to trash, then the
                                       new one renamed in, then the trash removed
    materialize, doctor --fix,         other tools' files: each step ordered so a
    manage uninstall,                  re-run converges (materialize writes its
    bundle move --to <remote>          record first)

  A local `bundle move` is one move. A writer of other tools' files lists
  them, with git's ids for what it will write, in its intent (L15), so a
  killed one is finished by its re-run and never observed as the library.

  A PULL IS A SEQUENCE (step 10c). `deps pull` and `deps upgrade` install
  each pinned commit's tree whole, as one new directory per commit under
  the cache, then replace `lock.yaml` once. A pull killed between its steps
  converges when it is run again: a tree already installed is a step done,
  and the lock is written last, so it never names a tree that is not
  there.

  TRASH. A removal renames what it removes into
  `.ctxloom/state/trash/<request id>/`, outside the library. A sequence's
  own trash is removed by its last step, so it leaves none. What `bundle
  remove`, `skill remove` and `clean` take is kept there for 7 days, then
  purged by the daemon. Staging is swept when a daemon starts; the trash is
  not, so what a removal kept survives a restart.

  AN EDIT IS ONE FILE. Inside an existing directory, a command changes one
  file: `bundle edit` adds or removes one item per call; `fragment edit`
  and `command edit` write the `.md` alone; distilling, or removing the
  distilled form, is its own command.

  A SKILL IS ONE DIRECTORY. Its metadata lives inside it
  (`skills/<name>/.meta.yaml`), and an MCP server's in its one
  `mcp/<name>.yaml`, so each is staged, renamed in and renamed to trash
  whole. A bundle still in the old layout (`skills/.<name>.meta.yaml`,
  `mcp/.<name>.meta.yaml`) is refused, naming the `git mv` that moves it.

  A DISTILLED FORM NAMES ITS SOURCE. Each `.distilled.md` records the
  SHA-256 of the `.md` it was distilled from. Every time assembly would use
  it, the current `.md` is hashed and compared: only a match uses the
  distilled form; a mismatch, or no recorded digest, serves the original
  and says the distilled form is stale (as do `fragment show` and doctor).
  No earlier verdict is trusted.

  The existing contracts these change: run.feature's and
  session_remove.feature's session lock, which moves inside the session's
  directory; the skill and MCP server layouts in mcp.feature and
  deps.feature; and bundle.feature's `bundle edit` with several items at
  once.

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
        | ctxloom config set engines.claude-code.command claude-beta    |

    Scenario Outline: A write killed at any point of it converges on its one retry, and is recorded once, as ctxloom's
      # new: step 10 U3, the four kill points; L3, intent then operation then fact.
      Given an initialized ctxloom project
      And the project's changes are committed
      And I run "ctxloom bundle list"
      And the daemon will be killed outright <point>
      When I run 'ctxloom bundle create notes --description "my notes"'
      Then the command succeeds
      And the command started a new daemon and sent the write once more, under the same id
      And the change is in the tree whole, once, and every bundle still reads
      And nothing is left under ".ctxloom/state/staging"
      When I run "ctxloom bundle list"
      And I run "ctxloom test library-events --format json"
      Then the output reports "events" having "3" entries
      And the output reports "events[1].type" as "ChangeIntended"
      And the output reports "events[1].verb" as "bundle create"
      And the output reports "events[1].steps[0].kind" as "create"
      And the output reports "events[1].steps[0].path" as "content/bundles/v2/notes"
      And the output reports "events[2].type" as "StepApplied"
      And the output reports "events[2].request_id" as the value of "events[1].request_id"

      Examples:
        | point                                                     |
        | after the write's intent is recorded, before it is staged |
        | after the write is staged, before it is renamed in        |
        | after the write is renamed in, before it is recorded done |
        | after the write is recorded done, before it is answered   |

    Scenario: A new directory is never renamed over one that appeared meanwhile
      # new: staged, then renamed in without replacing.
      Given an initialized ctxloom project
      And another process creates the bundle "notes" while its creation is staged
      When I run 'ctxloom bundle create notes --description "my notes"'
      Then the command exits with code 48
      And the refusal is "library-tree-resource-exists"
      And the other process's "notes" is untouched, and nothing staged is left behind

    Scenario: A skill is one directory, its metadata inside it
      # new: sidecars inside.
      Given an initialized ctxloom project
      And a bundle "demo" exists
      When I run "ctxloom skill create demo#skills/review"
      Then the command succeeds
      And the skill "review" is the one directory "skills/review" of the bundle "demo", holding its ".meta.yaml"

    Scenario Outline: A bundle whose metadata is in the old layout is refused, naming the git mv that moves it
      # new: step 10 D15, T27's hard migrate: refused with a fix, never read or moved by ctxloom.
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And the project already has the file ".ctxloom/content/bundles/v2/demo/<old>":
        """
        description: in the old place
        """
      When I run "ctxloom bundle show demo"
      Then the command exits with code 48
      And the refusal is "library-sidecar-old-layout"
      And the output contains "git mv .ctxloom/content/bundles/v2/demo/<old> .ctxloom/content/bundles/v2/demo/<new>"
      And the file ".ctxloom/content/bundles/v2/demo/<old>" exists

      Examples:
        | old                      | new                      |
        | skills/.review.meta.yaml | skills/review/.meta.yaml |
        | mcp/.tools.meta.yaml     | mcp/tools.yaml           |

  Rule: A write is applied only to what it was planned against

    Scenario: An edit whose file was changed by hand while her editor was open is refused as stale, and the hand edit survives
      # new: step 10 U3, and G4 as superseded (owner, 2026-11-16, night): the etag is the file's last
      # sequence in the tree's record; the editor is the preview's Edit need.
      Given a ctxloom project with a marker editor
      And a bundle "demo" exists
      And the project's changes are committed
      And the file ".ctxloom/content/bundles/v2/demo/fragments/example.md" is changed by hand while the editor is open, to:
        """
        changed by hand meanwhile
        """
      When I run "ctxloom fragment edit demo#fragments/example"
      Then the command exits with code 48
      And the refusal is "library-tree-plan-stale"
      And the output contains "fragments/example.md"
      And the output contains "look again"
      And the file ".ctxloom/content/bundles/v2/demo/fragments/example.md" contains "changed by hand meanwhile"
      And the file ".ctxloom/content/bundles/v2/demo/fragments/example.md" does not contain "EDITED-BY-TEST"

    Scenario: An edit with nothing changed under it is applied
      # new: the paired negative; the file's sequence has not moved, so the same edit applies.
      Given a ctxloom project with a marker editor
      And a bundle "demo" exists
      And the project's changes are committed
      When I run "ctxloom fragment edit demo#fragments/example"
      Then the command succeeds
      And the file ".ctxloom/content/bundles/v2/demo/fragments/example.md" contains "EDITED-BY-TEST"

    Scenario: An edit whose file was changed by hand, read, and changed back while her editor was open is refused as stale too
      # new: G4 superseded (owner, 2026-11-16, night): the etag is the sequence, not git's id, so a change
      # recorded and then undone still moves it; the file's id at the apply equals the preview's, and the
      # apply is refused all the same.
      Given a ctxloom project with a marker editor
      And a bundle "demo" exists
      And the project's changes are committed
      And the file ".ctxloom/content/bundles/v2/demo/fragments/example.md" is changed by hand while the editor is open, read, and changed back, to:
        """
        changed by hand meanwhile
        """
      When I run "ctxloom fragment edit demo#fragments/example"
      Then the command exits with code 48
      And the refusal is "library-tree-plan-stale"
      And the output contains "fragments/example.md"
      And the file ".ctxloom/content/bundles/v2/demo/fragments/example.md" does not contain "EDITED-BY-TEST"
      And the file ".ctxloom/content/bundles/v2/demo/fragments/example.md" does not contain "changed by hand meanwhile"

  Rule: An edit inside an existing directory changes one file

    Scenario: A bundle edit that names two items is refused
      # new: one change per call.
      Given an initialized ctxloom project
      And a bundle "demo" exists
      When I run "ctxloom bundle edit demo --add-fragment a --add-fragment b"
      Then the command exits with code 48
      And the refusal is "library-tree-one-change-per-call"
      And the output contains "one change per call"
      And the file ".ctxloom/content/bundles/v2/demo/fragments/a.md" does not exist

    Scenario: A fragment edit writes its source alone, and leaves its distilled form stale
      # new: the edit writes the .md only.
      Given an initialized ctxloom project
      And a fragment "rules" in bundle "demo" with a fresh distilled form
      When I run 'ctxloom fragment edit demo#fragments/rules --body "be kinder"'
      Then the command succeeds
      And only "fragments/rules.md" of the bundle "demo" changed
      And "ctxloom fragment show demo#fragments/rules" says its distilled form is stale

  Rule: A distilled form is used only while it matches its source

    Scenario: The digest is checked at each use, so a verdict from an earlier assembly is never kept
      # new: no cached verdict.
      Given an initialized ctxloom project
      And a fragment "rules" in bundle "demo" with a fresh distilled form
      And a profile "dev" with bundle "demo"
      When I run "ctxloom context assemble --profile dev --format json"
      Then the assembled context holds the distilled form of "rules"
      When the source of "rules" is edited by hand
      And I run "ctxloom context assemble --profile dev --format json"
      Then the assembled context holds the original text of "rules", not its distilled form
      # changed: owner ruling 2026-11-18, presence not position: the notices hold the code, wherever it is.
      And the output reports "notices[code=library-application-stale-distilled].code" as "library-application-stale-distilled"

    Scenario: After its source is edited, assembly serves the original and says the distilled form is stale
      # new: the digest is checked at every use.
      Given an initialized ctxloom project
      And a fragment "rules" in bundle "demo" with a fresh distilled form
      And a profile "dev" with bundle "demo"
      And the source of "rules" is edited by hand
      When I run "ctxloom context assemble --profile dev --format json"
      Then the command succeeds
      And the assembled context holds the original text of "rules", not its distilled form
      # changed: owner ruling 2026-11-18, presence not position: the notices hold the code, wherever it is.
      And the output reports "notices[code=library-application-stale-distilled].code" as "library-application-stale-distilled"

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

  Rule: What a removal takes is kept in the trash for 7 days; a sequence leaves none

    Scenario Outline: A removal keeps what it took in the trash, and the daemon purges it after 7 days
      # new: step 10 G12 (N = 7 days) and L12: the trash is recorded, kept across a restart, then purged.
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And I run "ctxloom skill create demo#skills/review"
      And the project already has the file ".ctxloom/cache/context/assembled.md":
        """
        assembled context
        """
      When I run '<removal>'
      Then the command succeeds
      And the file ".ctxloom/<path>" does not exist
      And the checkout's trash holds ".ctxloom/<path>" of that removal, whole
      When I run "ctxloom daemon stop"
      And "6d23h" pass on the daemon's clock, and its next round runs
      Then the checkout's trash holds ".ctxloom/<path>" of that removal, whole
      When "1h" more pass on the daemon's clock, and its next round runs
      Then the checkout's trash holds nothing
      And nothing is left under ".ctxloom/state/trash"

      Examples:
        | removal                                       | path                                  |
        | ctxloom bundle remove demo --yes              | content/bundles/v2/demo               |
        | ctxloom skill remove demo#skills/review --yes | content/bundles/v2/demo/skills/review |
        | ctxloom clean --yes                           | cache/context                         |

    @purge
    Scenario Outline: A sequence that takes something to the trash leaves no trash once it ends
      # new: step 10 G12(a), a sequence's own trash is purged by its last step.
      Given an initialized ctxloom project
      And a finished session "amber-swift-owl" with a transcript
      And a bundle "notes" exists
      And I run "ctxloom bundle export notes exported"
      When I run '<command>'
      Then the command succeeds
      And nothing is left under ".ctxloom/state/trash"

      Examples:
        | command                                                      |
        | ctxloom session remove amber-swift-owl --yes --even-if-live |
        | ctxloom bundle import exported/notes --force --yes           |

    Scenario: A write killed after staging leaves nothing staged once the next daemon starts
      # new: S50, staging is swept at a daemon's start; the trash is not.
      Given an initialized ctxloom project
      And a bundle "demo" exists
      And I run "ctxloom bundle remove demo --yes"
      And a write left its staging behind under ".ctxloom/state/staging" when its daemon was killed
      When I run "ctxloom daemon stop"
      And I run "ctxloom bundle list"
      Then the command succeeds
      And nothing is left under ".ctxloom/state/staging"
      And the checkout's trash holds ".ctxloom/content/bundles/v2/demo" of that removal, whole

  Rule: A multi-step write is a retryable atomic sequence, and a retry converges

    Scenario: A session's lock is inside its directory, so the two move as one
      # new: the lock inside the directory.
      Given an initialized ctxloom project
      And a session "amber-swift-owl" that is still running
      Then its lock is "sessions/amber-swift-owl/session.lock", inside its directory

    @purge
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
        | after the session's directory moved to trash, before the purge | ctxloom session remove amber-swift-owl --yes --even-if-live | nothing lists "amber-swift-owl", its streams are purged and no trash is left    |
        | after the old bundle moved to trash, before the new one moved in | ctxloom bundle import exported/notes --force --yes          | the bundle "notes" is the imported one, whole, and no trash is left              |

    Scenario Outline: Another tool's files are written in steps that converge, whichever step is killed
      # new: the foreign-file writers (step 10 U10).
      Given an initialized ctxloom project
      And the daemon will be killed outright after step <step> of '<command>'
      When I run '<command>'
      And I run '<command>'
      Then the command succeeds
      And the files it writes are as one uninterrupted run would leave them

      Examples:
        | command                                | step |
        | ctxloom materialize --target out --yes | 1    |
        | ctxloom materialize --target out --yes | 2    |
        | ctxloom doctor --fix                   | 1    |
        | ctxloom manage uninstall               | 1    |

    Scenario: Another tool's files are recorded as their command's intent in a project outside git too
      # new: DRAFT (the library is git-free, owner 2026-11-18): U10's unrecorded outside-git fallback is gone; a foreign write is recorded in any directory.
      Given an initialized ctxloom project outside git
      When I run 'ctxloom materialize --target out --yes'
      Then the command succeeds
      When I run "ctxloom test library-events --format json"
      Then the output reports "events[1].type" as "ChangeIntended"
      And the output reports "events[1].verb" as "materialize"
      And the output reports "events[2].type" as "StepApplied"

  Rule: A pull is a sequence of whole trees, then the lock, and a killed pull converges

    Scenario Outline: A pull killed between its steps converges when it is run again
      # new: step 10 U4 and O6.3, a killed pull.
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And a git remote "vendor" serving a ctxloom bundle
      And I run "ctxloom profile create dev --include origin/demo --include vendor/demo"
      And the daemon will be killed outright <between>
      When I run "ctxloom deps pull"
      And I run "ctxloom deps pull"
      Then the command succeeds
      And the file ".ctxloom/lock.yaml" contains "//bundles/demo" exactly 2 times
      And every tree the lock pins is installed whole, once
      And nothing is left under ".ctxloom/state/staging"

      Examples:
        | between                                                  |
        | after the first tree is installed, before the second     |
        | after every tree is installed, before the lock is written |
        | after the lock is written, before the pull is answered    |

    Scenario: A lock is never left naming a tree that is not installed
      # new: step 10 U4, the lock is the sequence's last step.
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --include origin/demo"
      And the daemon will be killed outright after every tree is installed, before the lock is written
      And the command's retry is turned off
      When I run "ctxloom deps pull"
      Then the command exits with code 32
      And the file ".ctxloom/lock.yaml" does not exist
      When I run "ctxloom deps pull"
      Then the command succeeds
      And every tree the lock pins is installed whole, once
