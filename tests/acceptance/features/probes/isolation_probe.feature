@live
Feature: Isolation probe — live proof against real vendor engines

  j002200_isolation.feature's own matrix proves ctxloom's SIDE of isolation — the right
  env var, pointed at the right scratch dir, seeded with the right bytes — against
  a cooperative recording spy, never a real engine binary. That is fast, hermetic,
  and correctly scoped for a regression test. It cannot prove the other half: that
  a real vendor engine actually reads the variable it was handed, and actually
  writes only where the boundary says it may.

  This feature is that other half, built to be run on its own — for ONE engine and
  ONE axis at a time — because its job is not "pass once in this repo's CI" but
  "answer the same question again, unattended, every time an engine
  ships a new version." `just isolation-probe ENGINE AXIS` runs a single row; how
  to read a failure (vendor regression vs ctxloom regression — they read
  differently) is below, and tests/acceptance/isolation_probe.go's package doc
  covers the two live-observation
  problems this feature solves (the worktree scratch and the container's own
  writable layer both disappear the instant the run ends, so both must be caught
  DURING the run, not after).

  ONE LIVE TURN PER CELL. Every scenario below makes AT MOST one real, paid engine
  call — no retries on failure (a flaky live call is evidence, not noise to retry
  away) — because this runs against real subscriptions/API keys on every engine
  release, and volume against a subscription's own limits is the one real residual
  cost of running it at all.

  READING A FAILURE: if the response never arrives (assertion a fails), the
  credential or the engine itself is the suspect — check auth first, this is not
  an isolation bug. If the response arrives but assertions b/c/d fail, isolation
  itself is the suspect: a write landed somewhere the boundary should have stopped
  it, or ctxloom's own bookkeeping (the config-home var, the container mount plan)
  didn't do what it claims. A row's leak assertion failing IS a bug (either a
  vendor regression or a ctxloom one — the scenario's own Then step names which
  half it is asserting).

  Background:
    Given Alice has a git-backed project

  # The primary sweep: every engine this repo drives, both axes, using whichever
  # credential path is ambient (env API key, or a host credential file) — exactly
  # ctxloom's own resolveEnvOrMountAuth precedence, so a cell can never claim to
  # have proven a path it did not actually take. Self-skips LOUDLY, per cell, with
  # the specific missing opt-in, credential AND axis named — see isolation_probe.go's
  # probeTargetAuth.
  # Each Examples block below carries its own @<engine> @<axis> tag pair —
  # not decoration, the addressing mechanism: `just isolation-probe <engine>
  # <axis>` sets ACCEPTANCE_TAGS="@live && @<engine> && @<axis>" to run
  # exactly this one cell, the single-row invocation a per-engine-release
  # regression check needs. Ten separate one-row Examples blocks (rather than
  # one ten-row table) is the standard Gherkin shape for per-row tagging —
  # tags attach to an Examples: block, not to an individual row within one.
  Scenario Outline: The isolation probe proves credentials and isolation hold for <engine> under the <axis> axis
    Given the isolation probe targets "<engine>" under the "<axis>" axis
    When the probe runs it live, writing a unique token in one turn
    Then the probe's core guarantees hold for "<engine>" under the "<axis>" axis

    @claude-code @worktree
    Examples:
      | engine      | axis     |
      | claude-code | worktree |

    # container-rootless is the only container ownership this probe (or any
    # box it has ever run on) has been able to reach — see
    # isolation_probe.go's probeAxis doc and task unwatched-discharge, which
    # retired the undifferentiated "container" spelling this row used to
    # write (`runtime: container`, no longer in config-schema.json's enum).
    @claude-code @container-rootless
    Examples:
      | engine      | axis               |
      | claude-code | container-rootless |

    # container-rootful: WIRED, UNVERIFIED. No box this suite has run on has
    # had a reachable rootful docker daemon (this one runs rootless 29.3.0)
    # or podman invoked rootful, so this row has never gone green — it
    # self-skips loudly via probeContainerRuntimeForAxis until run on a
    # runner that has one.
    @claude-code @container-rootful
    Examples:
      | engine      | axis              |
      | claude-code | container-rootful |







  # Auth-path duality: the primary sweep above reports which path it took, but a
  # dev box with subscription credentials on disk will always land on "seeded"
  # for a credentialed engine, never exercising the ENV-KEY BYPASS path — the
  # path a credentialed CI lane (secrets only, no host credential file) actually
  # takes. These four rows FORCE that path and self-skip loudly when the engine's
  # own API-key env var is not set, rather than silently falling back to the
  # seeded path and reporting a false pass.
  Scenario Outline: The isolation probe proves the API-key bypass path for <engine> under the worktree axis
    Given the isolation probe targets "<engine>" under the "worktree" axis using its API key credential
    When the probe runs it live, writing a unique token in one turn
    Then the probe's core guarantees hold for "<engine>" under the "worktree" axis

    @claude-code @bypass
    Examples:
      | engine      |
      | claude-code |



  # Back to: tests/acceptance/features/journeys/j002200_isolation.feature (the hermetic layer
  # this feature complements).
