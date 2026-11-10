//! A session's state is the lock's verdict. Go: sessionlock_test.go and
//! session.feature ("The listing takes each session's state from its lock,
//! not its end stamp": held is live, free is ended, no lock file is unknown).

use std::path::Path;
use std::sync::Arc;

use file_system::{Fs, Open, Rooted};
use os::{LockKind, Mode};
use session_ports::{Liveness, LockState, SessionLock};

use super::LockFiles;

const PROJECT: &str = "/work/proj";
const HARP: &str = "amber-swift-owl";

fn locks() -> (Arc<Rooted>, LockFiles) {
    let fs = Arc::new(Rooted::temp().unwrap());
    (fs.clone(), LockFiles::new(fs, PROJECT))
}

/// The lock file of `harp`, made (as a session that ran once made it).
fn made(fs: &Rooted, locks: &LockFiles, harp: &str) -> std::path::PathBuf {
    let path = locks.path(harp);
    fs.create_dir_all(path.parent().unwrap(), Mode::from_bits(0o755))
        .unwrap();
    fs.open(&path, Open::Create(Mode::from_bits(0o600)))
        .unwrap();
    path
}

// The lock is beside the session directory, never inside it: on Windows an
// open handle blocks deletion, so one inside would stop the sweep holding it
// from removing the directory.
#[test]
fn the_lock_sits_beside_the_session_directory() {
    let (_, locks) = locks();
    assert_eq!(
        locks.path(HARP),
        Path::new("/work/proj/.ctxloom/sessions/amber-swift-owl.lock")
    );
}

// session.feature: "a finished session ... with a transcript": the lock file
// is there and free.
#[test]
fn a_lock_nothing_holds_proves_the_owner_ended() {
    let (fs, mut locks) = locks();
    made(&fs, &locks, HARP);
    assert_eq!(locks.lock_state(HARP), LockState::Free);
}

// session.feature: "a session ... that is running again": a held lock is live
// whatever the index says.
#[test]
fn a_lock_a_running_process_holds_is_live() {
    let (fs, mut locks) = locks();
    let path = made(&fs, &locks, HARP);
    let owner = fs.open(&path, Open::Read).unwrap();
    assert!(fs.os().try_lock(&owner, LockKind::Exclusive).unwrap());
    assert_eq!(locks.lock_state(HARP), LockState::Held);
    drop(owner);
    assert_eq!(
        locks.lock_state(HARP),
        LockState::Free,
        "the owner ended, and the kernel let the lock go"
    );
}

// session.feature: "a finished session ... with a transcript but no session
// lock": "cannot determine" is never permission.
#[test]
fn no_lock_file_proves_nothing_and_a_probe_makes_none() {
    let (fs, mut locks) = locks();
    assert_eq!(locks.lock_state(HARP), LockState::Unproven);
    assert_eq!(locks.lock_state(HARP), LockState::Unproven);
    assert!(
        !fs.exists(&locks.path(HARP)).unwrap(),
        "a lock file the probe made would read as free on the next"
    );
}

// Go: only a regular file is a lock.
#[test]
fn a_directory_where_the_lock_should_be_proves_nothing() {
    let (fs, mut locks) = locks();
    fs.create_dir_all(&locks.path(HARP), Mode::from_bits(0o755))
        .unwrap();
    assert_eq!(locks.lock_state(HARP), LockState::Unproven);
}

// A probe holds nothing past its answer: a session starting right after it
// can take the lock.
#[test]
fn a_probe_holds_nothing_after_it_answers() {
    let (fs, mut locks) = locks();
    let path = made(&fs, &locks, HARP);
    assert_eq!(locks.lock_state(HARP), LockState::Free);
    let owner = fs.open(&path, Open::Read).unwrap();
    assert!(
        fs.os().try_lock(&owner, LockKind::Exclusive).unwrap(),
        "the probe let go"
    );
}

