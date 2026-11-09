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
`ctxloom test ...` commands) and run it as a separate process in a temporary
home. The red tests of a milestone are committed red on its branch
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

Slice S4 (sessions) built the following. Its store design is superseded: a
session's state goes through angzarr-embedded with in-memory storage: the
engine driver's I/O is event streams there, ctxloom reacts to the events, and
the driver writes the session's directory, `.ctxloom/sessions/<harp>/` in the
project (session home, metadata, records, worktree), from them. That directory
is the durable record; there is no project-level session store, claim stream
or index (list and search walk the directory; a
name is unique because it is the directory's name), and nothing is imported
from Go. M1 reshapes it.

- `core/wire/sessions-proto`, from `proto/ctxloom/sessions/v1`;
- `core/domain/sessions` (hand-written markup, as taskloom's `tasks`: the
  session aggregate and the `session-names` claim aggregate);
- `core/read-model/session-index` (the rows, state, title, search, retention);
- `core/ports/session-ports` (a project's store, `Liveness`, a session's
  files, Go's session directories);
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

A rename claims the new name, moves the output folder, records the name and
frees the old one; a removed session's name stays claimed. The refusal code is
`sessions-names-taken` (the crate-prefix rule).

Deliberate divergence from Go: `session remove` takes `--even-if-live`, as its
own refusal text already told the user to use.

Slice M1 (run) adds `ctxloom run`: `--one-shot <prompt>` and the bare interactive
session. A turn is a command. A child agent on claude runs one `claude -p
--output-format stream-json` command per turn (`--resume <native id>` on later
ones); the primary agent keeps the human's terminal, handed to the engine by
inheritance, and its data is meant to come from hooks. Crates:

- `core/ports/run-ports` (`RunnerLauncher`, `Engine`, `SessionRecords`) and
  `core/read-model/pending-launches` (the runs still owed a runner);
- `core/application/{run-launcher, run-application}`: the launcher is a
  `CatchUp` over `pending-launches` that drives the engine for one turn and
  records the outcome as facts, and a recorder that writes the session
  directory (`metadata.json`, `records/*.jsonl`: the events, not the conversation)
  from the events and writes the conversation to `transcripts/transcript.jsonl`
  (D27 envelope `v, harp, session_id, engine, seq, ts, kind`); `Runs` plans
  the run through the library's dry-run, mints, enqueues, launches, returns the
  reply;
- `adapters/driven/host-launcher` (a turn's command through `run_within_started`,
  the interactive launch through `run_terminal`: inherited stdio, the caller's
  process group, no deadline; each on a thread of its own, returning at the
  spawn) and the drive half of
  `engine-claude`; `adapters/driving/run-cli`.

An engine's hooks are its adapter's: `ctxloom hook <engine> execute
<NativeHookName>` (hidden) resolves the engine through `surface_ports::Registry`
and its `HookCodec` decodes the payload. The settings an older ctxloom wrote
(`hook <verb> --engine`) are stale and rewritten on the next surface apply.
The interactive engine env has no `HOME`: ctxloom's home reaches its hook
processes as `CTXLOOM_HOME` (`run_ports::HOME_VARIABLE`; the wiring takes it as
the home when `HOME` is absent, and names the resolved home in a run's env; only
the interactive allowlist passes it on), and the interactive session home's
`settings.json` calls `ctxloom hook claude-code execute <Event>` for the
session, turn, message, tool-batch and compaction (`PreCompact`, `PostCompact`)
events (a child's home gets no hooks). The hook command is the bare `ctxloom`,
so the INTERACTIVE engine's `PATH` is the directory of the running binary
(`Place::program_dir`, the wiring's `current_exe` parent), then the inherited
`PATH`; a child's turn env is unchanged. Two consequences: the session's own
shell also sees that directory first, so a sibling program in it shadows the
user's others (intended); and the binary must be named `ctxloom` for the hooks
to resolve it (a renamed binary still leads `PATH` but the bare name finds
nothing there: a known limit, not worked around).
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
credential exists so the interactive engine's hook processes can call back
over the listener without holding the runner's or the model's token: its
scope (`runs-application`'s `Scope`, one value checked in one place, derived
from the run's role in code and journaled nowhere) is today the six capture
facts alone, so widening it later is adding to the scope. What
a hook process receives, through the INTERACTIVE engine env only (like
`CTXLOOM_HOME`; `engine-claude`'s `INTERACTIVE_ENV_ALLOWLIST`, never a child's
turn env, whose allowlist is unchanged, and an inherited value from an
enclosing session is dropped): `CTXLOOM_ENDPOINT`, the URL of the run's TCP
listener, `http://<host>:<port>` (`https://` when the listener has a
certificate; the scheme drives the dial, so TLS is additive; the sink refuses
`https` until it has one to trust); the port is the system's (the listener
binds port 0 before the run is minted and the bound port is told);
`CTXLOOM_BEARER`, the composite `<run-id>.<64 hex>` (the gate splits it to look
the run up, no upstream metadata needed); `CTXLOOM_RUN`, the run id. The
URL's build and parse, the variables and the bind table are pure functions in
`run-ports` (`hook.rs`, `bind.rs`). The
secret is returned once by `Enqueuer::enqueue` to the launching code, which
puts it in the launch order's env; `Bearer`, `Token` and the `Debug` of
`TurnCommand`, `TurnRequest` and `Order` never render it, and no error carries
it. Residual, same-user: the model runs as the user and can read its own
engine's environment, so it can forge CAPTURE facts as the hook principal; it
can never forge runner lifecycle facts (the runner and model tokens are not in
that env), and the capture scope is why the hook credential is a third one.
The token now crosses loopback TCP in plaintext (ruling 32, accepted cost):
anyone on the host who can connect to the port can try the token (it is a
256-bit secret, valid until the run ends, scoped to the run's own session
stream, and a failed attempt says nothing of the token or its digest), and
anyone who can sniff loopback can read it. The listener has no connection
limit, idle or handshake timeout of its own (angzarr's `Listener` offers none:
an escalation); a request is capped at `hook-listener`'s `MAX_REQUEST_BYTES`.
Revisit trigger: any listener reachable beyond the host, a remote or
multi-host child, or an authority wider than capture on this credential: add
TLS (a per-run certificate the client pins) and its pin to the bearer's
transport.
The run is driven on a multi-thread runtime (`ctxloom-wiring`'s `run_runtime`,
one worker), because `Running::finish` blocks the driving thread and a task
spawned on a current-thread runtime (the listener) would starve meanwhile.
An interactive run serves its endpoint: `hook-listener`'s `HookListener` (angzarr's
coordinator `Handle` on a TCP port, port 0, never an unspecified address, the
arrival declared from the bind (the bind table, `run_ports::bind_for`: the host
runtime binds 127.0.0.1 and is told 127.0.0.1; the container rows of ruling 33
are the tested table and its typed refusals, not yet wired to a container
launch), behind `runs-application`'s `HookGate`) is started before the run is
minted (the port is part of what the engine is told) and stopped if the run
does not start; otherwise it is stopped, with a bounded grace, after the
engine exits and before the run's end is last recorded
(`ctxloom-wiring`'s `Hooks`). Not built: the hidden runner subcommand, and the
engine's model (`llm.configs.<label>.model` and any per-agent model are not
passed to the engine yet; they travel with the engines schema).

`crates/core/clippy.toml` holds the core's purity rules (no I/O, clock, env,
threads or hash-ordered collections) and reaches every core crate, because
clippy reads the nearest `clippy.toml`. A crate's own `clippy.toml` would
replace it, so none has one. It repeats the root `clippy.toml`'s settings,
and `just lint` refuses to run if they disagree.

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

- angzarr core `3311a3ae` (`angzarr-diagnostic`, `angzarr-yaml`,
  `angzarr-presentation`; the listener work upstream, coord/grpc-listener,
  is at this rev, and ctxloom does not use it yet);
- angzarr-embedded `ecfaaf65` on angzarr-router `9d22e8b7`;
- angzarr-build `e84092c2` (the markup generator, `angzarr-build` and
  `angzarr-codegen`, its own repository);
- angzarr-platform `3e05f5d9`;
- taskloom's crates `a31eb22a` and ctxloom-lib's `3043ef1f`, built on the
  same line,

so moving one moves all. The lock holds one of every angzarr crate. The platform crates (`os`, `file-system`, `file-lock`) are
angzarr-platform's, and `angzarr-presentation` and `angzarr-diagnostic` are
angzarr core's, all `Apache-2.0 OR BSD-3-Clause`; this workspace is
`AGPL-3.0-only`.

The driving edge's shared shims (`cli-support`: `Call<G>`, `Failure`, the
notices printers, `report_failure`, the argument readers) are
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
when the catalog has the error's `<code>-fix` id, the key omitted otherwise
(`ctxloom-cli`'s `report` adds it; angzarr-presentation's `Envelope` has no
such field yet, so taskloom's envelope stays three-field until it does). Exit codes: 0, 1 failed, 2 refused, 3 fatal
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

## Hook capture: the transcript has one writer

What the engine's hooks observe (a prompt, each MessageDisplay delta, a tool
call as size and SHA-256 only, a turn's end without text, a session's end, a
compaction) is a typed fact of the SESSION stream (`proto/ctxloom/sessions/v1/
capture.proto`; `runs-application::observe` makes them from the unified
`HookEvent`, `Capture` delivers them through a `run-ports::FactSink`, failing
open: a delivery that did not happen is dropped and said on stderr). The
session aggregate records each on a live session, nothing on any other, and a
redelivery is DUPLICATE. `run-launcher`'s `Recorder` is the ONLY writer of
`transcripts/transcript.jsonl` (a `Projection` over the stream's pages, `seq`
counting lines, the last page projected kept in `records/transcript-position.
json`); the launcher records its own prompt and answer as the same facts. A
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
`CTXLOOM_ENDPOINT` with `CTXLOOM_BEARER` when both are set and well formed, and
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
   run that judged nothing has shown nothing.
