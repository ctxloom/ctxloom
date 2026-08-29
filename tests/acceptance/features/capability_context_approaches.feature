@live @probe-p1-approach-sweep
Feature: Context-approach sweep — the same task, delivered by each mechanism the engine declares

  ctxloom can hand an agent its context four different ways, and until this
  feature existed it had only ever been observed doing it ONE way per engine:
  whichever way that engine defaults to. `agent.ApproachSystemPrompt` (claude's
  --append-system-prompt-file) and `agent.ApproachHook` (the SessionStart
  inject-context route) were both claimed by the descriptors and proven by
  nobody — rows 4 and 5 of the capability inventory, "C, ?" in both columns.

  This is that gap closed. Each cell runs the JSON hello-world task from
  engine_isolation_matrix.feature — byte-identical prompt, byte-identical
  bundle, a nonce planted in the agent's own composed context — with ONE line
  added to the agent binding:

    surfaces:
      context: <approach>

  so the run pins the delivery mechanism instead of taking the engine's own
  default. Everything else is held constant. A red here is therefore a finding
  about that MECHANISM on that engine, and about nothing else.

  WHAT THESE CELLS CANNOT SEPARATE, LEARNED THE HARD WAY. A nonce-echo cell asks
  a model a question, and a model's answer is downstream of EVERY channel at
  once — so it can never, by itself, attribute one. This probe recorded a false
  finding on exactly that mistake (a green codex hook cell credited to a hook
  that ctxloom's own stderr said had never been written; see the retraction where
  that Examples block used to be), and S4's hook-firing probe found the same
  class from the other side by catching codex answering a nonce probe after
  searching the workspace with rg.

  Two corrections came out of it, and both are load-bearing here:

    - the verdict now reads ctxloom's REPORT, not only the model's answer:
      approachPinHonoured refuses a run that degraded off the pin, and
      approachRequiredSurfaceDelivered refuses a hook-pinned cell whose hook
      surface production said it did not write;
    - each cell's registry row states whether it is SIDE-CHANNEL-CONTROLLED —
      whether the nonce bytes were out of reach of a file search. Only claude's
      system-prompt cell is: its delivery writes out of cwd, and its negative
      control is the hook cell beside it, which sees the identical project tree
      with the identical tools and comes back with nothing. Every other context
      delivery in the ladder lands IN the working directory (no engine but claude
      has an out-of-cwd realization), so those cells prove the bytes reached the
      workspace and the model produced them — not which route the model read.

  WHICH APPROACHES EXIST IS THE ENGINE'S ANSWER, NOT OURS. The cells below are
  exactly the (engine, approach) pairs the engines' own `ApproachTable`s
  declare: claude-code carries all three for its context surface, codex carries
  hook and unsafe-file, and kiro and opencode carry unsafe-file alone. A pair an
  engine does not declare has NO Examples row here — a scenario for a capability
  the engine says it does not have would skip forever and read as coverage — and
  the absence is written down in the probe registry
  (capability_probe_registry.go) as gated-out with the declaring symbol, so it
  can never read as forgotten. The Given step re-asks production the same
  question through operations.ResolveAgentSurfaces before spending a turn, so
  the registry and the engine cannot quietly drift apart.

  THE DEFAULT IS ALREADY COVERED, AND THAT IS WHY THESE CELLS LOOK REDUNDANT
  AND ARE NOT. claude's default at a shared-cwd launch is the system-prompt
  scratch file and codex's is the hook, so two of the five cells below pin what
  their engine would have chosen anyway. They still earn their turn: what P0
  measured was the engine's default, and a default can move — the pin is what
  makes "this mechanism works" a claim that survives ctxloom changing its mind
  about preference (see approach.go's own note that no single table order serves
  every caller).

  THE FALSE GREEN THIS FEATURE IS BUILT AROUND. A preference ctxloom cannot
  honour is a WARNING, not a failure: the run degrades to the engine's default
  delivery and carries on. That is the right product behaviour and it is lethal
  to a naive probe — the nonce still arrives, the JSON is still perfect, the
  cell still exits 0, and it has proved nothing at all about the approach in its
  own name. So the verdict reads stderr for production's degrade markers BEFORE
  it reads stdout, and a degraded run is a CONTEXT-DELIVERY failure: the context
  came through a channel this cell did not select.

  WHAT THESE CELLS STILL CANNOT SEE. Absence of the degrade warning proves the
  pin survived to the backend's surface selection. It is not a positive sighting
  of which writer ran — no ctxloom surface reports the resolved per-surface
  approach (`run --dry-run` shows the assembled context and the target file, not
  the mechanism), and the hook route's own artifact, the
  .ctxloom/cache/context/<hash>.md file the SessionStart hook reads, is removed
  at teardown by BaseContextProvider.Clear before an assertion could read it.
  That is stated here rather than smoothed over, and it is recorded as this
  probe's deferred work.

  THE ASSERTION IS P0'S, UNCHANGED AND UNLOOSENED. stdout, whitespace-trimmed
  and nothing else stripped, must parse as JSON and equal {"hello": "<the
  nonce>"} exactly. Fences, a preamble or terminal decoration are RED, and the
  red is the finding. Anything that must be tolerated gets its own tagged
  Examples block and its own measured evidence — never a weaker matcher.

  ADDRESSING ONE CELL. Every Examples block carries the probe, the engine, both
  isolation axes and its VARIANT as tags, so
  `ACCEPTANCE_TAGS="@live && @probe-p1-approach-sweep && @claude-code && @host
  && @ws-none && @var-hook"` selects exactly one cell. The variant tag is not
  decoration: two of these cells share an engine and both axes and differ only
  by the mechanism, so without it they are not separately addressable — and the
  minted-harp ledger keys on it too, which is what stops them from being handed
  the same nonce.

  Scenario Outline: A <engine> run under runtime <runtime> and workspace <workspace>, with context pinned to <approach>, returns exactly the JSON it was asked for
    Given the approach sweep targets "<engine>" under runtime "<runtime>" and workspace "<workspace>", pinning context delivery to "<approach>" for cell variant "<variant>"
    When it runs the JSON hello-world task in one turn under the pinned approach
    Then the run's output is exactly the expected JSON object, delivered by the pinned approach

    # claude's system-prompt route: the framed context goes to an out-of-cwd
    # scratch file consumed via --append-system-prompt-file. The only engine
    # that has it (agent.ApproachSystemPrompt is claude-only), and the approach
    # that touches least of the user's environment — no project file, no process
    # of ours, nothing to race.
    #
    # THE LADDER'S ONE SIDE-CHANNEL-CONTROLLED CONTEXT CELL. The delivery puts
    # no nonce bytes in the workspace, and the fixture bytes that ARE in the tree
    # are ruled out by the hook cell below: same tree, same tools, same prompt,
    # no nonce in the answer. An engine reading the fixture off disk would have
    # passed both.
    @claude-code @host @ws-none @var-system-prompt
    Examples:
      | engine      | runtime | workspace | approach      | variant       |
      | claude-code | host    | none      | system-prompt | system-prompt |

    # THE CONTAINER CELLS, and they ask a question no other probe answers.
    # P0 proves a nonce planted in DEFAULT composed context survives both axes.
    # These ask whether the PINNED system-prompt route does — and that is not
    # the same claim, because this delivery writes an out-of-cwd scratch file
    # (appendFlagDelivery, consumed via --append-system-prompt-file) rather than
    # a file in the tree the container already mounts. If the scratch file is
    # written host-side, a container cell CANNOT see it, and the cell reds as a
    # CONTEXT-DELIVERY failure. That red would be a real product finding about
    # containerized context delivery, not a fixture fault — which is why these
    # rows are worth their turns either way.
    #
    # BOTH workspace values, for the reason P6 measured the hard way: its
    # host/worktree cell failed where both-off and both-on passed. The defect
    # lives where exactly ONE boundary is on, so a container row without its
    # worktree partner rebuilds the blind spot it was added to remove.
    @claude-code @container-rootless @ws-none @var-system-prompt
    Examples:
      | engine      | runtime            | workspace | approach      | variant       |
      | claude-code | container-rootless | none      | system-prompt | system-prompt |

    @claude-code @container-rootless @ws-worktree @var-system-prompt
    Examples:
      | engine      | runtime            | workspace | approach      | variant       |
      | claude-code | container-rootless | worktree  | system-prompt | system-prompt |

    # FIXED 2026-08-16 (was RED, MEASURED 2026-08-13, AND THE FINDING WAS A
    # PRODUCT ONE). claude's hook route should carry context through a
    # SessionStart inject-context hook ctxloom writes into .claude/settings.json
    # and the vendor binary then executes. Three consecutive pre-fix runs,
    # three freshly minted harps, one shape — exit 0, well-formed JSON, no
    # trace of the nonce:
    #
    #   harp "vast-racy-pound"  -> {"hello":"2467643947"}
    #   harp "near-green-parka" -> {"hello":"bumpy-stony-sixth"}
    #   harp "free-rich-jet"    -> {"hello":"mere-teal-jet"}
    #
    # reported as: CONTEXT-DELIVERY failure — stdout is well-formed JSON but
    # carries nothing of the nonce.
    #
    # It was not a degraded pin (no degrade marker on stderr —
    # approachPinHonoured is the check that separates those and it passed),
    # and not an empty assembly (the same stderr says "context: 2 fragment(s),
    # ~336 tokens"). The mechanism was selected and the context was composed;
    # the model never saw it. Every other declared approach on both engines
    # went green on this same fixture within the hour.
    #
    # ROOT CAUSE: agent.LaunchBackend.deliverSet's SharedCell delivery loop
    # only installed the SessionStart injection hook (recoverContextViaHook)
    # when a surface's write returned a non-nil error. claude's ApproachHook
    # context surface (claude.noopContextDelivery) "succeeds" with a nil error
    # and a nil handle BY DESIGN — it is a documented no-op write on the
    # premise that the settings-carried SessionStart hook itself carries the
    # context. Nothing actually installed that hook on the success path, so a
    # run pinned to context delivery "hook" launched with zero context while
    # Setup reported success. FIX: deliverSet now also installs the hook
    # (agent.LaunchBackend.installContextInjectionHook, factored out of
    # recoverContextViaHook) whenever a SharedCell resolves SurfaceContext at
    # ApproachHook, whether or not the surface write itself errored.
    #
    # A CORRECTION TO THE ORIGINAL READING ABOVE, folded in 2026-08-16 after a
    # re-run against the still-broken code measured a fourth shape:
    # {"hello":"prone-wide-deity"} for nonce harp "pale-young-getup". That
    # string is not any nonce minted in that run and appears nowhere ctxloom
    # emitted it — the model INVENTED a harp-shaped value unprompted, it did
    # not need to read one from its ambient environment. So "the model found a
    # plausible phraselet in its ambient environment" is not the general
    # mechanism; it only explains the two answers above that were verifiably
    # this run's own CTXLOOM_SESSION_HARP. Two consequences: (1) the model
    # emitting harp-shaped strings unprompted means a future matcher must
    # check the EXACT minted value, never harp SHAPE alone — a shape check
    # would be vacuously green here; (2) the historical false-green risk this
    # cell's minted-harp design closes is still real for the session-harp leak
    # channel specifically (had the nonce still been the session's own harp —
    # the design before the 2026-08-12 minted-harp ruling — this cell could
    # have gone GREEN on a leaked value rather than a delivered one).
    #
    # DO NOT fix a recurrence of this by loosening anything. There is nothing
    # to loosen: the output was perfect and the value was simply not there.
    @claude-code @host @ws-none @var-hook
    Examples:
      | engine      | runtime | workspace | approach | variant |
      | claude-code | host    | none      | hook     | hook    |

    # claude's native-file route (CLAUDE.md marker-merge), on the WORKTREE axis.
    # The axis is the point: "unsafe" in ApproachUnsafeFile names a shared-cwd
    # race, and an isolated cell is the conversion that makes the same write
    # safe — claude is the one engine with a race-safe shared-cwd realization to
    # convert away from (claude.NewSurfaces' SharedRealization), so this is
    # where the native write is worth measuring.
    @claude-code @host @ws-worktree @var-unsafe-file-shared
    Examples:
      | engine      | runtime | workspace | approach    | variant            |
      | claude-code | host    | worktree  | unsafe-file | unsafe-file-shared |

    # THE CODEX HOOK CELL WAS HERE, AND IT WAS RETRACTED ON 2026-08-13.
    #
    # It ran green and P1 recorded, on that basis, that the 2026-07-14
    # fragment-drop finding was fixed. It was not. The run's own stderr — saved
    # and not read — said:
    #
    #   ctxloom: warning: codex hooks and MCP servers were NOT written: codex
    #   settings/prompts/skills are delivered per-session at launch; no durable
    #   project home exists — see config_home. ... codex's cwd-keyed AGENTS.md
    #   context is unaffected and was still written.
    #
    # There was no hook in that session, and there could not have been a clean
    # measurement even with one: codex's SurfaceFor resolves (context, Hook) to a
    # COMPOSED delivery that also writes the native AGENTS.md, which codex reads
    # by itself. The nonce arrived by AGENTS.md and the credit went to the hook.
    #
    # So the 2026-07-14 finding is REOPENED, the cell is deferred in the registry
    # with what an attributing version would need (a `config_home: project`
    # binding so a hook exists, S4's stamp-file discipline so its EXECUTION is
    # observed, and a way to suppress the AGENTS.md leg), and
    # approachRequiredSurfaceDelivered now reds any hook cell whose hook was
    # never written — so this cannot come back quietly.
    #
    # Do not re-add an Examples row here without that machinery. A green cell
    # that cannot attribute is worse than no cell: it retires the question.

    # codex's native-file route (AGENTS.md) — now the ONLY codex context cell,
    # since the hook cell above was retracted. Green 2026-08-13 in 7.0s. It
    # proves codex ingests a ctxloom-delivered AGENTS.md live, which nothing in
    # the suite proved before; it does NOT distinguish that native ingestion
    # from codex reading the same bytes with a file-search tool, because codex
    # has no out-of-cwd delivery and no negative control exists for it. The
    # registry row states the claim at that strength.
    @codex @host @ws-none @var-unsafe-file @wip
    Examples:
      | engine | runtime | workspace | approach    | variant     |
      | codex  | host    | none      | unsafe-file | unsafe-file |
