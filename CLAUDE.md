# Introduction

ctxloom is the Rust ctxloom: a port of the Go ctxloom onto angzarr-embedded,
one user journey at a time, with architect review of each milestone. The plan
(`ctxloom-rs-port.plan.md`, §10) orders the milestones by journey
(`crates/root/bin/ctxloom/features/journeys/`); each makes its journey's
scenarios pass, with the cli contracts it touches. A milestone starts as red
tests, committed red on `port/<milestone>`, reviewed, then implemented green.

The repository is `ctxloom` (github.com/ctxloom/ctxloom); the Go ctxloom is
preserved on its `go-final` branch. The primary checkout's leaf is `ctxloom`.

The features are the spec, and are written first: a journey tells a user's
story with the commands they type; a command's contract lives in its
`features/cli/` file. `docs/plans/ctxloom-features.md` ("Specs") and
`docs/reviews/2026-11-06-vocabulary.md` (names) are authoritative, and where
they disagree with older design in the port plan, they win.

The Rust taskloom is a separate repository. ctxloom reaches it as git
dependencies pinned by commit (`harp-names` and the `ports` it implements,
and later `tasks-lib`, linked in process), never by a path and never as a
copy: this repository holds only ctxloom.

# Working rules

## Tests go in `*.test.rs` files beside the code they test

A source file `foo.rs` keeps its tests in `foo.test.rs` in the same
directory, included at the end of `foo.rs`:

```rust
#[cfg(test)]
#[path = "foo.test.rs"]
mod tests;
```

Not inline `mod tests { ... }`, and not a `tests/` directory. The tests stay
beside the code they test, but out of the source file, so reading the code
does not mean reading its tests too.

Acceptance tests drive the real binary: `crates/root/bin/ctxloom/src/*.test.rs`
build it once (with its `test-support` feature, which turns on the hidden
`ctxloom test ...` commands and the `mock` engines) and run it as a separate
process in a temporary home. The `mock` and `mock-*` engines exist only under
`test-support` (`library::config::schema::ENGINES` and the wiring's
`engines()`): a plain build knows claude-code and opencode alone, and `just
build` proves it by running `scripts/plain-engines-check.sh` against the plain
binary. The red tests of a milestone are committed red on its branch
(`just test-commit` allows that off the default branch, never on it).

## Layout: a crate's directory is its role

Every crate lives at `crates/<ring>/<role>/<crate>`. The ring and role are
the architecture; there is no other table of crates.

- `core/wire`: generated prost types.
- `core/domain`: aggregates (decide, apply): the write side.
- `core/read-model`: projectors: the query side.
- `core/ports`: traits the core needs from outside.
- `core/application`: command and query handlers.
- `adapters/driven`: port implementations (stores, clock, git).
- `adapters/driving`: the CLI (and later gRPC, MCP): translation only.
- `root/lib`: the composition root's wiring as a library (`ctxloom-wiring`).
- `root/bin`: the `ctxloom` binary, the composition root.

Slice S0 made `ctxloom-store-sqlite` (driven), `ctxloom-cli` (driving),
`ctxloom-wiring` (wiring) and the `ctxloom` binary. Slice S1 (library read side)
adds:

- `core/read-model/library` (references, bundles, profiles, composite
  selection, the premise index, the layered config);
- `core/ports/ctxloom-ports` (the content and config ports);
- `core/application/library-application` (every library query);
- `adapters/driven/{content-fs, config-files}` and `adapters/driving/library-cli`.

The whole command line is parsed by `ctxloom-cli`; a crate that owns more
nouns joins it through its `Extension` seam (`ctxloom-wiring` wires `library-cli`
in).

Divergence from Go: a config whose `schema_version` is newer, older or unreadable is refused (exit 2, D15 strict decode); Go warns and continues. A `non_tty_format` of yaml, toml or markdown is refused (D28).

Slice S2 (library write side) adds, in the S1 crates, a write half: `library::write`
(pure planning: the config section merge with sorted keys, item/envelope/mcp/
skill/profile files, the scaffold, the loadout, the skill zip codec),
`library-application::write` (create, edit and remove, previews unless `--yes`,
export/import, the editor round trips), `ContentRepo::apply`/`claim`,
`UserFiles`, `Scaffold`, `Editor` and `LoadoutProbe` ports, and the driven
crates `editor-process` and `companion-probe` beside the writes of `content-fs`
and `config-files`. Files are the truth: a command plans the files, checks the
tree still reads, then hands the changes to the port; a refused write has
written nothing. A config write replaces the section it owns whole, never
heals a file that does not parse. `loadout --format yaml` is the one yaml
output; its prose is withheld until the owner rules (D24, D8).
Skill zips are written and read by `library::write::archive` over the `zip`
crate (deflate only, default features off) and crc32fast.

Slice S4 (sessions) built the following. The record is the project's durable
store (owner rulings 87, 87a): a session's state goes through angzarr-embedded
over `~/.ctxloom/projects/<id>/ctxloom.db` (machine-local), every hook's fact
an event when it is delivered. `session list`, `search`, `show` and
`transcript show` catch up from it on each read, so a live session shows what
was delivered so far; nothing projects the events into a file (no
`transcripts/transcript.jsonl`, `records/*.jsonl`, position file or
`metadata.json`). A session's directory, `.ctxloom/sessions/<harp>/` in the
project, holds what its engine needs (the session home, its native history,
its worktree), and `<harp>.lock` beside it is held by its run for its life. A
name is unique because its claim is. Accepted cost: a clone has no session
history (the store is machine-local; `sessions/` was always gitignored), and
the store grows without bound (ruling 90; compaction is taskloom
`untried-slander`).

- `core/wire/sessions-proto`, from `proto/ctxloom/sessions/v1`;
- `core/domain/sessions` (hand-written markup, as taskloom's `tasks`: the
  session aggregate and the `session-names` claim aggregate);
