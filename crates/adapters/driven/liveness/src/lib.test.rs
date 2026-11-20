//! A session's state is the lock's verdict. Go: sessionlock_test.go and
//! session.feature ("The listing takes each session's state from its lock,
//! not its end stamp": held is live, free is ended, no lock file is unknown).

use std::path::Path;
use std::sync::Arc;

use file_system::{Fs, Open, Rooted};
use os::{LockKind, Mode};
use session_ports::{Liveness, LockState, SessionLock};

use super::LockFiles;

/// The project, absolute on every host (`C:\work\proj` on Windows): a lock's
/// path is handed to the platform's lock as it is, which takes an absolute
/// one only.
fn project() -> String {
    at("/work/proj")
}

/// `path`, absolute and spelled the unix way, as this host spells it.
fn at(path: &str) -> String {
    os::host().flavor().from_top(path)
}
const HARP: &str = "amber-swift-owl";

fn locks() -> (Arc<Rooted>, LockFiles) {
    let fs = Arc::new(Rooted::temp().unwrap());
    (fs.clone(), LockFiles::new(fs, project()))
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
        Path::new(&at("/work/proj/.ctxloom/sessions/amber-swift-owl.lock"))
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
    let mut runner = LockFiles::new(fs.clone(), project());
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
    let mut second = LockFiles::new(fs.clone(), project());
    let _held = first.hold(HARP).expect("the first run takes it");
    let started = std::time::Instant::now();
    assert!(second.hold(HARP).is_err(), "a second holder is refused");
    assert!(
        started.elapsed() < std::time::Duration::from_secs(1),
        "refused at once, not after the platform's wait"
    );
    assert_eq!(first.lock_state("brisk-copper-moth"), LockState::Unproven);
}

// --------------------------------------- run locks (rulings 86, 92, 93)

use run_ports::{LockProbe, RunLocks};

use super::RunLockFiles;

/// ctxloom's home, absolute on every host.
fn home() -> String {
    at("/home/u")
}
const RUN: &str = "0a1b2c3d-0000-4000-8000-000000000001";

fn run_locks() -> (Arc<Rooted>, RunLockFiles) {
    let fs = Arc::new(Rooted::temp().unwrap());
    (fs.clone(), RunLockFiles::new(fs, home()))
}

// Ruling 92: a run's own lock is machine-local, under ctxloom's home, named by
// the run; held, it reads held to another process's probe.
#[test]
fn a_held_run_lock_reads_held_at_the_path_it_names() {
    let (fs, locks) = run_locks();
    let held = locks.hold_run(RUN).expect("the run's lock is taken");
    assert_eq!(
        held.path(),
        at("/home/u/.ctxloom/runs/0a1b2c3d-0000-4000-8000-000000000001.lock")
    );
    let other = RunLockFiles::new(fs.clone(), home());
    assert_eq!(other.probe(held.path()), LockProbe::Held);
}

// Ruling 92: let go once the runner is started, the lock leaves no file
// behind (a refused run leaves nothing), so its probe then proves nothing:
// a started run is judged by its session's lock (ruling 93).
#[test]
fn a_run_lock_let_go_leaves_no_file_and_proves_nothing() {
    let (fs, locks) = run_locks();
    let held = locks.hold_run(RUN).unwrap();
    let path = held.path().to_string();
    drop(held);
    assert!(!fs.exists(Path::new(&path)).unwrap(), "no file is left");
    assert_eq!(locks.probe(&path), LockProbe::Unproven);
}

// Ruling 92: a launcher that died (the kernel let its lock go, the file left
// behind) reads free: its run is an orphan.
#[test]
fn a_run_lock_whose_holder_died_reads_free() {
    let (fs, locks) = run_locks();
    let path = locks.path(RUN);
    fs.create_dir_all(path.parent().unwrap(), Mode::from_bits(0o755))
        .unwrap();
    fs.open(&path, Open::Create(Mode::from_bits(0o600)))
        .unwrap();
    assert_eq!(locks.probe(&path.display().to_string()), LockProbe::Free);
}

// Ruling 92: a run's lock another holds is refused at once.
#[test]
fn a_run_lock_another_holds_is_refused_at_once() {
    let (fs, locks) = run_locks();
    let _held = locks.hold_run(RUN).unwrap();
    let other = RunLockFiles::new(fs.clone(), home());
    assert!(other.hold_run(RUN).is_err());
}

// Ruling 93: a session's lock is probed where the session's project keeps it.
#[test]
fn a_sessions_lock_is_probed_in_its_project() {
    let (fs, locks) = run_locks();
    assert_eq!(
        locks.session(&project(), HARP),
        LockProbe::Unproven,
        "none yet"
    );
    let mut sessions = LockFiles::new(fs.clone(), project());
    let held = session_ports::SessionLock::hold(&mut sessions, HARP).unwrap();
    assert_eq!(locks.session(&project(), HARP), LockProbe::Held);
    drop(held);
    assert_eq!(locks.session(&project(), HARP), LockProbe::Free);
}

// A lock not taken says its kind: another process holds it (a decline: the
// session or run is live), or its file cannot be made (a failure).
#[test]
fn a_lock_another_holds_is_held_and_one_that_cannot_be_made_failed() {
    let (fs, mut first) = locks();
    let _held = first.hold(HARP).unwrap();
    let mut second = LockFiles::new(fs.clone(), project());
    assert!(matches!(
        second.hold(HARP),
        Err(session_ports::LockError::Held(_))
    ));
    let (fs, run_held) = run_locks();
    let _run = run_held.hold_run(RUN).unwrap();
    let other = RunLockFiles::new(fs.clone(), home());
    assert!(matches!(
        other.hold_run(RUN),
        Err(run_ports::LockError::Held(_))
    ));

    // A file where the lock's directory should be: nothing can be made.
    let (fs, mut blocked) = locks();
    let dir = blocked.path(HARP).parent().unwrap().to_path_buf();
    fs.create_dir_all(dir.parent().unwrap(), Mode::from_bits(0o755))
        .unwrap();
    fs.open(&dir, Open::Create(Mode::from_bits(0o600))).unwrap();
    assert!(matches!(
        blocked.hold(HARP),
        Err(session_ports::LockError::Failed(_))
    ));
    let (fs, runs) = run_locks();
    let dir = runs.path(RUN).parent().unwrap().to_path_buf();
    fs.create_dir_all(dir.parent().unwrap(), Mode::from_bits(0o755))
        .unwrap();
    fs.open(&dir, Open::Create(Mode::from_bits(0o600))).unwrap();
    assert!(matches!(
        runs.hold_run(RUN),
        Err(run_ports::LockError::Failed(_))
    ));
}
