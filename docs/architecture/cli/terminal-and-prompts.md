# Terminal ownership, the run UI, and interactive prompts

Two separate concerns share this page because they share one resource: the
process's terminal. The first is `ctxloom run`'s terminal *ownership* — raw
mode, the split view that draws the agent tree beside the engine's terminal,
and diagnostics redirection so warnings do not scribble over it. The
second is the package-wide *prompting* primitive: one buffered reader over
stdin, shared by every y/N question in the CLI, plus the terminal predicates
that decide whether prompting is legal at all.

## Terminal ownership during a run

```mermaid
flowchart TD
    RUNE["runRun — interactive arm"]
    RUNE --> IT["interactiveTerminal(ctx)<br/>run_terminal.go"]
    IT --> MR["termMakeRaw(stdin fd)"]
    IT --> WR["watchResize(ctx)<br/>run_resize_unix.go / run_resize_windows.go"]
    IT --> RESTORE["idempotent restore closure"]
    WR --> SIG["SIGWINCH → terminal size<br/>latest-wins coalescing, closes on ctx"]

    RUNE --> RD["redirectDiagnosticsForTUI<br/>run_terminal_ui.go"]
    RD --> LOG["clidiag → &lt;harpdir&gt;/diagnostics.log"]
    RUNE --> STU["setupSplitView"]
    STU --> SV["tui.StartSplit — the split view<br/>prefix key (ui.prefix_key) · panel · emulated pane"]
    RUNE --> FP["runState.frontendPort<br/>operations.NewFrontendPort — the session's frontend port"]
    FP --> STU
    STU --> PRES["modalPresenter · credentialPresenter<br/>Port.Approvals · Port.CredentialHolds"]
    STU --> ALERT["desktopAlert — platform.DesktopNotifier"]
    FP --> TERM["Port.Terminal(self)<br/>driveOwnedInteractive copies its output into the pane"]

    RUNE --> LAUNCH["launcher Start / Wait"]
    WR --> LAUNCH
    LAUNCH --> EXIT["restore() → status.Code"]

    VAL["validateTerminalUIConfig — called from runState.gateStartup"]
    VAL -.->|"bad ui.prefix_key ⇒ strictness finding"| GATE["gates.close(PhaseStartup)"]
```

- `interactiveTerminal` makes the frontend the terminal owner: it puts the
  real terminal in raw mode so keystrokes pass through untouched to the
  runner's pty, returns `os.Stdin` as the keystroke source plus a resize
  channel, and a restore func that switches off the terminal modes an engine
  may have left on (`terminalModeReset`: focus reporting, bracketed paste,
  every mouse mode, and the cursor shown again, written to stdout when it is a
  terminal) and then undoes raw mode. Each reset is a no-op on a mode the
  engine never set; the alternate screen is not reset. The restore is
  idempotent, so callers defer it immediately (panic safety) and may also
  call it inline before any normal-path output. When stdin is not a terminal
  it returns `(nil, nil, no-op)` and the run proceeds without a pty owner; a
  raw-mode failure on a real terminal warns that the session will not receive
  keystrokes.
- `watchResize` emits the initial size, then one per SIGWINCH, latest-wins,
  and closes the channel on ctx cancel; the Windows build-tagged counterpart
  emits one size and closes.
- `validateTerminalUIConfig` parses `ui.prefix_key` (`tui.ParsePrefixKey`); a
  bad value records a **fatal-class** strictness finding, consumed when
  `runState.gateStartup` closes the startup phase — before anything is
  spawned. `setupSplitView` starts the split view; a bad key there only warns
  and returns nil, reachable only under `--degraded` because the gate already
  aborted otherwise.