- `core/read-model/session-index` (the rows, state, title, search, retention,
  and each session's transcript folded from its capture events: `Projection`,
  D27's lines, `seq` counting them);
- `core/ports/session-ports` (a project's store, with `Purge`; `Liveness` and
  `SessionLock`; a session's files, Go's session directories);
- `core/application/sessions-application` (list, search, show, keep, remove,
  bind, the transcripts; Go's `session.yaml` schema 1 to 3 read through
  angzarr-yaml; the importer's types);
- `adapters/driven/{liveness, session-files}` and, behind `ProjectStores`,
  `ctxloom-store-sqlite`;
- `adapters/driving/{session-cli, hook-cli}` (`hook` is hidden: `session-bind`
  and `hud`).

A session's state is its lock's verdict, never its end stamp; its title is the
first prompt of its transcript; `hud` renders and records nothing. The
kernel's retry is `angzarr-embedded-runner`'s (the embedded pin). A read catches
up fresh each time; nothing caches a project's index. Rows and refusals say what was not
done and the command that would, in the message, so the json envelope carries
it too. A crate's catalog is `locales/en-US/messages.ftl`, ids the full code.

Names and ids are fixed by the owner's rulings (2026-11-03), and imported
streams are rooted on them, so they never change:

- a project's namespace is `UUIDv5(URL namespace, "ctxloom:project:" + project
  id)` (`sessions::project_namespace`);
- a name's claim stream root is `UUIDv5(project namespace, harp text)`
  (`sessions::claim_root`, domain `session-names`);
- an imported session's stable id is `UUIDv5(project namespace, harp text)`
  (`sessions_application::imported_id`). Go's `run_ends[]` and `end_reason`
  are NOT imported (they return with S10).

A rename is judged by the session on what its lock said (`RenameSession`
carries `LockEvidence`, owner ruling 91, per 73e): a held lock is refused
`SessionLive` (`sessions-session-live`, its fix: wait for the session's end;
an unproven lock does not refuse). Otherwise it claims the new name, moves the
output folder and the session's directory and lock (`SessionFiles::
move_session`, defect 2), records the name and frees the old one, each step
undone if a later one fails; a removed or destroyed session's name stays
claimed. The refusal code of a taken name is `sessions-names-taken` (the
crate-prefix rule).

`session remove` DESTROYS a session (owner rulings 94, 94a-d, 98), and exists
only under the cargo feature `session-destroy` (binary -> `ctxloom-wiring` ->
`session-cli`; `session sweep`, whose apply is that destroy, too). There is
one deleting verb: `session purge` is gone in every build, and a plain build
has no deleting verb at all (accepted cost: it cannot hide a session from the
listings); `scripts/plain-destroy-check.sh`, run by `just build`, holds the
plain binary to knowing none of `remove`, `purge` or `sweep`. The acceptance
tests build with the feature, and `session-cli` dev-depends on itself with
it, so its tests and `--all-targets` lint compile the destroy (no
`feature_rules` entry: the mutation recipe refuses one). A destroy purges,
from the store, the session's stream and each run stream UUIDv5(session,
1..=n), each guarded by the head just read (retried on a conflict), then
removes the session's whole directory (authored files too, a login link never
followed) and its lock; the name's claim is kept. The streams go first, so a
failure removing the directory leaves a session nothing lists. A hook fact
redelivered after it finds no session and records nothing. It is refused, as
before, for a session whose lock does not prove it ended (held, or none)
unless `--even-if-live` (the one override), for a kept one, and within the
transcript retention. A removal whose session holds files a person wrote
(anything but `transcripts/` and `native/`) warns first, naming each, as the
notice `sessions-application-destroys-authored`, never a refusal: on
`--yes`, in the report and with `--even-if-live` alike (owner ruling 104);
`features/cli/session_remove.feature` runs the built verb.
There is no forget-but-keep (`ForgetSession`, `SessionForgotten` and the
`sessions-forgotten` refusal are gone) and no transcript destroyer of its own
(`session transcript purge` is gone): the conversation is the session's
stream. Nothing marks a session purged (owner rulings 100, 102):
`RecordPurge`, `SessionPurged` and `SessionState.purged_at` (field 14,
reserved) are gone, `session import` reads Go's `purged_at` and records
nothing for it, no row or `show` says `purged`, and the sweep passes no
session over as already emptied.

Deliberate divergence from Go: `session remove` takes `--even-if-live`, as its
own refusal text already told the user to use.

Slice M1 (run) adds `ctxloom run`: `--one-shot <prompt>` and the bare interactive
session. A turn is a command. A child agent on claude runs one `claude -p
--output-format stream-json` command per turn (`--resume <native id>` on later
ones); the primary agent keeps the human's terminal, handed to the engine by
inheritance. Both are heard through hooks (a child since owner ruling 51).
Crates:

- `core/ports/run-ports` (`RunnerLauncher`, `Engine`, `SessionRecords`,
  `RunLocks`) and `core/read-model/pending-launches` (the runs still owed a
  runner, with their launcher's lock, and the runs started and not ended,
  with their session's place);
- `core/application/{run-launcher, run-application}`: the launcher is a
  `CatchUp` over `pending-launches` that drives the engine for one turn and
  records the outcome as facts, and sweeps what other processes left (below);
  `Runs` plans the run through the library's dry-run, enqueues, mints,
  launches, returns the reply. Nothing writes the session's directory but the
  launcher's session home (ruling 87a);
- `adapters/driven/host-launcher` (a turn's command through `run_within_started`,
  the interactive launch through `run_terminal`: inherited stdio, the caller's
  process group, no deadline; each on a thread of its own, returning at the
  spawn) and the drive half of
  `engine-claude`; `adapters/driving/run-cli`; and `adapters/driven/hook-tls` (the
per-run listener certificate's mint, see the credentials paragraph below).

An engine's hooks are its adapter's: `ctxloom hook <engine> execute
<NativeHookName>` (hidden) resolves the engine through `surface_ports::Registry`
and its `HookCodec` decodes the payload. The settings an older ctxloom wrote
(`hook <verb> --engine`) are stale and rewritten on the next surface apply.
An engine env has no `HOME`: ctxloom's home reaches its hook processes as
`CTXLOOM_HOME` (`run_ports::HOME_VARIABLE`; the wiring takes it as the home
when `HOME` is absent, and names the resolved home in a run's env; both
allowlists pass it on), and the session home's `settings.json` calls `ctxloom
hook claude-code execute <Event>`. The interactive home registers the session,
turn, message, tool-batch and compaction (`PreCompact`, `PostCompact`) events.
A one-shot CHILD's home (owner ruling 51) registers the per-turn ones only:
`UserPromptSubmit`, `PostToolBatch`, `MessageDisplay`, `Stop`, `PreCompact`,
`PostCompact`; never `SessionStart` or `SessionEnd`, which `claude -p` fires
once per PROCESS (every turn), and whose slow hook claude cancels. The hook
command is the bare `ctxloom`, so an engine's `PATH` (the terminal's and a
child's) is the directory of the running binary (`Place::program_dir`, the
wiring's `current_exe` parent), then the inherited `PATH`. Two consequences:
the session's own shell also sees that directory first, so a sibling program in
it shadows the user's others (intended); and the binary must be named `ctxloom`
for the hooks to resolve it (a renamed binary still leads `PATH` but the bare
name finds nothing there: a known limit, not worked around).
The launcher records `RunnerStarted{runtime, workspace, pid}` AFTER the spawn,
with the pid the spawn gave (`RunnerLauncher::launch` answers a `Running`, the
launcher records, `Running::finish` waits), and the pending launches clear on
it, not on the runner's first word; a command that cannot spawn is a
`LaunchFailed` and never a `RunnerStarted`. An interactive run's terminal
session is ended by the human (the terminal delivers Ctrl-C to the shared
process group, so it cancels a generation inside claude); ctxloom does not
forward SIGINT/SIGTERM yet, since a SIGTERM-only stop needs a platform signal
`Os` does not offer (`HostLauncher::stopped_by(Stop)` is there for it).
A run has THREE credentials, minted at enqueue with the same machinery, each
a 256-bit token whose SHA-256 alone is journaled in `RunEnqueued`
(`runner_`, `model_` and `hook_credential_hash`; the aggregate requires three
distinct digests, and `RunEnded` revokes all three in the fold). The hook
credential exists so the engine's hook processes (the terminal session's, and a
one-shot child's: ruling 27 accepts the child holding it, capture scope, its own
stream only) can call back over the listener without holding the runner's or the model's token: its
scope (`runs-application`'s `Scope`, one value checked in one place, derived
from the run's role in code and journaled nowhere) is today the six capture
facts alone, so widening it later is adding to the scope. What
a hook process receives, through the engine env (like `CTXLOOM_HOME`; both of
`engine-claude`'s allowlists, `ENV_ALLOWLIST` for a child's turn since ruling
51 and `INTERACTIVE_ENV_ALLOWLIST`; always the run's own values: an inherited
one from an enclosing session is replaced, never passed on), with
`CTXLOOM_SESSION_HARP`, the session's harp (for its statusline), and
`CTXLOOM_SESSION`, the session's stable id, what a hook addresses its facts to
(review finding 6: the wiring's `RunIds`; nothing on disk is read; the gate
still holds a hook to its run's session): `CTXLOOM_ENDPOINT`, the URL of the run's TCP
listener, `https://<host>:<port>` (TLS only: `http://` is the typed
`run-ports-endpoint-plaintext` and is never dialed, and port 0, a request to
bind and never a place to dial, is `run-ports-endpoint-port-zero`, for the
builder and the parser both); the port is the system's (the listener
binds port 0 once the run is enqueued and its session minted, and the bound
port is told);
`CTXLOOM_BEARER`, the composite `<run-id>.<64 hex>` (the gate splits it to look
the run up, no upstream metadata needed); `CTXLOOM_RUN`, the run id;
`CTXLOOM_TLS_CERT`, the run's listener certificate, PUBLIC (base64 of the DER on
one line, safe in an environment and in `docker -e`; `run_ports::PublicCert`
encodes, decodes and wraps it as PEM). The URL's build and parse, the
variables, the certificate's codec and the bind table are pure functions in
`run-ports` (`hook.rs`, `tls.rs`, `bind.rs`). The
secret is returned once by `Enqueuer::enqueue` to the launching code, which
puts it in the launch order's env; `Bearer`, `Token` and the `Debug` of
`TurnCommand`, `TurnRequest` and `Order` never render it, and no error carries
it. Credential values never render: the `Debug` of every type that carries an
engine environment (those three, through `run_ports::RedactedEnv`; `Permits`)
prints the variables' NAMES and withholds EVERY value, whatever the name, so
there is no list of sensitive names to keep in sync. Residual, same-user (ruling 37, ACCEPTED: the user boundary is the trust
boundary): the model runs as the user and can read its own engine's
environment, so it holds the bearer and the public certificate and can forge
CAPTURE facts as the hook principal; it can never forge runner lifecycle facts
(the runner and model tokens are not in that env), and the capture scope is why
the hook credential is a third one. The callback is TLS, server-auth only, no
mutual TLS (ruling 36): the per-run certificate authenticates the SERVER to the
child, which pins it as its SOLE trust root (no system roots, no fallback to
plaintext), and the bearer authenticates the child. So the token no longer
crosses loopback in the clear; what reaches the port can still try the token
(a 256-bit secret, valid until the run ends, scoped to the run's own session
stream; a failed attempt says nothing of the token or its digest), and a
process that can bind the port first cannot present the certificate (the key
never leaves the listener's process). The certificate is minted per run
BEFORE the listener is served and the engine spawned, by `hook-tls`'s `Minter`
behind `run_ports::CertMint` (rcgen on ring, ECDSA P-256, a non-CA
`serverAuth` leaf); its window is explicit (rcgen's default starts in 1975):
valid from five minutes before the clock (skew) to 24 hours after it, a cap
that outlasts any run and bounds a leak; its names are every address the
child may dial for the run (`CertNames::for_plan`: 127.0.0.1, ::1, the bind
and the told host, so a runtime's host alias is a DNS name), and the client's
`domain_name` is the URL's host. The private key lives in memory in
`run_ports::HookCertificate` (whose `Debug` withholds it) and goes to
`hook-listener`'s `TlsIdentity` and nowhere else: never journaled, logged, in
an environment or in an error. A hook that finds the listener named but the
certificate missing or garbled refuses (`HookSink::Refused`, `hook-cli-not-recorded`
naming `CTXLOOM_TLS_CERT`) and sends nothing. The listener has no connection
limit, idle or handshake timeout of its own (angzarr's `Listener` offers none:
an escalation); a request is capped at `hook-listener`'s `MAX_REQUEST_BYTES`.
Revisit trigger (ruling 37): ctxloom on a multi-user host with a shared uid,
or an authority wider than capture on this credential (ruling 38).
A one-shot child's model can read the same environment, so it can forge
capture facts for its OWN session (ruling 27's residual, accepted).
The run is driven on a multi-thread runtime (`ctxloom-wiring`'s `run_runtime`,
one worker), because `Running::finish` blocks the driving thread and a task
spawned on a current-thread runtime (the listener) would starve meanwhile.
Every run serves its endpoint, an interactive one and a one-shot alike (a
one-shot is its own owner: ruling 29; children of an interactive owner calling
back into ITS listener are not built): `hook-listener`'s `HookListener` (angzarr's
coordinator `Handle` on a TCP port, TLS only (`Listener::tls`, angzarr's `tls`
feature: tonic's `tls-ring`, never aws-lc), port 0, never an unspecified address, the
arrival declared from the bind (the bind table, `run_ports::bind_for`: the host
runtime binds 127.0.0.1 and is told 127.0.0.1; the container rows of ruling 33
are the tested table and its typed refusals, not yet wired to a container
launch), behind `runs-application`'s `HookGate`) is started, with the run's freshly
minted certificate, after the run is enqueued and its session minted and before
its order is placed (the port and the certificate are part of what the engine is
told; the hook credential's digest is journaled at the enqueue, so the gate
already finds the run) and stopped if the run does not start; otherwise it is stopped, with a bounded grace, after the
engine exits and before the run's end is last recorded
(`ctxloom-wiring`'s `Hooks`; `run-application`'s `serve`, `ready` and `drive`,
one path for both kinds of run). Not built: the hidden runner subcommand.

The locks a run is judged by (owner rulings 85, 86, 92, 93, defects 1 and 3;
advisory file locks through the platform's `file-lock`, taken at once or not
at all, never a pid). The run's OWN lock, `<home>/.ctxloom/runs/<run>.lock`
(`liveness::RunLockFiles`), is taken before its enqueue is visible, journaled
as `RunEnqueued.launcher_lock` (field 12, as in `EnqueueRun`), held in the
`Orders` and let go once `RunnerStarted` is recorded (or the launch failed);
its file is removed as it is let go, so only a dead holder's is ever left (a
refused run leaves nothing in the project). The SESSION's lock,
`<project>/.ctxloom/sessions/<harp>.lock`, is taken once the session is
minted and held until after its engine exits and the listener stops, so a
live session reads live (and `session remove` refuses it without
`--even-if-live`). A launcher's step sweeps other processes' runs: one still
owed a runner whose own lock is free, and one started whose session's lock
is free, have lost their owner; it probes, re-reads the run's stream (an
owner records its start or end before it lets its lock go, so a normal end
wins the race), then records the run's end, keyed by the run (a second
sweeper's is a DUPLICATE): `LaunchOrphaned` (`launch-orphaned:<run>`,
`END_CAUSE_LAUNCH_ORPHANED`) for one owed a runner, since no launch failed
(ruling 100), and `RunnerLost` (`runner-lost:<run>`, `END_CAUSE_RUNNER_LOSS`)
for one started (ruling 95); no orphan claims `LaunchFailed`. Then the
session's incarnation ended (`orphaned`). Every
run's end, or abandon after its mint, tells its session
`RecordIncarnationEnded` (`runner-exit`, `launch-failed`; keyed
`incarnation-ended:<session>:<n>`), so no incarnation stays open; nothing
owes a later incarnation: there is NO relaunch (owner ruling 103: it gets a
planning pass of its own, user-daemon.plan.md / `smashing-embargo`). The session has NO inbox (owner ruling 101): its
mail states (`RecordMessageArrived`, `ClaimMessage`, `DeliverMessage`,
`FailMessage` and their events), `WithdrawSteer`/`SteerWithdrawn`, the
`sessions-unknown-message` and `sessions-already-delivered` refusals, the
runner allowlist's inbox entries, `Feeds::inbox` and the mail-driven
relaunch and its budget are gone (delivery, acknowledgement and failure are
angzarr's transport; mail returns as one command to the recipient session,
the post-MVP daemon's design, taskloom `smashing-embargo`). A mint passes over a harp whose
directory exists, unclaimed, within `NAME_TRIES` (then `sessions-names-taken`). An
agent's `model` and `effort` reach the engine as the order's `model` and
`effort` (`--model`, `--effort` for claude, a turn and the terminal alike;
unset is no flag; claude takes `low|medium|high|xhigh|max`, any other level is
refused before launch).

A run's pre-launch checks are DECIDED by the runs aggregate, in `EnqueueRun`'s
`decide` (owner rulings 73, 73a-73i), on evidence fields the run application
gathers from its ports and puts in the command: `prompt_given` (the plan's),
`engine` and `drivable_engines` (the launcher's), `credential`
(`CredentialEvidence`: a file-only source, the built-in and the accepted
variables by NAME, whether one is set; never a value) and `fatal_findings`
(the plan's count; their text never enters the proto). `RunEnqueued` carries
none of it. The checks, in order: a one-shot's prompt (`runs-no-prompt`), the
engine (`runs-unknown-engine`, both INVALID_ARGUMENT), then a child's
credential (`runs-file-only-credential`, `runs-no-credential`), which an engine
nobody drives is not judged on (73d: such an engine is planned anyway, on the
agent's own engine, so the findings and the prompt still report). Every check
is run: one failure is its own refusal, unchanged; two or more, or one beside
the plan's fatal findings, are ONE `runs-launch-refused` (`LaunchRefused`,
FAILED_PRECONDITION) holding each in check order, said as one json envelope
with `refusals: [{code, args, message, fix?}]`, or in text the summary line then
each refusal's `ctxloom:` and `  fix:` lines (cli-support's `Failure::refusals`
and `say_failure`). Fatal findings alone are the library's
`library-application-startup-aborted`, as before; beside a refusal they are the
plan's warnings, said beside it, and the exit is 3 (the most severe wins),
else 2. The run is enqueued FIRST (73g): plan, evidence, `EnqueueRun`, the
project told, a name claimed, `MintSession`, the certificate, the listener,
the order. So a REFUSED run leaves nothing: no name, no session, no directory,
no event, no open incarnation, no certificate, no listener. A failure after
the enqueue (the project, the names, the mint, the certificate, the listener,
the first catch-up) ends the run with a `LaunchFailed` fact, so no run stays
owed a runner, and a session already minted has its directory written with
the run's end in it.

`crates/core/clippy.toml` holds the core's purity rules (no I/O, clock, env,
threads or hash-ordered collections) and reaches every core crate, because
clippy reads the nearest `clippy.toml`. A crate's own `clippy.toml` would
replace it, so none has one. It repeats the root `clippy.toml`'s settings,
and `just lint` refuses to run if they disagree.

The engine block reaches the drive (engines track 4). The plan's `DryRun.drive`
(`library::config::engines::Drive`: `command`, `session_home`, the `auth`
source, the agent's `env`) becomes `run_ports::EngineSettings`
(owned, names and paths only, never a credential value) in the `Order` and the
`TurnRequest`. engine-claude runs the configured `command` (default `claude`);
passes the curated allowlist plus `engines.<e>.env`, the configured auth
variables: a bounded, enumerable set the container runtime will read. There is
no `env_host` (owner ruling: no mode passes the host environment whole, and no
boolean config keys; `env_host` is an ordinary unknown key, no alias). The
`ANTHROPIC_AUTH_TOKEN` stays out of a child whatever the config says, unless
a permit names it (the `CTXLOOM_*` hook variables are a child's too since
ruling 51, always the run's own). The escape hatch is `CTXLOOM_PERMIT_<NAME>`
(owner rulings 78, 78a): `CTXLOOM_PERMIT_<NAME>=v` in the caller's
environment reaches the engine as `<NAME>=v`, every engine, a child's turn
and the terminal alike, over whatever the allowlist, a block (opencode's
`OPENCODE_*`) or the config said; it is applied in one place, the launcher,
after the engine built the command (`run_ports::apply_permits`). The
`CTXLOOM_PERMIT_*` variable is never passed on; a permit for a `CTXLOOM_*`
name is ignored and the run says so as a notice (by the permit's name, never
its value); no value renders in a `Debug` (every environment value is
withheld, see the credentials paragraph). An explicit `CTXLOOM_PERMIT_ANTHROPIC_AUTH_TOKEN` does give a
child the primary's token (78a): the cost is that such a child holds the
primary's own token, no distinct per-child credential. A permit never
replaces what ctxloom itself sets for the engine (ruling 78b: a DENYLIST
DERIVED from what was set, never an allowlist): `run_ports::owned_names` is
every name in the engine's command whose value is not the caller's as passed
through (what the adapter computed: `HOME` and the XDG bases relocated,
`CLAUDE_CONFIG_DIR`, `OPENCODE_CONFIG_DIR`, ...) plus the order's `owned`,
the names the run set before the engine saw the environment (`HOME`,
ctxloom's own home, which its hook processes' wiring reads first, and the
`PATH` it builds with the program's directory first); a denied permit is
ignored with the same notice (said from the launch, `Launched::ignored`). A
name the adapter merely passes through (`LANG`) stays permittable, as does an
XDG base under `session_home: host` (passed through, not relocated). Claude
stays token-only on Windows (ruling 82); `session_home: host` sets `CLAUDE_CONFIG_DIR`
to `$HOME/.claude` (no `HOME` is `run-ports-engine-no-host-home`), links no
login there; the hooks still fire, because the launch (the terminal's and a
child's turn) passes `--settings <session dir>/settings.json` (the ctxloom-owned file `prepare` writes,
in both home modes, never into the user's `~/.claude`; claude adds its hooks to the
config dir's own, and honours one `--settings`; no posture settings are passed
yet, so there was nothing to merge; verified with a fake program only, not a
live interactive TUI, nor with `--setting-sources user`; in a container the flag
must name the in-container path, a follow-up). A child's turn passes the same
single `--settings` (ruling 51; verified with a fake program only, not a live
`claude -p`). `EngineSettings::credentials` meets the
engine's `Credentials` with the source: a child is satisfied by the source's
`env` alone, the primary may share the source's `file` (or, for the built-in
source, the engine's own login: claude's `~/.claude/.credentials.json`,
opencode's `auth.json`), `none` and `login` ask a child for no variable. Where
the login is is the engine's `Credentials` (`user_dir`, `user_dir_variable`,
`login_file`, `home_dir`: the link is `home/<home_dir>/<login_file>` of the
session dir), and so is what the human is told when none is shared
(`sign_in`, the engine's own words), whether a missing one is worth saying
(`login_optional`), where it is never linked (`no_link_on`: claude's macOS
keychain and Windows' symlink privilege) and where a link that cannot be made
is a copy (`copy_on`: opencode's Windows, ruling 80; `LoginLinks::copy`, a
new 0600 file). The platform is the OS name the caller gives; macOS and
Windows are unverified live. Settings apply to the engine the plan resolved, and `run --engine
X` asks the plan for X (its engine block drives it). A launch whose agent `runtime` weakens
the engine's (or swaps container ownerships) says so as a notice. `init`'s
token guide asks the configured source.

## Architecture: hexagonal around CQRS/ES (checked by `just lint-arch`, and in review)

Dependencies point inward; a review refuses a change that breaks these, and
`just lint-arch` (part of `just lint`) holds every edge between workspace
crates to `arch-rules.toml`: a table of which role (`crates/<ring>/<role>`)
may depend on which, with the known departures listed as exceptions, each with
its reason and a fix (debt; a stale one fails). The lint is ctxloom-lib's
`arch-lint`, installed at the pinned commit into `~/.cache/ctxloom-arch-lint`.
A dev-dependency finding is a warning.

The rules:

- The core depends on nothing outside it: no I/O, async runtime or clock in
  decide, apply or a projection.
  Pure computation crates are allowed: `surface-ports` links serde_json (with
  `arbitrary_precision`, so a number keeps the digits it was written in) for
  its `Json` value's reader and writer.
- A driven adapter sees only ports (and wire types), never a use case. A
  driving adapter sees only the application layer, never a domain's
  internals. Adapters never depend on each other, except that driving
  adapters share angzarr core's `angzarr-presentation`, and a driven adapter
  may use the platform crates (`os`, `file-system`, `file-lock`).
- A domain never depends on a read model; a read model never depends on the
  application layer.
- Entities are aggregates; anything listed is a projector's read model,
  never an aggregate. There is no commit position, fork point or global
  position anywhere (a test greps the sources for them).

## A refusal is a typed message; its status lives in the proto alone

A command refuses with a typed refusal MESSAGE of ctxloom's own protos
(`proto/ctxloom/...`, and the scheduler's under `proto/io/angzarr/scheduler`),
one per rejection, listed in the `refuses` of the commands that raise it, each
with a `(refusal_status)` MESSAGE option (no default; the standard gRPC
meaning: NOT_FOUND for a missing thing, ALREADY_EXISTS for a taken name,
INVALID_ARGUMENT for a bad argument, FAILED_PRECONDITION when state forbids,
PERMISSION_DENIED when the requester may not). That option is the only place a
status is spelled: no Rust source holds a status constant, table, default or
`with_status` call (a test greps `crates/**/*.rs` for them); the one way Rust
names it is the generated `REFUSAL_STATUS` constant the `refuse!` macro of a
domain's `refusal.rs` reads. What a person is told (the crate-prefixed code,
its args, the en-US message) is the domain's `*Rejection` diagnostic, mapped
back from the refusal's TYPE by `TryFrom<&TypedRefusal>` and by nothing else,
never from its status: `sessions-application`'s `typed::run` for the sessions
commands, `runs-application`'s `AppError::from_failure` for the edge.

A fact is keyed by its `Fact.origin.external_id` (`FactOrigin`): the
coordinator derives the fact's command id from it, so a redelivery to one
stream is a DUPLICATE; the event it produces records the origin on its
`Causation::Fact`. A fact the kernel dead-letters is answered
`ANGZARR_REASON_FACT_DEAD_LETTERED`, which the runs application reports as
`Recorded::Parked` (final, never retried).

## The pins: one of each

The lock holds ONE `angzarr-embedded`, `angzarr-proto`, `angzarr-ports` and
`angzarr-diagnostic` (a test reads Cargo.lock). Every pin in `Cargo.toml` is
a full commit. The angzarr pins are one line, one per family member:

- angzarr core `35b29dea` (`angzarr-diagnostic`, `angzarr-yaml`,
  `angzarr-presentation`; `angzarr-coordinator-grpc`'s `tls` feature, which
  `hook-listener` enables, is `Listener::tls`);
- angzarr-embedded `ba73fabe` on angzarr-router `bc729edc`;
- angzarr-build `9e9eff01` (the markup generator, `angzarr-build` and
  `angzarr-codegen`, its own repository);
- angzarr-platform `dfae6726`;
- taskloom's crates `efc28291` and ctxloom-lib's `3b687062`, built on the
  same line,

so moving one moves all. The lock holds one of every angzarr crate. The platform crates (`os`, `file-system`, `file-lock`) are
angzarr-platform's, and `angzarr-presentation` and `angzarr-diagnostic` are
angzarr core's, all `Apache-2.0 OR BSD-3-Clause`; this workspace is
`AGPL-3.0-only`.

The callback's TLS stack is one crypto provider, ring (owner, ruling 35):
tonic's `tls-ring` (never `tls-aws-lc`; `hook-listener` and `hook-sink-grpc`
enable it, and `hook-listener` turns on `angzarr-coordinator-grpc`'s `tls`
feature) and `rcgen` (production dependency of `hook-tls`, default features
off: `crypto`, `pem`, `ring`). They bring rustls, ring, rustls-webpki and
their small dependencies; `ISC` (ring, untrusted, rustls-webpki) is in
`deny.toml`'s allow list for them, as MIT and BSD are. No aws-lc is to enter
the lock: rustls with two providers and none installed panics at config build.

The driving edge's shared shims (`cli-support`: `Call<G>`, `Failure` with its
`refusals`, the notices printers, `report_failure`, `say_failure` and its json
`Reported`, the argument readers) are
ctxloom-lib's (`github.com/ctxloom/ctxloom-lib`), a git dependency pinned by
full commit over https. Its angzarr pins are this workspace's, and the lock
holds one `cli-support` (the same test). The application's own global flags
(`library_application::Flags`) ride in the call as `Call<Flags>`'s `globals`;
ctxloom-lib itself knows no ctxloom word (its vocabulary check).

Every adapter that runs a program (`remote-git`, `worktree-git`,
`companion-probe`, `container-runtime`, `taskloom-cli-client`) runs it
through ctxloom-lib's `run-within`: the program under a deadline, its whole
process tree killed when the deadline passes (not only the program: a git
hook or a shell's `sleep` would otherwise live on holding the pipes), its
output bounded per stream, and the wait for that output bounded too
(`drain_grace`, two seconds), so a descendant that outlives a program that
exited cannot hang the caller. The program's environment is exactly the one
the adapter gives; its tests need `run-within`'s `test-support`, which waits
out ETXTBSY. Limits (os::process's): a descendant that calls `setsid` or
`setpgid` escapes the kill, and the program runs in a process group of its
own, so a Ctrl-C at the terminal does not reach it: a caller that must stop
a run on Ctrl-C hears `Os::stop_signal` and ends the tree itself.
`run_within_started` also calls a `started(pid)` once after a successful spawn
(never on a spawn failure). `run_terminal(spec, &Stop, started)` is the mode
for a program that owns the terminal: inherited stdio, NOT a process group of
its own (a Ctrl-C reaches it), no deadline, and a `Stop` (a cloneable flag the
caller sets, e.g. from `Os::stop_signal`) that kills only the DIRECT child, not
its descendants. Tested on unix; macOS and Windows are not. No adapter bypasses
`run-within` any more.
All of ctxloom-lib's crates are pinned at one rev (`cli-support`,
`process-env`, `project-dir`, `run-within`), and so is each family above.

## Co-developing a sibling or ctxloom-lib

In development the whole git dependency family (angzarr, -router, -embedded,
-platform, -build, harp, taskloom, ctxloom-lib) is used by path. `just
dev-local [root]` generates the gitignored `.cargo/config.toml`: a `[patch]`
per git source in Cargo.lock, every crate it takes from there mapped to its
absolute path in the sibling checkout under `root` (default `~/workspace`;
`DEV_LOCAL_<REPO>`, e.g. `DEV_LOCAL_ANGZARR`, `DEV_LOCAL_CTXLOOM_LIB`, names a
checkout; one at the lock's rev is preferred; a crate not found fails and
names it). `just dev-local-off` removes it (only a file it generated) and
restores Cargo.lock. Nothing path-based is ever committed: the pre-commit
gate refuses an outside-repo path dependency and a tracked
`.cargo/config.toml`, and the gates build the staged tree without the patch.
Under the patch Cargo.lock loses its git sources, so the two lock tests in
`workspace.test.rs` that read sources (`test_every_git_pinned_crate_is_in_the_lock_once`
and `test_the_git_pins_of_the_one_of_each_crates_are_the_pins_of_one_repository_each`)
skip, printing `SKIP <test>: <reason>`, only while `.cargo/config.toml` exists
and its first line is exactly `# generated by just dev-local`; with no config,
or one without that line, they assert as ever, and the commit gates (which
build the staged tree without the patch) run them. Re-pin once per landing: push the sibling (ctxloom-lib's gates and
mutation first), bump the revs in Cargo.toml, `just dev-local-off`.

## Storage is angzarr-store-sqlite

A project's store is angzarr-store-sqlite's `SqliteStore` (through
angzarr-embedded's `sqlite` feature), opened at
`~/.ctxloom/projects/<project-id>/ctxloom.db` by `ctxloom-store-sqlite` (the
file name, the placement and the port, over ctxloom-lib's `sqlite-store-opener`,
which refuses a store from before the embedded store, `ctxloom.db.pre-embedded`
naming its backup), beside taskloom's `state.db` and never the same file (D26). All file I/O goes
through the `file-system` seam, so a test under a temporary home touches
nothing else. ctxloom writes no SQL of its own.

## A test's fake program is `fixture_program::install_program`

A test that needs a program on a path (an editor, a `git`, a runtime, a
companion) calls ctxloom-lib's `fixture_program::install_program(fs, path,
body)` (a dev-dependency) and runs what it returns. Writing a script with an
executable mode and exec'ing it is racy in a multithreaded test process (a
thread's fork holds a copy of the writer: "text file busy", ETXTBSY); the
program is instead a link to a committed shim that `sh` runs the script
beside it through, so no file the test wrote is exec'd. No crate keeps a
helper of its own for this (a thin wrapper that only shapes the call is fine).
`just lint-fixtures` (`scripts/exec-mode-check.sh`, part of `just lint`) fails
a test line that gives a file an executable mode, unless
`scripts/exec-mode-allow.txt` lists it with why (a mode that is data: the bit
git commits, a listing reports, a PATH search wants). Its self-test is
`scripts/exec-mode-check.selftest.sh` (`just selftest`). The script and the
`gates/fixture-gates.just` recipe are copies of ctxloom-lib's.

## Errors are localizable: a code, args, and an en-US message

Every error implements `angzarr_diagnostic::Diagnostic`: a stable code
prefixed with the crate's name (`code!`), named args, and an en-US message in
`locales/en-US/messages.ftl` whose id is the full code and which uses exactly
the args. An id ending `-fix` is the remedy of the code before it, printed as
`  fix: ...` in text. A third party's text (the OS's, SQLite's) is the arg
`ext_err_txt`, verbatim. Each crate with diagnostics exports `EN_US` (its name
and its `.ftl`) and, under `test-support`, `diagnostic_samples()`; the
binary's `catalog()` lists every crate's `EN_US`.

## The command line

`--format` is `json` or `text` (json off a terminal); yaml, toml and markdown
are refused like any unknown format, exit 2. Errors are `ctxloom: <message>`
with an optional `  fix:` line in text, and the envelope `{code, args,
message}` on stderr in json, plus `fix` (the same text as the `  fix:` line)
when the catalog has the error's `<code>-fix` id, the key omitted otherwise,
and `refusals`, each refusal an aggregate refusal holds reported the same way,
in order, the key omitted when there are none (cli-support's `Reported`, which
`ctxloom-cli`'s `report` writes through `say_failure`; angzarr-presentation's
`Envelope` has neither field). Exit codes: 0, 1 failed, 2 refused, 3 fatal
findings. `-h` works at every depth, there is no `help` command, an unknown
verb says "Did you mean" and lists the verbs, `-v` is accepted everywhere
and changes no output.

## Remotes and dependencies (slice S3)

Crates: `deps` and `pin-changes` (core/read-model: addresses, references,
registry, lock, constraints, the pull/upgrade/reconcile decisions, the pin
disclosure), `deps-ports` (`Remotes`, `DepsFiles`, `RemoteLibrary`),
`deps-application`, `remote-git` (the git CLI with `GIT_*` scrubbed, clones
under `.ctxloom/cache/repos`, read from commits only, never a checkout),
`deps-files` (remotes.yaml, lock.yaml and the installed trees under
`.ctxloom/cache/bundles/<segments>/<bundle>/<sha>/`; it stands where the plan
named `content-fs` for these files), `deps-cli`. `ctxloom-wiring`'s `Commands`
composes them with the library: it merges `bundle push|move` into the
library's `bundle` noun and owns `remote` and `deps`.

- Evidence rule: content is removed upstream only on a not-found from a
  repository proven readable this run; an unreachable remote keeps everything.
- Lock schema 3 has no timestamps and rewrites byte-identically at unchanged
  pins; a corrupt lock is refused, never overwritten; an empty lock never
  replaces a populated one. Registry schema 1: unstamped is refused
  ("schema_version 0"), unknown keys survive a rewrite.
- Installed remote bundles join the library's catalog as `<remote>/<bundle>`
  and by canonical reference (`Catalog::origins`).
- Go parity kept: `.git` stays in canonical references; remote profiles may
  name only their own repository. Divergences: the default is cleared when
  its remote is removed (Go keeps it); an unstamped registry is refused;
  `materialize` is `context assemble`.
- Not built (no crate admitted): semver ranges (exact versions only), the
  forge API (`forge-github`, `remote discover`, `bundle push --pr`). The
  withheld red tests for them are `#[ignore]`d in `bundle_remote.test.rs`.
  angzarr-yaml cannot read Go's `? key` form for lock keys over 128 bytes.

## Hook capture: the transcript is the session's events

What the engine's hooks observe (a prompt, each MessageDisplay delta, a tool
call as size and SHA-256 only, a turn's end without text, a session's end, a
compaction) is a typed fact of the SESSION stream (`proto/ctxloom/sessions/v1/
capture.proto`; `runs-application::observe` makes them from the unified
`HookEvent`, `Capture` delivers them through a `run-ports::FactSink`, failing
open: a delivery that did not happen is dropped and said on stderr). The
session aggregate records each on a live session, nothing on any other, and a
redelivery is DUPLICATE. No file holds the transcript (owner ruling 87a):
`session-index` folds it from the stream's pages on each read (a
`Projection`, D27's envelope `v, harp, session_id, engine, seq, ts, kind`,
`seq` counting lines), so it is live: a session still running shows what its
hooks have delivered. A one-shot child's prompt and words come from its hooks ALONE (owner
rulings 26 and 51): the launcher records none of them, and reads the child's
stream only for the reply it returns and the run's outcome facts (RunnerStarted,
TurnCompleted/TurnFailed, LaunchFailed, RunExited). Accepted costs: a hook
delivery that is dropped loses that part of the record, and a child whose hooks
never reach the listener has no conversation in its transcript (its reply is
still printed); a failing tool has no status from the hooks (claude gives no
PostToolUse and no error flag; the stream's `is_error` is not recorded); every
`Stop` hook adds its runtime to the turn. The terminal session's opening prompt
(given on its command line) is its hooks' to record too (`UserPromptSubmit`,
under claude's prompt id; ruling 26's principle): the launcher records no
prompt for any run, only the run's own facts (a j001000 scenario proves it
lands once). A
reader coalesces deltas by `message_id` and `index`. ARRIVAL ORDER IS NOT
TURN ORDER: Stop and MessageDisplay are separate hook processes, and Stop
fires first (a live probe saw `turn_end` before the turn's words, and the
words can follow the next prompt), so a reader never assumes a turn's words
precede its `turn_end` or the next entry. `session transcript show` places an
assistant message by its `prompt_id` (the user entries are the prompts in
order, the prompt ids in order of first appearance are the same prompts; an
`entry` line carries no `prompt_id` of its own, which an additive field would
make exact). The listener cannot treat `turn_end` as "the words are in".
Compaction (`PreCompact`, `PostCompact`) is registered and recorded: the fact
holds the prompt, the phase, the `trigger` (`manual`/`auto`) and the summary's
size and SHA-256 (never its text, ruling B). Not captured: thinking
blocks, a tool's status (Claude's payload carries none) and a result's body.
A hook is a separate process and delivers through the run's listener:
`ctxloom-wiring`'s `HookSink` is `hook-sink-grpc`'s `GrpcSink` to
`CTXLOOM_ENDPOINT` with `CTXLOOM_BEARER`, pinning `CTXLOOM_TLS_CERT` as its only
root, when the endpoint and bearer are set and well formed (a missing or
garbled certificate is `HookSink::Refused`, never plaintext), and
fails open otherwise (and on any undelivered fact): it answers the engine as
usual and warns `hook-cli-not-recorded` for each observation. The `Unreachable`
sink is that fallback; the in-process `EdgeSink` is for tests and the run's own
facts. The session's end (`SessionEnd`) is captured like the rest.
A compaction's external id is `hook:compact:<session>:<prompt_id>:<trigger>:
<phase>`, so two compactions of one turn (a manual and an auto) are two facts;
two of the SAME trigger in one turn would still collide (the payload offers no
further discriminator). The listener is a port, not a file: a run
killed by SIGHUP or a timeout leaves nothing behind (ctxloom does not forward
or handle SIGINT, SIGHUP or SIGTERM itself).

## No path dependency leaves the repository

A tracked `Cargo.toml` or `.cargo/*.toml` never holds a dependency, `[patch]`
or `[replace]` `path` that is absolute or climbs out of the repository root,
and `.cargo/config.toml` is never tracked: local sibling checkouts live only
in the gitignored `.cargo/config.toml`. `scripts/path-deps-check.sh` (`just
lint-path-deps`, part of `just lint`, and lefthook's pre-commit `path-deps`
gate, third in order) judges the staged index; its self-test is in `just
selftest`.

## opencode is heard through its plugin (ruling 58)

`adapters/driven/engine-opencode` is opencode's adapter (registered in the
wiring's `engines()` and driven by the launcher, always: it is a real engine).
opencode has no hook commands; its hooks are plugins in its own process. Every
session home (`OPENCODE_CONFIG_DIR`) gets `plugins/ctxloom.js` (`PLUGIN`, node
builtins only), which forwards opencode's bus events (v2 `{id,type,properties}`)
and the `chat.message` trigger, serially and in order, by spawning `ctxloom hook
opencode execute <Event>` with the event on stdin, plus what it alone knows
under `ctxloom` (the prompt a step message answers, a compaction's trigger and
summary text). Its queue and dedupe live on `globalThis` (opencode may
instantiate a plugin twice in one process), it drops sub-agent sessions (a
`parentID`) and reasoning, runs each hook without opencode's `HOME` (so
`CTXLOOM_HOME` is the home) and with stderr to `<home>/ctxloom-hooks.log`
(never the TUI's terminal), and drains in `dispose` (5 s cap; `run` and the TUI
await it, probe P4/P5). The codec maps: prompt = `chat.message` by
`output.message.id`; words = a text part WHOLE at `time.end` (one fact per part,
message id = part id, no per-delta capture); tool = a part `completed|error` by
its part id, with its status (the proto's optional `ToolCallStatus`; the
transcript's tool line carries `status` only when known, claude's never); turn
end = the completed assistant message whose finish is not `tool-calls` (or
errored), never a `summary: true` one, and `session.idle` for the last prompt
(a refused-tool turn only idles; the second end is a DUPLICATE); compaction =
the compaction part (before; `auto` -> trigger) and the summary message
(after, size and SHA-256 only), keyed by the compaction's own message.
`observe` matches the unified kinds only (`compaction_before|after`; claude's
codec maps `PreCompact|PostCompact` onto them). The session's end is recorded
by the LAUNCHER at the TUI's exit (`Engine::session_ends_with_exit`; reason
`exit`, no native id, so a second run of the same session would collide).
A child turn is `opencode run --format json [--session <id>] [-m <model>]` with
the prompt on STDIN (an argv prompt with a space reaches the model quoted, and
`run` reads stdin to EOF); the stream is read for the reply (the final step's
text; a turn a refused tool ended is answered) and the outcome only. The TUI is
`opencode [-m] [--session] [--prompt <text>]`. Both run relocated: `HOME`,
every XDG base and `OPENCODE_CONFIG_DIR` in the session home, the npm pre-seed
(`node_modules/` and a `package-lock.json` naming `@opencode-ai/plugin`) in the
home and in `xdg/config/opencode`, no `OPENCODE_*` or XDG variable of the
caller's (`OPENCODE_PURE` would skip the plugin); `session_home: host` keeps
the user's dirs and still adds the session home as a config dir. Auth mirrors
claude's (owner ruling 69): opencode's login is `auth.json` in its data
directory (`$XDG_DATA_HOME/opencode` when that is absolute, else
`~/.local/share/opencode`), which an OAuth provider's refresh rewrites in
place, so the PRIMARY's relocated data directory gets a SYMLINK to the user's
file (`home/xdg/data/opencode/auth.json`), on macOS too (ruling 80), whatever
variable is set (her login may hold other providers); on Windows, when no
symlink can be made, a COPY (ruling 80; the cost, said in a notice: a token
the session refreshes is written to the copy, and the rotation can sign her
own login out); none there is said nothing of
(opencode runs keyless), a configured file missing is. A child is never given
the file, only variables: its built-in source (`token`) is the provider keys
`ANTHROPIC_API_KEY` and `OPENAI_API_KEY`, each optional and on both
allowlists (Go's `CTXLOOM_OPENCODE_API_KEY` is removed, ruling 79, hard
migrate; another provider's key is the config's `env`, or a permit for one
the `OPENCODE_*` block withholds, `CTXLOOM_PERMIT_OPENCODE_API_KEY`); no child
is refused for want of one, and a file-only source refuses an opencode child as it does
claude's. The bundle (1.18.4, read not run) writes `auth.json` with
`writeFile` and `chmod`, no rename, so a refresh writes through the link: not
verified live. Accepted residual: a primary and a human-run opencode
refreshing at the same instant may race (no cross-process lock; as claude).
An effort level is not passed (no flag verified). At rest
opencode carries its context alone (an `AGENTS.md` section). Costs: no
per-delta capture; one hook process per forwarded event; the plugin's own
logic is tested under node (`home.test.rs`, j001000), so those tests need node
on `PATH`. Not verified live: the pre-seed's `.ctxloom` marker file in
`node_modules/`, `--prompt`/`-m` on the TUI, `AGENTS.md` read from the config
dir, sub-agent sessions, auto compaction, opencode reading (and refreshing
through) the linked `auth.json`.

## Engines schema (track 1 landed on `engines/core`)

`engines:` holds, per fixed engine name, what its agents share: `command`
(home-layer config only), `session_home` (`session` | `host`; `host` is not
honoured from a project config until a trust check can gate it), `auth`,
`runtime`, `env`. `auth` is THE auth setting: `none`, `login` (the
human's own login shared in place), `token` (the engine's built-in variables),
or a source `{env: NAME|[NAMES], file: PATH}`; a name that is not a variable
name is refused without being repeated; a file is only a primary session's.
The top-level `auth:` key, `llm`, `agents.<n>.llm` and `agents.<n>.engine_home`
are ordinary unknown keys (hard migrate: no alias, no reader, no migration). The same goes for a bundle document in an older shape (`prompts:`, an item's `llm:`): the generation-0 to 3 reader is gone, only a newer `schema_version` is named, and the strict decode refuses the old keys.
An agent's `engine`, `model`, `effort` and `permissions` are its own, and
`library::config::engines::effective` answers each value with the layer that
said it (agent > engine > project > default; a list replaces, except `env`,
which MERGES, ruling 66: the engine block's names, then the agent's own added
key by key, each name with its layer in `env_keys` / the JSON's
`env.keys`; an agent can no longer narrow the engine's list; an agent's
runtime is used exactly and `Override::Replaced` says what it weakened).
Per-engine blocks (owner ruling 57): `agents.<n>.engine` is the agent's
default engine and `agents.<n>.engines.<e>: {model, effort, permissions}`
holds what is in engine `e`'s vocabulary (`permissions` is the block that was
`agents.<n>.permissions.<e>`, `{mode: ...}`); `runtime`, `env`, `profiles` and
the posture's engine-neutral keys (`permissions.approver`, ...) stay on the
agent. Hard migrate: flat `agents.<n>.model`/`effort` and an engine-keyed
`agents.<n>.permissions.<e>` are unknown keys. `effective_on(config, agent,
X)` is `run --engine X`: the agent's block for X, else X's defaults (no flag),
its runtime kept, its env merged with X's (as on its own engine), X's engine
block for command/home/auth.
`agent create|edit --model/--effort/--permissions` write the block of
`--engine` when given, else of the agent's engine (else the default engine;
a model or effort written there for an agent that names no engine says so in
the notice `notice-default-engine-block`, naming `agents.<n>.engines.<e>`,
ruling 68; `--permissions` with no engine anywhere is still refused).
`engine show X` lists the agents bound to X and those with a block for X; a
dry run's JSON carries `settings` (each value with its source), and its text
a Settings section naming the place that said each (ruling 72:
`agents.<a>.engines.<e>`, `agents.<a>`, `engines.<e>`, the top-level
`runtime`, or `default`). `agent list`,
`agent show`'s definition and the create/edit summary show the agent in the
config's shape (ruling 67): `engines` (JSON) / `engines.<e>: {...}` lines
(text, YAML flow form, keys sorted as written), `permissions` the neutral
posture alone; no flat model/effort.
`engines.<e>.command`, `auth.file` and `session_home: host` in a project
layer are taken out with a warning (`strip_project_only`, ruling 46).
`auth status` and doctor read the effective source (`auth-application`; any
one variable authenticates, `via` names it). A source that is a FILE alone
(owner ruling 53) authenticates no child: a child launch (`claude -p`,
`opencode run`) on it is the typed `runs-file-only-credential` (proto
`FileOnlyCredential`, its fix names the built-in variables to put in
`engines.<e>.auth.env` and export), decided by the run's enqueue (ruling 73e)
before a name is claimed or anything minted, as `runs-no-credential` is too; `auth status` gives it a
`file` row (`usable_by: primary`, `via` the path, and no `present`: nothing
looks at the file, ruling 65, so doctor cannot report a missing one) and
doctor a `warn`, both in the refusal's words (ruling 81: the row's `message`
and `remedy`, doctor's detail and fix, are `runs-file-only-credential` and its
fix rendered from the runs catalog, auth-application's `InRunsWords`). The primary session is unchanged. init's token guide
(`agent_token_exported`) counts a file alone as nothing to guide; instead init
warns of each engine in use on a file alone (ruling 64, `Auth::file_only`) in
the words of the refusal itself (`runs-file-only-credential` and its fix,
through auth-application's `FileOnly`), exit 0 as ever.

## Use `just`

`just --list` shows the public recipes by group. The checks are `just build`,
`just test` (cargo-nextest, required: a missing nextest fails the gate),
`just lint` (clippy, warnings denied), `just lint-deps` (cargo-deny) and
`just lint-os`; `just test-pkg <crate>...` is the quick loop, `just fmt` and
`just fmt-check` cover this workspace's crates only, `just run` runs the
binary, `just clean-target` is the only way to drop `target/`, and `just
selftest` checks the scripts. Raw `cargo build|test|clippy|fmt|deny|clean|
run|mutants`, `nextest`, `rustc`, `lefthook run`, `gitleaks`, and any
`--no-verify` or `LEFTHOOK=`/`SKIP=` skip of the hooks are blocked by ltk
(`.ltk/config.yaml`, checked by `scripts/ltk-rules-check.sh`); the recipe is
named in the block message. Every gate is terse, because its output lands in
an agent's context: a pass prints one `ok <gate> (<scope>) <duration>` line
and nothing else; a failure prints `FAIL <gate>: <summary> (full log:
target/logs/<gate>.log)` and at most 40 lines of extract (only failing tests,
the first 20 diagnostics, survivors capped, each with a `+K more` tail).
`scripts/output-budget.selftest.sh` holds that budget. Read the log for the
rest; `VERBOSE=1 just <gate>` shows the full output.

## The pre-commit gates are in gates/; the justfile gives their parameters

The gates and their recipes are `gates/gates.just` and
`gates/lefthook.yml`: the justfile imports the one and lefthook.yml extends
the other. The justfile keeps ctxloom's parameters for them
(`mutation_thresholds`, `workspace_wide`, `staged_tree`, `feature_rules`,
`features_prepare`) and its own recipes (`build`, `lint`, `test-pkg`, `run`,
`clean-target`, `mutants-batch`, `closeout`, `selftest`); their tool
configs are `deny.toml`, `.gitleaks.toml` and `.cargo/mutants.toml`. `just
hooks-install` installs the hooks, once per clone (every worktree shares
them), and again after a change to the gates names a new hook
(`gates/MIGRATING.md`). They came from the shared gates repository that
taskloom and annals also copied; a change here does not reach them.

lefthook runs the gates one at a time, stopping at the first failure, and
shows each gate's pass line and any failure's extract. Gates 1 to 5 run in
pre-commit (the private recipes `fmt-staged`, `secrets-scan-staged`,
`test-commit`, ...; `just --list` hides them, they run by name). Mutation (6)
runs in no git hook and is not run by hand: the `closeout` skill runs `just
closeout`, whose `mutants-batch` mutation-tests the whole batch (base to HEAD,
the base defaulting to the merge-base with `main`) in a throwaway worktree, in
the background of the recipe's own session, and prints the terse per-tier
verdict. It is REQUIRED before a batch of work reaches the default branch; no
hook enforces it (a fast-forward of `main` runs none), so merging without it
is how 280 survivors reached `main` once (2026-11-04). Nothing runs
cargo-mutants but the `mutants-staged` gate recipe, which `mutants-batch`
drives. A failing gate is a finding: fix the cause and commit again; never
`--no-verify`, and never weaken a gate to get through.

1. **fmt** formats the staged `*.rs` files in the index (`just
   fmt-staged`) rather than refusing an unformatted commit. A fully staged
   file's working copy gets the same content; a partially staged file's
   working copy is left exactly as it was, and named (`partially staged:
   <file> is committed formatted; its working copy is left as it was`).
   `just fmt-check` is the check for CI and manual runs.
2. **secrets**: gitleaks over the staged changes (`.gitleaks.toml`). A fake
   secret in a test ends its line with `gitleaks:allow`.
3. **clippy**: `just lint`, including the core's purity rules
   (`crates/core/clippy.toml`).
4. **deps**: `just lint-deps`, cargo-deny (`deny.toml`).
5. **tests**: `just test-staged` tests the changed crates and their
   dependents in the staged tree (HEAD plus the staged diff, a sibling
   worktree of the checkout); a staged `workspace_wide` file tests the whole
   workspace.
6. **mutants** (no hook; at close-out, `just mutants-batch`): `mutants-staged` mutation-tests the staged diff
   (`cargo mutants --in-diff`), holding new logic to its tier: every
   hand-written tier (domain, read models, ports, application and adapters)
   90% or more; the composition root is listed but not held (the justfile's
   `mutation_thresholds` has every tier); the generated wire code is
   excluded (`.cargo/mutants.toml`). Each mutant runs only its own crate's
   tests (`test_workspace = false`), under `cargo test`: a dependent's
   tests, the binary's acceptance tests included, kill nothing, so a crate
   kills its own mutants. A mutant judged equivalent or untestable gets
   `#[cfg_attr(test, mutants::skip)]` on its function, with `mutants` a
   dev-dependency (`mutants.workspace = true`, so nothing new is linked)
   and a one-line comment above it saying why; never an exclude in
   `.cargo/mutants.toml` or a lowered threshold. cargo mutants runs in a
   session of its own (`setsid`), and whatever it leaves running (a
   process a test spawned whose stop a mutant broke) is killed and named
   in the gate's log; the verdict does not change. The last run's mutants.out (each
   mutant's log, outcomes.json) is kept in target/logs/mutants-staged/. Red
   tests fail it on every branch (no mutant can be judged against failing
   tests), as does a run that found mutants and tested fewer: a mutation
   run that judged nothing has shown nothing. The verdict is the private
   recipe `_mutants-verdict` in `gates/gates.just` (owner ruling 99), inline
   jq over cargo-mutants' own `outcomes.json` (its `outcomes[]`, each
   mutant's file and summary, and `total_mutants`) and its exit code, no log
   read: it fails the red baseline (exit 4, kept), a run that ended before a
   verdict, fewer tested than found, a file under no threshold, a tier below
   its threshold, and a tier that found mutants and judged none (every one
   unviable). No feature reaches the mutation run: `mutants-staged` refuses
   a non-empty `feature_rules` (`_mutants-no-features`), so a crate whose
   tests need a feature of its own dev-depends on itself with it (as
   `session-cli` does for `session-destroy`). Both are held by
   `scripts/mutants-verdict.selftest.sh` (`just selftest`).
