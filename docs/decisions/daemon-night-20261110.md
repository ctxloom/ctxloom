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

### D-N16. The run's turn gate keyed by message ids (S-b)
- **Decision:** `QueueTurn{prompt_ref, resume, message_ids, urgency: TurnUrgency}`; `TurnQueued` carries the ids and urgency. The run folds `turns` (every turn queued and not ended, oldest first; `turn_in_flight` stays, the first of them). A steer while a turn is in flight emits `InterruptRequested{turn, message_ids}` (once per turn in flight) then `TurnQueued`, in one decision (an `Emit` continuation); a wake or an inform is refused `TurnInFlight` naming the turn in flight (it stays pending). A steer to an idle run is just queued.
- **Ends:** `TurnStarted`/`TurnCompleted`/`TurnFailed` (and their events) gain `message_ids`. An end naming message ids frees the open turn holding the first of them; one naming none frees the open turn of its number (the M1 one-shot's turn 1, never queued, frees nothing and records as before). External ids: `turn:<run>:<first message id>:<phase>`, else `turn:<run>:<turn>:<phase>` as before (a numeric message id could collide with a turn number: message ids are UUIDs or policy keys, never bare numbers).
- **`TurnUrgency`** is the runs proto's own enum (inform, wake, steer), mapped from the session's `Urgency` by the delivery policy, so `runs.proto` imports no sessions proto.
- **`urgency` is required** (the markup has no unspecified value): every `QueueTurn` now names one. Nothing in production sent `QueueTurn` before (the one-shot launcher records turn facts without queueing), so no caller changed behaviour.

### D-N17. The pending messages: a projection of MessageSent minus TurnQueued's ids (K3)
- **Decision:** `crates/core/read-model/pending-messages` (`Pending`, `PendingMessages` on the projector port, `catch_up()` over the sessions and runs domains), shaped like `pending-launches`. Consumption is kept per RUN stream (its session from `RunEnqueued`, the ids its `TurnQueued`s carried), and a message is pending when no run of its session carried it, so the cross-stream order of a catch-up (S-d) cannot resurrect a consumed message. A book from 0 rebuilds a stream; a purged one leaves.
- **A closed session has nothing pending** (no turn will carry it; the `MessageSent` stays in its stream as the record). `owed()` lists the sessions with something pending, what a driver acts on.

### D-N18. The owner-to-children view is the session index's record
- **Decision:** `SessionRecord` gains `owner`, `closed` and `children` (copied from the folded session, so the view per owner stream is the owner's own `ChildLaunched` minus ended, forgotten and failed). No separate crate: the index already folds every session stream with the aggregate. `View` gains `pending` (filled by `show` only, D30).

### D-N19. The policies are pure functions in `session-policies` (application)
- **Decision:** `crates/core/application/session-policies`: `Keyed<C>{project_id, cover, key, command}` (its `book()` is `sessions::keyed_book`, its command id `command_id(key)`) and `KeyedFact<F>{.., external_id, fact}`. A policy acts with the user's authority: no requester (D-N9).
  - `deliver(RunGate{session, incarnation, live, in_flight}, pending)`: no live run, nothing; idle, every pending message in the order sent if any is a wake or steer (informs alone wait, D30), urgency steer if any steer else wake; in flight, the pending informs and steers (the steer's turn is the next turn), wakes wait. Key `send:<first wake or steer carried>`. `resume: true`. The gate is the run's own folded state, never a read model.
  - `cascade(owner, children)` → `CloseSession{cause: "owner-closed"}` per child, `cascade:<owner>:<child>`; `child_ended(child, owner, closed)` → `EndChild` on the owner, `child-ended:<owner>:<child>` (added: without it the owner would hold a closed child until destroyed).
  - `notice(child, harp, owner, ended, supported)`: on `notice_parent`, `SendMessage` to the owner, a wake when the owner's engine takes one, else an inform, else nothing; message id = key = `notice:<child>:<incarnation>`.
  - `launch(owner, launched, output_dir)` → the child's `ClaimName` (`claim:<owner>:<child>`, on the target's claim stream) and `MintSession{owner, origin: agent, depth}` (`mint:<owner>:<child>`) in the TARGET store (D19); `launch_failed(owner, child, reason)` → the fact `RecordChildLaunchFailed`, `launch-failed:<owner>:<child>`.
  - `destroy(target, owner, children)` → `CloseSession{cause: "owner-destroyed"}` per child (`owner-destroyed:<target>:<child>`) and `ForgetChild{target}` on its owner (`destroy:<target>`).
- **Races:** two policies closing one child (cascade, destroy) record one `SessionClosed` (the second finds it closed); a redelivered key is DUPLICATE (tested through the aggregate).

### D-N20. The destroy tells the links before it purges (rulings 94, 98; M3)
- **Decision:** `session remove --yes` runs `session_policies::destroy` over the session's record (its `owner` and `children`, from the session index) after its refusals and before the purge: a keyed `CloseSession{cause: "owner-destroyed"}` to each child and `ForgetChild` to its owner, each in the store of the project it names (D19: the destroyed session's own, else one of `every_project()`; a project with no store here is passed over). A refusal (a child already destroyed: `NoSession`) is passed over; a kernel or store failure fails the remove before anything is purged. A remove that only reports sends nothing.
- **Why:** K3: a purge emits no event, so nothing could tell the children or the owner afterwards.
- **Not done:** the child's runs are not stopped by the close (a `RequestStop` policy on `SessionClosed` is the daemon's), and a cross-project child whose project is unknown to this user stays open (a "close abandoned owners" sweep, D29's accepted cost).

### D-N21. `session show` lists the pending messages (D30)
- **Decision:** `show` catches the pending messages up fresh from the session's store (the sessions and runs domains) and fills `View.pending`; `list` and `search` leave it empty (no catch-up cost on a listing). Text: after the record, `pending:  <n> message(s) no turn has carried yet`, then `  <id> (<urgency>): <first line>` per message; none, no line. JSON: `pending: [{message_id, urgency, text, sender?}]`, left out when empty (as the other optional keys are). The cli contract (`features/cli/session.feature`, @pending) gains the scenario; its step that sends a message needs a hidden `ctxloom test` command, which the daemon part builds with the transport.

### D-N22. The stamping seam: `runs_application::stamped` and `Identity::requester`
- **Decision:** `Identity::requester(project_id)` names the identity's session as a `SessionRef` (the listener knows the project from the store it serves); `stamped(command, &requester)` overwrites `requester` on `CloseSession`, `SendMessage`, `ClearChild`, `LaunchChild` and `StopSession` and leaves every other command as it was. The daemon's listener calls it on each command its ORCHESTRATION scope (D25) admits; that scope (which command types the hook or orchestrator credential may send) is the daemon part's: the gate's `Scope` lists only command types (S-f).

## smashing-embargo (part 2): the opencode ACP child

### D-N23. agent-client-protocol =5.2.0 is in; every JSON output it would reorder is made order-explicit (D21's gate)
- **Finding:** the crate (default features off) turns on serde_json's `preserve_order` (and `raw_value`) in every build that links engine-opencode, the binary's included: a `serde_json::Map` then keeps insertion order instead of sorting. The whole workspace's tests under that build showed one failure (taskloom-cli-client: a passed-through envelope's args came in the envelope's order); a read of every `serde_json::Value`/`Map`/`json!` in non-test code found four outputs that would change bytes: the transcript's D27 lines (session-index: `json!` in source order, previously sorted), the config blocks shown as JSON (library-application `json::convert`, `config_show::json`: the document's order), doctor's settings rewrite (`hooks::strip`: the user's file order) and those envelope args. Structs, angzarr-presentation's envelope, `surface_ports::Json` and the YAML config writer (`library::write`'s sorted-key merge links no serde_json) are unaffected; forge-github's request bodies (to GitHub, not shown) are left in source order.
- **Decision:** each of the four sorts its keys explicitly (a `BTreeMap` collect, or a recursive `sorted`), so the bytes are what they were under either serde_json map; a guard test per output asserts the exact sorted bytes (it fails under the unified build without the fix: shown for the transcript line). cargo-deny passes: `multiple-versions = "warn"` already admits the new duplicates (a second derive_more, darling, hashbrown, indexmap, schemars, foldhash), and every licence is allowed.
- **Also noted:** agent-client-protocol itself turns on its schema crate's `unstable_proxy_chains` (not ours to turn off; nothing of it is used).
- **Alternatives:** the schema crate alone (M3; still `preserve_order`, and a hand-written JSON-RPC client); STOP (the change was four small sites).

### D-N24. The ACP connection runs on futures' executor, on a thread the child owns
- **Decision:** engine-opencode's child runs the crate's own connection (`Client.v1()`, its `Lines` transport) under `futures::executor::block_on` on a thread of the child's. Std pipes are bridged by threads: a reader thread turns stdout into the transport's incoming `Stream` (an unbounded futures channel), and the outgoing `Sink` writes each line to stdin (blocking, small lines; stdout is always drained, so opencode never blocks the other way). The control (sync, any thread) and the connection decide under one lock and post every call to one channel, so calls are made in the order decided (an interrupt decided before a held prompt is written is sent before it).
- **Alternatives:** a tokio runtime owned by the adapter (tokio is in the workspace, but the crate needs no runtime and the claude child is std threads); a runtime the caller provides (the port is synchronous: the daemon's caller has nothing to hand in); the crate's `process` feature (refused by D21).
- **Why:** no runtime dependency, the claude child's shape (threads, one lock), and the crate's own JSON-RPC (ids, routing, the permission request's reply).

### D-N25. The input's end is a notification of ctxloom's own, never written
- **Decision:** `end` and the control's `Drop` send `_ctxloom/end_of_input` through the connection; the outgoing sink never writes it and closes stdin instead. A line the engine can no longer take is dropped, not an error, so the connection keeps reading.
- **Why:** the crate queues what is sent; closing stdin from the control could beat the queued `session/cancel` to the pipe. Through the sink the end is ordered after every line before it, and the connection still reads opencode's `cancelled` reply and its exit (the spike: `dispose` runs on EOF, exit in 64 ms).

### D-N26. Steer mid-turn is the native join; `interrupt` then steer is the hard stop (D20)
- **Decision:** `send(steer)` to a turn in flight writes the prompt at once (`Queued::Written`; opencode takes it at the next step boundary); each prompt's reply ends ITS messages' turn, both answered with the joined turn's last agent message (the spike: both prompts reply `end_turn` together, "the first prompt never gets its own answer"). After `interrupt()` (`session/cancel`), a steer is held first (`Queued::Interrupting`) and written when the cancelled prompts have replied. Wake mid-turn and inform are held (an inform rides on the next prompt, D30). `InterruptAnswered{accepted}` is said at the first reply after a cancel: `cancelled` is accepted, a turn that outran it is not (ACP's cancel has no answer of its own). `TurnStarted` is said when the prompt is sent (ACP has no echo).
- **Alternatives:** steer always cancel-then-prompt (claude's outcome exactly: the turn in flight would end `Interrupted`); the join only (no hard stop).
- **Why:** D20 names both; the port's `interrupt` is the caller's hard stop, and per-prompt ends keep D-N16's run gate whole (the steer's queued turn is freed by its own end).

### D-N27. The model is set by opencode's `session/set_model`, as a custom request
- **Decision:** after `session/new` (and after each clear's), the child sends `session/set_model {sessionId, modelId}` (a `JsonRpcRequest` derived in engine-opencode); the opening (or clear) is done only when it answers. A failure to set it ends the child (its exit's detail says why) rather than running another model.
- **Alternatives:** ACP v1's stable `session/set_config_option {configId: "model"}` (in the schema, unverified against opencode); `-m` on `opencode acp` (unverified).
- **Why:** the spike verified `session/set_model` live on opencode 1.18.4; nothing else is.

### D-N28. A child's permission requests are answered by a new `ChildSpec.permissions`
- **Decision:** `run_ports::ChildPermissions { Refuse (default), Allow }` on the `ChildSpec`; opencode answers `session/request_permission` with the `reject_once` / `allow_once` option, else `cancelled`; never an "always" (nothing outlives the request). The daemon maps the agent's permission posture onto it (not built: no caller builds a `ChildSpec` yet).
- **Why:** no posture reached the child port; a child has no human to ask, so the default fails closed.

### D-N29. Resume is `session/load`; `ChildSpec.session_id` has no opencode meaning
- **Decision:** a `resume` loads the session (`session/load`, advertised as `loadSession`); its replayed updates arrive before the load's answer and are ignored (no turn is in flight). `session_id` (claude's clear-by-restart id) is ignored: opencode names its own sessions, and its clear is `session/new` on the same process.

### D-N30. Replies are ordered behind the updates before them; a reply never given is the exit's
- **Decision:** every request is sent with `prepare_request(..).on_receiving_result(..)`, so its reply's handler holds the crate's dispatch order (an eager `send_request` may route a fast reply ahead of the `agent_message_chunk`s before it: seen in the tests, the answer came out empty). A reply the crate fails because the engine's output ended (`is_incoming_transport_closed`, `is_connection_terminated`) is dropped, so the turn in flight ends as the exit says ("the engine exited mid-turn: <stderr>"), as claude's does.
- **Residual:** after the output ends, the main task drains what was already heard; a reply whose handler the crate had not yet run is lost to the exit's failure (a turn reported failed that opencode answered as it died).

### Not done (opencode ACP child)
- No caller: the daemon builds no `ChildSpec` yet, so nothing maps the agent's permission posture onto `ChildPermissions`, and the wiring registers no `ChildEngine` for opencode.
- Not verified live: the whole driver runs against a fake `opencode acp` (the spike's lines); `session/load` resume, a `max_tokens`/`refusal` stop, and `request_permission` against real opencode are unprobed.
- The effort level is not passed (no flag verified, as for a turn). `ChildSpec.session_id` is ignored (D-N29).
- `end` still uses `terminate_tree` (SIGTERM then kill) after the grace: by then the turn was cancelled and the input closed, so a server still there is not busy; a tool that ran under `setsid` escapes the group either way (the spike's orphan hazard, unchanged).
