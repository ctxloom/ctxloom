use std::path::Path;

use super::*;
use crate::fixtures::{fs, root};

const OLD: &str = "old-project-id";
const NEW: &str = "new-project-id";

fn entry(id: &str, path: &str) -> String {
    format!("  - project_id: {id}\n    path: {path}\n    created_at: 2026-01-01T00:00:00Z\n")
}

/// A home whose index holds `entries`, and the index's path.
fn home_with(entries: &[(&str, &str)]) -> (file_system::Rooted, std::path::PathBuf) {
    let home = root();
    let index = home.root().join(".ctxloom/projects/index.yaml");
    let body: String = entries.iter().map(|(id, path)| entry(id, path)).collect();
    let fs = fs();
    fs.create_dir_all(index.parent().unwrap(), Mode::from_bits(0o700))
        .unwrap();
    fs.write_atomic(
        &index,
        format!("schema_version: 1\nprojects:\n{body}").as_bytes(),
        Perms::Exact(Mode::PRIVATE_FILE),
    )
    .unwrap();
    (home, index)
}

#[test]
fn test_the_old_binding_of_the_root_is_taken_out_and_others_kept() {
    let (home, index) = home_with(&[(OLD, "/work/a"), ("other-id", "/work/b")]);
    let fs = fs();
    let taken = unbind_stale(&*fs, home.root(), Path::new("/work/a"), NEW).unwrap();
    assert!(taken);
    let text = fs.read_to_string(&index).unwrap();
    assert!(!text.contains(OLD), "{text}");
    assert!(text.contains("other-id"), "{text}");
    assert!(text.contains("/work/b"), "{text}");
}

#[test]
fn test_the_binding_of_the_id_kept_stays() {
    let (home, index) = home_with(&[(NEW, "/work/a")]);
    let fs = fs();
    let taken = unbind_stale(&*fs, home.root(), Path::new("/work/a"), NEW).unwrap();
    assert!(!taken, "a retry's own binding is not stale");
    assert!(fs.read_to_string(&index).unwrap().contains(NEW));
}

#[test]
fn test_a_root_nothing_binds_changes_nothing() {
    let (home, index) = home_with(&[(OLD, "/work/a")]);
    let fs = fs();
    let before = fs.read_to_string(&index).unwrap();
    let taken = unbind_stale(&*fs, home.root(), Path::new("/work/z"), NEW).unwrap();
    assert!(!taken);
    assert_eq!(fs.read_to_string(&index).unwrap(), before);
}

#[test]
fn test_no_index_is_nothing_to_take_out() {
    let home = root();
    let taken = unbind_stale(&*fs(), home.root(), Path::new("/work/a"), NEW).unwrap();
    assert!(!taken);
}

#[test]
fn test_an_index_that_does_not_parse_is_left_as_it_is() {
    let home = root();
    let index = home.root().join(".ctxloom/projects/index.yaml");
    let fs = fs();
    fs.create_dir_all(index.parent().unwrap(), Mode::from_bits(0o700))
        .unwrap();
    fs.write_atomic(
        &index,
        b"projects: [unclosed",
        Perms::Exact(Mode::PRIVATE_FILE),
    )
    .unwrap();
    let taken = unbind_stale(&*fs, home.root(), Path::new("/work/a"), NEW).unwrap();
    assert!(!taken);
    assert_eq!(fs.read_to_string(&index).unwrap(), "projects: [unclosed");
}
