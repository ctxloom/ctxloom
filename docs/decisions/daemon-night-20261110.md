# Daemon night decisions, 2026-11-10

Owner bypassed the unattended stop conditions on this branch for tonight (review after MVP). Every design call made without a ruling is recorded here: the decision, the alternatives, why.

## diabolic-karate: the claude streaming child behind a child-engine port

### N1. Pins: taskloom moves with the platform pin
- **Decision:** ctxloom-lib to 1000f53 (run_piped) moves angzarr-platform to 1e83ece, and taskloom's crates to d233493 (taskloom's own re-pin onto 1e83ece, nothing else changed), so the lock keeps one `os`, `file-system` and `file-lock`. `arch_lint_rev` follows ctxloom-lib.
- **Alternatives:** keep platform d543f5e (two `os` crates in the lock: breaks the one-of-each invariant); a ctxloom-lib rev on the old platform (none has run_piped).
- **Why:** the only way to get run_piped with one of each crate.

### N2. The port's shape: a control and an observation iterator
- **Decision:** `run_ports::ChildEngine::start(&ChildSpec) -> StartedChild { control: Box<dyn ChildControl>, observations: Box<dyn Iterator<Item = ChildObservation> + Send> }`. `ChildControl` (`Send + Sync`, `&self`): `pid`, `send(ChildMessage{id, text}, Urgency) -> Queued`, `interrupt -> bool`, `clear -> Queued`, `end(grace) -> Ended{exit, undelivered}`. Observations: `Started{native_id}`, `TurnStarted{message_ids}`, `TurnEnded{message_ids, outcome: TurnOutcome}`, `InterruptAnswered{accepted}`, `Cleared{native_id}`, `Exited{exit, detail}`. Synchronous, std only (the core links no runtime).
- **Alternatives:** one object with a blocking `next()` (a caller could not send while another thread waits); an async stream (the core has no runtime); callbacks (the core has no threads to own them).
- **Why:** the daemon steers from one task and drains observations on another; an iterator over an mpsc receiver is the smallest std shape for that.
- `ChildEngine` has no `name()`: `Engine::name` already names the engine, and the same struct implements both (two `name`s would make every call ambiguous).

### N3. The adapter holds what cannot start now; one turn in claude at a time
- **Decision:** engine-claude never writes a message mid-turn. A wake (or a clear) to a busy child is HELD in the adapter and written when the turn in flight ends (its `result`). A steer to a busy turn writes the interrupt control request and is held FIRST (after earlier steers); to an idle child it is a wake. An inform is held and rides on the next turn written (its text first, joined by a blank line; the turn names every message id).
- **Alternatives:** write every message at once and let claude queue it (the probe: a mid-turn message queues as its own turn) — then a steer runs behind any wake already queued (review S2), and the stream's turns can only be matched to messages by guessing; refuse inform on claude (S-c) — but D15 says inform rides on the next message, and D20 holds on opencode, so the same urgency means the same outcome on both.
- **Why:** with one turn in claude at a time, every line of the stream belongs to the turn in flight, so turns are keyed by message id exactly (D28), the steer overtakes, and nothing depends on claude's internal queue.
- **Cost:** the adapter's held queue is in memory; a crash loses it (the daemon's pending projection, K3, is the durable owed list; `end` returns the held ids as `undelivered`). A steer that arrives after a turn is written but before claude has started it may find nothing to interrupt (untested).

### N4. A turn starts at its echo; the native id is the stream's `session_id`
- **Decision:** the command passes `--replay-user-messages`; the echo (`isReplay: true`) of the message given is `TurnStarted`. The native id is the latest `session_id` on any event; the first `system`/`init` is `Started`. A clear (`/clear` as a user message) ends at its `result`; `Cleared{native_id}` carries the session id after the `conversation_reset` (the probe: `new_conversation_id` is NOT the new `session_id`; the next `init` names it), or `None` when no reset was seen.
- **Alternatives:** match the echo's text to the message (fragile, and the inform merge changes the text); take `new_conversation_id` as the rotation (wrong id for `--resume`).
- **Why:** the D28 sheet confirms a turn by claude's echo, and D17 records the rotation as the session id a later `--resume` needs.

