@live
Feature: P3 — hooks actually FIRE, proven by the hook's own stamp file

  ctxloom writes hooks into three engines' native surfaces: claude's
  .claude/settings.json, codex's $CODEX_HOME/config.toml [hooks], kiro's
  .kiro/agents/<name>.json. That ctxloom writes the right BYTES is well proven —
  golden tests, the settings-io tests, and
  TestDeliveryApproach_HookCarriageMatchesDeclaration in tests/integration all
  check carriage, and carriage is our own behaviour.

  Whether a vendor binary then READS that file and EXECS the command in it has
  never been proven anywhere in this repo, hermetically or live. That is the one
  step in the chain we do not control, and it is the entire subject of this
  feature. Carriage without firing is a hook that exists only in a file nobody
  ran — success-shaped, silent, and exactly the failure this project produces
  most often.

  THE PROOF IS A FILE, NOT A SENTENCE. The hook ctxloom delivers does one thing:
  it appends the harp it was handed ON ITS OWN ARGV to a stamp file beside the
  script itself, resolved from the script's own location rather than from a cwd
  nobody promised us. The assertion reads that file's BYTES. An engine cannot
  fake this by being clever with the settings file it was given — the harp sits in that file in plain text and any engine
  could quote it back — because only an engine that actually EXECUTED the
  command can make the stamp file exist at all. Quotable versus executable is
  the whole probe.

  Nothing here can flake on prose habits. There are no fences to strip, no
  preamble to tolerate, no output contract to honour. That is deliberate: the
  design's own counter-argument notes that a strict text assertion measures
  engine and prompt jointly, so where a claim admits a non-prose observable, the
  probe takes it.

  AN EMPTY STAMP FILE IS NOT A PASS, AND A MISSING ONE IS NOT A SKIP. Three
  outcomes are named separately because they are three different findings: the
  file is absent (the hook never ran), the file exists and is empty (the hook
  ran and wrote nothing — this project's characteristic silent no-op), the file
  carries bytes that are not this cell's harp (something ran, but not this hook
  with this argument). A bare existence check would blur all three.

  TWO STAGES, AND ONLY CODEX HAS BOTH. Stage (a) — firing — runs on every cell.
  Stage (b) asks whether the engine INGESTS what the hook printed, and it is
  asserted only where production itself makes that claim: codexApproaches lists
  agent.ApproachHook FIRST for agent.SurfaceContext, so the hook is codex's
  DEFAULT context route. claude declares ApproachHook too, but claude's
  SurfaceFor resolves that pair to noopContextDelivery — the documented no-op
  that never carries — so asserting an echo on a claude cell would red it for
  failing to do something ctxloom never asked. kiro reads steering files
  instead. Stage (b) uses a SECOND minted harp, planted only in the hook's
  standard output, so the two stages cannot satisfy each other.

  WHAT IS ABSENT HERE IS DECLARED, NOT FORGOTTEN. opencode has no Examples row
  because opencode declares hooks gone at the mechanism level (noHooksReason:
  "opencode has no hook mechanism") — a gate by ABSENCE, recorded in the probe
  registry, where the completeness test refuses an undeclared gap. codex's
  session_end kind is absent for the same reason at finer grain
  (unsupportedHookKinds[bundles.HookEventSessionEnd] / codex.NoSessionEndReason,
  "codex has no session-end event"), which is precisely why this probe plants on
  session_start: it is the one kind all three engines carry.

  THE CONTAINER AXIS, AND THE REASON IT WAITED. This probe was host/none only,
  and the reason was physical rather than cautious: the stamp file was an
  absolute HOST path, so a containerized engine would have written it inside
  another filesystem namespace and this assertion — reading the host path —
  would have reported a hook-firing failure that was really a mount gap. That is
  now fixed AT THE FIXTURE, which is why it needed no mount and no production
  change: the script resolves its stamp from `dirname "$0"`, so the proof lands
  beside the script wherever the workspace was mounted or checked out, and the
  workspace is the one thing a container cell bind-mounts. The stamp is then read
  from the directory the engine ACTUALLY RAN IN, which for a worktree cell is a
  per-agent checkout rather than the project.

  Moving the fixture inside the workspace does mean an agent can now READ the
  stamp script. That costs nothing: the verdict demands the file EXIST carrying
  this cell's argv harp, and reading a path does not create a file. The codex row
  below is the measured proof — it found its harp by grepping the fixture's own
  script, answered correctly, and red anyway.

  ADDRESSING ONE CELL. Every Examples block carries its engine and both axes as
  tags, so an ACCEPTANCE_TAGS expression of the live tag, this probe's tag, the
  engine tag, the runtime tag and the workspace tag — joined with && — runs
  exactly one cell. (Written out in prose rather than shown literally: a
  description line that BEGINS with an at-sign is parsed as a tag line, and a
  tag containing whitespace aborts the parse of this whole file, taking every
  scenario in it. That is not hypothetical; it happened while writing this
  paragraph.) These are real paid turns; run them one at a time.

  Scenario Outline: A <engine> run execs the session_start hook ctxloom delivered
    Given the hook-firing probe targets "<engine>" under runtime "<runtime>" and workspace "<workspace>"
    When it runs one turn with that hook installed
    Then the hook's stamp file carries the harp it was given on its argv

    @claude-code @host @ws-none @probe-p3-hook-firing
    Examples:
      | engine      | runtime | workspace |
      | claude-code | host    | none      |

    # The container cells, added once the stamp stopped being a host-absolute
    # path. A PAIR on purpose: P6's host/worktree cell failed where both-off and
    # both-on passed, so a container row without its worktree partner rebuilds
    # that blind spot. Stage (a) only, as on claude's host row — claude's
    # SurfaceFor resolves ApproachHook to noopContextDelivery, so ctxloom never
    # delivers claude's context through a hook and this cell must not assert an
    # echo production never asked for.
    # @wip — RED, and it is a CAPABILITY FINDING measured 2026-08-26: a
    # containerized claude run does not produce the stamp. exit 0, the turn
    # answered normally, no stamp on the bind-mounted workspace.
    #
    # CONTROLLED AGAINST THE OBVIOUS HARNESS CAUSE: re-run with the hook command
    # written as a HOST-ABSOLUTE path (valid in-container under the identity
    # mapper) instead of the workspace-relative one — it did not fire either, so
    # the relative path is not the cause. The identical fixture fires reliably on
    # host/none the same day.
    #
    # NOT YET ISOLATED between "ctxloom never wrote the hook into the container"
    # and "claude never ran one it was given": the carriage scan reads the
    # project tree and the session root ON THE HOST, and a container's settings
    # are written where neither looks, so carriage is unobservable here rather
    # than absent. Isolating it needs a scan inside the container.
    @claude-code @container-rootless @ws-none @probe-p3-hook-firing @wip
    Examples:
      | engine      | runtime            | workspace |
      | claude-code | container-rootless | none      |

    # The worktree partner. The hook script reaches the per-agent checkout only
    # because the fixture is COMMITTED, and the stamp is written there rather
    # than in the project — probeCellRunDir resolves that checkout after the run.
    # @wip — RED, and it fails EARLIER than its partner above (2026-08-26):
    # probeCellRunDir found ZERO per-agent worktrees after the run, so there was
    # no checkout left to read a stamp from. That is a compound of two things —
    # the hook did not fire, so the checkout stayed clean, and a clean checkout
    # is pruned by the WIP-safe teardown before the assertion runs. The refusal
    # is deliberate: falling back to the project directory would let a cell that
    # never got its checkout pass on the host fixture's evidence.
    @claude-code @container-rootless @ws-worktree @probe-p3-hook-firing @wip
    Examples:
      | engine      | runtime            | workspace |
      | claude-code | container-rootless | worktree  |

    # @wip — RED, and it is a CAPABILITY FINDING: codex hooks written by ctxloom
    # NEVER FIRE. This is the row that made building this probe worth it.
    #
    # This would otherwise be the one cell running BOTH stages: codex ingests
    # its session-start hook's stdout as context by declaration, so it would
    # additionally prove the harp the hook printed came back in the turn.
    #
    # WHAT WAS MEASURED, 2026-08-13:
    #
    #   1. Carriage is FINE. With the binding declaring `config_home: project`,
    #      the hook command was observed live in
    #      <project>/.ctxloom/state/<harp>/home/.codex/config.toml while the
    #      engine was running. ctxloom wrote what it claims to write.
    #   2. Firing does not happen. No stamp file, on any attempt.
    #   3. The cause is the VENDOR's, isolated with ctxloom out of the picture:
    #      a hand-built CODEX_HOME carrying the identical [[hooks.SessionStart]]
    #      block was run twice against codex 0.144.4, same config, same prompt —
    #
    #        codex exec                                  → hook did NOT run
    #        codex exec --dangerously-bypass-hook-trust  → hook RAN (stamp written)
    #
    #      codex gates hooks behind a PERSISTED HOOK TRUST that ctxloom neither
    #      satisfies nor bypasses. codex says nothing when it declines, so the
    #      whole mechanism is silently inert.
    #
    # AND THIS ROW IS WHY THE ASSERTION IS A FILE. On the 2026-08-13 run codex
    # answered with the CORRECT stage-(b) harp — while its hook had never fired.
    # Its own stderr shows how: it ran `rg` across the temp tree and the session
    # store until it found the phrase sitting in the fixture's hook script. A
    # probe that asserted only the echo would have reported GREEN on a hook
    # mechanism that is completely dead. This cell reds because stage (a) is a
    # file on disk and is judged FIRST.
    #
    # WHY THIS MATTERS WELL BEYOND THIS FEATURE: the hook is codex's DEFAULT
    # CONTEXT ROUTE (codexApproaches lists ApproachHook first for
    # SurfaceContext). If hooks never fire, ctxloom's hook-carried context
    # delivery to codex is inert too, and any codex row that looks green for
    # context is arriving through the compositional AGENTS.md route instead.
    #
    # DO NOT loosen this cell, and do not delete it because it fails. Untag when
    # ctxloom satisfies codex's hook trust; the cell is how you will know.
    @codex @host @ws-none @probe-p3-hook-firing @wip
    Examples:
      | engine | runtime | workspace |
      | codex  | host    | none      |

    # kiro renames the event rather than lacking it: KiroWriter.mapHooks routes
    # unified session_start onto kiro's own agentSpawn hook. Stage (a) only —
    # kiro takes context from steering files, so hook-output ingestion is not a
    # claim it makes.
    @kiro @host @ws-none @probe-p3-hook-firing
    Examples:
      | engine | runtime | workspace |
      | kiro   | host    | none      |
