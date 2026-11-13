# Introduction

ctxloom is the Rust ctxloom: a port of the Go ctxloom onto angzarr-embedded,
one user journey at a time, with architect review of each milestone. The plan
(`ctxloom.plan.md`'s roadmap, §3, the one integrated plan, owner 2026-11-13)
orders the milestones by journey
(`crates/root/bin/ctxloom/features/journeys/`), with work on
`daemon/integration`; each makes its journey's
scenarios pass, with the cli contracts it touches. A milestone starts as red
tests, committed red on `port/<milestone>`, reviewed, then implemented green.
All work is cut from and landed on `daemon/integration` (owner 2026-11-13:
"One line: all work on integration"); it carries everything `main` had.

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

A branch keeps CLAUDE.md true for the facts it changes, in the same branch (ruling 171).

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
run it as a separate process in a temporary home, built (with its
`test-support` feature, which turns on the hidden `ctxloom test ...` commands
and the `mock` engines) ONCE per nextest run by the setup script
`scripts/nextest-build-binaries.sh` (`.config/nextest.toml`), which names it
and the companion stub in `CTXLOOM_TEST_BINARY` and
`CTXLOOM_TEST_COMPANION_STUB`; `binary()` builds for itself only without
them (owner ruling 144: once per test PROCESS cost about 2.3 s a test). The `mock` and `mock-*` engines exist only under
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

Divergence from Go: a config whose `schema_version` is newer, older or unreadable is refused (exit 20, D15 strict decode); Go warns and continues. A `non_tty_format` of yaml, toml or markdown is refused (D28).

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
Every item verb takes one reference, `create` too (M3, M7): `fragment|command
create <bundle>#<kind>s/<name>` (`Library::item_create_at`) opens the editor
on its template and stores what the editor gives back (an editor that saves
nothing stores the template; an empty buffer is
`library-application-write-empty-edit` and writes nothing), and makes a bare
bundle nobody had (`ItemCreated::bundle_created`); the two-word Go form
(`<bundle> <name>`) keeps Go's placeholder, no editor, and its refusal of a
missing bundle. `skill create <bundle>#skills/<name>` (or the two words)
scaffolds its `SKILL.md` and opens none.
Skill zips are written and read by `library::write::archive` over the `zip`
crate (deflate only, default features off) and crc32fast.
A file's executability is ONLY its skill sidecar's `executable:` list
(`skills/.<n>.meta.yaml`), on every OS (owner ruling 2026-11-12; Windows
reports every file executable): `library::write::skill::declared_executables`
answers it for a bundle's tree, and nothing reads a mode from disk or git
(`ContentRepo` has no `executable_files`, `UserFiles::read_tree` and
`deps_ports::RepoTree` are bytes alone). `skill show`'s mode column, an
export, an import (the sidecar it brings), an installed bundle and a pin's
disclosure take it from there; an install writes the declared files 0755 as
a convenience for running them, never read back; a push commits them 100755
and every other file 100644 (`Remotes::publish`'s `executable`, `git add
--chmod`; a pull request's `PullRequest::executable`). Visible change on
unix: a file chmod'ed executable but not listed shows 0644 and is pushed
100644. Not changed: a skill zip's entry modes, which its export writes
from the sidecar and its import turns into the sidecar's list.

Slice S4 (sessions) built the following. The record is the user's durable
store (owner rulings 87a, T14, which reverses 87's store per project): a
session's state goes through angzarr-embedded over the ONE per-user
`<ctxloom home>/.ctxloom/ctxloom.db` (machine-local; see "Storage is
angzarr-store-sqlite"), every project's streams in it, each rooted on its
resource name (below), every hook's fact an event when it is delivered. `session list`, `search` and `show`
are the daemon's reads (slice 2 step 6, "The daemon's API"), from the session
index it keeps warm, each waiting for the daemon's next round so it answers
what was committed before it was asked; `transcript show` catches up from the
store on each read; so a live session shows what was delivered so far; nothing projects the events into a file (no
`transcripts/transcript.jsonl`, `records/*.jsonl`, position file or
`metadata.json`). A session's directory, `.ctxloom/sessions/<harp>/` in the
project, holds what its engine needs (the session home, its native
history), and `<harp>.lock` beside it is held by its run for its life. Its
scratch checkouts are NOT there (owner ruling 2026-11-12, Q1): they are under
ctxloom's home, outside the project,
`<home>/.ctxloom/worktrees/<project-id>/<harp>/<agent>`
(`isolation_ports::session_worktrees`, a `Request`'s `work_dir`), each on the
session's own branch for its agent, `ctxloom/<harp>/<agent>` (F1: two
sessions of one agent no longer collide), its path canonical (macOS's
`/private/var`, the trust key), and every git `worktree-git` runs is `git -c
core.longpaths=true ...`; `GitWorkspaces` lists, knows and reclaims them
there (a session is known by its directory, its lock or its checkouts). A
run's engine is PLACED where every launch is set up, run-launcher's
`set_up_launch` (`place_workspace`; the launcher's `prepare` hands it
`Launcher::with_workspaces`'s, the wiring's `GitWorkspaces`), for every
launch path alike: the plan's axis
(`DryRun.axes.0`, the flag else the config's `workspace`) rides the `Order`
(`Order.workspace`: the axis, the session's checkouts directory from
`Place.worktrees`, the agent), and the placed directory is the
`PrepareRequest`'s and the `TurnRequest`'s `cwd` and `RunnerStarted.workspace`
(`Order.cwd` stays the live project), so claude's trust entry
(`seeded_state`, `projects.<cwd>.hasTrustDialogAccepted`) is the checkout's
resolved path. `none` is the project; a checkout is
its path; one that cannot be had (not a git repository, the add failed, no
checkouts directory or no workspaces port) is the project and the notice
`library-application-notice-workspace-unavailable`; one the port refuses
fails the launch (`LaunchFailed`). Once the engine has exited and the
listener stopped, and before the session's lock is let go, the run lets the
checkout go (`Launcher::release`, the port's WIP-safe `remove`, never
forced; owner ruling 2026-11-12, Q2): a clean one is removed (`git worktree
remove` drops its own record; never a repository-wide `git worktree prune`),
its branch deleted when merged; one holding work, or that will not go, is left and
said, `library-application-notice-checkout-left` (path, branch, why). A
resume re-enters or re-creates the same path. A name is unique because its claim is. Accepted cost: a clone has no session
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
- `core/application/sessions-application` (list, search, show, keep, title,
  bind, the transcripts; a removal's types and the head-guarded `purge` the
  daemon's destroy uses; Go's `session.yaml` schema 1 to 3 read through
  angzarr-yaml; the importer's types);
- `adapters/driven/{liveness, session-files}` and, behind `ProjectStores`,
  `ctxloom-store-sqlite`;
- `adapters/driving/{session-cli, hook-cli}` (`hook` is hidden: `session-bind`
  and `hud`).

A session's state is its lock's verdict, never its end stamp; its title is the
one `session rename` set (S55), else the first prompt of its transcript; `hud` renders and records nothing. The
kernel's retry is `angzarr-embedded-runner`'s (the embedded pin). A read catches
up fresh each time and keeps the records of the project asked (`SessionState.project_id`, kept as a reference, S49); nothing caches a project's index. Rows and refusals say what was not
done and the command that would, in the message, so the json envelope carries
it too. A crate's catalog is `locales/en-US/messages.ftl`, ids the full code.

Names and ids (owner, 2026-11-12, slice 2 B3/re-root/S55, superseding the
2026-11-03 formulas: `sessions::project_namespace`, `claim_root` and
`sessions_application::imported_id` are gone, a hard migrate with no reader
of the old roots; a store written before is unreadable, start fresh):

- a session is named `projects/{project}/sessions/{session}` and a project
  `projects/{project}`, each segment a harp (`sessions::validate`'s grammar,
  not AIP-122's charset), formatted and parsed in ONE place, `sessions::name`
  (`SessionName`, `ProjectName`; a name of another shape, or a segment that
  is no harp, is `SessionRejection::NameMalformed`, `sessions-name-malformed`);
- a stream's root is the UUIDv5 of its resource name in `sessions::NAMESPACE`
  (`1473f25e-0851-4e01-8741-81055551414f`, minted once; it NEVER changes:
  every root in every store derives from it, so a new value orphans every
  stream): a session's stream and its name's claim (domain `session-names`)
  at `SessionName::root`, the session's stable id `SessionName::uid`; a
  project harp's claim (`project-names`) at `ProjectName::root`; the
  `projects` stream, which mints the harp and so cannot be rooted on it, at
  `project_id_root`, the UUIDv5 of the project id (which holds no `/`);
- a project is named (`projects` aggregate, S54) at the first resolution of
  its directory, before any session of it is minted: today by the wiring
  (`Wiring::named_project`) before a run (`run_application::Place::
  project_harp`), a child's launch, an import and the test seed, and by the
  daemon's `ResolveProject` (below, "The daemon's API"). A launch in a project
  with no harp is `sessions-application-no-project-harp`;
- a new session's harp is chosen before its run is enqueued (its id is its
  name's) and claimed after; a name another run claimed meanwhile ends the
  run refused `sessions-names-taken`;
- an imported Go session is rooted on its new name, its project named at the
  import (`ImportRequest::project_harps`, fresh harps the wiring fills). Go's
  `run_ends[]` and `end_reason` are NOT imported (they return with S10).

A harp never changes (S42; S55, owner 2026-11-12): `session rename <harp>
<title>` sets the session's free-text TITLE (`SetSessionTitle` ->
`TitleSet`, `SessionState.title`; live or ended; the title held again records
nothing, an empty one clears it), shown in its listing and `show` in place of
the first prompt, and in the daemon's `Session.title` beside the summary. Its
harp, its name, its directory, its lock and its output folder stay where
they were. The harp-changing rename is gone with its claim release and its
moves: `RenameSession`, `RenamedTo`, `SessionLive`, `HarpTooLong`,
`LockEvidence`, `validate_rename`, `SessionFiles::move_output|move_session`
and `session edit --name`. A removed or destroyed session's name stays
claimed. The refusal code of a taken name is `sessions-names-taken` (the
crate-prefix rule).

`session remove` DESTROYS a session (owner rulings 94, 94a-d, 98), and exists
only under the cargo feature `session-destroy` (binary -> `ctxloom-wiring` ->
`session-cli` and `daemon-api-grpc`; `session sweep`, whose apply is that
destroy, too, as is `session remove --older-than <age>`, the one
destroyer's age form (docs/plans/ctxloom-features.md "Sessions"), which is
this project's sweep with that purge age, its apply named `session remove
--older-than <age> --yes`; a session name and `--older-than` are one or the
other, and `--even-if-live` is the single session's alone). The sweep
purges a session whose directory holds no file like any other: its record
is its stream (ruling 87a). The DAEMON destroys (owner, 2026-11-12, slice 2 step 6), the
store's one owner: both verbs are its clients (`DeleteSession`,
`SweepSessions`, below, "The daemon's API"), and nothing but the daemon
opens the store for a destroy (the in-process path, `SessionService::remove`,
is gone). There is
one deleting verb: `session purge` is gone in every build, and a plain build
has no deleting verb at all (accepted cost: it cannot hide a session from the
listings); `scripts/plain-destroy-check.sh`, run by `just build`, holds the
plain binary to knowing none of `remove`, `purge` or `sweep`. The acceptance
tests build with the feature, and `session-cli` (and `daemon-api-grpc`)
dev-depends on itself with it, so its tests and `--all-targets` lint compile the destroy (no
`feature_rules` entry: the mutation recipe refuses one). A destroy purges,
from the store, the session's stream and each run stream UUIDv5(session,
1..=n), each guarded by the head just read (retried on a conflict), then
removes the session's whole directory (authored files too, a login link never
followed) and its lock; the name's claim is kept, and the daemon's warm
session index, pending messages and board are told the streams are gone
(the board its runs' too, so a destroyed session leaves no board row).
Before the purge each child the session holds is closed and its owner
forgets it (`session_policies::destroy`, keyed commands through the core). Its scratch
worktrees go first, in ONE safe order both verbs share
(`reclaim_application::remove` over `Removals`): classified first, the whole
removal refused if one is spared or skipped
(`isolation-application-keeps-session`; `--even-if-live` reaches no
worktree), else the clean ones removed,
then the session destroyed (F2; a worktree that went dirty in between is
left and the session kept). The streams go first, so a
failure removing the directory leaves a session nothing lists. A hook fact
redelivered after it finds no session and records nothing. It is refused, as
before, for a session whose lock does not prove it ended (held, or none;
the daemon reads the lock) unless `--even-if-live` (the one override), for
a kept one, and within the transcript retention: its project's
`session_transcript_retention`, read from its REGISTERED root (30 days
unless configured; one that is no age is `AgeInvalid`, a configuration that
does not read `ConfigUnreadable`, a sweep's too, the flag or not). A refused removal is the typed `RemovalRefused`
carrying its PLAN (the files, their classes and bytes, the authored ones,
the worktrees), so it prints exactly the plan it printed in process, then
the refusal in the refusing use case's words (`daemon_api::
RemovalRejection`), exit 20. A removal whose session holds files a person wrote
(anything but `transcripts/` and `native/`) warns first, naming each, as the
notice `sessions-application-destroys-authored`, never a refusal: on
`--yes`, in the report and with `--even-if-live` alike, and `session sweep`
gives it for each session it destroys or plans to (its row's `authored`;
owner ruling 104: every destroy of authored files warns);
`features/cli/session_remove.feature` runs the built verb, and
j001300 the age form. Worktrees ctxloom did not create (any but the
project's own checkout and those under `isolation_ports::scratch_root`,
`<home>/.ctxloom/worktrees`) are never listed or touched by either verb;
doctor reports them, `DOCTOR-CHECK-FOREIGN-WORKTREES-s9` (`ctxloom_ports::
ForeignWorktrees`, worktree-git's `GitForeignWorktrees`, read-only), each
with its branch's merge state against the project's HEAD and its dirty
state as git answered them (unknown when git did not), and the `git -C
<project>` commands that remove it, the forced forms (`worktree remove
--force`, `branch -D`) only where the safe ones would refuse.
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

An engine may advise on a plan (owner ruling 168): `Engine::advise(&Planned)
-> Vec<Advice>` is shown what the plan resolved for it (`run_ports::Planned`:
the agent, model, effort and permissions document, the OS, whether the launch
is interactive and whether a primary credential variable is set, by name
only) and answers zero or more diagnostics of its own crate (`Advice`, a
boxed `Diagnostic`: code, args, en-US message, an optional `-fix`), said as
notices and never a refusal; by default it says nothing (claude-code says
only `engine-claude-windows-token-only`, at a Windows terminal launch with
no token set). The library asks the plan's engine in
`launch_of`, beside the runtime-weakening notice
(`Library::with_engines`, the wiring's real engines;
`Notice::Engine`), so a dry run and a launch, which plans through the same
dry run, say them alike, text and json.
A command's program is FOUND before it is spawned, a child's turn and the
terminal's alike, in the one launch path (`Launcher::prepare` asks
`RunnerLauncher::find_program`; `host-launcher`'s `find`, given the wiring's
filesystem by `HostLauncher::searching`): on the command's own `PATH` (the
engine's, led by the program's directory), by `os::Flavor::candidates` (on
Windows PATHEXT's, the command's own or cmd.exe's default), an executable
preferred to a batch file wherever each is on the PATH; the file found is
spawned. std's spawn would try `<name>.exe` alone, so an npm `claude.cmd` was
"program not found" (owner, 2026-11-12). Found nowhere, the run is not
started: `run-ports-program-not-found` (or `-no-path`), naming the names and
directories searched, exit 10. A batch file is run by std through cmd.exe
with its own quoting (Go's `batchcmdline.go` rules, usable-epileptic), and
std refuses it an argument with a line break or NUL; `run_ports::
check_batch_arguments` refuses that before the spawn (on Windows: a
multi-line opening prompt to `claude.cmd`), `run-ports-batch-arguments`, exit
20, its fix claude's native build or no opening prompt.
An engine's hooks are its adapter's: `ctxloom hook <engine> execute
<NativeHookName>` (hidden) resolves the engine through `surface_ports::Registry`
and its `HookCodec` decodes the payload. The settings an older ctxloom wrote
(`hook <verb> --engine`) are stale and rewritten on the next surface apply.
An engine's `HOME` is its session home (owner ruling 112, claude's as
opencode's, a terminal's and a child's turn alike; under `session_home: host`
the user's own), so what its tools keep under `~` lands in
`.ctxloom/sessions/<harp>/home/`, never in the project (a relocation, not
confinement: the engine runs as the user and reaches her files by absolute
path); the cost: they see none of the user's dotfiles. A plan on
`session_home: host` says that it runs in her own home, reading and
changing her own memory, MCP servers and history, as the notice
`library-application-notice-session-home-host` (`Notice::SessionHomeHost`,
from the plan's drive in the dry run, so a dry run and a launch say it alike,
with an agent or none; never a refusal; j002200). On Windows the session home is also its
`USERPROFILE` (node's `os.homedir()` reads it there), and `APPDATA` and
`LOCALAPPDATA` are `<home>/AppData/Roaming` and `<home>/AppData/Local`
(`run_ports::WINDOWS_PROFILE_VARIABLES`, `session_profile`; under `host`
the user's own, passed on); the system's `SYSTEMROOT`, `SYSTEMDRIVE`,
`WINDIR`, `TEMP`, `TMP`, `PATHEXT` and `COMSPEC` pass on as they are
(`WINDOWS_STARTUP_VARIABLES`). Every one is on all four allowlists and none
is set on unix. The run's environment is keyed as the platform compares
names (the wiring's `run_env`, `os::Flavor::env_key`: uppercase on Windows,
so `Path` is `PATH` to the allowlists, the run's `PATH` and the permits; the
spelling is not kept). The user's own home is resolved ONCE, the
platform's home variable (`HOME`, `USERPROFILE` on Windows; the wiring's
`Wiring::user_home`), and carried as a value (`Place::user_home`,
`Order`/`TurnRequest::user_home`): her login is found under it, her claude
and opencode settings read from it, and a `session_home: host` engine given
it; nothing reads the environment's `HOME` for it (Git Bash sets its own).
ctxloom's home reaches its hook processes as
`CTXLOOM_HOME` (`run_ports::HOME_VARIABLE`; the wiring takes it as the home
FIRST, before `HOME`, and names the resolved home in a run's env; both
allowlists pass it on). The live project reaches them as `CTXLOOM_ROOT`
(`run_ports::ROOT_VARIABLE`, set by `run_launcher::set_up_launch` to the
launch's project, over any the caller had, owned so no permit replaces it, on both
engines' allowlists), so a hook or the agent's own `ctxloom` call from a
checkout resolves the project (`find_app_dir` honours it before the walk;
the checkout's `.ctxloom` is the tracked config alone). The session home is seeded, once (one there is left
as it is), with a `.gitconfig` of the user's `[user] name/email` alone, as git
resolves them in the project (`worktree-git`'s `Git::user`,
`run_ports::git_config`, written by the launcher): a file, not
`GIT_AUTHOR_*`/`GIT_COMMITTER_*` (they would outrank a repository's own
user), and nothing else of her git configuration (no signing key, credential
helper or alias); not under `session_home: host`, where the engine's
`HOME` is hers and already holds it (`Launcher::seed_git_user`). claude's own state file in the session home,
`.claude.json`, is seeded (owner ruling 113) with `hasCompletedOnboarding:
true` and `projects.<the run's directory>.hasTrustDialogAccepted: true`, the
keys claude 2.1.322 reads (no theme picker, security notes or trust prompt),
MERGED into what is there (`Engine::merges` names the files an engine merges
into, and the launcher hands their bytes in `PrepareRequest::existing` with
the run's `cwd`; one that is not a JSON object is left as it is). It is
claude's undocumented state: a release that changes it shows the onboarding
again (visible, not harmful). The session home's user-scope `settings.json`
is the USER'S OWN claude `settings.json` with ctxloom's values laid over it
(owner ruling 169, reversing "nothing else of her settings crosses" of
rulings 112 and 166): read from claude's config dir as claude finds it,
`$CLAUDE_CONFIG_DIR` else `~/.claude` (the wiring's `claude_user_settings`,
told to `Claude::over_user_settings` as a `run_ports::UserSettings`), its
`permissions` replaced WHOLE by the agent's document (none: none of hers
left either), everything else of hers kept (attribution, model,
`effortLevel`, `env`, plugins, hooks, `statusLine`, and her
`skipDangerousModePermissionPrompt`: ruling 166 is now a case of the copy).
ctxloom's hooks and statusline stay in `ctxloom-hooks.json`, the
`--settings` file, which outranks user scope. It is rewritten from hers at
every launch (T21); no file of hers is a start from `{}`; one that is not a
JSON object, or does not read, is left out (the home starts from `{}`) and
said as the notice `library-application-notice-user-settings-left-out`
naming its path (`Prepared::left_out`, `Launched::left_out`), never a
refusal. Under `session_home: host` nothing is copied (the config dir IS
hers, never written). No value of hers renders: `UserSettings`'s and
`HomeFile`'s `Debug` show a path and a size alone (her `env` may hold a
secret). Cost: her hooks fire in the session too, and an invalid value of
hers disables the agent's permissions with them (one file, ruling 145's
caveat). Every command is measured against the platform's command line
once it is complete and before it is spawned (owner ruling, 2026-11-12;
`run_ports::check_command_line`, called in one place, the launcher's
prepare, `Launcher::on_os(wiring.os_name())`): the terminal session's
opening prompt is the one piece of content on an engine's command line
(claude's positional prompt, opencode's `--prompt`; a child's turn has it on
stdin, which is not measured). One table of constants, no libc call:
Windows, 32767 UTF-16 units for the whole line, its NUL included, counted as
std quotes it (a `.cmd`/`.bat` program as if every unit were escaped);
Linux, 131072 bytes per argument or environment string, its NUL included
(MAX_ARG_STRLEN). Linux's total (ARG_MAX, a quarter of the stack rlimit) and
macOS's (`kern.argmax`, a sysctl) are not constants and the platform crates
read neither, so no total is checked: such a command fails at the spawn as
before. A command over a limit is `run-ports-command-too-long` (exit 20,
`RunError::TooLong`, the launch's `Outcome::TooLong`; the run ends
launch-failed, nothing is spawned), its fix: start the session without the
prompt and paste it, or `run --one-shot`, which reads the prompt from
stdin. The terminal session (claude's and opencode's TUI) is
also given `TERM` and `COLORTERM`, without which it draws in no colour, and
`NO_COLOR`, her choice to have none (no-color.org); a
child's turn is given none of the three. The session home's `ctxloom-hooks.json` calls `ctxloom
hook claude-code execute <Event>`. The interactive home registers the session,
turn, message, tool-batch and compaction (`PreCompact`, `PostCompact`) events.
A one-shot CHILD's home (owner ruling 51) registers the per-turn ones only:
`UserPromptSubmit`, `PostToolBatch`, `MessageDisplay`, `Stop`, `PreCompact`,
`PostCompact`; never `SessionStart` or `SessionEnd`, which `claude -p` fires
once per PROCESS (every turn), and whose slow hook claude cancels.
EVERYTHING ctxloom delivered at rest rides the launch (owner ruling
2026-11-12), from the RUN agent's selection (`DryRun.surfaces`, library-
application's `launch_surfaces` over `Project::carried`, the same as
`package`; `Order.surfaces` -> `PrepareRequest.surfaces`, a
`run_ports::LaunchSurfaces` whose `Debug` names what it carries and withholds
every value), in every working directory: claude lays the bundles' authored
hooks and ctxloom's own callbacks (session-start, skill-mates) into
`ctxloom-hooks.json` beside the capture events, each once (a child: no
`SessionStart`/`SessionEnd`), and the statusline (`statusLine`) there too;
`deny_tools` is `permissions.deny` laid over the agent's document where it
lives (the session home's `settings.json`, or the hooks file under `host`);
the servers not served by the session are `ctxloom-mcp.json`, written at
every launch and named by `--mcp-config` (variadic: an option always follows
it; a turn's, the terminal's and a hosted child's, `drive::push_mcp_config`);
the commands and skills are the session home's `commands/` and
`skills/` (claude's user scope under CLAUDE_CONFIG_DIR), and under
`session_home: host` files written into the session's checkout alone
(`Prepared::workspace_files`, hidden by `Engine::workspace_patterns`,
`.claude/commands/` and `.claude/skills/`; never the live project), taken out
at the release before the checkout goes. Probed live on claude 2.1.322 (the
stream's `init` event, temporary project and CLAUDE_CONFIG_DIR, no model
call): `--mcp-config` loads (`source: dynamic`) and replaces a same-named
project server, user-scope `commands/` and `skills/` are offered, and
`--disallowedTools` (variadic, so unused) removes a tool. A hook claude
cannot narrow (a tool class it has no tool for) is never dropped unsaid: the
launch is `run-ports-engine-cannot-carry` (`EngineError::CannotCarry`),
nothing launched (M6). opencode's
plugin runs the bundles' authored hooks (`ctxloom-authored-hooks.json` in its
session home: `tool.execute.before`, where exit 2 blocks the call,
`tool.execute.after`, `session.created`, `chat.message`, `session.idle`; read
from opencode 1.18.4's plugin interface, not run live), and its session home
(`OPENCODE_CONFIG_DIR`, in both home modes) takes the commands as
`commands/<name, / as ->.md` and the servers as `mcp.<name>` of its
`opencode.json` (`{type: local, command: [program, ...args], environment}`
or `{type: remote, url, headers}`; `engine_opencode`'s `carried`, M6, from
opencode's docs, not run live; a command a later launch no longer carries is
left there). A project refresh
gives back at rest what a launch carries (`EngineSurfaces::rides_launch`:
every kind for claude; the context left as before), and `manage check` says
`hooks_at_launch`; an explicit `materialize` still writes them at rest; a
bare `claude` started without ctxloom has none of them (accepted). The hook
command is the bare FILE NAME of the running binary (owner rulings 163, 164;
`ctxloom` for a binary of that name, which is what `just install`
installs): claude's hooks and statusline, at rest and in a session
home, and opencode's plugin's `COMMAND` all run it. The wiring resolves it
ONCE (`ctxloom-wiring`'s `program::running`, from `std::env::current_exe`,
which on Linux is `/proc/self/exe`, a symlink's target and never the link; the
platform's executable suffix, `std::env::consts::EXE_SUFFIX`, stripped in any
case, so `ctxloom.exe` is `ctxloom` and every written hook, statusline and
plugin command is `ctxloom hook ...` on every OS, the PATH search finding the
`.exe` itself (owner ruling 2026-11-12, amending 163 and 164); the
authored `ctxloom`, `surface_ports::CALLBACK_PROGRAM`, when it cannot) and
tells the adapters as a value when it builds them (`Claude::calling`,
`Opencode::calling`, `Mock::calling`, `Doctor::calling`); an authored callback
(`ctxloom hook <verb>` in a bundle or ctxloom's own package) is installed
running it (`Hook::installed_as`). An engine's `PATH` (the terminal's and a
child's) is the directory of the running binary (`Place::program_dir`, the
same resolution's directory), then the inherited `PATH`, joined as the
platform joins it (`run_ports::search_path_led_by`: `;` on Windows, the
directory quoted if it holds one; `:` elsewhere), so the bare name is
this binary. The session's own shell also sees that directory first, so a
sibling program in it shadows the user's others (intended). Only the running
binary's entries are its wiring (`manage hooks check`, a surface's
`hooks_present` and `statusline`); doctor reads an entry running `ctxloom` or
the running binary's name as ctxloom's. The CLI's own help and fix texts
still say `ctxloom`.
The launcher records `RunnerStarted{runtime, workspace, pid}` AFTER the spawn,
with the pid the spawn gave (`RunnerLauncher::launch` answers a `Running`, the
launcher records, `Running::finish` waits), and the pending launches clear on
it, not on the runner's first word; a command that cannot spawn is a
`LaunchFailed` and never a `RunnerStarted`. An interactive run's terminal
session is ended by the human (the terminal delivers Ctrl-C to the shared
process group, so it cancels a generation inside claude).
A run asked to stop stops its engine first (ruling 114): for the whole run
`ctxloom-wiring`'s `signals` hears SIGHUP, SIGINT and SIGTERM
(`os::Os::signals`, each named), so none ends ctxloom by its default and
orphans the engine. A one-shot stops on any of the three; the session on
SIGHUP or SIGTERM only (a SIGINT is the terminal's Ctrl-C, which reaches the
engine itself, and is let pass). The stop is `HostLauncher::stopped_by(Stop)`'s
`Stop`: a turn's tree is asked to stop (SIGTERM to its group, run-within's
`run_within_stopped_by`), given `Within::stop_grace` (five seconds), then
killed; a terminal session's direct child is killed. The run then ends as any
run whose engine exited (its end facts, the listener stopped, the locks
released), and the command exits 128 plus the signal's number, the error
`run-application-stopped` (`RunError::Stopped`) naming the signal. A second
signal during the grace is heard and changes nothing; a signal that cannot be
listened for is said as a notice and the run goes on unforwarded. Tested with
the platform's fake (`os::Signals::fake`) and, for a one-shot's SIGTERM, end
to end in j001000.
ONE PRINCIPAL, THE USER (the separation model, owner 2026-11-12, slice 2;
separation-analysis.md). On the host there are no principals but the user:
a process of the user, the model included, can do anything the user can
(read every credential, write the store, run every verb). ctxloom defends
its TCP listeners against NON-USER callers (other users, web pages,
containers, machines) and promises nothing between the user's own
processes. A run's listener takes one bearer; it keeps non-user callers off
the socket, and nothing separates the user's own processes. That bearer is
the run's hook bearer, minted at enqueue: a 256-bit token whose SHA-256
alone is journaled in `RunEnqueued` (`hook_credential_hash`; `RunEnded`
revokes it in the fold, a bound on how long a harvested bearer is good for).
The runner's and the model's tokens are gone (`EnqueueRun` and
`RunEnqueued` fields 4 and 5 reserved; `Allowlist::runner/model`,
`RunsGate`, `Admission`, `CredentialsNotDistinct` and runs-application's
`Principal` with them), and so are the scope tables (`Scope`,
`hook_allowlist`, `orchestration_scope`, `OrchestratorGate`): an
authenticated caller is the user, with the user's allowlist
(`runs_application::user_allowlist`, every command and fact the session
domain handles, read from the aggregate, since angzarr's unrestricted
allowlist is its own). The run's `HookGate` (the CLI run's own listener's,
kept while the CLI serves its own runs) identifies the run its bearer names
and `owns` that run's session stream alone: an addressing check (a fact's
session must be its run's), not a permission. What
a hook process receives, through the engine env (like `CTXLOOM_HOME`; both of
`engine-claude`'s allowlists, `ENV_ALLOWLIST` for a child's turn since ruling
51 and `INTERACTIVE_ENV_ALLOWLIST`; always the run's own values: an inherited
one from an enclosing session is replaced, never passed on), with
`CTXLOOM_SESSION_HARP`, the session's harp (for its statusline), and
`CTXLOOM_SESSION`, the session's stable id, what a hook addresses its facts to
(review finding 6: the wiring's `RunIds`; a fact's session must be its run's,
an addressing check; a hook also reads the user daemon's discovery file, to
deliver there first with the user bearer it names): `CTXLOOM_ENDPOINT`, the URL of the run's TCP
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
there is no list of sensitive names to keep in sync (redaction keeps the
user's secrets out of output the model and her transcripts read; it is not a
separation). A process of the user, the model included, can do anything the
user can: read every credential, write the store, run every verb (rulings
37's "never forge", 38 and 27's residual are retired). The callback is TLS,
server-auth only, no mutual TLS (ruling 36): the per-run certificate
authenticates the SERVER to the client, which pins it as its SOLE trust
root (no system roots, no fallback to plaintext), and the bearer keeps
non-user callers off the socket. So the token never crosses loopback in the
clear; what reaches the port can still try the token (a 256-bit secret,
valid until the run ends; a failed attempt says nothing of the token or its
digest), and a process of another user that binds the port first cannot
present the certificate (the key never leaves the listener's process). The certificate is minted per run
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
naming `CTXLOOM_TLS_CERT`) and sends nothing. The listener's limits are
angzarr's generic `Listener` defaults (core `f6fed5e5`; `hook-listener` sets
none): a TLS handshake within 10 s, an HTTP/2 keepalive ping every 60 s
answered within 20 s, a connection closed after 1 h plus a 30 s grace (never
zero: a zero grace is `ListenError::ZeroConnectionAgeGrace` at bind, which
`hook-listener` carries as its bind error's cause), at most 200 streams per
connection, and no request timeout of the server's own. There is still no
limit on the number of connections; a request is capped at `hook-listener`'s
`MAX_REQUEST_BYTES`.
Revisit trigger: agents run in containers (a per-container bearer and real
scopes return there; angzarr's `Gate`/`Allowlist`/`Principal` seam and
`bind_for` are kept for it). A one-shot child's model can read the same
environment, so it can write any session.
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
one path for both kinds of run). `run --session <harp>` (owner ruling 115,
a one-shot or the terminal) resumes a session: the run application reads the
name's claim (none: `sessions-session-unknown`), folds the session's stream
(empty: `sessions-session-destroyed`, a removed session's claim being kept),
probes its lock (held: `sessions-session-running`), after a launcher step has
swept other processes' dead runs; the session aggregate's `ResumeSession`
then owes the NEXT incarnation (`IncarnationOwed{n+1, "resume"}`, refused
while one is open, `sessions-session-running`; with no bound native id,
`sessions-no-native-session`; for an `--engine` not the session's,
`sessions-engine-mismatch`; for a `--workspace` not the axis the session
was minted on, `MintSession.workspace` (none recorded reads `none`),
`sessions-workspace-mismatch`, and with none given it is planned on the
session's own, so it re-enters its checkout) BEFORE the run of `(session, n+1)` is enqueued,
so a refused resume burns no run stream; a refused enqueue ends the owed
incarnation (`launch-failed`). The run is planned on the session's engine,
reuses its harp, directory and session home (no mint, no claim), and the
`Order`'s `resume` (the native id) reaches the engine as
`TurnRequest::resume` (claude `--resume`, opencode `--session`). A child's
native id is bound from its turn's stream; a terminal session's is bound by
the launcher at the terminal's exit, from the last `SessionEnded.native_session`
its hooks recorded (claude's SessionEnd; opencode's end is the launcher's own,
with no native id, so an interactive opencode session cannot be resumed yet).
Not built:
the hidden runner subcommand.

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
for one started (ruling 95); no orphan claims `LaunchFailed`. A started run
whose OWN lock (`RunEnqueued.launcher_lock`, which `pending-launches` keeps
on a started run) is free has lost its owner too: a host that held it for
the run's life, as the daemon does for a child it hosts, is gone (T3, T18);
a primary removes its file once its runner started, which proves nothing.
Then the session's incarnation ended (`orphaned`). The sweep is ONE
implementation, `run_launcher::Sweeper`: `Launcher::step` runs it over the
runs it holds no order for, and the daemon's launcher runs it as its first
step (below). Every
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
refused before launch). Its permissions are the ENGINE'S OWN DOCUMENT (owner
rulings 133-139): `agents.<n>.engines.<e>.permissions`, copied verbatim and
checked only for being an object (owner, 2026-11-13: a list or a word is
refused at load, `library-application-config-breaks-schema`; any object is
taken, a typo in it reaches the engine, the accepted cost), else the engine's default document (claude-code's `{defaultMode:
acceptEdits}`, so an agent with no block runs as before; opencode's none; a
block written replaces it whole). The plan carries it as JSON text in the
order it was written (`DryRun::permissions`, `library_application::json::
document_text`), the order and the launcher hand it to `Engine::prepare`
(`PrepareRequest::permissions`), and the config writer keeps it in its own
order (`library::write::section::sorted_section`; the rest of the agents
section is sorted as ever). engine-claude writes it as the `permissions`
of the session home's user-scope `settings.json`, over the user's own
settings (ruling 169, above; the home is the session's `CLAUDE_CONFIG_DIR`;
none of either, `{}`), APART from
ctxloom's hooks, which are `ctxloom-hooks.json`, the file `--settings` names
(owner ruling 145: claude 2.1.322 refuses a settings file whole for one
invalid value, hooks included, so a bad value disables the block alone; the
probe is in the ruling-145 commits). User scope is claude's lowest: a single
value the project's `.claude/settings.json` also sets is the project's, lists
merge, and it is where `defaultMode: auto|bypassPermissions` may take effect.
Under `session_home: host` (the config dir is the user's own `~/.claude`,
never written) the block stays the `permissions` of the hooks file, with the
cost that a bad value there disables the hooks too. No `--permission-mode` is
passed, claude's mode is the document's `defaultMode`. A profile's
`deny_tools` rides the launch (above), laid into the same `permissions.deny`. engine-opencode writes the session home's `opencode.json`
(`OPENCODE_CONFIG_DIR`, which opencode 1.18.4 reads after the project's) with
the document as its `permission` object (none, no `permission`, so no earlier
run's lingers; read from the 1.18.4 bundle, not run live), over the user's
own global config by analogy with claude's (ruling 169, an extension the
owner is to confirm): `opencode.json` of `$XDG_CONFIG_HOME/opencode` when
absolute, else `~/.config/opencode` (xdg-basedir, as its state directory;
`engine_opencode::user_config_file`, the wiring's `opencode_user_config`,
`Opencode::over_user_config`), its `permission` replaced whole, one that is
not a JSON object (a JSONC file with comments among them) left out with the
same notice; under `session_home: host` opencode reads hers itself and
nothing is copied. Her `opencode.jsonc` and `config.json` are not read. A document that is not
JSON is `run-ports-engine-bad-permissions` (ctxloom's own rendering, so a
bug). Both files are rewritten whole from the order at EVERY launch (T21: a
fresh session, a turn, a resume; neither is in `Engine::merges`), so an
agent's edit of its own permissions never survives into a launch. ctxloom
chooses no opencode agent (`build`/`plan`): no flag for one.
Gone (hard migrate): ctxloom's mode lists and launch check (ruling 125),
`--permission-mode` (ruling 116), `run-ports-engine-unknown-permission-mode`,
every `permission_mode` field, and `agent create|edit --permissions` (ruling
138: the block is written in the config by hand, `config edit`, or by the
setup agent). `docs/config/permissions.md` is the page.

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
plan's warnings, said beside it, and the exit is 21 (the most severe wins),
else 20. The run is enqueued FIRST (73g): plan, evidence, `EnqueueRun`, the
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
`ANTHROPIC_AUTH_TOKEN` is not passed to a child whatever the config says (so a
child bills the account its config names; it does not hide the token from a
child, which runs as the user), unless a permit names it (the `CTXLOOM_*` hook variables are a child's too since
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
to `<her home>/.claude` (no home is `run-ports-engine-no-host-home`), links no
login there; the hooks still fire, because the launch (the terminal's and a
child's turn) passes `--settings <session dir>/ctxloom-hooks.json` (the ctxloom-owned file `prepare` writes,
in both home modes, never into the user's `~/.claude`; claude adds its hooks to the
config dir's own, and honours one `--settings`; the agent's permissions
document is not in it but in the session home's own `settings.json` (ruling
145);
verified with a fake program only, not a
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
keychain and Windows' symlink privilege; on Windows claude is TOKEN ONLY,
owner ruling of 2026-11-12: the terminal session's engine is asked first,
through `Engine::advise` (`Planned`'s `os`, `interactive` and
`credential_set`, the last by name only), and engine-claude's
`engine-claude-windows-token-only` notice, which says this is a workaround
for claude's own credential handling (one session's token rotation signs the
others out) and whose fix is `claude setup-token` then export
`CLAUDE_CODE_OAUTH_TOKEN`, replaces the login sharing; a NOTICE, not a
refusal: `/login` in the session still signs that session in, and a refusal
would block init's interview) and where a link that cannot be made
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
  driving adapter sees only the application layer (and the generated wire
  types, owner M7: pure data an API edge renders), never a domain's
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

- angzarr core `f6fed5e5` (`angzarr-diagnostic`, `angzarr-yaml`,
  `angzarr-presentation`; `angzarr-coordinator-grpc`'s `tls` feature, which
  `hook-listener` enables, is `Listener::tls`);
- angzarr-embedded `94f630a3` on angzarr-router `bec0c2bf`;
- angzarr-build `7547415e` (the markup generator, `angzarr-build` and
  `angzarr-codegen`, its own repository);
- angzarr-platform `a35789ef`;
- taskloom's crates `7af05807` and ctxloom-lib's `fb8e8212`, built on the
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
forge-github's HTTPS is the same stack (owner, 2026-11-12): reqwest's
provider-less rustls backend (`rustls-tls-manual-roots-no-provider`, no
default features), handed forge-github's own `ClientConfig` (`tls.rs`: ring,
and the roots of the system's certificate store as rustls-native-certs reads
them; a store with none is refused), so no OpenSSL is linked (`ldd` of the
Linux binary names no libssl or libcrypto). It added hyper-rustls and
rustls-native-certs to the lock and removed native-tls and openssl with their
helpers; rustls-platform-verifier was measured and not taken (its lock brings
jni and its family for Android).

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
hook or a shell's `sleep` would otherwise live on holding the pipes; killed
is SIGKILL sent to the group and the program reaped: a descendant dies when
the kernel next runs it, which can be just after the run returns, so a test
that checks a helper is gone waits for it, bounded), its
output bounded per stream, and the wait for that output bounded too
(`drain_grace`, two seconds), so a descendant that outlives a program that
exited cannot hang the caller. The program's environment is exactly the one
the adapter gives; its tests need `run-within`'s `test-support`, which waits
out ETXTBSY. Limits (os::process's): a descendant that calls `setsid` or
`setpgid` escapes the kill, and the program runs in a process group of its
own, so a Ctrl-C at the terminal does not reach it: a caller that must stop
a run on a signal hears it (`Os::signals`) and requests the `Stop` it ran the
program with (`run_within_stopped_by`: the tree asked to stop, `stop_grace`,
then killed; the outcome `stopped`).
`run_within_started` also calls a `started(pid)` once after a successful spawn
(never on a spawn failure). `run_terminal(spec, &Stop, started)` is the mode
for a program that owns the terminal: inherited stdio, NOT a process group of
its own (a Ctrl-C reaches it), no deadline, and a `Stop` (a cloneable flag the
caller sets, e.g. from `Os::signals`) that kills only the DIRECT child, not
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

## The daemon's one launcher, and the read models it registers (slice 1)

The user daemon (`ctxloom daemon serve`) hosts its children through ONE
launcher in the application ring (owner T18): `core/application/
daemon-application`'s `Daemon`. Its first step, `start_up`, is the sweep
(`run_launcher::Sweeper`, above); then `tick` catches up the read models,
plans each project on the board (`session-policies`), and takes the steps
(launch, start, feed, clear, end), recording what each child said as its
run's facts. Its ports: the engine registry (`HostedEngine`, an `Engine`
beside its `ChildEngine`, resolved by NAME; the wiring's `hosted_engines(wiring, program)`
composes claude and opencode, and nothing in the launcher names an adapter),
`ChildPlans` (the child's project's dry run on its own engine, and her git identity there), `HandOffs`,
`ChildHomes`, `Ears` (the root pumps each child's observations off a thread:
no thread in the core), `RunLocks`, `TokenSource` and the edge.
`ctxloom-wiring/src/daemon/serve.rs` only composes it, with the single-instance lock,
the runtime, the store and its core, the listener, the discovery file and
the pace.

The daemon has ONE principal, the user (slice 2). At each start it mints
the user bearer (T12: 256 bits, `credentials`' `OsTokenSource`) and a new
certificate, and serves its listener behind `runs-application`'s
`UserGate`, which keeps the bearer's SHA-256 alone, in memory, compares in
constant time (`TokenHash::matches`), and answers anything else (no
credential, another bearer, a run's hook bearer, another scheme) with a
bare UNAUTHENTICATED that says nothing of why (S11); an admitted caller is
the user, with `user_allowlist` (every session command and fact) on every
stream the daemon reaches. There is ONE daemon per OS USER, ever (owner,
after step 3/4: no clustering): its single-instance lock, `daemon.lock`,
is keyed to the account, never to `CTXLOOM_HOME` or `HOME`, at the place
ONE function says (`daemon::files::instance_lock_path`):
`<account home>/.ctxloom/daemon/daemon.lock`, the account's home being the
system's (`daemon::account_home`, angzarr-platform's `Os::user_home`: the
account database's, never a variable). That is the daemon directory of the
account's DEFAULT ctxloom home, so with no `CTXLOOM_HOME` the lock, the
discovery file and the daemon's files are one directory, and two ctxloom
homes on one account share the lock (no runtime directory: `XDG_RUNTIME_DIR`
is a variable, absent on macOS and Windows). THE DAEMON'S HOME is its
`CTXLOOM_HOME` when it was given one, else the ACCOUNT's home, never its
`HOME` (M4, `daemon::daemon_home`; `daemon serve` is wired with it,
`Wiring::at`), and the user's own home it tells a hosted child is the
account's too. Under `test-support` the account's home is
`CTXLOOM_TEST_ACCOUNT_HOME`, else the process's `HOME` (each test's
temporary home: no test reaches the real account), and
`CTXLOOM_TEST_INSTANCE_DIR` names the lock's directory instead (two homes
can share one); a client passes both to the daemon it spawns. Cost, for the
plain-build probe: a plain build has no override, so
`scripts/plain-destroy-check.sh`'s API probe takes the account's real lock
and writes its real discovery file, and so runs in CI alone (`CI` set) and is
skipped, and said, elsewhere. The daemon advertises itself in
the discovery file beside the lock, `daemon.discovery`: one
`ctxloom.v1.Discovery` in protobuf's binary encoding (S5;
`daemon-proto`, `run_ports::Discovery`'s codec, format 1; the old
`daemon.endpoint` line file is gone, hard migrate) naming the endpoint, the
public certificate (DER), the user bearer, the pid, the binary's FULL
versionator stamp (M3; the stamp `ctxloom version` says, threaded from the
binary, never `CARGO_PKG_VERSION`) and the ctxloom home it serves (field 8;
a file naming none is unreadable). A client whose ctxloom home is not the
serving daemon's is refused before it does anything, `ctxloom-wiring-daemon-
other-home` (exit 20, naming both homes, its fix the daemon's home or a
`daemon stop` from it), and never served by a second daemon (`daemon
status`, `daemon stop` and the hidden verbs; a hook's rediscovery passes
such a daemon over and delivers to its run's listener). The file is 0600
and its directory 0700, tightened on each publish whoever made it (the
single-instance lock may have); on Windows the platform sets no mode and the profile's
inherited ACL stands (M8). It keeps other users out, not the user's own
processes. `Discovery`'s `Debug` withholds the bearer, and the daemon's log
never holds it. Addressing at the daemon (owner, after step 3/4): the
target session travels ONCE, as the command's Cover root, and a command's
`requester` is its own field, set by the client (the hidden verbs and the
hooks from `CTXLOOM_SESSION`) and taken as sent. No run is sent to the
daemon, and the daemon does NOT check a fact's session against a run: the
session aggregate records nothing on a session that is not live. Accepted
cost: a buggy client could write to the wrong live session unnoticed. The
CLI run's own listener keeps its `HookGate` header check (a fact's session
must be its run's) until runs are hosted. A hosted child is told the user's home as a value
(`ChildSpec::user_home`, the daemon's `Place::user_home`, the account's
home, `daemon::account_home`), and its `PATH` is led by the program's directory as
the platform joins it (`run_ports::search_path_led_by`, on the daemon's
`Place::os`); the environment its launcher hands off is keyed as the
platform compares names (`os::Flavor::env_key`). ONE SESSION-SETUP PATH
(owner, 2026-11-12): every launch is set up by `run_launcher::set_up_launch`
(`LaunchSetup`: the `HomeSetup`, the workspace axis, the env and its owned
names), the launcher's `prepare` and the daemon's `Daemon::spawn_child`
alike: the engine placed in its workspace (`place_workspace`), its session
home set up there by `set_up_home` (`HomeSetup`: harp, context,
interactive, cwd, permissions, `session_home`, git user, the run agent's
surfaces; the engine's merge files read back, ruling 113, its files
written, its seeds once, and in a session home the user's git identity as
`.gitconfig`, ruling 112), `Prepared.workspace_files` written into its
checkout, and `CTXLOOM_ROOT` named. A hosted child's plan carries the run
agent's surfaces (`HostedChild.surfaces`, from `DryRun.surfaces`); its axis
is `none` (no workspaces, its mint records none) until its request carries
one. The daemon reaches the
child's project through `ChildHomes::records` (the same
`run_ports::SessionRecords`, `session-files`' `SessionDirs`) and her git
identity through `ChildPlans::git_user` (the wiring's `child_git_user`: git
in the project, in her launcher's environment with her home, the
account's, as `HOME`). The hosted engines are composed as every launch's
are (`oneshot::launch_engines`, `commands::hosted_engines(wiring, program)`):
calling the running program (ruling 163), claude over her own
`settings.json` (ruling 169), opencode seeding her `model.json` (ruling 167)
and over her config, each read AT EVERY LAUNCH (T21): a hosted engine is
composed anew for each launch (`HostedEngine::for_launch`, the wiring's
`daemon::launch_engines_for`), under the account's home, where the LAUNCHER
finds her settings: `daemon launch` hands off its caller's
`daemon::SETTINGS_PLACES` (`CLAUDE_CONFIG_DIR`, `XDG_CONFIG_HOME`,
`XDG_DATA_HOME`, `XDG_STATE_HOME`) beside the child's environment
(`daemon_application::HandOff`'s `launcher`, the hand-off file's format 2,
a `@NAME=value` line each), never into it, and never the daemon's own
environment. A hosted child's session home is prepared with its agent's
permissions document for the engine, verbatim (rulings 133-137,
`run_application::HostedChild::permissions`). Not yet:
`run_ports::check_command_line` does not measure a hosted child's command:
it carries no prompt (its turns are fed over the child's stream), only
paths, model and effort.

A daemon is LIVE when it answers the standard `grpc.health.v1.Health`
Check SERVING (T16, M11: `hook-sink-grpc`'s `serving`, tonic-health's
client and messages, a direct dependency at the version the lock already
held for angzarr's server, over TLS pinning the certificate the discovery
file names, presenting no bearer, within the wiring's `HEALTH_WAIT`, one
second). The single-instance lock (`daemon.lock`,
`DaemonFiles::hold_instance`, the platform's `file_lock::take_current`) says
only that one daemon serves: its file is made once and never removed or
replaced by ctxloom, and a serving daemon checks at each tick that the file
at the lock's path is still the one it locked (`DaemonFiles::still_holds`:
`Fs::same_file`, device and inode, volume and file index); removed or
replaced, it excludes nobody, so the daemon exits (closing the gap of two
daemons at once). `daemon status`, `daemon stop`, a hook's rediscovery and a
client's `ensure` all ask the health check, never the lock, so a daemon that
holds the lock and does not answer (stopped, hung) is not serving, and a
stale port another process took is never handed anything. A serving daemon
looks at its discovery file each tick: gone, it publishes it again (it still
holds the single-instance lock); naming another daemon (its lock file was replaced, and
another took it), it exits, and withdraws only its OWN advertisement
(`DaemonFiles::withdraw_own`), never the other's. A client with no live
daemon spawns `ctxloom daemon serve` and waits for a live one to advertise
itself (the one spawned or another client's, whichever took the single-instance lock)
within `SPAWN_WAIT`, eight seconds: S16's ten (`HOOK_BOUND`) less a
delivery's two (`DELIVERY_MARGIN`). A stale file is left for the new daemon
to publish over. The spawned daemon's environment is `daemon_env`'s:
`PATH`, `TMPDIR`, `LANG`, `LC_ALL` and `run_ports::WINDOWS_STARTUP_VARIABLES`,
matched as the platform compares names (`os::Flavor::env_key`, so Windows'
`Path` passes), and the spawner's `CTXLOOM_HOME` only when it set one;
NEVER the spawner's `HOME`, `USERPROFILE`, `APPDATA`, `LOCALAPPDATA` or XDG
bases (its confinement: the daemon's home is the account's), nor anything
else of the spawner's. It is started DETACHED (`os::process::spawn_detached`:
`setsid` on unix, so it leads a session of its own with no controlling
terminal; `DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP`, breaking away from
a job, on Windows), so no hangup, Ctrl-C or kill of the client's group
reaches it; proved on Linux through procfs (its session id is its pid, and
none of those variables is in its environment), unverified on macOS and
Windows. IDLENESS counts the runs started and not ended (M9:
`Daemon::live_runs`, from `pending-launches`, any process's: the CLI's
interactive sessions and one-shots as well as its hosted children), asked
at the idle limit, after the runs whose owner is gone are ended by the
start's sweep (`Sweeper`, of every run it does not host itself); so a
daemon exits only once it has hosted nothing, had nothing to do and known
no live run for its idle time. The launcher runs inside the runtime's
`block_on`, on the thread that started it, not on its two workers, which
serve the listener (M10); the API's use cases run on the blocking pool
(below).

THE DAEMON'S API (slice 2 step 5; `proto/ctxloom/v1/daemon.proto`'s
`Ctxloom` service). Its package is `ctxloom.v1` (owner OQ-1, 2026-11-13: one
versioned package, `proto/ctxloom/v1/`, for the daemon, its sessions and the
library; it was `ctxloom.daemon.v1`, so a refusal's `Any` type URL and every
method path changed, no field number did; the domain packages
`ctxloom.sessions.v1`, `runs`, `trust`, `approvals` are not the API and keep
theirs). The daemon serves it beside angzarr's coordinator on its
ONE listener (`hook-listener`'s `HookListener::serve_tcp_beside`), each RPC
admitted by the same `UserGate` first. Its use cases are
`core/application/daemon-api`'s `Api` (behind `Calls`, boxed, so the edge is
not generic): `GetDaemon` (pid, endpoint, full stamp, start, the runs it
hosts as `HostedRun.Kind` `CHILD`, idle and idle-limit seconds; never its bearer),
`StopDaemon` (refused `DaemonHosting` naming the harps while it hosts live
runs, unless `even_if_live`, which ends them first, S12), `ResolveProject`,
`GetProject`, `GetSession`, `ListSessions` and `SearchSessions`, read from
the WARM `session-index` the kernel keeps (a session's state its lock's,
`SessionStates`); never by replaying the store per call. What the loop
knows (its endpoint, the runs it hosts, its idle time, a stop asked) is the
`Hosting` port, the wiring's `Shared`, told at each round; the old stop
FILE is gone (`daemon stop` is `StopDaemon`). The gRPC edge is
`adapters/driving/daemon-api-grpc` (tonic stubs generated by
`daemon-proto`'s `server`/`client` features, tonic-prost-build, messages
extern-pathed onto its prost types). Every message is also its proto3 JSON
(OQ-21, owner 2026-11-13): `daemon-proto`'s build compiles the messages a
second time from the same descriptor set, the well-known types the package
uses (`Timestamp`, `FieldMask`) compiled INTO the crate
(`daemon_proto::google::protobuf`, converting to and from prost-types' own
with `From`), and `pbjson-build` (`=0.9.0`, runtime `pbjson` `=0.9.0`)
generates every message's serde, proto field names kept; the well-known
types' JSON is hand-written (`daemon-proto`'s `wkt.rs`). The session verbs
and `daemon status|stop` print it; its spike holds it byte-identical to the
hand-written renderer it replaced (`session_cli::proto_json`, now test-only)
on the same messages (`proto_json.test.rs`; session-cli's tests take serde_json's
`preserve_order`, as the binary has it through engine-opencode's
agent-client-protocol). GATE FIRST, EVERYTHING (owner, after
step 5): the whole generated server is wrapped in `daemon_api_grpc::Gated`,
so every request to the service, served RPC or not, presents the user
bearer (`UserGate`) or is the one bare UNAUTHENTICATED, and a caller that is
not the user learns nothing, not even which RPCs are served (the listener's
health service is angzarr's own, and stays ungated: liveness, T16). Each
served RPC's use case runs on the
runtime's BLOCKING pool (`spawn_blocking`, M10); a refusal is answered under
the gRPC code its `(refusal_status)` declares (S11), the typed refusal packed
in `google.rpc.Status.details`; a failure is INTERNAL. `DeleteSession` and
`SweepSessions` are served only in a `session-destroy` build (M5, owner
2026-11-12: `daemon-api-grpc`'s feature of that name, which the wiring's
turns on; the crate dev-depends on itself with it, as `session-cli` does):
`Api::delete_session` and `Api::sweep_sessions` (`daemon-api`'s `destroy`),
over their own ports, `Purges` (the one store, the wiring's `StorePurges`
over `InProcessCore::store_handle`, a holder let go with the listener before
the close) and `Places` (a session's project by its directory: its session
directories and locks, its worktrees with git in the caller's environment
(`env`, S7), its checkouts under the account's ctxloom home by the
session's project id (`Places::worktrees`, `isolation_ports::worktrees_root`),
its configuration read from its REGISTERED root, the wiring's
`ProjectPlaces`); a sweep's purge age is the request's, else
`session_purge_age` of its `project` (refused `AgeInvalid` when it is no
age, `ConfigUnreadable` when the configuration does not read). Their
answers carry the plan (additive: `DeleteSessionResponse`'s harp, applied,
files, bytes freed, events destroyed and worktrees; `SweepSessionsResponse`'s
rows, counts, cutoff and each removal). Each carries the client's
`request_id` (S8: `Client::with_request_ids`, a UUIDv7 minted once per call,
the SAME on its one retry on a new daemon), and an applied destroy's answer
is kept by it for the daemon's life (`destroy::Applied`, in memory), so a
retry already applied is answered with its first result; after a restart a
retried `DeleteSession` whose session the store says is destroyed (its name
claimed by it, its stream empty) is answered applied from those facts,
nothing counted (accepted cost: a fresh `session remove --yes` of an
already-destroyed session, which carries a new id, is answered so too, not
`SessionNotFound`), and a retried sweep runs again over what is left. A
destroy call's deadline is its own (`DaemonClient::destroying`, the wiring's
`DESTROY_WAIT`: worktree-git's 30 s timeout times six git invocations times
ten worktrees, plus `CALL_WAIT`), not a read's five seconds; a call past its
deadline is unreachable (the client's own timer, no channel timeout), as is a call whose
connection the transport dropped or reset (a status with a source: the
daemon gone mid-call), never a failed call. Every other RPC is the generated
default, UNIMPLEMENTED, to the user bearer alone (a plain build's
`DeleteSession` and `SweepSessions` among them); `scripts/plain-destroy-check.sh` probes the
plain binary's daemon over curl (HTTP/2): `GetDaemon` and `DeleteSession`
without a bearer are 16, and `DeleteSession` and `SweepSessions` presenting
the user bearer (read from the discovery file's field 5, handed to curl in a
0600 header file) are 12. The daemon's own refusals are
`ctxloom.v1`'s (`NameInvalid`, `SessionNotFound`, `ProjectNotFound`,
`ProjectUnknown`, `DaemonHosting`); their status is the generated
`daemon_proto::Refusal::REFUSAL_STATUS`, read by `daemon-proto`'s build from
each message's option (no markup domain generates it for a package with no
command). `daemon_api::rejection_of` tells a typed refusal in its crate's
words (`daemon-api-*`, or the sessions domain's).

The client side (owner M7): `core/ports/daemon-ports`' `DaemonApi` (one
method per served RPC, each answering the service's own message or a
`Called`: refused with its typed refusal, unauthenticated, unimplemented,
unreachable, failed), implemented by `adapters/driven/daemon-client` over
tonic, pinning the discovery file's certificate as its sole root and
presenting the user bearer, each call within a deadline; a detail that is a
`google.rpc.ErrorInfo` is no typed refusal. The application's client use
cases are `daemon-api`'s `stop`, `resolve` and `reconcile` (`CallError`:
a refusal 20, a failed call 10), and its `Client` (slice 2 step 6), what
the commands that are the daemon's clients call: `daemon status` and
`daemon stop` and the session reads (`SessionReads`: `session list`, the
bare noun, `search`, `show`; `session_cli::read`, which opens no store).
A `Client` reaches its daemon through daemon-ports' `Daemons` (the wiring's
`daemon::Reach`): `serving` finds the one the discovery file names, live
and this home's, and never starts one (S14: `daemon status|stop` are
answered locally when none serves, status printing `no ctxloom daemon is
serving`, the empty message in json); `reach` reconciles a daemon of
another stamp first (M3), else spawns one and waits (S16's bound), and with
none started is `Unreached::NotStarted`, `daemon-ports-not-started` (exit
10, naming its log as `~/.ctxloom/daemon/daemon.log` under the home);
another home's or a busy other version's is `Unreached::Declined` (20).
Each call resolves its project first (`ResolveProject`, S6; outside a
project a read of its own is `ProjectUnknown`; `--all` lists `projects/-`),
sends the caller's WHOLE environment in every request (`EnvVars env = 94`,
S7; the reads ignore it), and a call whose daemon could not be reached
mid-call is made once more, whole, on the daemon reached again (S8). Their
`--format json` is the answer's proto3 JSON (ruling 146, the message's
generated serde (OQ-21): proto field names, a default left out, an enum by
its name, a 64-bit integer as a decimal string, a Timestamp RFC 3339 with
`Z` and 0, 3, 6 or 9 fraction digits, `daemon-proto`'s `wkt.rs`; the
hand-written `session_cli::proto_json` it replaced is test-only, the
oracle the generated JSON is held byte-identical to);
their text is the table and the facts it was (a session's owner and
children by resource name). The `Session` the daemon answers carries what
`show` says: its native id, its project's root, its output folder, its
rotations and (GetSession) its pending messages; a live one's end is
blank. `session remove` and `session sweep` are clients too (owner,
2026-11-12; `SessionRemovals`, `session_cli::destroy`): a removal answers
`Removed::Done` or `Removed::Refused` with the plan its refusal carried,
their previews and `--detailed-exitcode` 1 as before, their json the
answers' proto3 JSON (`DeleteSessionResponse`'s, `SweepSessionsResponse`'s), an
age (`--purge-older-than`, or `session remove --older-than`) that is none
refused before the daemon is asked, naming the flag given;
the write's request id (S8) is not sent yet, so a retry on a new daemon of
a removal that applied answers `SessionNotFound`. Not yet clients: the
library, config, deps, init, doctor and run.

VERSION SKEW (M3): a client whose stamp is not the serving daemon's asks it
(`GetDaemon`); hosting nothing, it is stopped (`StopDaemon`), waited out and
the client starts its own; hosting live runs, the command is refused,
`daemon-api-version-skew-busy` (exit 20, naming both stamps and the harps,
fix `ctxloom daemon stop --even-if-live`). Today the hidden write verbs
(`daemon send|close|clear|launch`) and `daemon resolve` reconcile first;
under `test-support`, `CTXLOOM_TEST_STAMP` names the client's stamp.

PROJECT REGISTRATION (owner, 2026-11-12). `ResolveProject` is
register-or-recheck: the client sends what IT resolved (`Wiring::
registration`): its project id (taskloom's project-registry identity, so a
worktree is its primary checkout's), its root, the repository's git common
dir and its root commit (worktree-git's `Git::vcs_identity`: `rev-parse
--path-format=absolute --git-common-dir`, `rev-list --max-parents=0 HEAD`, of
several roots the lexically least), and optionally the project name it
expects. The daemon names the project on first sight (S54, fresh harps),
then sends `RegisterProject` (projects aggregate; keyed by all it says and
the registered identity it is judged against):
recorded once (`ProjectRegistered`: root, common dir, root commit), and on
every later call only the ROOT COMMIT is rechecked (a worktree's root
differs; its common dir is the primary's): another is refused
`ProjectIdentityChanged`, an expected name not the registered harp
`ProjectHarpMismatch`, each naming the registered project; an unnamed
project is refused `UnnamedProject` (`sessions-project-not-named`). Outside
git the identity is empty, and ADOPTED (owner, after step 5): the first
recheck with a common dir and a root commit records them,
`ProjectIdentityAdopted` (the root stays the registered one), and the
comparison is strict from then on (the empty identity again is refused as
changed). Every reader folds a project's stream with the domain's one
`sessions::fold_project` (the aggregate, the session index, the API). It answers the
`Project` (`projects/{harp}`, the registered root, the id, the identity);
`GetProject` reads it from the warm index (`SessionIndex::project_named_by`,
`ProjectRow`). The hidden `ctxloom daemon resolve` is its client today;
step 6's clients call it first.

The daemon's kernel registers its read models as projectors (S32):
`session-index`, `pending-messages` and `daemon-board`, by those names, so a
`HandleSync` (T16: `run_ports::Edge::serve_sync`, the kernel's `handle_sync`
behind the exposure; `EdgeCommands::handle_sync` answers core's `Synced`)
waits for the ones it names and answers each one's view: the session as
`ctxloom.v1.Session` (`daemon-proto`, compiled from the daemon's
drafts), named `projects/{project harp}/sessions/{harp}` (`SessionRecord.name`,
`sessions::SessionName`) once its project is named (the `projects` aggregate,
S54), its title the one set (S55); the session with its pending messages;
the session's state. A CLI command's core registers none
(`InProcessCore::start`); the daemon's does (`start_with`, `Projectors::
filled`). There is ONE fold per read model: `core/read-model/held-projection`'s
`Held` is a shared handle, the same instance the kernel runs inline and the
daemon's catch-up drives, each page folded once (one before where the fold
is on its stream is passed over; a book from 0 with another first event is
folded afresh; a book past where it is is a gap, read from the store first,
else left for the catch-up).

## Storage is angzarr-store-sqlite

ctxloom's store is ONE per user (owner T14): angzarr-store-sqlite's
`SqliteStore` (through angzarr-embedded's `sqlite` feature), opened at
`<ctxloom home>/.ctxloom/ctxloom.db` (the home resolved as everywhere:
`CTXLOOM_HOME` first) by `ctxloom-store-sqlite` (the file name, the placement
and the `ProjectStores` port, over ctxloom-lib's `sqlite-store-opener`, which
refuses a store from before the embedded store, `ctxloom.db.pre-embedded`
naming its backup). Every project's streams are in it, each rooted on its
resource name (slice 2 re-root, above), so nothing routes by file: there is no `route.rs` and no routing metadata. taskloom's
`state.db` stays taskloom's, per project, never the same file (T2, D26). A
write for a project still makes its `~/.ctxloom/projects/<id>/` directory,
which taskloom's registry lists projects by; ctxloom keeps nothing in it. A
project that still has a store of its OWN there (`projects/<id>/ctxloom.db`,
from before) is refused per command (T17, S48): exit 20,
`ctxloom-store-sqlite-per-project-store`, its fix naming the file to remove;
nothing is imported (accepted cost: that history is lost), and the file is
left as it is. Hooks are not refused (a hook never fails its engine); the
store port refuses it too, for whatever reaches a store past the command
line.
The store is its owner's alone (owner ruling 2026-11-12): every open, to
write or to read, sets the store's directory, ctxloom's home `.ctxloom`, 0700
and its file, `-wal` and `-shm` 0600 (the opener's, so `ctxloom-store-sqlite`
sets no mode of its own), so an existing store open to others is tightened on
its next open; a mode that cannot be set fails the open
(`sqlite-store-opener-restrict`). A project's `projects/<id>/` directory,
which holds nothing of ctxloom's, is made 0755 inside it. On Windows no mode
is set: the profile's ACL holds it (ruling M8).
A store is closed, never dropped, where its file matters after (ruling 131):
angzarr-embedded's `SqliteStore::close` waits for the connection's close, so
the last close's checkpoint and the log's removal are over when it returns
(dropped, they are left to sqlx's detached thread, which an exiting process
need not let run). `SqliteStores` holds the one store, closes a read's before
it reopens it to write, and `SqliteStores::close` closes the one it holds; the wiring closes the sessions use cases' stores after every `session`
command and a hook's bind (`Sessions::close`; a failed close fails a command
that had not), the seed's and `test open-store`'s, a run's at its end, and
the daemon's at its exit (after the launcher, the edge and the listener,
the read models it kept of those its kernel registers are let go too: each
holds the store through its gap fill, `StoreFill`), and on a start that
fails before it serves (the certificate's mint, the bind, the discovery
file's publish: what it started is let go, the log says "the start failed"
then "the store is closed").
A run's store has holders past the use case: the core's application and
edge, the use cases' edges, and the hook listener's connection and stream
tasks, which tonic spawns itself, so a call still in the handler keeps the
coordinator, its edge, the application and the store. `HookListener::stop`
is angzarr's `Running::shutdown_within(STOP_GRACE)` (ruling 140): past the
grace it ends every connection still open (the call is cut) and returns only
once every copy of the service is released. So the end lets each go before
the close: the listener stopped (by the run use case), `Runs::into_core`
(every part but the core, with their edges), then `InProcessCore::close`,
which consumes the core and only then takes the store by value (the core keeps
it in `CoreStores`, not `OneStore`, to have it back) and closes it, on the
run's own runtime, which is not shut down for it. A holder still there is
`Unclosed::StillHeld`, and a close that fails is `Unclosed::Failed`: either
fails the run (exit non-zero), never a quiet drop. A read's connection is read-only, so an empty
`-wal`/`-shm` may stay after a read; a write leaves neither
(`store_close.test.rs`). All file I/O goes
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
are refused like any unknown format, exit 20. Errors are `ctxloom: <message>`
with an optional `  fix:` line in text, and the envelope `{code, args,
message}` on stderr in json, plus `fix` (the same text as the `  fix:` line)
when the catalog has the error's `<code>-fix` id, the key omitted otherwise,
and `refusals`, each refusal an aggregate refusal holds reported the same way,
in order, the key omitted when there are none (cli-support's `Reported`, which
`ctxloom-cli`'s `report` writes through `say_failure`; angzarr-presentation's
`Envelope` has neither field). `-h` works at every depth, there is no `help` command, an unknown
verb says "Did you mean" and lists the verbs, `-v` is accepted everywhere
and changes no output.

Exit codes (owner rulings 149, 152, 153; a hard migrate from Go's 0, 1, 2,
3) follow ctxloom-lib's convention, `docs/exit-codes.md` in that repository
(blocks: 0-9 successes, 10-19 failures, 20-29 declines, 128+n signals; the
more severe of two outcomes, the higher detailed code, wins), encoded as
ctxloom-lib's `exit-code` crate's `Exit` (re-exported by `cli-support`). An
application error says its class by implementing `ExitClass`; a `Failure`
carries an `Exit`; `ctxloom_cli::run` turns it into the status with
`Exit::code(detailed)`, the one place the collapse lives. ctxloom's codes:

- **0** ok (an apply, a read, or a plan or preview that succeeded);
- **1** would change: a plan or preview that succeeded and would act,
  returned ONLY under the global `--detailed-exitcode` (without it, 0):
  `run --dry-run` whose plan has no fatal finding (it would launch);
  a removal's preview without `--yes` (`bundle|fragment|command|skill|
  profile|agent|companion remove`); `remote remove`
  without `--yes`; `deps upgrade` without `--yes` with a pin to move or
  drop; `clean` without `--yes` with a target present; `session remove`
  without `--yes` that is not refused; `session sweep` without `--yes`
  with a planned destroy; `agent edit --engine` without `--yes` whose
  new engine cannot carry what the old one did (the swap preview);
- **10** failed: ctxloom tried and something broke (an adapter's error,
  I/O, the store or the kernel, an engine, git, a clone, a fetch, a
  publish, a pull, the retry budget);
- **20** refused, a decline: the request or the state forbids it, by kind,
  whatever Go said (owner ruling 152): a usage error (clap's own 2
  mapped), an unknown format or verb, a config the strict decode refuses
  or that does not read, a name nobody has (a session, a bundle, an item,
  a profile, an agent, a remote), a taken name, a live session (a run's
  or session's lock another process holds; a lock file that cannot be
  made is 10: the lock ports answer `LockError::Held` or `Failed`), any
  refusal of a domain or the read model or a write, a container verb with
  no runtime, a refused consented refresh, a refused delivery, a hook its
  service refused, and doctor's `fail` row (the refusal it guarantees);
- **21** fatal findings: ONLY the run plan's (a dry run's or a launch's
  startup abort, materialize's strict refusal, a fatal isolation finding);
  a launch both refused and with fatal findings is 21;
- **128+n** stopped by signal n (ruling 114).

Each application error says its class in its `ExitClass` impl. An error
wrapping another crate's says which it is: `AppError::Cause` (an adapter's:
10) and `AppError::Declined` (`AppError::cause`, a read-model or write
refusal: 20) in library- and deps-application; `SetupError::Classed` keeps
a library error's own class. An engine's own status (an interactive
session's, init's interview) is said in these classes by
`Exit::from_child_status`, never passed through: 0 ok, 128+n the signal,
any other failed (10).

## The library as AIP resources (OD-40, the spine `aip/spine`)

The library's API messages are `proto/ctxloom/v1/library.proto`, in the
`Ctxloom` service's package (`ctxloom.v1`) and generated by `daemon-proto`
with the rest; daemon.proto's service names their methods. Its header is
the spec of the resource tree (`projects/{project}/...`, `users/me/...`
outside a project, OQ-3; a bundle's origin in its name, OQ-4), the ID
grammar (OQ-6: a bundle's or item's ID is `^[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`;
a dependency's 16 hex digits of its identity's SHA-256, OQ-15; an engine's,
agent's, companion's and remote's its config key; the item grammar is
enforced where a write MINTS a name, `library::write::item::valid_id`
through library-application's one `write::require_id`: bundle create
(nested `team/rules` refused, OQ-5, a hard migrate of Go's nested create)
and import, fragment and command create (either form, and the bare bundle
the reference form makes), `bundle edit --add-fragment|--add-prompt|
--add-mcp` of a name not already there, skill create and import, profile
create and import, and `mcp server set` of a server the bundle lacks;
`library-application-write-bad-id`, exit 20, with a `-fix`; a name already
on disk is read as before, its refusal B1's) and the conventions: a
default left out, presence (`optional`) on `applied`, `held`, `default`,
`resolves` and `no_distill` alone (OQ-7; the session's too:
`DeleteSessionResponse.applied` and `WorktreeReport.applied`, so a
`session remove` plan prints `"applied": false`); `uint32` counts and tokens,
`uint64` bytes (OQ-12); a free-form document (permissions, the config) a
`google.protobuf.Value` (OQ-11, owner ruling 2026-11-13, superseding
Struct: printed as written whatever its kind, so a permissions document
that is a list or a word round-trips; no proto of any engine's
configuration; numbers doubles for display, read exactly under
`arbitrary_precision` by `daemon-proto`'s `wkt.rs`, which also watches
library.proto in its build script);
every write its OWN response (`CreateBundleResponse`, ...; OQ-9), each with
`path`, `applied`, `detail`, `apply_command` (OQ-8), `needs` 90, `etag` 91
and `notices` 92. Every enum of `ctxloom.v1` follows AIP-216 and AIP-126
(owner ruling 2026-11-13, OQ-B1): a state is `State`, nested in the message
it describes, `STATE_UNSPECIFIED` its zero; another enum's zero is
`<NAME>_UNSPECIFIED`, its other values unprefixed, nested in its one owner
(`AssembledFragment.Form`, `Launch.Kind`, `Launch.Stdio`, `HostedRun.Kind`, `Session.State`,
`RemovalFile.Class`, `Run.Mode`, `RemovalRefused.Reason`, `Bundle.Origin`,
`Bundle.View`, `Agent.View`, `EngineAuth.State`, `Config.View`); the three
shared by several messages stay top-level, unprefixed but their zero
(`DistilledState`, `ItemView`, `ItemKind`), so proto3's package-wide value
names do not collide. JSON prints the bare value (`"state": "ENDED"`,
`"class": "MACHINE"`); the domain packages keep their prefixed style.
`EnvVars` and `Globals` moved to needs.proto (library.proto
cannot import daemon.proto, which imports it). Not yet written: the B4
custom methods' responses (push, browse, discover, pull, check, upgrade),
their shapes on a ruling. `PlanStale` stays FAILED_PRECONDITION: OQ-10 rules
ABORTED, and angzarr's `RefusalStatus` has no such value yet.

Conversion is at the CLI's edge (design §6): library-application's views
are unchanged, and each noun's module in `library-cli` (`noun/<noun>.rs`,
design §8.4) is the ONE path from its views to its messages, over the
helpers they share in `api.rs`, printed as the message's generated proto3
JSON. One file per noun, so the converting branches touch none in common:
`noun/<noun>.rs` and `.test.rs` (read verbs, write verbs, the mapping),
the binary's `library_cli_<noun>.test.rs` and `fixtures/parity/<noun>.tsv`
(B1 `bundle`, `item` for fragment, command and `fragment premises`,
`skill`, `mcp`, `profile`; B2 `agent`, `config`, `companion` with
`loadout`, `run` for `run --dry-run`; B3 `search`, `context`); the shared
ones (`lib.rs`'s dispatch and prelude, `api.rs`, `write.rs`'s helpers,
`text.rs`, `commands.rs`, the harnesses `lib.test.rs`, `write.test.rs` and
`library_cli.test.rs`) are not a noun's. A resource
name's parent comes from `library_cli::Names`, asked only when a json
answer prints one (text reaches no daemon): the wiring's is the daemon's
`ResolveProject` (`daemon_api::Client::library_parent`, the wiring's
`daemon::library_parent`; OQ-2, so a json answer may start the daemon), or
`users/me` outside a project, where no daemon is asked (OQ-3). The project
is the ctxloom project, the nearest `.ctxloom` up (or `CTXLOOM_ROOT`), as
every library read finds it (`Wiring::library_registration`, the names'
alone; the sessions' client keeps `registration()`, the registry's work
root): its identity is read at that root when bound and minted there once
when no run has named it, so a read never writes outside the project's own
`.ctxloom/project-id`, and a subdirectory names its root's project. Converted:
the `engine` noun (the worked example): `engine list` is
`ListEnginesResponse` (`{engines: [...]}`), `engine show` an `Engine`
named `projects/{harp}/engines/<engine>`, its agent rows' name `agent`,
their settings flat (`agents[agent=x].model.value`), the auth state
`PRESENT|MISSING|NOT_NEEDED` (`EngineAuth.State`), an unset model no `value`; its
text is unchanged. `scripts/plain-engines-check.sh` reads the TEXT
listing, so `just build` reaches no daemon. The catalog nouns (B1,
`aip/catalog`): `bundle list|show|view`, `fragment|command list|show`,
`fragment premises`, `skill list|show`, `mcp server list|show`, `profile
list|show` and every write of theirs (but `bundle push|move`, B4's) print
their AIP resource or their method's own response (`ListBundlesResponse`,
`Bundle`, `CreateBundleResponse`, `DeleteFragmentResponse`, ...), mapped by
`noun/<noun>.rs` over library-cli's `catalog.rs` (the one place a catalog
name becomes a resource name: `demo` is `<parent>/bundles/demo`,
`<remote>/<b>` `<parent>/remotes/<remote>/bundles/<b>`,
`ctxloom+companion:<c>` `<parent>/companions/<c>/bundles/loadout`; an item
is `<bundle's>/<fragments|commands|skills|mcpServers|profiles>/<id>`, a
project's own profile `<parent>/bundles/project/profiles/<p>`; a
configured reference (a profile's parents and bundles) is a name where its
text places it and keeps its text where it does not, a remote's URL
reference among them, OQ-16, no notice yet; an ID on disk that breaks the
grammar is named as it is). `bundle show` lists each child as a
`BundleItem` naming it (`fragments[item$=/fragments/x]`, OQ-14), `bundle
view <b>` is the `Bundle` with its `files`, `bundle view <b>#<kind>/<i>` the
item's own resource (its `body` the bytes served); `mcp server show <n>` is
`ListMcpServersResponse` filtered by the ID; `fragment premises` the
`PremiseIndex` singleton (`<parent>/premiseIndex`, each entry's `fragment`
a name). A removal's preview is its `Delete<Noun>Response` with `applied:
false`, `detail` and `apply_command`; an edit that changed nothing has
`applied: false` and no `changes`; `mcp server set` says `created` when the
bundle lacked the server. The verbs that answered in text only (`fragment|
command show`, `profile create|edit|modify|export|import`, `mcp server
edit`) answer their message only when `--format json` is asked for by name,
and stay text by default (an agent's shell runs `fragment show` off a
terminal). The views gained what a name needs and the JSON never printed
(`BundleRow::commit`, `BundleShow::listing`, `BundleView::viewed`,
`ItemShow::bundle`, `ProfileRow|ProfileShow::entry`, all `serde(skip)`).
Go parity ends for their JSON (`library_parity`'s `aip_json`: such a row is
held to its exit code and to answering JSON; its text rows stay Go's).
Every other noun still prints its view, until its branch (B2
configuration, B3 queries, B4 remotes) converts it.

B3 (`aip/queries`) converted `search` and `context assemble`. `search` is
`SearchLibraryResponse`: a local hit's `resource` is its item's name under
its bundle (`projects/{harp}/bundles/demo/fragments/golang`; a project
profile under the bundle `project`, an MCP server under `mcpServers`), its
`kind` the `ItemKind` value; a remote hit, not installed, has no name, its
`item` the name in the remote and its `pull_reference`; a zero count and
empty lists are left out (`{"query": "x"}` when nothing matched).
`context assemble` is an `AssembledContext` (daemon.proto, its defects
fixed: `tokens` uint32, OQ-12, and `profiles`, `missing_fragments` and
`missing_tags` added, design §5.9): its profiles named, each fragment it
used `{fragment, form}` (`AssembledFragment.Form`, `ORIGINAL|DISTILLED`), a fragment asked for and
not found kept as the text it was asked by (OQ-16); its `notices` field is
left unfilled, the notices said on stderr. Where an item is comes from
library-application, which knows the catalog: `Located {bundle, item}`,
the bundle as the catalog names it (`<remote>/<bundle>` for an installed
one, a companion's loadout by its `ctxloom+companion:` reference),
`Project::locate` (a remote's URL reference resolved to that name), on the
`#[serde(skip)]` fields `SearchHit::located`, `Assembled::profiles_located`
and `used` (with `tokens`); library-cli names it (`noun::search::
bundle_name` and `item_name`, as B1's `catalog` names bundles). Go parity
ends for search's JSON: its rows left `fixtures/parity/search.tsv` with
their goldens, and the text rows stay.

B2 (`aip/configuration`) converted `agent`, `companion`, `config` and `run --dry-run`: `agent list` is
`ListAgentsResponse` (`{agents: [...]}`, an empty one `{}`), `agent show`
the `Agent` itself (no `definition` level: `engine`, `profiles`,
`engines.<e>.model` at the top, `resolved` beside them, an env key's name
`variable`), each agent's `default` printed true or false (library-cli asks
`Library::agent_default` once more, for a json answer only); `create`,
`edit` and `remove` answer `CreateAgentResponse`, `UpdateAgentResponse` and
`DeleteAgentResponse`, the agent under `agent` (a preview's and a
removal's by name alone), `applied`, a preview's `apply_command` and
`detail`, through `api::written`, the write half of the one path. A
reference (an agent's `engine` and `profiles`) still prints its configured
text, as the engine noun's agent rows do. `agent default` is still text
only. `companion list` is `ListCompanionsResponse`, each companion named
`projects/{harp}/companions/<name>`, its `command`, and `resolves` (was
`exists`); `companion add` answers `CreateCompanionResponse` (`applied`
when the registration is new or changed, `resolves` true: its program
answered the probe) and `remove` `DeleteCompanionResponse`; `bin` is left
unset (a companion is a name and the command that runs it, not Go's
`ctxloom-companion-<name>`). `loadout` is unchanged. `config show
[section] [--raw]` is the `Config` singleton, `projects/{harp}/config`
(`users/me/config` outside a project), the document it printed before
under `document` (so `agents.default.engine` reads
`document.agents.default.engine`); `config create` and `edit` stay text
only. `run --dry-run` is `CreateRun` with `validate_only`, its json the
`Run` it would create (daemon.proto's `Run`, amended per design §5.9:
`settings` an `AgentSettings`, `tokens` uint32, and `workspace`,
`runtime`, `prompt` and `fragments` added; the draft's `Setting` is gone),
named nothing, so it asks no daemon; a finding's false `fatal` is left out. Go parity ends for these nouns' JSON (design §8.1): their JSON rows
are retired from `fixtures/parity/`, the text rows stay; `config show`'s
JSON rows still compare Go's answer, the resource's `document`
(`library_parity`'s `as_go_printed`), and the dry run's compared keys
read a default proto3 JSON leaves out as Go's printed one (`printed`).

The feature steps read proto3 JSON (design §8.1, the binary's
`library.test.rs` and `gherkin_common.test.rs`): a selector `[key$=suffix]`
picks the element whose `key` ends so (`engines[name$=/engines/opencode]`);
`{project}` in a step's path or value is the project's harp, which the
harness asks the hidden `ctxloom test project-name` (test-support; it
prints the `Names` parent, `projects/<harp>` or `users/me`); an absent field
reads as its default (`as "false"`, `"0"` or `""` passes where proto3 JSON
left it out, anything else does not), and an absent list has 0 entries.

## What an engine cannot carry (M6, j002000)

An engine's word on what it cannot carry is its surface half's
`EngineSurfaces::hook_losses` (the unified hook events it has no form for,
each with why), handed to the library as data by the composition root
(`Library::with_hook_losses`, from `commands::engines()`, the mocks
included under `test-support`). A profile's hook on such an event is LOST
on that engine (ctxloom's own callbacks are not the profile's):
`library_application::NotCarried {engine, surface: hooks, event, reason}`.
`agent show` gives the agent's losses on its engine (`not_carried`, text
`Not carried by <engine>:`); doctor's `DOCTOR-CHECK-NOT-CARRIED-r8` warns
of each agent that has one (`Doctor::reporting_losses`, the library's
`agents_losses`); and `agent edit <name> --engine <new>` whose new engine
loses something the old did not is the SWAP PREVIEW without `--yes`
(`Library::agent_edit`, `AgentWritten::Preview`, `SwapPreview`; its json
`UpdateAgentResponse`: `applied: false`, `from`, `to`, `not_carried`,
`apply_command`, OD-40; the edit
judged as the write would judge it first; nothing written; exit 1 under
`--detailed-exitcode`). An edit that changes no engine, or loses nothing,
writes as ever, and `agent create` never previews. Only hooks are judged:
a kind an engine does not carry in a session at all is not reported yet.
opencode's losses are the unified events its plugin has no trigger for
(`authored::carries`): `session_end` (opencode has no native one),
`permission_ask`, `post_tool_batch`, `message_display` and the two
compaction events.

## Remotes and dependencies (slice S3)

Crates: `deps` and `pin-changes` (core/read-model: addresses, references,
registry, lock, constraints, the pull/upgrade/reconcile decisions, the pin
disclosure), `deps-ports` (`Remotes`, `DepsFiles`, `RemoteLibrary`),
`deps-application`, `remote-git` (the git CLI with `GIT_*` scrubbed, clones
under `.ctxloom/cache/repos`, read from commits only, never a checkout),
`deps-files` (remotes.yaml, lock.yaml and the installed trees under
`.ctxloom/cache/bundles/<segments>/<bundle>/<sha>/`; it stands where the plan
named `content-fs` for these files), `deps-cli`. A repository's
`<segments>` (`deps::address::cache_segments`, under `cache/repos` and
`cache/bundles` alike) are its host and path with every `:` as `_`, on every
OS (owner ruling 2026-11-12: `host_8443`, a Windows drive's `C_`; one layout
everywhere). The cache is regenerable: a unix cache an older ctxloom spelled
with the `:` is orphaned and cloned again once, and `clean` removes both
spellings, as it removes each cache tree whole. `ctxloom-wiring`'s `Commands`
composes them with the library: it merges `bundle push|move` into the
library's `bundle` noun and owns `remote` and `deps`.

- JSON is the API's (OD-40, B4, owner rulings on the B4 draft of
  2026-11-13): every `remote`, `deps` and `bundle push|move` json answer is
  the proto3 JSON of its `ctxloom.v1` message (library.proto), mapped from
  deps-application's views at deps-cli's edge and named under the parent the
  wiring's `DaemonNames` gives (`deps_cli::Names`; `projects/{harp}`, or
  `users/me`). A Remote is `<parent>/remotes/<r>`, a Dependency
  `<parent>/dependencies/<16 hex of SHA-256 of its identity>`
  (`deps::lock::dependency_id`, OQ-15). `remote show` is
  `BrowseRemoteResponse` (a browsed bundle has no resource name), `remote
  discover` `DiscoverRemotesResponse`, `deps pull` `PullDependenciesResponse`
  (one `results` list by `State`, no status or counts: an empty pull is
  `{}`), `deps check [ref]` one `CheckDependenciesResponse` (rows by
  `DependencyCheck.State`; a whole check lists outdated and unchecked only),
  `deps upgrade` `UpgradeDependenciesResponse`, `deps hold|unhold` the
  Dependency, `bundle push` `PushBundleResponse` (`State` COMMITTED or
  PULL_REQUEST_OPENED) and `bundle move` `MoveBundleResponse` (a directory
  move gives its `path` alone). A pin change is the shared `PinChange`
  (`old_`/`new_`, `ChangeKind`, `PinChange.Compatibility`, ItemKind's `HOOK`).
  Text output is unchanged.
- Evidence rule: content is removed upstream only on a not-found from a
  repository proven readable this run; an unreachable remote keeps everything.
- `deps upgrade <reference> [--yes]` (`<remote>/<bundle>` or canonical;
  `Deps::upgrade_only`, `deps::plan::only`; M3, M7) moves that one pin, or
  drops it when nothing reaches it; every other lock entry keeps its commit
  and hold, and no other reference gets a first pin; a reference the lock
  does not hold is `deps-application-nothing-to-hold`. `deps hold <ref>
  --undo` (M7, the vocabulary's toggle undo) releases a hold; `deps unhold`
  stays for deps.feature.
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

Milestone M7 (j001900, diagnosis) also adds `manage check` naming an export
in the project `stale` where a dry `materialize --yes` of the default
agent's profiles, for the kinds the record says it claimed, would change or
take something out.

## Built-in content (owner rulings 110, 111, narrowed by 117, 123, 126-128)

ctxloom ships ONE bundle in the binary, `ctxloom`, its operational prompts:
`prompts/setup-interview.md` (the interview `ctxloom init` opens with and
`init prompt` prints) and `prompts/distill.md` (the owner's own text, ruling
123, once ctxloom-default's `skills/distill.yaml`; no third-party notice or
licence ships with it; for the distill activity, which is not built yet), in
`crates/core/read-model/library/resources/builtin/ctxloom/`
(`library::builtin`). It is listed and NEVER injected (ruling 126): it
carries no fragment, profile or tag, no profile includes it, and no session
is handed it; only the init and distill activities read it. The four
ctxloom-default bundles are no longer built in, and `init` and `config
create` seed a BARE `default` profile that inherits nothing (accepted cost: a
fresh project has no coding guidance until a remote is added). The catalog
lists it under the read-only built-in origin `ctxloom/builtin` (ruling 127;
`Catalog::with_builtins`, joined by the composition root through
`Library::with_builtins`; `bundle list` gives `origin: BUILTIN` (OD-40) in
json and `[built-in]` in text; only the bundle is labelled, as no built-in
profile or fragment exists). A project bundle named `ctxloom` (readable or
not) overrides it whole, and init reads its interview THROUGH the catalog
(ruling 128: `Library::operational_prompt`, behind init-application's
`Prompts` port, and outside a project the built-in one): the project's
`prompts/setup-interview.md` is what init opens with, and one that lacks it
is refused, `library-application-operational-prompt-missing` (exit 20),
never answered from the embedded text. Trust cost, accepted by the owner:
a project's content can rewrite the prompt ctxloom's own setup runs on. The
deps closure counts the bundle as the project's own, so nothing is fetched,
pinned or written under `.ctxloom/cache` for it. Every write to it is
refused (`library-application-write-builtin-read-only`), its fix naming the
override (`bundle export ctxloom <bundles dir>`); an export, and an import of
the name, are not writes to it. Under `test-support` only,
`CTXLOOM_TEST_NO_BUILTINS` withholds it, so the Go-parity and Go-ported tests
(`Project::on_gos_catalog`) hold the catalog Go had. `init` and `config
create` seed no remote (the registry is empty) and `init` pulls nothing.
Divergence from Go: Go seeded and cloned the `ctxloom-default` remote and
pulled the profile's dependencies; that repository stays an ordinary remote.

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
usual and warns `hook-cli-not-recorded` for each observation. Before that
endpoint, a hook delivers to the user daemon its discovery file names, if
one serves (`HookSink::rediscovered`, read at each hook), presenting the
USER bearer that file holds (`run_ports::Bearer::user`), never its run's;
one that does not answer is passed over for the environment's way. The
`Unreachable` sink is that fallback; the in-process `EdgeSink` is for tests and the run's own
facts. The session's end (`SessionEnd`) is captured like the rest.
A compaction's external id is `hook:compact:<session>:<prompt_id>:<trigger>:
<phase>`, so two compactions of one turn (a manual and an auto) are two facts;
two of the SAME trigger in one turn would still collide (the payload offers no
further discriminator). The listener is a port, not a file: a run
killed by a timeout leaves nothing behind, and one asked to stop by SIGHUP,
SIGINT or SIGTERM stops its engine, records its end and stops the listener
before it exits (ruling 114, above). A SIGKILL is not heard: the engine
outlives it.

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
and the `chat.message` trigger, serially and in order, by spawning `<program> hook
opencode execute <Event>` (the running binary's file name, `home::plugin`)
with the event on stdin, plus what it alone knows
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
the user's dirs and still adds the session home as a config dir. The
relocated state directory is seeded, once (owner ruling 167, 2026-11-11; one
there is left as it is, so a pick made in the session stays), with the
user's own `model.json` as read (`$XDG_STATE_HOME/opencode` when absolute,
else `~/.local/state/opencode`, on every platform: `engine_opencode::
user_model_file`, read by the wiring's `opencode_recent_models` and told to
`Opencode::seeding_model`), at `home/xdg/state/opencode/model.json`
(`MODEL_SEED`): with no configured model and no `-m`, opencode 1.18.4 starts
on the first of its `recent` it still knows, and without it picked a model
itself. It is a `Prepared::seeds` entry, which the launcher writes only where
the home has none (as the `.gitconfig`); under `host` nothing is seeded, and
none of hers, or one that does not read, is none, said nothing of. Nothing
else of her opencode state crosses. Auth mirrors
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
An effort level is not passed (no flag verified). An agent with no
model lets opencode choose its own, whose fallback can be a model with no
tool support (owner ruling 168), so the plan says so: the notice
`engine-opencode-no-model`, naming `agents.<agent>.engines.opencode.model`,
its fix `ctxloom agent edit <agent> --engine opencode --model
<provider/model>` (never a refusal; a run with no agent is told nothing). At rest
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
holds what is in engine `e`'s vocabulary (`permissions` is that engine's own
document, passed verbatim, rulings 133-139; written only there, ruling 136);
`runtime`, `env` and `profiles` stay on the agent. There is no engine-neutral
posture (ruling 134): `permissions` at the project level and on an agent
(its `approver`, `approval_timeout`, `sandbox`, `network`) is an unknown key.
Hard migrate: flat `agents.<n>.model`/`effort` and an engine-keyed
`agents.<n>.permissions.<e>` are unknown keys. `effective_on(config, agent,
X)` is `run --engine X`: the agent's block for X, else X's defaults (no flag),
its runtime kept, its env merged with X's (as on its own engine), X's engine
block for command/home/auth.
`agent create|edit --model/--effort` write the block of
`--engine` when given, else of the agent's engine (else the default engine;
a model or effort written there for an agent that names no engine says so in
the notice `notice-default-engine-block`, naming `agents.<n>.engines.<e>`,
ruling 68); no flag writes `permissions` (ruling 138).
`engine show X` lists the agents bound to X and those with a block for X; a
dry run's JSON carries `settings` (each value with its source), and its text
a Settings section naming the place that said each (ruling 72:
`agents.<a>.engines.<e>`, `agents.<a>`, `engines.<e>`, the top-level
`runtime`, or `default`). `agent list`,
`agent show` (its json the `Agent` resource, OD-40) and the create/edit
summary show the agent in the
config's shape (ruling 67): `engines` (JSON) / `engines.<e>: {...}` lines
(text, YAML flow form, keys sorted as written), each block that holds
`permissions` followed by "passed to <engine> verbatim; ctxloom does not
check it" (ruling 139; `agent show`, `engine show` and the dry run's Settings
say it under the document too, and their JSON carries it as written); no
flat model/effort, no `approver`, no "Headless posture".
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

## Config holds words, never booleans (rulings 59, 60, 106, 108)

No key a person writes is a boolean (`schema.rs` has no `Bool` node; a
free-form engine permission document may still read `true`). Hard migrate,
prerelease, no `schema_version` bump: each old key is an ordinary unknown key
(refused, exit 20), with no alias, reader, migration or warning.

| old | new | default |
|---|---|---|
| `config.use_distilled: true\|false` | `config.content_form: distilled\|original` | `distilled` |
| `config.statusline: true\|false` | `config.statusline: ctxloom\|none` | `ctxloom` |
| `config.silence_unsupported` | deleted | |
| `sync.auto_sync` | `sync.on_run: none\|missing` | `missing` |
| `permissions.network: true\|false` (and an agent's) | deleted (ruling 134), with `approver`, `approval_timeout`, `sandbox` | |
| `output_dir` (top level, unread) | `output.dir` | `ctxloom/` |
| (new, ruling 162) | `legacy.go: present\|retired` (home config only) | `present` |
| (new, owner ruling of 2026-11-12) | `config.premised: on-demand\|always` | `on-demand` |

`config.premised` says when a fragment with a premise loads (owner ruling of
2026-11-12; `library::compose::Premised`, read by `Config::premised`):
`on-demand` holds it back (unless the caller named it) and ends the
assembled context with the premise index of what was held back
(`library::premise::render`, each name once, sorted), so `context
assemble`, the dry run, `run` and materialize carry the index; `always`
loads every fragment with the rest and renders no index (the static
assembly). The index's instruction (`library::premise::INSTRUCTION`) is the
owner's Go text but for its last clause, which tells the agent to run
`ctxloom fragment show <name>` for each name it chose, not Go's
`assemble_context` MCP tool (not built: T20; the wording is pending the
owner's approval); `fragment show` reads a companion's fragment
(`ctxloom+companion:<bin>#fragments/<name>`) by asking the companions for
their loadouts. Divergence from Go: Go never put the index in the context
(it served it over MCP); the parity tests compare a dry run without it, and
Go's goldens with the clause reworded.
The scaffold writes no `config:` section; `manage statusline install|
uninstall` writes `ctxloom`/`none`. Output JSON may keep booleans for yes/no
facts. `sync.on_run: missing` makes the run (one-shot, session, init's
interview) pull before it is planned, the same pull `manage install` ends
with; a failed pull is a notice and the run goes on (a dry run does not
pull). A session's outputs are `<project root>/<output.dir>/<harp>`
(`output.dir` relative to the project root unless absolute), no longer
`~/Documents/ctxloom/<basename>/<harp>`; nothing gitignores them, and a
harp never changes, so nothing moves the folder (S55). A first init at a terminal asks for `output.dir`
before the interview (suggesting `ctxloom/`, saying outputs are committable)
and writes the answer; then, in the same style, whether conditional guidance
loads only when it applies or always (`init_application::PREMISED_QUESTION`,
suggesting `on-demand`; an answer that is neither word is asked again, at
most three times, then the suggestion stands), and writes it as
`config.premised`. Off a terminal neither is asked and nothing is written.

`legacy.go` (owner ruling 162) says whether Go ctxloom still runs on this
machine beside this one; both write hook entries into the user's shared
`~/.claude/settings.json`. It is the HOME config's alone: a project layer that
says it is refused (`library::config::schema::HOME_ONLY` and
`take_home_only`, a `breaks the schema` violation naming the home config,
exit 20; under `--degraded` it is said and dropped, never read). Doctor's
stale-hooks check (`DOCTOR-CHECK-STALE-HOOKS-n5`, doctor-checks' `hooks.rs`)
calls a hook entry Rust-era when it is EXACTLY `<name> hook <engine> execute
<Event>`, `<name>` the running binary's file name (ruling 163) or bare
`ctxloom` (one command line, or `command: <name>` with the words in `args`,
as `prepare` and the surface write it), and Go-era otherwise, whatever its
program (a binary named `ctxloom-rs` still leaves `ctxloom hook stamp-plan`
alone) (`ctxloom hook
stamp-plan`, `session-bind`, the older `hook <verb> --engine` ...). Under
`present` each Go-era entry is an `info` row of its own,
`DOCTOR-CHECK-GO-ERA-HOOK-n6` ("Go-era hook, left alone", its event, command
and file; shown by `doctor --all` and in json), and `--fix` touches nothing;
under `retired` they are stale (the warn row) and `--fix` removes them. A
Rust-era entry is never stale. Doctor reads the key as a run reads the config;
a config no run can read counts as `present`, so a broken config never lets
`--fix` delete Go's hooks.

ctxloom has no posture of its own (owner ruling 134, superseding rulings 59,
70, 106-109 and 125 as far as permissions go): the launch no longer judges a
`network`/`sandbox` word against its runtime; `runs-unenforced-posture`,
`UnenforcedPosture`, `PostureEvidence` and `run_ports::enforced` are gone
(`EnqueueRun` field 13 `posture` and `LaunchRefused` field 6
`unenforced_posture` are reserved). Such intent is said in the engine's own
words (its permissions document) and enforced by the engine; a claude
`sandbox` (a sibling of `permissions` in its settings) needs a block of its
own, a follow-up not built. `runtime` stays engine-neutral, as process
isolation.

## The version is VERSION's, stamped by versionator

`VERSION` (the version, no prefix) and `.versionator.yaml` (prefix `v`,
`dirty.ignoreUntracked: true`, and `updates:` keeping the binary's
`crates/root/bin/ctxloom/Cargo.toml` `package.version` in step) hold it;
`just show-version`, `set-version <v>`, `bump-patch|minor|major` and `bump`
change it (and record it in Cargo.lock; no release recipe: a human cuts
releases, "Releases" below). The binary's `build.rs` runs versionator (v0.3.0 or newer: `go
install github.com/benjaminabbitt/versionator@v0.3.0`) and passes the stamp to
rustc as `CTXLOOM_VERSION_STAMP`, so THE BUILD NEEDS VERSIONATOR ON PATH: none,
or a stamp not of the shape, fails it, naming why. The stamp is the Go
family's: a clean tree `v<MAJOR.MINOR.PATCH[-pre]>-<ShortHash>` (stable, so
clean builds cache), a tree with a TRACKED change that then
`-<YYYYMMDDTHHMMSS, the build's UTC time>-dirty` (each dirty build its own; an
untracked file is not a change). Its pieces are pure functions in
`src/stamp.rs`, which build.rs includes. The build re-runs on a commit, a
checkout, a staged change or an edit to a tracked file (the git directory
read through a worktree's `.git` file), and at every build of a dirty tree.
`ctxloom version`, `--version` and doctor's version row say the stamp, and the
binary refuses to start with one not of the shape (`ctxloom-unstamped`, exit
10). cargo-mutants copies `.git` (`copy_vcs`) so a mutant's build can stamp.

## Use `just`

`just --list` shows the public recipes by group; `just install` (group
`install`) builds the plain binary and installs it as
`~/.local/lib/ctxloom/ctxloom` with the wrapper `~/.local/bin/ctxloom` (ahead of a Go ctxloom in `~/go/bin` on PATH)
(`CTXLOOM_HOME="$HOME/.ctxloom-rs"`), idempotently, one line per action. On
Windows, under Git Bash, `just install-windows` builds the plain binary and
installs it as `%LOCALAPPDATA%\Programs\ctxloom\ctxloom.exe`, sets the user
variable `CTXLOOM_HOME` to `%USERPROFILE%\.ctxloom-rs` with `setx` (only when
the registry's value differs), and says how to add the directory to the user
`Path` when it is not on PATH (no registry edit for it); unverified on
Windows. The checks are `just build`,
`just test` (cargo-nextest, the one test runner, mutation included, owner
ruling 144; required: a missing nextest fails the gate),
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

nextest's committed config is `.config/nextest.toml` (workspace-wide): each
test in a process of its own; SLOW at 30 s and killed at 120 s
(`terminate-after`; the longest test takes about 5 s); a test that leaves a
child holding its stdout or stderr past 200 ms FAILS (`leak-timeout`
`result = "fail"`); retries 0 (a retry would pass a flake, and a flake is a
finding); the `ports` test group runs the tests that bind a port, or free
one and dial it, one at a time across binaries (hook-listener,
hook-sink-grpc, forge-github, the wiring's `oneshot::tests::hooks` and
`capture`, the binary's `tests::hook`); and the setup script above. A
second setup script, `guard-homes` (`scripts/nextest-guard-homes.sh`, every
test on every platform; OD-34: a test once kept the shell's CTXLOOM_HOME and
registered 85 temporary projects in the owner's real home), names
CTXLOOM_HOME, CLAUDE_CONFIG_DIR, OPENCODE_CONFIG_DIR and the four XDG bases
as one FILE, `target/test-guard/inherited-home`, so a test that keeps one
from the process and writes under it fails (ENOTDIR) instead of writing
into the caller's; the wiring's
`test_the_run_inherits_no_home_a_test_could_write_into` holds the run to
it. The wiring's tests read the process environment through
`host_snapshot()` (which drops those names, as `Env` unsets them), under
the `ENV_LOCK` its `Env` tests hold while they change it. `_tests`
exits with codes of its own, never nextest's passed through (nextest's 102
is "cargo metadata failed"): 0 passed, 100 a test failed, 101 not built, 120
a doc test failed, 121 nextest failed otherwise (`nextest exited <code>` in
the log); 100 and 120 are red tests, 101 and 121 are refused on every
branch (`scripts/tests-exit.selftest.sh`).

## CI (`.github/workflows/ci.yml`)

GitHub Actions, as thin callers of the shared CI repository
github.com/ctxloom/ci (owner rulings 2026-11-12): its composite actions
`setup-rust` (rust-toolchain.toml, made the default; pinned tools by
taiki-e/install-action; rust-cache, saved on main only), `setup-protoc`
(3.21.12 by checksum), `setup-versionator` (v0.3.0's release binary by
checksum, so no job sets up Go for it), `setup-ltk` (ltk from the Go
ctxloom's go-final, go-installed on a cache miss only, for
`ltk-rules-check.sh`), `gate-logs` (the logs dumped and uploaded on a
failure) and `package-binary`, and its reusable workflows `binaries.yml` and
`release.yml`, all pinned at ONE commit of its main (bump them together).
Every action is pinned to a commit, every tool to a version. Linux
(`ubuntu-24.04`; it moves into the dev container once that image is
published, a commented `container:` line marks the place) runs every gate
but mutation: `just build`, `fmt-check`, `lint`, `lint-os`, `lint-deps`,
`test`, `check-windows`, `selftest`, the whole suite. Windows
(`windows-latest`, MSVC, Git Bash) and macOS (`macos-15`, arm64) run `just
build` and `just test` natively: the WHOLE suite on main, pull requests and
tags, and on any other branch push the nextest profile `ci-platform`
(`NEXTEST_PROFILE`, which nextest reads itself and `_tests` names in its
log): its `default-filter` (`.config/nextest.toml`) is every crate depending
on `os`, `file-system` or `run-within` (`rdeps()` of each of their direct
users, listed because nextest's `rdeps()` names workspace crates only;
`scripts/ci-platform-check.sh`, in the Linux job, fails on one missing),
`test(/::os::/)` (no test is in an
`os` module yet, so that term selects nothing until the OS-specific tests
are moved), and the path-handling core crates run-ports, setup-application,
reclaim-application, library-application, run-application and run-launcher.
`just test` everywhere else stays the full suite. Every dependency is a
public https git pin, so no credential is needed. For Windows, nextest's
setup script runs under `sh` (`cfg(windows)` entry in `.config/nextest.toml`;
the unix one is unchanged), and the plain-binary checks find `ctxloom.exe`
and give it a USERPROFILE, TEMP and SYSTEMROOT.

The binaries are a workflow of their own, parallel to CI
(`.github/workflows/binaries.yml`, on a push to main or a dispatch): the
shared binaries workflow builds the plain binary in the `ci-artifact`
profile (`just release-binary ci-artifact`: no test-support, no
session-destroy, cargo's release defaults, no LTO, no debug info, the two
plain-build checks) for linux-x86_64, windows-x86_64, macos-arm64 and
macos-x86_64 (cross-built on the macos-15 runner with `CARGO_BUILD_TARGET`,
which `release-binary` honours, and run under Rosetta), each the artifact
`ctxloom-<stamp>-<os>-<arch>.tar.gz` (`.zip` on Windows) holding `ctxloom`
(`ctxloom.exe`): tar keeps the exec bit that upload-artifact's zip drops.
`release-package <os> <out> <name> ci-artifact` packages the binary alone.

The nightly (`.github/workflows/nightly.yml`, on main at 03:17 UTC, and on a
dispatch) runs the whole suite on linux, windows, macos-15 and
macos-15-intel (x86_64 tested natively there only), and a FULL mutation run
scored by tier: `just mutants-shard <k/n>` (cargo-mutants' `--shard`, the
whole workspace, in place, no feature) in n parallel jobs, each under the
hosted job's 6-hour cap, then `just mutants-score` merges their
outcomes.json and judges them with the gates' own `_mutants-verdict`
against `mutation_thresholds`, writing `scores.json` (per tier: killed,
viable, percent, threshold, pass), uploaded as `mutation-scores` and
appended to the run's summary. A tier below its threshold fails the score
job, after the scores are kept.

The release profile (owner ruling 2026-11-12, Cargo.toml's
`[profile.release]`) is fat LTO, one codegen unit, opt-level 3,
`debug = "line-tables-only"` and `strip = "none"`, unwinding panics; cargo's
strip is not used, because it deletes what the debug file needs. Stripping is
packaging's: `just release-package <linux|macos|windows> [out] [name]`
(`scripts/release-package.sh`) builds the release binary and writes it
stripped NEXT TO its debug file: linux `<name>` (`objcopy --strip-all
--add-gnu-debuglink`) and `<name>.debug` (`--only-keep-debug
--compress-debug-sections=zlib`), the build ids checked equal and, with an
llvm-symbolizer found, `main` of the stripped binary resolved to file:line
through the debug file (`scripts/release-package.selftest.sh` proves the same
on a C program); macOS `<name>` (stripped after the dSYM, signed again ad
hoc, its UUID the dSYM's) and `<name>.dSYM.zip`; Windows `<name>.exe` as
built and `<name>.pdb`. `split-debuginfo` is cargo's default.

Releases (`.github/workflows/release.yml`, owner rulings 2026-11-12): a person
runs `just set-version <v>`, commits, and pushes the tag `v<v>` on that
commit (agents never tag). The shared binaries workflow refuses a tag that is
not VERSION's, then packages the four plain binaries (`just release-package
<os>`) named `ctxloom-<stamp>-<os>-<arch>`, each archived (`.tar.gz`, `.zip`
on Windows) beside its debug file (`.debug`, `.dSYM.zip`, `.pdb`); the
shared release workflow adds `install.sh` and `install.ps1` (ctxloom/ci's
installers, their defaults set to this repository and `ctxloom`: checksum
verified against SHA256SUMS, refused with no checker; `~/.local/lib/ctxloom/
ctxloom` plus a `~/.local/bin/ctxloom` wrapper and the quarantine cleared on
macOS; `%LOCALAPPDATA%\Programs\ctxloom`, HKCU Path and Unblock-File on
Windows), `SHA256SUMS` over all of them, and attaches them to a GitHub
release for the tag (`gh` with the default token; `contents: write` for that
job alone; a pre-release when the version has one). Its `workflow_dispatch`
builds them as the run's artifacts and creates no release. The released
wrapper sets no `CTXLOOM_HOME` (the release workflow's `wrapper-env`, an
owner decision).

A test that needs a fake program (an `sh` script through
`fixture_program::install_program`, which refuses `Unsupported` off unix),
or runs `sh` or `node`, starts `if !runs_scripts() { return; }`: each crate's
test fixtures hold the same `runs_scripts()`, which off unix prints `SKIP
<test>: <why>` on stderr (the test named by its thread) and answers false, so
a skip is never silent and the test runs unchanged on unix. A feature
scenario skips the same way: `files::put_program` panics with
`files::UNSUPPORTED`, and the feature runner says `SKIP <scenario>: <why>`
and counts it failed nowhere.

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

A throwaway worktree (the staged tree, the mutants batch's, the ratchet
base's) is let go by `gates/drop-worktree.sh <top> <path>`: its directory and
its OWN record under `.git/worktrees/`, nothing else. Nothing runs `git
worktree prune` (`scripts/drop-worktree.selftest.sh` holds the gates, the
justfile and the scripts to it; worktree-git's removal ran one too): prune
judges every worktree of the repository and deletes the record of each whose
directory it cannot see, so a gate run in the dev container, which mounts only
the checkout, cut every other worktree on the host off from the repository
(2026-11-12).

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
drives, and the nightly's whole-workspace `mutants-shard` (scored by
`mutants-score`, through `_mutants-verdict`), which no gate runs. A failing gate is a finding: fix the cause and commit again; never
`--no-verify`, and never weaken a gate to get through.

No commit carries an attribution trailer (owner ruling 170), and the
`commit-msg` hook holds every commit to it: `just commit-msg-check <file>`
(the private `_no-trailers` in `gates/gates.just`) refuses a message with a
`Co-Authored-By:` trailer line, in any case, on any line (the subject and
body too, leading space and a space before the colon allowed; prose that
only names the words passes), printing `FAIL commit-msg: the message
carries an attribution trailer ...` and each offending line by number; a
pass is one `ok   commit-msg (message)` line. What follows git's scissors
line (`commit -v`'s diff) is not judged. Its self-test is
`scripts/commit-msg.selftest.sh` (`just selftest`). A clone installs the
hook with `just hooks-install` once more (`gates/MIGRATING.md`).

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
5. **tests**: `just test-staged` runs the changed crates' OWN tests in the
   staged tree (HEAD plus the staged diff, a sibling worktree of the
   checkout), no dependent's (owner ruling of 2026-11-13, fast hooks,
   acceptance at landing): a library's change does not select the binary
   depending on it, so the binary's acceptance suite runs at commit only
   when the binary crate itself changed (`scripts/test-staged.selftest.sh`).
   On the default branch (and wherever the branch cannot be told)
   `test-commit` asks `test-staged dependents`, which adds the workspace
   dependents as before, so red is never committed there. A staged
   `workspace_wide` file tests the whole workspace. Red tests are committed
   off the default branch only (`test-commit`), and no judge looks at them
   at commit: ctxloom's `red_judge` is `""` (the red-commit judge is gone,
   2026-11-13); a red scenario the branch added, or a green one it broke, is
   refused at landing by the ratchet of `land-ratchet` and
   `closeout-ratchet` (below).
6. **mutants** (no hook; at close-out, `just mutants-batch`): `mutants-staged` mutation-tests the staged diff
   (`cargo mutants --in-diff`), holding new logic to its tier: every
   hand-written tier (domain, read models, ports, application and adapters)
   90% or more; the composition root is listed but not held (the justfile's
   `mutation_thresholds` has every tier); the generated wire code is
   excluded (`.cargo/mutants.toml`). Each mutant runs only its own crate's
   tests (`test_workspace = false`), under cargo-nextest (`test_tool =
   "nextest"`, owner ruling 144: the runner and `.config/nextest.toml` of
   every other test gate; `--no-tests=pass`, so a crate with no tests
   catches nothing; no doc test is lost, the workspace has none runnable): a dependent's
   tests, the binary's acceptance tests included, kill nothing, so a crate
   kills its own mutants. An unviable mutant (one that does not build)
   needs no skip: it counts neither way. A mutant judged equivalent or untestable gets
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
   its threshold, and a run that found mutants and judged none of them at
   all (every one unviable; owner ruling 120, amending 99: the whole run's
   rule, so a tier all unviable beside one that judged passes, its row
   saying `none judged`). No feature reaches the mutation run: `mutants-staged` refuses
   a non-empty `feature_rules` (`_mutants-no-features`), so a crate whose
   tests need a feature of its own dev-depends on itself with it (as
   `session-cli` does for `session-destroy`). Both are held by
   `scripts/mutants-verdict.selftest.sh` (`just selftest`).

A slice landing on an integration branch that carries a red spec on purpose
(daemon/integration, its daemon scenarios red) lands through `just
land-ratchet <base>` (owner ruling of 2026-11-13; `scripts/closeout-ratchet.sh
--no-mutation`): `closeout-ratchet`'s gates (build, lint, lint-deps, the full
test, lint-os) and its ratchet, without mutation (no mutants-batch, even
one `RATCHET_GATES` names), each line and the summary said as `land-ratchet`.
Each branch landing on the integration branch runs it; the full `just
closeout-ratchet <base>` runs once per batch and before main. `just
closeout-ratchet <base>` (owner ruling 165; `scripts/closeout-ratchet.sh`) is
closeout's six gates as usual, mutation included (over merge-base(<base>,
HEAD)..HEAD), except that the feature runner may fail where the base's does
and nowhere else: the slice's red scenarios must be a SUBSET of the base
tip's. It may turn red green, never add a red scenario or break a green one
(`FAIL ratchet: N new red scenarios: B broke green, A added red`, each named,
capped). Every other test failure fails `test` as ever. The scenario list is
the feature runner's: `GHERKIN_RATCHET=<absolute file>` makes
`every_feature_runs` write a line per scenario, sorted, `red<TAB><id>` or
`green<TAB><id>` (the id `<feature path under features/> :: [<rule> ::
]<scenario>[ :: | <example row> |]`; a failure no scenario owns is `<feature>
:: (outside a scenario)`), and pass whatever failed, so only the ratchet's
judge (`scripts/ratchet-judge.sh`) decides. The base's list is `just
ratchet-base <base>` (`scripts/ratchet-base.sh`): only that test, on the
base's tip, in a throwaway worktree `.<checkout>.ratchet-base` beside the
checkout (target dir `target/ratchet-base`), cached by sha at
`target/ratchet/<sha>.list`; a base from before the list gets the runner
commit that taught it applied, and says so. On main the base has no red
scenario, so the ratchet is the plain closeout. Exit 0, 1 (a gate, a new red,
or no base list), 2 (usage). Held by `scripts/closeout-ratchet.selftest.sh`
(`just selftest`).
