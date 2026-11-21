use std::path::Path;

use super::{lock_path, lock_project, Held, LOCK_FILE};

// Step 10 U9 (T28): a session's lock is inside its directory, so the two
// move as one rename; this is the one place its path is formed.
#[test]
fn a_sessions_lock_is_inside_its_directory() {
    let dir = Path::new("/work/proj/.ctxloom/sessions/amber-swift-owl");
    assert_eq!(LOCK_FILE, "session.lock");
    assert_eq!(lock_path(dir), dir.join("session.lock"));
}

// The project a session's lock is of, read back from the path `lock_path`
// forms; none from a path too short to be one.
#[test]
fn a_locks_project_is_four_up_from_it() {
    let lock = lock_path(Path::new("/work/proj/.ctxloom/sessions/amber-swift-owl"));
    assert_eq!(lock_project(&lock), Some(Path::new("/work/proj")));
    assert_eq!(lock_project(Path::new("sessions/x/session.lock")), None);
    assert_eq!(lock_project(Path::new("")), None);
}

// Defect 1: a held session lock renders as held, and nothing of its guard.
#[test]
fn a_held_session_lock_renders_as_held() {
    assert_eq!(format!("{:?}", Held::new(42_u8)), "Held");
}
