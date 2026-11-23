//! The workspace's own invariants: the lock holds one of each angzarr crate,
//! and no commit position is anywhere in the sources.

use crate::tests::files;
use std::path::Path;

/// angzarr crates a lock must hold exactly once, whatever pulls them in:
/// taskloom's `tasks-lib` brings its own graph, so the pins must agree.
const ONE_OF_EACH: [&str; 8] = [
    "angzarr-proto",
    "angzarr-ports",
    "angzarr-diagnostic",
    "angzarr-embedded",
    "angzarr-embedded-runner",
    // rust-support's: its own angzarr pins are ours, so it must not be held
    // at two commits (one from a transitive consumer).
    "cli-support",
    "process-env",
    "sqlite-store-opener",
];

#[test]
fn test_the_lock_holds_one_of_each_angzarr_crate() {
    let lock = include_str!("../../../../../Cargo.lock");
    let mut wrong = Vec::new();
    for name in ONE_OF_EACH {
        let held = lock
            .lines()
            .filter(|line| *line == format!("name = \"{name}\""))
            .count();
        if held != 1 {
            wrong.push(format!("{name}: {held} in the lock"));
        }
    }
    assert!(wrong.is_empty(), "one of each is required: {wrong:?}");
}

/// Every package of the lock that comes from git, by name, each with the
/// source it comes from.
fn git_packages(lock: &str) -> Vec<(String, String)> {
    let mut found = Vec::new();
    for block in lock.split("\n\n") {
        let field = |key: &str| {
            block
                .lines()
                .find_map(|l| l.strip_prefix(key)?.strip_prefix(" = \""))
                .and_then(|v| v.strip_suffix('"'))
        };
        if let (Some(name), Some(source)) = (field("name"), field("source")) {
            if source.starts_with("git+") {
                found.push((name.to_string(), source.to_string()));
            }
        }
    }
    found
}

