use super::*;

// The kill points are named as the `test-die-at` switch and the feature's
// harness name them.
#[test]
fn test_each_point_is_named_as_the_kill_switch_names_it() {
    assert_eq!(Point::Staged.word(), "staged");
    assert_eq!(Point::Trashed.word(), "trashed");
}

// Staging and trash are below the tree's private state, never beside a
// target.
#[test]
fn test_staging_and_trash_are_below_the_tree_s_state() {
    assert!(STAGING_DIR.starts_with(&format!("{STATE_DIR}/")));
    assert!(TRASH_DIR.starts_with(&format!("{STATE_DIR}/")));
    assert_ne!(STAGING_DIR, TRASH_DIR);
}
