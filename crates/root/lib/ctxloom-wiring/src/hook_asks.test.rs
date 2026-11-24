//! The statusline's count of waiting asks: nothing where it cannot be asked.

use crate::commands::tests::World;

const SESSION: &str = "projects/swift-amber-falcon/sessions/brisk-copper-moth";

// No daemon serves this home: the count is 0 and none is started (a unit
// test's executable never spawns itself as the daemon, so the log is the
// witness for the end-to-end scenario in session_approvals.feature).
#[test]
fn test_no_daemon_serving_counts_no_ask() {
    let mut world = World::new("statusline-asks-no-daemon");
    world.set(run_ports::SESSION_VARIABLE, SESSION);
    assert_eq!(super::waiting(&world.env()), 0);
}

#[test]
fn test_a_session_naming_no_project_counts_no_ask() {
    let mut world = World::new("statusline-asks-no-project");
    world.set(run_ports::SESSION_VARIABLE, "not-a-session-name");
    assert_eq!(super::waiting(&world.env()), 0);
}

#[test]
fn test_no_session_counts_no_ask() {
    let world = World::new("statusline-asks-no-session");
    assert_eq!(super::waiting(&world.env()), 0);
}