### N5. Outcomes reuse `TurnOutcome`
- **Decision:** a turn's end is `run_ports::TurnOutcome`: `result` with `terminal_reason: aborted_streaming` is `Failed{Interrupted}`; `is_error` is classed by `api_error_status` as the per-turn reader does (`stream::class_of`); an engine that exits mid-turn ends the turn `Failed{EngineError}` with stderr's start, then `Exited`. A `result` with no turn in flight ends a turn naming no message.
- **Why:** the run application already records TurnCompleted/TurnFailed from it.

### N6. The child's command
- **Decision:** `claude -p --input-format stream-json --output-format stream-json --verbose --replay-user-messages --append-system-prompt-file <ctx> --settings <home>/settings.json [--model] [--effort] [--resume <id> | --session-id <id>]`, a CHILD's environment (`ENV_ALLOWLIST`, no `ANTHROPIC_AUTH_TOKEN`) exactly as a turn's. No `--include-partial-messages` (the deltas are the hooks' to record, ruling 51; the stream is read for the reply, the outcome and the rotation only). `--session-id` is D17a's restart-with-a-fresh-id; `resume` wins if both are set.
- **Not changed:** the child home's hooks are still the per-turn set (ruling 51 left SessionStart out because `claude -p` fired it per process = per turn; a long-lived child fires it once, and `/clear` fires `SessionStart:clear`). Registering it again is a follow-up for the daemon wiring.

### N7. Ending, dropping, draining
- **Decision:** `end(grace)` interrupts a turn in flight, closes stdin (claude exits 0 on EOF), waits `grace`, then `terminate_tree(grace)` (SIGTERM to the group, then kill). Dropping the control closes stdin only (the child ends after its turn). The reader's stdout read is bounded after the exit by a 2 s drain grace, after which the tree is killed (a descendant holding the pipe), as run-within's own drain.
- **Alternatives:** kill on drop (loses a turn's work silently); leave the child running on drop (orphans in tests and on a daemon bug).
- **Why:** D15: the end is "close stdin or stop the process"; EOF is the clean one, the stop is the bound.

### N8. `Json::compact` in surface-ports
- **Decision:** add `Json::compact()` (serde_json's one-line writer) beside `pretty()`, for JSON-lines.
- **Alternatives:** a serde_json dependency in engine-claude; hand-escaping strings.
- **Why:** the one JSON value type the adapters already share.

## smashing-embargo (part 1): the daemon's domain model (D28)

### D-N9. The requester: a stamped `optional SessionRef`; absent is the user
- **Decision:** every command the owner rule guards (`CloseSession`, `SendMessage`, `ClearChild`, `LaunchChild`, and `StopSession` now) carries `optional SessionRef requester {project_id, session}`, which the listener STAMPS from the credential (S-f). Absent means the USER: the human at the CLI, or a policy of the user's daemon acting with the user's authority (ruling 37: the user boundary is the trust boundary). A session requester is allowed only when it is the target's recorded `owner` (for `LaunchChild`: the owner session itself, its own stream root and project).
- **Alternatives:** a separate principal enum (user, owner, policy) in each command (more surface; the listener knows only the credential's session); letting the target session act on itself (D25 names only the owner; a session closing itself is the human's or its owner's call).
- **Why:** the domain rule is one comparison against the folded state, and a command that names no session cannot be forged by a model (the listener always stamps a session credential's session).

### D-N10. `StopSession` reconciled with D25 (S-f)
- **Decision:** `StopSession` gains the stamped `requester`. When it names a session, the owner rule decides (the owner stops its own child whatever the child's depth; any other session is `NotOwner`). When it names none, Go's I2 depth rule stands (`requester_depth > 0` is `ChildCannotStop`), so today's callers (the run application stamps `requester_depth` only) behave exactly as before.
- **Alternatives:** drop `ChildCannotStop` (breaks the runs application's admission, which stamps depth from the run identity); refuse every child requester even when it is the owner (contradicts D25's orchestration scope for a grandchild owner).

### D-N11. The close is `CloseSession` → `SessionClosed`, a refusal named `SessionIsClosed`
- **Decision:** `CloseSession{cause, requester}` emits `SessionClosed{cause}` once; a second close records nothing. The state keeps `closed` and `closed_cause`. The refusal for "this session is closed" is the message `SessionIsClosed` (code `sessions-session-closed`) because the event already owns the name `SessionClosed`. `SendMessage` and `ClearChild` refuse it; `LaunchChild` on a closed owner refuses `OwnerClosed`. `BindEngineSession`, the capture facts, keep, rename and the incarnation end still record on a closed session (they report what already happened while it wound down). A close stops no run itself: a policy on `SessionClosed` ordering `RequestStop` on its runs is the daemon part's.
- **Why:** K1's terminal state with the smallest change to what facts record.

### D-N12. One message, `SendMessage` → `MessageSent`, its command id from `message_id`
- **Decision:** `SendMessage{message_id, urgency, text, requester, supported}`. `supported` (the recipient engine's urgencies, asked by the sender's application) is the capability EVIDENCE; an urgency not in it (and so every urgency when it is empty) is `UrgencyUnsupported` (fail closed; S-c). `MessageSent` records `sender = requester`. The command id is `sessions::command_id(message_id)`: UUIDv5 in a fixed namespace, UUIDv5(URL, `ctxloom:command`), of the key text; `sessions::keyed_book` builds the book for any domain's command, and every policy key below goes through it.
- **Urgency enum:** `URGENCY_UNSPECIFIED` is not a domain value (the markup drops it; a wire message carrying it does not decode), so there is no `unspecified` word.
- **Why:** D28's sheet (command id = message_id) with angzarr's UUID command ids.

### D-N13. A wake owes an incarnation through a continuation step
- **Decision:** a wake to an open, unstopped session with no open incarnation emits `MessageSent` then `IncarnationOwed{incarnation + 1, reason: "wake"}` in ONE decision (M2), as an `Emit` whose continuation returns the owed event. A wake to a stopped session owes nothing (the stop flag's meaning is unchanged). An inform never owes one (D30). No relaunch budget, backoff or `RelaunchWithheld` yet (deferred: M2's budget needs the scheduler, the daemon part's).
- **Alternatives:** a separate `OweIncarnation` command from a policy (two decisions: a crash between them leaves a wake with nothing to run it).

### D-N14. The owner link lives on both streams; `Child` is hand-written state
- **Decision:** `MintSession{owner}` / `SessionMinted{owner}` record the child's owner; the owner's stream records `ChildLaunched{child: SessionRef, child_root, harp, engine, depth = owner's + 1}` and folds `children` (launched minus `ChildEnded`, `ChildForgotten`, `ChildLaunchFailed`). `Child{session, root}` is hand-written in `state.rs` (the markup generates a type only for a message a command or event carries).
- **LaunchChild's checks, in order:** `NoSession`, `NotOwner` (only the owner session itself or the user), `OwnerClosed`, `NoOpenIncarnation`, `DelegationLimit` (the command's `delegation_limit` is the config's evidence; fail closed: 0 allows none; open children counted from the fold), `UntrustedTarget` (another project, `trust.trusted` false). The child's stable id and harp are the caller's (minting is random; decide is deterministic).
- **The owner's bookkeeping:** `EndChild{child, cause}` → `ChildEnded`, `ForgetChild{child}` → `ChildForgotten` (commands, refusing `NoSession` only; a child the owner does not hold records nothing, so two policies racing to forget and end the same child record one event between them), and the fact `RecordChildLaunchFailed{child, reason}` → `ChildLaunchFailed` (K2's compensation; `reason` is a refusal code, never a secret, M4).
- **Status choices:** `NotOwner` PERMISSION_DENIED; `SessionIsClosed`, `OwnerClosed`, `NoOpenIncarnation`, `DelegationLimit`, `UntrustedTarget` and `UrgencyUnsupported` FAILED_PRECONDITION (state or capability forbids, not a malformed argument).

### D-N15. `ClearChild` → `ClearRequested{clear_id}` on the child
- **Decision:** the clear names a `clear_id` (the requester's), keyed `clear:<session>:<clear_id>`; refused `NoSession`, `SessionIsClosed`, `NotOwner`. The rotation itself stays `BindEngineSession` (the driver maps `conversation_reset`), unchanged.
