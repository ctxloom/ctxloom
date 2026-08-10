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
  "answer the same question again, unattended, every time claude-code / codex /
  kiro / opencode ships a new version." See
  website/src/content/docs/security/isolation.md's "The executable probe" section
  for how to run a single row and how to read a failure (vendor regression vs
  ctxloom regression — they read differently, see below), and
  tests/acceptance/isolation_probe.go's package doc for the two live-observation
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
  didn't do what it claims. kiro's row below asserts a KNOWN leak
  positively — that is expected RED-if-fixed, not a bug; every other row's leak
  assertion failing IS a bug (either a vendor regression or a ctxloom one — the
  scenario's own Then step names which half it is asserting).

  Background:
    Given Alice has a git-backed project

  # The primary sweep: every engine this repo drives, both axes, using whichever
  # credential path is ambient (env API key, or a host credential file) — exactly
  # ctxloom's own resolveEnvOrMountAuth precedence, so a cell can never claim to
  # have proven a path it did not actually take. Self-skips LOUDLY, per cell, with
  # the specific missing credential AND axis named — see isolation_probe.go's
  # probeWorktreeAuthAvailable / probeContainerAuthAvailable for kiro, the one
  # engine whose skip reason is a documented product gap rather than
  # a missing credential.
  # Each Examples block below carries its own @<engine> @<axis> tag pair —
  # not decoration, the addressing mechanism: `just isolation-probe <engine>
  # <axis>` sets ACCEPTANCE_TAGS="@live && @<engine> && @<axis>" to run
  # exactly this one cell, the single-row invocation a per-engine-release
  # regression check needs. Ten separate one-row Examples blocks (rather than
  # one ten-row table) is the standard Gherkin shape for per-row tagging —
  # tags attach to an Examples: block, not to an individual row within one.
  # See website/src/content/docs/security/isolation.md.
  Scenario Outline: The isolation probe proves credentials and isolation hold for <engine> under the <axis> axis
    Given the isolation probe targets "<engine>" under the "<axis>" axis
    When the probe runs it live, writing a unique token in one turn
    Then the probe's core guarantees hold for "<engine>" under the "<axis>" axis

    @claude-code @worktree
    Examples:
      | engine      | axis     |
      | claude-code | worktree |

    @claude-code @container
    Examples:
      | engine      | axis      |
      | claude-code | container |

    @codex @worktree
    Examples:
      | engine | axis     |
      | codex  | worktree |

    @codex @container
    Examples:
      | engine | axis      |
      | codex  | container |

    @kiro @worktree
    Examples:
      | engine | axis     |
      | kiro   | worktree |

    @kiro @container
    Examples:
      | engine | axis      |
      | kiro   | container |

    @opencode @worktree
    Examples:
      | engine   | axis     |
      | opencode | worktree |

    @opencode @container
    Examples:
      | engine   | axis      |
      | opencode | container |

  # Auth-path duality: the primary sweep above reports which path it took, but a
  # dev box with subscription credentials on disk will always land on "seeded"
  # for claude/codex/opencode, never exercising the ENV-KEY BYPASS path — the
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

    @codex @bypass
    Examples:
      | engine |
      | codex  |

    @opencode @bypass
    Examples:
      | engine   |
      | opencode |

  # kiro's credential-store leak (legal-hula: KIRO_HOME isolates config, but
  # subscription auth lives in a GLOBAL sqlite KIRO_HOME never touches) is no
  # longer reachable via a bare worktree run — ctxloom now REFUSES to start kiro
  # in worktree isolation without KIRO_API_KEY specifically BECAUSE of this leak
  # (credentialSeedFixIt), which is the right behavior but also closes off the one
  # window this probe would otherwise use to prove the leak live. --degraded is
  # the documented escape hatch for exactly this refusal, so this ONE scenario
  # deliberately passes it, to keep the leak provable rather than merely asserted
  # in prose. If this ever goes RED because the run reports NO leak, that is
  # GOOD NEWS (kiro's credential store became genuinely KIRO_HOME-scoped) and the
  # fix is to widen HonoursVarForCreds in auth.go and retire this scenario, not to
  # "fix" the assertion.
  @kiro @kiro-leak
  Scenario: The isolation probe proves kiro's global credential store leak under --degraded
    Given the isolation probe targets kiro's known credential-store leak
    When the probe runs it live under --degraded, writing a unique token in one turn
    Then the probe confirms kiro's global credential store was touched, as expected

  # Back to: tests/acceptance/features/j002200_isolation.feature (the hermetic layer
  # this feature complements) · website/src/content/docs/security/isolation.md
  # (the narrative account of what these engines actually do).
