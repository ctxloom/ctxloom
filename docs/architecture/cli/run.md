# `ctxloom run` — the session host

`ctxloom run` (`internal/adapters/cli/run.go`, `runState`) is the SESSION
HOST: it resolves what context, which engine and which isolation boundary a
launch takes, stands the project's coordinator up, mints the launch as an
owner-owned run on it, starts the run's runner, and holds the terminal (or
the stdin/stdout pipes) until the run ends. Every launch is the same trunk;
what differs is the starter the runner is started through and how the host
drives it.

## Phases, in order

1. **Config and prompt** — `runState.loadConfig` (the reader's warnings are
   recorded as findings), `resolvePrompt`.
2. **Startup tasks** — `runStartupTasks`: the remote-dependency sync (announced
   — it is the one startup task with a network side), the companion report,
   the orphaned-worktree and orphaned-container reapers. All gated off by
   `--dry-run`, which must be side-effect free.
3. **Dry run** — `emitDryRun` runs the SAME resolver over stateless ports (an
   in-memory session store, the project root as the cell) and stops; it is
   gated (`gateStartup`) like a real start, so previewing a broken setup
   says so.
4. **Resolve the launch** — `resolveLaunch` (`operations.StartRun` over
   `launch.Deps`): the session minted, the package assembled and carried, the
   session's MCP endpoint minted onto the session record, the cell prepared.
   `refused` reports the startup gate first for a refusal the gate explains
   (an empty assembly, a runtime the cell could not provide).
5. **The startup gate** — `gateStartup`: every fatal-class finding recorded
   so far aborts here (exit `strictness.ExitCodeFatalFindings`), before the
   engine launches; `--degraded` lowers them to warnings.
6. **The coordinator** — `hostCoordinator` → `mcp.HostCoordinatorForSession`:
   the project's ONE coordinator, hosted in this process (a project another
   live session owns is refused, `coord.ErrStateOwned`), and the owner's
   credential — the identity the owner-owned run is minted under, revoked on
   the same teardown that closes the coordinator.
7. **The transport** — `startTransport` → `startOwnedRun` →
   `Coordinator.StartOwnedRun`: the run is enqueued parent-less under the
   owner's identity and its runner (`ctxloom runner <engine>`) started
   through the launch's starter with the run's own reach-back trio — on a pty
   the host holds for an interactive launch (`ptyStarter`,
   `internal/adapters/hostpty`; a container's foreground rides
   `internal/adapters/attach`), as a plain process for `--one-shot`
   (`processStarter`, `operations.RunnerStarter`). The Launch reaches the
   runner over `StartRun`; nothing about the run rides this process's
   environment.
8. **The drive** — `drive`: an interactive run pumps the terminal seams onto
   the runner's pty master (`driveOwnedInteractive`, with `adapters/termui`'s
   observation layer); a one-shot collects the run's FINAL answer off the
   coordinator's event stream and records the oneshot transcript
   (`runOneshotViaCoord`).
9. **Teardown** — the runner is killed through its handle, the coordinator
   drained (`BeginDrain`) then closed, the owner credential revoked, the
   session marked ended.

## What a session delivers, and where

The runner delivers the launch into the SESSION's home — the engine's
settings, hooks, MCP registry, context, commands and skills — never into the
project (ruled 2026-09-21: sessions carry their surfaces; the project is
written only by the explicit `manage hooks install`). ctxloom's own MCP
server reaches the engine as the session's endpoint (URL + bearer in the
session's registry), rendered from the companion loadout's declaration by
`delivery.InputsFor`; see `docs/architecture/cli/mcp.md`.

## Invariants

- The engine is spawned exactly once per turn, with exactly one assembled
  context, under exactly one resolved permission posture, and its exit
  reaches the shell.
- A process-owning entry point gates on strictness before it spawns
  (`gateStartup`; `profile materialize` and the version gate use the same
  `phaseGates`).
- The runner is the one credential holder: the host keeps the owner's
  credential for minting and revocation and stamps nothing on its own
  environment.