- `prepareSessionIO` hands the split view the human's terminal (`os.Stdin`,
  already raw, and `os.Stdout`) and the session's terminal from the port. The
  split view then owns them: it reads the human's input itself and writes it
  to the session's terminal, and sizes that terminal to its pane, so the
  drive pumps no stdin and no resizes; the drive copies the engine's output
  into `SplitView.Pane`. `terminalUIIdentity` is the panel's header.
  Everything the split view and the presenters show or do rides the session's
  frontend port (`runState.frontendPort`,
  [core/frontend-port.md](../core/frontend-port.md)): the approval presenter
  presents `Port.Approvals()` on `tui.SplitView`, and the credential notice
  polls `Port.CredentialHolds` into the panel. None of them holds the
  coordinator (`TestArch_FrontendsHoldNoCoordinator`).
- The split view is the approval queue's only presenter, so a run that does
  not start it cannot present approvals, and marks its queue unattended
  (`markUnattended`, over `coord.ApprovalQueue.Unattended`): `hostCoordinator`
  for a one-shot owner, `prepareSessionIO` when no split view starts
  (`--plain-terminal`, no tty, an unparseable prefix key). The queue then
  refuses every request no grant covers at once, at every depth, naming why
  and the remedy (`unattendedReason`): a permission rule, or the posture that
  asks nobody, named for every shipped engine in its own words, since the
  asker may be a child on an engine other than the root's. The startup
  warnings (`runState.warnPosture`) predict the same outcome from the same
  facts (`presentsApprovals`) before either has happened.
- `driveOwnedInteractive` (`run_pty.go`) attaches the session's terminal
  through `Port.Terminal(activeHarp)` — the run's pty — and `driveSession`
  copies its output to the session's stdout (the pane, or the terminal
  itself under `--plain-terminal`) until the runner exits. The split view
  stopping on its own (`sessionIO.uiDone`) means the human's terminal failed
  under it: like a drain that ended, that is output lost, and the runner is
  ended; with the run's ctx it is the signal. Every exit path restores the
  terminal: `SplitView.Close` draws the pane's last frame and gives the
  terminal back (the alternate screen left, the mouse modes reset), then raw
  mode is undone.
- `redirectDiagnosticsForTUI` diverts `clidiag` warnings to
  `<harpdir>/diagnostics.log` and returns a restore func. It is installed
  only for interactive runs that own a terminal and did not pass
  `--plain-terminal`.
- `shutdownSignals` (`signals_unix.go` / `signals_windows.go`) is the
  build-tagged signal set every long-lived command's `signal.NotifyContext`
  uses.

`--plain-terminal` disables the split view for one session: the engine draws
on the terminal directly, and keystrokes and resizes are pumped onto its pty.

## The stdin invariant

```mermaid
flowchart LR
    SR["stdinReader — prompt.go<br/>the single bufio.Reader over os.Stdin"]
    PL["promptLine(prompt)"] --> SR
    PL --> C["every interactive prompt in the package"]
    SR --> STDIN["os.Stdin"]
```

`stdinReader` is the single buffered reader over `os.Stdin`, shared by every
interactive prompt. A fresh `bufio.Reader` per prompt would silently
discard any bytes a previous reader buffered past its line, so all prompts
read through this one reader. `promptLine` and `plural` live in
`prompt.go` as cross-command primitives.

`init.go`'s setup interview has a structurally different reader: `initPrompts`
wraps an injectable `io.Reader`. Its constructor `newInitPromptsFrom`
additionally puts the **process-global** terminal into raw mode even when
built over an arbitrary reader, so the "injectable reader" seam does not
fully isolate from a real tty.

## The split view

The split view is the interactive run's whole screen: the agent tree on the
left, the selected agent's view on the right, drawn by one compositor.

- **`internal/adapters/termemu`** is the one VT emulator seam. `termemu.Terminal`
  is the interface; `termemu.NewXVT` implements it over charmbracelet/x/vt. It
  commits a `termemu.Frame` at synchronized-output boundaries and reports the
  child's title, bell, OSC 52 clipboard and mouse mode through
  `termemu.Callbacks`. Only adapters import it: the layering rule keeps core and
  shared free of it, as it does cli/tui.
