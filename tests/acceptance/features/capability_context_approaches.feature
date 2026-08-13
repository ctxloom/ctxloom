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
    @claude-code @host @ws-none @var-system-prompt
    Examples:
      | engine      | runtime | workspace | approach      | variant       |
      | claude-code | host    | none      | system-prompt | system-prompt |

    # claude's hook route: context arrives through a SessionStart inject-context
    # hook that ctxloom writes into .claude/settings.json and the vendor binary
    # then EXECUTES. This cell is therefore also the first live evidence that
    # claude runs a ctxloom-written hook at all — inventory row 7 is P3's to
    # prove properly, but a green here cannot happen without it.
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

    # codex's hook route — THE CELL THIS PROBE WAS PARTLY BUILT FOR. A finding
    # recorded live on 2026-07-14 (see the comment above liveAgents["codex"] in
    # live_engine_registry.go) says codex's run-path context cache file — the
    # one its SessionStart hook actually reads — carries only
    # companion-contributed fragments and DROPS the active profile's own bundle
    # fragments, reproducibly, with an identical hash across two profiles whose
    # fragment content differed. This cell's nonce lives in exactly such a
    # profile bundle fragment. It therefore either reconfirms that defect with a
    # CONTEXT-DELIVERY failure, or records that it is fixed. Either answer is
    # the measurement; neither is a reason to loosen anything.
    @codex @host @ws-none @var-hook
    Examples:
      | engine | runtime | workspace | approach | variant |
      | codex  | host    | none      | hook     | hook    |

    # codex's native-file route (AGENTS.md). The control for the row above: same
    # engine, same axes, same nonce channel, different mechanism. If the hook
    # cell reds and this one greens, the defect is in the hook route and the
    # differential says so without anyone having to argue about the model.
    @codex @host @ws-none @var-unsafe-file
    Examples:
      | engine | runtime | workspace | approach    | variant     |
      | codex  | host    | none      | unsafe-file | unsafe-file |
