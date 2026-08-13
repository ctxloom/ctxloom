@live
Feature: Engine × isolation matrix — the simplest round trip, through every engine and every isolation scheme

  Before anyone can trust what ctxloom does with an engine, one question has to
  be answerable for every engine and every isolation scheme we ship: does a run
  come back with the answer at all? Not "did the flag parse", not "did the
  container launch", not "was the credential isolated" — did the engine we
  launched, in the sandbox we put it in, with the context we composed, return
  the thing it was asked for.

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

  CONTAINER CELLS ARE EXPECTED TO BE RED UNTIL CONTAINER AUTH KEYING LANDS.
  Agent containerization has never been demonstrably correct here — the
  container-auth lane sat red for fifteen days — and the fix is in flight on its
  own branch. These rows exist now, and red, ON PURPOSE: the map of which cells
  fail and how is the evidence that work is measured against. Do not tag them
  green-by-loosening, and do not delete them because they fail.

  ADDRESSING ONE CELL. Every Examples block carries its engine, its runtime axis
  and its workspace axis as tags — the idiom isolation_probe.feature
  established — so `just engine-matrix <engine> <runtime> <workspace>` runs
  exactly one cell. That is the unit of work here: these are real, paid engine
  calls, a container cell can be minutes long, and running the matrix in
  parallel on a loaded box is how a machine gets OOM-killed mid-measurement.

  # Each cell self-skips LOUDLY, naming the engine and BOTH axes, when its
  # engine is missing or unauthenticated, when that specific axis cannot
  # authenticate it (kiro's container axis, for instance, can only be
  # authenticated by KIRO_API_KEY — the host's subscription login does not
  # satisfy it), or when no container runtime is reachable. A blank cell in the
  # matrix always has a reason attached.
  Scenario Outline: A <engine> run under runtime <runtime> and workspace <workspace> returns exactly the JSON it was asked for
    Given the engine matrix targets "<engine>" under runtime "<runtime>" and workspace "<workspace>"
    When it runs the JSON hello-world task in one turn
    Then the run's output is exactly the expected JSON object

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

    @codex @host @ws-none
    Examples:
      | engine | runtime | workspace |
      | codex  | host    | none      |

    @codex @host @ws-worktree
    Examples:
      | engine | runtime | workspace |
      | codex  | host    | worktree  |

    @codex @container @ws-none
    Examples:
      | engine | runtime   | workspace |
      | codex  | container | none      |

    @codex @container @ws-worktree
    Examples:
      | engine | runtime   | workspace |
      | codex  | container | worktree  |

    @kiro @host @ws-none
    Examples:
      | engine | runtime | workspace |
      | kiro   | host    | none      |

    @kiro @host @ws-worktree
    Examples:
      | engine | runtime | workspace |
      | kiro   | host    | worktree  |

    @kiro @container @ws-none
    Examples:
      | engine | runtime   | workspace |
      | kiro   | container | none      |

    @kiro @container @ws-worktree
    Examples:
      | engine | runtime   | workspace |
      | kiro   | container | worktree  |

    @opencode @host @ws-none
    Examples:
      | engine   | runtime | workspace |
      | opencode | host    | none      |

    @opencode @host @ws-worktree
    Examples:
      | engine   | runtime | workspace |
      | opencode | host    | worktree  |

    @opencode @container @ws-none
    Examples:
      | engine   | runtime   | workspace |
      | opencode | container | none      |

    @opencode @container @ws-worktree
    Examples:
      | engine   | runtime   | workspace |
      | opencode | container | worktree  |