- **`tui.Split`** (the bubbletea model) and **`tui.SplitView`** (`tui.StartSplit`)
  compose the screen. The left panel is the roster from the frontend port
  (`WatchRuns`, re-read with `ListRuns` on each event), with a token gauge and an
  activity glyph per row; it folds a subtree on Enter and collapses to a glyph
  column on a narrow terminal. The right pane is the root's emulated terminal,
  each row serialized once from the emulator, or, for a selected child, that
  child's event feed plus a line that sends `agent_send` (or `agent_steer` to
  interrupt). `ui.prefix_key` toggles focus between list and pane; every other
  key goes to the pane.
- **Approvals keep their focus locks**: a keystroke typed for the engine never
  answers an approval (`tui.PresentPolicy`). An approval arriving rings the
  bell and raises a desktop notification beside it, both under one rate
  limit (`approvalBellInterval`) and neither while the approvals view is
  open. The notification is `platform.DesktopNotifier`: `notify-send` on
  Linux, found as the engines' binaries are (`shellenv.Resolve`) and skipped
  when absent; macOS and Windows show none yet. Approvals are answered only
  in the terminal.
- **claude draws whole frames**: its interactive launch keeps the classic
  renderer (`CLAUDE_CODE_DISABLE_ALTERNATE_SCREEN`) and forces synchronized
  output (`CLAUDE_CODE_FORCE_SYNC_OUTPUT`), so the emulator commits each frame
  at its end.
- **Scroll mode reads the engine's history.** claude's pinned classic renderer
  scrolls what it has drawn off the top of the emulated screen into the
  emulator's history (`termemu.Terminal.History`; `Frame.History` counts it at
  each commit). The prefix then `[` (or PgUp, a page back) shows that history
  in the pane, marked `[back/length]` at its top right; PgUp/PgDn, Up/Down, `g`
  and `G` move, `q` or Esc return to live. The view is anchored as output
  arrives, as tmux's copy mode is, until the history reaches x/vt's bound and
  starts dropping its oldest lines. Keys, pastes and clicks never reach the
  engine meanwhile; its output keeps landing. The human's terminal reports the
  mouse (cell motion, for the wheel) only while scrolling: `Split.keepScroll`
  ends scroll mode whenever the pane or the keyboard goes elsewhere, so every
  exit restores the engine's own mouse mode, and closing the split view resets
  the terminal's.
- **One path to the child**: keys, mouse reports, pastes, focus reports and the
  emulator's replies all cross x/vt's reply pipe to one drain, so they reach
  the engine in the order they were sent.
- **`internal/testsupport/termoracle`** is the root terminal surface's contract
  (prefix-key encodings, the input token corpus, the summon contract), taken
  from the terminal layer the split view replaced; `tui.Split` passes it, so a
  behaviour the run depended on cannot be dropped unnoticed.

## Terminal predicates

`terminal.go` defines `isInteractiveTerminal` (stdin *and* stdout are
terminals — the gate every `-i` surface checks), `stdinIsPiped`,
`stderrIsTerminal` and `stdoutIsTerminal`. `isInteractiveTerminal` is a
package variable so tests can substitute it.

## Invariants

- **One reader over stdin.** See above.
- **Restore is idempotent and always registered.** `interactiveTerminal`
  returns a restore closure the caller defers.
- **The prefix key is validated before anything spawns.**
  `validateTerminalUIConfig` runs inside `runState.gateStartup`, ahead of the
  startup gate closing, so a malformed `ui.prefix_key` aborts the launch rather
  than silently disabling the viewer mid-session.
- **Pointer identity, not file descriptor, decides paging.** `shouldPage`
  (`pager.go`) requires `out == os.Stdout` *and* `stdoutIsTerminal()`; a
  redirected writer is never paged even if stdout happens to still be a tty.
- **Diagnostics redirection is interactive-only.** `clidiag` warnings go to a
  per-harp log only when the run owns a terminal and `--plain-terminal` is
  not set.
- **The terminal is given back on every exit.** `sessionIO.restore` closes
  the split view before it undoes raw mode, on a clean exit, an error and a
  signal alike.
