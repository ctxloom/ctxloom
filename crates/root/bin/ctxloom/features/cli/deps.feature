# world: surfaces
# Ported from cli/deps.feature (rev f36e8dfb2)
# Ported from journeys/j001400_bundle_distribution.feature (rev f36e8dfb2)
# NOT ported: nothing.
# Split (owner-ruled as daemon.feature's was; Windows CI timed this one file out
#   at 360 s, its time spread evenly over its Rules): its other Rules are in
#   cli/deps_upstream, deps_pins and deps_trees.feature, their text unchanged.

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
  `upgrade` moves them — and it is the only thing that does: `pull` (forced or
  not) creates first pins and keeps every existing one, even when a profile
  changed its constraint. `upgrade` shows each move first and applies it only
  with --yes.

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

    # Tabled by format: off a terminal (which this harness always is) the
    # no-flag row gets the JSON ListDependenciesResponse (OD-40: its empty
    # "dependencies" left out, `{}`), not the "No dependencies installed."
    # text line. Reuses the branch's shared empty-collection step.
    Scenario Outline: A project with nothing installed reports an empty closure
      Given an initialized ctxloom project
      When Alice asks what she has installed:
        """
        ctxloom <flags> deps list
        """
      Then the command succeeds
      And the output reports "dependencies" as empty, saying "No dependencies installed"

      @json-default
      Examples: no --format at all takes the derived default off a terminal; an explicit one wins in both directions
        | flags         |
        |               |
        | --format json |
        | --format text |

    # NOT tabled, pinned to --format text: this remedy line
    # ("run 'ctxloom deps pull' to install it") is written straight to
    # renderDepsList's text-only branch — ListDependenciesResponse carries
    # only {dependencies}, no hint field — so a json row has nothing to
    # assert here.
    Scenario: A project with nothing installed says what to run next
      Given an initialized ctxloom project
      When Alice asks what she has installed:
        """
        ctxloom --format text deps list
        """
      Then the command succeeds
      And the output contains "ctxloom deps pull"

    Scenario: The listing names each dependency, its commit and its origin
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --include origin/demo"
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
    # Tabled by format: same reasoning as the Outline above — bare `deps`
    # aliases to `deps list`.
    Scenario Outline: Bare deps lists the closure
      Given an initialized ctxloom project
      When I run "ctxloom <flags> deps"
      Then the command succeeds
      And the output reports "dependencies" as empty, saying "No dependencies installed"
      And the output does not contain "Available Commands:"

      @json-default
      Examples: no --format at all takes the derived default off a terminal; an explicit one wins in both directions
        | flags         |
        |               |
        | --format json |
        | --format text |

    # A hold changes what `upgrade` is allowed to do, so a listing that could
    # not show it would be a listing you cannot plan from.
    Scenario: A held dependency is marked held in the listing
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --include origin/demo"
      And I run "ctxloom deps pull"
      And I run "ctxloom deps hold origin/demo"
      When Alice reviews what is frozen:
        """
        ctxloom deps list
        """
      Then the command succeeds
      And the output contains "held"

  Rule: Pull installs exactly what is pinned, and never overstates what happened

    `deps pull` records the pin straight into the active lock. It is also
    incremental: an item whose lock entry
    already resolves from the clone cache is not re-fetched, and is reported
    as kept at its locked commit rather than "already installed" — a phrase
    that reads as "you have the latest", which upstream having since moved
    makes false.

    Scenario: Referencing a remote bundle and pulling it locks the dependency closure
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --include origin/demo"
      When Alice installs the content her profile draws on:
        """
        ctxloom deps pull
        """
      Then the command succeeds
      And the file ".ctxloom/lock.yaml" contains "//bundles/demo"

    # Registering a remote is the one act that admits a repository's content.
    # An address alone admits nothing: the pull is refused, it says how to
    # register the repository, and nothing is registered or pinned behind the
    # user's back.
    Scenario: Pulling straight from an unregistered address is refused
      Given an initialized ctxloom project
      And the profile "dev" draws on a bundle straight from an unregistered git repository
      When Alice installs the content her profile draws on:
        """
        ctxloom deps pull
        """
      Then the command fails
      And the output contains "remote not registered"
      And the output contains "ctxloom remote create <name> file://"
      And the file ".ctxloom/lock.yaml" does not exist
      When I run "ctxloom remote list --format json"
      Then the output reports no "remotes[name$=/remotes/remote]"

    # A profile shipped in a repository may name only that repository's
    # content: registering one remote must never admit another repository its
    # profiles happen to name.
    Scenario: A remote profile reaching into another repository is refused
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And a git remote "vendor" serving a bundle "kit" whose profile "reach" draws on bundle "demo" of the remote "origin"
      And I run "ctxloom profile create dev --include vendor/kit"
      And I run "ctxloom deps pull"
      When Alice looks at the vendor's profile:
        """
        ctxloom profile show vendor/kit#profiles/reach
        """
      Then the command fails
      And the output contains "a remote profile may refer only to bundles in its own repository"
      And the output contains "//bundles/demo"

    # Composing several repositories is what a profile of the user's own is
    # for: each repository it names was registered, so each is admitted.
    Scenario: A local profile draws on several registered remotes
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And a git remote "vendor" serving a ctxloom bundle
      And I run "ctxloom profile create dev --include origin/demo --include vendor/demo"
      When Alice installs the content her profile draws on:
        """
        ctxloom deps pull
        """
      Then the command succeeds
      And the file ".ctxloom/lock.yaml" contains "//bundles/demo" exactly 2 times

    Scenario: A second pull is incremental — an already-locked dependency is not re-fetched
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --include origin/demo"
      And I run "ctxloom deps pull"
      When Alice pulls again with nothing new pinned:
        """
        ctxloom deps pull --format text
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
      And I run "ctxloom profile create dev --include origin/demo"
      And I run "ctxloom deps pull"
      And the remote "origin" changes fragment "demo-frag" to "MARKER-SKIPPED-PULL-never-seen"
      When Alice pulls again while upstream has moved on:
        """
        ctxloom deps pull --format text
        """
      Then the command succeeds
      And the output contains "Skipped (kept at their locked commit)"
      And the output contains "ctxloom deps check"
      And the output does not contain "ctxloom deps upgrade"
      And the output does not contain "already installed"
      When I run "ctxloom materialize dev --target out --backend claude-code"
      Then the command succeeds
      And the file "out/CLAUDE.md" contains "Demo fragment content."
      And the file "out/CLAUDE.md" does not contain "MARKER-SKIPPED-PULL-never-seen"

  Rule: Pull writes the project's engine files only with --yes

    After a pull installs remote content, a project refresh brings the
    project directory's engine files up to date: what an engine's launch
    carries itself (claude's hooks, MCP servers, settings, commands and skills,
    owner ruling 2026-11-12) is given back, and the rest re-delivered. That directory is shared by every
    session and person using the checkout, so the refresh asks the same
    consent `ctxloom materialize` asks before writing there: `--yes`. Without
    it the PULL STILL SUCCEEDS. It installs and locks the closure, writes no
    engine file, and names what the refresh would have changed and the two
    ways to apply it. The note compares against the project as it stands: a
    project already current gets no note, and a kind the refresh would be
    refused is named with its fix.

    Scenario: A pull without --yes installs the closure and leaves the project's engine files alone
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --include origin/demo"
      And I run "ctxloom materialize --yes"
      When Alice pulls without consenting to the project refresh:
        """
        ctxloom deps pull --format text
        """
      Then the command succeeds
      And the file ".ctxloom/lock.yaml" contains "//bundles/demo"
      And the file ".claude/settings.json" contains "SessionStart"
      And the output contains "Project refresh skipped"
      And the output contains "ctxloom deps pull --yes"
      And the output contains "ctxloom materialize --yes"

    Scenario: A pull right after a --yes pull has no refresh to skip
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --include origin/demo"
      And Alice has already let a pull refresh the project:
        """
        ctxloom deps pull --yes --format text
        """
      When Alice pulls again without consenting to the project refresh:
        """
        ctxloom deps pull --format text
        """
      Then the command succeeds
      And the output does not contain "Project refresh skipped"

    Scenario: A refresh the project would refuse is named with its fix, and the pull still succeeds
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --include origin/demo"
      And I run "ctxloom materialize --yes"
      And the project already has the file ".claude/settings.json":
        """
        { this is not json
        """
      When Alice pulls again without consenting to the project refresh:
        """
        ctxloom deps pull --format text
        """
      Then the command succeeds
      And the output contains "Project refresh skipped"
      And the output contains "hooks refused"
      And the output contains "fix: "

    Scenario: A pull with --yes also refreshes the project's engine files
      # new: owner ruling 2026-11-12, the hooks ride the launch, so the refresh gives back the copy an earlier materialize wrote.
      Given an initialized ctxloom project
      And a git remote "origin" serving a ctxloom bundle
      And I run "ctxloom remote default origin"
      And I run "ctxloom profile create dev --include origin/demo"
      And I run "ctxloom materialize --yes"
      When Alice pulls and lets the refresh write the project:
        """
        ctxloom deps pull --yes --format text
        """
      Then the command succeeds
      And the file ".claude/settings.json" does not contain "SessionStart"
      And the output does not contain "Project refresh skipped"