// Harps are separate: one session's held lock says nothing of another's.
#[test]
fn each_harp_has_its_own_lock() {
    let (fs, mut locks) = locks();
    let a = made(&fs, &locks, "amber-swift-owl");
    made(&fs, &locks, "brisk-copper-moth");
    let owner = fs.open(&a, Open::Read).unwrap();
    assert!(fs.os().try_lock(&owner, LockKind::Exclusive).unwrap());
    assert_eq!(locks.lock_state("amber-swift-owl"), LockState::Held);
    assert_eq!(locks.lock_state("brisk-copper-moth"), LockState::Free);
    assert_eq!(locks.lock_state("calm-dusty-wren"), LockState::Unproven);
}

// Defect 1: the run HOLDS its session's lock for its life, so the probe (any
// other process's) reads it live; let go, it reads ended. The lock file is
// made by the hold: a session that never ran has none.
#[test]
fn a_held_session_lock_reads_live_and_let_go_reads_ended() {
    let (fs, mut probe) = locks();
    let mut runner = LockFiles::new(fs.clone(), PROJECT);
    assert_eq!(probe.lock_state(HARP), LockState::Unproven, "no lock yet");
    let held = runner.hold(HARP).expect("the run takes its lock");
    assert_eq!(probe.lock_state(HARP), LockState::Held);
    drop(held);
    assert_eq!(probe.lock_state(HARP), LockState::Free);
}

// Defect 1: a lock another holds is refused at once (no wait): two runs never
// own one session at a time.
#[test]
fn a_session_lock_another_holds_is_refused_at_once() {
    let (fs, mut first) = locks();
    let mut second = LockFiles::new(fs.clone(), PROJECT);
    let _held = first.hold(HARP).expect("the first run takes it");
    let started = std::time::Instant::now();
    assert!(second.hold(HARP).is_err(), "a second holder is refused");
    assert!(
        started.elapsed() < std::time::Duration::from_secs(1),
        "refused at once, not after the platform's wait"
    );
    assert_eq!(first.lock_state("brisk-copper-moth"), LockState::Unproven);
}

// ------------------------------------------------ processes (ruling 86)
//
// PROPOSAL: `/proc` through the file-system seam (no cfg, no new dependency):
// a host with no `/proc` (macOS, Windows) proves nothing, so no run is ever
// judged an orphan there. These run on this (Linux) host's real `/proc`.

use run_ports::{ProcessIdentity, ProcessState, Processes};

use super::ProcFiles;

fn host() -> ProcFiles {
    let fs = file_system::Os::new(os::host(), Path::new("/")).unwrap();
    ProcFiles::new(Arc::new(fs))
}

// Ruling 86: this process is its pid and its start, and it is running.
#[test]
fn this_process_is_its_pid_and_its_start_and_it_runs() {
    let processes = host();
    let me = processes.current();
    assert_eq!(me.pid, std::process::id());
    assert!(me.started > 0, "{me:?}");
    assert_eq!(processes.state(&me), ProcessState::Running);
}

// Ruling 86: a pid with another start is another process (the pid reused):
// the one recorded is gone. A pid nothing has is gone.
#[test]
fn a_reused_pid_or_one_nothing_has_is_gone() {
    let processes = host();
    let me = processes.current();
    let reused = ProcessIdentity {
        started: me.started + 1,
        ..me
    };
    assert_eq!(processes.state(&reused), ProcessState::Gone);
    let nobody = ProcessIdentity {
        pid: u32::MAX - 7,
        started: 1,
    };
    assert_eq!(processes.state(&nobody), ProcessState::Gone);
}

// Ruling 86: an identity that names no process proves nothing, and a host with
// no `/proc` proves nothing about any process.
#[test]
fn an_unknown_identity_or_a_host_without_proc_proves_nothing() {
    let processes = host();
    assert_eq!(
        processes.state(&ProcessIdentity::default()),
        ProcessState::Unproven
    );
    let (fs, _) = locks();
    let without_proc = ProcFiles::new(fs);
    let someone = ProcessIdentity { pid: 1, started: 1 };
    assert_eq!(without_proc.state(&someone), ProcessState::Unproven);
    assert!(without_proc.current().is_unknown());
}
