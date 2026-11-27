# Backends never depend on a frontend crate (owner ruling 2026-11-27: "shared below both")

Status: step 1 built on `refactor/daemon-contract`; the rest planned. V = verified
in the tree on 2026-11-27 (cargo metadata, direct edges; greps of the sources),
I = inferred, not yet checked.

## The rule and the measured violations

A frontend (a driving adapter that is the CLI's: the `*-cli` crates, `mcp-server`,
`hook-cli`; `ctxloom-wiring`) may depend on a backend; a backend (`core/*`,
`adapters/driven|store|kernel`, `root/daemon`) never depends on a frontend. (V)
Every edge that breaks it starts at `ctxloom-daemon`:

| edge | what the daemon takes | step |
|---|---|---|
| ctxloom-daemon -> setup-cli | `wire` (doctor/auth result <-> API message) | 1 |
| ctxloom-daemon -> surface-cli | `wire` (materialize, uninstall, check, gitignore) | 2 |
| ctxloom-daemon -> deps-cli | `wire` (pulled, upgraded, created, edited, removed, defaulted, view) | 3 |
| ctxloom-daemon -> hook-cli | `Recording`, `Unrecorded`, `NotRecorded` (hook delivery types) | 4 |
| ctxloom-daemon -> ctxloom-cli | `Failure`, `CliError` (the entry points' return type) | 5 |
| ctxloom-daemon -> ctxloom-wiring | composition, hosted-run pieces, test fixtures | 6 |

No core crate and no driven adapter depends on a frontend (V). `ctxloom-wiring`
depends on `hook-listener` (a backend server crate): the direction is allowed.

Two driving crates are the BACKEND's own servers, not frontends: `daemon-api-grpc`
and `hook-listener` (V: only the daemon, the binary and wiring use them). The
role they sit in (`adapters/driving`) is what the final rule would forbid, so they
are reclassified first (step 0).

## Answers to the three opened questions

1. **Who owns `wire`** (the mapping between a use case's result and the API's
   message, both directions). Recommend: one `*-api` crate per area in
   `core/application`, as `library-api` already is (V: library-api holds the
   served library reads, `resources`, and `deps::browsed`, and the CLIs and the
   daemon both depend on it). Each holds the area's to-message half (the daemon's)
   and its read-back half (the CLI's), depending on `daemon-proto` and the area's
   use-case crates, never on a frontend. Areas: `setup-api` (setup-cli's wire),
   `surface-api` (surface-cli's), and the deps mapping into `library-api::deps`
   (V: it exists and is the deps reads' backend). (V for the pattern, I that no
   area's mapping reaches back into its CLI crate: step 1 measured setup's; 2 and
   3 measure theirs first.)
2. **Does `daemon-api` split into client and server halves.** Recommend yes, as a
   step after the wire moves: `daemon-api` stays the server (`Api`, `Calls`, its
   ports) and a new `daemon-api-client` (core/application) holds `Caller`,
   `Client`, `ChildWrites`, `SessionReads` and the refusal vocabulary both halves
   share (`Rejection`, `ApiRejection`, `CallError`), the server depending on it.
   Why: `session-cli` and `mcp-server` depend on `daemon-api` only for the client
   half but link the server's `Api` (V: `daemon-api` dependents). (I: the split is
   clean only if `Written`, `Launch` and the refusal types sit wholly in the
   client half; step 7 measures it before moving anything.)
3. **Where the composition lives.** Recommend a new crate `ctxloom-compose` in a
   new role `root/compose`, below both `ctxloom-wiring` and `ctxloom-daemon`,
   depending on `core/*` and `adapters/driven` and on neither `adapters/driving`
   nor `adapters/store|kernel`: the per-call `Wiring`, `build_deps*`,
   `build_setup*`, `SetupUseCases`, `library::build*`, `registry`,
   `fresh_project_harps`, `checkout_*`, `program`. (V: wiring builds these over
   `adapters/driven` (21 edges); the in-process CLI builds the same use cases,
   `commands.rs`; the daemon runs them where the caller stands. I: that none of
   them names a CLI crate's type; step 6 measures it first, and what does is
   moved or split before the crate is cut.) The hosted-run pieces
   (`oneshot::{runs_over, tell_project, output_root, launch_engines, git_user}`,
   `frontend::SESSION_STACK`) are the daemon's hosting halves left in wiring:
   they move into `ctxloom-daemon` where only it uses them, into `ctxloom-compose`
   where the client half of the same run does too. (I.)

## Order, and what is independent

- **0.** Reclassify `daemon-api-grpc` and `hook-listener` out of
  `adapters/driving` into a new role `adapters/serving` (the backend's own
  servers). Behaviour: none. Boundary: the role says what it is. Independent.
  `arch-rules.toml` lists the new role; no rule forbids anything new yet.
- **1.** `setup-cli::wire` -> `setup-api`. Removes ctxloom-daemon -> setup-cli.
- **2.** `surface-cli::wire` -> `surface-api`. Removes ctxloom-daemon -> surface-cli.
- **3.** `deps-cli::wire` -> `library-api::deps`. Removes ctxloom-daemon -> deps-cli.
- **4.** hook delivery types -> `runs-application` (where the hold and the flush
  already are). Removes ctxloom-daemon -> hook-cli. (I: the daemon uses them in
  tests only; if so the edge is a dev-dependency and the move is only for the
  rule.)
- **5.** The daemon's entry points return the daemon's own error, mapped to
  `Failure` by the binary or wiring. Removes ctxloom-daemon -> ctxloom-cli.
- **6.** Composition -> `ctxloom-compose`; hosted-run pieces to the daemon or
  compose; the test fixtures the daemon's tests take from wiring move with the
  code they test or stay behind a test-support feature of `ctxloom-compose`.
  Removes ctxloom-daemon -> ctxloom-wiring. THE LAST EDGE.
- **7.** `daemon-api` client/server split. Independent of 1 to 6 (it concerns
  frontends reaching the server's code, not the daemon reaching frontends); do it
  after 6 so the contract it names is the final one.
- Steps 1 to 5 are independent of each other and of 0; 6 follows 1 to 3 (the
  composition's result types are what the wire modules map). Each is a green
  commit of its own.

**The rule lands in step 6**, the same commit as the last edge it forbids:
`arch-rules.toml` then says `root/daemon` and `core/*` may depend on neither
`adapters/driving` nor `root/lib`, with no exception (the rule hard-fails and
allows nothing). It cannot land earlier without an exception, and none is
allowed. If step 0 is not done, `root/daemon -> daemon-api-grpc|hook-listener`
would fail it, so 0 is a prerequisite of 6.

## The 23 CLI-to-application departures (`CLI_APPLICATION_DEPARTURES`)

Out of scope for this plan. They are frontends depending on backend use-case
crates: the direction this ruling asks for. Whether a CLI runs a use case in
process or asks the daemon is the ONE WRITER axis (G1), not this one. Steps 1 to
3 may remove some departures as a side effect (a CLI that depended on an
application crate only for the mapping now depends on the `*-api` crate); the
list is then lowered in the step that changes it, since a stale entry fails.
Recommend: leave the list, shrink it where a step makes an entry stale, and plan
the rest separately. (I.)

## Step 1, built

`setup-cli`'s `wire` module (its `status_word`, `status_of`, `checks`, `report`,
`auth_rows`, `rows_of`) is `setup-api::wire`; setup-cli reads the answers back
through it and `ctxloom-daemon` says its answers through it. Removed edge:
`ctxloom-daemon -> setup-cli`. CLAUDE.md names the crate where it names the wire.

## Progress and divergences

- Step 0 built: `daemon-api-grpc` and `hook-listener` are in `adapters/serving`.
  Divergence: the role's allow list is `core/application`, `core/wire`,
  `core/ports`, `core/domain` and the presentation crates (the listener's and
  the service's tests build ports and domain types), so four dev-dependency
  exceptions (`daemon-api-grpc` -> `run-ports`, `hook-listener` -> `run-ports`,
  `runs`, `sessions`) went stale and were removed in the same commit; `root/lib`
  lists the role (wiring uses `hook-listener`).
- Step 2 built: `surface-cli`'s mapping (`materialized`, `uninstalled`,
  `checked`, `check_report`, `gitignore_*`, `materialize_request`, `NoticeOf`)
  is `surface-api::wire`. Divergence: `wire.rs` also held `InProcess`, a
  frontend type (the test double that serves the setup writes in process); it
  stays in surface-cli (`in_process.rs`, `surface_cli::InProcess`). Removed
  edge: ctxloom-daemon -> surface-cli.
- Step 3 built: deps-cli's answer mapping (`dependency`, `remote`, the
  refresh's pure half and the helpers `item_kind`, `count`) is
  `library_api::deps_wire`, beside `library_api::deps` (the reads' mapping).
  Divergence: the plan said `library-api::deps`; a sibling module keeps the
  two files readable, the crate is the same. `refresh.rs` split: `Skipped`,
  `view`, `message`, `from_message`, `applies` moved (their fields are public);
  `text`, the CLI's words, stays in deps-cli (with its two tests). library-api
  gained `presentation` and `cli-support` (the refusals' words and fixes).
  Removed edge: ctxloom-daemon -> deps-cli.
- Step 4 built: `Recording`, `Unrecorded` and `NotRecorded` are
  `setup-application`'s (`recording.rs`, beside `HookAnswer`, which `record`
  takes), not `runs-application`'s as the plan said: `Recording::record` takes
  a `HookAnswer`, which `runs-application` does not know. hook-cli re-exports
  them and keeps `execute_recording` and the `From<Unrecorded> for HookWarning`
  words. Confirmed inference: the daemon used them in tests only
  (`capture.test.rs`, which drives wiring's capture over the daemon's core).
  Removed edge: ctxloom-daemon -> hook-cli.
- Step 5 built: the daemon's entry points return `cli_support::Failure`
  (the shared type `ctxloom_cli::Failure` re-exports; the daemon already
  depended on cli-support) and the one `CliError::Output` it used became
  `TestError::Output` (wiring, test-support). Divergence from the plan, which
  said "the daemon's own error": no new error type was needed, `Failure` is not
  ctxloom-cli's. Removed edge: ctxloom-daemon -> ctxloom-cli (and its
  test-support feature line). Note for landing: this branch is cut from a main
  before the `session-destroy` -> `purge` rename (328f9f353), so the daemon's
  Cargo features need the rename on merge.

## Step 6 measured, and what it needs decided (2026-11-27)

State: steps 0 to 5 are built; ctxloom-daemon's only frontend edge left is
ctxloom-wiring. Step 6 (and so the arch-rules check) is NOT built, because the
measurement below shows the plan's single move is really two, and three homes
the plan did not name.

What the daemon takes from wiring (V, from the sources): `Wiring`, `LibError`,
`registry`, `fresh_project_harps`, `run_id`, `session_name`, `SweepSettings`;
`checkout::{checkout_harp, mint_checkout_harp, checkout_tree}`; `program`;
`setup::SetupUseCases`; `commands::{build_deps, build_deps_over, build_setup,
build_setup_over, git_remotes, DepsPuller, SetupRefresh}`; `library::{build,
build_over}`; `oneshot::{runs_over, sync_at_start, tell_project, Driven,
output_root, launch_engines, LaunchEngines, git_user, equipped}`;
`frontend::SESSION_STACK`; `capture::{Deadline, EdgeSink, ...}`; and from
`daemon/`: `files`, `flush`, `runtime`, `shutdown`, `Pace`, `DaemonEntry`,
`caller_wiring`, `caller_env`, `account_home`, `daemon_home`, `clock_offset`,
`own_stamp`, `instance_lock`, `files_of`, `library_parent_if_named`.

Which of those modules name a frontend crate (V, a count of `<frontend>::`
uses per file): `commands.rs` (container, ctxloom, deps, hook, library, run,
setup, surface CLIs and mcp-server), `library.rs` (library_cli x12, deps_cli,
session_cli, setup_cli, surface_cli), `daemon/mod.rs` (ctxloom_cli once),
`daemon/shutdown.rs` (ctxloom_cli once), `capture.rs` (hook_cli twice). Free of
frontends: `lib.rs`'s Wiring, `checkout`, `program`, `setup`, `oneshot`,
`frontend`, `error`, `sweep_config`, `daemon/{files, flush, runtime}`.

So step 6 is two commits:
- **6a, inside wiring, no crate move**: split each mixed module so every item the
  daemon takes sits in a frontend-free file: `commands.rs` -> `compose.rs`
  (the `build_*`, `git_remotes`, `DepsPuller`, `SetupRefresh`) beside `Commands`;
  `library.rs` -> `library_build.rs` (`build`, `build_over`, `PathSearch`) beside
  `LibraryCommands`; `daemon/mod.rs` -> `daemon/shared.rs` (`Pace`, `Serve`,
  `DaemonEntry`, `caller_*`, `account_home`, `daemon_home`, `clock_offset`,
  `own_stamp`, `instance_lock`, `files_of`, the constants) beside the CLI verbs;
  `shutdown.rs`'s clap `noun()` apart from its logic; `capture.rs`'s two
  `hook_cli` uses apart from `EdgeSink`/`Deadline`. Green, no behaviour change.
- **6b, the crate**: `ctxloom-compose` (role `root/compose`: `core/*`,
  `adapters/driven`, `adapters/serving`, `adapters/support`, presentation;
  neither `adapters/driving`, `adapters/store|kernel` nor `root/lib`) takes the
  frontend-free set by `git mv`; wiring depends on it; ctxloom-daemon imports
  from it; the arch rule lands.

Design questions the plan did not settle (for the owner):
1. `DaemonEntry`, `Serve` and `Pace` are types the daemon RETURNS and wiring
   TAKES. Recommend they live in `ctxloom-compose` (below both), not in either
   half. (I.)
2. `daemon/{files, flush, runtime}` and the pure part of `shutdown` are process
   infrastructure both halves use (the discovery file's place, the hold's
   flush at start, the runtime). Recommend `ctxloom-compose`, not a second
   shared crate. (I: `flush` and `shutdown` must name no CLI type once split.)
3. `capture` (the hook's delivery to the daemon: `EdgeSink`, `Deadline`,
   `HoldingRecording`) is a client of the daemon that the daemon's own start
   flush and its tests also use. Recommend `ctxloom-compose`. (I.)
4. Tests that cross both halves. `store_tests.test.rs` drives wiring's
   `Commands` with the daemon's entry: recommend it moves to the binary crate's
   tests (the composition root of both), not stays in ctxloom-daemon.
   `fixtures.test.rs`'s `World` builds a `Commands` (a frontend type): recommend
   splitting it into a base `World` (compose, behind test-support) and the
   `run_line`/`try_line` runners (wiring). `capture.test.rs` and the other
   daemon tests need only the base.
5. Does `ctxloom-compose` keep the name `Wiring`? Recommend yes: the type's
   meaning (a process's home, filesystem and environment) is unchanged; only
   its crate moves, wiring re-exporting it so the CLI's paths stay.
Step 7 (the daemon-api split) has a design question of its own that the plan
marked inferred: where `Written`, `Launch` and the refusal vocabulary sit.
It is independent of 6 and not started.

## Step 6a built

Inside wiring, no crate move: `commands.rs` -> `compose.rs` (`build_deps*`,
`build_setup*`, `git_remotes`, `DepsPuller`, `SetupRefresh`, `engines`,
`token_vars`); `library.rs` -> `library_build.rs` (`build`, `build_over`,
`PathSearch`, the advising engines); `daemon/mod.rs` -> `daemon/cli.rs` (the
`daemon` verbs `serve`, `status`, `stop`, `resolve` and their renders, with
their two tests); `shutdown.rs` no longer names `ctxloom-cli` (its own
`Call` alias and `output`, which says `LibError::Output`, a new diagnostic
`ctxloom-wiring-output`); `capture.rs` imports the recording types from
setup-application. Divergences: the plan listed `lib.rs`'s Wiring as already
frontend-free; it is, but `catalog_sources` there names frontend catalogs and
stays in wiring in 6b. Tests of the moved items that build a `Commands`
(`commands.test.rs`: engines, refresh, pull) stay beside `Commands`.

## The CLI-to-application rule is arch-rules' (owner ruling 2026-11-27)

The binary's `CLI_APPLICATION_DEPARTURES` and its test are gone. `arch-rules.toml`
has the class `@api` (`library-api`, `setup-api`, `surface-api`), the
`adapters/driving` role sees `core/wire`, `@api` and the presentation crates, and
the 25 other edges are `[[exception]]`s with their reasons (23 from the old list,
plus `mcp-server` -> `daemon-api` and the dev edge `deps-cli` -> `run-application`
that the old test did not look at). Step 7's client crate joins `@api` when it
lands in core/application.

## Step 6b built

`ctxloom-compose` (`crates/root/compose/ctxloom-compose`, role `root/compose`)
holds every module of wiring that names no frontend crate: `capture`,
`checkout`, `compose`, `error` (`LibError`), `frontend`, `frontend_asks`,
`hook_*`, `library_build`, `oneshot`, `process_env`, `program`, `rebind`,
`root_flag`, `setup`, `signals`, `sweep_config`, `test_error`, the `Wiring`
(`lib.rs`), the shared `daemon/` (`mod`, `files`, `flush`, `heard`, `runtime`,
and a new `drain` with `notice_text`, `epoch_seconds`, `configured_drain_timeout`
taken out of `shutdown`), `store_verb` (`StoreVerb`, `StoreTest`, `SeedArgs`,
out of `test_commands`) and the fixtures. Wiring keeps `commands`, `library`,
`sessions`, `top`, `mcp`, `interview`, `isolation`, `test_commands`,
`daemon/{cli, shutdown, watch}` and `catalog_sources`, and re-exports compose
(`pub use ctxloom_compose::{...}`; `daemon` is `daemon::*` of compose beside its
own three files), so no path through `ctxloom_wiring` changed. `daemon/contract.rs`
is gone. `ctxloom-daemon` depends on compose, on no frontend: `arch-rules.toml`'s
`root/daemon` lists no `adapters/driving` and no `root/lib`, `root/compose` is
a role of its own, and `root/lib` lists it.

Codes (owner: "new names, codes change"): the diagnostics that moved are
`ctxloom-compose-*` (`no-home`, `bad-session`, `output`, `no-runtime`,
`daemon-other-home`, `project-root-needed`, `project-unnamed`,
`daemon-instance-held`, `checkout`, `scan`, `held-set-aside`, and the test-support
`seed`, `test-output`, `test-refused`, `test-findings`); `shutdown-*` stay
`ctxloom-wiring-*` (their own catalog, `ctxloom-wiring`'s `EN_US`).

Divergences: (1) `store_tests.test.rs` stays in the daemon crate, rewritten to
call the daemon's `store_tests::run` by verb, not through `Commands` (the
command-line parse of the verbs is wiring's `test_commands` tests and the
features'); it did not need to move to the binary. (2) The fixtures: the base
`World` is compose's (behind test-support), `World::commands()`, `run_line` and
`try_line` are wiring's (`fixtures::WorldCommands`). (3) `SPAWNS_ITSELF`
(`cfg(test)`, per crate) could not stand once the spawn lived in a crate the
wiring's tests build without `cfg(test)`: the first run of wiring's tests spawned
its own harness as a daemon, recursively, and put 5000 processes on the machine
(killed by hand, nothing lost). It is now an explicit flag (owner ruling): nextest's `guard-homes` sets `CTXLOOM_TEST_HARNESS` for every test, and only a test-support build reads it (`daemon::HARNESS_VARIABLE`, `own_executable`). (An earlier commit used a `deps`-directory path rule; replaced.) (4) compose enables
`angzarr-embedded/sqlite` itself: its catalog lists the store's, and the feature
was only unified in by the CLI crates.

## Step 7 built (owner ruling 2026-11-27: a shared contract crate)

`daemon-api` is three crates in `core/application`: `daemon-api-contract` (the
refusals and their mapping both ways, `ApiError`, `rejection_of`, `setup_error`,
`deps_error`, `library_refusal`, `app_error`, the one launch's wire conversion,
`LibraryWriteRequest` with its per-request impls and `TRUST_INDEX`),
`daemon-api` (the server) and `daemon-api-client` (`Client`, `Caller`,
`CallError`, `Written`, `Launch`, `reconcile`, `LibraryReads`/`LibraryWrites`,
`SessionRemovals`, `Waker`, the trust and approvals client halves). Frontends
and `ctxloom-compose` depend on contract + client only; `mcp-server` and
`session-cli` need no exception any more, and both new crates are in class
`@api`. The server and the client do not depend on each other.

Divergences from the ruling's list: `CallError`, `Written`, `Launch` (the
struct), `LibraryReads/Writes` and `Removed` are the CLIENT's, not the
contract's: measured, nothing server-side uses them (the server's `approvals`
and `trust` mixed a client `impl Caller` with the `impl Api`; those two files
and their tests were split, `approvals_client`-style, one pair per crate). The
contract holds what both sides use. `library_writes!`'s generated impls of
`LibraryWriteRequest` are in the contract because the trait is (orphan rule).
The refusal tests that built a `CallError` lost those lines (`client.test.rs`
covers `CallError`). No `purge` feature on the contract: the removal types are
not feature-gated on this branch (main's `purge` rename comes with the merge).
Codes: the 27 refusal ids are `daemon-api-contract-*`, `version-skew` and
`launch-lock` are `daemon-api-client-*`, the server keeps `start-up-incomplete`,
`handing-over` and its notices. `ctxloom-compose` has a dev-dependency on
`daemon-api` for one test's server warning.

