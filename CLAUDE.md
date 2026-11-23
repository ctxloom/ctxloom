# Introduction

ctxloom is the Rust ctxloom, built on angzarr-embedded (it began as a port of
the Go ctxloom), one user journey at a time, with architect review of each milestone. The plan
(`ctxloom.plan.md`'s roadmap, §3, the one integrated plan, owner 2026-11-13)
orders the milestones by journey
(`crates/root/bin/ctxloom/features/journeys/`), with work on
`daemon/integration`; each makes its journey's
scenarios pass, with the cli contracts it touches. A milestone starts as red
tests, committed red on `port/<milestone>`, reviewed, then implemented green.
All work is cut from and landed on `daemon/integration` (owner 2026-11-13:
"One line: all work on integration") until step 11 lands it on `main`; from
then on work lands on `main`.

The repository is `ctxloom` (github.com/ctxloom/ctxloom), re-created with
fresh history on 2026-11-20 (`main` is one commit). The Go-era and
daemon-era history is in github.com/ctxloom/ctxloom-legacy (archived,
read-only reference, never merged; the Go ctxloom is its `go-final` branch).

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
them (owner ruling 144: once per test PROCESS cost about 2.3 s a test). No process
a test starts outlives it: the acceptance tests' `Project` ends, when it is
dropped (a panic's unwinding too), every process its `start_*` started that
is still its running child (SIGTERM, ten seconds, then SIGKILL; `ps` and
`kill`, so unix only), before its home is removed; a left frontend would
keep its daemon serving, or start another (2026-11-15, U8r). nextest's
leak-timeout cannot see these: a daemon's stdio is null and a frontend's
is piped to the test process, not to nextest. The `mock` and `mock-*` engines exist only under
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
- `adapters/support`: infrastructure driven adapters share, depending on no
  other workspace crate, each listed in `arch-rules.toml`'s `[classes]
  support` (step 10, G9: `git-cli`, the one git runner).
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

Divergence from Go: a config whose `schema_version` is newer, older or unreadable is refused (exit 48, D15 strict decode); Go warns and continues. A `non_tty_format` of yaml, toml or markdown is refused (D28).

Slice S2 (library write side) adds, in the S1 crates, a write half: `library::write`
(pure planning: the config section merge with sorted keys, item/envelope/mcp/
skill/profile files, the scaffold, the loadout, the skill zip codec),
`library-application::write` (create, edit and remove, previews unless `--yes`,
export/import, the editor round trips), `ContentRepo::apply`/`claim`,
`UserFiles`, `Scaffold`, `Editor` and `CompanionProbe` ports, and the driven
crates `editor-process` and `companion-probe` beside the writes of `content-fs`
and `config-files`. Files are the truth: a command plans the files, checks the
tree still reads, then hands the changes to the port; a refused write has
written nothing. A config write replaces the section it owns whole, never
heals a file that does not parse. `loadout --format yaml` is the one yaml
output; its prose is withheld until the owner rules (D24, D8): ctxloom's
own loadout is data (`library::write::loadout::own`, its `run:` bundle's
version, tags, author and `mcp` servers, no description, notes or
fragment), written as the document by `loadout::document`.
Every item verb takes one reference, `create` too (M3, M7): `fragment|command
create <bundle>#<kind>s/<name>` (`Library::item_create_at`) opens the editor
on its template and stores what the editor gives back (an editor that saves
nothing stores the template; an empty buffer is
`library-application-write-empty-edit` and writes nothing), and makes a bare
bundle nobody had (`ItemCreated::bundle_created`); the two-word Go form
(`<bundle> <name>`) keeps Go's placeholder, no editor, and its refusal of a
missing bundle, and is a daemon write too (step 10b: the create requests'
additive `placeholder`, served by library-api's `placeholder` over
`Library::item_create`). `skill create <bundle>#skills/<name>` (or the two words)
scaffolds its `SKILL.md` and an EMPTY `.meta.yaml` (`{}`, the empty
mapping, `library::write::skill::EMPTY_META`) beside it, one new directory,
and opens none. `fragment|command edit` writes the
`.md` ALONE (owner T29, T30): it distills nothing and never touches the
`.distilled.md` beside it, which then reads stale by its digest (below,
"Distill"); `--no-distill` is accepted and only says so.
Skill zips are written and read by `library::write::archive` over the `zip`
crate (deflate only, default features off) and crc32fast.
A skill is ONE directory, its metadata inside it, `skills/<n>/.meta.yaml`
(owner T27, 2026-11-18, evening), a file of the package (`Skill::files`)
and so DELIVERED with it everywhere the directory is copied whole: a
materialize into an engine's skills directory (claude's
`.claude/skills/<n>/`, the session home's `skills/`), an export zip, an
install and a push; `skill show` lists it. An MCP server is ONE file,
`mcp/<n>.yaml`, its `tags`, `notes` and `installation` in it after its
runtime fields (`library::write::mcp::content_file`), so `mcp server set
--url X --notes Y` on a new server is one file. A bundle still in the old
layout (`skills/.<n>.meta.yaml` or `mcp/.<n>.meta.yaml` beside the item)
is refused per bundle (owner, QUESTIONS 2(a); D15): the tree reader's
`BundleError::OldLayout`, `library-sidecar-old-layout`, exit 48, which
`bundle show` (and every read through `Project::bundle`) answers with, and
a listing names with the skipped-bundle notice while listing the rest; its
paths are named from the project's root (`BundleError::located`), its fix
the `git mv` that migrates it (for a server: copy the fields into its file,
then `git rm`); an INSTALLED bundle's fix names its remote's URL to migrate
in instead. Nothing reads or moves an old sidecar; a hook's
`hooks/<event>/.<n>.meta.yaml` sidecar is unchanged.
A file's executability is ONLY its skill's metadata's `executable:` list
(`skills/<n>/.meta.yaml`), on every OS (owner ruling 2026-11-12; Windows
reports every file executable): `library::write::skill::declared_executables`
answers it for a bundle's tree, and nothing reads a mode from disk or git
(`ContentRepo` has no `executable_files`, `UserFiles::read_tree` and
`deps_ports::RepoTree` are bytes alone). `skill show`'s mode column, an
export, an import (the metadata it brings), an installed bundle and a pin's
disclosure take it from there; an install writes the declared files 0755 as
a convenience for running them, never read back, and a launch, which
carries a skill whole, writes them executable where it puts them (the
session home's `skills/`, 0700, `SessionRecords::write_executable`; under
`session_home: host` the checkout's, 0755, `isolation_ports::PlacedFile`;
`run_ports::HomeFile::executable`); a push commits them 100755
and every other file 100644 (`Remotes::publish`'s `executable`, `git add
--chmod`; a pull request's `PullRequest::executable`). Visible change on
unix: a file chmod'ed executable but not listed shows 0644 and is pushed
100644. Not changed: a skill zip's entry modes, which its export writes
from the metadata and its import turns into the metadata's list (an
import always writes the metadata: the archive's, else the empty mapping;
a forced import replaces it with the directory).

Step 10b U3 (writes as library-domain commands; `t13-step10.plan.md` §2
U3, protos.md §1.1) serves `bundle create`, `fragment|command create
<bundle>#<kind>s/<id>` and `edit`, `skill create`, `config set`, `mcp
server set|edit` (`UpdateMcpServer`: a mask names a set's fields, a field
named and given empty cleared; no mask is the whole server through the
editor, the preview's Edit need; a missing server is created only when
`allow_missing`, which the CLI always sends, else refused
`LibraryMcpNotFound`, AIP-134; `--header`, `--tag` and `--served-by` are
gone, owner ruling, hard migrate; a server's notes and installation are
in its one file, owner T27, so a set of them beside a runtime field is
one file, one call) and the
removals `bundle|profile|agent|companion remove` in the daemon,
`agent create|edit` (`CreateAgent`, `UpdateAgent`: the agent's fields,
an edit's named by its mask, a field given empty cleared; `model`,
`effort`, `surfaces` and `roots` are INPUT_ONLY Agent fields and `home`
a field of each request, additive, owner ruling 2026-11-18; an edit
without `--yes` is previewed first, a swap whose new engine cannot carry
what the old one did printed as that preview and nothing applied, any
other edit applied with the preview's etag; the summary is the answer's,
a profile its short library reference), `config edit` (`EditConfig`,
an additive RPC, owner ruling 2026-11-18: the config of where it is run
through the editor as a round trip, the preview's Edit need its text and
the apply its answer written whole and unparsed, so a config that does not
load can still be mended; left as it was, nothing applied), `profile create`
(`CreateProfile`, under the bundle its parent names, `--bundle`, else
the project's own, `project`, made with it the first time as one new
directory; `--add-skill` is the INPUT_ONLY `Profile.skills`, additive),
`profile edit|modify` (`UpdateProfile`: given flags, a patch in one call,
its add/remove lists additive request fields and `description` by mask,
a mask sent even naming none, so `profile modify <name>` alone changes
nothing; no mask and no list, the editor's round trip, its preview's Edit
need), `bundle export` (`ExportBundle`, D2's EXCEPTION, the one write
outside ctxloom's trees: the recording library's `UserFiles` records the
tree, `Planned::Outside`, so planning, a preview's twice, writes nothing;
the daemon takes it aside, `daemon_api::library_writes::outside_of`, and
on an apply makes it with no step and no intent on any stream,
`Library::write_outside`, content-fs's `UserFiles::write_tree`: built in a
staging directory beside it, `.<leaf>.ctxloom-staging-<pid>-<n>`, renamed
in with NOREPLACE, the staging removed either way; its paths are the
caller's, below her `cwd`, `-o` the additive `ExportBundleRequest.output`;
a path that is there is refused first,
`library-application-write-export-destination-exists`, a draft; once
made it is recorded on its SOURCE tree's stream, the tree whose git sees
the bundle's manifest, `RecordBundleExported` keyed `export:<request id>`
(its event `BundleExported`, additive; `library-checkout` keeps it by
request id), so the same request sent again is answered from the record,
applied, its source and destination as then (AIP-155, owner 2026-11-18,
night; `answered_before`), and a NEW request onto that destination is
refused as before), a
local `bundle move <b> --to <dir>` (`MoveBundle`, step 10b U9,
QUESTIONS 1(a) owner-approved: deps-cli tells `--to` apart in process,
`Deps::move_target`, a remote still published there, U4, anything that is
no directory refused as before; a directory is the daemon's ONE Move of
the bundle out of this tree into the project at `dir`,
`Library::bundle_move_into`, `ContentRepo::move_into` recorded as
`Planned::MovedInto`, shaped `writes::Shape::Move { into }`, its intent's
additive `MovePath.to_tree` naming the other tree's `.ctxloom`, which
`decide` never judges `to` on, its operation `Operation::Move { into }`,
content-fs's one `rename_noreplace` below that tree; only its `from`
files are expected gone, and the other tree's next read records the
arrival; a bundle there is refused, `--force` too (owner 2026-11-18,
night), as is its own place,
`library-application-write-move-destination-exists|-itself`, drafts, the
first's fix naming `ctxloom bundle remove <bundle>` in that project then
the move again; the CLI's text and json are the move's as before, from the
response's additive `source`; deps-application's `move_bundle` is a
remote's move alone, `DepsFiles::copy_bundle` and its local copy gone), `companion add` (`CreateCompanion`: the daemon
probes the program in the caller's environment and registers the name in
the HOME config, one layer; one registered already applies nothing),
`agent default <name>` (`SetDefaultAgent`, one layer, `defined` saying
whether the agent is defined yet; the bare `agent default`, the current
default, is still read in process, as a default may name an agent no
listing has), and
`bundle edit` (`UpdateBundle`: its mask allowed several paths of the one
`bundle.yaml`, owner ruling 2026-11-18, and its items added and removed
by the request's lists; an item beside anything else is two files,
refused `OneChangePerCall`, so bundle.feature's listing outline edits the
version and the fragment in two calls, coordinator 2026-11-18, for owner
review)
and `fragment|command|skill remove` (`DeleteFragment`, `DeleteCommand`,
`DeleteSkill`: an item with its distilled form is several paths taken
away, the T28 sequence of their trashes, each kept, step 10b U9; a skill
is its one directory, one trash, owner T27), from one table (`daemon_proto::library_writes!`, every layer;
`library_api::served_writes!`, each verb's use case and response; the
CLI's `SERVED_WRITES`). Every write request carries the caller's `cwd`
(97) and `env` (94), as the reads do (E1, extended to the writes on
2026-11-17 for the owner's review), so the daemon resolves its project
and checkout from the caller's directory and runs git in its
environment. A write is observed as a read is, then its request
id looked up in `library-checkout` (which keeps every intent by request
id: a retry of a write done is answered as applied, one owed is finished
from its step), then PLANNED: the use case over `Library::recording`,
whose content and config ports record what it writes and write nothing,
shaped by `library_api::writes::shape` into ONE step (a file replaced, a
new directory, a path trashed; more than one file is
`library-tree-one-change-per-call`), then INTENDED (the step's command
under the request id, the content ids of what it writes,
`library_tree::content_id`, the SHA-256 of the bytes; step 10c U13 rework),
made by ONE atomic operation (U8,
`ctxloom_ports::Operations`, content-fs's `TreeOperations`, through the
`Observing`'s `operations`; `library_api::writes::operation` maps the
step): a file written under the tree's `state/staging/<request id>/` and
renamed over its target, a new directory built whole there and renamed
in with NOREPLACE (one that appeared meanwhile is
`library-tree-resource-exists`, and is left as it was), a removal one
rename into `state/trash/<request id>/<path>` (nothing there to take is
`library-tree-no-such-resource`); nothing is staged beside its target.
It is then read back and recorded `RecordStepDone`, and answered with
the path's new etag. Where one operation cannot do it, a write is a T28
SEQUENCE (step 10b U9): `writes::Shape::Sequence`, its steps listed in its
intent (`RunSequence`), carried out one at a time, each one atomic
operation recorded done (`done:{request id}:{step}`), and RESUMED on a
retry at the first step not done (`End::Pending{done}`, the recorded
steps; a step's content, a file's bytes or a new directory's files, is
the retry's re-plan's step of its kind at its path,
`writes::content_for`/`operation_of`; a step that writes content the
re-plan no longer holds fails the write). A sequence's trash is not kept:
its last step, `Operation::Purge` (`PurgeSlot`), removes the request's own
slot (G12(a)). `bundle import` is served (`ImportBundle`; its source the
caller's path, read from her `cwd` when relative): a new bundle is one
new directory, and a forced one over a bundle there is the sequence
trash, create, purge (the use case plans the old bundle gone and a new
one of EVERY file imported, `Planned::BundleGone` then `NewBundle`, so
`shape` makes it the sequence; `--yes` is accepted and changes nothing,
an import asks nothing). `clean` is served too (`Clean`, an additive RPC;
setup-cli's `Cleaning`, the wiring's `DaemonServed`): the daemon measures
the cache trees (`daemon_api::Caches`, cache-files') and takes each one
present into the checkout's trash, one write, a sequence of kept trashes
(a sequence that purges no slot of its own keeps its trash, G12), each
said by `library-api-notice-trashed`; its text and json are the report
they always were (`setup_cli::report_of`, from the response's additive
`items`). A step on a path outside the library's pathspec (`cache/`,
`sessions/`, `state/`) leaves nothing the record follows
(`writes::in_library`). A sequence's first path is judged on the
caller's etag, each other on its own token now. A library use case's
failure is said in its own words, its code and args
(`daemon_api::refusal::app_error`, as a deps failure is). A tree's staging is swept the first time a daemon
observes the tree (`Observations::sweeping`, a read's or a write's; S50);
the trash is never swept at start. An apply that trashed says where,
the notice `library-api-notice-trashed` (its path and slot as the tree
shows them, `.ctxloom/state/trash/<request id>/`, kept 7 days; texts.md
§3, `daemon_api::library_writes::trashed_notice`). A removal's slot is in the tree's
state (`library_tree::Slot`: its paths, the time its intent was recorded,
`kept`; every lone removal is kept, a sequence's own is not, U9), and
a kept slot `KEEP_DAYS` (7, G12) old or more by the daemon's clock is
purged when the daemon next observes its tree (`Observations::purging`;
QUESTIONS 4 says each round and at start, and the observation is where a
tree's directory is known): the domain's `PurgeTrash` (its age the
evidence, refused `library-tree-slot-not-due` under 7 and
`library-tree-no-such-slot` once purged; its command id UUIDv5 of
`purge:{tree}:{request id}`), the slot removed whole, its `PurgeSlot` step
recorded done, which drops it from the state. Under test-support the
daemon's clock is moved on by `CTXLOOM_TEST_CLOCK_OFFSET` whole days,
passed on to a daemon a command starts. Each rename's directories are
synced after it, the target's then the source's when it is another, and
a purged slot's trash directory after the purge (owner ruling
2026-11-18, evening; angzarr-platform's `Fs::sync_dir`: fsync on unix,
nothing on Windows), so a power loss cannot undo a step the record holds
done; a sync that fails fails the step, `content-fs-sync-dir`. An
observation that sees exactly a pending intent's ids is that step done
(`StepApplied`), so a daemon killed after the operation is recorded
once, as ctxloom's; a step done lays its paths over the record's dirty
set. The editor round trip is the preview's Edit need: the daemon's
`AnsweredEditor` answers `--body` or the need's answer, else keeps the
ask; the CLI launches the need's EDITOR itself (`Served::edit_document`).
A removal's preview carries a Confirm need (`library_api::served_writes::
removed_of`: its text `Remove <target>?`, drafted in the 10b spec's
texts.md as `library-api-confirm-remove` (owner-delegated, not yet
rendered from that id), and the preview's target as its arg `target`, from which the CLI
prints the preview in its existing words); the CLI asks it at the terminal
(`Served::confirm`, `[y/N]`; none off a terminal) and on a yes sends the
apply with the answer and the preview's etag, else prints the preview and
applies nothing (`library_cli::write::removal_served`); an apply refused
`library-tree-plan-stale` (the bundle changed before her answer) previews
the removal again, prints it, and ends in the refusal; `--yes` is one
call. `DeleteCompanion` has no `answers` field, so its apply sends none.
`session remove`'s preview carries the same Confirm need (step 10c U11:
`daemon_api::destroy::removal_confirm`, id `confirm`, `Remove session
"<harp>"?`) and an etag, the UUIDv5 of the plan's files (rel, class, size)
and worktrees (path, verdict) (`removal_etag`); at her terminal session-cli
asks it (`session_cli::Asking`) and her yes is
`SessionRemovals::apply_removal`, the preview's etag and her answer. An
apply that names an etag is refused, before anything is destroyed,
`daemon-api-needs-unanswered` (`ctxloom.v1.NeedsUnanswered`) without a
confirmed answer and `daemon-api-plan-stale` (`ctxloom.v1.PlanStale`,
ABORTED) when its plan now tags otherwise; a retry whose session is gone is
answered as before. ctxloom's MCP server is an eliciting client (rmcp's
`elicitation` feature): each need of a preview is a form elicitation
(`mcp_server::elicit`: a Confirm the boolean `confirmed`, a Choose a string
among its choices, an Ask or an Edit the string `text`, the suggestion or
the document's text its default; an optional need's field not required),
put only to an engine that declared form elicitation; a required need not
answered (or not yes) declines the write and nothing is applied. An Edit is
answered with the document's new text, and no editor is started (the
request's `presentable` says `can_ask`, never `can_launch`). D16 (S40):
`child_close` of a child holding open children previews its cascade as
`GetSession`'s children (`Caller::close_needs`, every open descendant named,
nearest first) and asks to confirm it; a no is answered `"<child>" was not
closed: ...` and closes nothing; a child with none closes unasked. The test
build's tools `test_remove_session` and `test_create_fragment` (callable,
never listed; QUESTIONS.md 7) drive a removal's Confirm and a fragment's
Edit through the elicitation.
A preview's etag is the path's last sequence; the apply sends it back
(`etag`) and the daemon puts it in the step's command as `seen_sequence`
(`daemon_api::library_writes::seen_sequence`; empty, a `--yes`, is the
path's token now), so `decide` refuses a path moved since the preview
(`library_tree::judge(seen, now)`, the one definition of stale: `Judged::Fresh`
or `Stale {seen, now}`; every step of every write asks it) `library-tree-plan-stale`, whose status is ABORTED (OQ-10; angzarr core
b1445adf). An edit's preview with no text given probes (the daemon's
`AnsweredEditor::probing`): it plans the file its apply would write, for
that file's etag, and still asks the Edit need; the CLI's `fragment|command
edit` sends that etag with its apply. An etag that is not a number is
refused `daemon-api-etag-invalid` (`ctxloom.v1.EtagInvalid`,
INVALID_ARGUMENT, exit 48; its text a draft the owner delegated).
`--body` (fragment|command create and edit) and `config set <key> <value>
[--home]` (one key, coerced as an override and judged by the schema at
its path) are the spec's new surfaces. Test-support's `test-die-at`
kills a write at `intended`, `staged`, `trashed`, `renamed` and `done`
(and `test-die-after-apply` at `done`); `test-hold-at` (the same words)
holds a write at its point while the file names it (at most 30 s), so a
test standing for another process acts between two steps; and a
command's `CTXLOOM_TEST_NO_RETRY` turns the client's one retry off
(`Client::without_retry`), so what a killed daemon left is left. Every
other write still runs in process until it is served.

Other tools' files are the daemon's too (step 10c U10, L15, T26):
`materialize`, `manage uninstall` and `doctor --fix` are served
(`Materialize`, `UninstallManage`, `RunDoctor` with `fix`; additive RPCs;
`surface_cli::Foreign`, setup-cli's `DoctorFixing`, the wiring's
`DaemonServed`). The daemon runs the use case where the caller stands
(`daemon_api::ForeignPlans`, the wiring's `daemon::foreign`) over a
RECORDING store that writes nothing (`SurfaceFiles::recording`, its own
reads seeing what it recorded; the doctor's `writing_through` recording
user files), then writes what it recorded as ONE sequence on the
checkout's stream (`Api::foreign_write`): the writer's claims record
(`<home>/.ctxloom/records/*.claims.yaml`) first, then each file a
`ReplaceFile` marked `foreign` (project-relative, or absolute outside the
project), every path and git's id for it listed in the intent
(`Intent.foreign`, `RunSequence.foreign`), each made by
`Operation::Outside` (written whole beside itself, as an engine's files
always were, or removed with the directories that leaves empty below its
`keep`) and recorded done, so its retry resumes at the first step not done
(a removal the re-plan no longer holds is made from its intent alone),
and a re-run converges. The domain refuses a foreign step anywhere but a
sequence of a foreign writer's verb (`library_tree::FOREIGN_WRITERS`,
`PlanRejected` `library-tree-foreign-outside-writer`, a bug), judges no
token of one, and folds its `StepApplied` into `LibraryState.foreign`,
never `resources` or the dirty set. The kill point after the `n`th step
is `step-<n>`. A use case's own error is the call's, never in band
(`daemon_api::setup_error`, by the class it says of itself): a decline
(48) the typed refusal `SetupDeclined`, fatal findings (49)
`SetupFatalFindings`, each its diagnostic's code and args as its `Cause`
(the client's `Rejection::LibraryRead`, and `Rejection::FatalFindings`,
which `CallError` classes 49), a failure (32) said in its own words
(`ApiError::Diagnosed`); nothing is written. `materialize --diff` and
`manage install`'s refresh still deliver in process.

The setup reads are the daemon's (step 10c U5, U6, D1): `doctor` (its
checks, `RunDoctor` without `fix`, `RunDoctorResponse.checks`), `auth
status` (`GetAuthStatus`), `manage check` (`CheckManage`) and `init
prompt` (`GetInitPrompt`), additive RPCs riding `library_writes!`'s
plumbing (`write_requests!`: cwd, env, globals), through setup-cli's
`SetupReading` and `surface_cli::Foreign::check` (the wiring's
`DaemonServed`). The daemon runs each use case where the caller stands
(`daemon_api::SetupReads`, the wiring's `daemon::setup_reads`, through
`caller_wiring`), the project's trust its own (`init prompt`'s
`Flags.trusted`, never asked back), and the CLI reads the answer back
into the report it always printed (setup-cli's and surface-cli's
`wire`), so with no daemon each fails, exit 32, diagnosing nothing in
process (QUESTIONS 8). doctor's rows hold owned text
(`doctor_checks::Check`'s `marker` and `name`, auth's `Row` `mode` and
`usable_by` are `String`s) so the client rebuilds them. `manage gitignore
install` is a library write (`InstallGitignore`, `surface_cli::Foreign::
install_gitignore`): `Library::gitignore_install` writes `.ctxloom/.gitignore`
through the scaffold when it differs (`Scaffold::read`), the recording
scaffold plans it as `Planned::TreeFiles`, and it is one `ReplaceFile` of the
content tree.

INIT ANSWERS ONE MESSAGE (owner ruling 2026-11-19, batch 3; ruling 146): the
apply's `InitProjectRequest` carries each `--remote` (`remotes`, additive,
named `personal`, `personal-2`, ...) and the apply, after the project is
made, registers each as `remote create` registers it
(`Api::create_remote_write`) and each companion its answers accepted as
`companion add` does, each under a request id derived from the init's own
(`init::derived_id`: a retry finds each write as it left it), answering them
in `InitProjectResponse.remotes` and `.companions`; one that would not
register is said there (`error`), never a failure of the init. The CLI sends
that ONE call and prints that ONE message: json (the default off a terminal,
or `--format json`) is the response as a message, its notices (and each
remote's) said on stderr; `--format text` is the prose (`Created ctxloom
directory`, `Added remote`, `Added companion`, warnings on stderr). A
`.ctxloom` that is there with no project named yet (a clone's) is named by the
apply when it has remotes to register (`name_existing`, its id answered for
the client to bind). The acceptance harness's stood-in terminal
(`CTXLOOM_TEST_TERMINAL`) runs `init` with `--format text` (a real terminal's
default; the binary's own default format still follows the stream), so the
scenarios that stand a terminal in read the prose.

The remotes' and the dependencies' writes are the daemon's (step 10c U4,
protos.md §1.2, L9): `remote create|edit|remove|default` (`CreateRemote`,
`UpdateRemote` with `new_remote_id` and a mask naming each flag given,
`DeleteRemote`, `SetDefaultRemote`) and `deps pull|upgrade`
(`PullDependencies`, `UpgradeDependencies`; additive RPCs), each through
deps-cli's `Served` writes (`deps_cli::served_writes!`, the wiring's
`DaemonServed`), its text read back from the answer (deps-cli's
`written`). The daemon plans each where the caller stands
(`daemon_api::DepsPlans`, the wiring's `daemon::deps`): the deps use case
in a wiring of the caller's directory and ENVIRONMENT, so git runs on the
caller's `PATH` with its `HOME` and credentials (`GIT_*` scrubbed by
remote-git as ever), over a RECORDING store
(`deps_files::RecordingDepsFiles`: the registry, the lock, each tree
installed with its files and identity marker, each tree replaced or pruned
recorded, never made, and read back by the reads after). A remote's write
is one library write of `remotes.yaml` (`Api::carry_out`). A pull or an
upgrade is ONE `RunSequence` (verb `deps pull` or `deps upgrade`) on the
checkout's stream (`Api::deps_write`, daemon-api's `deps_writes`): a
`CreateDirectory` per tree, no entries (the cache is presence and
identity, never content), made whole by `Operation::CreateDir` into
`cache/bundles/<segments>/<bundle>/<sha>` and completed by
`RecordTreeInstalled` whose `tree` is git's own tree id as remote-git
reports it (`Remotes::bundle_tree`; nothing in ctxloom computes a git
id); then `lock.yaml` (written only where it differs); then each tree it
no longer keeps into the sequence's own trash and the purge of its slot;
then, for a consented refresh (`--yes`), each engine file as a foreign
step (`deps pull` is a foreign writer). A tree already renamed in whole
is a step done, so a killed pull converges on its retry or its re-run,
and the lock never names a tree that is not installed. The kill points
are `tree-installed` (after the first tree), `trees-installed` (after
the last), `done`. A consented refresh the project refuses once the pull
applied is the answer's `refresh_refusal`, and the command ends refused
(48) after the report, as in process. `remote list` gives each remote's
forge. Not served yet: `deps check`, `hold|unhold`, `bundle push|move
--to <remote>` and `manage install`'s pull.

Slice S4 (sessions) built the following. The record is the user's durable
store (owner rulings 87a, T14, which reverses 87's store per project): a
session's state goes through angzarr-embedded over the ONE per-user
`<ctxloom home>/.ctxloom/ctxloom.db` (machine-local; see "Storage is
angzarr-store-sqlite"), every project's streams in it, each rooted on its
resource name (below), every hook's fact an event when it is delivered. `session list`, `search` and `show`
are the daemon's reads (slice 2 step 6, "The daemon's API"), from the session
index it keeps warm, each waiting for the daemon's next round so it answers
what was committed before it was asked; `transcript show` is one too
(`GetTranscript`, owner's H3 follow-up); so a live session shows what was delivered so far; nothing projects the events into a file (no
`transcripts/transcript.jsonl`, `records/*.jsonl`, position file or
`metadata.json`). A session's directory, `.ctxloom/sessions/<harp>/` in the
project, holds what its engine needs (the session home, its native
history), and its lock INSIDE it, `<harp>/session.lock` (step 10 U9, T28:
the directory and its lock move as one; `session_ports::lock_path`, the one
place the path is formed), is held by its run for its life. Its
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
`PrepareRequest`'s and the `TurnRequest`'s `cwd` and `RecordRunnerStarted.workspace`
(`Order.cwd` stays the live project), so claude's trust entry
(`seeded_state`, `projects.<cwd>.hasTrustDialogAccepted`) is the checkout's
resolved path. `none` is the project; a checkout is
its path; one that cannot be had (not a git repository, the add failed, no
checkouts directory or no workspaces port) is the project and the notice
`library-application-notice-workspace-unavailable`; one the port refuses
fails the launch (`RecordLaunchFailed`). Once the engine has exited, and before
the session's lock is let go, the run lets the
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
- `core/ports/session-ports` (the user's store through `ProjectStores`, with `Purge`; `Liveness` and
  `SessionLock`; a session's files, Go's session directories);
- `core/application/sessions-application` (list, search, show, keep, title,
  bind, the transcripts; a removal's types and the head-guarded `purge` the
  daemon's destroy uses; Go's `session.yaml` schema 1 to 3 read through
  angzarr-yaml; the importer's types);
- `adapters/driven/{liveness, session-files}` and, behind `ProjectStores`,
  `ctxloom-store-sqlite`;
- `adapters/driving/{session-cli, hook-cli}` (`hook` is hidden: `hook
  <engine> execute <native>` and the statusline; a session start's bind is
  one of its capture facts, below).

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
`feature_rules` entry: the mutation recipe refuses one). A destroy is a T28
sequence on the PROJECT stream (O2, step 10b U9): the domain's
`RemoveSession` (its command id the request's, keyed `removal:{request id}`,
a sweep's `removal:{request id}:{harp}`; the project stream keeps it when
the session's own is purged) is decided on the evidence the daemon
gathers (`RemovalEvidence`: the lock's verdict, kept, the retention, the
worktrees; `sessions::removal_refused`, which a preview asks too; its
refusals `SessionLive`, `SessionKept`, `WithinRetention`, `KeepsSession`
are said as `session remove`'s refusals were), and recorded
`SessionRemovalIntended` with its steps (`sessions::REMOVAL_STEPS`:
`worktrees`, `trash`, `purge`, `purge_trash`), each then made and recorded
`RecordRemovalStepDone` (`ProjectState.removals` holds it open until its
last is done). The worktrees step is reclaim's, made just before the
intent (its classification is the removal's judgement) and recorded done
at once. Then the session's whole directory, its lock inside it (authored
files too, a link never followed), is renamed into its checkout's
`state/trash/<removal id>/` (`Places::operations`, content-fs's
`TreeOperations`; the `trashed` kill point), then the session's stream and
each run stream UUIDv5(session, 1..=n) are purged from the store, each
guarded by the head just read (retried on a conflict), then the trash slot
is removed (`Operation::Purge`), so none is left; the name's claim is kept,
and the daemon's warm
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
left and the session kept). The sweep (both verbs' age form) is as whole
(owner rulings 2026-11-13 and 2026-11-14): checkouts go ONLY as part of
destroying their session. Each session's checkouts are classified first,
changing nothing, and a session the sweep keeps, for ANY reason (running
or unproven, marked keep, a checkout spared or skipped, no purge age,
active since it, within its retention, its purge refused or failed), keeps
EVERY checkout, its clean ones held in the report saying why the session
was kept; only a session it purges has its clean ones removed on
`--yes`, by that purge, through `reclaim_application::remove`'s order
(`reclaim_application::sweep`). The directory goes first, so a
directory that cannot be taken leaves the session whole; a retry of a
removal still open on its project's stream resumes it at its first step
not done, never judged again (its own trash leaves no lock to prove it
ended). A hook fact
redelivered after it finds no session and records nothing. It is refused, as
before, for a session whose lock does not prove it ended (held, or none;
the daemon reads the lock) unless `--even-if-live` (the one override), for
a held lock on Windows even with it (owner ruling 2026-11-18, D18: a
directory holding an open file cannot be renamed into the trash there; the
daemon, `Api::on_os` given the wiring's `os_name`, names the held lock in
the evidence, `RemovalEvidence.blocking_lock`, by
`daemon_api::destroy::blocking_lock`, and the domain refuses it up front,
`sessions-session-lock-held`, said as `sessions-application-refused-lock-held`
with its `-fix`, end the session first; the wire's
`RemovalRefusal.Reason.LOCK_HELD`, its `detail` the lock's path; unix is
unchanged), for a kept one, and within the transcript retention: its project's
`session_transcript_retention`, read from its REGISTERED root (30 days
unless configured; one that is no age is `AgeInvalid`, a configuration that
does not read `ConfigUnreadable`, a sweep's too, the flag or not). A refused removal is the typed `RemovalRefusal`
carrying its PLAN (the files, their classes and bytes, the authored ones,
the worktrees), so it prints exactly the plan it printed in process, then
the refusal in the refusing use case's words (`daemon_api::
RemovalRejection`), exit 48. A removal whose session holds files a person wrote
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
inheritance (by the command, the session's frontend, from the launch the
daemon prepared: U5, below). Both are heard through hooks (a child since owner ruling 51).
A claude hosted child and a claude one-shot turn are launched with
`--permission-prompt-tool mcp__ctxloom__approval_prompt` (step 8 U6, D7;
`engine_claude::APPROVAL_PROMPT_TOOL`, after `--settings`): an ask its
permissions make is put to ctxloom's MCP approval prompt, which waits on the
user's answer through the daemon (Approvals, below). It needs no allow entry,
and no home ctxloom writes registers a PermissionRequest hook of ctxloom's
(probe P0: such a hook fires beside the tool and races it). A turn is named
the prompt only where ctxloom's server rides its launch
(`TurnRequest::approval_prompt`, the launcher's `!no_context`): a launch with
no context (OD-38; the distiller's) carries no server, so no prompt claude
would not find, and its asks are claude's headless default (denied). The
terminal session (claude's TUI) is given none: its human answers at her own
terminal (S28).
Crates:
A one-shot's reply is another model's words, so where no human reads it they
are contained (owner ruling 2026-11-20): in TEXT format with stdout NOT a
terminal (`--format text` asked off a terminal; json is the off-terminal
default, and `Call::stdout_is_terminal` is the one terminal test) `run-cli`
prints `run_application::Answer::contained` (setup-application's `contained_reply`; the CLI may not see that crate): a DRAFT note line, then one
`<message id="<session id>" from="<harp>" urgency="reply">` element whose
body is escaped by the same `escaped` a hook-delivered message uses, so a
forged closing element is data. At a terminal the reply is bare, and json is
unchanged. A one-shot's run also journals the grant record a hosted child's
does (`RunEnqueued.grant`, `run_application::hosted::grant_of`: the MCP
servers carried and the permissions document), which `session show` prints;
the human's terminal session journals none.

- `core/ports/run-ports` (`RunnerLauncher`, `Engine`, `SessionRecords`,
  `RunLocks`) and `core/read-model/pending-launches` (the runs still owed a
  runner, with their launcher's lock, and the runs started and not ended,
  with their session's place);
- `core/application/{run-launcher, run-application}`: the launcher is a
  `CatchUp` over `pending-launches` that drives the engine for one turn and
  records the outcome as facts, and sweeps what other processes left (below);
  `Runs` plans the run through the library's dry-run, enqueues, mints,
  launches, returns the reply (a one-shot's in the daemon that hosts it, U4;
  `run_application::client` is the command's side). Nothing writes the
  session's directory but the launcher's session home (ruling 87a);
- `adapters/driven/host-launcher` (a turn's command through `run_within_started`,
  the interactive launch through `run_terminal`: inherited stdio, the caller's
  process group, no deadline; each on a thread of its own, returning at the
  spawn) and the drive half of
  `engine-claude`; `adapters/driving/run-cli`; and `adapters/driven/hook-tls` (the
user daemon's listener certificate's mint, see the credentials paragraph below;
no run mints one of its own, slice 4 U7).

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
Every engine process is made by THE ONE LAUNCH (T10):
`run_launcher::launch_of` makes a `run_ports::Launch` (the domain value of
`ctxloom.v1.Launch`: its run, kind, engine, found program, args, whole env,
cwd, session home and lock, stdio and stop; beside it, never on the wire,
its input and what it said: the ignored permits and the settings left out)
in one order, set up (`set_up_launch`), the engine's command (the
terminal's `drive_interactive`, a one-shot's `drive_turn`, owner ruling
s4-Q8, or a child's `ChildEngine::command`), the permits, the program
found, the batch check, the length check, each refusing at its step
(`Unprepared`, carrying what the steps before said). `Launcher::prepare`
only calls it, and so does the daemon's `Daemon::spawn_child`; the
launcher starts what it made through `RunnerLauncher::start` (a one-shot's
as a turn, a terminal's or an editor's on the terminal, a child refused),
and a child is started by its engine as made (`ChildEngine::start(&spec,
&launch)`, which only spawns it and speaks the protocol: engine-claude's
child applies no permit of its own any more). daemon-api's `launch`
converts it to and from the wire. The old turn answer enum is
`run_ports::TurnLaunch`.
A command's program is FOUND before it is spawned, a child's turn, the
terminal's and a hosted child's alike, in the one launch (`launch_of` asks
`RunnerLauncher::find_program`; `host-launcher`'s `find`, given the wiring's
filesystem by `HostLauncher::searching`, the daemon's by
`Daemon::finding`): on the command's own `PATH` (the
engine's, led by the program's directory), by `os::Flavor::candidates` (on
Windows PATHEXT's, the command's own or cmd.exe's default), an executable
preferred to a batch file wherever each is on the PATH; the file found is
spawned. std's spawn would try `<name>.exe` alone, so an npm `claude.cmd` was
"program not found" (owner, 2026-11-12). Found nowhere, the run is not
started: `run-ports-program-not-found` (or `-no-path`), naming the names and
directories searched, exit 32. A batch file is run by std through cmd.exe
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
`session_home: host` (the agent's own, else its engine block's: owner
ruling of 2026-11-13, below) says that it runs in her own home, reading and
changing her own memory, MCP servers and history, as the notice
`library-application-notice-session-home-host` (`Notice::SessionHomeHost`,
from the plan's drive in the dry run, so a dry run and a launch say it alike,
with an agent or none; never a refusal; j002200), naming the place that said
it, `{ $place }.session_home: host` (`agents.<a>` when the agent said it,
else `engines.<e>`: the owner's approved sentence for the engine block). On Windows the session home is also its
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
the checkout's `.ctxloom` is the tracked config alone; without it a scratch
checkout is a git root and the walk ends there, never at an enclosing
project). The tools that find their data through `HOME` keep the USER's data
(owner, 2026-11-22): the launch names `RUSTUP_HOME`, `CARGO_HOME` and
`npm_config_cache` to the engine (`run_ports::tool_homes`, set by
`run_launcher::launch::give_tool_homes` in `launch_of`, so every launch kind
and both engines alike), each the value the user's own environment sets when
non-empty (npm's `NPM_CONFIG_CACHE` spelling counts), else the tool's default
under `Place::user_home` (`<user home>/.rustup`, `/.cargo`, `/.npm`), none
where neither exists. The names are owned (the order's `owned`, so no permit
replaces one, ruling 78b) and on all four engine allowlists. Under
`session_home: host` the launch adds none (HOME is hers). Accepted cost: a
session can change her shared toolchain and caches. taskloom's home is not
part of this.
The session home is seeded, once (one there is left
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
`run_ports::check_command_line`, called in one place, `launch_of`, on
`Launcher::on_os(wiring.os_name())` or the daemon's `Place::os`): the terminal session's
opening prompt is the one piece of content on an engine's command line
(claude's positional prompt, opencode's `--prompt`; a child's turn has it on
stdin, which is not measured). One table of constants, no libc call:
Windows, 32767 UTF-16 units for the whole line, its NUL included, counted as
std quotes it (a `.cmd`/`.bat` program as if every unit were escaped);
Linux, 131072 bytes per argument or environment string, its NUL included
(MAX_ARG_STRLEN). Linux's total (ARG_MAX, a quarter of the stack rlimit) and
macOS's (`kern.argmax`, a sysctl) are not constants and the platform crates
read neither, so no total is checked: such a command fails at the spawn as
before. A command over a limit is `run-ports-command-too-long` (exit 48,
`RunError::TooLong`, the launch's `Outcome::TooLong`; the run ends
launch-failed, nothing is spawned), its fix: start the session without the
prompt and paste it, or `run --one-shot`, which reads the prompt from
stdin. The terminal session (claude's and opencode's TUI) is
also given `TERM` and `COLORTERM`, without which it draws in no colour, and
`NO_COLOR`, her choice to have none (no-color.org); a
child's turn is given none of the three. The session home's `ctxloom-hooks.json` calls `ctxloom
hook claude-code execute <Event>`. The interactive home registers the session's
start (`SessionStart`; not its end, owner 2026-11-15: nothing of ctxloom's is
in it), turn, message, tool-batch and compaction (`PreCompact`, `PostCompact`)
events.
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
hooks (the registered companions' loadouts' too: j001800's guard is ltk's
`pre_tool` hook, never at rest in the project) and ctxloom's own callbacks
(session-start, skill-mates) into `ctxloom-hooks.json` beside the capture
events, each once (a child: no
`SessionStart`/`SessionEnd`), and the statusline (`statusLine`) there too;
`deny_tools` is `permissions.deny` laid over the agent's document where it
lives (the session home's `settings.json`, or the hooks file under `host`);
the servers not served by the session are `ctxloom-mcp.json`, written at
every launch and named by `--mcp-config` (variadic: an option always follows
it; a turn's, the terminal's and a hosted child's, `drive::push_mcp_config`),
and it ALWAYS lists ctxloom's own MCP server, `ctxloom` (below, "ctxloom's
own MCP server");
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
The launcher records `RecordRunnerStarted{runtime, workspace, pid}` AFTER the spawn,
with the pid the spawn gave (`RunnerLauncher::launch` answers a `Running`, the
launcher records, `Running::finish` waits), and the pending launches clear on
it, not on the runner's first word; a command that cannot spawn is a
`RecordLaunchFailed` and never a `RecordRunnerStarted`. An interactive run's terminal
session is ended by the human (the terminal delivers Ctrl-C to the shared
process group, so it cancels a generation inside claude).
A ONE-SHOT IS HOSTED BY THE USER'S DAEMON (U4, T7; owner s4-Q8): `run
--one-shot` is its client (`run_application::client`, wired by
`oneshot::client_one_shot`). The command resolves its project through the
daemon (its id minted at its root where nothing has), reaches the daemon
(started if none serves; none can be: `daemon-ports-not-started`, exit 32,
nothing minted or started), and calls `CreateRun{ONE_SHOT}` with the line's
whole request (`CreateRunRequest`'s prompt, agent, engine, profile,
fragments, tags, command, workspace, `no_context`, session), its whole
environment (S7) and its working directory (`cwd`). The daemon hosts it
(`daemon_application::OneShots`, its root's `oneshot::HostedOneShots`): on
a thread of its own, `Runs::one_shot` over the daemon's ONE store (its core
shared, `InProcessCore::share`), in a wiring built from the client's
environment and directory (`oneshot::hosted_wiring`), so it is planned, its
dependencies pulled (`sync.on_run`), enqueued (ruling 73), named, minted,
locked (the session's lock and the run's are the DAEMON's), placed and its
engine's TURN command (`claude -p`, `opencode run`) driven through the one
launcher, exactly as before, its engine the daemon's child. `CreateRun` is
answered once the run is placed (`Runs::telling`, `run_application::
Placement`), or with its `end` (`Run.end`, its name `<parent>/runs/-`) when
it ended before that; the client then follows `WatchRun` (s4-Q2: its
notices, its reply, a `stop`, its `end`) and prints them. A diagnostic
crosses as its code and args flattened as angzarr flattens a cause
(`client::flat_args`), rebuilt by the client (`client::report_of`: an
integer-looking arg as an integer, a dotted name as the cause it wrapped)
for its own catalog, so text output is as before; in json an arg that
reads as an integer prints as a number. `RunEnd.refusals` carries an
aggregate refusal's refusals, and the exit crosses as its code
(`client::exit_of`). A command that vanishes without a signal leaves the run
to its end under the daemon (owner s4-Q7: the tokens are spent for nobody;
its reply is in the run's facts, its words in the transcript); a daemon
killed mid-turn fails the command (10, the stream broken: S8(a), a one-shot
is not run again). `GetRun` and `WatchRun` of a run the daemon does not
host are `RunNotFound`. The daemon counts its one-shots as what it hosts
(idle, `GetDaemon`'s `runs` as ONE_SHOT, a `StopDaemon` refusal), stops
them with its children (`end_all`, as a SIGTERM) and waits for each to end
before its store is closed.
THE INTERACTIVE SESSION IS PREPARED BY THE DAEMON, ITS TUI STARTED BY THE
COMMAND (U5: T9, S3, S17, S20; owner s4-Q6 for init's interview, which is
the same frontend). `run` (and init's interview, `interview::SetupSessions`)
is the session's FRONTEND (`run_application::frontend`, wired by
`frontend::client_terminal`; the in-process interactive path is gone): its
project resolved by the daemon, the daemon reached (none can be:
`daemon-ports-not-started`, exit 32, nothing planned or minted), and
`CreateRun{TERMINAL}` sent as a one-shot's is. The daemon prepares it
(`daemon_application::Terminals`, its root's `frontend::HostedTerminals`):
on a thread of its own (a 16 MiB stack: the whole run is driven on it),
`Runs::interactive` over the ONE store in the client's wiring, planned,
enqueued, named, minted and set up exactly as before, its runner
`daemon_application::Frontended`: the one `Launch` (with the session lock's
path, `Launch.session_lock`) handed to the client in `Run.launch` (the
user's environment crosses to her own client in that answer alone; a read,
`GetRun`, carries none), then the run's stream read (`frontend::
StoreReports`, every `POLL`) for the frontend's start report (at most
`START_WITHIN`, 60 s, else the launch failed) and its exit report. The
session's lock is the FRONTEND's (the daemon's `Runs` takes none): taken
before the TUI starts (held by another: refused, `RecordLaunchFailed` reported,
nothing started), held for its life, let go only after its exit is
reported. The frontend starts the launch on this terminal through
`run_terminal` (exactly its program, arguments, WHOLE environment and
directory), reports `RecordRunnerStarted{pid}` after the spawn and `RecordRunExited`
after the exit (or `RecordLaunchFailed`) as the run's facts through the
coordinator's `Handle`, keyed as the launcher keys its own (`started:<run>`,
`exit:<run>`, `launch-failed:<run>`, `runs_application::launch_failed_book`),
so the daemon's own record of each is a DUPLICATE; the user gate admits
these three facts. Each report is ONE attempt (owner 2026-11-14, a daemon
dying under a live TUI, replacing U5's 60 s retry): to the daemon serving
(found, or started, as a hook's, `daemon::hook_daemon`, within
`HOOK_BOUND`), behind what the session's hold holds (delivered first, in
order), or HELD in the session's hold when no up-to-date daemon takes it
(`runs_application::Holding::report`, the wiring's `DaemonReports`; a
`HeldFact` with the run's `incarnation`, below), for the frontend's next
report or any command's flush to deliver in order; a held report is said
(`held the session's start for the next daemon: <why>`, a draft). The
start-report race (owner 2026-11-14): the run's OWN lock is the
FRONTEND's, taken (`<home>/.ctxloom/runs/<request id>.lock`) BEFORE it
sends `CreateRun` (`CreateRunRequest.launcher_lock`, field 14;
`HostRequest::launcher_lock`, `Runs::held_by_frontend`), journaled as the
run's `launcher_lock` instead of one of the daemon's own while it probes
held (an older frontend's request names none: the daemon takes its own, as
before), and let go once the start report is recorded (applied or a
duplicate), else at the frontend's end; so a daemon started between the
plan and the start report reads the run's owner alive. Under
`test-support` the frontend waits, before it takes the session's lock,
while `~/.ctxloom/test-frontend-gate` is there (the forcing test,
`tui_adopt.test.rs`). The
daemon, once the exit is in the stream, does what the CLI's launcher did:
the checkout released WIP-safe, the
incarnation ended (`runner-exit`); a frontend gone without reporting (its
session's lock free, no exit recorded) is ended as a kill. While the TUI
runs the frontend follows `WatchRun` (reopened on the next daemon when one
dies): a `stop` (the session CLOSED, S20: `Frontended` sees `SessionClosed`
in the session's stream) asks the TUI to stop, then kills it after
`Terminal::STOP_GRACE`; once the TUI is over the run's last notices (said
after the TUI, as before) and its end are read for up to 10 s. A daemon
killed mid-session does not end the TUI; its reports reach the next daemon.
The daemon counts a session it prepares as a client it serves (idle). A
DAEMON DYING UNDER A LIVE TUI (owner 2026-11-14; `daemon-application`'s
`adopt.rs`): a new daemon ADOPTS, at its start, every run its sweep leaves
(owed a runner or started, its owner alive by its locks: the session's
lock, or the run's own the frontend holds; at the start nothing is the
daemon's own, so every survivor is another's; each with its session's
place, project and harp, from its mint, `pending-launches`' `PendingLaunch`
as its `StartedRun`, so a run owed a runner whose start and exit reports
were both held, flushed at the start, lets its checkout go in its own
project), said in its log, and takes
over its END PATH each round (`Daemon::end_the_adopted`): the run's end
(the frontend's exit report, or its `RecordLaunchFailed`) ends the incarnation
(`runner-exit`, or `launch-failed` never started) and lets its checkout go
WIP-safe (the sweep's `LostCheckouts`: only one under ctxloom's
`worktrees`, never forced, one holding work left and said); a frontend
gone without reporting (the session's lock free, the run's own not held,
no end once that was seen) is a kill, `RecordRunExited{signal: 9}` keyed
`exit:<run>` (its late report a DUPLICATE), then the same end; one never
started whose own lock is left free is the sweep's (`launch-orphaned`).
The idle count's sweep (`live_runs`) leaves an adopted run to it. A
draining daemon leaves TUIs untouched, so the daemon after it adopts them
and the two never act on one run (`ctxloom shutdown` asks the TUIs'
frontends themselves to stop: SHUTDOWN). Accepted residual: the checkout's files
a `session_home: host` launch placed are not known to the adopter (none
taken out first), so such a checkout is left, said. A frontend killed
between the plan and its start report leaves its run lock FREE: the next
sweep (or the adoption) ends the run `launch-orphaned`.

A run asked to stop stops its engine first (ruling 114): for the whole run
`ctxloom-wiring`'s `signals` hears SIGHUP, SIGINT and SIGTERM
(`os::Os::signals`, each named), so none ends ctxloom by its default and
orphans the engine. A one-shot stops on any of the three; the session on
SIGHUP or SIGTERM only (a SIGINT is the terminal's Ctrl-C, which reaches the
engine itself, and is let pass). The stop is `HostLauncher::stopped_by(Stop)`'s
`Stop`: a turn's tree is asked to stop (SIGTERM to its group, run-within's
`run_within_stopped_by`), given `Within::stop_grace` (five seconds), then
killed; a terminal session's direct child is asked to stop (SIGTERM), given
`Terminal::STOP_GRACE` (5 s), then killed. The run then ends as any
run whose engine exited (its end facts, the locks released), and the command exits 128 plus the signal's number, the error
`run-application-stopped` (`RunError::Stopped`) naming the signal. A second
signal during the grace is heard and changes nothing; a signal that cannot be
listened for is said as a notice and the run goes on unforwarded. Tested with
the platform's fake (`os::Signals::fake`) and, for a one-shot's SIGTERM, end
to end in j001000. A HOSTED one-shot's stop crosses to its daemon (U4): the
command sends the run's `runs.RequestStop{reason: <signal>}` through the
coordinator (keyed `stop:<run>`, so a second is a DUPLICATE; one heard
before the run is placed is sent once it is; the user gate admits
`RequestStop`), the daemon's tick finds the `StopRequested` in the run's
stream (`Daemon`'s stop step) and requests the run's `Stop`, says `stop` on
`WatchRun`, and ends the run `run-application-stopped` for that signal; the
command keeps reading to the end and exits 128 plus the signal's number. A
stop that cannot reach the daemon is said as a notice; the engine then runs
on under the daemon to its turn's end.
ONE PRINCIPAL, THE USER (the separation model, owner 2026-11-12, slice 2;
separation-analysis.md). On the host there are no principals but the user:
a process of the user, the model included, can do anything the user can
(read every credential, write the store, run every verb). ctxloom defends
its TCP listener (the user daemon's, the only one: no run serves one of its
own, slice 4 U7) against NON-USER callers (other users, web pages,
containers, machines) and promises nothing between the user's own
processes. A run mints NO credential (owner ruling s4-Q3): the hook
bearer's token and its digest went with the per-run listener (`EnqueueRun`
and `RunEnqueued` field 6, `hook_credential_hash`, reserved beside fields 4
and 5, the runner's and the model's, gone in slice 2; `Enqueuer::new` takes
no token source and `Enqueued` is the run alone; the `CredentialHashInvalid`
refusal, `RunState`'s `hook_credential_hash` and `credential_revoked`, and
`runs-application`'s `HookGate` and `HookIdentity` are deleted; M10b mints
per-container bearers with a record of their own). The runner's and the
model's tokens are gone too (`Allowlist::runner/model`, `RunsGate`,
`Admission`, `CredentialsNotDistinct` and runs-application's `Principal`
with them), and so are the scope tables (`Scope`, `hook_allowlist`,
`orchestration_scope`, `OrchestratorGate`): an authenticated caller is the
user, with the user's allowlist (`runs_application::user_allowlist`, every
command and fact the session domain handles, read from the aggregate, since
angzarr's unrestricted allowlist is its own, the runs domain's
`RequestStop`, U4, and the frontend's `RecordRunnerStarted`, `RecordRunExited` and
`RecordLaunchFailed` facts, U5; the coordinator's command half reaches the session
and run domains alone, `EdgeCommands::reach`). What a hook process
receives, through the engine env (like `CTXLOOM_HOME`; both of
`engine-claude`'s allowlists, `ENV_ALLOWLIST` for a child's turn since ruling
51 and `INTERACTIVE_ENV_ALLOWLIST`; always the run's own values, set by
`run_ports::name_the_run` for a terminal, a one-shot turn and a hosted child
alike, every inherited hook variable taken out first, never passed on), is
`CTXLOOM_SESSION`, the session's resource name `projects/{p}/sessions/{s}`
(slice 3, s3-Q3: the ONE session variable; `CTXLOOM_SESSION_HARP` is retired,
hard migrate), whose last segment is the harp a statusline shows and whose
UUIDv5 (`sessions::SessionName::id`) is the stable id a hook addresses its
facts to (review finding 6: the wiring's `RunIds`), and `CTXLOOM_RUN`, the run
id. With `CTXLOOM_HOME` a hook finds the user's daemon by its discovery file
and presents the user bearer it names (U2). No host run tells its engine
`CTXLOOM_ENDPOINT`, `CTXLOOM_BEARER` or `CTXLOOM_TLS_CERT`
(`run_ports::LISTENER_VARIABLES`; `no_listener.test.rs` runs a one-shot and a
terminal session and finds none of them, and no ctxloom process listening but
the daemon's endpoint). KEPT FOR M10b's container arrival (OD-13), each with a
doc comment saying so, to be flagged as dead code if M10b lands without
calling it: those three variables, `run_ports::hook_environment` (nothing on
the host calls it: `workspace.test.rs`'s
`test_nothing_but_the_m10b_kept_list_calls_hook_environment` holds its callers
to run-ports' own module), `Bearer::compose|parse` (the composite
`<run-id>.<64 hex>`), `PublicCert`'s environment form (base64 of the DER on
one line), `CertNames::for_plan`, the bind table (`run_ports::bind_for`, its
container rows ruling 33's tested table and typed refusals, no row wired to
a launch) and the hook's env-named arrival (a hook that finds
`CTXLOOM_ENDPOINT` set delivers there alone, `HookSink::from_environment`,
never to the daemon and never spawning one; `https://` only: `http://` is the
typed `run-ports-endpoint-plaintext` and is never dialed, and port 0 is
`run-ports-endpoint-port-zero`, for the builder and the parser both; a missing
or garbled certificate is `HookSink::Refused`, `hook-cli-not-recorded` naming
`CTXLOOM_TLS_CERT`, and nothing is sent). The URL's build and parse, the
variables, the certificate's codec and the bind table are pure functions in
`run-ports` (`hook.rs`, `tls.rs`, `bind.rs`). Credential values never render:
the `Debug` of every type that carries an engine environment (`TurnCommand`,
`TurnRequest` and `Order`, through `run_ports::RedactedEnv`; `Permits`) prints
the variables' NAMES and withholds EVERY value, whatever the name, so there is
no list of sensitive names to keep in sync, and `Bearer` and `Token` never
render (redaction keeps the user's secrets out of output the model and her
transcripts read; it is not a separation). A process of the user, the model
included, can do anything the user can: read every credential, write the
store, run every verb (rulings 37's "never forge", 38 and 27's residual are
retired). The callback is TLS, server-auth only, no mutual TLS (ruling 36):
the daemon's certificate authenticates the SERVER to the client, which pins
it as its SOLE trust root (no system roots, no fallback to plaintext), and the
user bearer keeps non-user callers off the socket. The certificate is minted
at the daemon's start by `hook-tls`'s `Minter` behind `run_ports::CertMint`
(rcgen on ring, ECDSA P-256, a non-CA `serverAuth` leaf; its window is
explicit, rcgen's default starts in 1975), and the client's `domain_name` is
the URL's host. The private key lives in memory in
`run_ports::HookCertificate` (whose `Debug` withholds it) and goes to
`hook-listener`'s `TlsIdentity` and nowhere else: never journaled, logged, in
an environment or in an error. The listener's limits are angzarr's generic
`Listener` defaults (angzarr `71b453a4`; `hook-listener` sets none): a TLS
handshake within 10 s, an HTTP/2 keepalive ping every 60 s answered within
20 s, a connection closed after 1 h plus a 30 s grace (never zero: a zero
grace is `ListenError::ZeroConnectionAgeGrace` at bind, which
`hook-listener` carries as its bind error's cause), at most 200 streams per
connection, and no request timeout of the server's own. There is still no
limit on the number of connections; a request is capped at `hook-listener`'s
`MAX_REQUEST_BYTES`.
Revisit trigger: agents run in containers (a per-container bearer and real
scopes return there; angzarr's `Gate`/`Allowlist`/`Principal` seam and
`bind_for` are kept for it). A one-shot child's model can read the same
environment, so it can write any session.
The run is driven on a multi-thread runtime (`ctxloom-wiring`'s `run_runtime`,
one worker), because `Running::finish` blocks the driving thread.
A turn's wait PARKS a thread of the blocking pool and never an async worker
(owner, 2026-11-19): the launcher waits through `run_ports::Parks`
(`Launcher::with_parks`; the wiring's `BlockingParks`, `daemon/runtime.rs`),
`finish` itself staying blocking. The parked threads need no cap of their
own: a runner runs one agent and an agent has one turn at a time. A steer
reaches a hosted child through its `ChildControl` (a lock and a write to its
stdin), not through the thread waiting on its turn.
No run serves an endpoint of its own (slice 4 U7, owner ruling s4-Q3): the
per-run `HookListener`, its certificate's mint, its `HookGate`, the run
use case's `serve` (with `run_ports`' `HookServer` and `Serving`, and
`RunError`'s `Bind` and `Listener`, `run-application-listener`) and
`ctxloom-wiring`'s `Hooks` are gone; the hooks of every run, a hosted
one-shot's, a terminal session's and a hosted child's, deliver to the user
daemon's one listener. Under `test-support` a hook process (its `HOME` the
session home) finds that daemon through its `CTXLOOM_HOME`, which
`daemon::account_home` takes for the account's home after the test's own
override, as a shipped binary finds the account's real home. `run --session <harp>` (owner ruling 115,
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
native id is bound from its turn's stream (a daemon child's clear, its
stream's `conversation_reset`, recorded as the clear then the new one's
bind); a terminal session's is the one its SessionStart hook bound (owner
2026-11-15, H-Q9 folded in: an observation of the event, `bind:<session>:
<native_id>`, delivered and held like every capture fact, never written in
the hook's own process; a clear's SessionStart rotates it, the conversation
before kept in the session's `rotations`), and nothing is bound at the exit
(opencode binds none from its hooks yet, so an interactive opencode session
cannot be resumed). A CLOSED session is REOPENED by its resume (owner
2026-11-15: a close is ctxloom's control of its sessions, not the engine's
conversation): `ResumeSession` records `SessionReopened{cause: resume}`
before the incarnation it owes, which clears `closed` and `closed_cause`, so
the session takes work again; nothing else reopens one (a re-host, a
message and a clear of a closed one stay refused). A session has NO stop
latch (owner 2026-11-15, the design note's Q9): `SessionState.stopped`
(field 20, reserved), `StopSession`, the `sessions-child-cannot-stop`
refusal (`ChildCannotStop`) and runs-application's `AgentRequests` (whose
one kind it was) are gone, and `StopOrdered`, in streams recorded before,
is RETIRED in place (still decodes, folds to nothing, never emitted); a
wake, a start and a re-host no longer ask whether a session was stopped.
Close is the one "no more work"; stopping a running session is its run's
`runs.RequestStop`, which ends that run (so its incarnation) and nothing
more (the `session stop` verb, its scenarios `@pending`, is not built).
Not built:
the hidden runner subcommand.

Delegation on the daemon (step 9, owner rulings 2026-11-17). A hosted
child's run enqueue journals what the child was GIVEN (`RunEnqueued.grant`,
`runs.v1.Grant`: its MCP servers by name, ctxloom's own unless the launch
has no context, and its permissions document; never a server's command or
environment), and `session show` (text and json, `Session.grant`) says its
latest run's, so a later `agent edit` changes only the next child's record;
a one-shot journals none yet. `session show` also says how the latest run
failed (`Session.last_failure`: its last turn's class, `rate-limited`
among them, or `launch-failed`, with a notice saying what to do); nothing
retries a failed turn, `session send <harp> wake` resumes it. `session stop
<harp>` asks the session's current run to stop (`runs.RequestStop`, keyed
`stop:<session>:<n>`); the daemon's `Stop` step ends a hosted child's
engine, its incarnation ends `stopped`, and the child stays open, its
hand-off kept, for its next message. Each incarnation a child's run ended
is told to its open owner (`child <harp> ended its run <n>: <cause>`), but
an end a re-host continues (orphaned, drained) and an idle reap. A hosted
child's checkout is made with git in its LAUNCHER's environment (the
hand-off's: her `GIT_*`, her `SSH_AUTH_SOCK`), a sweep's in the daemon's
(`ChildWorkspaces::of`'s `env`). Not built: waking a terminal orchestrator
(its informs stay pending in `session show`).

The locks a run is judged by (owner rulings 85, 86, 92, 93, defects 1 and 3;
advisory file locks through the platform's `file-lock`, taken at once or not
at all, never a pid). The run's OWN lock, `<home>/.ctxloom/runs/<run>.lock`
(`liveness::RunLockFiles`), is taken before its enqueue is visible, journaled
as `RunEnqueued.launcher_lock` (field 12, as in `EnqueueRun`), held in the
`Orders` and let go once `RecordRunnerStarted` is recorded (or the launch failed);
a terminal run's is its FRONTEND's instead (`runs/<request id>.lock`, taken
before it asked; U5's start-report race, above);
its file is removed as it is let go, so only a dead holder's is ever left (a
refused run leaves nothing in the project). The SESSION's lock,
`<project>/.ctxloom/sessions/<harp>/session.lock`, is taken once the session is
minted and held until after its engine exits (an
interactive session's by its frontend, from before its TUI starts until its
exit is reported, U5), so a live session reads live (and `session remove` refuses it without
`--even-if-live`). A launcher's step sweeps other processes' runs: one still
owed a runner whose own lock is free, and one started whose session's lock
is free, have lost their owner; it probes, re-reads the run's stream (an
owner records its start or end before it lets its lock go, so a normal end
wins the race), then records the run's end, keyed by the run (a second
sweeper's is a DUPLICATE): `RecordLaunchOrphaned` (`launch-orphaned:<run>`,
`END_CAUSE_LAUNCH_ORPHANED`) for one owed a runner, since no launch failed
(ruling 100), and `RecordRunnerLost` (`runner-lost:<run>`, `END_CAUSE_RUNNER_LOSS`)
for one started (ruling 95); no orphan claims `RecordLaunchFailed`. A started run
whose OWN lock (`RunEnqueued.launcher_lock`, which `pending-launches` keeps
on a started run) is free has lost its owner too: a host that held it for
the run's life, as the daemon does for a child it hosts, is gone (T3, T18);
a primary removes its file once its runner started, which proves nothing.
A child the daemon hosts holds its SESSION's lock too, for its life
(slice 4 U6, the owner's ruling on U4: `ChildHomes::hold_session`, the
platform's file lock at `<project>/.ctxloom/sessions/<harp>/session.lock`, its
directory made; one another holds starts nothing, tried again later), so
another launcher's sweep (a hosted one-shot's `Launcher::step`, in the
daemon's own process) never takes a live hosted child for lost where a
session lock file was left free by an earlier run. A started run's turns
still open (the one in flight, any queued behind it) are ended FIRST,
`RecordTurnFailed{class: TURN_FAILURE_CLASS_RUNNER_LOST}` keyed
`turn:<run>:<turn>:failed` (S18: after the run's end it records no turn's
end), so its owner is told of each through the notice policy. Then the
session's incarnation ended (`orphaned`). The sweep is ONE
implementation, `run_launcher::Sweeper`: `Launcher::step` runs it over the
runs it holds no order for, and the daemon's launcher runs it as its first
step (below), `releasing` what each lost run held (`Releases`, OD-19: its
checkout now; a container in M10b): its `Swept` names the run's project and
the workspace its `RecordRunnerStarted` recorded, and `released` what came of it. Every
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
(which holds no permissions in the session home)
(owner ruling 145: claude 2.1.322 refuses a settings file whole for one
invalid value, hooks included, so a bad value disables the block alone; the
probe is in the ruling-145 commits). User scope is claude's lowest: a single
value the project's `.claude/settings.json` also sets is the project's, lists
merge, and it is where `defaultMode: auto|bypassPermissions` may take effect.
Under `session_home: host` (the config dir is the user's own `~/.claude`,
never written) the block stays the `permissions` of the hooks file, with the
cost that a bad value there disables the hooks too. No `--permission-mode` is
passed, claude's mode is the document's `defaultMode`. A profile's
`deny_tools` rides the launch (above), laid into the same `permissions.deny`,
and ctxloom ALLOWS its own MCP server's tools by laying `permissions.allow:
["mcp__ctxloom__*"]` into the same document the same way (after its own
`allow`, once; `engine_claude::drive::OUR_TOOLS` and `laid`; OD-4, owner
2026-11-13, amended 2026-11-14: a stated exception to rulings 133-139's
"verbatim", as `deny_tools` already was), where the document lives: the
session home's `settings.json`, or the hooks file under `host`; never
`ctxloom-hooks.json` in the session home. A launch with no context, which
offers no server, lays none. engine-opencode writes the session home's `opencode.json`
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
(the plan's count) and `fatal` (the findings themselves, `FatalFinding` text and
remedy, additive, owner 2026-11-21: a child's launch has nobody else to say
them; a one-shot's are the library's and are left empty). `RunEnqueued` carries
none of it. The checks, in order: a one-shot's prompt (`runs-no-prompt`), the
engine (`runs-unknown-engine`, both INVALID_ARGUMENT), then a child's
credential (`runs-file-only-credential`, `runs-no-credential`), which an engine
nobody drives is not judged on (73d: such an engine is planned anyway, on the
agent's own engine, so the findings and the prompt still report). Every check
is run: one failure is its own refusal, unchanged; two or more, or one beside
the plan's fatal findings, are ONE `runs-launch-refused` (`LaunchRefusals`,
FAILED_PRECONDITION) holding each in check order, said as one json envelope
with `refusals: [{code, args, message, fix?}]`, or in text the summary line then
each refusal's `ctxloom:` and `  fix:` lines (cli-support's `Failure::refusals`
and `say_failure`). Fatal findings alone are the library's
`library-application-startup-aborted`, as before; beside a refusal they are the
plan's warnings, said beside it, and the exit is 49 (the most severe wins),
else 48. A launch that carries them (a child's: `CreateHandOff`'s pre-check,
`daemon_api::Planned::fatal`, filled by `ApiHandOffs::plan` from the plan's
`previewed.fatal`, and `LaunchChild`'s enqueue, `Evidence::fatal`) refuses with
them in `LaunchRefusals.findings` (field 8; `RunRejection::LaunchRefused.findings`,
the `LaunchFinding` diagnostic: `runs-fatal-finding` with its remedy as the
`-fix`, `runs-fatal-finding-said` with none; the wording a DRAFT), and
`daemon_api::CallError::refusals` lists them after the checks, so `session
launch` prints each as a refusal line (text) and under `refusals` (json); the
`count` is unchanged. The run is enqueued FIRST (73g): plan, evidence, `EnqueueRun`, the
project told, a name claimed, `MintSession`, the session's lock, the order.
So a REFUSED run leaves nothing: no name, no session, no directory, no event,
no open incarnation. A failure after the enqueue (the project, the names, the
mint, the session's lock, the first catch-up) ends the run with a `RecordLaunchFailed` fact, so no run stays
owed a runner, and a session already minted has its directory written with
the run's end in it.

`crates/core/clippy.toml` holds the core's purity rules (no I/O, clock, env,
threads or hash-ordered collections) and reaches every core crate, because
clippy reads the nearest `clippy.toml`. A crate's own `clippy.toml` would
replace it, so none has one. It repeats the root `clippy.toml`'s settings,
and `just lint` refuses to run if they disagree.

The engine block reaches the drive (engines track 4). The plan's `DryRun.drive`
(`library::config::engines::Drive`: `command`, the effective `session_home`
(the agent's, else the engine block's), the `auth` source, the agent's
`env`) becomes `run_ports::EngineSettings`
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
`OPENCODE_*`) or the config said; it is applied in one place, the one
launch (`run_launcher::launch_of`, a hosted child's too), after the engine
built the command (`run_ports::apply_permits`). The
`CTXLOOM_PERMIT_*` variable is never passed on; a permit for a `CTXLOOM_*`
name is ignored and the run says so as a notice (by the permit's name, never
its value); no value renders in a `Debug` (every environment value is
withheld, see the credentials paragraph). An explicit `CTXLOOM_PERMIT_ANTHROPIC_AUTH_TOKEN` does give a
child the primary's token (78a): the cost is that such a child holds the
primary's own token, no distinct per-child credential. A terminal session's
launch (`LaunchKind::Terminal` alone, `run_launcher::launch_of`, after the
permits above are applied) also puts `CTXLOOM_PERMIT_<N>=v` in the engine's
environment for each of the engine's child-credential names `N`
(`CredentialPolicy::child_env`, so never `ANTHROPIC_AUTH_TOKEN`) the user's
environment holds non-empty (`run_ports::permits_for`; a permit the caller
already set stays): claude withholds its own token from the `ctxloom mcp
serve` it starts, which inherits the permit, so a `child_launch` call's
environment carries it into the hand-off, and the child's credential evidence
counts the permit spelling (`run_application::credential::holds`). A child's
turn, a one-shot and a hook carry none.
A permit never
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

## The general flow: facts in, aggregates decide, projections drive the work (owner, 2026-11-18)

THE pattern for every domain (owner: "events/facts come in, aggregate
decides, emits events, projector/agent runner handles those events
(possibly batching them)"; "we read from the projection, and the
projection updates the worker agents"):

1. Outside state enters as FACTS (an angzarr `Fact`), keyed by
   `FactOrigin.external_id`, so a resend is a DUPLICATE (the coordinator
   derives the command id from it). A tree's files enter as one
   `RecordTreeListed`; a hook's observations as the capture facts.
2. AGGREGATES decide and apply, purely (`#[handles_fact]`, `decide`,
   `#[applies]`): the library aggregate compares a listing with the hashes
   its state holds and records only what differs.
3. PROJECTORS fold events into read models (angzarr `ProjectorHandler`,
   caught up by `CatchUp`: held in memory, `CatchUp::new`, or with its
   positions in the store, `CatchUp::stored`).
4. Every READ answers from a projection (the session index, the library
   checkouts, the board), never by folding a stream or observing per call
   beyond its own need.
5. Event HANDLERS (a projector, or the agent runner that hosts the
   sessions) consume those events, possibly batching them, and drive work
   outward: the runner sets or delivers what a session is owed, and the
   edge (a hook, or a stream) hands it to the engine. Built: the daemon's
   loop (`daemon_application::Daemon::tick`, the runner) steps
   `daemon_application::owed::OwedContexts` under `CatchUp::stored` over
   the library and sessions domains (positions under `owed-contexts`, so
   a later daemon resumes and a re-handed book records nothing twice); it
   records `RecordContextOwed` on each live session a change touches
   (keyed `owed:<session>:<tree>@<seq>`), the session index folds it, and
   the hook only COLLECTS (`RedeliverContext`). `WatchLibrary` is woken by
   the fold itself (`LibraryCheckout::watch`), never polled.
6. A CACHE in front of an observation is the edge's (never projector
   state), keyed by stat; an invalidation (`ctxloom context refresh`,
   `Observations::forget`) wipes the memo, never the projection, and the
   re-observation emits facts only where content hashes differ.

## Re-injection is projection-driven (step 10c U13 rework, owner 2026-11-18)

A live session whose loadout a library change touches is handed its
re-assembled context at its next hook, ONCE per batch:

- Per-tree stream POSITIONS, not time: a session's loadout records the
  positions its project's trees were at before its plan
  (`RecordLoadout.covered`, read from the checkouts projection by the
  daemon; a one-shot's and a seed's cover none, and then a change recorded
  after the loadout is owed). The session index folds
  `session_index::Handed` (`loaded_at`, `covered`, `owed`: one
  `OwedChange {tree, sequence, bundles, paths}` per tree and sequence).
  A loadout is also owed at once what its trees changed past its
  positions (`owed-at-load:` keys), so a change between the plan and the
  loadout is not lost.
- COLLECTION (`daemon_api::Api::redeliver_context`, then `collect`): the
  positions it covers are read BEFORE the re-assembly, so a change recorded
  mid-delivery stays owed (the race forced in daemon-api's reactions
  test); one delivery covers every change owed (batching), keyed
  `reinject:<session>:<H(positions)>` (`sessions::positions_text`), and
  records `RecordContextRedelivered.covered`. No round is awaited: a hook
  observes its trees only past `HOOK_TRUST_SECONDS` (300) since their last
  scan (`Observations::observe_trees_trusting`), and what that records is
  collected at a later hook. No round, timer or watcher observes a tree
  (G5(b) superseded); every read, preview and write scans (through the
  memo).
- ONE BUDGET per hook answer, claude's 10,000 characters
  (`run_launcher::HOOK_CONTEXT_BUDGET`): `RedeliverContextRequest.budget`
  is what the parts before it left (0, the whole). Over it,
  `run_launcher::redelivered_within` gives a framing line and only the
  changed fragments, whole (asked of the library by the owed paths), else
  a pointer to `ctxloom context assemble`, else nothing (still owed).

Step 10 U11's forcing checks are the binary's `workspace.test.rs`:
`test_no_cli_crate_depends_on_an_application_crate_but_the_departures` (no
`*-cli` driving crate's `[dependencies]` names a `crates/core/application`
crate; today's 23 edges are `CLI_APPLICATION_DEPARTURES`, each with what it
is used for, because step 10a U2's deletion of them is not done; a new edge
or a stale entry fails) and
`test_nothing_in_the_daemon_opens_a_library_tree_outside_the_observation`
(the daemon's root sources, `ctxloom-wiring`'s `daemon/`, `oneshot.rs` and
`frontend.rs`, open a library, `library::build(`, `library::build_over(`,
`build_setup(`, `build_setup_over(`, only at `DAEMON_LIBRARY_OPENS`' files and counts, each
reached through the observation or a stated departure; daemon-api and
daemon-application open none). A hosted one-shot and a terminal run observe
their trees first (`Api::observe_for`), as a setup read does, and so do the
trust preview (`GetTrust`, at the project's registered root) and a hosted
child's hand-off (`CreateHandOff`, before its plan); the one departure is a
hosted child's plan and output root read again at each launch (the daemon
loop's `start`, which has no observation to reach). A plan's loadout
(`run_application::loadout_of_plan`) names a local bundle by its directory
(`ctxloom+local:demo` is `demo`), as a change's path is judged against it:
spelled as referenced, a bundle edit observed apart from a config edit owed
a hosted child nothing.

## Architecture: hexagonal around CQRS/ES (checked by `just lint-arch`, and in review)

Dependencies point inward; a review refuses a change that breaks these, and
`just lint-arch` (part of `just lint`) holds every edge between workspace
crates to `arch-rules.toml`: a table of which role (`crates/<ring>/<role>`)
may depend on which, with the known departures listed as exceptions, each with
its reason and a fix (debt; a stale one fails). The lint is rust-support's
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

Two trees of answers that are not a success (owner ruling 2026-11-14), each
with its root:

- BUSINESS REFUSALS: commands only. A fact is never refused for a business
  reason (it already happened: its handler records it, or records nothing,
  and its one failure is a bug). The status is the refusal message's
  `(refusal_status)` alone (below). Roots: each domain's `refuse!` macro
  (`sessions`, `runs` and `scheduler`'s `src/refusal.rs`), reading
  the generated `Refusal::REFUSAL_STATUS`; at the edge `daemon_api::
  ApiRefusal::of` (a daemon proto refusal's `REFUSAL_STATUS`) and
  `ApiRefusal::rejected` (a domain's, as the kernel coded it), answered by
  `daemon_api_grpc`'s `status_of_error`; and the hook listener's
  `runs_application::hook_commands::coded_of` (its `Failure::Rejected`
  arm, the status the refusal declared).
- INFRASTRUCTURE ERRORS: what is wrong with the request's surroundings,
  never a business decision: a session the daemon has no record of
  (`run_ports::NO_SUCH_SESSION`, NOT_FOUND), an etag or expected-sequence
  conflict's ABORTED (angzarr's own spec keys, their status the
  `AngzarrReason` surface in angzarr's protos), a malformed book, a store or
  kernel failure (INTERNAL), an unserved method (UNIMPLEMENTED). Their
  status MAY be named in Rust, but only at its ONE root:
  `run_ports::Infrastructure` (`crates/core/ports/run-ports/src/
  infrastructure.rs`), an enum naming each error with its status once
  (`code()`, angzarr's transport-free `Code`; `status()`, its number) and,
  where one status carries two, its reason key (`key()`: `SessionUnknown`
  NO_SUCH_SESSION, `Parked` angzarr's `DEAD_LETTERED`, `Unmet`
  `run_ports::DIAGNOSED`, a host precondition unmet said in a diagnostic of
  its own: git missing or too old, FAILED_PRECONDITION, owner ruling
  2026-11-17, never UNAVAILABLE, which a client reads as an unreachable
  daemon). Both listeners
  answer through it: the hook listener's command half
  (`runs_application::hook_commands`: a kernel failure's `Malformed` or
  `Failed`, an unknown session's `SessionUnknown`) and the daemon's service
  (`daemon_api_grpc`'s `status_of_error`: `ApiError::Failed`,
  `Unimplemented`, `Diagnosed` as `Unmet`; through `daemon_api`'s re-export, as a driving adapter
  reaches no port). Both clients read an answer back through
  `Infrastructure::read(status, keys)` (`hook_sink_grpc`'s
  `delivery_of_code`, `daemon_client::called`), each mapping the error to
  its own outcome. The workspace test
  `test_no_status_is_spelled_in_rust_but_at_the_infrastructure_root` refuses
  a gRPC status by name (`Code::<Name>`, tonic's `Status::<name>(`) in any
  Rust source but the root; a `*.test.rs` file, which plays the wire's
  other end, may spell one. No status or message on the wire changed.

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

Names follow the naming rule (OD-53, owner 2026-11-15; ctxloom-personal's
`event-sourcing-naming` fragment): a command is imperative (`CloseSession`),
a fact is a request to record, `Record*` (`RecordRunnerStarted`,
`RecordAlarmDue`), an event names its subject in the past
(`SessionClosed`), and a refusal names a state, never an event or a verb
(`SessionIsClosed`, `RunIsEnded`, `ProjectIdentityMismatch`). Unit N1 renamed
the free ones: every run fact, the scheduler's `RecordAlarmDue` and
`RecordAlarmReached`, the refusals `RunIsEnded`, `LaunchRefusals`,
`OwnerIsClosed`, `SessionIsDestroyed`, `ProjectIdentityMismatch`,
`ApprovalIsSettled` and daemon-api's `RemovalRefusal`, and the fixture's
`DummyClosed`. A refusal's rename is a wire change only: its diagnostic CODE
(`runs-ended`, `runs-launch-refused`, ...) and its `*Rejection` variant are
unchanged. STORED events keep their names (`Compacted`, the names domain's
`Claimed` and `Released`, the run's `*Recorded` events): a stream holds them
by type URL. Facts are never stored by type, but a hold file carries a held
fact's book, type URL and all, so a rename is safe only because `ctxloom
shutdown` flushes the holds before an install (no alias table).

## The pins: one of each

The lock holds ONE `angzarr-embedded`, `angzarr-proto`, `angzarr-ports` and
`angzarr-diagnostic` (a test reads Cargo.lock). Every pin in `Cargo.toml` is
a full commit. The angzarr pins are one commit of one repository,
`71b453a4` of github.com/angzarr-io/angzarr (core, `angzarr-embedded` and its
router, the platform crates, `angzarr-build` and `angzarr-codegen` were five
repositories and are crates of that workspace now; `angzarr-coordinator-grpc`'s
`tls` feature, which `hook-listener` enables, is `Listener::tls`). taskloom's
crates (`d43fb683`) and
rust-support's (`5bc1d60f`) are built on the same commit, so moving one moves
all. The lock holds one of every angzarr crate. The platform crates (`os`,
`file-system`, `file-lock`) are `Apache-2.0 OR BSD-3-Clause`; the shared runtime
crates (`angzarr-diagnostic`, `angzarr-presentation`, `angzarr-yaml`,
`angzarr-embedded`...) are AGPL-3.0-only with angzarr's runtime exception and
the coordinators with its gRPC API permission, each allowed by name in
`deny.toml`; this workspace is `Apache-2.0 OR BSD-3-Clause`.

The API protos' annotations and their linter are pinned too (owner rulings
S36, S37, S57, D6; step 10 U1). googleapis' `google/api/{resource,
field_behavior,client,launch_stage}.proto`, `google/longrunning/
operations.proto` and `google/rpc/status.proto`, with googleapis' LICENSE
(Apache-2.0), are vendored under `proto/third_party/google/` at ONE commit,
`ff8fd20f` (2026-10-30), each file's SHA-256 in its manifest
`proto/third_party/google/SHA256SUMS`, the one place the commit and the
digests are spelled: `just vendor-googleapis`
(`scripts/vendor-googleapis.sh fetch`) fetches them and refuses one whose
digest is not the manifest's, and `check` holds the tree to it offline.
angzarr's `google/api/annotations.proto` and `http.proto` are not vendored,
and operations.proto, which imports them, is imported by nothing yet.
daemon-proto's build adds `proto/third_party` to its include path; the
annotations are options, so nothing of googleapis is a field. api-linter
is release v2.4.1 (linux-amd64 archive SHA-256 `adf6b94b...`, the digest
GitHub publishes for it), fetched once into
`~/.cache/ctxloom-api-linter/2.4.1/` and checked before it is unpacked
(`scripts/lint-api.sh`); `just lint-api` runs it over `proto/ctxloom/v1`
with `proto/api-linter.yaml` (no rule disabled there; each departure is a
commented exception at its site, S38's `answers`, S44's folded streams),
angzarr's own protos found at the locked commit by `cargo metadata`. It
runs on Linux x86_64 only and is NOT yet part of `just lint` or CI: the
drafts' findings it still reports (about 2.8k: missing comments and field
behaviours, and findings that would change the wire, awaiting the owner)
fail it.

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
rust-support's (`github.com/benjaminabbitt/rust-support`), a git dependency pinned by
full commit over https. Its angzarr pins are this workspace's, and the lock
holds one `cli-support` (the same test). The application's own global flags
(`library_application::Flags`) ride in the call as `Call<Flags>`'s `globals`;
rust-support itself knows no ctxloom word (its vocabulary check).

Every adapter that runs a program (`remote-git`, `worktree-git`,
`companion-probe`, `container-runtime`, `taskloom-cli-client`) runs it
through rust-support's `run-within`: the program under a deadline, its whole
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
own, so a Ctrl-C at the terminal does not reach it. The git adapters
(`remote-git`, `worktree-git`) share ONE git runner, the
support crate `git-cli` (G9): the environment scrubbed of the variables git
calls repository-local (`git rev-parse --local-env-vars`, asked once; every
`GIT_*` when it cannot be asked), never interactive (`GIT_TERMINAL_PROMPT=0`,
empty `GIT_ASKPASS` and `SSH_ASKPASS`), under `run-within`; each adapter
says a failure in its own words. A caller that must stop
a run on a signal hears it (`Os::signals`) and requests the `Stop` it ran the
program with (`run_within_stopped_by`: the tree asked to stop, `stop_grace`,
then killed; the outcome `stopped`).
`run_within_started` also calls a `started(pid)` once after a successful spawn
(never on a spawn failure). `run_terminal(spec, &Stop, started)` is the mode
for a program that owns the terminal: inherited stdio, NOT a process group of
its own (a Ctrl-C reaches it), no deadline, and a `Stop` (a cloneable flag the
caller sets, e.g. from `Os::signals`) that asks only the DIRECT child to stop,
not its descendants (SIGTERM to that one process, `os::process::request_stop`;
none on Windows), gives it `Terminal::STOP_GRACE` (5 s; `run_terminal_with`
takes a `Terminal` of another), then kills it. Tested on unix; macOS and Windows are not. No adapter bypasses
`run-within` any more.
All of rust-support's crates are pinned at one rev (`cli-support`,
`process-env`, `project-dir`, `run-within`), and so is each family above.

## Co-developing a sibling or rust-support

In development the whole git dependency family (angzarr, harp, taskloom,
rust-support) is used by path, through `cargo local-pins` (a separate cargo
subcommand, installed per machine). `cargo local-pins [NAME...]` writes the
gitignored `.cargo/local-pins.toml` (first line `# Generated by local-pins.
Machine-local paths: do not commit.`): a `[patch."<url>"]` per pinned git
source, every crate mapped to its path in a sibling checkout; with several
checkouts it asks, or off a terminal refuses and lists them (`--pick LABEL`
chooses; `--dry-run`, `--yes`). It adds `include = [{ path = "local-pins.toml",
optional = true }]` to the gitignored `.cargo/config.toml` and checks cargo
uses each patch (`cargo update -p` when a version moved). There is no refresh:
cargo reads the checkouts live, and it is rerun by hand when the pins move.
`cargo local-pins clear` removes the file and restores the pre-patch
Cargo.lock. Nothing path-based is ever committed: the pre-commit gate refuses
an outside-repo path dependency and a tracked `.cargo/config.toml` or
`.cargo/local-pins.toml` (`scripts/path-deps-check.sh`, and `just
dev::pins-check`, which also wants full-hash pins), and the gates build the
staged tree without the patch.
Under the patch Cargo.lock loses its git sources, so the two lock tests in
`workspace.test.rs` that read sources (`test_every_git_pinned_crate_is_in_the_lock_once`
and `test_the_git_pins_of_the_one_of_each_crates_are_the_pins_of_one_repository_each`)
skip, printing `SKIP <test>: <reason>`, only while `.cargo/local-pins.toml`
exists and holds a `[patch` table; without it they assert as ever, and the
commit gates (which build the staged tree without the patch) run them.
The shared dev module (rust-support's `tools/dev`, linked once per clone as
the gitignored `.dev` by `tools/dev/install.sh <this checkout>`; the justfile's
`mod? dev '.dev'` is optional) keeps the pin recipes: `just dev::repin` moves
every pin of a registered dependency (Cargo.toml and `pins.toml`'s
`arch_lint_rev` in the justfile) to that checkout's HEAD; `just dev::land`
prints, and with `--go` performs, the push order across repositories; `just
dev::pins-reachable` (pre-push) refuses a pin that is not on its remote.
Re-pin once per landing: push the sibling (rust-support's gates and mutation
first), bump the revs in Cargo.toml, `cargo local-pins clear`.

## The daemon's one launcher, and the read models it registers (slice 1)

The user daemon (`ctxloom daemon serve`) hosts its children through ONE
launcher in the application ring (owner T18): `core/application/
daemon-application`'s `Daemon`. Its first step, `start_up`, catches up
every read model it plans and its API answers from (the board, the pending
messages, the session index) with the store's log and then sweeps
(`run_launcher::Sweeper`, above) and ADOPTS what the sweep leaves (a live
TUI's run, its end path then the daemon's: U5's paragraph, "A DAEMON DYING
UNDER A LIVE TUI"), all before the root publishes the
discovery file (`serve.rs`'s `swept_and_advertised`), so a fresh daemon
serves nothing (no fact, command or read: the user bearer reaches no one
before the publish) from folds older than the log (owner, s4-Q14 H3's
condition). A START-UP THAT FAILS (its catch-up or its sweep; owner ruling
OD-52) still publishes the daemon, and is LOUD: the log says `WARNING: the
start-up catch-up failed: <why>; serving anyway ...`, the root's `Shared`
keeps why (`daemon_api::Hosting::start_up_failed`), `GetDaemon` answers it
as `Daemon.warnings` (field 11, each a `Notice`: daemon-api's
`StartUpIncomplete`, `daemon-api-start-up-incomplete`, arg `why`, with a
`-fix`; wording a DRAFT for the owner), `daemon status` shows it (text
`warning: ...` and its `  fix:` line; json `warnings`), and every command
that reaches that daemon says each warning on stderr as it ends, its
answer unchanged, from the ANSWERS IT ALREADY HAD (owner ruling OD-55,
2026-11-15: "we're not calling again"): the service attaches the daemon's
standing warnings (`Calls::warnings`, `Api::warnings`, the same list as
`Daemon.warnings`) to EVERY answer, a refusal's or failure's status too, as
gRPC response metadata, `daemon_proto::WARNING_METADATA`
(`ctxloom-warning-bin`, one value per warning, each a prost-encoded
`Notice`, so the client renders its code and args from its own catalog;
binary metadata, base64 on the wire), added by one wrapper around the whole
generated server (`daemon_api_grpc::Warned`, inside `Gated`, so a caller
that is not the user is told none; read once the answer is made, so a
drain's answer carries its hand-over). The command's client hears each
answer's (`daemon_client::DaemonClient::hearing`, `warnings_in`; the
wiring's `hearing_client_of`, every `Reach` but `daemon status`'s,
`Reach::showing_warnings`, which shows them in its answer instead) and
`daemon::heard` keeps each once a process, however many answers carried it,
for the command's end. No `GetDaemon` is asked for them. The coordinator's
answers (`Handle`, `HandleSync`) carry none (its bearer check is inside it;
every command reaching the daemon makes a service call too). Under
`test-support` the daemon's log says `served <path>` for each service call
(`daemon_api_grpc::Told`, serve.rs's `api_served`), which a test counts
calls by. While it
stands, each round first tries the whole start-up again
(`started_up_again`); the first that succeeds CLEARS it (the folds resume
from their positions, so a later whole catch-up leaves nothing missing)
and the log says `the start-up catch-up, tried again, succeeded`. Under
`test-support` the switch file `~/.ctxloom/daemon/test-start-up-fails`
fails every start-up attempt while it is there, its line the why (never
taken; a test removes it). Doctor has no daemon check, so it says
nothing of it. Then `tick` catches up the read models,
plans each project on the board (`session-policies`), and takes the steps
(launch, start, feed, clear, end), recording what each child said as its
run's facts. Its ports: the engine registry (`HostedEngine`, an `Engine`
beside its `ChildEngine`, resolved by NAME; the wiring's `hosted_engines(wiring, program)`
composes claude and opencode, and nothing in the launcher names an adapter),
`ChildPlans` (the child's project's dry run on its own engine, and her git identity there), `HandOffs`,
`ChildHomes` (its session directories, and its session's lock),
`ChildWorkspaces` (`Daemon::placing`: the project's worktree-git
workspaces), `Ears` (the root pumps each child's observations off a thread:
no thread in the core), `RunLocks`, `TokenSource` and the edge.
`ctxloom-wiring/src/daemon/serve.rs` only composes it, with the single-instance lock,
the runtime, the store and its core, the listener, the discovery file and
the pace. The start's sweep runs BEFORE the daemon is advertised (its
discovery file published), so the first command that finds a new daemon
finds the record whole (T18).

SWEEP, RE-HOST AND T21 (slice 4 U6; T18, T19, S18, S47, OD-19). The start's
sweep releases a lost run's checkout WIP-safe (`LostCheckouts`: only one
under ctxloom's own `<home>/.ctxloom/worktrees/`, never the project; the
port's never-forced `remove`, worktree-git's, never a prune; one holding
work is left and said in the log). A CHILD whose last incarnation was lost
(the board's `last_end` is `orphaned`) or handed over by a draining daemon
(`drained`, `session_policies::ENDED_DRAINED`; DRAIN below: `daemon stop
--drain`, `ctxloom shutdown`, a SIGTERM), not closed or owed another,
with a native session bound, is RE-HOSTED by the plan's step
`rehost:<child>:<n+1>` (`daemon-application`'s `rehost.rs`): the sessions
command `RehostSession{recent_rehosts, cause}` owes `IncarnationOwed{n+1,
rehost}`, its evidence the daemon's count of the session's own `rehost`
owes recorded in the hour before the loss (the last `IncarnationEnded`'s
recorded instant: decide reads no clock), refused
`sessions-rehost-budget-spent` (`RehostBudgetSpent`) at
`sessions::REHOST_BUDGET` (60); then the child stays ended (its run
runner-lost) and its owner is told, `notice:<child>:<n>:rehost-budget`. A
drain's hand-over is `cause: drain` (s4-Q5, `Step::Rehost.cause`), never
counted nor refused for the budget. The re-hosted incarnation is the
plan's ordinary start (`Step::Start.resume`, the session's bound native id
from its second incarnation on: claude `--resume`), its environment read
again from its hand-off (T6) and its settings written again from the
user's config (T21). Never a one-shot (S8(a)) and never a primary. A hosted
child FOLLOWS ITS AGENT'S `workspace` (owner s4-Q11): its axis is planned
at its launch and recorded at its mint (`MintSession.workspace`), a re-host
re-enters the axis its session was minted on, its checkout is
`isolation_ports::session_worktrees` under ctxloom's home for its agent,
placed by the one launch, `RecordRunnerStarted.workspace` is where it works, and
at its exit the checkout is let go WIP-safe (the files the launch placed
taken out, then the port's `remove`; one holding work left and said with
its branch). Under `test-support` a daemon is killed outright at a step by
the switch file `~/.ctxloom/daemon/test-die-at` (its word: `enqueued`,
`minted`, `started`, `turn`, `hook`, `discovery`; taken by the daemon that
finds it; `hook` is any capture fact's, `RecordPromptSubmitted`,
`RecordAssistantText`, `RecordToolCall`, `RecordTurnEnded`,
`RecordCompaction` or `RecordConversationCleared`, the daemon's
`serve::step_of` table, never a bind): the core's edge tells each book it applied
(`EmbeddedEdge::telling`) and the daemon aborts once one of the step's is
applied, before it is answered; `turn` is also taken by the loop while a
hosted child's turn is in flight (`Daemon::in_flight`), and `discovery`
while its discovery file is written. `ctxloom test record` prints every
stream of the one store, and `ctxloom test seed-rehosts <harp> <n>` plants
a child's past re-hosts.

RESTART WITH RESUME (step 10e Stage 2a; owner rulings 2026-11-20). The
SESSION AGGREGATE decides when a restart is owed; nothing else does. The
command `RequestRestart{reason, note}` (reason `operator | context |
config`, the additive `RestartReason`; only `operator` is emitted so far,
by `ctxloom session restart <harp> [--note]`, `Caller::restart`, a command
through the coordinator like `session stop`, keyed
`restart:<session>:<incarnation>:operator`; a terminal session is refused
client-side, `daemon-api-primary-not-hosted`: its engine is its `ctxloom
run`'s) records `RestartOwed{reason, incarnation, note}` for the OPEN
incarnation, once: a request while one is owed records nothing, whatever
its reason; refused `sessions-no-session`, `sessions-session-closed`,
`sessions-no-run-to-restart` (`NoRunToRestart`) and
`sessions-no-native-session`. `SessionState.restart_owed` (field 31) folds
it and the next `IncarnationOwed` closes it, so a restart owed is
recognisable from the stream alone, after any crash (an owed restart with no
later incarnation), never from memory. Context and config DECIDING, the
deletion of the hook re-injection and primaries are not built (S2b, S2c).
The HOSTING STATE is `daemon-board`'s `hosting.rs`, the board itself (its
session and run fold, an ephemeral projector subscription's: folded from 0
by every process, proved on the Bus in `bus.test.rs`), not a second
projector: per agent `Agent::Idle | Active{turn} | Parked{turn, reason} |
Restarting{reason}`, DERIVED, never stored. Active is a turn queued on the
open incarnation's run and not ended (`RunState.turns`, the run stream's
`TurnStarted/Completed/Failed` facts); the engine's end (`RunEnded`) forces
Idle, so a turn end a dropped hook never delivered cannot park a restart
for ever. PARK is that state (a restart owed while a turn is in flight), a
deferral: no handler blocks, and another agent's launch or message is
never held up. While parked the plan lets a steer through to the turn in
flight and holds a wake and an inform (`session_policies::plan`'s `runs`):
they are delivered into the resumed incarnation. At the Active to Idle edge
`Daemon::restart_the_due` (each tick, `Board::restarts_due`: restarting,
its incarnation open, its run live; only an engine this daemon hosts) ends
the engine once (`ChildControl::end`); its exit ends the incarnation
`restarted` (`ENDED_RESTARTED`, no notice to the owner) and the plan's
re-host owes the next incarnation for the cause `restart`
(`sessions::RESTARTED`, `RehostSession`, never counted against the
re-host budget), however the incarnation ended: an engine that exited on
its own with a restart owed is restarted all the same. The tick-driven
`plan()` is kept for every start (smallest change: the hosting state is a
read of the board it already catches up, and the effect is the hand-over's
`ChildControl::end` path). A draining daemon restarts nothing: it hands the
child over, and the restart owed is still in the stream for the next daemon.

The daemon has ONE principal, the user (slice 2). At each start it mints
the user bearer (T12: 256 bits, `credentials`' `OsTokenSource`) and a new
certificate, and serves its listener behind `runs-application`'s
`UserGate`, which keeps the bearer's SHA-256 alone, in memory, compares in
constant time (`TokenHash::matches`), and answers anything else (no
credential, another bearer, a run's composite bearer, another scheme) with a
bare UNAUTHENTICATED that says nothing of why (S11); an admitted caller is
the user, with `user_allowlist` (every session command and fact, and a
run's `RequestStop`) on every stream the daemon reaches. There is ONE daemon per OS USER, ever (owner,
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
other-home` (exit 48, naming both homes, its fix the daemon's home or a
`daemon stop` from it), and never served by a second daemon (`daemon
status`, `daemon stop`, `daemon resolve` and the child verbs; a hook's rediscovery passes
such a daemon over and holds its facts). The file is 0600
and its directory 0700, tightened on each publish whoever made it (the
single-instance lock may have); on Windows the platform sets no mode and the profile's
inherited ACL stands (M8). It keeps other users out, not the user's own
processes. `Discovery`'s `Debug` withholds the bearer, and the daemon's log
never holds it. Addressing at the daemon (owner, after step 3/4): the
target session travels ONCE, as the command's Cover root, and a command's
`requester` is its own field, set by the client (a session's MCP tools
and its hooks from `CTXLOOM_SESSION`; the user's child verbs name none,
D25) and taken as sent. No run is sent to the
daemon, and the daemon does NOT check a fact's session against a run: the
session aggregate records nothing on a session that is not live. Accepted
cost: a buggy client could write to the wrong live session unnoticed (the
per-run listener's `HookGate` header check is gone with it, slice 4 U7). A
hosted child is told the user's home as a value
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
finds her settings: a launch's hand-off keeps its caller's
`daemon::SETTINGS_PLACES` (`CLAUDE_CONFIG_DIR`, `XDG_CONFIG_HOME`,
`XDG_DATA_HOME`, `XDG_STATE_HOME`) beside the child's environment
(`daemon_application::HandOff`'s `launcher`, a `@NAME=value` line each of
the hand-off file, format 3, which adds the `!agent=<agent>` line; format 2
reads as the default agent), never in it, and never the daemon's own
environment.

THE CHILD VERBS AND THE HAND-OFF (slice 4 U3: S2, S19, T6, S10; owner
rulings s4-Q1, s4-Q13). The user's `session launch <owner> [--name]
[--agent] [--engine] ["<task>"]`, `session send <harp> {inform|wake|steer}
<text> [--id]`, `session clear <harp>` and `session close <harp>`
(`session-cli`'s `is_child`/`child`) are clients of the daemon, as the
USER: `daemon_api::Caller::user` over the project the command resolved
(`daemon::child_caller`; outside a project `daemon-api-project-unknown`,
exit 48), each the session domain's command through `HandleSync`,
`requester` none, keyed by an id minted once per call (`--id` is a
message's), so its retry on a new daemon is a DUPLICATE; parsed before any
daemon is reached (S14: a usage error starts none; the urgency is a
required word). `--format json` is the target's `Session` (a launch's, the
child's, as launched until its daemon mints it); text says `<harp>: <id>
recorded` (`already recorded` for a duplicate), `closed` (`already
closed`), `launched, a child of <owner>`. Their refusals are the domain's
words (`daemon_api::rejection_of` maps the sessions and runs domains'
typed refusals: `sessions-session-closed`, `sessions-session-unknown`,
`runs-no-credential`, ...), exit 48. A send's urgency is judged on the
target's engine (`session_policies::supported`, OD-58): a hosted child on
an engine with a child adapter, `session_policies::CHILD_ENGINES`
(claude-code and opencode; the wiring's `hosted_engines` is held to it by
its test), takes inform, wake and steer, and is woken by its children's
notices; any other session takes an inform alone
(`sessions-urgency-unsupported`). The hidden `daemon
launch|send|close|clear` are gone (hard migrate); `daemon resolve` stays
hidden. A launch (`Caller::launch`; the MCP tool `child_launch` too, its
owner the calling session, its harp minted when it names none,
`Caller::naming`) holds the child's LAUNCH LOCK (`Caller::holding`: the run
lock `launch-<child>`, `daemon_api::launch_lock_name`, under
`<home>/.ctxloom/runs/`, the platform's file lock taken at once or not at
all, so a second launch of one child meanwhile is refused
`daemon-api-launch-in-progress`) from before its `CreateHandOff` to after
its `LaunchChild`; its `LaunchChild` (keyed `launch:<owner>:<child>`, the delegation limit
`daemon_api::DELEGATION_LIMIT`, 16), and a refused `LaunchChild` is followed
by `DeleteHandOff` (kept while an open session holds it). The child's
engine is the one asked, else its AGENT's configured engine (an agent naming
none, the config's default-engine rule): the daemon's plan resolves it, and
`HandOff.engine` (field 2, additive) names it back for the `LaunchChild`. The daemon serves
`CreateHandOff{parent, hand_off_id, owner, engine, agent, env}`
(`daemon-api`'s `hand_off.rs`, the root's `daemon::files::ApiHandOffs`)
checking BEFORE it writes anything, so a refused launch leaves nothing: the
owner in the warm index (else the sessions domain's `SessionUnknown`,
"there is no session named ..."), the child's plan (its project's dry run
for the agent on the engine, its agent's when none is named, in the
launch's environment; one that cannot be made is `ConfigUnreadable`), and
its credential as its enqueue will judge it (the runs aggregate's own
checks, `runs::pre_launch_refusal`, refused with the typed refusal the
enqueue would answer, under its message's status); then the hand-off is written MARKED IN LAUNCH (`<id>.launching` beside it,
written first, so no launch's hand-off is ever there unmarked) and 0600 (in the 0700 `~/.ctxloom/daemon/launch/`, named by the child's
stable id; what the child may be given of the environment is
`engine_claude::launcher_environment`'s). The hand-off lives as long as the
child is open (T6): READ at each of its starts (`HandOffs::read`, a start
after a daemon restart included; there is no `take`), planned for the agent
it names, deleted at the child's close (`Step::End`), and swept at each
tick once the board has caught up (`Daemon::sweep_hand_offs`, owner's ruling
on U3: tied to the launch, no timer and no pid): a file whose session the
board holds closed is deleted; one no session knows of (neither a session
nor an owner's child) is deleted at once unless it is marked and its
creator still holds the launch lock (`Kept::creator_holds`, a probe of the
lock: a crash mid-launch leaves a mark whose lock reads free, so it is
cleared); a marked one a `LaunchChild` recorded (its owner holds the child)
is CLAIMED, its mark cleared (`HandOffs::claim`); an open child's is kept.
Accepted cost (T6): an API key lies at rest for the child's life. Not yet:
a hosted child's workspace axis (s4-Q11), the root session's persisted
launcher places for `child_launch` (s4-Q1; a session's call sends its
engine's environment, whose settings places are its session home's). A
running interactive session closed stops its TUI (U5, above). A hosted child's session home is prepared with its agent's
permissions document for the engine, verbatim (rulings 133-137,
`run_application::HostedChild::permissions`). A hosted child is made by the
one launch (`launch_of`, T10) like every other: its permits applied (the
`HOME` and `PATH` the daemon sets owned), its program found, its command
measured (it carries no prompt: its turns are fed over the child's
stream).

A daemon is LIVE when it answers the standard `grpc.health.v1.Health`
Check SERVING (T16, M11: `hook-sink-grpc`'s `serving`, tonic-health's
client and messages, a direct dependency at the version the lock already
held for angzarr's server, over TLS pinning the certificate the discovery
file names, presenting no bearer, within the wiring's `HEALTH_WAIT`, one
second). The single-instance lock (`daemon.lock`,
`DaemonFiles::take_instance`, the platform's `file_lock::take_current`;
`Unheld::Held` when another held it for the wait, `Unheld::Failed` when it
cannot be taken at all, its directory or file not made) says
only that one daemon serves: its file is made once and never removed or
replaced by ctxloom, and a serving daemon checks at each tick that the file
at the lock's path is still the one it locked (`DaemonFiles::still_holds`:
`Fs::same_file`, device and inode, volume and file index); removed or
replaced, it excludes nobody, so the daemon exits (closing the gap of two
daemons at once). `daemon status`, `daemon stop`, a hook's rediscovery and a
client's `ensure` all ask the health check, never the lock, so a daemon that
holds the lock and does not answer (stopped, hung) is not serving, and a
stale port another process took is never handed anything. A serving daemon
looks at its discovery file each tick (`serve::why_it_exits`): gone, it
exits and does NOT advertise itself again (owner ruling 2026-11-17; the
accepted cost: no self-heal of a hand-deleted discovery file, the next
client starts a fresh daemon); naming another daemon (its lock file was
replaced, and another took it), it exits, and withdraws only its OWN
advertisement (`DaemonFiles::withdraw_own`), never the other's. Once started,
a daemon never creates or re-creates a file in its home (owner, 2026-11-14
and 2026-11-17; j000100's exit is deleting `~/.ctxloom`, which `rm -rf`
empties depth-first while the directory is still there): its log is opened
once as it starts (`logger`, its directory made first) and every line is
written through that handle, never reopened by its path, and the discovery
file is published once; so a daemon whose home was deleted exits and leaves
nothing there. It also exits, each tick, once its home's ctxloom
directory (`<home>/.ctxloom`, its store's) is gone (`DaemonFiles::home_gone`,
OD-60), because the lock may be elsewhere: the account's, while
`CTXLOOM_HOME` names another home (`just install`'s `~/.ctxloom-rs`), or a
test's own place. A call that would create files under its home
(`CreateRun`, `CreateHandOff`: hand-offs and their `.launching` marks, run
locks, a session's directory and checkout) passes one gate first,
daemon-api's `Api::creating` (owner ruling 2026-11-17): once the home is gone
(`Hosting::home_gone`, the wiring's `Shared` asking `DaemonFiles::gone_home`)
it is refused `ctxloom.v1.DaemonHomeGone` (FAILED_PRECONDITION,
`daemon-api-home-gone`, its fix: run the command again), nothing planned or
written, and the loop exits on its next round on the same look; a launch the
loop makes itself (a `LaunchChild`'s) follows the round's own look
(`why_it_exits`). Accepted residual: a call already past the gate, or a
round already past its look, while the home is being removed can still make
a file there. A replaced lock (its directory still there) is
logged as ever. A client with no live
daemon spawns `ctxloom daemon serve` and waits for a live one to advertise
itself (the one spawned or another client's, whichever took the single-instance lock)
within `SPAWN_WAIT`, eight seconds: S16's ten (`HOOK_BOUND`) less a
delivery's two (`DELIVERY_MARGIN`). It watches the daemon it spawned: once
that one has exited, the wait goes on only when it declined because another
daemon holds the single-instance lock (`daemon serve` exits 48,
`ctxloom-wiring-daemon-instance-held`, `spawn_waits_on`), and any other exit
(it could not start) ends the wait at once; it used to wait out the eight
seconds for a daemon that had died (owner ruling 2026-11-18, the daemon
cluster's cap). A stale file is left for the new daemon
to publish over. The spawned daemon's environment is `daemon_env`'s:
`PATH`, `TMPDIR`, `LANG`, `LC_ALL` and `run_ports::WINDOWS_STARTUP_VARIABLES`,
matched as the platform compares names (`os::Flavor::env_key`, so Windows'
`Path` passes), and the spawner's `CTXLOOM_HOME` only when it set one;
NEVER the spawner's `HOME`, `USERPROFILE`, `APPDATA`, `LOCALAPPDATA` or XDG
bases (its confinement: the daemon's home is the account's), nor anything
else of the spawner's. It is started DETACHED (`os::process::spawn_detached` of an
`os::process::Detach`, its stdio null: `setsid` on unix, so it leads a
session of its own with no controlling terminal, and every descriptor above
stderr close-on-exec; `DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP`,
breaking away from a job, on Windows, with a `PROC_THREAD_ATTRIBUTE_HANDLE_LIST`
naming its three standard handles alone), so no hangup, Ctrl-C or kill of
the client's group reaches it, and it holds no handle of the client's (a
test's output pipes end when the client exits, not when the daemon idles
out); proved on Linux through procfs (its session id is its pid, and
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
`Hosting` port, the wiring's `Shared`, told at each round, except its
one-shots, read live from `OneShots` at each call (askew-glitter: a one-shot
ends on its own thread, its client told its reply in the same change, so a
`StopDaemon` asked right after its reply never counts it); the old stop
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
the SAME on its one retry on a new daemon). Nothing of a destroy is kept
in memory (O2: `destroy::Applied` is gone): a retry of a removal still open
resumes it; a retried `DeleteSession` whose session the store says is
destroyed (its name claimed by it, its stream empty) is answered applied
from those facts, nothing counted (AIP-155's first response is not
reproduced: its counts are gone with the session; accepted cost: a fresh
`session remove --yes` of an already-destroyed session, which carries a
new id, is answered so too, not `SessionNotFound`), and a retried sweep
runs again over what is left (each of its removals intended once). A
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
a refusal 48, a failed call 32), and its `Client` (slice 2 step 6), what
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
10, naming its log as `~/.ctxloom/daemon/daemon.log` under the home), or,
when the one it spawned exited first, `Unreached::Exited`,
`daemon-ports-exited` (32: its exit status and the last lines of that log,
where the spawned daemon's stderr goes, so a stack overflow says itself;
CI on Windows, 2026-11-19). `daemon serve` serves on a thread of its own
with a 16 MiB stack (the wiring's `DAEMON_STACK`; Windows gives the main
thread 1 MiB; and every daemon runtime thread, the blocking pool's too,
has it: `daemon::runtime::build`, with at least four workers and one per
CPU), and logs every panic of its process as `the daemon
panicked: ...`; under test-support `CTXLOOM_TEST_DAEMON_START=panic|
stack:<MiB>` makes its start panic or use that much stack;
another home's or a busy other version's is `Unreached::Declined` (48).
Each call resolves its project first (`ResolveProject`, S6; outside a
project a read of its own is `ProjectUnknown`; `--all` lists `projects/-`),
sends its `Caller` (below) in every request made on the user's behalf
(S7; a read by name carries none), and a call whose daemon could not be reached
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
their previews and `--detailed-exitcode` 17 as before, their json the
answers' proto3 JSON (`DeleteSessionResponse`'s, `SweepSessionsResponse`'s), an
age (`--purge-older-than`, or `session remove --older-than`) that is none
refused before the daemon is asked, naming the flag given;
the write's request id (S8) is not sent yet, so a retry on a new daemon of
a removal that applied answers `SessionNotFound`. Not yet clients: the
library, config, deps, init, doctor and run.

VERSION SKEW (M3; owner OD-61/OD-62, 2026-11-15: ONE ctxloom version runs
at a time, and nothing drains a daemon for its version): a client whose
stamp is not the serving daemon's asks it (`GetDaemon`); hosting nothing,
it is stopped (a plain `StopDaemon`), waited out and the client starts its
own; hosting live runs, the command is refused, `daemon-api-version-skew`
(`SkewError::Mismatch`, exit 48, naming both stamps and the harps, fix
`ctxloom daemon stop`, `--even-if-live` ending what it hosts), whatever the
two versions. Nothing passes one version's work to another's: a newer
client's automatic drain, H-Q6's hook nudge, the successor version
(`StopDaemonRequest.version`, field 4, `Daemon.draining_for`, field 10, and
`DaemonDraining.successor`, field 2, all reserved), `daemon-api-version-skew-busy`
and `daemon-api-draining` on this path are gone; a hook held for an older
daemon only holds. An
install stops every running ctxloom first (SHUTDOWN, below). `daemon resolve` reconciles
first, and the child verbs as every client reaching through `Reach` does;
under `test-support`, `CTXLOOM_TEST_STAMP` names the client's stamp.

DRAIN (OD-54; owner 2026-11-15). A daemon drains when it is asked: `ctxloom
daemon stop --drain` (`Client::drain`, `StopDaemon{drain: true}`),
`ctxloom shutdown [<duration>]`
(SHUTDOWN, below; the same request with `drain_timeout_seconds`, field 5)
or a SIGTERM to the daemon (serve.rs's `hear_terminate`: the first is the
drain, logged `SIGTERM: asked to drain`; a second is the
plain stop, `--even-if-live`, ruling 114's stop of each engine). A drain is
never refused for what the daemon hosts; `even_if_live` wins over it. Its
TIMEOUT is the first that says: the request's `drain_timeout_seconds`, the
home config's `daemon.drain_timeout` (a span of the one grammar,
rust-support's `duration-text`, whose `now` is zero; home layer only, `HOME_ONLY`), READ AGAIN AT EACH
ROUND (`Shared::drain_period`, `Daemon::hand_over_within`; logged `the drain
waits <n>s from its start`), so a change written while it drains (the
config is no client of the daemon, so nothing refuses the write) is the
drain's, still timed from its start; else the daemon's default,
`daemon_application::HAND_OVER_PERIOD` (five minutes; `daemon serve
--hand-over-seconds`, hidden, sets that default, a test's short one).
Hosting nothing, the daemon exits at once, unless a live frontend of its
home remains (owner ruling 2026-11-16, PREVENT the early drain exit; the
wiring's `serve::stays_for`, `Staying`): a draining daemon then STAYS,
logged `draining: hosting nothing, it stays for the live frontends pid
<n>`, so a frontend's last reports (its TUI's exit) always reach a
daemon and are never held for want of one; it exits once no frontend is
live (a frontend gone without reporting reads free by its lock, so it
holds nothing), or once the drain's time and `FRONTENDS_GRACE` (30 s:
the frontends asked to stop then, by `ctxloom shutdown` or by their
notice's `stop_at`, stop their TUI and report) have passed, said `after
the drain's <n>s and 30s more, the frontends ... still run: exiting`. A
plain stop (`--even-if-live`, the second SIGTERM) stays for none. `--drain` with `--even-if-live` is a usage error (clap's
`conflicts_with`, exit 48); nothing new under `--detailed-exitcode`. The
drain WARNS AT ONCE (OD-54 (2)): while a daemon drains and hosts
anything, its `Daemon.warnings` (StopDaemon's answer and GetDaemon's)
carry `daemon-api-handing-over` (`daemon_api::HandingOver`: its stamp, the
harps, the period in seconds, `Hosting::hand_over_seconds`, the timeout as
the loop reads it now; its successor version gone, OD-62, so its wording
is a DRAFT again), and it rides every
answer's metadata as the start-up's does (OD-55): the asking command hears
it from the stop's answer, a later call's `DaemonDraining` refusal carries
it too, and each says it on stderr as it ends, once; its log says each
busy child as it is steered. The draining daemon
(`Hosting::drain`/`draining`, `Daemon.draining`, field 12, said by
`daemon status`) refuses every new call but `GetDaemon`, `StopDaemon` and the
project's names (`ResolveProject`, `GetProject`: a command that only names
its project, as a config change's json does, starts no work and lands) with
the typed `DaemonDraining` (`daemon-api-draining`, naming its stamp and
what it hosts, wording a DRAFT; `Api::open`): new runs and launches
(`CreateRun`, `CreateHandOff`), the session reads and destroys, the
transcript and the assembly; the hooks' facts and the frontends' reports
(the coordinator, not the service) still land, and so do the calls on work
a client already started, which start none (OD-62 refuses NEW
invocations): `WatchRun` (a one-shot's client watches its run only once
`CreateRun` answered, so a drain asked in between refused it, exit 48, where
the period's stop is 143; a frontend's watch of its run likewise; an
unknown run is `RunNotFound`), `DeleteHandOff` (the withdrawal of a
hand-off whose launch the drain refused), and a one-shot's `RequestStop`
(the coordinator's `Handle`). Its loop hands over(`Daemon::hand_over`): it starts no child and delivers no message
(`Step::delivers`; what is pending is the next daemon's), but the guidance
below; each hosted child bound to its native session with no turn in
flight (and no guidance of its own still pending) is ended once; its exit
ends its incarnation `drained`, its hand-off KEPT; one-shots finish their
turn, and one still running once the hand-over's period (below) has passed
is stopped as `daemon stop --even-if-live` stops one (SIGTERM: its caller
exits 143, `run-application-stopped` naming it), said in the log
(`<harp>: a one-shot still running after <n>s: its engine stopped
(SIGTERM)`; owner, 2026-11-15, U8c); it exits once it hosts nothing. A BUSY child (a turn in flight, at
the hand-over's first round) is STEERED (OD-54 (1)) with the approved
guidance, `session_policies::DRAIN_GUIDANCE` ("ctxloom is being upgraded:
finish the step you are on and end your turn; you will be resumed in this
conversation."), by the one steer path: the session's own `SendMessage`
(`session_policies::drain_guidance`, a steer keyed and identified
`drain:<child>:<incarnation>`), delivered by the plan's delivery (the one
delivery a draining daemon lets through, to that run while the guidance is
pending) and fed as any steer (the turn in flight interrupted, the guidance
its next turn); the child is ended once that turn ends. A child still
hosted once the hand-over's period has passed on the daemon's clock
(its timeout, above; `Daemon::clocked` over the `Monotonic` port, the
root's its process's `Instant`, a test's a fake) is
handed over AUTOMATICALLY (OD-54 (2)): ended once, its turn in flight cut,
its incarnation `drained`, its hand-off kept, and said in the log
(`<harp>: still busy after <n>s: its turn in flight cut, handed over`); no
kill is offered. The next daemon re-hosts each such child, cause `drain`,
resuming its conversation. TUIs are untouched. Accepted residuals: a child
never bound to its engine's session is ended at the period too and,
having no conversation, is not re-hosted; a guidance still pending when
the period cuts its child (never delivered) is delivered by the next
daemon, as a steer to the re-hosted child. `daemon stop --even-if-live` is
the escalation (its children end `runner-exit` and are not re-hosted).
TUIs are not handed over: a frontend outlives its daemon, and the daemon
after the drained one adopts it (U5), or `ctxloom shutdown` asks it to stop;
a draining daemon stays while one is live (above), so it takes its last
reports.

SHUTDOWN (owner, 2026-11-15, OD-62; `ctxloom-wiring`'s `daemon/shutdown.rs`).
`ctxloom shutdown [<duration> | now]` stops every running ctxloom of this
home, as an installer must before it replaces the binary. Its time is a
span of the one grammar (rust-support's `duration-text`, read directly:
`15m`, `1h30m`, `90s`, `0s`, and its word `now`, zero);
none given, `daemon.drain_timeout`, else five minutes; one that is no span
is refused before anything is asked, `ctxloom-wiring-shutdown-bad-time`
(exit 48). A time that is not zero DRAINS the daemon serving within it
(the drain's request, its timeout this one), then waits, at most that long,
for the daemon to exit AND the live frontends to end (a TUI is left to its
human for the whole time), and for the daemon a grace longer
(`DRAIN_GRACE`, 30 s: its period's end stops its one-shots and cuts its
busy children) when no frontend is live (one that is keeps the daemon, so
the frontends are asked to stop at once). Then, `now` at once: each live interactive frontend is
asked to stop and waited for (`FRONTENDS_WAIT`, 20 s), then the daemon
serving then (a frontend's report may have started one) the plain stop
(`--even-if-live`) and waited for (`STOP_WAIT`, 30 s). Each step is said
as it happens (text; json: `{drain_seconds, steps, held}` at the end). Nothing is
killed: what still runs is said, `ctxloom-wiring-shutdown-incomplete`
(exit 32, the daemon's and the frontends' pids); all stopped, exit 0.
FLUSH BEFORE INSTALL (owner ruling 2026-11-16; N1 renames run facts' types,
and a hold stores each fact by type URL): shutdown delivers the home's
holds through the daemon serving (`daemon::flush::flush_within`,
`FLUSH_WAIT` 30 s; never a daemon started for it, so an old hold never
meets a newer daemon) at its start, once the frontends of a drain have
ended, and before the plain stop, saying what it delivered (and what it
set aside, H3's notice). Then it judges the holds
(`shutdown::undelivered_in`, `runs_application::undelivered` over the
spool's survey): EMPTY when no hold has a live record, a fact or a gap a
flush would deliver, or is in a format this ctxloom cannot read; a hold's
set-aside part is never delivered again, holds nothing a daemon will
decode, and does not count (doctor reports it). Not empty is said, each
session's harp and count (`held, undelivered: <harp> (3 facts); ...`;
else `held: nothing undelivered`), and declines the command,
`ctxloom-wiring-shutdown-held` (exit 48: the state forbids reporting
done; it outranks what still runs, 10), its fix running the old binary's
`ctxloom session list` (its daemon takes them on its way in) and
`ctxloom doctor`; the wording is a DRAFT. No alias table; the accepted
cost: a stuck hold blocks an install. A
frontend is found by its lock (`liveness::FrontendLocks`): the frontend of
every interactive session (`frontend::client_terminal`) holds
`<home>/.ctxloom/frontends/<pid>.lock` for its life, and is asked to stop
by the file `<pid>.stop` beside it, which it looks for every 200 ms
(`watch_for_stop`): it then requests its `Stop` (the TUI asked to stop, then
killed after its grace, as a SIGTERM to it does, ruling 114), reports its
end and exits, said `asked to stop by `ctxloom shutdown``. A drain TELLS
each live frontend at once (OD-63; its spec, `features/cli/shutdown.feature`,
owner-approved 2026-11-16): the file `<pid>.notice`
beside its lock (`liveness::FrontendNotice`: a `stop_at: <epoch seconds>|none`
line, then the text; a later one replaces it), which the frontend shows on
its terminal's stderr as `ctxloom: <text>` once per notice (`Watch::look`).
`ctxloom shutdown <span>` tells it with no `stop_at` (it asks the stop
itself at the span's end); a SIGTERM's drain tells it with `stop_at` its
start plus the timeout, told again when the timeout changes while the
daemon lives, and the frontend stops itself then (said `the drain ctxloom
was told of ended: ...`), and reports its end to the daemon, which
stayed for it. The notice text (`shutdown::notice_text`, "ctxloom is shutting down:
this session will be stopped in <span>; finish and exit") and both stop
sentences are DRAFTS (OD-63). Under `test-support`, the daemon directory's
`test-deaf-to-stop` makes a daemon take a stop and a drain and ignore them,
and `test-shutdown-stop-wait` (seconds) shortens `STOP_WAIT`, so a test
sees the exit 32. Its `test-hold-alarms` (one line, seconds since the
epoch) holds the scheduler's clock at that instant while it is there
(`DaemonFiles::alarms_held_at`, serve's `HeldAlarms`), so no alarm armed
since comes due until the test removes it: session_approvals.feature's
asks with `approval_timeout: 2s` are held from before the ask until "the
ask's time runs out", never ended early by a slow machine (the race rule). A dead
frontend's lock reads free and its files are taken away by the next look;
no process id is trusted and no signal is sent to one (Windows alike). The
shutdown reaches the daemon without the version check (`Reach::observing`),
so a new binary stops an older daemon; an older daemon ignores the timeout
field and drains within its own. `just install [when]` (and
`install-windows [when]`) runs the NEW binary's `shutdown` before a
changed binary replaces the installed one (`just install now`: at once),
through `scripts/install-shutdown.sh` (its self-test in `just selftest`):
what will not stop (any failure, 10) is said and the install goes on; a
DECLINE (20-29: holds left undelivered, or a time that is no span) is
`FAIL install: ...` and the installed binary is not replaced. A daemon's exit lets its
runtime go within `RUNTIME_END` (5 s, `Runtime::shutdown_timeout`), never
waited on for good: a live frontend's session keeps a blocking task (its
`WatchRun`) that a plain drop waited out for the frontend's life, every
SIGTERM meanwhile taken by tokio's handler and heard by nothing (2026-11-15,
ruling 114). A frontend's watch for its stop and notices runs beside its
session in the main thread's future (`frontend::beside`), never spawned:
that thread holds std's stderr lock for the whole command (the binary's
`main`), so a notice written from the runtime's one worker hung it, and
the signals and the stop with it.
A frontend also tells its human of the permission asks that arrive in its
project (owner ruling 2026-11-22, project-wide 2026-11-23;
`frontend::tell_of_asks`, the pure half in `frontend_asks`): every `ASK_LOOK`
(1 s) it asks the daemon serving, never starting one, for the waiting asks of
its whole project (`ListApprovals` with no root, the bare `session approvals`
listing: a one-shot's and any other session's asks too; each TUI of the project
tells every ask, the accepted cost; polled because no stream of asks exists),
and for each it has not told of rings the bell and
writes one stderr line through the same path as a drain's notice, in the same
main-thread future: who asks, what it asks to run (an option's rule, else
tool and input) and `ctxloom session approvals answer <ask> <options>`. Once
per ask; an ask no longer waiting is never told. The ask's words are another
agent's, so control characters in them are spaces. The line's words are a
DRAFT. No statusline count; answering stays in the CLI. Under `test-support`
the watch appends `look` and `told <ask>` lines to
`<home>/.ctxloom/test-frontend-asks`, which session_approvals.feature waits
on.

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
`ProjectIdentityMismatch`, an expected name not the registered harp
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

A RE-CREATED REPOSITORY IS RE-BOUND (owner ruling 2026-11-19, batch 3, over
the smoke fix that refused it): a repository deleted and made again at its
path (another root commit) has no `.ctxloom`, so `ctxloom init` there asks the
daemon unregistered and, once the daemon has named the new project, the
wiring's `bind_project` takes the home index's old binding of the root out
first (`rebind::unbind_stale`, under the registry's own `index.yaml.lock`)
and binds the root to the new id: no refusal, no hand edit. Accepted cost:
the old project's sessions and store rows stay under the old id, orphaned,
until cleaned. Still refused (`sessions-project-identity-changed`, exit 48):
any command run where the root HAS a `.ctxloom` (its `project-id`, else the
index's binding) naming a project whose registered root commit is not the
repository's, init's own preview included.

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
(`InProcessCore::start`, which also registers no policy and resumes
nothing at start); the daemon's does (`start_with`, `Projectors::
filled`). There is ONE fold per read model: `core/read-model/held-projection`'s
`Held` is a shared handle, the same instance the kernel runs inline and the
daemon's catch-up drives, each page folded once (one before where the fold
is on its stream is passed over; a book from 0 with another first event is
folded afresh; a book past where it is is a gap, read from the store first,
else left for the catch-up).

## Trust (step 8 U1, ruling 142; plan `approvals-trust-step8.plan.md` §0)

Step 8 U1 adds three crates: `core/wire/trust-proto` (from
`proto/ctxloom/trust/v1/trust.proto`), `core/domain/trust` (the trust
aggregate) and `core/read-model/trust-index` (its projector). A project's
trust is `projects/{harp}/trust` (D2, S58), one stream per project, rooted on
the UUIDv5 of that name in `sessions::NAMESPACE` (`trust::trust_root`, as
every ctxloom root is; the proto draft's "URL namespace" wording is not
used). `GrantTrust` names the etag its preview showed and `current_etag`, the
etag of the settings now; the aggregate refuses `trust-plan-stale`
(`PlanStale`, FAILED_PRECONDITION) when they differ. Both etags and the
actor are the CLIENT's (D13: nothing stamps yet): the user's `Caller` asks
`GetTrust` again just before it sends, and the actor is her login (`USER`,
else `LOGNAME`, else `USERNAME`; the wiring's `sessions::user_login`).
Granting what is trusted, or revoking what is not, records nothing; a trust
lasts until `RevokeTrust` (S23). There is no remote trust (D5).

The daemon serves `GetTrust` (what trusting the project would allow, read at
its REGISTERED root through daemon-api's `Trusts` port, the wiring's
`ProjectTrusts` over `Library::trust_settings`: the project layer's
ruling-46 keys, from `strip_project_only`, which now returns each path with
its value, and a project `ctxloom` bundle as `bundles/ctxloom`; sorted by
path, and `daemon_api::etag_of` of them, a length-prefixed UUIDv5) and
`ListTrusts` (the projects trusted NOW, from the warm `trust-index`; a revoked
trust is not listed). Both refuse while the daemon drains, as every new call
does. The index is registered in the daemon's kernel (`Projectors::trusts`)
and caught up at the start and each round (daemon-application's
`Folds::trusts`). The trust domain is exposed on loopback alone, and the
coordinator reaches it (runs-application's `EdgeCommands::reach`), with
`GrantTrust` and `RevokeTrust` on the user allowlist. The coordinator
refuses a command naming its refusal's TYPE alone (its ErrorInfo's
`refusal_type`): daemon-client reads that back as a typed refusal of that
type with no fields, so `trust-plan-stale` reaches the CLI as a refusal
(exit 48).

`ctxloom trust [--yes] [--undo]` and `trust list` are session-cli's second
noun (`session_cli::trust`, a bare-runs group): it previews (`GetTrust`); at
a terminal (or the stood-in `CTXLOOM_TEST_TERMINAL`) it asks `[y/N]` on
stderr and only `y`/`yes` grants; off one it prints the preview and changes
nothing (exit 17 under `--detailed-exitcode` when the apply would change
something); `--yes` applies. The grant and revoke are `HandleSync` on
projector `trust-index`, keyed once per call. `trust-plan-stale`'s text is
accepted as drafted (owner, 2026-11-16: every text step 8 drafted is
accepted verbatim, for a full review before release).

Trust is ENFORCED (step 8 U2). The daemon reads it (`TrustIndex::trusts`, by
project id) for every launch it makes, a one-shot, a terminal session, a
hand-off and a hosted child's start, and plans it with
`library_application::Flags::trusted` (never the client's to say: no flag or
variable sets it). It gates EVERY library read the daemon serves too (step
10a U7, D7: daemon-api's `read_site`, the project's id from
`registered_project`, a read under `users/me` untrusted), so `engine show`
before `trust --yes` and after `--undo` holds them back, and between shows
them. Untrusted, `config::load` holds ruling 46's four keys back from the
project layer (`Config::held_back`), each said as the typed notice
`library-application-notice-untrusted-setting` (`Notice::UntrustedSetting`:
`path`, `file`, `home_file`; its `-fix` names `ctxloom trust` and the home
config), which replaced ruling 46's untyped "a project's config cannot set
it" warning (deleted, U7), and the plan names those THIS launch reads (`DryRun::untrusted`: its engine's
block and its agent's, owner Q8), which are `EnqueueRun.untrusted_settings`
(field 14): the runs aggregate refuses them LAST of its checks (Q9),
`runs-untrusted-project-settings` (`UntrustedProjectSettings{paths}`,
FAILED_PRECONDITION; `LaunchRefusals` field 7); the credential
evidence is read with them held back, so an untrusted `auth.file` is reported
once. Trusted, the project's own keys apply until the trust is revoked (S23).
An in-process library verb (a write, until 10b) is planned untrusted and
holds them back with the same notice; init's interview read is still read
in process and asks the daemon one `GetTrust` (`Library::with_trust`),
since no served read answers an operational prompt yet (distill's is the
daemon's own read since step 10b). A project's own
`ctxloom` bundle replaces an operational prompt (init's interview, distill's)
only once the project is trusted (D4): `Project::operational_prompt` passes it
over for the built-in otherwise, with the notice
`library-application-notice-prompt-untrusted`, which `init prompt` says with
its remedy. Where no flag says, the library asks `Library::with_trust` (the
wiring's `daemon::trust_ask`: ONE `GetTrust` of the user's daemon, which may
start one), and only when a project `ctxloom` bundle is there to decide on.

A child may work in ANOTHER project (step 8 Q1, D8): `session launch <owner>
--project <dir>` (the shell's working directory) and `child_launch`'s
`project` (relative to the session's `CTXLOOM_ROOT`, Q-C). The caller resolves
the directory (`Caller::resolving`, the wiring's `daemon::project_resolver`: a
command run there, its `CTXLOOM_ROOT` dropped; a directory in no ctxloom
project is `daemon-api-project-unknown`, exit 48) and registers it
(`ResolveProject`); the child is named in that project,
`CreateHandOffRequest.project` names it, and the daemon refuses an untrusted
one, `sessions-untrusted-target`, before anything is planned or written, else
plans the child at its root with its trust (`HandOff.project` answers it).
`LaunchChild` carries its project's id and root and `TargetTrust` from the
daemon's answers (`same_project`; `trusted`, GetTrust's, unread for the
owner's own project). Such a child is WHOLE across the two projects (step
8 U2b, owner ruling 2026-11-16). One board holds every project's sessions
(T14) and each project's plan plans its own, a step reaching across where
its owner or child is (session-policies' `plan`): the owner's plan launches
and CLOSES its child there (the cascade, `cascade:<owner>:<child>`, sent to
the child's project), the child's plan tells each turn it ends to its owner
(`notice:...`, in the owner's project) and ends it on its owner when it
closes (`child-ended:...`); its hosted run is ended, its engine exits and
its hand-off is removed, as within one project. Its outputs are its OWN,
as every session's (owner ruling 2026-11-16: one rule, its owner's
project or another): the plan mints it with no output folder
(session-policies' `launch`), and the daemon reads its project's
`output.dir` there (`ChildPlans::output_root`, the wiring's
`child_output_root`, with the env its launcher handed),
`<its root>/<output.dir>/<its harp>`, never its owner's folder; one that
cannot be read is a launch that failed (its owner told). The owner's side addresses it by
its full name (`projects/<its project>/sessions/<harp>`, the launch's
answer); a bare harp is the caller's project's. Its asks are the root's
project's (Q-A, U4).

## Approvals (step 8 U4, ruling 142; plan `approvals-trust-step8.plan.md` §0)

Step 8 U4 adds three crates: `core/wire/approvals-proto` (from
`proto/ctxloom/approvals/v1/approvals.proto`, compiled for the first time;
it keeps its own `SessionRef`, one package per wire crate), `core/domain/
approvals` (the approval and approval-names aggregates) and
`core/read-model/pending-approvals` (their projector). An ask is a session's
permission ask (a claude child's or one-shot's, routed through ctxloom's MCP
approval prompt): one stream per ask, rooted on UUIDv5(the asking session's
id, its key `approval:<run>:<request id>`, `approvals::ask_root`), so a
redelivered ask is one; its NAME is `ask-<adjective>-<noun>` (D9, Q7:
harp-names' two-word mint, `ask-` before it), claimed in `approval-names`
on the root of its resource name `projects/{project}/approvals/{ask}` under
its ROOT's project (Q-A). `RecordApprovalAsked` (a fact, the daemon's own)
records it once with its options; a timeout of 0 (`approval_timeout: now`,
D16) records it expired in the same decide. `RecordApprovalWithdrawn` (a
fact, `mcp serve`'s on claude's `notifications/cancelled`) withdraws a
waiting ask. `AnswerApproval` (the user's) names one of its options:
refused `approvals-approval-unknown`, `-approval-settled` (`answered
(<option>)`, `expired`, `withdrawn`), `-option-not-offered` (naming those
offered) or `-message-not-relayed` (a message with an ACP option, Q6). The
fact journals the approval's, the child's and the root's resource names and
the asking agent, so the projection is the `ctxloom.v1.Approval` resource.
claude's options are ctxloom's (`approvals::claude_options`): the rule is
the tool and its exact input, `Bash(<command>)` or `<Tool>(<path>)` for
Read, Write, Edit, MultiEdit (`file_path`) and NotebookEdit
(`notebook_path`), offering `allow-once`, `allow-session`, `allow-always`
and `deny`; any other tool offers `allow-once` and `deny` alone (Q5).
An ask ends by its alarm (U5; S26, D11, D12): `approvals::ArmsExpiry`
(the approvals' policy towards the scheduler) arms `approval-expiry:<the
ask's stream root>` at the instant `ApprovalAsked` was recorded plus its
`timeout_seconds` (`approvals::expiry_at`; an ask with no time, `now`, is
expired in its own decide and arms nothing), and `ApprovalAnswered` or
`ApprovalWithdrawn` cancels it; `approvals::DeliverToApprovals`
(`scheduler::delivery!`) hands the ask `RecordAlarmReached`, which the
aggregate re-checks against its own state (still waiting, its own alarm, the
instant it armed) before it records `ApprovalExpired`; the waiting tool then
answers `mcp-server-approval-expired`. Both policies are ONE entry
of `core_link::ALARM_CONSUMERS` (`approvals_expiry`). Only the daemon's
core resumes owed policy work at start (owner ruling 2026-11-16; below), so
the resume's cost, which grows faster than the sources' history (the
daemon's start over 1000 settled asks takes over a second; angzarr-
embedded's quadratic resume is being fixed upstream), is the daemon's
alone: a CLI command's core start is under a millisecond at any history.

The daemon serves `CreateApproval` (D10: the session's run, engine, agent
and `approval_timeout` are the child it hosts, daemon-application's `Asker`,
told the API each round through `Hosting::asker`, else the live one-shot it
hosts as it was placed (U6, D7: `run_application::Placement`'s `agent`,
`approval_timeout` and `interactive`, `OneShots::asker`; the human's
terminal session is no asker, S28); a session it does not
host is `daemon-api-session-not-hosted`; idempotent on (session, request
id); read back from the ask's own stream), `GetApproval` (unknown:
`approvals-approval-unknown`), `ListApprovals` (a project's asks, oldest
first, under a root, waiting) and `WaitApproval` (answered once settled,
read afresh each round; the gRPC edge awaits it, so a caller that hangs up
drops it; the client's deadline is an INSTANT, `daemon_ports::Until`, the
ask's recorded expiry plus a minute, `mcp_server`'s `wait_until`, so a wait
retried on a new daemon ends when the first would have). Every
approvals call is served while the daemon drains (D6). The kernel registers
both aggregates (approvals on loopback, the names not exposed) and the
pending approvals (`Projectors::approvals`, `Folds::approvals`); the
coordinator reaches the approvals domain; the user allowlist adds
`AnswerApproval` and `RecordApprovalWithdrawn`.

`ctxloom mcp serve` offers `approval_prompt` (`TOOL_NAMES` 9;
description `tools/approval_prompt.md`): `CreateApproval`, then
`WaitApproval`, answered in claude's verified shape (P0):
`{"behavior":"allow","updatedInput":<input>}`, with the session rule
(`updatedPermissions`, destination `session`) for allow-session and
allow-always, or `{"behavior":"deny","message"}` (the user's message, else
`mcp-server-approval-denied`, `-expired`, `-denied-at-once`). A
cancelled call is withdrawn with claude's reason and answered nothing (rmcp
drops it). claude's `server/discover` (a string id, before `initialize`)
is answered `{}` by the server's own reader of its input
(`mcp_server::pump`), before rmcp sees it.

`session approvals [<harp>]` lists this project's waiting asks (text form
texts.md §6; its words, claude's option labels and `trust`'s text
are session-cli's catalog, `session_cli::text::Text`, ruling after U4); `session approvals answer <ask|harp> <option>
["<message>"] [--yes]` names an ask by its name, or by its asking session's
harp while exactly one waits (`daemon-api-approval-ambiguous` naming each,
`daemon-api-approval-none-waiting`: client-side), and sends
`AnswerApproval` through `HandleSync` on `pending-approvals`; a refusal the
coordinator names by type alone is said with the ask as the daemon has it.
An `allow-always` of a claude ask writes its rule into the asking agent's
`agents.<a>.engines.<e>.permissions` `allow` (`Library::allow_always`)
WHERE IT TAKES EFFECT (owner ruling after U4): a project's agent replaces
the home's of the same name whole, so into the project's definition when
the project defines the agent, else the home's (defined by neither, the
project's); from that definition's own document for the engine, else the
effective one, which is then its engine's default (so `defaultMode` is
kept); the preview names the file. Q3's order: the ask checked
waiting, the rule written, then the ask answered; off a terminal without
`--yes` it previews (exit 17 under `--detailed-exitcode`), at one it asks
`[y/N]`; a rule written for an ask settled meanwhile stays,
`daemon-api-approval-rule-kept` (exit 48). A new session's chosen name may not begin `ask-`
(`sessions::validate_session_name`, `sessions-harp-invalid` reason
`reserved`, refused by the caller before anything is written); an existing
one keeps its name (Q-B).

Step 8 lands CLAUDE-ONLY (owner, 2026-11-16): an ask is routed only from a
claude child or one-shot (its prompt tool, above). opencode's routing (U7:
ACP `session/request_permission` relayed to the approvals, its one-shots
over ACP, `opencode run` deleted) is parked on `daemon/s8-u7-opencode-acp`
with its scenarios and the feature's opencode prose, to return once claude
works through the daemon. Until then an opencode child's ACP permission
request is refused, as before (daemon-application passes
`run_ports::ChildPermissions::default()`, `Refuse`: fail closed), and an
opencode one-shot is `opencode run`, which has no ask channel (every ask
auto-rejected). The approvals domain already knows ACP's option kinds
(`deny-always`, `-message-not-relayed`); nothing in this build records one.
Nor does a launch with no context (the distiller's, `run --no-context`):
it carries no ctxloom server and so no prompt tool, and claude's headless
default denies its asks (owner ruling, 2026-11-16). An interactive session
asks at its own terminal (S28); the mock engine asks nothing.

## The scheduler runs in the daemon (step 8 U5a; plan `approvals-trust-step8.plan.md` U5)

The scheduler's crates (`core/domain/scheduler`, `core/read-model/
scheduler-due`, `core/application/scheduler-loop`, `core/ports/
scheduler-ports`; design `scheduler.plan.md`) are wired. Every core
registers `SchedulerAggregate`, exposed to nobody (its streams are written by
policies and the clock loop alone). Only the DAEMON's core
(`core_link::InProcessCore::start_with`) then registers each domain that
arms alarms, `core_link::ALARM_CONSUMERS` (an `AlarmConsumer` is a `fn(&mut
Registry)` registering the consumer's arm/cancel policy towards the
scheduler and its `scheduler::delivery!` policy), and resumes at start
(`CoreStores::at_start`, `Resume::AtStart`), which runs the policy work its
store owes: an alarm fired and not delivered is delivered (a crash between
the two hops, or a delivery policy new to a store with history), an ask
recorded and not armed is armed. Its one entry is the approvals' expiry (U5,
above). A CLI command's core (`InProcessCore::start`) registers no alarm
consumer, no policy at all, and resumes nothing (`Resume::Never`: it lists
no stream and scans no policy source; owner ruling 2026-11-16). That is
correct because nothing a CLI process commits is a policy's source: the
policies' sources are the approvals' and the scheduler's streams, and every
ask, answer, withdrawal and alarm is the daemon's (the hook gate, the
daemon API's `CreateApproval` and answers, the scheduler loop); a CLI
process commits only the sessions domains' (`sessions-application`'s
registry, which holds no policy), and no production CLI process starts an
`InProcessCore` at all (`run` and the session verbs are the daemon's
clients). Work a core without the consumers left owed waits for the
daemon's next start (`core_link.test.rs`'s `resume_at_start`).
The daemon's core also registers the due model (`Projectors::due`, the
wiring's `scheduling::SchedulerDues`: `scheduler_due::Due` held by
`held-projection`, so the kernel's inline fold and the loop's catch-up are
ONE fold, each page once; `scheduler-loop`'s `SchedulerLoop::sharing` reads
it through `scheduler_due::ReadsDue`). The daemon runs `SchedulerLoop::run`
for its life on its runtime (`daemon/serve.rs`'s `scheduler_over`, the
wiring's `scheduling::start`): on `core_link::SystemClock` (the clock each
call's sample reads), waiting `min(next due, the daemon's poll)` on the
runtime's timer, cut short by any book the kernel folds into the due model
(an alarm armed in process fires at once) and by the stop; it fires through
the kernel directly (`SharedCore::kernel`), so the edge's test kill switch
never sees a clock fact. A tick that fails (its catch-up) is said and the
loop goes on, its model kept; the log says each `alarm fired: <target>
<alarm>`, each fact that failed and each unreadable stream. It runs while the
daemon drains; once the daemon's loop returns it is stopped
(`lifelong_ended`: its current tick finishes, at most five seconds, else it
is cut and said) BEFORE anything else is let go, so its copy of the core is
gone before the store closes (ruling 131; a running scheduler is a holder:
`Unclosed::StillHeld`). A daemon started after another, killed or stopped,
rebuilds the model from every scheduler stream's start: an alarm that came
due meanwhile fires once, late, at the instant armed. `scheduling.test.rs`
proves arm-fire-deliver, cancel, the in-process wake, the kill and restart,
the resume's delivery and the holder, over a real store with
`scheduler-fixture`'s dummy domain registered as a test's consumer
(`InProcessCore::start_consuming`, crate-private; the fixture is a
dev-dependency, never in any build of the binary), a `FakeClock` moved by
the test and forced wakes, no sleep. Not proven: a real process kill (the
"kill" aborts the loop's task and drops the core unclosed). The binary's
daemon firing an alarm is proven end to end by session_approvals.feature's
expiry scenario (an ask's alarm under a 2 s `approval_timeout`).

## The library domain (step 10a U0; plan `t13-step10.plan.md`, G4 superseded)

Step 10a U0 adds three crates on `proto/ctxloom/library/v1/library.proto`
(the red spec's `protos.md`, amended for G4's supersession):
`core/wire/library-domain-proto`, `core/domain/library-tree` (G10; the
domain is `library`) and `core/read-model/library-checkout` (`Held`). One
stream per library TREE (L1): a project checkout's `.ctxloom/`,
`projects/{p}/checkouts/{c}/library`, rooted on the UUIDv5 of that name
(`sessions::root_of`), or the home's, named `users/me/library` whatever
home it is and rooted on `users/me/checkouts/{c}/library` (QUESTIONS B(a):
each `CTXLOOM_HOME` a stream of its own; `library_tree::TreeName`). `{c}` is
the checkout's harp in its `.ctxloom/state/checkout` (out of git, so a git
worktree is a checkout of its own; L2): the wiring's `checkout_harp` reads
it and mints nothing, `mint_checkout_harp` mints it once (`O_EXCL`, a file
left empty re-minted), for U2's first read. Nothing holds a file's text, a
config value or a key: paths, git object ids, statuses and request ids.

An observation is a FACT, `RecordTreeObserved`, keyed `obs:{tree}:
{H(last_cause)}:{head}:{H(sorted dirty)}` (L4, `observation_key`, SHA-256
hex, each `path NUL oid NL` in path order; `observation_book` builds the
keyed fact), so the same sight twice is a DUPLICATE and A to B to A to B
never collides. A FACT CARRIES NO SEQUENCE (owner, G4 superseded): the event
it records is given one, its page's, and `apply` (`PageContext::sequence`)
folds each named path to `{last sequence, oid}` (`library_tree::Library`,
the aggregate's and `library-checkout`'s ONE fold); a rename empties its
`from_path`; a baseline clears them and sets `baseline_sequence`.
`Library::token(path)` is the etag: the greatest sequence at or under the
path, else the baseline's. `last_cause` is the page's causation (a fact's
external id, a command's id). The aggregate records an observation only when
it adds something: a tree's first is its baseline; a change already held at
the same oid is dropped (an observation made against an older state records
only what it adds); HEAD alone moving records nothing (QUESTIONS A), nor
does a motion alone. `RecordStepDone` (the pending intent's next step ties
its `observed` paths to its event's sequence) and `RecordStepFailed` record
for a pending intent only. The five commands (`ReplaceFile`,
`CreateDirectory`, `TrashPath`, `MovePath`, `RunSequence`, each with a
`verb`) are decided on `Evidence` in order (`BuiltinReadOnly`; the plan's
`PlanRejected`, which speaks as the plan's own diagnostic, or
`library-tree-tree-unreadable`), then existence as the record knows it
(`ResourceExists`, `NoSuchResource`) and the token the caller saw
(`seen_sequence`, `PlanStale`); each judges only what its `refuses` lists, a
sequence's steps their tokens alone; they record a `ChangeIntended`, which
moves no token. Nothing sends them before 10b (U3). The texts are 10a's and
10b's accepted drafts. Departures from the approved proto, each forced by
angzarr-build's domain layer and wire-compatible: `Step`'s arms and
`TreeObserved`'s cause are `optional` fields (no `oneof` is mapped);
`OneChangePerCall` waits for 10b (U8), as no command refuses it yet. git is
no evidence (the library is git-free, below): the git refusals
`NotInGit`, `LibraryIgnored` and `TreeInMotion`, the `Motion` enum and every
`in_motion` field are gone, their fields RESERVED (`Evidence` 4 to 7,
`TreeObserved` 8, `RecordTreeObserved` 10, `RecordTreeListed` 5) and their
names never reused. Not built here: the observer (U12's git port, U2's
reconcile), intent attribution and supersession (10b, U13), trash. The observer and
the reconcile are U2's (below, "A library read observes its trees first").

The kernel registers the aggregate in every core, on loopback
(`core_link`), the daemon's `library-checkout` (`Projectors::checkouts`,
caught up at the start and each round, `Folds::checkouts`); the coordinator
reaches the domain and the user allowlist holds its five commands and three
facts (runs-application's `EdgeCommands::reach`, `user_allowlist`). Under
`test-support` only, `ctxloom test library-events [--home] --format json`
(QUESTIONS G) reads the cwd checkout's (or the home's) stream from the one
store: `{"tree", "events": [{type, sequence, baseline, head_before,
head_after, changes: [{path, status, from_path?, oid?}]}]}`,
`"tree": null` where no checkout harp was minted; it mints and records
nothing.

## The library is git-free (owner 2026-11-18; superseding G1, G2, G3 and L11)

The scanner sees a library tree (below), so nothing on the library path
asks git: a library works in ANY directory, in git or not, whatever git
ignores and whatever git is doing. No read or write is refused for git's
sake (`library-tree-not-in-git`, `-library-ignored` and `-tree-in-motion`
are gone, with `locate::refusal`, `reconcile::motion_of` and the read
notices `library-api-notice-tree-in-motion`); git missing fails no library
command. The HOME is never made a repository (the home step,
`ensure_home_repository`, `HOME_GITIGNORE` and
`library-api-notice-home-repository-made` are gone) and ctxloom commits
nothing, in the home or a project (G3's home commit, `home_commit`,
`library-api-notice-home-not-committed` and the `RecordHomeRepositoryMade`
fact are gone): the event store is the history. The home's library is
observed whenever it has a `.ctxloom`. `config create` makes no
repository (`with_repository_step` and `Notice::Git` are gone). Foreign
writes (U10) are recorded in any directory: U10's unrecorded outside-git
fallback is gone. Accepted cost: the home loses its per-write git history.
`HomeRepositoryMade` stays a decodable EVENT, never emitted and folded as
nothing but its cause, so a stream a 10b daemon wrote replays
(`LibraryState.home_repository_made`, 11, is reserved).

git stays where a feature needs it: child checkouts (`worktree-git`),
remote bundles (`remote-git`) and the session home's identity seed. Init
asks no git (step 10c U6) and neither does `config create`, so
ctxloom-ports' `TreeObserver`, `adapters/driven/library-git` (`GitTrees`,
its `library-git-*` diagnostics), `init_application::ensure_repository`
and `init-application-notice-git-initialized` are gone with their last
user.

## A library read observes its trees first (step 10a U2, in part)

`core/application/library-api` is the daemon's library reads' crate. A
tree is SEEN BY THE SCANNER, never by git (step 10c U13 rework, owner
2026-11-18): `ctxloom_ports::LibraryScanner` (the wiring's `TreeScanner`,
handed out by `Observers::scanner`, one per daemon) walks the library's
paths (`ctxloom_ports::LIBRARY_PATHS`: `content/bundles/v2`, `config.yaml`,
`remotes.yaml`, `lock.yaml`; L13) and hashes each file:
`library_tree::content_id`, the SHA-256 of its bytes, ctxloom's digest and
never git's (no SHA-1 is added). Its edge memo, keyed by path and stat
(length and mtime), reuses a hash whose file is unchanged and had settled
(`SETTLED`, 2 s, git's racily-clean case) when hashed; `forget` drops it.
`Observations::observe` sends ONE `RecordTreeListed` (every file and its
content id, keyed `listed:{tree}:{H(base_cause)}:{H(sorted listing)}`,
`library_tree::listing_key`, chained so A to B to A is three) unless the
scan matches what the record lists (`listed_as_held`; a path a step took
away, held with an empty id, is absent: `Library::listed`); the AGGREGATE
compares it (`listing_changes`: each path added, modified or deleted,
sorted; a move is a delete and a create) and records the `TreeObserved`
a read's would be, the first a baseline of every file, an intent's own
change attributed as before. Writes' intents and steps name content ids
(`writes::intent_of` asks nothing; `left_by` and an export's manifest ask
the scanner). No git is asked at all (the library is git-free, above).
`Observations::observe(scanner, site)` observes a `Site` (a checkout's, or
the home's), naming the checkout first (`CheckoutHarps`: the wiring's
`TreeHarps`, `mint_checkout_harp`), reconciling against the kernel's `library-checkout`
(projected inline, so the next read sees the last one's), and records the
fact through the edge (`observation_book`); no lock is held across the
recording: one sight twice against one state is a DUPLICATE, and one
against a moved state records what it adds (forced in `observe.test.rs`).
`observe_read` is a read's whole step: the project's checkout, then the
home's library when it has one (`Observers`: the wiring's `CallerGits`,
the daemon's one scanner and the home); it says no notice of its own now
(its `notices` plumbing is kept, empty).
`library_api::names` is the library's resource naming, one copy
(library-cli re-exports it until each noun is served).

The daemon's `Api::observing(Observing)` (serve.rs) makes every library
read it serves (`Api::library_read`, `AssembleContext` among them) observe
first: the library domain's refusals travel as its own typed refusals
under their messages' status (`daemon_api::library_refusal`;
`rejection_of` maps `PlanRejected` and the write refusals back to
`Rejection::Library`, exit 48). A failure an adapter diagnoses (once git
missing or too old, now e.g. a deps read's) is a DIAGNOSED failure (step
10a U2, owner ruling 2026-11-17): `ApiError::Diagnosed {code, args}` (the
adapter's diagnostic's), answered FAILED_PRECONDITION
(`run_ports::Infrastructure::Unmet`, never UNAVAILABLE, which a client
reads as an unreachable daemon) carrying a `google.rpc.ErrorInfo` whose
domain is `run_ports::DIAGNOSED` (`ctxloom.dev/diagnostic`), reason the
code and metadata the args; the client reads it back as
`Called::Diagnosed` (`daemon_client::called`, before any status is read),
so the command says the diagnostic in its own catalog's words and exits
10.

`just bench-reconcile` (O7; `LIBRARY_BENCH=reconcile`, else the test says
SKIP) times an observation's scan over `CallerGits`' scanner, its memo
warm, on a repository of 100 000 files with 500 in `.ctxloom` (the
scanner walks the library alone): p95 under 50 ms for a read seeing nothing
new and one seeing a hand edit. (Its git-era figures, p95 46 and 50 ms, were
`git status`'s.)

ONE `Caller` message (owner, 2026-11-18; needs.proto): the client's working
context, carried at field 98 of every request made on a user's behalf
(everything that plans, reads the library, runs git, writes or launches).
It replaced the hand-copied `env` (94), `presentable` (95), `globals` (96)
and `cwd` (97), and CreateRun's `cwd` (13) and ResolveProject's `cwd` (2),
all reserved (a non-additive change, ruled); InitProject's `cwd` (2), the
directory to initialize, stays. It holds `cwd`, the WHOLE `env` (the daemon
takes a call's temporary directory from it, `TMPDIR`, else `/tmp`; on
Windows `TMP`, `TEMP`, `USERPROFILE`, the Windows directory; only a call
with no environment at all is in the daemon's own: the wiring's
`caller_temp`), `globals`, `presentable` and `client` (`Client`: kind
`CLI|MCP|HOOK`, the running binary's file name and directory, the version
stamp). NOT in it (ruled): the umask (a known limit for group-shared
trees), the caller's session (a domain command's `requester`), locale, TZ,
uid, OS and render flags. Reads by name and the daemon's lifecycle carry
none: the binary's `workspace.test.rs` reads the descriptor set
(`daemon_proto::descriptors()`) and FAILS any RPC whose request lacks
`Caller caller = 98`, but those of its explicit `CALLERLESS_RPCS` (GetDaemon,
StopDaemon, GetProject, the session reads, transcripts, WatchSession,
ListWorktrees, DeleteHandOff, GetRun, WatchRun, the approvals and trusts),
and fails a listed one that carries it. Rust reads it through
`daemon_proto::Carries` (generated for every message by daemon-proto's
build: `CALLER`, `caller()`, `caller_env()`, `caller_cwd()`,
`caller_globals()`, `caller_presentable()`, `caller_client()`) and writes it
through `CarriesMut` and `Caller::fill_from` (each part only where the
request names none: a command sets its own flags and presentable). The
transport fills it: `daemon_client::DaemonClient::filling(caller)` fills
every request it sends (the wiring's `caller_of(env, kind, stamp)`, set
where each client is made: `Reach::client`, `MCP` for a never-stopping
reach, `resolve`, `HookDaemons`), so no path that builds a request can
leave its client out. The daemon REFUSES a client whose stamp is not its
own, whatever its kind (`Calls::admit`, `daemon_api::admit`, asked by the
gRPC edge's `answer` for every request whose type carries a `Caller`,
before its use case): the typed `ClientVersionMismatch`
(failed-precondition), `daemon-api-client-version`, exit 48, its fix
`ctxloom daemon stop`; a request with no client names no version and is
refused alike. Under test-support a spawned daemon is given its client's
`CTXLOOM_TEST_STAMP`. A launch runs the CLIENT's program, not the daemon's
`/proc/self/exe` (within ruling 37's same-user trust: the client can name
nothing she could not run herself): a hosted one-shot's and a terminal
run's `HostRequest.program`, a hosted child's `HandOff.program`
(`daemon_application::Program`, `daemon_api::program_of`), for its hooks'
and statusline's command (`ForLaunch` composed with it) and to lead its
engine's `PATH`; none named, the daemon's own. A child's program is kept
beside its hand-off in the daemon's memory (`HandedPrograms`), never in the
file (its format unchanged): a daemon started again between the hand-off
and the launch runs its own. Not built (escalated): the CLI saying
`can_launch` only at a terminal; library-cli still says it can launch the
editor everywhere, as the editing scenarios edit off a terminal.

The owner's rulings of 2026-11-17 (E1, E2, E5, O3, the read notices) are
built:

- E1: every library read request (each Get, List, Search, Browse and
  Discover of library.proto, and `AssembleContext`) carries its `Caller`
  (its `cwd`, `env` and `globals`; below). A served
  read (daemon-api `library_reads`) resolves its project by its harp, is
  read at the caller's directory (else the registered root), finds the
  checkout there as a process would (the root's `ProjectLibraries`; a git
  worktree is a checkout of its own, L2), observes it and the home, then
  opens the library in the caller's environment under its globals (never
  `trusted`: U7).
- E2: `AssembleContext` answers the context WITHOUT the premise index, and
  what a premise held back as data (`AssembledContext.premised`); none of
  `library::compose::Options.surface` / `AssembleRequest.surface` means
  "the caller renders it". The MCP server's `context_assemble` renders it
  on the launch (`library_api::premise::render_premise_index`, re-exported
  by daemon-api, `Shown::Launch`); the CLI's `context assemble` renders it
  at rest (`Shown::AtRest`) and prints the message with the index in its
  `context` and `premised` and `notices` emptied, as the in-process answer
  printed it. The words are still `library::premise`'s (reached through
  `library_application::premise`): the in-process launch package and
  materialize render with them too, so the instruction text stays in the
  core until those are served (10b/10c).
- E5: each decline of a library use case is a typed refusal of its own
  (`ctxloom.v1.Library*`, library.proto, each with its `(refusal_status)`),
  mapped back to its `library-application-*` diagnostic by its type alone
  (`daemon_api::library_refusals`, `Rejection::LibraryRead`); a wrapped
  cause rides as its code and flattened args (`ctxloom.v1.Cause`).
  A read model's own refusal (`AppError::Declined`) is typed per cause too:
  each `library` diagnostic a read declines with (the catalog's nine, the
  profile's eleven, the config's not-a-mapping and unwritable) is its own
  message, `ctxloom.v1.Library<Catalog|Profile|Config><Variant>`, its args
  its fields, both ways from one table (`read_model_declines!` in
  `library_refusals.rs`, keyed by the diagnostic's code; the deps read
  models' declines are rows of the same table); `LibraryDeclined`
  is left for a cause no read declines with (a write's, typed with 10b).
- Notices: a library read's own notices (the observation's, then the use
  case's) ride the response metadata key `ctxloom-notice-bin`
  (`daemon_proto::NOTICE_METADATA`, one prost `Notice` per value, apart
  from the standing warnings), carried to a client as `daemon_ports::Read`
  and said by the CLI before its answer, in its own catalog's words (by
  code and args). Only an ANSWERED read carries them: a refused or failed
  read says its refusal alone (accepted, as a status has no notices).
  `AssembleContext`'s ride in `AssembledContext.notices` instead (the MCP
  tool's answer is the message), where the MCP edge adds its own
  `daemon-api-assembly-missing-fragment` / `-tag` for what it asked and
  the catalog lacks (`daemon_api::missing_notices`).
- No bundles directory (owner ruling 2026-11-17): a project with no
  bundles directory lists as one with no bundles ("No bundles installed."
  and its install hint, a project's or none's), never Go's mkdir line,
  and a read creates nothing; installing a bundle makes the directory.
- D12: one table, `daemon_proto::library_reads!`, generates each layer's
  part of every library read (the port's default, the client's call, the
  API's `Calls` default, the gRPC handler, and daemon-api's `LibraryReads`,
  the CLI's client: each `fn(under, request)`, its target the client's
  parent, `projects/{harp}` else `users/me`, then `under`). What the daemon
  SERVES is `library_api::served!` (each row's
  `library_api::reads::$method(library, flags, parent, request)`), from
  which `Api`'s `Calls` methods are generated; what the CLI asks is
  library-cli's `served_reads!`, from which its `Served` trait, the
  wiring's `DaemonServed` and the tests' `ServedBy` are generated. A read
  is served by a row in each. The deps reads are the same, over their own
  tables: `library_api::deps_served!` (`library_api::deps::$method(deps,
  parent, request)`, `Api::deps_read`, over the `Libraries` port's
  `read_deps`, the wiring's `ProjectLibraries` opening the project's
  `Deps`) and deps-cli's `served_reads!` (its `Served`, which the wiring's
  `DaemonServed` implements too); `Api::library_read` and `deps_read`
  share one `read_site` (parent, directory, environment, flags,
  observation). `AssembleContext` is a library read too
  (`Api::assemble_context` through `library_read`; `LibraryReads::
  assemble_context`, `Served::assemble_context`); the daemon's
  `Assemblies` port and the wiring's `ProjectAssemblies` are gone.
- Targets: a command names what it reads by its text AS TYPED
  (`library_api::names::bundle_target`, `reference_target`), read back
  exactly in the daemon (`bundle_under`, `reference_under`: a remote's
  bundle, a canonical reference with slashes, a reference of no valid
  shape, `ctxloom+local:` all round trip, `%` and `/` escaped within one
  segment), so the use case refuses it in its own words (`invalid
  reference format`, `bundle not found: demo#fragments/testing`).
- One per-call builder (U2's Deletes): every call the daemon serves is
  read as `daemon::caller_wiring(home, temp, (dir, root), env)` (over
  `caller_env`): a process at the caller's directory in the caller's
  environment, ctxloom's home as its home and the caller's home kept as
  its own; `root`, when given, its `CTXLOOM_ROOT` over the caller's. The
  library reads' `ProjectLibraries`, the trust read's `ProjectTrusts`, the
  sweep's settings, a child's plan (`child_library`) and a hosted run
  (`oneshot::hosted_wiring`) are all made over it;
  and every library is built as a run's is (owner 2026-11-21):
  `library::build(wiring)` makes the dependency use cases itself and
  `library::build_over(wiring, remotes)` takes a caller's, neither optional,
  so a library without the remotes (a profile naming an installed remote
  bundle unresolvable) is unrepresentable: the daemon's child plan, doctor,
  the setup reads, the seed and the container verbs all read remote bundles
  as a launch does; `per_call_env` (which
  set ctxloom's home as the caller's HOME) is gone.
- Served: `ListBundles` (`bundle list`), `GetBundle` (`bundle show`, FULL;
  `bundle view <bundle>`, CONTENT: its files; a path no item kind serves,
  `<bundle>#<path>` read back whole, refused as the view refuses it),
  `bundle view <bundle>#<kind>/<item>` as the item's own read (owner
  ruling 2026-11-17: rendered from the item's message, no source-bytes
  field, not the file byte for byte): `GetFragment`/`GetCommand` its body
  (`# (distilled version)` over a distilled one), `GetSkill`,
  `GetMcpServer` (one bundle's server, a bundle of one answering whatever
  the ID, as Go's view did) and `GetProfile` their fields as YAML under
  `# <Kind>: <id>`, keys sorted, empty ones left out, references short
  (library-cli's `viewed_by_the_daemon`, `read_back::{body,skill,mcp,
  profile}_view`; a profile's `select_tags`, `fragments`, `hooks` and
  `deny_tools` among them, below; library-application's `bundle_view` of
  an item answers its data alone, `Viewed`, no content), `ListRemotes`
  (`remote list`), `BrowseRemote` (`remote show`: what a remote publishes,
  read from its cached clone as before, cloned first where none is cached;
  `library_api::deps::browse_remote`, the remote's ID read back from its
  name exactly; owner ruling 2026-11-17; no profiles, as before and as
  Go), `ListDependencies` (`deps list`); a deps read's decline, a lock or
  registry that does not read, is its own typed refusal, the deps read
  models' ten (`ctxloom.v1.Deps<Variant>`: unreadable, too-new, too-old,
  bad-shape, forge-type, unknown-forge, no-repository, lock-empty,
  lock-key, lock-unreadable; E5, `read_model_declines!` beside the
  library's), and its failure (a remote that cannot be browsed) is said
  in its own diagnostic (`ApiError::diagnosed`, exit 32; daemon-api's
  `refusal::deps_error`), `ListFragments`,
  `ListCommands`, `GetFragment`, `GetCommand` (`fragment|command
  list|show`), `GetPremiseIndex` (`fragment premises`), `ListSkills`,
  `GetSkill` (`skill list|show`), `ListCompanions` (`companion list`, the
  programs looked for on the caller's `PATH`), `ListProfiles`, `GetProfile`
  (`profile list|show`; a bare name's target `/profiles/<name>`,
  `names::profile_target`), `ListAgents`, `GetAgent` (`agent list|show`;
  `names::named_target`; an agent's resolution error said in the program's
  words, `Library::with_words`, the wiring's catalog), `ListEngines`,
  `GetEngine` (`engine list|show`), `ListMcpServers` (`mcp server list`,
  and `mcp server show <id>` as its `mcp_server_id` filter), `SearchLibrary`
  (`search`; a `--type` word the API's `ItemKind` has no value for is
  refused by the CLI, `library-application-search-type`, as the request
  cannot carry it), `GetConfig` (`config show [section] [--raw]`: `SET`
  is `--raw`; served while the daemon drains, so a drain's own timeout is
  changed and read back while it runs), and `AssembleContext` (`context
  assemble`). The CLI is the request and the render (D10):
  `library_cli::served` says the read's notices, then prints the message
  as json or its text, drawn from the message alone (library-cli's
  `read_back`: the message read back as the view the text renderer
  prints); a one-bundle listing that holds none asks the whole catalog
  too, for its "exist in other bundles" line. The view-to-message mappings
  are `library_api::resources`, the one mapping. With no daemon, a served
  read fails (`daemon-api-unreached`, exit 32), text or json alike: a text
  answer no longer reaches no daemon (so `scripts/plain-engines-check.sh`
  proves the plain engine list by an agent written on a mock, refused in
  process naming the list). There is no home step (git-free), in the
  daemon or in process (`library_cli::is_served_read`'s `SERVED_VERBS`
  table, `mcp server list|show`, `search`, `loadout` and `run --dry-run`,
  still says which verbs are served).
- Text prints CANONICAL forms (owner rulings 2026-11-17), as the message
  holds them, never the spelling written (no as-written field crosses the
  wire): a reference is its SHORT library reference, harp-free, as a
  person types it (`library_api::names::short_reference`, the names table
  read back: a bundle by its catalog name, `demo`, `<r>/<b>`; an item
  `<bundle>#<kind>/<id>`, `project#profiles/base`, `demo#mcp/x`; one
  OQ-16 cannot place keeps its text): a profile's bundle, parents and
  bundles, an agent's profiles, an MCP server's bundle, a dry run's
  profiles, `fragment premises`; json keeps the resource name. A
  resource's own name is the one a person types (a project profile's bare
  name, `ctxloom+local:<b>#profiles/<p>`, else its short reference); a
  free-form document (permissions, `config show`) has its keys sorted. The
  Go-parity rows that printed the written spelling carry `# changed:`
  (profile.tsv, mcp.tsv, run.tsv; library_parity's `as_canonical`).
- `run --dry-run` is `CreateRun` with `validate_only` (`Api::preview_run`,
  `LibraryReads::preview_run`, library-cli's `Served::preview_run`): planned
  as a library read (its trees observed first) at the caller's directory, in its
  environment, under the project's trust as the daemon reads it, then
  judged on the untrusted project settings it reads as the runs aggregate
  judges an enqueue (`runs::pre_launch_refusal`, owner answer H:
  `runs-untrusted-project-settings`, exit 48, with the plan's fatal findings
  beside it one `runs-launch-refused`, exit 49); nothing is enqueued,
  minted or started. Its `Run` (`library_api::resources::planned`) carries
  the workspace axis and runtime the launch SETTLES on
  (`Run.settled_workspace`, `Run.settled_runtime`, fields 19 and 20: the
  asked or configured, else `none` and `host`; `workspace` and `runtime`
  stay what was asked and what the agent declares, Go's top-level keys) and
  `Run.settings_agent` (field 18, the agent the settings are of), its
  notices in `Run.notices`, so its text (agent, axes, Settings section) is
  drawn from the message; a plan with fatal findings prints and exits 49.
  A dry run never mints: a project nothing has named is asked under
  `users/me` from inside it (`daemon::preview_client`), and the daemon
  leaves its names unresolved.
- O3: the session-start nudge asks the daemon's `ListAgents` and
  `ListProfiles`, the skill mates its `ListSkills` (the bundle from the
  skill's resource name, the name its `Skill.display_name`, the SKILL.md
  frontmatter `name:` as the package names it, else its id: owner ruling
  2026-11-17), within the hook's bound,
  degraded (setup-application's `HookLibrary`, the root's
  `DaemonHookLibrary`; a hook whose environment names an endpoint asks only
  a daemon already serving); with no daemon in time a hook says nothing of
  the library and reads nothing in process.

- A profile's message (owner ruling 2026-11-17) carries its `select_tags`
  (11), `fragments` (12, `ProfileFragment{fragment, priority}`: an item
  reference by its resource name, a bare name as written), `hooks` (13,
  `ProfileHooks{unified, ext}`, field for field the library's
  `HooksConfig`, each hook a `ProfileHook`, field for field the library's
  `Hook`, read by `HooksConfig::read`; an entry no hook reads from left
  out, as the package leaves it) and `deny_tools` (14), all OPTIONAL.
  `profile show` prints them (Fragments, `(priority N)` where set; Select
  tags; Denied tools; Hooks, each under its document path, `unified.<e>` or
  `ext.<engine>.<e>`, on one line in YAML's flow form, keys sorted; Go
  printed none of them), and a profile's one-item `bundle view` gives them
  in the document's shape, keys sorted, references short.

- `GetLoadout` (owner ruling 2026-11-17): `loadout` asks the daemon for
  ctxloom's own `Loadout`, ALWAYS as `users/me/loadout` (the pattern
  `projects/{p}/loadout` answers the same): it is the same in every project
  and a companion probe asks it where there may be no project, git or
  identity, so the CLI asks it through the wiring's `daemon::home_client`
  (no project registered, minted or observed; library-cli's
  `Served::own_loadout`, not a `served_reads!` row) and a bare directory is
  left as it was; `GetLoadoutRequest{name, env, globals, cwd}` as every
  library read; the resource its `run:` bundle, `LoadoutBundle{version,
  tags, author, mcp_servers}`, each server `LoadoutMcpServer{id,
  served_by}`, field for field `library::write::loadout::OwnLoadout`; no
  field carries the withheld prose), answered by
  `library_api::reads::get_loadout` (`resources::loadout`) after the home's
  observation, as a `users/me` read; the CLI writes
  the document from the message (`read_back::loadout`, then
  `library_application::loadout::document`), whatever the output format,
  after the read's notices. The in-process path (the `LOADOUT` constant
  printed as it was) is gone; as a served read it needs a daemon. No
  library read is left in process (U2 done), with ONE named exception
  (D8, owner ruling 2026-11-17): the operational prompts, init's interview
  (`SetupSessions::setup_interview`, the wiring's `interview.rs`) and
  distill's instruction, were read in process through 10a, each asking
  the daemon one `GetTrust` (`Library::with_trust`), because no RPC
  answers an operational prompt; they are ruled to be served with 10b.
  Distill's is (step 10b: the daemon plans the distill's ask, under the
  project's trust as every served read is); init's is U6's.
  Ruling 128's behaviour is unchanged: a trusted project's own `ctxloom`
  bundle replaces the prompt whole, and one lacking it is refused.

## Storage is angzarr-store-sqlite

ctxloom's store is ONE per user (owner T14): angzarr-store-sqlite's
`SqliteStore` (through angzarr-embedded's `sqlite` feature), opened at
`<ctxloom home>/.ctxloom/ctxloom.db` (the home resolved as everywhere:
`CTXLOOM_HOME` first) by `ctxloom-store-sqlite` (the file name, the placement
and the `ProjectStores` port, over rust-support's `sqlite-store-opener`, which
refuses a store from before the embedded store, `ctxloom.db.pre-embedded`
naming its backup). Every project's streams are in it, each rooted on its
resource name (slice 2 re-root, above), so nothing routes by file: there is no `route.rs` and no routing metadata. taskloom's
`state.db` stays taskloom's, per project, never the same file (T2, D26). A
write for a project still makes its `~/.ctxloom/projects/<id>/` directory,
which taskloom's registry lists projects by; ctxloom keeps nothing in it. A
project that still has a store of its OWN there (`projects/<id>/ctxloom.db`,
from before) is refused per command (T17, S48): exit 48,
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
command (`Sessions::close`; a failed close fails a command
that had not), the seed's and `test open-store`'s, a run's at its end, and
the daemon's at its exit (after the launcher, the edge and the listener,
the read models it kept of those its kernel registers are let go too: each
holds the store through its gap fill, `StoreFill`), and on a start that
fails before it serves (the certificate's mint, the bind, the discovery
file's publish: what it started is let go, the log says "the start failed"
then "the store is closed").
A store has holders past its use cases: the core's application and edge,
the use cases' edges, and the daemon's hook listener's connection and stream
tasks, which tonic spawns itself, so a call still in the handler keeps the
coordinator, its edge, the application and the store. `HookListener::stop`
is angzarr's `Running::shutdown_within(STOP_GRACE)` (ruling 140): past the
grace it ends every connection still open (the call is cut) and returns only
once every copy of the service is released. So the end lets each go before
the close: the listener stopped (no run has one of its own since slice 4
U7), `Runs::into_core` (every part but the core, with their edges), then
`InProcessCore::close`,
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
companion) calls rust-support's `fixture_program::install_program(fs, path,
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
`gates/fixture-gates.just` recipe are copies of rust-support's.

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
are refused like any unknown format, exit 48. Errors are `ctxloom: <message>`
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
3) follow rust-support's convention, `docs/exit-codes.md` in that repository
(blocks: 0 and 16-31 successes, 32-47 failures, 48-63 declines, 128+n signals,
the class being `status >> 4` under `--detailed-exitcode`; the more severe of
two outcomes, the higher detailed code, wins), encoded as
rust-support's `exit-code` crate's `Exit` (re-exported by `cli-support`). An
application error says its class by implementing `ExitClass`; a `Failure`
carries an `Exit`; `ctxloom_cli::run` turns it into the status with
`Exit::code(detailed)`, the one place the collapse lives: without
`--detailed-exitcode` every success is 0, with it ok is 16 and a distinguishing
success 17-31; failures and declines are their codes either way. ctxloom's codes:

- **0** ok (an apply, a read, or a plan or preview that succeeded), and every
  success without `--detailed-exitcode`; **16** is ok under it;
- **17** would change: a plan or preview that succeeded and would act,
  returned ONLY under the global `--detailed-exitcode` (without it, 0):
  `run --dry-run` whose plan has no fatal finding (it would launch);
  a removal's preview without `--yes` (`bundle|fragment|command|skill|
  profile|agent|companion remove`); `remote remove`
  without `--yes`; `deps upgrade` without `--yes` with a pin to move or
  drop; `clean` without `--yes` with a target present; `session remove`
  without `--yes` that is not refused; `session sweep` without `--yes`
  with a planned destroy; `agent edit --engine` without `--yes` whose
  new engine cannot carry what the old one did (the swap preview);
- **32** failed: ctxloom tried and something broke (an adapter's error,
  I/O, the store or the kernel, an engine, git, a clone, a fetch, a
  publish, a pull, the retry budget); the convention's **47** broken
  (a failure no retry fixes) is not used: nothing in ctxloom classes an
  error so;
- **48** refused, a decline: the request or the state forbids it, by kind,
  whatever Go said (owner ruling 152): a usage error (clap's own 2
  mapped), an unknown format or verb, a config the strict decode refuses
  or that does not read, a name nobody has (a session, a bundle, an item,
  a profile, an agent, a remote), a taken name, a live session (a run's
  or session's lock another process holds; a lock file that cannot be
  made is 32: the lock ports answer `LockError::Held` or `Failed`), any
  refusal of a domain or the read model or a write, a container verb with
  no runtime, a refused consented refresh, a refused delivery, a hook its
  service refused, and doctor's `fail` row (the refusal it guarantees);
- **49** fatal findings: ONLY the run plan's (a dry run's or a launch's
  startup abort, materialize's strict refusal, a fatal isolation finding);
  a launch both refused and with fatal findings is 49;
- **128+n** stopped by signal n (ruling 114).

Each application error says its class in its `ExitClass` impl. An error
wrapping another crate's says which it is: `AppError::Cause` (an adapter's:
32) and `AppError::Declined` (`AppError::cause`, a read-model or write
refusal: 48) in library- and deps-application; `SetupError::Classed` keeps
a library error's own class. An engine's own status (an interactive
session's, init's interview) is said in these classes by
`Exit::from_child_status`, never passed through: 0 ok, 128+n the signal,
any other failed (32). A hosted run's `exit` crosses the wire as its code under
the flag (`client::exit_of` reads it back by its blocks).

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
`library-application-write-bad-id`, exit 48, with a `-fix`; a name already
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
`RemovalFile.Class`, `Run.Mode`, `RemovalRefusal.Reason`, `Bundle.Origin`,
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
answer prints one (`run --dry-run`, below, asks always; a read the daemon
serves, step 10a U2, reaches it text or json): the wiring's is the daemon's
`ResolveProject` (`daemon_api::Client::library_parent`, the wiring's
`daemon::library_parent`; OQ-2, so a json answer may start the daemon), or
`users/me` outside a project, where no daemon is asked (OQ-3). ONE
project resolver serves every command (owner ruling 2026-11-13): runs,
sessions and library reads belong to the project `CTXLOOM_ROOT` names,
else to the nearest `.ctxloom` up (not ctxloom's home's) that no git root
lies between (owner ruling 2026-11-20: the walk stops at the first git root,
a `.git` directory or file, so a worktree's too, unless that root holds the
`.ctxloom`; a directory in a nested repository never adopts an enclosing
project, and resolves to its own or to none), as every library
read finds it (`Wiring::ctxloom_project`, through `find_app_dir`); only
where there is neither is the root the registry's own (the git top, else
the working directory). THE PROJECT ROOT AND THE DAEMON ROOT ARE TWO THINGS
(owner ruling 2026-11-22): the project root is `CTXLOOM_ROOT`, or its flag
form, the global `--root[=<dir>]` (`--root=<dir>` names it, a bare `--root`
makes the user's home directory the project root; the flag wins over the
variable; `ctxloom_wiring::root_flag::applied` puts it in the environment
snapshot as `CTXLOOM_ROOT` before anything is wired, so the daemon, which
reads the caller's whole environment, sees it); the daemon root is
`CTXLOOM_HOME` (the store, the daemon's directory, the `users/me` library;
to be renamed `CTXLOOM_DAEMON_ROOT` / `--daemon-root` apart). The walk never
adopts the user's home's `.ctxloom` nor the daemon root's as a project
(`Surroundings::user_home`, `home`); only an explicit root does. A run
(`client_one_shot`, `client_terminal`) where no project is found, no root is
named and no git root is above the working directory is refused
`ctxloom-wiring-project-root-needed` (exit 48, its fix naming `--root`, the
wording a DRAFT); `init` there makes the project in the working directory.
The per-verb `--home` of `config set` and `agent create|edit` is the home
CONFIG layer, unrelated. `agent create|edit`'s root selection per surface kind is `--surface-root
kind=root` (it was `--root`, which the global took; hard migrate, no alias). It is the registry's `Environment.root`
(`Wiring::environment`), which the run, the sessions use cases, the
daemon client's `registration()` and the names' `library_registration()`
(the same, but `users/me` outside a ctxloom project) all use, so a
subdirectory names its root's project and a monorepo subfolder with a
`.ctxloom` of its own is a project of its own (accepted cost: its runs move
to that project's streams in the one store). A COMMAND THAT MUST NAME THE PROJECT MINTS its
id at the RESOLVED ROOT (owner ruling 2026-11-14, revising 2026-11-13's
"only run and init mint", since `.ctxloom/project-id` is gitignored and every
fresh clone has none; never in a subfolder): a json library or deps answer
that prints a resource name (`library_parent`, `Wiring::
library_registration(Minting::AtRoot)`, through `Wiring::
naming_registration`), `daemon resolve`, a run (`tell_project`) and a bare
`init` (`Wiring::mint_project`, after its scaffold; not `init prompt`). A
read that prints no project name writes nothing (a text answer asks no
names); outside a ctxloom project the parent is `users/me` and nothing is
written. A DRY RUN NEVER MINTS (owner: "dry run should *not* mint"): its
names source is `Minting::Never` (`library::minting`: the library's `run`
noun is the dry run alone), which in a project nothing has named is
refused, `ctxloom-wiring-project-unnamed` (exit 48, its fix `ctxloom
init`), though the dry run asks `Names::named_parent`, which answers none
there. A
session listing or search in a project with no id (or wherever no id is
bound) names no project and lists nothing, asking no daemon
(`daemon_api::Client::unnamed`), and a `session show|remove` of one is
`daemon-api-project-unknown`. The test harness's "an initialized ctxloom
project" (`Project::initialized`, `with_editor`, a fixture's `.ctxloom`) carries
the id init mints (`Project::named`); `Project::configured` has none. Converted:
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
item's own resource as its Get answers it (its `body` the bytes served); `mcp server show <n>` is
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
`projects/{harp}/companions/<name>`, its program `bin`, `resolves` (was
`exists`) and its `state` (`ENABLED`/`DISABLED`); `companion add` answers
`CreateCompanionResponse` (`applied` when the registration is new,
`resolves` true: its program answered the probe), `remove`
`DeleteCompanionResponse`, and `enable`/`disable` (served writes of the
home config, step 10b) `EnableCompanionResponse`/`DisableCompanionResponse`
(`companion`, `path`, `applied` when the state changed, `etag`, `notices`;
their requests take `validate_only`): owner 2026-11-18, night, the one
non-additive change, for these two RPCs alone (they answered the bare
`Companion`, AIP-136). `loadout` is unchanged.

Companions (M4; owner rulings OD-8 to OD-10, OD-22, 2026-11-13): a companion
is registered by NAME alone (`companions.<n>: {state: enabled|disabled}`,
config schema 9, `enabled` the default; a hard migrate from the command form
an earlier build wrote, whose `command:` is now an unknown key, and
`Companion.command` field 3 is reserved; Go's generation-8 name list is
migrated in memory, each name enabled). Its program is
`library::companion::program` (`ltk`, `taskloom`, `reprise` their own, any
other `ctxloom-companion-<n>`), found where it is asked. ONE probe,
`companion-probe`'s `ProcessProbe` behind `ctxloom_ports::CompanionProbe`
(`find`, never run; `loadout`), serves every caller (an assembly, `companion
add`'s check, `companion list`'s `resolves`, doctor's
`DOCTOR-CHECK-COMPANIONS-m4`, `container tooling` through
`Library::companion_inits`): it holds no environment or directory, and each
call names its `ProbeSite` (D1): the caller's environment (whose `PATH` is
searched) and the project's root as the program's working directory
(`Library::with_probe_cwd`, the caller's directory outside a project).
`companion enable|disable` write the home's registration, one section
replace, nothing when it is so already (D3); a disabled companion is listed
and asked by nothing. An enabled companion whose program is absent is the
notice `library-application-companion-not-on-path` (name and program), never
a refusal. A companion's hooks count in every agent's losses, one with no
profiles too (OD-22, `Project::losses_on`). `config show
[section] [--raw]` is the `Config` singleton, `projects/{harp}/config`
(`users/me/config` outside a project), the document it printed before
under `document` (so `agents.default.engine` reads
`document.agents.default.engine`); `config create` and `edit` stay text
only. `run --dry-run` is `CreateRun` with `validate_only`, its json the
`Run` it would create (daemon.proto's `Run`, amended per design §5.9:
`settings` an `AgentSettings`, `tokens` uint32, and `workspace`,
`runtime`, `prompt` and `fragments` added; the draft's `Setting` is gone),
named under the project the daemon names. EVERY dry run asks the daemon
(owner rulings 2026-11-14: "dry run *absolutely* calls (and spawns, if
necessary) a daemon", then text or json, named project or not), before it
plans: `daemon_api::Client::named_parent` resolves a project's parent, and
where there is nothing to resolve (outside a project, or one nothing has
named) reaches the daemon and asks `GetDaemon`, so a daemon not running is
started, and one that cannot be is the client's `daemon-ports-not-started`
(exit 32, naming the log), as for every daemon client's command; a text
dry run prints no name. Its json is `<parent>/runs/-` (no
run ID is given, nothing being created), its agent, engine and profiles
resource names. It never mints the project's id (`Minting::Never`; library-
cli's `Names::named_parent`, the wiring's `library_parent_if_named`): in a
project nothing has named, its `name` prints as `null` and its references
keep their configured text. A finding's false `fatal` is left out. Go parity ends for these nouns' JSON (design §8.1): their JSON rows
are retired from `fixtures/parity/`, the text rows stay; `config show`'s
JSON rows still compare Go's answer, the resource's `document`
(`library_parity`'s `as_go_printed`), and the dry run's compared keys
read a default proto3 JSON leaves out as Go's printed one (`printed`), its
agent and profiles each name's last segment (`as_configured`).

The feature steps read proto3 JSON (design §8.1, the binary's
`library.test.rs` and `gherkin_common.test.rs`): a selector `[key$=suffix]`
picks the element whose `key` ends so (`engines[name$=/engines/opencode]`);
`{project}` in a step's path or value is the project's harp, which the
harness asks the hidden `ctxloom test project-name` (test-support; it
prints the `Names` parent, `projects/<harp>` or `users/me`); an absent field
reads as its default (`as "false"`, `"0"` or `""` passes where proto3 JSON
left it out, anything else does not), and an absent list has 0 entries.

## The terminal wake (step 9, owner rulings 2026-11-18)

A primary terminal session is woken through its engine's own messaging
socket. claude exports `CLAUDE_CODE_MESSAGING_SOCKET` (and `_TOKEN`) to what
it starts, so `ctxloom mcp serve` (the stdio server it starts from
`ctxloom-mcp.json`) holds `WatchSession` open for its own session
(`CTXLOOM_SESSION`; server-streaming, checked as `GetSession` is) and acts on
`daemon_api::Waker`: a pending wake it never woke for, with none outstanding
(one at a time), is issued (`sessions.IssueWake`, journaled), posted
(engine-claude's `messaging_wake`: Go's NDJSON, an auth line then
`{"type":"user",...,"content":"ctxloom: wake <16 hex> (messages delivered at
turn start)"}`, `run_ports::wake_line`), and disarmed after 60 s
(`DisarmWake`; a post that fails, at once). A wake is a nudge: the session's
pending messages are the truth. Its prompt hook (`turn_start`) delivers its
informs and wakes as `additionalContext`; a tool call's (`post_tool`)
delivers none (a primary is offered no steer). A hook asks the daemon ONCE
(the lean hook RPC, owner budget 2026-11-18: under 20 ms added per hook at
p95, `just hook-latency`): `DeliverHookContext` (additive, daemon.proto;
setup-application's `HookContexts`, the wiring's `DaemonHookContexts`, a
`Caller` of the hook's own session, `Caller::hook_context`), answered from
the projections with NO round awaited (`GetSession`'s `Hosting::fresh` is
not on the hook path): the pending messages of the urgencies the hook
names, fitted by `messages_context`, each in an element
`<message id=".." from=".." urgency="..">text</message>` with the text and
the attribute values XML-escaped (`delivery::escaped`), so a sender's text
cannot close its element or forge another (owner ruling 2026-11-19; `from` is the sender's name, `ctxloom` for a daemon notice told by its `notice:`/`drain:` id, `session_policies::is_daemon_message`, and `the user` only for a message the user sent), under
a header saying the elements hold untrusted data from another agent, not
instructions from the user (DRAFT), then what the session is owed (as
`RedeliverContext`) within what the messages left, joined by
`compose_context`; the ids the context carries are `HookContext.offered`
(`delivered`, field 2, is no longer set). The call is a READ that marks
nothing delivered (owner 2026-11-19: "we can tolerate duplicates, not
losses"; at-least-once, never marked delivered before it has been). The
hook prints the context, FLUSHES stdout, and only then sends
`AcknowledgeHookDelivery` (additive; `hook_cli::deliver` holds the order;
`Setup::acknowledge`, `HookContexts::acknowledge`,
`Caller::acknowledge_hook_delivery`), which records the ids
`DeliverMessages` (which redeems the wake its prompt was) and is
idempotent per id: the session aggregate keeps
`SessionState.pending_messages` (field 30, additive; `MessageSent` adds,
`MessagesDelivered` removes) and `DeliverMessages` records only ids still
pending. Claude gives no acknowledgement, so printed and flushed is the best
proof there is. A hook that dies before the ack leaves the messages pending,
offered again (the visible id lets the model ignore a repeat); two hooks
within one round may both carry one; an ack that fails is a repeat later,
said nowhere. Messages the budget leaves out stay pending (a first message
too long alone is still delivered cut and acknowledged; `session show` has
the whole). Forced in daemon-api's `hook_context` tests and hook-cli's
`deliver`. The hook's call trusts the discovery
file first, unprobed (`HookDaemons::trusting`,
`daemon::hook_daemon_unprobed`: a health check is a TLS connection of its
own); reached unreachable, it probes and starts one as every hook does.
Delivered messages are a primary's alone (a child's are fed as turns). A wake line with no message offered is blocked (its wake is still
acknowledged, so redeemed); a human's prompt never is. One hook answer has ONE 10,000-character context budget
(claude's cap), shared in order by setup-application's `compose_context`:
the skill mates line (the hook's own), then the daemon's part: the messages
(given what is left; past it they wait behind a pointer to `session show`),
then U13's re-delivered context. The
daemon publishes each watched session's `SessionSignal` every round (its
pending wakes, its outstanding wake) and repeats it every 5 s, so a gone
watcher is noticed. A primary takes `Wake` (never `Steer`) only while
watched (`session_policies::supported(session, wakeable)`, `GetSession`'s
`wakeable`); a wake to one nobody watches (a `--no-context` session has no
ctxloom server) is refused client-side, `daemon-api-wake-inform-only`.
`session show` says `wake: sent, not answered`. The wire is undocumented:
`just validate-wake-claude` (no gate) re-measures it on each claude bump;
on 2.1.325 a non-descendant's post is NOT refused. The post is the platform's
`os::socket::post_local` (a unix socket, each write within 5 s); on Windows,
whose endpoint is unmeasured, it is `Unsupported`, and the wake fails, said.

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
`apply_command`, OD-40, and a Confirm need, `needs`; the edit
judged as the write would judge it first; nothing written). At a terminal
the need is asked (owner ruling 2026-11-18, evening, as a removal's is;
its question `library-api::served_writes::swap_confirm`, a draft in the
10b spec's texts.md §3b) and a yes applies it with the answer and the
preview's etag; off a terminal or on a no it is the preview, printed
(exit 17 under `--detailed-exitcode`); an apply refused stale
(`library-tree-plan-stale`) is previewed again and ends in the refusal.
An edit that changes no engine, or loses nothing,
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
  `BrowseRemoteResponse` (the daemon's `BrowseRemote`, mapped by
  library-api's `deps::browsed`; a browsed bundle has no resource name), `remote
  discover` `DiscoverRemotesResponse`, `deps pull` `PullDependenciesResponse`
  (one `results` list by `State`, no status or counts: an empty pull is
  `{}`), `deps check [ref]` one `CheckDependenciesResponse` (rows by
  `DependencyCheck.State`; a whole check lists outdated and unchecked only),
  `deps upgrade` `UpgradeDependenciesResponse`, `deps hold|unhold` the
  Dependency (a local bundle, no dependency, `{"name": null}`), `bundle
  push` `PushBundleResponse` (`State` COMMITTED or PULL_REQUEST_OPENED) and
  `bundle move` `MoveBundleResponse` (a directory move gives its `path`, the
  bundle there `{"name": null}`: that project's name is not this one's to
  give). An unresolved name prints as JSON `null`, never left out (owner
  ruling 2026-11-14): pbjson-build prints nothing for an unset field, so the
  answer names the paths whose name always prints and
  `daemon_proto::with_unresolved_names` puts `null` (first, a name being
  field 1) where one is absent. A pin change is the shared `PinChange`
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
licence ships with it; what `fragment|command|bundle distill` asks the
model with, read through the catalog as init's interview is), in
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
is refused, `library-application-operational-prompt-missing` (exit 48),
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

## Distill (plan step 5, finishes M3; owner T30, S51, OD-38, OD-39)

A distilled form names its source: each `.distilled.md` records, in its
front matter as `source_sha256`, the SHA-256 of the `.md` it was distilled
from (`library::distill`, pure: `state`, `source_digest`, `distilled_file`,
`request`, `implausibly_short`). Every read hashes the `.md` again
(`ItemBody::source_digest`, `distilled_digest`, `distilled_state`): only a
match is FRESH and served where `config.content_form` prefers distilled
forms (`ItemRead::resolve`); a mismatch, or no digest (a form from before
T30, an inline document's, hard migrate), is STALE and its original is
served. No verdict is kept. A stale form preferred is said at every use, the
notice `library-application-stale-distilled` (a dry run, a launch, `context
assemble`, materialize, `fragment show --distilled`); `fragment|command
show` gives the state (json `distilled`: `NONE|FRESH|STALE`, text
`distilled: stale`), `bundle show` STALE, and doctor warns of each
(`DOCTOR-CHECK-STALE-DISTILLED-t1`, the library's `stale_distilled`). The
context assemble json's `notices` stay empty until T13 (they are said on
stderr), so atomic_writes.feature's two `notices[0].code` scenarios are red.

`fragment|command distill <ref> [-f]` and `bundle distill <bundle> [-f]`
are SERVED (step 10b, owner ruling 2026-11-18, evening): the DAEMON owns
the model call as a RUN, and the `.distilled.md` write FOLLOWS from the
run's outcome (daemon-api's `distill.rs`). `DistillFragment` /
`DistillCommand` (`force`, additive) plan the ask as a write's preview
(observed, over the recording library, nothing written):
`Library::item_distill_ask` answers an item marked `no_distill` (said,
nothing asked) or a FRESH form without `-f` at once (`applied` false,
`detail` `fresh`/`no_distill`, no run); otherwise the ask is
`ctxloom#prompts/distill` (read through the catalog under the project's
trust, ruling 128; the prompt's text is the owner's) with the body inside
`<content_to_compress>` tags, and the call hosts it as a ONE_SHOT on NO
CONTEXT (as `CreateRun` hosts one, `OneShots::create_reacting`) and
answers with the run's name (`run`, additive), which the CLI follows with
`WatchRun` (`Served::follow_run`, run-application's `client::followed`; a
signal heard asks the daemon to stop it, and the command exits 128 plus its
number, ruling 114). Its stops are heard from BEFORE the distill call
(`Served::hear_stops`, the wiring's `signals::CommandStops`, on a thread
and runtime of its own), so one that arrives while the daemon starts the
run is not taken by its default and is told to the follow as heard
already. The run REACTS
(`daemon_application::Reaction`, awaited in `Told::done` on the daemon's
runtime, before its watchers see its end; the core spawns nothing): no
answer is `library-application-distill-failed` (its cause the run's end);
an answer is written as a served write (`Library::item_distilled`, one
atomic Replace), under a request id KEYED by its run
(`sessions::command_id("distill:<run>")`), so the outcome told twice is
one write, a DUPLICATE, and against the etag the ask's preview answered
for the form's file, so a form edited meanwhile is refused stale
(`PlanStale`). Its plan reads the item again: a source whose digest moved
since the ask is `library-application-distill-source-moved` (typed
`LibraryDistillSourceMoved`, ABORTED, exit 48; its text a DRAFT), and an
answer under 2% of the source `library-application-distill-truncated`
(exit 32): nothing is written, so the form there before is kept. What the
write came to is the run's end, which the CLI says as the command's. The
reaction is in memory as the one-shot is: a daemon that dies during the
run loses it (the sweep's), and nothing follows. The write is made while
the daemon drains too (`Api::write_planned`: it follows from work already
taken), through the API as shared (`Api::shared`'s self handle). `bundle
distill` is one run per item, each its own reaction (the plan's U13 notes
do not decide the shape; this is the simpler one): its items are read
as `bundle show` reads them (`GetBundle` FULL), fragments then commands,
each distilled and followed before the next; the first failure fails the
command and what was distilled before it stays. json is the AIP
`DistillFragmentResponse`/`DistillCommandResponse` (`applied` true once
its run ended answered; library-api's `distill` builds them), and `bundle
distill` the `Bundle` with each item's state. The in-process path (the
`Distiller` port, `OneShotDistiller`, `item_distill`, `bundle_distill`)
is gone. There is no `work` domain and no provenance (owner, 2026-11-13: no
`distilled_in`, no `activity` label).

`run --no-context` (owner OD-38) is the user-visible context-free run, the
one-shot, the session and the dry run alike (`RunRequest::no_context`): the
plan composes nothing (no profile, fragment, premise index or companion
guidance) and carries no surface of the profiles' and NO MCP server,
ctxloom's own included (a server's instructions are context): the plan's
surfaces say `no_context` (`surface_ports::Inputs::no_context`), so claude's
`ctxloom-mcp.json` lists none (`--mcp-config` still names it) and none of
ctxloom's tools is laid into the agent's permissions (OD-4's allowance is
for a server offered), and opencode's `opencode.json` has no `mcp.ctxloom`. The capture hooks are the engine's and stay;
the agent is `--agent`, else the default agent where there is one, else
none (the default engine), and keeps its engine, model and permissions; the
statusline stays as configured. Beside `--profile`, `--fragment` or `--tag`
it is `library-application-no-context-exclusive` (exit 48).

## Hook capture: the transcript is the session's events

What the engine's hooks observe (a prompt, each MessageDisplay delta, a tool
call as size and SHA-256 only, a turn's end without text, a compaction, a
clear, the conversation a session start names, and a gap where a hook
dropped one) is a typed fact of the SESSION stream (`proto/ctxloom/sessions/v1/
capture.proto`; `runs-application::observe` makes them from the unified
`HookEvent`; a hook delivers them through a `run-ports::FactSink`, or HOLDS
them for later, below, and never fails its engine). The
session aggregate records each on a live session, nothing on any other, and a
redelivery is DUPLICATE. No file holds the transcript (owner ruling 87a):
`session-index` folds it from the stream's pages on each read (a
`Projection`, D27's envelope `v, harp, session_id, engine, seq, ts, kind`,
`seq` counting lines), so it is live: a session still running shows what its
hooks have delivered. A one-shot child's prompt and words come from its hooks ALONE (owner
rulings 26 and 51): the launcher records none of them, and reads the child's
stream only for the reply it returns and the run's outcome facts (RecordRunnerStarted,
RecordTurnCompleted/RecordTurnFailed, RecordLaunchFailed, RecordRunExited). Accepted costs: a hook
delivery that is dropped (on the environment's endpoint, or past the hold's
bound) loses that part of the record, and a child whose hooks
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
A hook is a separate process, and how it delivers is `ctxloom-wiring`'s
hook arm (`sessions.rs`, slice 4 §3.8, S16, OD-13). (i) Its environment
names an endpoint (`CTXLOOM_ENDPOINT`: no host run sets it since U7, kept
for M10b's containers): `HookSink` is `hook-sink-grpc`'s
`GrpcSink` there with `CTXLOOM_BEARER`, pinning `CTXLOOM_TLS_CERT` as its
only root, when they are set and well formed (a missing or garbled
certificate is `HookSink::Refused`, never plaintext), else `Unreachable`;
through `Capture`, failing open: a delivery that did not happen is dropped
and said, `hook-cli-not-recorded` for each observation. Such a hook never
spawns a daemon and never holds (a container's hold would sit in the
container). (ii) Otherwise it is `HoldingRecording` (s4-Q14, unit H2): it
reaches the user daemon its discovery file names, read at each hook, and
starts one when none serves (`daemon::hook_daemon`, `ensure_within`), all
within its bound, `HOOK_BOUND` (10 s) or the engine's `delivery_budget`
where that is shorter (`capture::hook_bound`; the spawn wait is what the
bound leaves `DELIVERY_MARGIN` of, at most `SPAWN_WAIT`), presenting the USER bearer that file holds
(`capture::daemon_sink`, `run_ports::Bearer::user`), never its run's; then
`runs_application::Holding` decides, over fact-spool's `Spool` at its
ctxloom home and the hook's `Deadline` (`HookClock`; the core reads no
clock). It HOLDS every observation (`hook-cli-held`, its `why`) when no
daemon was reached or started (H-Q2) or the daemon is older than the hook
(`!run_ports::up_to_date(daemon, own)`, H-Q1; the hook's stamp is the
binary's, a test's `CTXLOOM_TEST_STAMP` under test-support, and so is a
`daemon serve`'s). Up to date, it first delivers what its session's hold
holds (owner, H-Q5 revised: the client side flushes, never the daemon):
`take` (none: another process is taking it), each record in held order the
daemon is up to date for, through the same sink, each waiting only what is
left of the bound, then the delivered prefix cut, again (at most
`TAKE_ROUNDS`, 8) until nothing it can deliver is left; a record held by a
newer hook is kept and passed over, a gap record is delivered as the gap
fact in its place (below), a record whose
external id is not its book's origin or whose book is not this session's
stream is dropped and said. Then its own facts are delivered. A hold
another process is taking, a held delivery left unsettled, or the bound
spent leaves the rest held and holds the hook's own facts BEHIND it
(`hook-cli-held-behind`), so a session's facts reach its stream first-in,
first-out; an own delivery left unsettled is held and the rest behind it.
A final answer (`Refused`, `Parked`) is never held (said, not recorded).
A hold at its bound, or one that cannot be used, drops the fact, said as
`hook-cli-not-recorded`. The wording of `hook-cli-held` and
`-held-behind` is a draft, the owner's to approve (H-Q10). A COMMAND
flushes too (unit H3, `ctxloom-wiring`'s `daemon::flush`): any ctxloom
command that reaches the daemon (`daemon::Reach`, its `serving` and
`reach`) first delivers what EVERY session's hold under its home holds,
once a process (`on_the_way_in`), through the daemon's listener with the
user bearer (`capture::daemon_sink`), the same `runs_application::Flush`
as a hook's (`Flush::every`, sessions in the order `HeldFacts::sessions`
lists them): only when the command is not older than the daemon
(`runs_application::command_flushes`), each hold taken at once or skipped
(`Left::Taken`, never waited for), within `FLUSH_BOUND` (5 s: a full hold of ten facts, one TLS handshake each, fits a slow host; owner ruling 2026-11-21) for the whole
flush, a hold the bound did not reach left untouched (`Left::OutOfTime`);
what it could not deliver stays held. Nothing a flush meets is dropped
silently (owner, H3 follow-up): a held record the daemon finally refuses or
parks, or one addressed to a session the daemon has no record of, or one
that is no fact of its hold's session, goes to the hold's set-aside part, as corruption does (run-ports' `Fate::SetAside`,
`Batch::settle_fates`; fact-spool writes its frames as the next
`<root>.held.corrupt-<n>`), a hook's flush and a command's alike; a command
says it ONCE a session as it ends, `ctxloom-wiring-held-set-aside` (the
session's harp, how many, each reason once; `daemon::flush::take_said`,
said by `Commands::run`; the wording a draft, the owner's to approve).
A capture fact addressed to a session the daemon has no record of (its
stream holds none: never minted, or destroyed) records nothing, as ever,
but is never answered "applied" (owner ruling, H3): the daemon's listener
(`runs_application::EdgeCommands`, by `runs_application::no_such_session`:
a capture fact applied with no event) answers it NOT_FOUND under the key
`run_ports::NO_SUCH_SESSION`, the full name of capture.proto's
`FactSessionUnknown` (a fact is never refused, so no `(refusal_status)`
declares it), which hook-sink-grpc reads as `Delivery::NoSuchSession`
(final; the in-process `EdgeSink` answers it alike); a hook says it, a
flush sets it aside, its reason "the daemon has no record of this
session". `session list|show|search` and `session transcript show` are daemon reads
(`daemon_api::SessionReads`, `get_transcript` over `GetTranscript`), so a
read includes what was held. Every
session's, not only its project's: the daemon and its one store serve the
home, and a hold names its session only inside its records. The `daemon`
noun's own `status`, `stop` and `resolve` deliver none (`Reach::observing`:
they look and change nothing). The frontend (slice 4 U5, `ctxloom-wiring`'s
`frontend.rs`) flushes at its start (its reach's flush on the way in) and
again once its exit is reported (`daemon::flush::flush_holds(&wiring,
stamp)`: the daemon serving the home, none started; what it set aside said
as the command ends). `ctxloom shutdown` flushes them too, and declines
while one is left with a live record (SHUTDOWN, flush before install).
The daemon never reads
a hold, with ONE named exception (OD-59 (2), owner 2026-11-16, "ALLOW the
check"): its START-UP sweep, before it judges a run (the runs owed a runner
and the runs started), delivers that run's session's hold
(`daemon_application::StartHolds`, the wiring's `daemon::flush::StartFlush`,
`Daemon::flushing_at_start`), so a TUI frontend that exited while no daemon
served (its exit report held) has its run ended as the report says (its
exit code, `RunnerExit`; the sweep's re-read finds the run ended and leaves
it to the adoption's end path: incarnation `runner-exit`, checkout let go),
never `runner-lost`. It is the client side's own flush of one session
(`runs_application::Flush::session`, through the daemon's in-process
`EdgeSink`, within `FLUSH_BOUND`), no second reader of a record; its read
models are caught up first, so a "no such session" is true. What it cannot
deliver (a record of a newer ctxloom, a hold in a format this one cannot
read, one another process takes, one set aside) leaves the run to the sweep
as before, said in the log (`ended runner-lost without what its hold held:
<why>`). Nothing else of the daemon reads a hold: not its rounds, not its
API. A hook that holds because its daemon is older only holds: it asks
no daemon to drain (H-Q6's nudge is gone, OD-62).
A GAP (the owner: a gap is delivered into the transcript) is a fact a
hold dropped past its bound: its gap record is delivered, in its place in
the order, as `RecordCaptureGap` (`capture.proto`: the dropped fact's
external id, `why`, `dropped_at` the hook's clock, the hook's stamp;
external id `hook:gap:<the dropped fact's external id>`,
`Observation::gap`), recorded as `CaptureGapped` on a live session, so the
session's record says for good which fact is missing there, and when; the
transcript's line is `kind: gap`, `gap: {facts: 1, dropped, why,
dropped_at}`, and its text `gap: 1 fact not recorded (<id>, dropped at
<time>: <why>)` (the wording a draft, the owner's to approve). The in-process `EdgeSink` is for tests and the run's own
facts, and the daemon's start-up flush (above). A unit test's executable never spawns itself as the daemon
(`SPAWNS_ITSELF`). The session's end is NOT captured (owner 2026-11-15):
it is its run's end (`IncarnationEnded`) and its lock's verdict.
`RecordSessionEnded` is gone; `SessionEnded`, in every interactive stream
recorded before, is RETIRED in place (angzarr refuses an unknown stored
type): never emitted, folded to nothing and rendered nothing (no line, no
`seq`). A SessionEnd an older home still fires is answered and observes
nothing. A clear (claude's `/clear`: SessionStart `source: clear`) is
`RecordConversationCleared{native_session, trigger}` (`ConversationCleared`,
no content; `hook:clear:<session>:<the new conversation>`, so a daemon
child's driver reporting the same clear is one fact), then the new
conversation's bind; its transcript line is `kind: clear` (`native_session`,
`trigger`), the text transcript showing neither a clear nor a compaction.
The bind is not a capture fact to `no_such_session` (it records nothing on a
session already bound to it).
A compaction's external id is `hook:compact:<session>:<prompt_id>:<trigger>:
<phase>`, so two compactions of one turn (a manual and an auto) are two facts;
two of the SAME trigger in one turn would still collide (the payload offers no
further discriminator). The listener is a port, not a file, and the
daemon's (no run serves one, slice 4 U7): a run killed by a timeout leaves
nothing behind, and one asked to stop by SIGHUP, SIGINT or SIGTERM stops its
engine and records its end before it exits (ruling 114, above). A SIGKILL is not heard: the engine
outlives it.

A session's HOLD (s4-Q14, unit H1; a hook holds there and flushes it since
H2, above) keeps capture facts
in order for a ctxloom process of an up-to-date version to deliver (owner:
the client side pushes a hold over the API; the daemon reads no hold file).
"Up to date" is semver not older, `run_ports::up_to_date(daemon, fact) =
!newer(fact, daemon)`, over the ONE `run_ports::newer` the skew rule uses
too (`daemon_api::newer` re-exports it). The port is run-ports'
`HeldFacts` (`hold`, `is_empty`, `sessions`, `take`, `discard`, `survey`;
`Batch::cut`/`settle`); the adapter is `adapters/driven/fact-spool`'s
`Spool`: `<home>/.ctxloom/held/<session root>.held` (`run_ports::HELD_DIR`),
the directory made and tightened 0700 and each hold 0600 at every use (on
Windows no mode: the ACL, M8), a mode not set failing the call
(`run-ports-hold-restrict`). A hold is the magic `CTXHELD\x01`, then records
of a u32-LE length, a u32-LE CRC-32 (crc32fast) and one `ctxloom.v1.HeldRecord`
(`proto/ctxloom/v1/held.proto`: a `HeldFact`, the hook's stamp, the session's
name, the external id and the `CommandBook`'s exact bytes, and (field 6,
`incarnation`) for a frontend's report the incarnation whose RUN stream its
book is on, 0 for the session's own stream; a flush delivers a run fact only
on the run root `runs::run_root(session, incarnation)`, else sets it aside;
or a `HeldGap`);
`HeldFact`'s `Debug` withholds the book. Bound: 32 MiB a session and 256 MiB
a home (`SESSION_BOUND`, `HOME_BOUND`); past either the NEWEST fact is
dropped (`Held::Full`, for the caller to say on stderr) and a gap record
written in its place, gaps allowed `GAP_SLACK` (64 KiB) past the bound;
earlier facts are kept. Two `file-lock` locks a session: `<root>.lock`, the
short append lock (a hook's wait `APPEND_WAIT`, 1 s; the wait, or its
pause, is `Spool::waiting`/`pausing`, the seam tests force a held lock
through), and `<root>.flush.lock`, held by a `take` until its batch is
settled or dropped and NEVER waited for, so of several processes taking one
hold exactly one gets the batch and the others `None`. A settle rewrites
the hold to what was kept plus what was appended since its snapshot, only
while the hold still begins with the snapshot's bytes (a discard
meanwhile: `Settled::Replaced`, left alone); a batch dropped unsettled (a
crash between delivering and cutting) changes nothing, and its facts are
delivered again, each a DUPLICATE. A torn tail is cut by the next append or
take, never set aside; corruption (a length past the longest record, a bad
CRC, a payload that is no record) keeps the valid prefix and sets the rest
aside as `<root>.held.corrupt-<n>` (0600, never parsed again); a hold of
another format is never touched (`run-ports-hold-format`). No record is
fsync'd. The property tests use a seeded generator of the test's own (no
property crate is in the lock).

What reclaims a hold (s4-Q14, unit H4; owner H-Q7 (a), H-Q8 (a)): a
session's DESTROY, and nothing else. The hold is the client's, so the
command that asked the daemon for the destroy (`session remove`, `session
sweep`) reads this home's holds by the session's resource name
(`reclaim_application::Holds` over the `Spool`, `SessionHolds`,
`destroys_held`) once the daemon answers: each session it destroys or
plans to that holds anything warns first, the notice
`sessions-application-destroys-held` (its facts and set-aside parts; never
a refusal), and an applied destroy then discards the hold and its
set-aside parts; a refused removal touches no hold; a hold that cannot be
read or discarded fails the command (32) after the daemon's answer is
printed. A downgrade's facts (of a newer ctxloom) stay held until a
reinstall or the destroy. Doctor reports every hold,
`DOCTOR-CHECK-HELD-FACTS-h1` (doctor-checks' `held.rs`, wired by
`Doctor::reporting_holds` with this binary's own stamp): `ok` when nothing
is held, `info` for facts a daemon of their version takes held up to an
hour (`STALE_AFTER_SECONDS`), `warn` for one held longer, of a newer
ctxloom, with gaps, set-aside parts, an unreadable format or a bad tail,
or holds that cannot be read; each hold named by its harp (else its root)
with its count, size, stamps and oldest instant; `--fix` removes none.
`ctxloom clean` never takes a hold (it is no cache).

## No path dependency leaves the repository

A tracked `Cargo.toml` or `.cargo/*.toml` never holds a dependency, `[patch]`
or `[replace]` `path` that is absolute or climbs out of the repository root,
and `.cargo/config.toml` and `.cargo/local-pins.toml` are never tracked:
local sibling checkouts live only in the gitignored `.cargo/local-pins.toml`
(`cargo local-pins`). `scripts/path-deps-check.sh` (`just
lint-path-deps`, part of `just lint`, and lefthook's pre-commit `path-deps`
gate, third in order) judges the staged index; its self-test is in `just
selftest`.

## ctxloom's own MCP server (daemon slice 3, T20; OD-1 to OD-7)

`ctxloom mcp serve` (hidden; `adapters/driving/mcp-server`, on rmcp pinned
`=3.9.0`, default features off, `server` and `transport-io` only: ruling 150;
3.9.0 because cargo-deny refuses 3.8.0's advisory, and it brings sha2 0.11
and its family into the lock) is the runner's callback into the daemon.
Every session home registers it, but for a launch with no context (`run
--no-context`, OD-38; the Distill section): claude's `ctxloom-mcp.json` as
`"ctxloom": {"command": <program>, "args": ["mcp", "serve"]}`, opencode's
`opencode.json` as `mcp.ctxloom = {type: local, command: [<program>, "mcp",
"serve"], enabled: true}` (`surface_ports::CTXLOOM_SERVER`; a carried server
of that name is shadowed, OD-6). The entry holds no identity: the server is
the session `CTXLOOM_SESSION` names in the environment it inherits from its
engine (OD-7's live probe, `scripts/mcp-env-probe.sh`, run by the owner
2026-11-14 on claude 2.1.322: claude passes its environment, withholding
its own `CLAUDE_CODE_OAUTH_TOKEN`; the probe gives claude, the server and
the daemon its call starts a temporary `CTXLOOM_HOME`, `HOME` and project
of its own and stops that daemon before removing them, held by
`scripts/mcp-env-probe.selftest.sh` in `just selftest`), and with none it is
refused, `mcp-server-no-session` (exit 48). `initialize` and `tools/list`
reach no daemon. The command line holds stdout locked, so the server's
lines reach it through a channel and are written on the command's thread
(`mcp_server::serve(server, out)`). Its tools (`mcp_server::TOOL_NAMES`)
are `session_list`, `session_show`, `transcript_show`, `context_assemble`
(its `bundles` argument is `AssembleContext`'s `fragments`), `child_launch`
(slice 4 U3, s4-Q1: `name`, `agent`, `task`, all optional; the session's
own child, as `session launch` is the user's; its description the owner's
approved text, which tells the engine to give a child its task and to
prefer such children to its own subagents), `child_send`, `child_close` and `child_clear`, each
described in the owner's approved words (2026-11-14), one Markdown file per
tool beside the crate (`mcp-server/tools/<tool>.md`), embedded by
`include_str!` at compile time and offered without the file's closing
newline (nothing is read at run time). `approval_prompt` (OD-1) is not
offered until it is built. Each tool is
a call of `daemon_api::Caller`, a SESSION as the daemon's client. Its
project is its name's (`projects/{p}`, OD-2), never resolved from a cwd.
Its reads are the service's (`GetTranscript` and `AssembleContext` are now
served: the transcript from the warm index, refused `TranscriptEmpty` with
no lines; the assembly at the project's REGISTERED root in the caller's
environment, a library read (step 10a U2), its fragments named, a missing
fragment or tag a notice the tool's edge adds). Its writes are the domain's commands through the coordinator's
`HandleSync` (`DaemonApi::handle_sync`, `daemon-client` over angzarr's
coordinator client), its `requester` the caller (project uid from
`GetProject`, its own stable id), its evidence from the target as the daemon
has it (s3-Q4), its command id keyed by an id minted once per call (a
message's, a clear's; a close's `close:<uid>`), so the one retry on a new
daemon is a DUPLICATE. The answer is the session-index projection, or
`GetSession` when none came back. A session's call never ends what a
daemon hosts (OD-3, owner 2026-11-13/14; OD-62, 2026-11-15;
`Reach::never_stopping`, `daemon_api::reconcile_session`): meeting an IDLE
daemon of an OLDER version (`daemon_api::newer`: the stamps'
`MAJOR.MINOR.PATCH[-pre]` by semver precedence; the hash and a dirty
build's time order nothing, so two builds of one version are neither), it
asks it the plain stop (`StopDaemon`, never `even_if_live` and never a
drain: its loop ticks no new child, its discovery is withdrawn, and its
listener's stop answers the calls in flight within `STOP_GRACE`), waits it
out, starts its own and is answered there; a busy one (or one that refuses
the stop for its runs, `DaemonHosting`), or one of an older or the same
version, refuses the call `daemon-api-version-skew`, naming both stamps,
and is asked no drain. Under test-support, a
`test-die-after-apply` file in the daemon's directory kills the daemon once
a client's `HandleSync` is applied and before it answers
(`serve.rs`'s `Dying`, taken by the daemon that finds it), and `ctxloom test
session-events <harp>` lists a session's events.

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
codec maps `PreCompact|PostCompact` onto them). opencode says nothing of a
session's end, and nothing records one (owner 2026-11-15: its end is its
run's); its `/new` (a clear) is not captured yet (unprobed).
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
`runtime`, `env`. `session_home` is the AGENT's again (owner ruling of
2026-11-13, superseding ruling 44(b) for this key; hard migrate, no alias,
the key is `session_home` at both levels): `agents.<n>.session_home`
resolves agent > engine > default (`session`), like `runtime`, kept under
`run --engine`, each value with its layer (`agent`, `engine`, `default`) in
the dry run's `settings.session_home` and Settings row, `agent show`'s
`resolved.session_home`, `engine show`'s agents, and an `Agent`'s own
`session_home` (the binding's word); `agent create|edit --session-home
<session|host>` writes it (empty clears it; another word is
`library-application-write-unknown-session-home`). Agent writes go to the
project config unless `--home` (vocabulary §4's spelling) sends them to the
home config, and each layer takes the values valid for it (owner ruling of
2026-11-14): `session` anywhere, `host` only on a home write, where it
takes effect; a project write of `host` is refused,
`library-application-write-session-home-host-in-project` (exit 48, its fix
the same write with `--home`), and writes nothing. A binding replaces a
lower layer's whole, so an agent the project also binds says `host` only
once the project's binding is gone. `approval_timeout` (OD-41, D16, step 8
U3) is how long an ask of the engine's child waits for an answer, the
engine's alone (no agent says it; a project layer may): a span
(`schema::engine_block`'s `Pat::Span`, rust-support's `duration-text`, as
`daemon.drain_timeout`) or `now` (zero: every ask denied at once), unsaid
`5m` (`engines::APPROVAL_TIMEOUT_DEFAULT`, 300 s; one that does not read,
under `--degraded`, is the default too). `Effective::approval_timeout` holds
it as written with its layer (`engine` or `default`), shown by `engine show`
(text and the `Engine`'s field 8) and the dry run's Settings and
`settings.approval_timeout` (`AgentSettings` field 7); `Drive`'s and
`run_ports::EngineSettings`'s `approval_timeout` is the span, read with the
plan at every launch and re-host (T21) into run-application's `HostedChild`
(its `settings`). On claude a child's turn (a hosted child's or a
one-shot's, never the terminal's) is given `MCP_TOOL_TIMEOUT`, the timeout
plus 60 s in milliseconds, only when the timeout is over 300 s (owner ruling
Q12; `engine_claude::mcp_tool_timeout`), set in `drive::environment`, so it
is owned (ruling 78b: a permit for it is ignored); at 300 s or under it is
not set, and a permit may give one. `docs/config/permissions.md` is the page.
Its asks reach ctxloom's approval prompt (U4's tool, U6's
`--permission-prompt-tool`). `auth` is THE auth setting: `none`, `login` (the
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
(its `approver`, `approval_timeout`, `sandbox`, `network`) is an unknown key
(an ask's wait is the engine block's `approval_timeout`, above).
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
say it under the document too). Text and JSON print the document with its
keys sorted (E3, owner ruling 2026-11-17: they are drawn from the daemon's
message, a `google.protobuf.Value`, which keeps no written order); what
reaches the engine is still the document verbatim, in its written order; no
flat model/effort, no `approver`, no "Headless posture".
`engines.<e>.command`, `auth.file` and `session_home: host` (an engine
block's or an agent's, `agents.<a>.session_home`) in a project layer are
held back until the user trusts the project (`strip_project_only`, ruling
46; the notice `library-application-notice-untrusted-setting`, U7).
An agent's references are resource names (design §2, B2's references): its
`engine` `<parent>/engines/<e>`, each of its `profiles` B1's profile name
(`library-cli`'s `catalog::profile_ref`; one that names no place keeps its
text, OQ-16), and `resolved.engine` the engine's. So are the dry run's
`Run`'s agent (`<parent>/agents/<a>`), engine and profiles, their
configured text where the project has no name yet.
init's no-token notice is `setup-cli-token-not-exported` (`CliFault::TokenNotExported`, args `variables`, `export`, `command`; `-fix` in the same catalog), a code a script can key on: the json warning line carries `code`, `warning` and `remedy` as every notice.
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
(refused, exit 48), with no alias, reader, migration or warning.

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
| (new, owner 2026-11-15, OD-62) | `daemon.drain_timeout: <span>\|now` (home config only; DRAIN) | `5m` |

`config.premised` says when a fragment with a premise loads (owner ruling of
2026-11-12; `library::compose::Premised`, read by `Config::premised`):
`on-demand` holds it back (unless the caller named it) and ends the
assembled context with the premise index of what was held back
(`library::premise::render`, each name once, sorted), so `context
assemble`, the dry run, `run` and materialize carry the index; `always`
loads every fragment with the rest and renders no index (the static
assembly). The index's instruction (`library::premise::
selection_instruction_on`) is the owner's Go text but for its last clause,
which follows the surface the context rides (s3-Q5, owner-approved
2026-11-14; `premise::Surface`, `compose::Options::surface`): on a LAUNCH
(the run's plan, a dry run's too, and the daemon's assembly for a session's
`context_assemble` tool, `AssembleRequest::surface`), where ctxloom's server
is offered, "Then for each name you chose, call the ctxloom
`context_assemble` tool with those names as its `bundles` argument."; AT
REST (materialize, `context assemble`, `fragment premises`, `agent show`),
"run `ctxloom fragment show <name>` for each name you chose" (a run with no
context carries no index at all); `fragment show` reads a companion's fragment
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
(`output.dir` relative to the project root unless absolute), a hosted
child's too, under its own harp in the project it is minted in (owner
ruling 2026-11-16), no longer
`~/Documents/ctxloom/<basename>/<harp>`; nothing gitignores them, and a
harp never changes, so nothing moves the folder (S55). A first init at a terminal asks for `output.dir`
before the interview (suggesting `ctxloom/`, saying outputs are committable)
and writes the answer; then, in the same style, whether conditional guidance
loads only when it applies or always (`init_application::PREMISED_QUESTION`,
suggesting `on-demand`; an answer that is neither word is asked again, at
most three times, then the suggestion stands), and writes it as
`config.premised`. Then it offers the companions (M4): an `Add the <n>
companion?` `[Y/n]` for each shipped companion whose program is on her
`PATH`, then `Add companions by name?`; each accepted or named one is
registered by the apply as `companion add` registers it (the daemon's
`CreateCompanion`, from the apply's own answers by
`init_application::accepted_companions`), one whose program is not there
said and skipped (`init_application::companion_questions`: the questions out
and the answers in as data, D2; the terminal is setup-cli's alone). Off a terminal none
is asked and nothing is written. The interview's opening is the setup
interview, then each enabled companion's `init.setup_guidance` as it wrote
it, each after the assembly's `---` separator (`Prompts::companion_guidance`,
j000300; no words of ctxloom's are added).

`legacy.go` (owner ruling 162) says whether Go ctxloom still runs on this
machine beside this one; both write hook entries into the user's shared
`~/.claude/settings.json`. It is the HOME config's alone: a project layer that
says it is refused (`library::config::schema::HOME_ONLY` and
`take_home_only`, a `breaks the schema` violation naming the home config,
exit 48; under `--degraded` it is said and dropped, never read). Doctor's
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
(`EnqueueRun` field 13 `posture` and `LaunchRefusals` field 6
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
`just lint-os` (`just lint-api`, api-linter over the API protos, is not
yet a gate: see "The pins"); `just test-pkg <crate>...` is the quick loop, `just fmt` and
`just fmt-check` cover this workspace's crates only, `just run` runs the
binary, `just clean-target` is the only way to drop this checkout's
`target/` (`just trim-targets`, below, sweeps the pool's by age), and `just
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

Agents work in a POOL of three long-lived worktrees, each with a warm
`target/` (owner ruling of 2026-11-16): `~/workspace/worktrees/ctxloom--slot-1`
to `-3` of the user's own home (the justfile's `slot_root`, `slot_prefix`,
`slot_count`; `CTXLOOM_SLOT_ROOT` replaces the root). Departure from the
worktree layout rule (`~/workspace/worktrees/<project>--<branch-slug>`): a
slot holds many branches in turn, so it is named by its number, not a
branch. `just slot lease <branch> [--from <base>] [--wait <secs>] [--by
<who>]` (`scripts/slot.sh`) takes the first free, clean slot (one already
made before one not yet made, which `git worktree add` makes), switches it
to the branch (made from `<base>` when neither it nor `origin/<branch>`
exists) and prints its path alone on stdout; with every slot leased it says
each one's holder and refuses, or with `--wait` retries until the time is
up. A free slot holding uncommitted work (a crashed holder's) or a detached
HEAD no ref holds is passed over, and said. The lease is an advisory
`flock` on `<git common dir>/ctxloom-slots/slot-<n>.lock` held by a holder
process of its own session that lives exactly as long as the lease's OWNER
(the first ancestor not a shell or `just`: an agent's Claude Code process;
`--holder <pid>` names another), checked by pid and start time, so a
crashed agent's slot frees itself; `slot-<n>.lease` beside it says who,
which branch and since when. Limit: subagents of one Claude Code process
share its pid, so one of them that crashes keeps its lease until the
process ends or it is released. `just slot release [<slot>]` (default: the
slot `just` was run from) refuses a slot holding a tracked change or an
untracked file, or whose HEAD is in no remote-tracking ref (unless
`--allow-unpushed`), then detaches its HEAD (the branch is free for any
checkout; files and `target/` stay warm), lets the lease go and sweeps the
slot's `target/` (`--no-sweep` skips it). `just slot list` says each slot:
leased (by whom, branch, since), free (where, clean or DIRTY, the last
lease) or absent. A switch rebuilds only the crates the branch changed (measured
2026-11-16, `just build` plus `test-pkg run-launcher sessions
ctxloom-wiring`: 133 s cold, 102 s on a switch to a 32-commit branch, 35 s
back; `target/` 8.4G, +5.6G, +1G; a sweep of the 15G after aging took 2 s
and left 4.7G, the next build still fresh). The
gates' own trees (the staged tree, the ratchet base, the mutants batch)
keep their own target directories. `just trim-targets [--dry-run] [--days
<n>] [<checkout>...]` (`scripts/trim-targets.sh`; no checkout: the slots)
sweeps BY AGE what no build has used for `<n>` days (default 2): every unit
whose fingerprint no build has read for that long, with its artifacts, by
the released cargo-sweep pinned at `cargo_sweep_version` (0.8.0, `cargo
install --locked cargo-sweep@0.8.0`; another version fails, naming the
install line), and every rustc incremental directory not written for that
long. A unit goes with its fingerprint, so cargo just rebuilds it. "Used"
is the access time cargo's fingerprint read renews, about daily on a
`relatime` mount (/home's); a `noatime` mount fails and removes nothing. A
checkout whose cargo holds a `.cargo-lock` is kept, and said. Both are
self-tested in `just selftest` (`scripts/slot.selftest.sh`,
`scripts/trim-targets.selftest.sh`, which builds a fixture workspace and
needs cargo-sweep; CI's Linux job installs it). No compiler cache is used,
and `CARGO_INCREMENTAL` is cargo's default (owner ruling of 2026-11-16,
after measuring: released sccache cannot share a workspace crate's compile
between worktree paths), but in the ratchet base's tree, built with it off
(below).

nextest's committed config is `.config/nextest.toml` (workspace-wide): each
test in a process of its own; 12 tests at once (`test-threads`, measured on
this 20-core shared host; CI's workflows keep one a core,
`NEXTEST_TEST_THREADS: num-cpus`), each feature's scenarios 2 at once
(`SCENARIOS_AT_ONCE`; `GHERKIN_SCENARIOS_AT_ONCE=<n>` overrides it, read
once, to measure); SLOW at 30 s and killed at 120 s
(`terminate-after`; the longest unit test takes about 5 s), except the
feature runner's slowest features, below; a test that leaves a
child holding its stdout or stderr past 200 ms FAILS (`leak-timeout`
`result = "fail"`); retries 0 (a retry would pass a flake, and a flake is a
finding); the `ports` test group runs the tests that bind a port, or free
one and dial it, one at a time across binaries (hook-listener,
hook-sink-grpc, daemon-client, daemon-api-grpc, forge-github, the wiring's
`capture`, the binary's `tests::hook`); and the setup script above. A
second setup script, `guard-homes` (`scripts/nextest-guard-homes.sh`, every
test on every platform; OD-34: a test once kept the shell's CTXLOOM_HOME and
registered 85 temporary projects in the owner's real home), names
CTXLOOM_HOME, CLAUDE_CONFIG_DIR, OPENCODE_CONFIG_DIR and the four XDG bases
as one FILE, `target/test-guard/inherited-home`, so a test that keeps one
from the process and writes under it fails (ENOTDIR) instead of writing
into the caller's; the wiring's
`test_the_run_inherits_no_home_a_test_could_write_into` holds the run to
it. A test process holds a LIFELINE for its life (2026-11-18, test-support
only): `tests::lifeline()`, an advisory lock on
`<temp>/ctxloom-test-lifelines/<pid>-<nanos>.lock` (one whose lock is free
is removed when the next is made), named to every `ctxloom` it spawns as
`CTXLOOM_TEST_LIFELINE` (`workspace.test.rs` holds every spawner with a
cleared environment to it) and passed on by a client to a daemon it
spawns. A test-support daemon tries the lock each round
(`serve::Lifeline`); once it can take it, its test is gone (a test killed
by nextest's timeout drops no TempDir, and its detached daemon, kept busy
by a child mid-turn, never idled out: six were found hours old), so it
says `its test is gone: ending what it hosts`, ends them as a stop even if
live does, and exits. The kernel lets the lock go however the test ended,
and a reused pid names no lock. A plain build reads no such variable. The
forcing test is `a_daemon_whose_test_is_killed_ends_what_it_hosts_and_exits`
(its lifeline held by a process of the scenario's own, killed outright).
The wiring's tests read the process environment through
`host_snapshot()` (which drops those names, as `Env` unsets them), under
the `ENV_LOCK` its `Env` tests hold while they change it. `_tests`
exits with codes of its own, never nextest's passed through (nextest's 102
is "cargo metadata failed"): 0 passed, 100 a test failed, 101 not built, 120
a doc test failed, 121 nextest failed otherwise (`nextest exited <code>` in
the log); 100 and 120 are red tests, 101 and 121 are refused on every
branch (`scripts/tests-exit.selftest.sh`). `test-staged` runs
`_tests-checked`, which after a 100 or 120 asks `gates/red-sanity.sh` (owner,
2026-11-19, R0 and R1) whether the red is tests' or the environment's, and
exits 122 (refused on every branch, never red; not 121, which is the
runner's) when it is the environment's: R0, the staged worktree, its target
directory or a program the setup script named (`CTXLOOM_TEST_BINARY`,
`CTXLOOM_TEST_COMPANION_STUB`, listed in the target directory's
`nextest-binaries.list`) is gone after the run; R1, over 100 failures, half
or more of the run, in packages that have a `*.test.rs` at HEAD (a red-first
new crate is exempt; an unreadable Summary or a failure in no package is
refused). Accepted: a deliberate commit breaking over 100 existing tests at
once is refused, and a gross but sub-threshold break (101 of 5,000) passes
the commit gate for the land ratchet to catch
(`scripts/red-commit-sanity.selftest.sh`).

The feature runner is ONE TEST PER FEATURE FILE (2026-11-18, superseding
the clusters of the owner's ruling of 2026-11-14, whose
`library_agent_changes` timed out at 90 s under load), named by its path:
`gherkin_features.test.rs`'s `features` module, a module per directory and
a test per file, so `tests::gherkin_features::features::cli::agent` runs
`cli/agent.feature` (`feature_of_test`, `test_of_feature`). nextest runs them at
once, as many as it runs tests, each in a process of its own, and a
filterset selects one. A new feature file is listed in its directory's
module (`every_feature_file_has_a_test_of_its_own` refuses one left out,
and a test naming no file). Within one cucumber run a feature's scenarios
take turns (their steps block cucumber's one executor thread), so
`gherkin_common::run_feature` runs each scenario of a feature as a run of
its own (`scenario_ids`, then `run_only` with its id, as
`GHERKIN_SCENARIO` does), `SCENARIOS_AT_ONCE` (3) at once on threads of
their own, their outcomes put together in the file's order; an id whose
own run ran nothing is red, never left out. Under `GHERKIN_TIMINGS`, and
for a feature whose scenarios cannot be listed (a parse error, reported as
before), the feature is one run. The forcing test is
`a_features_scenarios_run_at_once` (two scenarios that each hold until
both daemons serve). The run lasts at least its slowest feature
(cli/daemon.feature). A feature that would not fit the 120 s has a
`terminate-after` of its own, an override naming it in
`.config/nextest.toml`, 4 times (not the clusters' 2.4: one at a time,
daemon.feature took 3.5 times as long at load 50 as at 20) the longer of
two whole-workspace runs on 2026-11-18 (20 cores, load 18 to 28; the table
is there), whole 30 s periods: `daemon` and `run` 180 s, `agent`,
`session_approvals` and `library_changes` 150 s; every other feature
(28.7 s at most) keeps the 120 s. Windows keeps the limits measured for
the clusters, each given to the features that were in it (a feature never
took longer than its cluster), and macOS the daemon cluster's 300 s for
its features. A host of fewer cores was not measured. Measured on
2026-11-18, the whole `just test` took 254.1 s (nextest 133.6 s, load 8)
with the clusters, 116 s (nextest 95.8 s, load 22 to 39) with a test per
feature, and 105 s (nextest 67.4 s, load 18 to 28) with its scenarios 3 at
once, the scenario list byte-identical in each.
Separation: each scenario has a temporary root of its own,
so its own ctxloom home, daemon, single-instance lock, store, holds and
stubs; ports are the system's (port 0), and the feature tests are not in the
`ports` group (they share no port).
`two_clusters_at_once_never_see_each_others_daemon_or_files` (its name from
the clusters) is the forcing test: two runs at once, four scenarios each
starting a daemon through a
command and holding it until all four serve, each answered by its own
alone, four pids, homes and endpoints (its steps are
`gherkin_separation.test.rs`; a home shared between scenarios fails it).
After a run the runner puts back the default panic hook: two cucumber runs
at once can leave cucumber's quiet one, which swallowed the test's own
assertion message.

## CI (`.github/workflows/ci.yml`)

GitHub Actions, as thin callers of the shared CI repository
github.com/ctxloom/ci (owner rulings 2026-11-12): its composite actions
`setup-rust` (rust-toolchain.toml, made the default; pinned tools by
taiki-e/install-action; rust-cache, saved on main only), `setup-protoc`
(3.21.12 by checksum), `setup-versionator` (v0.3.0's release binary by
checksum, so no job sets up Go for it), `setup-ltk` (ltk, `go install`ed on a cache miss only from the Go
ctxloom's `cmd/ltk` at the commit f36e8df, now in ctxloom-legacy's `go-final`, for
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
job, after the scores are kept. Each shard first asks whether the mutation
run's memory cap (below) can be had on its runner; where no systemd user
scope can be made, that shard runs deliberately UNCAPPED
(`MUTANTS_MEMORY_MAX=none`, said in the step summary): the runner's VM is
the job's alone, so a runaway mutant fails that shard and nothing else.
`mutants-shard <k/n> [out] [package]` with a package narrows the run to that
one crate (`0/1` for all of it), how one crate's mutants are run whole.
A shard holding mutants of the binary runs its feature runner, so on a tree
that carries red spec on purpose (an integration branch, ruling 165) its
unmutated baseline was red (cargo-mutants' exit 4, the score judging
nothing: run 43870047304 on daemon/integration, historical, before the
fresh-history reset). Such a shard first makes
the tree's own list (`just ratchet-base HEAD`, below), and when it has red
runs cargo-mutants with `GHERKIN_RATCHET_BASE` naming it: every run of the
runner, the baseline's and each mutant's, is judged against that list in
the test, so the tree's red passes, a new red fails (the baseline, or kills
the mutant). A list with no red (main) sets nothing: the plain gate. A
shard with no mutant of the binary (`cargo mutants --list`) makes no list.
The suites job still runs the runner straight, so its red is judged there.

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

ONE STAGED TARGET FOR EVERY CHECKOUT (owner, 2026-11-23: three slots'
`target/staged` of 13-66 GB each filled /home twice): the staged tree
(`_staged-tree`, and `_commit-tree`) builds into `<cache>/staged-target`
under ONE lock, `<cache>/staged-tree.lock`, whichever worktree runs it, with
`<cache>` = `$GATES_STAGED_CACHE` (the self-tests' seam), else
`${XDG_CACHE_HOME:-<account home>/.cache}/ctxloom/gates`; the account's home
is the passwd database's, never `$HOME` (an agent session may relocate it).
A committer that finds the lock held says `wait staged-tree: ...` once,
naming the lock and the holder's pid, source and checkout, and waits with no
timeout (the kernel frees it with its holder), never fails. The trees differ
by worktree, so cargo rebuilds what changed (accepted). The age sweep
(`just trim-targets`) does not reach it; `rm -rf` the directory to reclaim
it. `scripts/scratch-tree-lock.selftest.sh` and
`scripts/test-staged.selftest.sh` hold the path and the serialization.

Each throwaway's lock (`.staged-tree.lock` in the cache, `.ratchet-base.lock`,
`.mutants-batch.lock`) is a descriptor bash opens without close-on-exec and
`flock`s; the gate holds it, and what it runs is started without it (`9>&-`,
`7>&-`, `8>&-`), so nothing that outlives the gate (a test's detached
daemon) holds the next run's lock. The staged tree's recipe inherited it
until 2026-11-15 (U8r: two leaked test daemons held a commit's
`test-staged` for half an hour); `scripts/scratch-tree-lock.selftest.sh`
(`just selftest`) holds `_scratch-tree` to it through procfs (Linux).

Whole-workspace test runs are serialized MACHINE-WIDE (2026-11-16: up to
three agents share one machine, and two memory-capped runs at once can
still take all its RAM; two global OOMs did). `gates/tests-lock.sh
<label> <command>...` runs the command under a `flock` on one file per
user, `${XDG_CACHE_HOME:-$HOME/.cache}/ctxloom/gates/workspace-tests.lock`
(`TESTS_LOCK_FILE` overrides it, the self-tests' seam), never under
`$XDG_RUNTIME_DIR`, which an agent's shell often lacks or sets itself and
which goes when the last session ends; the path names no repository, so
the gates' copies in taskloom and annals would share it. Its takers:
`_test-workspace` (so `just test`, `test-staged`'s workspace leg, and
`push-check`'s and `merge-check`'s), outside `_run` so a wait is said on
the terminal and the gate's time is the run's; `_tests --workspace` itself
(so `closeout`'s, `closeout-ratchet`'s and `land-ratchet`'s test legs,
through `just test`); and `scripts/ratchet-base.sh`, which runs itself again
under it once its cached list is not there, before its `.ratchet-base.lock`.
A run of named packages (`test-pkg`, `test-staged`'s changed crates) and the
mutation run take none. A waiter says once on stderr `wait <label>: ...`,
naming the lock and the holder's pid, label and directory (which the
holder writes into the file), and waits with no timeout: the kernel lets a
flock go when its holder dies, so a wait is only ever on a live run. A
holder exports `GATES_TESTS_LOCK_HELD=<the file>`, and a taker inside its
run takes nothing (nesting never deadlocks); its descriptor (8) is closed
for the command (`8>&-`), so a test's daemon never holds it. With no
`flock` (macOS, Windows' Git Bash on CI, where each job has its own VM) the
lock is skipped and said once, `note <label>: flock is not on PATH ...`.
`scripts/tests-lock.selftest.sh` (`just selftest`) runs two concurrent
`just test`s on a fixture with a fake cargo (serialized, one wait line
naming the holder), a run nested in a holder, the descriptor through
procfs, `_tests <package>` while the lock is held, and no `flock`.
`gates/` is a copy of the shared gates repository: the change is to be
ported there (`gates/MIGRATING.md`).

lefthook runs the gates one at a time, stopping at the first failure, and
shows each gate's pass line and any failure's extract. Gates 1 to 6 run in
pre-commit (the private recipes `fmt-staged`, `secrets-scan-staged`,
`lint-staged`, `plain-binary`, `test-commit`, ...; `just --list` hides them, they run by name). Mutation (7)
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
3. **clippy**: `just lint-staged`: `just lint` run over the STAGED tree (owner
   ruling 2026-11-18, night), in `_staged-tree`'s throwaway worktree with
   test-staged's shared staged target, including the core's purity rules
   (`crates/core/clippy.toml`).
4. **plain binary**: `just plain-binary` (owner ruling 2026-11-18,
   evening) builds the workspace without its dev-dependencies, as `just
   build` does, and holds the binary it made to the two plain-build checks,
   `scripts/plain-engines-check.sh` (the real engines alone, ruling 52) and
   `scripts/plain-destroy-check.sh` (no destroy, ruling 94b); one `ok
   plain-binary` line, its log `target/logs/plain-binary.log`. `just build`
   ends with it.
5. **deps**: `just lint-deps`, cargo-deny (`deny.toml`).
6. **tests**: `just test-staged` runs the changed crates' OWN tests in the
   staged tree (HEAD plus the staged diff, a sibling worktree of the
   checkout), no dependent's (owner ruling of 2026-11-13, fast hooks,
   acceptance at landing): a library's change does not select the binary
   depending on it, so the binary's acceptance suite runs at commit only
   when the binary crate itself changed (`scripts/test-staged.selftest.sh`).
   On the default branch (and wherever the branch cannot be told)
   `test-commit` asks `test-staged dependents`, which adds the workspace
   dependents as before, so red is never committed there. A staged
   `workspace_wide` file tests the whole workspace. Every one of these runs
   is NARROWED (2026-11-18): `staged_tests_filter`, ctxloom's
   `scripts/staged-features.sh`, given the staged paths, prints the nextest
   filterset the run is held to (`_tests --filter=<set>`): every test but
   the feature runner's tests of one feature file each, and of those only
   the staged feature files' (`tests::gherkin_features::features::<path>`),
   so no commit runs a feature file it does not stage, whatever it changes
   (a step definition included); the pass line says `narrowed by
   scripts/staged-features.sh`. `just test`, and so `land-ratchet` and
   `closeout-ratchet`, is never narrowed: every feature runs there.
   Red tests are committed
   off the default branch only (`test-commit`), and no judge looks at them
   at commit: ctxloom's `red_judge` is `""` (the red-commit judge is gone,
   2026-11-13); a red scenario the branch added, or a green one it broke, is
   refused at landing by the ratchet of `land-ratchet` and
   `closeout-ratchet` (below).
7. **mutants** (no hook; at close-out, `just mutants-batch`): `mutants-staged` mutation-tests the staged diff
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
   in the gate's log; the verdict does not change. Every cargo-mutants
   run (`mutants-staged`, so `mutants-batch`, `closeout` and
   `closeout-ratchet`, and `mutants-shard`) is under a MEMORY CAP,
   `gates/memory-cap.sh` (owner ruling of 2026-11-15, after a runaway
   daemon-application mutant grew to about 70 GB twice and the global OOM
   took the editor's whole user scope with every session in it): a
   transient systemd user scope with `MemoryMax=` the justfile's
   `mutants_memory_max` (8G, owner ruling of 2026-11-16, env
   `MUTANTS_MEMORY_MAX`), `MemorySwapMax=`
   `mutants_swap_max` (2G, env `MUTANTS_SWAP_MAX`) and
   `OOMPolicy=continue`, so the cap's OOM killer kills the runaway test
   alone (its mutant is caught) and the run goes on; the default policy
   would stop the whole run. The command starts only once the scope's
   `memory.max` is seen set, and the log ends with the scope's memory peak
   and any OOM kill. A shell with no `XDG_RUNTIME_DIR` (an agent's) gets
   `/run/user/<uid>` when its bus is there; with no user bus the gate FAILS
   before anything is built, naming the fix, and never runs uncapped
   unasked: `MUTANTS_MEMORY_MAX=none` is the opt-out, stated in the run's
   output. The TEST GATES are capped the same way (owner ruling of
   2026-11-16): every run of the gates' `_tests` (`just test`, `test-pkg`,
   `test-staged`, and so the test leg of `closeout`, `closeout-ratchet` and
   `land-ratchet`), build and tests, and the ratchet base's feature runner
   (`scripts/ratchet-base.sh`), at `tests_memory_max` (16G, env
   `TESTS_MEMORY_MAX`) and `tests_swap_max` (2G, env `TESTS_SWAP_MAX`), so
   one runaway test is killed alone, nextest reports it failed and runs the
   rest (forced in the self-test with a runaway nextest test). No user bus
   is `_tests`' 121 before anything is built (its FAIL line naming
   `TESTS_MEMORY_MAX=none`, the opt-out, said in the run's log); the
   wrapper names the caller's opt-out variable (`MEMORY_CAP_OPT_OUT`). CI
   and the nightly set `TESTS_MEMORY_MAX=none` deliberately (a hosted
   runner's VM is the job's alone; Windows and macOS have no systemd), each
   test step saying so. `gates/` is a copy of the shared gates repository:
   the change is to be ported there.
   Its self-test is `scripts/memory-cap.selftest.sh` (`just selftest`).
   The last run's mutants.out (each
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
(daemon/integration, its daemon scenarios red; the landing branch until
step 11, `main` is the base afterwards, with no red scenario) lands through `just
land-ratchet <base>` (owner ruling of 2026-11-13; `scripts/closeout-ratchet.sh
--no-mutation`): `closeout-ratchet`'s gates (build, lint, lint-deps, the full
test, lint-os) and its ratchet, without mutation (no mutants-batch, even
one `RATCHET_GATES` names), each line and the summary said as `land-ratchet`.
Each branch landing on the integration branch runs it; the full `just
closeout-ratchet <base>` runs once per batch and before main. `just
closeout-ratchet <base>` (owner ruling 165; `scripts/closeout-ratchet.sh`) is
closeout's six gates as usual, mutation included (over merge-base(<base>,
HEAD)..HEAD, given `GHERKIN_RATCHET_BASE` naming the base's list when it
has red, as `mutants-shard` is), except that the feature runner may fail where the base's does
and nowhere else: the slice's red scenarios must be a SUBSET of the base
tip's. It may turn red green, never add a red scenario or break a green one
(`FAIL ratchet: N new red scenarios: B broke green, A added red`, each named,
capped). Every other test failure fails `test` as ever. The scenario list is
the feature runner's: `GHERKIN_RATCHET=<absolute file>` makes
its feature tests write a line per scenario, sorted, `red<TAB><id>` or
`green<TAB><id>` (the id `<feature path under features/> :: [<rule> ::
]<scenario>[ :: | <example row> |]`; a failure no scenario owns is `<feature>
:: (outside a scenario)`), and pass whatever failed, so only the ratchet's
judge (`scripts/ratchet-judge.sh`) decides. Beside the list it writes
`<list>.failures`, why each red scenario is red (a record per failed step:
`red<TAB><id>`, `  step: <keyword> <text>`, its message indented, capped at
2000 bytes), since a passing test's console output is never shown; the
judge's FAIL line names it (`why: <list>.failures`), and the base's is cached
beside its list. The feature tests of one nextest run assemble that one list,
and its failures file, between them: each, holding `<list>.lock`, replaces
the lines and records of the features it ran and keeps the rest, sorted as
one; `<list>.run` names the run that wrote it (`NEXTEST_RUN_ID`), so what
an earlier run left is dropped and the file is exactly the run's scenarios.
`GHERKIN_RATCHET_BASE=<absolute file>`, a base's list, judges each feature
against it in the test itself: it fails naming each of its scenarios red
here and not red there (`broke green`, `added red`, the judge's rule) and
passes the base's red, so a mutation run of a tree with red spec still has
the runner judging (`mutants-shard`, `closeout-ratchet`'s mutants-batch).
`GHERKIN_SCENARIO=<id>` runs that one scenario alone, `GHERKIN_ONLY=<text>`
the features whose path holds it; a feature's test they leave out passes
having run nothing, and when no feature matches at all every feature's test
fails. A
cached base list can freeze a flaky scenario's colour, so every scenario
whose colour on the branch differs from the base's list is RE-CONFIRMED on
the base before it is judged (owner, 2026-11-14; `ratchet-base.sh <base>
--reconfirm <ids> <out>`, in the base's throwaway worktree): run alone, then
in its feature file. A difference both re-runs give the base's listed colour
is reproduced and judged as ever (a new red stays one, a turned green
passes); one that is not, or was not re-run, is `FLAKY <id>`, named with its
colours and its failures-file reason, and FAILS the ratchet whichever way it
went: a flake is a finding, never a pass. What a re-confirmation found is
cached by the base's sha, `target/ratchet/<sha>.reconfirm` (and
`.reconfirm.failures`) beside its list (2026-11-18: a land spent 651 s
re-confirming 14), so a later land on the same base re-runs only the ids
not cached there (`note ratchet: K of the N ... re-confirmed ... before`);
only an id both re-runs gave its listed colour is cached (a flaky one is
re-run every time), and the cache is renamed into place once its
re-confirmation succeeded, failures first, so an interrupted run's work
does not count (`closeout-ratchet.selftest.sh` kills one mid-way). The base's list is `just
ratchet-base <base>` (`scripts/ratchet-base.sh`): only the runner's tests
(its feature tests, a base's clusters, or its `every_feature_runs` from
before those), on the
base's tip, in a throwaway worktree `.<checkout>.ratchet-base` beside the
checkout (target dir `target/ratchet-base`, built with
`CARGO_INCREMENTAL=0`: each base is a new tree, so its incremental cache
was written and never read; `target/debug` keeps
theirs), cached by sha at
`target/ratchet/<sha>.list`; a base from before the list gets the runner
commit that taught it applied, and says so (that commit is the first line of
a `git log` read with `sed -n 1p`, never `head -n 1`, whose early close
killed git log with SIGPIPE and, under pipefail, the script with a silent
141; `scripts/ratchet-base.selftest.sh` forces it with a log longer than a
pipe buffer). On main the base has no red
scenario, so the ratchet is the plain closeout. Exit 0, 1 (a gate, a new red,
or no base list), 2 (usage). Held by `scripts/closeout-ratchet.selftest.sh`
(`just selftest`).
