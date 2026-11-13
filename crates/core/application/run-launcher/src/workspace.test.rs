//! The placement, over a scripted workspaces port.

use crate::fakes::{
    worktree_checkout as checkout, worktree_refused as refused,
    worktree_unavailable as unavailable, ScriptedWorkspaces as Scripted,
};
use isolation_ports::Request;

use super::{place_workspace, Axis};

const WORK: &str = "/h/.ctxloom/worktrees/calm-otter/amber-swift-owl";

#[test]
fn the_live_project_asks_the_port_nothing() {
    let port = Scripted::new(checkout);
    let placed = place_workspace(Axis::None, "/work/proj", WORK, "reviewer", &[], &port).unwrap();
    assert_eq!(placed.cwd, "/work/proj");
    assert_eq!(placed.checkout, None);
    assert_eq!(placed.warning, None);
    assert!(port.asked.lock().unwrap().is_empty());
}

#[test]
fn a_worktree_places_the_engine_in_its_checkout() {
    let port = Scripted::new(checkout);
    let placed =
        place_workspace(Axis::Worktree, "/work/proj", WORK, "reviewer", &[], &port).unwrap();
    let path = format!("{WORK}/reviewer");
    assert_eq!(placed.cwd, path);
    assert_eq!(placed.checkout.unwrap().path, path);
    assert_eq!(placed.warning, None);
    let asked = port.asked.lock().unwrap();
    assert_eq!(
        *asked,
        [Request {
            project_dir: "/work/proj".to_string(),
            work_dir: WORK.to_string(),
            agent: "reviewer".to_string(),
            artifact_patterns: Vec::new(),
        }]
    );
}

#[test]
fn a_worktree_that_cannot_be_had_runs_in_the_project_and_says_why() {
    let port = Scripted::new(unavailable);
    let placed =
        place_workspace(Axis::Worktree, "/work/proj", WORK, "reviewer", &[], &port).unwrap();
    assert_eq!(placed.cwd, "/work/proj");
    assert_eq!(placed.checkout, None);
    assert!(placed
        .warning
        .unwrap()
        .contains("/work/proj is not a git repository"));
}

#[test]
fn a_session_with_no_checkouts_directory_runs_in_the_project_and_says_so() {
    let port = Scripted::new(checkout);
    let placed = place_workspace(Axis::Worktree, "/work/proj", "", "reviewer", &[], &port).unwrap();
    assert_eq!(placed.cwd, "/work/proj");
    assert!(placed.warning.is_some());
    assert!(port.asked.lock().unwrap().is_empty());
}

#[test]
fn a_refused_worktree_is_the_launchs_failure() {
    let port = Scripted::new(refused);
    let error =
        place_workspace(Axis::Worktree, "/work/proj", WORK, "reviewer", &[], &port).unwrap_err();
    assert_eq!(error.code(), "run-ports-records-bad-path");
}

#[test]
fn the_axis_is_named_by_its_word_and_anything_else_is_the_project() {
    assert_eq!(Axis::of("worktree"), Axis::Worktree);
    assert_eq!(Axis::of("none"), Axis::None);
    assert_eq!(Axis::of(""), Axis::None);
    assert_eq!(Axis::Worktree.word(), "worktree");
    assert_eq!(Axis::None.word(), "none");
}

// What the launch writes into a checkout is hidden from git there.
#[test]
fn a_worktree_hides_what_the_launch_writes_into_it() {
    let port = Scripted::new(checkout);
    let patterns = vec![".claude/commands/".to_string()];
    place_workspace(
        Axis::Worktree,
        "/work/proj",
        WORK,
        "reviewer",
        &patterns,
        &port,
    )
    .unwrap();
    assert_eq!(port.asked.lock().unwrap()[0].artifact_patterns, patterns);
}
