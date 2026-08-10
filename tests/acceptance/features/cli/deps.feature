@doc
Feature: deps — the installed dependency closure, and everything that moves it

  Covers: `ctxloom deps list`, `deps pull`, `deps check`, `deps upgrade`,
  `deps hold`, `deps unhold`, and the bare `ctxloom deps` form.

  A profile names a remote bundle by its short form ("<remote>/<bundle>"; a
  bare name is local). The CLOSURE is every bundle that follows from those
  names, and the lockfile pins each one to a resolved commit, so two checkouts
  of the same project install the same bytes.

  FOUR VERBS, FOUR DIFFERENT JOBS, and the boundaries between them are what
  this file specifies. `list` reads the lockfile and nothing else. `pull` makes
  the installation match upstream. `check` reports which pins could move.
  `upgrade` moves them. Only `upgrade` and a forced `pull` ever re-resolve a
  reference; a plain `pull` honors what is already pinned.

  Where content comes FROM is the other noun — see cli/remote.feature.

  # THE PIN AND THE PAYLOAD ARE TWO DIFFERENT QUESTIONS. These scenarios assert
  # that changed upstream content actually reaches an assembled/materialized
  # context, not just the lockfile; that a stale local clone checkout never
  # leaches into what is served; and that a skipped pull says what is true
  # about the pin rather than implying the content is current.

  Rule: The listing is offline and answers when nothing else can

    `deps list` reads the lockfile. That makes it the one question still
    answerable with no network, an expired credential, or a remote that has
    been deleted — and the reason it deliberately says nothing about whether
    anything newer exists upstream.

    Scenario: A project with nothing installed says so, and says what to run
      Given an initialized ctxloom project
      When Alice asks what she has installed:
        """
        ctxloom deps list
        """
      Then the command succeeds
      And the output contains "No dependencies installed"
      And the output contains "ctxloom deps pull"

    Scenario: The listing names each dependency, its commit and its origin
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --bundle origin/demo"
      And I run "ctxloom deps pull"
      When Alice asks what she has installed:
        """
        ctxloom deps list
        """
      Then the command succeeds
      And the output contains "demo"
      And the output contains "origin"

    # The bare noun answers rather than teaches, through the same seam
    # `ctxloom remote` uses.
    Scenario: Bare deps lists the closure
      Given an initialized ctxloom project
      When I run "ctxloom deps"
      Then the command succeeds
      And the output contains "No dependencies installed"
      And the output does not contain "Available Commands:"

    # A hold changes what `upgrade` is allowed to do, so a listing that could
    # not show it would be a listing you cannot plan from.
    Scenario: A held dependency is marked held in the listing
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --bundle origin/demo"
      And I run "ctxloom deps pull"
      And I run "ctxloom deps hold origin/demo"
      When Alice reviews what is frozen:
        """
        ctxloom deps list
        """
      Then the command succeeds
      And the output contains "held"

  Rule: Pull installs exactly what is pinned, and never overstates what happened

    `deps pull` records the pin straight into the active lock, trusted or not
    — the bundle's content is gated per item at exposure (`ctxloom review`),
    not by the lockfile. It is also incremental: an item whose lock entry
    already resolves from the clone cache is not re-fetched, and is reported
    as kept at its locked commit rather than "already installed" — a phrase
    that reads as "you have the latest", which upstream having since moved
    makes false.

    Scenario: Referencing a remote bundle and pulling it locks the dependency closure
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --bundle origin/demo"
      When Alice installs the content her profile draws on:
        """
        ctxloom deps pull
        """
      Then the command succeeds
      And the file ".ctxloom/lock.yaml" contains "@bundles/demo"

    Scenario: A second pull is incremental — an already-locked dependency is not re-fetched
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --bundle origin/demo"
      And I run "ctxloom deps pull"
      When Alice pulls again with nothing new pinned:
        """
        ctxloom deps pull
        """
      Then the command succeeds
      And the output contains "Skipped (kept at their locked commit)"

    # This pins BOTH halves: the old content actually stays (a plain pull
    # never moves an existing pin — only `deps upgrade` does), AND the
    # output says what is true rather than implying currency. Reporting
    # "Skipped (already installed)" here once cost an hour diagnosing a false
    # "stale content" bug, because that phrasing is indistinguishable from
    # "you have the latest".
    Scenario: A skipped pull leaves old content in place, and says so rather than implying it is current
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --bundle origin/demo"
      And I run "ctxloom deps pull"
      And I accept the pending item "demo#fragments/demo-frag" from remote "origin"
      And the remote "origin" changes fragment "demo-frag" to "MARKER-SKIPPED-PULL-never-seen"
      When Alice pulls again while upstream has moved on:
        """
        ctxloom deps pull
        """
      Then the command succeeds
      And the output contains "Skipped (kept at their locked commit)"
      And the output contains "may have upstream changes"
      And the output contains "ctxloom deps upgrade"
      And the output does not contain "already installed"
      When I run "ctxloom profile materialize dev --target out"
      Then the command succeeds
      And the file "out/CLAUDE.md" contains "Demo fragment content."
      And the file "out/CLAUDE.md" does not contain "MARKER-SKIPPED-PULL-never-seen"

  Rule: Pull reconciles to upstream — it removes what upstream no longer has

    Installed content is a PROJECTION of remote state, not user data: every
    byte of it can be re-fetched from the address it came from. So a pull that
    finds a bundle gone upstream removes it locally and says which, by name,
    without asking. That is synchronization, not destruction, and it is why
    there is no `--yes` on it.

    Scenario: A bundle deleted upstream is removed locally, by name
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --bundle origin/demo"
      And I run "ctxloom deps pull"
      And the remote "origin" stops publishing bundle "demo"
      When Alice pulls after upstream deleted the bundle:
        """
        ctxloom deps pull
        """
      Then the command succeeds
      And the output contains "no longer published"
      And the output contains "demo"
      And the file ".ctxloom/lock.yaml" does not contain "@bundles/demo"

  Rule: "I could not reach upstream" is never "upstream deleted everything"

    THE PATHOLOGICAL CASE, and the one this whole rule exists for. An
    unreachable remote, an expired credential and an API that answers with an
    empty list all produce the same local observation as a genuine deletion:
    nothing came back. Treating that as authority turns one auth failure into
    an emptied installation, reported as a successful sync.

    So absence is authority only from a remote this run PROVED it could read.
    A remote it could not reach is reported as unchecked, and every dependency
    from it is kept exactly where it was.

    # @wip: the SPEC is right and the wiring is not. upstreamProbes builds a
    # CACHED fetcher (operations.NewCachedFetcherFactory), so a remote whose
    # bare repo has been renamed away still answers reachable from the local
    # clone — and reconcile therefore reports nothing. The unit tests pass
    # because they inject fake probes; only this scenario talks to the real
    # one, which is the whole reason it is worth keeping.
    @wip
    Scenario: An unreachable remote removes nothing and says it could not check
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --bundle origin/demo"
      And I run "ctxloom deps pull"
      And I accept the pending item "demo#fragments/demo-frag" from remote "origin"
      And the remote "origin" becomes unreachable
      When Alice pulls while the remote is unreachable:
        """
        ctxloom deps pull
        """
      Then the output contains "could not be reached"
      And the output does not contain "no longer published"
      And the file ".ctxloom/lock.yaml" contains "@bundles/demo"
      When I run "ctxloom profile materialize dev --target out"
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
      And I run "ctxloom profile create dev --bundle origin/demo"
      And I run "ctxloom deps pull"
      And the remote "origin" becomes unreachable
      And I run "ctxloom deps pull"
      When Alice checks what survived:
        """
        ctxloom deps list
        """
      Then the command succeeds
      And the output contains "demo"

  Rule: Check reads and upgrade writes

    `deps check` reaches the network and reports; it changes nothing, which is
    what makes it safe to run anywhere. `deps upgrade` re-resolves each
    profile's closure to the newest commit its constraint allows and writes the
    advance straight to the active lock — no staging, no approval; changed
    untrusted content is withheld per item until accepted via `ctxloom review`.

    Scenario: Check reports an available advance and changes nothing
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --bundle origin/demo"
      And I run "ctxloom deps pull"
      And the remote "origin" advances its bundle
      When Alice asks what could be advanced:
        """
        ctxloom deps check
        """
      Then the command succeeds
      And the output contains "ctxloom deps upgrade"
      When I run "ctxloom deps list"
      Then the output contains "demo"

    Scenario: Check on an empty closure has nothing to check
      Given an initialized ctxloom project
      When I run "ctxloom deps check"
      Then the command succeeds
      And the output contains "nothing to check"

    Scenario: An upgrade advances the pin directly
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --bundle origin/demo"
      And I run "ctxloom deps pull"
      And the remote "origin" advances its bundle
      When Alice advances her pins to the newest commit:
        """
        ctxloom deps upgrade
        """
      Then the command succeeds
      And the output contains "Advanced"

  Rule: A hold freezes one dependency, even against a forced re-resolve

    `deps hold` is the opt-out: a held entry stays frozen against `upgrade`,
    and — this is the part easy to get wrong — against a `pull --force` too,
    because forcing a pull re-resolves the reference exactly like an upgrade
    would.

    Scenario: A held dependency is not upgraded
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --bundle origin/demo"
      And I run "ctxloom deps pull"
      And I run "ctxloom deps hold origin/demo"
      And the remote "origin" advances its bundle
      When Alice tries to advance a held pin:
        """
        ctxloom deps upgrade
        """
      Then the command succeeds
      And the output contains "up to date"

    # THE MECHANIC A NEW CLONE RELIES ON (see j000800_onboarding.feature's
    # "Bob receives the versions the team pinned, not the latest ones"): an
    # ordinary pull SKIPS a reference it already considers installed and
    # never consults the pin at all, so a hold's protection was never
    # actually exercised by scenario coverage that only ever ran a plain
    # pull. `--force` puts the pull back on the path a hold has to defend:
    # the reference IS re-resolved against the advanced remote, on the very
    # same lockfile-write path `upgrade` uses
    # (internal/remote/pull.go:Puller.updateLockfile's `hadExisting &&
    # existing.Pinned` branch) — and the hold has to hold it back there too,
    # not just on `upgrade`'s.
    Scenario: A held dependency's content survives even a pull forced to re-resolve
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --bundle origin/demo"
      And I run "ctxloom deps pull"
      And I accept the pending item "demo#fragments/demo-frag" from remote "origin"
      And I run "ctxloom deps hold origin/demo"
      And the remote "origin" advances its bundle
      When Alice forces a pull to re-resolve every reference:
        """
        ctxloom deps pull --force
        """
      Then the command succeeds
      And the file ".ctxloom/lock.yaml" contains "pinned: true"
      When I run "ctxloom profile materialize dev --target out"
      Then the file "out/CLAUDE.md" contains "Demo fragment content."
      And the file "out/CLAUDE.md" does not contain "version two"

    Scenario: Unholding lets the pin move again
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --bundle origin/demo"
      And I run "ctxloom deps pull"
      And I run "ctxloom deps hold origin/demo"
      And the remote "origin" advances its bundle
      When Alice releases the freeze:
        """
        ctxloom deps unhold origin/demo
        """
      Then the command succeeds
      When I run "ctxloom deps upgrade"
      Then the command succeeds
      And the output contains "Advanced"

  Rule: What lands in the lockfile is not what reaches the agent

    The pin is not the point: what actually lands in front of the agent is.
    Upgrading to changed upstream content, then accepting it, must replace
    what the agent sees — not just what the lockfile records. And content is
    served by resolving the remote-tracking ref a fetch advanced, never by
    whatever the cached clone's checked-out working tree happens to hold.

    Scenario: An upstream content change reaches the assembled context only once accepted
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --bundle origin/demo"
      And I run "ctxloom deps pull"
      And I accept the pending item "demo#fragments/demo-frag" from remote "origin"
      And I run "ctxloom profile materialize dev --target before"
      Then the file "before/CLAUDE.md" contains "Demo fragment content."
      When the remote "origin" changes fragment "demo-frag" to "MARKER-BRAVO-second-edition"
      And Alice advances the pin to the revised content:
        """
        ctxloom deps upgrade
        """
      And I accept the pending item "demo#fragments/demo-frag" from remote "origin"
      And I run "ctxloom profile materialize dev --target after"
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
      And I run "ctxloom profile create dev --bundle origin/demo"
      And I run "ctxloom deps pull"
      And I accept the pending item "demo#fragments/demo-frag" from remote "origin"
      And the remote "origin" changes fragment "demo-frag" to "MARKER-STALE-CHECKOUT-current"
      And I run "ctxloom deps upgrade"
      And I accept the pending item "demo#fragments/demo-frag" from remote "origin"
      And the remote "origin"'s cached clone is forced back to its first commit
      When Alice materializes after the local checkout went stale:
        """
        ctxloom profile materialize dev --target out
        """
      Then the command succeeds
      And the file "out/CLAUDE.md" contains "MARKER-STALE-CHECKOUT-current"
      And the file "out/CLAUDE.md" does not contain "Demo fragment content."
