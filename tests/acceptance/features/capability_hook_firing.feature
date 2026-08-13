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
  it appends the harp it was handed ON ITS OWN ARGV to a stamp file at an
  absolute path outside the engine's working directory. The assertion reads that
  file's BYTES. An engine cannot fake this by being clever with the settings
  file it was given — the harp sits in that file in plain text and any engine
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

  HOST/NONE ONLY, AND THE REASON IS PHYSICAL. The stamp file is an absolute HOST
  path. A containerized engine would write it inside another filesystem
  namespace, and this assertion — reading the host path — would report a
  hook-firing failure that is really a mount gap. A container row needs a
  mounted stamp path designed first; the step refuses the axis loudly rather
  than running a cell that would lie.

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

    # The one cell that runs BOTH stages: codex ingests its session-start hook's
    # stdout as context by declaration, so this row additionally proves that the
    # harp the hook printed — written to no file the engine reads and present in
    # no prompt — came back in the turn.
    @codex @host @ws-none @probe-p3-hook-firing
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
