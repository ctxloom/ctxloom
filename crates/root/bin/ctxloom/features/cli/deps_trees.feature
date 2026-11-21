# world: surfaces
# Ported from cli/deps.feature (rev f36e8dfb2)
# Ported from journeys/j001400_bundle_distribution.feature (rev f36e8dfb2)
# NOT ported: nothing.
# Split from cli/deps.feature (see its header): these Rules, their text unchanged.

@doc
Feature: deps — holds, and the tree a pull keeps

  Covers `deps hold`, and a pull keeping a published bundle's tree exactly as published. The verbs, the closure and the lockfile are
  described in cli/deps.feature; where content comes FROM is cli/remote.feature.

  Rule: A hold freezes one dependency, even against a forced pull

    `deps hold` is the opt-out: a held entry stays frozen against `upgrade`,
    and against a `pull --force`, which reinstalls each reference rather than
    skipping it.

    Scenario: A held dependency is not upgraded
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --include origin/demo"
      And I run "ctxloom deps pull"
      And I run "ctxloom deps hold origin/demo"
      And the remote "origin" advances its bundle
      When Alice tries to advance a held pin:
        """
        ctxloom deps upgrade --format text
        """
      Then the command succeeds
      And the output contains "up to date"

    # THE MECHANIC A NEW CLONE RELIES ON (see j000800_onboarding.feature's
    # "Bob receives the versions the team pinned, not the latest ones"): an
    # ordinary pull SKIPS a reference it already considers installed and
    # never consults the pin at all. `--force` puts the pull on the full
    # fetch-and-record path (Puller.Pull, then Puller.updateLockfile) against
    # the advanced remote, and the held content has to survive it.
    Scenario: A held dependency's content survives even a forced pull
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --include origin/demo"
      And I run "ctxloom deps pull"
      And I run "ctxloom deps hold origin/demo"
      And the remote "origin" advances its bundle
      When Alice forces a pull of every reference:
        """
        ctxloom deps pull --force
        """
      Then the command succeeds
      And the file ".ctxloom/lock.yaml" contains "held: true"
      When I run "ctxloom materialize dev --target out --backend claude-code"
      Then the file "out/CLAUDE.md" contains "Demo fragment content."
      And the file "out/CLAUDE.md" does not contain "version two"

    Scenario: Unholding lets the pin move again
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --include origin/demo"
      And I run "ctxloom deps pull"
      And I run "ctxloom deps hold origin/demo"
      And the remote "origin" advances its bundle
      When Alice releases the freeze:
        """
        ctxloom deps unhold origin/demo
        """
      Then the command succeeds
      When I run "ctxloom deps upgrade --yes --format text"
      Then the command succeeds
      And the output contains "Applied 1 pin(s)."

  Rule: A pull keeps a published bundle's tree exactly as it was published

    A pulled bundle is the publisher's tree, file for file. Each kind keeps its
    metadata where that kind stores it, structures stay structures, a skill
    package arrives whole with its modes, and hooks keep their declared order.

    Background:
      Given Trent authors a directory-form bundle "atelier" carrying every surface kind

    # Every artifact, profile included, is compared with the published bytes.
    # The steps read the consumer's pulled tree, not the project directory.
    Scenario Outline: A pulled tree carries every published artifact byte for byte
      # new: the publication half of j001400's per-kind scenario and of its profile scenario.
      Given Trent publishes the "atelier" tree to his company repo
      When Alice references the company's "atelier" bundle and pulls it
      Then Alice's pulled "atelier" tree carries "<artifact>" byte for byte as published

      Examples:
        | artifact                        |
        | fragments/house-style.md        |
        | prompts/ship-it.md              |
        | mcp/ledger.yaml                 |
        | hooks/post_file_edit/stamp.yaml |
        | skills/reviewer/SKILL.md        |
        | profiles/studio.yaml            |

    # Content kinds carry their metadata as front-matter inside the file; an
    # MCP server carries it in its one file, and a skill in the sidecar inside
    # its own directory (owner T27), which a pull that copied only content
    # files would lose.
    Scenario Outline: Each kind's metadata survives publication in the place that kind stores it
      Given Trent publishes the "atelier" tree to his company repo
      When Alice references the company's "atelier" bundle and pulls it
      Then Alice's pulled "atelier" tree stores the "<kind>" metadata "<probe>" as <placement>

      Examples:
        | kind     | probe                    | placement                                  |
        | fragment | ATELIER-FRAGMENT-DESC    | front-matter in "fragments/when-shipping.md" |
        | command  | ATELIER-COMMAND-DESC     | front-matter in "prompts/ship-it.md"       |
        | mcp      | ATELIER-MCP-DESC         | a field of "mcp/ledger.yaml"               |
        | skill    | ATELIER-SKILL-DESC       | the sidecar "skills/reviewer/.meta.yaml"   |

    # An MCP server is a structure: its command, args and env are compared
    # parsed, so a flattened or reordered round trip fails where a substring
    # match would pass.
    Scenario: A published MCP server's configuration structure survives, not merely its text
      Given Trent publishes the "atelier" tree to his company repo
      When Alice references the company's "atelier" bundle and pulls it
      Then Alice's pulled "atelier" MCP server "ledger" parses with its command, args and env intact

    # A skill is the one multi-file item, and the one where a file's mode
    # matters: the whole package is compared, and its script must stay
    # executable.
    Scenario: A published skill package arrives whole, with its script still executable
      Given Trent publishes the "atelier" tree to his company repo
      When Alice references the company's "atelier" bundle and pulls it
      Then Alice's pulled skill "reviewer" contains exactly the files Trent published, byte for byte
      And Alice's pulled skill "reviewer" file "scripts/run.sh" is executable

    # A tree stores one file per hook, so a hook's place in its event's
    # sequence is the order field of its sidecar. The fixture's declared order
    # (stamp before audit) disagrees with the alphabetical one, and the pulled
    # hooks are read the way assembly reads them: bucketed by each hook's own
    # declared event and sorted by its order.
    Scenario: Hooks arrive in the right event buckets AND in their declared order within each
      Given Trent publishes the "atelier" tree to his company repo
      When Alice references the company's "atelier" bundle and pulls it
      Then Alice's pulled "atelier" hooks under "post_file_edit" are exactly "stamp, audit" in that order
      And Alice's pulled "atelier" hooks under "session_start" are exactly "greet" in that order
      And no pulled "atelier" hook appears under an event it was not declared in
