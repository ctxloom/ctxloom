//! The places the ports name: pure functions of their parts.

use std::path::Path;

use super::{session_worktrees, worktrees_root};

// Owner ruling 2026-11-12 (Q1): a project's checkouts live under ctxloom's
// home, one directory per session, outside the project.
#[test]
fn a_sessions_checkouts_are_under_the_ctxloom_home_by_project_and_harp() {
    let home = Path::new("/h");
    assert_eq!(
        worktrees_root(home, "calm-otter"),
        Path::new("/h/.ctxloom/worktrees/calm-otter")
    );
    assert_eq!(
        session_worktrees(home, "calm-otter", "amber-swift-owl"),
        Path::new("/h/.ctxloom/worktrees/calm-otter/amber-swift-owl")
    );
}
