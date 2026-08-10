@doc
Feature: remote — registering remote sources, and pulling shared content across the pin

  `ctxloom remote` owns two different things under one noun. The REGISTRY is
  local bookkeeping: which remotes exist, which one is the default — pure
  config, no network. CONTENT operations (`pull`, `upgrade`, `show`) reach a
  real git remote and move a project's lockfile, which pins every remote
  dependency to a resolved commit.

  A remote is just an address; its content still takes the review path. A
  remote is a Git repository (GitHub or generic git) publishing bundles. A
  local profile names a remote bundle by its per-remote short form
  ("<remote>/<bundle>" — a bare name is local), `remote pull` fetches and
  locks the dependency closure, and whether the pulled content ever reaches
  the agent is decided per item by `ctxloom review` (see review.feature),
  independent of the lockfile. To auto-trust a publisher's content, trust
  their signing key — never the URL.

  This is the comprehensive per-noun spec: what the noun DOES, leaf by leaf,
  hermetically against a seeded file:// repository. The narrative version —
  Bob cloning a fresh project and getting exactly what the team pinned, not
  whatever upstream happens to be serving that morning — is
  journeys/j000800_onboarding.feature, which asserts what a PERSON sees.

  # THE PIN AND THE PAYLOAD ARE TWO DIFFERENT QUESTIONS. These scenarios also
  # assert that changed upstream content actually reaches an assembled/
  # materialized context (not just the lockfile), that a stale local clone
  # checkout never leaches into what's served, and that "Skipped (already
  # installed)" — while accurate about the file being present — does not mean
  # the content is current; only `remote upgrade` (or a forced re-resolving
  # pull) moves an existing pin, and a hold survives even that.

  Rule: The registry is local bookkeeping — no fetch happens

    Registering, defaulting, and removing a remote only ever touch
    `.ctxloom/remotes.yaml`. None of the three names, let alone reaches, a
    bundle — that is what makes them safe to run with no network and no
    credential.

    Scenario: Registering a remote records it and it is listed back
      Given an initialized ctxloom project
      When Alice registers a remote:
        """
        ctxloom remote create origin file:///tmp/acceptance-remote.git --forge git
        """
      Then the command succeeds
      And the file ".ctxloom/remotes.yaml" contains "origin"
      When I run "ctxloom remote list"
      Then the command succeeds
      And the output contains "origin"

    Scenario: A remote can be made the default
      Given an initialized ctxloom project
      And I run "ctxloom remote create origin file:///tmp/acceptance-remote.git --forge git"
      When Alice sets the default remote:
        """
        ctxloom remote default origin
        """
      Then the command succeeds
      And the file ".ctxloom/remotes.yaml" contains "default: origin"

    # Bare `remove` is a preview: it must leave the remote registered. A guard
    # that quietly destroyed anyway would still pass a scenario that only
    # checked exit code — the follow-up `remote list` is what actually
    # catches that.
    Scenario: Bare remote remove reports and destroys nothing
      Given an initialized ctxloom project
      And I run "ctxloom remote create origin file:///tmp/acceptance-remote.git --forge git"
      When I run "ctxloom remote remove origin"
      Then the command succeeds
      And the output contains "Nothing was removed"
      And the output contains "--yes"
      When I run "ctxloom remote list"
      Then the output contains "origin"

    Scenario: Removing a remote takes it out of the registry
      Given an initialized ctxloom project
      And I run "ctxloom remote create origin file:///tmp/acceptance-remote.git --forge git"
      When Alice removes the remote:
        """
        ctxloom remote remove origin --yes
        """
      Then the command succeeds
      When I run "ctxloom remote list"
      Then the output does not contain "origin"

  Rule: A remote's catalog can be browsed without installing anything

    `remote show` and the equivalent MCP resource both read a remote's
    published bundles without touching any profile or lockfile — browsing is
    not pulling.

    Scenario: Browsing a remote lists the bundles it publishes
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      When Alice browses what a remote publishes:
        """
        ctxloom remote show origin
        """
      Then the command succeeds
      And the output contains "@bundles/demo"

    Scenario: A remote's catalog is also readable over MCP
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      When the agent reads resource "ctxloom://remotes/origin/contents"
      Then the resource contains "demo"

  Rule: Pull fetches exactly what is pinned, incrementally, and never overstates what happened

    `remote pull` records the pin straight into the active lock, trusted or
    not — the bundle's content is gated per item at exposure (`ctxloom
    review`), not by the lockfile. It is also incremental: an item whose lock
    entry already resolves from the clone cache is not re-fetched, and is
    reported as skipped rather than "already installed" — a phrase that reads
    as "you have the latest", which upstream having since moved makes false.

    Scenario: Referencing a remote bundle and pulling it locks the dependency closure
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --bundle origin/demo"
      When Alice fetches the content her profile draws on:
        """
        ctxloom remote pull
        """
      Then the command succeeds
      And the file ".ctxloom/lock.yaml" contains "@bundles/demo"

    Scenario: A second pull is incremental — an already-locked dependency is not re-fetched
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --bundle origin/demo"
      And I run "ctxloom remote pull"
      When Alice pulls again with nothing new pinned:
        """
        ctxloom remote pull
        """
      Then the command succeeds
      And the output contains "Skipped (kept at their locked commit)"

    # This pins BOTH halves: the old content actually stays (a plain pull
    # never moves an existing pin — only `remote upgrade` does), AND the
    # output says what is true rather than implying currency. Reporting
    # "Skipped (already installed)" here once cost an hour diagnosing a false
    # "stale content" bug, because that phrasing is indistinguishable from
    # "you have the latest".
    Scenario: A skipped pull leaves old content in place, and says so rather than implying it is current
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --bundle origin/demo"
      And I run "ctxloom remote pull"
      And I accept the pending item "demo#fragments/demo-frag" from remote "origin"
      And the remote "origin" changes fragment "demo-frag" to "MARKER-SKIPPED-PULL-never-seen"
      When Alice pulls again while upstream has moved on:
        """
        ctxloom remote pull
        """
      Then the command succeeds
      And the output contains "Skipped (kept at their locked commit)"
      And the output contains "may have upstream changes"
      And the output contains "ctxloom remote upgrade"
      And the output does not contain "already installed"
      When I run "ctxloom profile materialize dev --target out"
      Then the command succeeds
      And the file "out/CLAUDE.md" contains "Demo fragment content."
      And the file "out/CLAUDE.md" does not contain "MARKER-SKIPPED-PULL-never-seen"

  Rule: Upgrade advances pins; a hold freezes one, even against a forced re-resolve

    `remote upgrade` re-resolves each profile's dependency closure to the
    newest commit its version constraint allows and writes the advance
    straight to the active lock — no staging, no approval; changed untrusted
    content is withheld per item until accepted via `ctxloom review`.
    `bundle hold` is the opt-out: a held entry stays frozen against `upgrade`,
    and — this is the part easy to get wrong — against a `pull --force` too,
    because forcing a pull re-resolves the reference exactly like an upgrade
    would.

    Scenario: An upgrade advances the pin directly
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --bundle origin/demo"
      And I run "ctxloom remote pull"
      And the remote "origin" advances its bundle
      When Alice advances her pins to the newest commit:
        """
        ctxloom remote upgrade
        """
      Then the command succeeds
      And the output contains "Advanced"

    Scenario: A held dependency is not upgraded
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --bundle origin/demo"
      And I run "ctxloom remote pull"
      And I run "ctxloom bundle hold origin/demo"
      And the remote "origin" advances its bundle
      When Alice tries to advance a held pin:
        """
        ctxloom remote upgrade
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
      And I run "ctxloom remote pull"
      And I accept the pending item "demo#fragments/demo-frag" from remote "origin"
      And I run "ctxloom bundle hold origin/demo"
      And the remote "origin" advances its bundle
      When Alice forces a pull to re-resolve every reference:
        """
        ctxloom remote pull --force
        """
      Then the command succeeds
      And the file ".ctxloom/lock.yaml" contains "pinned: true"
      When I run "ctxloom profile materialize dev --target out"
      Then the file "out/CLAUDE.md" contains "Demo fragment content."
      And the file "out/CLAUDE.md" does not contain "version two"

  Rule: What lands in the lockfile is not what reaches the agent — only accepted content is served, and only from the current ref

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
      And I run "ctxloom remote pull"
      And I accept the pending item "demo#fragments/demo-frag" from remote "origin"
      And I run "ctxloom profile materialize dev --target before"
      Then the file "before/CLAUDE.md" contains "Demo fragment content."
      When the remote "origin" changes fragment "demo-frag" to "MARKER-BRAVO-second-edition"
      And Alice advances the pin to the revised content:
        """
        ctxloom remote upgrade
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
      And I run "ctxloom remote pull"
      And I accept the pending item "demo#fragments/demo-frag" from remote "origin"
      And the remote "origin" changes fragment "demo-frag" to "MARKER-STALE-CHECKOUT-current"
      And I run "ctxloom remote upgrade"
      And I accept the pending item "demo#fragments/demo-frag" from remote "origin"
      And the remote "origin"'s cached clone is forced back to its first commit
      When Alice materializes after the local checkout went stale:
        """
        ctxloom profile materialize dev --target out
        """
      Then the command succeeds
      And the file "out/CLAUDE.md" contains "MARKER-STALE-CHECKOUT-current"
      And the file "out/CLAUDE.md" does not contain "Demo fragment content."
