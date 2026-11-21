# world: surfaces
# Ported from cli/deps.feature (rev f36e8dfb2)
# Ported from journeys/j001400_bundle_distribution.feature (rev f36e8dfb2)
# NOT ported: nothing.
# Split from cli/deps.feature (see its header): these Rules, their text unchanged.

@doc
Feature: deps — what upstream changing, or being unreachable, does to the installation

  Covers a pull reconciling to upstream, an unreachable upstream, the daemon running the pull's git, and the pin against the payload. The verbs, the closure and the lockfile are
  described in cli/deps.feature; where content comes FROM is cli/remote.feature.

  Rule: Pull reconciles to upstream — it removes what upstream no longer has

    Installed content is a PROJECTION of remote state, not user data: every
    byte of it can be re-fetched from the address it came from. So a pull that
    finds a bundle gone upstream removes it locally and says which, by name,
    without asking. That is synchronization, not destruction, and it is why
    removing asks for no `--yes`.

    Scenario: A bundle deleted upstream is removed locally, by name
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --include origin/demo"
      And I run "ctxloom deps pull"
      And the remote "origin" stops publishing bundle "demo"
      When Alice pulls after upstream deleted the bundle:
        """
        ctxloom deps pull --format text
        """
      Then the command succeeds
      And the output contains "no longer published"
      And the output contains "demo"
      And the file ".ctxloom/lock.yaml" does not contain "//bundles/demo"

  Rule: "I could not reach upstream" is never "upstream deleted everything"

    THE PATHOLOGICAL CASE, and the one this whole rule exists for. An
    unreachable remote, an expired credential and an API that answers with an
    empty list all produce the same local observation as a genuine deletion:
    nothing came back. Treating that as authority turns one auth failure into
    an emptied installation, reported as a successful sync.

    So absence is authority only from a remote this run PROVED it could read.
    A remote it could not reach is reported as unchecked, and every dependency
    from it is kept exactly where it was.

    Scenario: An unreachable remote removes nothing and says it could not check
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --include origin/demo"
      And I run "ctxloom deps pull"
      And the remote "origin" becomes unreachable
      When Alice pulls while the remote is unreachable:
        """
        ctxloom deps pull --format text
        """
      Then the output contains "could not be reached"
      And the output does not contain "no longer published"
      And the file ".ctxloom/lock.yaml" contains "//bundles/demo"
      When I run "ctxloom materialize dev --target out --backend claude-code"
      Then the file "out/CLAUDE.md" contains "Demo fragment content."

    # The lockfile surviving is not enough on its own: a pull that pruned the
    # CONTENT while leaving the pin behind would pass a lockfile-only
    # assertion and still have emptied what the agent receives. So the
    # offline listing is read back too — it is the surface a person would use
    # to find out what just happened to their installation.
    Scenario: A remote that cannot be reached leaves the listing intact
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --include origin/demo"
      And I run "ctxloom deps pull"
      And the remote "origin" becomes unreachable
      And I run "ctxloom deps pull"
      When Alice checks what survived:
        """
        ctxloom deps list
        """
      Then the command succeeds
      And the output contains "demo"

    # The lock rebuild after a pull keeps the previous entries of whatever part
    # of the closure it could not reach. That is safe only if it is SAID: a
    # summary with no such line reads as a complete lock.
    Scenario: A pull whose lock could not reach part of the closure names what it kept
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom remote create gone file:///nonexistent/nonexistent-ctxloom-remote --forge git"
      And I run "ctxloom profile create dev --include origin/demo"
      And the project already has the file ".ctxloom/content/bundles/v2/project/profiles/orphan.yaml":
        """
        parents:
          - file:///nonexistent/nonexistent-ctxloom-remote@bundles/kit#profiles/parent
        """
      When Alice pulls with part of the closure unreachable:
        """
        ctxloom deps pull --format text
        """
      Then the output contains "Lock incomplete"
      And the output contains "nonexistent-ctxloom-remote"
      And the output contains "previous lock entries were kept"

    # The same rule for the commands that report currency. A check that could
    # not fetch the remote has only the clone it fetched LAST time to read, and
    # an answer read from that clone is about the past: it is unchecked, never
    # "up to date".
    Scenario: A check that could not reach the remote does not call it up to date
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --include origin/demo"
      And I run "ctxloom deps pull"
      And the remote "origin" becomes unreachable
      When Alice checks for updates while the remote is unreachable:
        """
        ctxloom deps check --format text
        """
      Then the output does not contain "up to date"
      And the output contains "could not be checked"

    Scenario: An upgrade that could not reach the remote does not call everything up to date
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --include origin/demo"
      And I run "ctxloom deps pull"
      And the remote "origin" becomes unreachable
      When Alice upgrades while the remote is unreachable:
        """
        ctxloom deps upgrade --format text
        """
      Then the output does not contain "Everything is up to date"
      And the output contains "unreachable"

  Rule: A pull is the daemon's, and runs git as the command that asked would

    The daemon pulls, but the git it runs is the one on the PATH of the
    command that asked, with that command's HOME and credentials, and
    nothing of `GIT_*` (scrubbed, as ever). A daemon started by one terminal
    therefore never lends its environment to another's pull.

    Scenario: A pull runs the git on the asking command's PATH
      # new: step 10 U4, git runs in the caller's environment.
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --include origin/demo"
      And a ctxloom daemon is serving for this home
      And a git that records each call is first on the PATH of the next command alone
      When I run "ctxloom deps pull"
      Then the command succeeds
      And the recording git was called for the pull
      And the file ".ctxloom/lock.yaml" contains "//bundles/demo"

    Scenario: A pull records each tree it installs, and the next read finds it by its marker alone
      # new: step 10 L9, install facts; the cache is presence and identity, never content.
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --include origin/demo"
      When I run "ctxloom deps pull"
      And I run "ctxloom test library-events --format json"
      Then the output reports "events[verb=deps pull].type" as "ChangeIntended"
      And the output reports "events[verb=deps pull].steps[0].kind" as "create"
      And the output reports "events[verb=deps pull].steps[1].kind" as "replace"
      And the output reports "events[verb=deps pull].steps[1].path" as "lock.yaml"
      When I run "ctxloom deps list --format json"
      Then the command succeeds
      And the output contains "//bundles/demo"

  Rule: What lands in the lockfile is not what reaches the agent

    The pin is not the point: what actually lands in front of the agent is.
    Upgrading to changed upstream content must replace
    what the agent sees — not just what the lockfile records. And content is
    served by resolving the remote-tracking ref a fetch advanced, never by
    whatever the cached clone's checked-out working tree happens to hold.

    Scenario: An upstream content change reaches the assembled context once the pin advances
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --include origin/demo"
      And I run "ctxloom deps pull"
      And I run "ctxloom materialize dev --target before --backend claude-code"
      Then the file "before/CLAUDE.md" contains "Demo fragment content."
      When the remote "origin" changes fragment "demo-frag" to "MARKER-BRAVO-second-edition"
      And Alice advances the pin to the revised content:
        """
        ctxloom deps upgrade --yes
        """
      And I run "ctxloom materialize dev --target after --backend claude-code"
      Then the command succeeds
      And the file "after/CLAUDE.md" contains "MARKER-BRAVO-second-edition"
      And the file "after/CLAUDE.md" does not contain "Demo fragment content."

    # A clone's checked-out HEAD is not the source of truth for content — the
    # remote-tracking refs a fetch advances are. Forcing the local checkout
    # back to the very first commit must not resurrect old content.
    Scenario: A stale local checkout never leaks into what's served
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --include origin/demo"
      And I run "ctxloom deps pull"
      And the remote "origin" changes fragment "demo-frag" to "MARKER-STALE-CHECKOUT-current"
      And I run "ctxloom deps upgrade --yes"
      And the remote "origin"'s cached clone is forced back to its first commit
      When Alice materializes after the local checkout went stale:
        """
        ctxloom materialize dev --target out --backend claude-code
        """
      Then the command succeeds
      And the file "out/CLAUDE.md" contains "MARKER-STALE-CHECKOUT-current"
      And the file "out/CLAUDE.md" does not contain "Demo fragment content."