/// What a lock check came to: skipped while `cargo local-pins` patches the
/// build (a path package has no `source` line, so the lock shows no git
/// crates), or run, with its verdict.
#[derive(Debug, PartialEq)]
enum Outcome {
    Skipped(&'static str),
    Checked(Result<(), String>),
}

/// Runs `check`, unless `pins` (the `.cargo/local-pins.toml`, if there is one)
/// holds a `[patch` table. No file, or one without a patch table, runs it
/// unchanged.
fn judge(pins: Option<&str>, check: impl FnOnce() -> Result<(), String>) -> Outcome {
    if pins.is_some_and(|p| p.lines().any(|l| l.trim_start().starts_with("[patch"))) {
        return Outcome::Skipped(
            "a `cargo local-pins` patch is active (`cargo local-pins clear` lifts it)",
        );
    }
    Outcome::Checked(check())
}

fn local_pins() -> Option<String> {
    let path = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../../../.cargo/local-pins.toml");
    files::fs().read_to_string(&path).ok()
}

fn settle(name: &str, outcome: Outcome) {
    match outcome {
        Outcome::Skipped(why) => eprintln!("SKIP {name}: {why}"),
        Outcome::Checked(verdict) => verdict.unwrap(),
    }
}

fn check_every_git_crate_once(lock: &str) -> Result<(), String> {
    let packages = git_packages(lock);
    if packages.len() <= 20 {
        return Err(format!("the lock holds git crates: {}", packages.len()));
    }
    let mut names: Vec<&str> = packages.iter().map(|(n, _)| n.as_str()).collect();
    names.sort_unstable();
    let mut twice: Vec<&str> = names
        .windows(2)
        .filter(|w| w[0] == w[1])
        .map(|w| w[0])
        .collect();
    twice.dedup();
    if twice.is_empty() {
        Ok(())
    } else {
        Err(format!("git crates held more than once: {twice:?}"))
    }
}

fn check_one_repository_each(lock: &str) -> Result<(), String> {
    let packages = git_packages(lock);
    for name in ONE_OF_EACH {
        let sources: Vec<&str> = packages
            .iter()
            .filter(|(n, _)| n == name)
            .map(|(_, s)| s.as_str())
            .collect();
        if sources.len() != 1 {
            return Err(format!("{name}: {sources:?}"));
        }
        if !(sources[0].contains("github.com/angzarr-io/")
            || sources[0].contains("github.com/benjaminabbitt/rust-support"))
        {
            return Err(format!("{name}: {}", sources[0]));
        }
    }
    Ok(())
}

#[test]
fn test_every_git_pinned_crate_is_in_the_lock_once() {
    let lock = include_str!("../../../../../Cargo.lock");
    let name = "test_every_git_pinned_crate_is_in_the_lock_once";
    settle(
        name,
        judge(local_pins().as_deref(), || check_every_git_crate_once(lock)),
    );
}

#[test]
fn test_the_git_pins_of_the_one_of_each_crates_are_the_pins_of_one_repository_each() {
    let lock = include_str!("../../../../../Cargo.lock");
    let name = "test_the_git_pins_of_the_one_of_each_crates_are_the_pins_of_one_repository_each";
    settle(
        name,
        judge(local_pins().as_deref(), || check_one_repository_each(lock)),
    );
}

/// A lock as a local-pins patch leaves it: path packages, no `source` lines.
const PATCHED_LOCK: &str = "[[package]]\nname = \"angzarr-embedded\"\nversion = \"0.1.0\"\n";

/// The first line `cargo local-pins` writes into `.cargo/local-pins.toml`.
const LOCAL_PINS_HEADER: &str = "# Generated by local-pins. Machine-local paths: do not commit.";

#[test]
fn test_without_a_local_pins_file_the_lock_checks_still_assert() {
    for check in [check_every_git_crate_once, check_one_repository_each] {
        let outcome = judge(None, || check(PATCHED_LOCK));
        assert!(
            matches!(outcome, Outcome::Checked(Err(_))),
            "no local-pins file must still assert: {outcome:?}"
        );
    }
}

#[test]
fn test_a_local_pins_file_with_a_patch_table_skips_the_lock_checks() {
    let pins = [
        format!("{LOCAL_PINS_HEADER}\n\n[patch.\"https://github.com/angzarr-io/angzarr\"]\nangzarr-embedded = {{ path = \"/x\" }}\n"),
        "[patch.crates-io]\n".to_string(),
        format!("{LOCAL_PINS_HEADER}\n  [patch.\"x\"]\n"),
    ];
    for file in &pins {
        for check in [check_every_git_crate_once, check_one_repository_each] {
            let outcome = judge(Some(file), || check(PATCHED_LOCK));
            assert!(
                matches!(outcome, Outcome::Skipped(_)),
                "{file:?}: {outcome:?}"
            );
        }
    }
}

#[test]
fn test_a_local_pins_file_without_a_patch_table_does_not_skip() {
    let pins = [
        String::new(),
        format!("{LOCAL_PINS_HEADER}\n"),
        "[build]\njobs = 4\n".to_string(),
        "# [patch.\"x\"]\n".to_string(),
        "x = \"[patch\"\n".to_string(),
    ];
    for file in &pins {
        let outcome = judge(Some(file), || check_every_git_crate_once(PATCHED_LOCK));
        assert!(
            matches!(outcome, Outcome::Checked(Err(_))),
            "{file:?}: {outcome:?}"
        );
    }
}

/// The four names no source may carry (the store has no commit position:
/// ctxloom reads by stream position). Spelled in pieces so this file does
/// not hold them itself, which keeps the scan free of any exclusion.
fn banned() -> [String; 4] {
    [
        concat!("commit", "_position"),
        concat!("Commit", "Position"),
        concat!("fork", "_point"),
        concat!("global", "_position"),
    ]
    .map(String::from)
}

/// Every file under `dir`, but build output and VCS state.
fn files(dir: &Path, found: &mut Vec<std::path::PathBuf>) {
    for path in files::children(dir) {
        let name = path.file_name().expect("a name");
        if name == "target" || name == ".git" {
            continue;
        }
        if files::is_dir(&path) {
            files(&path, found);
        } else {
            found.push(path);
        }
    }
}

/// Exclusions, stated: `target/` and `.git/` (build output and VCS state)
/// and files that are not UTF-8 (SQLite fixtures). Nothing else: the `crates`
/// and `proto` trees, every file in them, this one included.
#[test]
fn test_no_store_wide_position_is_anywhere_in_the_workspace_sources() {
    let root = super::repo_root();
    let mut all = Vec::new();
    for tree in ["crates", "proto"] {
        files(&root.join(tree), &mut all);
    }
    assert!(!all.is_empty(), "scanned nothing under {}", root.display());
    let mut hits = Vec::new();
    for path in all {
        let Ok(text) = files::read(&path) else {
            continue;
        };
        for term in banned() {
            if text.contains(&term) {
                hits.push(format!("{}: {term}", path.display()));
            }
        }
    }
    assert!(hits.is_empty(), "a commit position is back: {hits:#?}");
}

/// The terms that spell a refusal's business status. A status exists
/// EXCLUSIVELY as the `(refusal_status)` option of a message of the protos;
/// the Rust sources hold none (the generated `REFUSAL_STATUS` constant,
/// derived from the proto, is the one way to name it, and carries none of
/// these). Spelled in pieces so this file does not hold them itself.
fn statuses() -> Vec<String> {
    [
        concat!("with", "_status"),
        concat!("Business", "Status::"),
        concat!("Refusal", "Status::"),
        concat!("REFUSAL_", "STATUS_"),
        concat!("FAILED_", "PRECONDITION"),
        concat!("INVALID_", "ARGUMENT"),
        concat!("ALREADY_", "EXISTS"),
        concat!("PERMISSION_", "DENIED"),
        concat!("OUT_OF_", "RANGE"),
        concat!("NOT", "_FOUND"),
    ]
    .map(String::from)
    .to_vec()
}

/// No refusal status is spelled in any Rust source: not a constant, a table,
/// a default or a status override. This file included (its terms are
/// spelled in pieces).
#[test]
fn test_no_refusal_status_is_spelled_in_the_rust_sources() {
    let root = super::repo_root();
    let mut all = Vec::new();
    files(&root.join("crates"), &mut all);
    let mut scanned = 0;
    let mut hits = Vec::new();
    for path in all {
        if path.extension().and_then(|e| e.to_str()) != Some("rs") {
            continue;
        }
        let Ok(text) = files::read(&path) else {
            continue;
        };
        scanned += 1;
        for term in statuses() {
            if text.contains(&term) {
                hits.push(format!("{}: {term}", path.display()));
            }
        }
    }
    assert!(scanned > 100, "scanned only {scanned} Rust files");
    assert!(hits.is_empty(), "a status is spelled in Rust: {hits:#?}");
}

/// The one root of the infrastructure tree (owner, 2026-11-14): the one
/// source that may name a gRPC status. Its own `*.test.rs` is a test file.
const INFRASTRUCTURE_ROOT: &str = "crates/core/ports/run-ports/src/infrastructure.rs";

/// The spellings of a gRPC status in Rust: a code by name (tonic's
/// `Code`, angzarr's `Code` and the router's `GrpcCode` alike) and tonic's
/// status constructors. Built from the bare names, so this file holds none.
fn status_spellings() -> Vec<String> {
    let codes = [
        "Cancelled",
        "Unknown",
        "InvalidArgument",
        "DeadlineExceeded",
        "NotFound",
        "AlreadyExists",
        "PermissionDenied",
        "ResourceExhausted",
        "FailedPrecondition",
        "Aborted",
        "OutOfRange",
        "Unimplemented",
        "Internal",
        "Unavailable",
        "DataLoss",
        "Unauthenticated",
    ];
    let constructors = [
        "cancelled",
        "unknown",
        "invalid_argument",
        "deadline_exceeded",
        "not_found",
        "already_exists",
        "permission_denied",
        "resource_exhausted",
        "failed_precondition",
        "aborted",
        "out_of_range",
        "unimplemented",
        "internal",
        "unavailable",
        "data_loss",
        "unauthenticated",
    ];
    codes
        .iter()
        .map(|code| format!("Code::{code}"))
        .chain(constructors.iter().map(|ctor| format!("Status::{ctor}(")))
        .collect()
}

/// The spellings of a status found in `text`.
fn spelled_statuses(text: &str) -> Vec<String> {
    status_spellings()
        .into_iter()
        .filter(|term| text.contains(term.as_str()))
        .collect()
}

/// Whether `path` (relative to the repository) may spell a status: the
/// infrastructure root, or a test file (`*.test.rs`), which plays the other
/// end of the wire (a fake daemon's arbitrary answer) and asserts what it
/// reads.
fn may_spell_a_status(rel: &str) -> bool {
    rel == INFRASTRUCTURE_ROOT || rel.ends_with(".test.rs")
}

/// No Rust source but the infrastructure root spells a gRPC status (the
/// two trees: a business refusal's status is its proto's
/// `(refusal_status)`, above; an infrastructure error's is named once, at
/// `run_ports::Infrastructure`). And the root does name them, so the
/// allowance is not vacuous.
#[test]
fn test_no_status_is_spelled_in_rust_but_at_the_infrastructure_root() {
    let root = super::repo_root();
    let mut all = Vec::new();
    files(&root.join("crates"), &mut all);
    let mut scanned = 0;
    let mut hits = Vec::new();
    for path in all {
        if path.extension().and_then(|e| e.to_str()) != Some("rs") {
            continue;
        }
        let rel = path
            .strip_prefix(&root)
            .expect("under the root")
            .to_string_lossy()
            .replace('\\', "/");
        if may_spell_a_status(&rel) {
            continue;
        }
        let Ok(text) = files::read(&path) else {
            continue;
        };
        scanned += 1;
        for term in spelled_statuses(&text) {
            hits.push(format!("{rel}: {term}"));
        }
    }
    assert!(scanned > 100, "scanned only {scanned} Rust files");
    assert!(
        hits.is_empty(),
        "a status is spelled outside {INFRASTRUCTURE_ROOT}: {hits:#?}"
    );
    let named = spelled_statuses(&files::read(root.join(INFRASTRUCTURE_ROOT)).expect("the root"));
    assert!(
        named.contains(&format!("Code::{}", "NotFound")),
        "the root names no status: {named:?}"
    );
}

#[test]
fn test_the_status_scan_allows_the_root_and_test_files_alone() {
    assert!(may_spell_a_status(INFRASTRUCTURE_ROOT));
    assert!(may_spell_a_status(
        "crates/adapters/driven/hook-sink-grpc/src/lib.test.rs"
    ));
    for elsewhere in [
        "crates/adapters/driven/hook-sink-grpc/src/lib.rs",
        "crates/core/ports/run-ports/src/sink.rs",
        "crates/core/ports/run-ports/src/infrastructure.rs.bak",
        "crates/other/run-ports/src/infrastructure.rs",
    ] {
        assert!(!may_spell_a_status(elsewhere), "{elsewhere}");
    }
}

#[test]
fn test_the_status_scan_finds_each_spelling() {
    let stray = format!("let code = tonic::Code::{};", "NotFound");
    assert_eq!(
        spelled_statuses(&stray),
        vec![format!("Code::{}", "NotFound")]
    );
    let grpc = format!("GrpcCode::{}", "Aborted");
    assert_eq!(
        spelled_statuses(&grpc),
        vec![format!("Code::{}", "Aborted")]
    );
    let built = format!("Err(Status::{}(\"x\"))", "internal");
    assert_eq!(
        spelled_statuses(&built),
        vec![format!("Status::{}(", "internal")]
    );
    for clean in [
        "Code::from_i32(error.status())",
        "Status::new(code, message)",
        "Status::with_details(code, message, details)",
        "ExitCode::FAILURE",
        "RpcStatus::decode(bytes)",
    ] {
        assert!(spelled_statuses(clean).is_empty(), "{clean}");
    }
}

/// A test that spawns the binary in a directory of its own, with a cleared
/// environment, must hand it the temporary root it made that directory under
/// (`TMPDIR`). The binary finds its project by walking up from the working
/// directory and stops at its temporary root; without it, it stops at `/tmp`,
/// so a temporary directory made anywhere else (a `TMPDIR` below the real
/// home, as the closeout's mutation run sets one) lets the walk climb out of
/// the test and find the real `~/.ctxloom` as its project.
#[test]
fn test_every_spawn_in_a_directory_hands_the_child_its_temporary_root() {
    let src = super::repo_root().join("crates/root/bin/ctxloom/src");
    let mut missing = Vec::new();
    let mut spawners = 0;
    for path in files::children(&src) {
        if !path.to_string_lossy().ends_with(".test.rs") || path.ends_with("workspace.test.rs") {
            continue;
        }
        let text = files::read(&path).expect("a test file");
        if text.contains(".env_clear()") && text.contains(".current_dir(") {
            spawners += 1;
            if !text.contains("\"TMPDIR\"") {
                missing.push(path.display().to_string());
            }
        }
    }
    assert!(spawners >= 5, "found only {spawners} spawning test files");
    assert!(missing.is_empty(), "no TMPDIR handed on: {missing:#?}");
}

/// A test that spawns the binary with a cleared environment hands it this
/// test process's lifeline (`tests::lifeline`, `CTXLOOM_TEST_LIFELINE`), so
/// a daemon it starts, or its command starts, exits once the test is gone,
/// killed or not, and ends what it hosts.
#[test]
fn test_every_spawn_with_a_cleared_environment_hands_the_child_its_lifeline() {
    let src = super::repo_root().join("crates/root/bin/ctxloom/src");
    let mut missing = Vec::new();
    let mut spawners = 0;
    for path in files::children(&src) {
        if !path.to_string_lossy().ends_with(".test.rs") || path.ends_with("workspace.test.rs") {
            continue;
        }
        let text = files::read(&path).expect("a test file");
        if text.contains(".env_clear()") && text.contains(".current_dir(") {
            spawners += 1;
            if !text.contains("crate::tests::lifeline()") {
                missing.push(path.display().to_string());
            }
        }
    }
    assert!(spawners >= 5, "found only {spawners} spawning test files");
    assert!(missing.is_empty(), "no lifeline handed on: {missing:#?}");
}

#[test]
fn test_annals_has_left_the_workspace() {
    let meta = super::cargo_metadata();
    let annals: Vec<&str> = meta["packages"]
        .as_array()
        .expect("packages")
        .iter()
        .filter_map(|p| p["name"].as_str())
        .filter(|name| name.starts_with("annals"))
        .collect();
    assert!(annals.is_empty(), "still in the graph: {annals:?}");
}

/// The sources that may call `run_ports::hook_environment`: its own module
/// and that module's tests. It is KEPT for M10b's container arrival (OD-13),
/// and no host run calls it (slice 4 U7: the per-run listener is gone, and
/// with it the endpoint, bearer and certificate a host engine was told).
/// M10b's container launcher joins this list when it lands; a review of
/// M10b flags the function as dead code if it lands without calling it.
const HOOK_ENVIRONMENT_CALLERS: [&str; 2] = [
    "crates/core/ports/run-ports/src/hook.rs",
    "crates/core/ports/run-ports/src/hook.test.rs",
];

/// Nothing outside M10b's kept list calls `hook_environment` (U7, OD-13).
#[test]
fn test_nothing_but_the_m10b_kept_list_calls_hook_environment() {
    let root = super::repo_root();
    let mut all = Vec::new();
    files(&root.join("crates"), &mut all);
    // Spelled in pieces so this file does not hold the call itself.
    let call = concat!("hook_", "environment(");
    let mut scanned = 0;
    let mut callers = Vec::new();
    for path in all {
        if path.extension().and_then(|e| e.to_str()) != Some("rs") {
            continue;
        }
        let Ok(text) = files::read(&path) else {
            continue;
        };
        scanned += 1;
        let relative = path
            .strip_prefix(&root)
            .expect("under the repository")
            .to_string_lossy()
            .replace('\\', "/");
        // A call, not a longer name ending in it (this test's own name).
        let calls = text.match_indices(call).any(|(at, _)| {
            !text[..at]
                .chars()
                .next_back()
                .is_some_and(|c| c.is_alphanumeric() || c == '_')
        });
        if calls && !HOOK_ENVIRONMENT_CALLERS.contains(&relative.as_str()) {
            callers.push(relative);
        }
    }
    assert!(scanned > 100, "scanned only {scanned} Rust files");
    assert!(
        callers.is_empty(),
        "hook_environment is M10b's alone (OD-13): {callers:#?}"
    );
}

/// The `Ctxloom` RPCs whose request carries no [`daemon_proto::v1::Caller`]
/// (owner, 2026-11-18): the reads by name (a run, a session, its transcript,
/// an approval, a trust, the project, a worktree listing not yet served) and
/// the daemon's own lifecycle (`GetDaemon`, `StopDaemon`, which a client of
/// another version must still reach to stop it). Every other RPC acts on a
/// user's behalf (it plans, reads the library, runs git, writes or launches)
/// and carries one.
const CALLERLESS_RPCS: &[&str] = &[
    "GetDaemon",
    "StopDaemon",
    "GetProject",
    "GetSession",
    "ListSessions",
    "SearchSessions",
    "GetTranscript",
    "WatchTranscript",
    "WatchSession",
    "ListWorktrees",
    "DeleteHandOff",
    "GetRun",
    "WatchRun",
    "CreateApproval",
    "GetApproval",
    "ListApprovals",
    "WaitApproval",
    "GetTrust",
    "ListTrusts",
];

/// Each RPC of the `Ctxloom` service, by name, and whether its request has a
/// `caller` field of type `Caller` at 98.
fn rpcs_carrying_the_caller() -> Vec<(String, bool)> {
    let set = daemon_proto::descriptors().expect("the descriptor set decodes");
    let package: Vec<_> = set
        .file
        .iter()
        .filter(|file| file.package() == "ctxloom.v1")
        .collect();
    let carries = |input: &str| {
        let name = input.trim_start_matches(".ctxloom.v1.");
        package
            .iter()
            .flat_map(|file| &file.message_type)
            .filter(|message| message.name() == name)
            .flat_map(|message| &message.field)
            .any(|field| {
                field.name() == "caller"
                    && field.number() == 98
                    && field.type_name() == ".ctxloom.v1.Caller"
            })
    };
    package
        .iter()
        .flat_map(|file| &file.service)
        .filter(|service| service.name() == "Ctxloom")
        .flat_map(|service| &service.method)
        .map(|method| (method.name().to_string(), carries(method.input_type())))
        .collect()
}

#[test]
fn test_every_request_made_on_a_users_behalf_carries_the_caller() {
    let rpcs = rpcs_carrying_the_caller();
    assert!(rpcs.len() > 100, "read only {} RPCs", rpcs.len());
    let without: Vec<_> = rpcs
        .iter()
        .filter(|(name, carried)| !carried && !CALLERLESS_RPCS.contains(&name.as_str()))
        .map(|(name, _)| name)
        .collect();
    assert!(
        without.is_empty(),
        "these requests act on a user's behalf and carry no `Caller caller = 98`: {without:#?}"
    );
}

#[test]
fn test_the_callerless_list_names_only_rpcs_that_carry_no_caller() {
    let rpcs = rpcs_carrying_the_caller();
    for listed in CALLERLESS_RPCS {
        match rpcs.iter().find(|(name, _)| name == listed) {
            None => panic!("{listed} is listed callerless and is no RPC"),
            Some((_, true)) => panic!("{listed} is listed callerless and carries a Caller"),
            Some((_, false)) => {}
        }
    }
}

/// The CLI crates' dependencies on application crates there are today (step
/// 10 U11's forcing check): every one is a DEPARTURE from the rule, listed
/// with what it is used for, until the CLI crates are request and render
/// alone (step 10a U2's "the CLI crates' application dependencies" deletion,
/// not done yet). A new edge fails the check; so does an entry here whose
/// edge is gone (take it out).
const CLI_APPLICATION_DEPARTURES: &[(&str, &str, &str)] = &[
    (
        "container-cli",
        "isolation-application",
        "its error and container rows, rendered",
    ),
    (
        "ctxloom-cli",
        "library-application",
        "the global `Flags` it parses",
    ),
    (
        "deps-cli",
        "deps-application",
        "the rows it renders and its catalog",
    ),
    (
        "deps-cli",
        "library-api",
        "resource names, reads' flags, the deps resources",
    ),
    (
        "hook-cli",
        "setup-application",
        "`HookAnswer`, what a hook answers its engine",
    ),
    (
        "library-cli",
        "library-application",
        "`Flags`, `SourcedValue`, `AgentSettings` and hints, rendered",
    ),
    (
        "library-cli",
        "library-api",
        "resource names, resources and served writes' helpers",
    ),
    ("run-cli", "library-application", "its catalog (`EN_US`)"),
    (
        "run-cli",
        "run-application",
        "the run's use case, in process",
    ),
    (
        "session-cli",
        "daemon-api",
        "the daemon's client (`CallError`, `Written`, `Removed`)",
    ),
    (
        "session-cli",
        "isolation-application",
        "worktree verdicts and rows, rendered",
    ),
    (
        "session-cli",
        "reclaim-application",
        "holds and `parse_age`",
    ),
    (
        "session-cli",
        "sessions-application",
        "timestamps, urgencies and classes",
    ),
    (
        "setup-cli",
        "auth-application",
        "auth rows and their catalog",
    ),
    ("setup-cli", "deps-application", "deps' errors and notices"),
    ("setup-cli", "doctor-checks", "doctor's `Report`"),
    (
        "setup-cli",
        "init-application",
        "init's companions and prompts ports",
    ),
    ("setup-cli", "library-api", "reads' flags"),
    ("setup-cli", "reclaim-application", "clean's `CleanReport`"),
    (
        "setup-cli",
        "sessions-application",
        "its errors and catalog",
    ),
    ("surface-cli", "library-api", "reads' flags"),
    ("surface-cli", "library-application", "the global `Flags`"),
    (
        "surface-cli",
        "setup-application",
        "materialize's diff and the hooks' status, rendered",
    ),
];

/// The package name a crate directory's `Cargo.toml` gives.
fn package_name(manifest: &str) -> Option<String> {
    let package = manifest.split("[package]").nth(1)?;
    package.lines().find_map(|line| {
        let value = line.trim().strip_prefix("name")?.trim().strip_prefix('=')?;
        Some(value.trim().trim_matches('"').to_string())
    })
}

/// The names a manifest's `[dependencies]` table lists (not its dev or
/// build dependencies).
fn dependency_names(manifest: &str) -> Vec<String> {
    let mut names = Vec::new();
    let mut inside = false;
    for line in manifest.lines() {
        let line = line.trim();
        if line.starts_with('[') {
            inside = line == "[dependencies]";
            continue;
        }
        if !inside || line.is_empty() || line.starts_with('#') {
            continue;
        }
        let name = line
            .split(['=', '.', ' '])
            .next()
            .unwrap_or_default()
            .trim_matches('"');
        if !name.is_empty() {
            names.push(name.to_string());
        }
    }
    names
}

/// The package names of the crates directly under `dir`.
fn packages_under(dir: &Path) -> Vec<(String, String)> {
    let mut found = Vec::new();
    for path in files::children(dir) {
        let Ok(manifest) = files::read(path.join("Cargo.toml")) else {
            continue;
        };
        if let Some(name) = package_name(&manifest) {
            found.push((name, manifest));
        }
    }
    found
}

/// The rule (step 10 U11): no CLI crate (a driving adapter named `*-cli`)
/// depends on an application crate (`crates/core/application/*`); the CLI
/// is request and render, and every use case runs in the daemon. Today's
/// edges are the listed departures.
#[test]
fn test_no_cli_crate_depends_on_an_application_crate_but_the_departures() {
    let root = super::repo_root();
    let applications: Vec<String> = packages_under(&root.join("crates/core/application"))
        .into_iter()
        .map(|(name, _)| name)
        .collect();
    assert!(applications.len() > 10, "read {applications:?}");
    let clis: Vec<(String, String)> = packages_under(&root.join("crates/adapters/driving"))
        .into_iter()
        .filter(|(name, _)| name.ends_with("-cli"))
        .collect();
    assert!(clis.len() > 5, "read {} CLI crates", clis.len());
    let mut edges = Vec::new();
    for (cli, manifest) in &clis {
        for dependency in dependency_names(manifest) {
            if applications.contains(&dependency) {
                edges.push((cli.clone(), dependency));
            }
        }
    }
    let listed = |cli: &str, application: &str| {
        CLI_APPLICATION_DEPARTURES
            .iter()
            .any(|(c, a, _)| *c == cli && *a == application)
    };
    let new: Vec<_> = edges.iter().filter(|(c, a)| !listed(c, a)).collect();
    assert!(
        new.is_empty(),
        "a CLI crate depends on an application crate (the CLI is request and render): {new:#?}"
    );
    let stale: Vec<_> = CLI_APPLICATION_DEPARTURES
        .iter()
        .filter(|(c, a, _)| !edges.iter().any(|(ec, ea)| ec == c && ea == a))
        .collect();
    assert!(
        stale.is_empty(),
        "a departure whose edge is gone, take it out: {stale:#?}"
    );
}

#[test]
fn test_a_manifests_dependencies_are_its_dependencies_table_alone() {
    let manifest = "[package]\nname = \"demo-cli\"\n\n[dependencies]\n# a note\nlibrary-application.workspace = true\nclap = { workspace = true }\n\"quoted\" = \"1\"\n\n[dev-dependencies]\nrun-application.workspace = true\n";
    assert_eq!(package_name(manifest).as_deref(), Some("demo-cli"));
    assert_eq!(
        dependency_names(manifest),
        ["library-application", "clap", "quoted"]
    );
}

/// Every place the daemon's sources open a library tree (step 10 U11's
/// forcing check), by file, with how many opens it holds and how each is
/// reached: through the observation (the scanner, `library_api::observe`)
/// first, else a stated DEPARTURE. An open added anywhere else, or a count
/// that moves, fails the check: the new open is to follow an observation.
const DAEMON_LIBRARY_OPENS: &[(&str, usize, &str)] = &[
    (
        "crates/root/lib/ctxloom-wiring/src/daemon/places.rs",
        2,
        "a library read's library, once `library_reads` observed its trees (`read_site`); \
         the trust preview's (`GetTrust`), once `get_trust` observed the project's tree at its \
         registered root",
    ),
    (
        "crates/root/lib/ctxloom-wiring/src/daemon/setup_reads.rs",
        2,
        "a setup read's (doctor, auth status, init prompt), once `setup_read` observed its trees",
    ),
    (
        "crates/root/lib/ctxloom-wiring/src/daemon/foreign.rs",
        1,
        "a foreign writer's, once `observe_trees` observed the tree it records in",
    ),
    (
        "crates/root/lib/ctxloom-wiring/src/daemon/deps.rs",
        1,
        "a dependency write's, once the write observed the tree it lands in",
    ),
    (
        "crates/root/lib/ctxloom-wiring/src/daemon/mod.rs",
        1,
        "a hosted child's plan at its hand-off, once `create_hand_off` observed its tree; \
         DEPARTURE: its plan and output root read again at each launch (the daemon loop's \
         `start`) are read unobserved: the loop has no observation to reach",
    ),
    (
        "crates/root/lib/ctxloom-wiring/src/oneshot.rs",
        1,
        "a hosted one-shot's or terminal run's plan, once `observe_for` observed its trees",
    ),
];

/// What opens a library tree in the root's sources.
const LIBRARY_OPENERS: &[&str] = &[
    "library::build(",
    "library::build_over(",
    "build_setup(",
    "build_setup_over(",
];

/// How many library opens `text` holds (a definition, `fn build(` or `fn
/// build_setup(`, is none).
fn library_opens(text: &str) -> usize {
    LIBRARY_OPENERS
        .iter()
        .map(|opener| {
            text.match_indices(opener)
                .filter(|(at, _)| !text[..*at].ends_with("fn "))
                .count()
        })
        .sum()
}

/// The rule (step 10 U11): nothing in the daemon reads a library tree
/// outside the observation. The daemon's root sources (`ctxloom-wiring`'s
/// `daemon/`, and the hosted runs' `oneshot.rs` and `frontend.rs`) open a
/// library only where [`DAEMON_LIBRARY_OPENS`] lists; the core's daemon
/// crates (daemon-api, daemon-application) open none at all (they reach a
/// library through their ports, each observed first).
#[test]
fn test_nothing_in_the_daemon_opens_a_library_tree_outside_the_observation() {
    let root = super::repo_root();
    let wiring = root.join("crates/root/lib/ctxloom-wiring/src");
    let mut sources = Vec::new();
    files(&wiring.join("daemon"), &mut sources);
    sources.push(wiring.join("oneshot.rs"));
    sources.push(wiring.join("frontend.rs"));
    for core in ["daemon-api", "daemon-application"] {
        files(
            &root.join("crates/core/application").join(core),
            &mut sources,
        );
    }
    let mut found = Vec::new();
    for path in sources {
        let name = path.to_string_lossy().replace('\\', "/");
        if !name.ends_with(".rs") || name.ends_with(".test.rs") {
            continue;
        }
        let text = files::read(&path).expect("a source");
        let opens = library_opens(&text);
        if opens > 0 {
            let relative = path
                .strip_prefix(&root)
                .expect("under the repository")
                .to_string_lossy()
                .replace('\\', "/");
            found.push((relative, opens));
        }
    }
    let mut listed: Vec<(String, usize)> = DAEMON_LIBRARY_OPENS
        .iter()
        .map(|(file, count, _)| ((*file).to_string(), *count))
        .collect();
    found.sort();
    listed.sort();
    assert_eq!(
        found, listed,
        "a library open in the daemon that is not listed (it is to follow an observation)"
    );
}

#[test]
fn test_a_library_open_is_counted_and_its_definition_is_not() {
    let text = "fn build(w) {}\nlet a = crate::library::build(&w);\nlet c = crate::library::build_over(&w, d);\nfn build_setup(x) {}\nbuild_setup_over(&w, d, f);\n";
    assert_eq!(library_opens(text), 3);
}

/// The daemon root's variable is `CTXLOOM_DAEMON_ROOT`; the name it replaced
/// is read and passed on nowhere (a hard migrate, no alias). The old name is
/// spelled in two pieces so this file is not its own hit; the dated
/// decision logs under `docs/decisions/` are history and are not judged.
#[test]
fn test_no_tracked_file_names_the_retired_daemon_root_variable() {
    let retired = format!("CTXLOOM_{}", "HOME");
    let root = super::repo_root();
    let listing = std::process::Command::new("git")
        .arg("-C")
        .arg(&root)
        .args(["ls-files", "-z"])
        .output()
        .expect("git ls-files");
    assert!(listing.status.success(), "git ls-files failed");
    let mut scanned = 0;
    let mut hits = Vec::new();
    for rel in String::from_utf8_lossy(&listing.stdout).split('\0') {
        if rel.is_empty() || rel.starts_with("docs/decisions/") {
            continue;
        }
        let Ok(text) = files::read(root.join(rel)) else {
            continue;
        };
        scanned += 1;
        for (n, line) in text.lines().enumerate() {
            if line.contains(&retired) {
                hits.push(format!("{rel}:{}", n + 1));
            }
        }
    }
    assert!(scanned > 500, "scanned only {scanned} files");
    assert!(hits.is_empty(), "the retired variable is named: {hits:#?}");
}
