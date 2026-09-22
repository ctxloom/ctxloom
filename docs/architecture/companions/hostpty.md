# `internal/adapters/hostpty` + `internal/adapters/attach` — the runner on a pty

**What they are.** The originator's side of an interactive turn. `ctxloom run` (and `ctxloom
init`'s discovery launch) starts `ctxloom runner <engine>` on a pseudo-terminal whose MASTER the
originating process holds, and pumps its terminal seams onto that master: keystrokes in, the
engine's bytes out, each resize onto the pty. The runner owns the slave and execs the engine on
it; resize and signals cross the pty, never a proxied stream.

- `hostpty.Start(ctx, cmd)` runs the self-exec'd runner on a pty for a HOST cell: the child is a
  session leader (`Setsid`, `Setctty`) armed with `PR_SET_PDEATHSIG(SIGTERM)` on Linux, so a
  runner never outlives the originator. `Session` carries `Master()`, `Resize`, `Exited()`
  (closed once the child is reaped), `Wait` (the exit code; closes the master) and `Kill`.
- `attach.Start(ctx, cmd, name, remove)` is the CONTAINER counterpart: the runtime CLI's
  `docker run -i -t … ctxloom runner <engine>` (`isolation.Policy.InteractiveRunner`) on the same
  kind of pty, so the CLI's `-it` attachment IS the terminal the runner's stdio lands on. What
  attach adds is teardown by NAME: the CLI's death does not end the container the daemon runs, so
  `Kill` removes the container first.

**The contract they own.** *One pty master per interactive run, wherever the runner runs.* The
frontend (`internal/adapters/cli/run_pty.go`'s `driveOwnedInteractive`, over `termui`) is blind to
the cell: it pumps onto `runnerTTY` and reads the run's outcome from the coordinator's event stream
(`RunCompleted`), which the runner emitted before it exited. No keepalive, no exec-into, no
handoff file: the runner is the container's foreground process, started by the coordinator-owned
run's starter (`runState.ptyStarter`).

**Where the docker `-it` composition was settled.** The slice-13 GATE
`TestCoordOwnerRun_InteractiveContainerIsTheForegroundRunner` (`internal/core/coord`,
`docker_integration`) types a line on the master and reads the mock's echo back through
`docker run -i -t`, asserts the container's command is `runner mock`, that no `docker exec` is in
the process table, and that nothing under the session's `persist/` carries a run-start.
