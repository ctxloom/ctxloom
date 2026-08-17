@live
Feature: Claude × runtime × workspace matrix — the simplest round trip, on every axis pairing

  Before anyone can trust what ctxloom does with an engine, one question has to
  be answerable for every axis pairing we ship: does a run come back with the
  answer at all? Not "did the flag parse", not "did the container launch", not
  "was the credential isolated" — did the engine we launched, in the sandbox we
  put it in, with the context we composed, return the thing it was asked for.

  CLAUDE IS THE ENGINE THIS FEATURE DRIVES NOW. The matrix is built on two axes
  that apply to any engine ctxloom ships, and claude is the one currently wired
  active on all four combinations of them. Codex, kiro and opencode are
  designed on the identical two axes — their Examples blocks are further down
  this file, each now tagged work-in-progress, carrying every comment and
  measured finding they already had, untouched — but they are not the engine
  under active test here. Wire one back in the same way claude is wired above
  them: drop the work-in-progress tag once it becomes the engine under active
  test again.

  THE TWO AXES, NAMED PLAINLY, because the table below crosses them and a
  reader should not have to already know which is which to read it. `runtime`
  (host | container) is the CONTAINERIZATION axis — it isolates the PROCESS the
  engine runs in. `workspace` (none | worktree) is the ISOLATION axis — it
  isolates the FILES the engine sees. They are INDEPENDENT: a container run can
  still mount the workspace at the exact absolute path the live project lives
  at, and a worktree run still executes the engine on the host, outside any
  container — picking one says nothing about the other. Both column names stay
  `runtime` and `workspace` throughout this suite: they are the product's own
  spelling, matching config.yaml and the CLI flags.

  Nothing else in this suite answers that. j002300_cross_engine_delegation.feature
  proves DELEGATION, and only on the host axis with workspace none.
  isolation_probe.feature proves the ISOLATION GUARANTEES — what a run wrote and
  where, and whether a credential leaked — and deliberately asserts nothing
  about what the engine said. j002200_isolation.feature and
  j002400_container.feature prove the axis MACHINERY against the mock, with no
  vendor engine involved at all. This feature is the floor underneath all three,
  and it is written to be run cell by cell, unattended, whenever an engine or an
  isolation path changes.

  THE TASK IS THE SIMPLEST ONE THAT CAN STILL FAIL HONESTLY. Each cell plants a
  random nonce in the agent's OWN composed profile context, then asks for one
  JSON object carrying it — JSON only, no preamble, no postamble, no code
  fences. The nonce is random per run, so no memorised "hello world" answer can
  pass; it lives in the CONTEXT rather than the prompt, so a cell also proves
  ctxloom's context delivery survived that isolation scheme.

  THE ASSERTION IS STRICT ON PURPOSE. stdout (whitespace-trimmed, nothing else
  stripped) must parse as JSON and equal {"hello": "<the nonce>"} exactly. An
  engine that wraps its answer in ```json fences, or opens with "Here is your
  JSON:", goes RED. That red is a FINDING about that engine in that cell — the
  precise thing this matrix exists to produce — and it must never be absorbed by
  loosening the matcher. Anything that has to be tolerated gets its own tagged
  Examples block and its own measured evidence, where a reader can see it.
  The failure message names WHICH shape it found: a run that failed outright, a
  silent no-op (exit 0, empty stdout), an output-FORMAT failure, a
  CONTEXT-DELIVERY failure (well-formed JSON, no nonce), or a wrong shape.

  EACH CELL USES PRODUCTION'S OWN CREDENTIAL MECHANISM, NOT A HARNESS
  SUBSTITUTE. We drive real engines on real subscriptions, and every axis
  already has a solved mechanism: the host axis uses the engine's real home;
  the worktree axis seeds per-agent homes through credentialSeedSpecs; the
  container axis mounts the host credential store (claude's read-write, so a
  refresh lands in the live chain — merge 07072acf). Those mechanisms all
  resolve from the real host home, so these cells deliberately do NOT isolate
  it, and seed nothing themselves: a cell that substituted its own credential
  delivery would prove nothing about the product, and a cell more cautious than
  the product is not a test of it. What this floor asserts on stays isolated —
  a fresh temp project holds the fixture, and the assertion reads only stdout.

  THE CONTAINER CELLS WENT GREEN ON 2026-08-13, AND THAT IS WHAT THEY WERE FOR.
  Agent containerization had never been demonstrably correct here — the
  container-auth lane sat red for fifteen days — so these rows were added red ON
  PURPOSE, as the map the fix would be measured against. Container auth keying
  then landed, and all eight cells were run serially: claude-code, codex and
  opencode passed on BOTH container axes against real engines, credentials
  arriving through the real-home read-write mount; kiro's two gated loudly on
  its own limitation (its subscription credential is a global sqlite no HomeVar
  relocates, so KIRO_API_KEY is the only key that opens the axis — a real
  product limitation, recorded as one).

  CLAUDE'S FULL FOUR-CELL FLOOR WAS RE-VERIFIED ON 2026-08-16, RIGHT AFTER THIS
  MATRIX NARROWED TO CLAUDE-ONLY ACTIVE ROWS. All four claude cells were run on
  the subscription lane and all four PASSED: host/none and host/worktree each
  ~5.5s, container/none 57s (including the image build), container/worktree
  8s. Narrowing the matrix to claude did not just restructure the table — it
  re-measured every cell claude carries.

  The cells stay exactly as strict as they were. A red here is now a
  REGRESSION rather than an expectation, which is the whole point of having run
  them while they failed. Do not tag them green-by-loosening, and do not delete
  them because they fail.

  ADDRESSING ONE CELL. Every Examples block carries its engine, its runtime axis
  and its workspace axis as tags — the idiom isolation_probe.feature
  established — so `just engine-matrix <engine> <runtime> <workspace>` runs
  exactly one cell. That is the unit of work here: these are real, paid engine
  calls, a container cell can be minutes long, and running the matrix in
  parallel on a loaded box is how a machine gets OOM-killed mid-measurement.

  # Each cell self-skips LOUDLY, naming the engine and BOTH axes, and every
  # remaining skip names something PRODUCTION cannot do — never something this
  # harness declined to arrange. The live example is kiro's worktree and
  # container axes: credentialSeedSpecs["kiro"] marks XDG_DATA_HOME
  # GatedOnCreds with HonoursVarForCreds FALSE, because kiro's subscription
  # credential is a GLOBAL sqlite that no HomeVar relocates — so ctxloom
  # refuses to start rather than silently hand the agent a fresh, logged-out
  # data home, and KIRO_API_KEY is the only key that opens those two axes.
  # That is a real product limitation, recorded here as one.
  # A blank cell in the matrix always has a reason attached.
  Scenario Outline: A <engine> run under runtime <runtime> and workspace <workspace> returns exactly the JSON it was asked for
    Given the engine matrix targets "<engine>" under runtime "<runtime>" and workspace "<workspace>"
    When it runs the JSON hello-world task in one turn
    Then the run's output is exactly the expected JSON object

    # ACTIVE FLOOR — claude only. VERIFIED LIVE 2026-08-16 on the subscription
    # lane: all four cells below run serially, all four PASSED — host/none
    # ~5.5s, host/worktree ~5.5s, container/none 57s (including the image
    # build), container/worktree 8s. See the header for the fuller account.
    @claude-code @host @ws-none
    Examples:
      | engine      | runtime | workspace |
      | claude-code | host    | none      |

    @claude-code @host @ws-worktree
    Examples:
      | engine      | runtime | workspace |
      | claude-code | host    | worktree  |

    @claude-code @container @ws-none
    Examples:
      | engine      | runtime   | workspace |
      | claude-code | container | none      |

    @claude-code @container @ws-worktree
    Examples:
      | engine      | runtime   | workspace |
      | claude-code | container | worktree  |

    # ============================================================
    # OTHER ENGINES — SAME TWO AXES (runtime × workspace), NOT YET DRIVEN.
    #
    # Everything below runs on the identical grid claude uses above. Each
    # block is tagged @wip rather than deleted or commented out: the rows stay
    # PARSED, stay ADDRESSABLE by `just engine-matrix <engine> <runtime>
    # <workspace>`, and stay VISIBLE in the corpus, and every measured finding
    # recorded beside them — notably kiro's host/none row a few blocks down —
    # survives verbatim. Wire an engine back into the ACTIVE FLOOR above the
    # same way claude is wired: drop its @wip tag when it becomes the engine
    # under active test again.
    # ============================================================

    # @wip — not a defect: codex is designed on the same two axes claude
    # drives above, but it is not the engine under active test right now.
    # Untag when codex becomes a driven engine again.
    @codex @host @ws-none @wip
    Examples:
      | engine | runtime | workspace |
      | codex  | host    | none      |

    # @wip — not a defect: codex is designed on the same two axes claude
    # drives above, but it is not the engine under active test right now.
    # Untag when codex becomes a driven engine again.
    @codex @host @ws-worktree @wip
    Examples:
      | engine | runtime | workspace |
      | codex  | host    | worktree  |

    # @wip — not a defect: codex is designed on the same two axes claude
    # drives above, but it is not the engine under active test right now.
    # Untag when codex becomes a driven engine again.
    @codex @container @ws-none @wip
    Examples:
      | engine | runtime   | workspace |
      | codex  | container | none      |

    # @wip — not a defect: codex is designed on the same two axes claude
    # drives above, but it is not the engine under active test right now.
    # Untag when codex becomes a driven engine again.
    @codex @container @ws-worktree @wip
    Examples:
      | engine | runtime   | workspace |
      | codex  | container | worktree  |

    # @wip — RED, and the finding is NOT about the model. Measured 2026-08-13:
    # kiro produced the requested object byte-perfectly, and ctxloom handed it
    # back on stdout wrapped in TERMINAL DECORATION — an ANSI colour sequence
    # and an interactive prompt marker:
    #
    #   \x1b[38;5;141m> \x1b[0m{"hello":"CTXLOOM-HELLO-a1eeaec503ddef0c"}\x1b[0m
    #
    # reported as: OUTPUT-FORMAT failure — stdout is not a bare JSON object
    # (invalid character '\x1b' looking for beginning of value).
    #
    # So a one-shot kiro run's stdout is not machine-readable: any caller
    # piping `ctxloom run --one-shot` into a JSON parser gets a syntax error,
    # while the engine did everything right. That is a ctxloom-side channel
    # defect (the interactive `> ` echo leaking into a non-interactive
    # capture), not an engine one, and it is exactly the class of thing this
    # floor exists to surface.
    #
    # DO NOT fix this by stripping ANSI in the assertion. The contract under
    # test is "stdout IS the JSON"; a matcher that launders the stream would
    # report success to this suite while every real consumer still breaks.
    # Untag when a one-shot kiro run's stdout is the bare object.
    @kiro @host @ws-none @wip
    Examples:
      | engine | runtime | workspace |
      | kiro   | host    | none      |

    # @wip — not a defect: kiro is designed on the same two axes claude drives
    # above, but it is not the engine under active test right now. Untag when
    # kiro becomes a driven engine again. (Separate from the host/none row
    # above, which is @wip for its own measured ANSI-decoration finding and
    # keeps that reason regardless of this one.)
    @kiro @host @ws-worktree @wip
    Examples:
      | engine | runtime | workspace |
      | kiro   | host    | worktree  |

    # @wip — not a defect: kiro is designed on the same two axes claude drives
    # above, but it is not the engine under active test right now. Untag when
    # kiro becomes a driven engine again. (Production's own conditional gate
    # on this axis — credentialSeedSpecs["kiro"] needs KIRO_API_KEY — still
    # applies underneath this tag; see the header comment above the outline.)
    @kiro @container @ws-none @wip
    Examples:
      | engine | runtime   | workspace |
      | kiro   | container | none      |

    # @wip — not a defect: kiro is designed on the same two axes claude drives
    # above, but it is not the engine under active test right now. Untag when
    # kiro becomes a driven engine again. (Production's own conditional gate
    # on this axis — credentialSeedSpecs["kiro"] needs KIRO_API_KEY — still
    # applies underneath this tag; see the header comment above the outline.)
    @kiro @container @ws-worktree @wip
    Examples:
      | engine | runtime   | workspace |
      | kiro   | container | worktree  |

    # @wip — not a defect: opencode is designed on the same two axes claude
    # drives above, but it is not the engine under active test right now.
    # Untag when opencode becomes a driven engine again.
    @opencode @host @ws-none @wip
    Examples:
      | engine   | runtime | workspace |
      | opencode | host    | none      |

    # GREEN, but FLAKY — recorded rather than smoothed over. Measured
    # 2026-08-13: two consecutive attempts, the first FAILED and the second
    # passed in 100s, same fixture, same box. The failing attempt's stderr:
    #
    #   ctxloom: warning: run channel down (reconnecting): rpc error: code =
    #     Unavailable ... dial tcp 127.0.0.1:1: connect: connection refused
    #   ctxloom: warning: runner dial-home failed (reconnecting; the
    #     coordinator synthesizes loss meanwhile): coord: open RunnerChannel:
    #     ... dial tcp 127.0.0.1:1: connect: connection refused
    #
    # 127.0.0.1:1 is not a real endpoint, so the runner was handed a
    # placeholder reach-back address rather than a live one — the same family
    # as the standup-death silence fixed at 2725325e, and worth chasing on
    # that evidence. This cell is left untagged (it does pass) with the
    # flakiness documented here: if it goes red in a lane, check for that dial
    # address before assuming the engine.
    #
    # ADDENDUM 2026-08-16: this cell is now ALSO tagged @wip, for an
    # unrelated, second reason — opencode is not the engine under active test
    # right now (same as every other row in this section). The "left
    # untagged" sentence above describes the 2026-08-13 decision and is kept
    # verbatim as history; it no longer describes this cell's current tag
    # state. Untag when opencode becomes a driven engine again — that is
    # independent of the flakiness finding above, which remains open on its
    # own and does not itself block untagging.
    @opencode @host @ws-worktree @wip
    Examples:
      | engine   | runtime | workspace |
      | opencode | host    | worktree  |

    # @wip — not a defect: opencode is designed on the same two axes claude
    # drives above, but it is not the engine under active test right now.
    # Untag when opencode becomes a driven engine again.
    @opencode @container @ws-none @wip
    Examples:
      | engine   | runtime   | workspace |
      | opencode | container | none      |

    # @wip — not a defect: opencode is designed on the same two axes claude
    # drives above, but it is not the engine under active test right now.
    # Untag when opencode becomes a driven engine again.
    @opencode @container @ws-worktree @wip
    Examples:
      | engine   | runtime   | workspace |
      | opencode | container | worktree  |
