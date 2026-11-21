//! Ruling 131: every ctxloom process that opens a project's store closes it
//! before it exits (angzarr-embedded's `SqliteStore::close`), so the moment
//! the command has exited, with no wait, the store is whole on its own: its
//! log holds nothing. A write's close is the file's last read-write one,
//! which checkpoints the log into the file and removes it and its index; a
//! read's connection is read-only, which SQLite never lets remove them, so
//! an empty log may stay after a read.
//!
//! Dropped instead, a store's close is left to the driver's thread, which the
//! process's exit need not let run: taskloom measured 4 of 30 writing runs
//! leaving a log that held the command's commits. That race depends on
//! thread timing and cannot be forced from here; these tests are forced red
//! by the RED commit's stubbed closes (`todo!()`), which every path below
//! reaches, and the unit tests of `ctxloom-store-sqlite` and the wiring's
//! core hold each close on its own.

use std::path::{Path, PathBuf};

use super::library::Project;
use super::{Env, Ran};
use crate::tests::files;

const HARP: &str = "amber-swift-owl";

/// The one store under `home` (T14).
fn store_under(home: &Path) -> PathBuf {
    let store = home.join(".ctxloom/ctxloom.db");
    assert!(files::exists(&store), "the one store: {}", store.display());
    store
}

/// What is beside `store`: its log and its index, where left.
fn beside(store: &Path) -> Vec<&'static str> {
    ["ctxloom.db-wal", "ctxloom.db-shm"]
        .into_iter()
        .filter(|name| files::exists(store.with_file_name(name)))
        .collect()
}

/// Whether `store`'s log holds anything.
fn logged(store: &Path) -> bool {
    files::read_bytes(store.with_file_name("ctxloom.db-wal")).is_ok_and(|log| !log.is_empty())
}

fn ok(ran: &Ran, what: &str) {
    assert_eq!(ran.code, 0, "{what}: {}", ran.said());
}

#[test]
fn test_every_session_command_closes_its_store_before_it_exits() {
    let env = Env::new();
    let seeded = env.run(&["test", "seed-session", HARP, "--transcript"]);
    ok(&seeded, "seed");
    let store = store_under(env.home());
    assert_eq!(
        beside(&store),
        Vec::<&str>::new(),
        "the seed: nothing beside"
    );
    // `session list|show|search` and `transcript show` open no store: they
    // are the daemon's reads (slice 2 step 6; the transcript's since the
    // owner's H3 follow-up), and the daemon, the store's one owner (T14),
    // holds it open while it serves. So the write is checked first, while
    // no daemon serves.
    let kept = env.run(&["session", "keep", HARP, "--format", "json"]);
    ok(&kept, "keep");
    assert!(!logged(&store), "keep: the log holds nothing");
    assert_eq!(beside(&store), Vec::<&str>::new(), "keep: nothing beside");
    let shown = env.run(&["session", "transcript", "show", HARP, "--format", "json"]);
    ok(&shown, "transcript show");
}

// Not forced red: `test open-store` closes the store it opened with
// `SqliteStore::close` itself, with no stub of ctxloom's between, and the
// drop it replaced lost its race only by thread timing. The close's own
// promise is angzarr-embedded's to test; this holds the command to it.
#[test]
fn test_the_store_a_test_command_opens_is_closed_before_it_exits() {
    let env = Env::new();
    let ran = env.run(&["test", "open-store", "calm-otter"]);
    ok(&ran, "open-store");
    let store = store_under(env.home());
    assert_eq!(beside(&store), Vec::<&str>::new(), "made, migrated, closed");
}

// A session start's hook opens no store of its own (owner 2026-11-15, H-Q9):
// its bind is delivered to the user's daemon, which holds the store from
// then on, as every capture fact is.
#[test]
fn test_a_session_starts_hook_has_the_daemon_bind_its_session() {
    let env = Env::new();
    ok(&env.run(&["test", "seed-session", HARP]), "seed");
    let named = env.run(&["session", "show", HARP, "--format", "json"]);
    ok(&named, "show");
    let named = named.json()["name"].as_str().expect("a name").to_string();
    let transcript = env.home().join("native.jsonl");
    let payload = serde_json::json!({
        "session_id": "vendor-session-42",
        "hook_event_name": "SessionStart",
        "source": "startup",
        "transcript_path": transcript,
    })
    .to_string();
    let ran = env.run_hook(
        &["hook", "claude-code", "execute", "SessionStart"],
        &payload,
        &[("CTXLOOM_SESSION", named.as_str())],
    );
    ok(&ran, "SessionStart");
    let shown = env.run(&["session", "show", HARP, "--format", "json"]);
    ok(&shown, "show");
    assert!(
        shown.stdout.contains("vendor-session-42"),
        "the hook bound the session: {}",
        shown.stdout
    );
}

/// A child that answers its turn and fires no hook.
const CHILD: &str = r#"cat > /dev/null
echo '{"type":"system","subtype":"init","session_id":"9d764b45-a56a-48d7-ac9d-c2a16c5669f1"}'
echo '{"type":"result","subtype":"success","is_error":false,"result":"STORE-CLOSE-REPLY","session_id":"9d764b45-a56a-48d7-ac9d-c2a16c5669f1"}'
"#;

// U4, T7: a one-shot is the daemon's, and the command opens no store (T1);
// the daemon that hosted it closes its one store as it exits.
#[test]
fn test_a_one_shot_opens_no_store_and_its_daemon_closes_the_store_at_its_exit() {
    if !files::runs_scripts() {
        return;
    }
    let project = Project::initialized();
    project.bundle("demo");
    project.profile("dev", "demo");
    let bin = project.home().join("bin");
    files::put_program(bin.join("claude"), CHILD);
    let path = match crate::tests::host_var("PATH") {
        Some(host) => format!("{}:{}", bin.display(), host.to_string_lossy()),
        None => bin.display().to_string(),
    };
    let ran = project.run_with(
        &[
            ("PATH", &path),
            ("CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat01-fixture-not-real"), // gitleaks:allow
        ],
        &[
            "run",
            "--profile",
            "dev",
            "--one-shot",
            "hello",
            "--format",
            "json",
        ],
    );
    ok(&ran, "run --one-shot");
    assert!(ran.stdout.contains("STORE-CLOSE-REPLY"), "{}", ran.said());
    assert!(
        !ran.stderr.contains("was not closed") && !ran.stderr.contains("did not close"),
        "nothing of the run's store is the command's: {}",
        ran.said()
    );
    let store = store_under(project.home());
    let stopped = project.run(&["daemon", "stop"]);
    ok(&stopped, "daemon stop");
    // Its exit is waited for (bounded): the daemon says it exited only
    // once it has closed its store.
    let log = project.home().join(".ctxloom/daemon/daemon.log");
    let deadline = std::time::Instant::now() + std::time::Duration::from_secs(20);
    while !files::read(&log).unwrap_or_default().contains(" exited") {
        assert!(
            std::time::Instant::now() < deadline,
            "the daemon never exited: {}",
            files::read(&log).unwrap_or_default()
        );
        std::thread::sleep(std::time::Duration::from_millis(50));
    }
    let deadline = std::time::Instant::now() + std::time::Duration::from_secs(10);
    while !beside(&store).is_empty() {
        assert!(
            std::time::Instant::now() < deadline,
            "the daemon's store closed: nothing beside ({:?}; {})",
            beside(&store),
            files::read(&log).unwrap_or_default()
        );
        std::thread::sleep(std::time::Duration::from_millis(50));
    }
    assert!(!logged(&store), "its log holds nothing");
}
