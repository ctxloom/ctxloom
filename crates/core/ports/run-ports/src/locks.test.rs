use super::HeldLock;

// Rulings 86, 92: a held run lock names its file (what the enqueue journals),
// and its Debug names that path and nothing of the guard.
#[test]
fn a_held_lock_names_its_path_and_renders_it() {
    let held = HeldLock::new("/h/.ctxloom/runs/r.lock".to_string(), ());
    assert_eq!(held.path(), "/h/.ctxloom/runs/r.lock");
    assert_eq!(
        format!("{held:?}"),
        r#"HeldLock { path: "/h/.ctxloom/runs/r.lock" }"#
    );
}
