//! A project's places as the daemon's destroy reaches them, under a
//! temporary root (never the real home).

use daemon_api::Places;

use super::ProjectPlaces;
use crate::fixtures;

fn places(root: &file_system::Rooted) -> ProjectPlaces {
    ProjectPlaces {
        fs: fixtures::fs(),
        home: root.root().join("home"),
        temp: root.root().join("tmp"),
    }
}

// The configuration is read from the project's registered root (owner,
// 2026-11-12), whatever the daemon's own directory: its purge age there.
#[test]
fn the_purge_age_is_read_from_the_registered_root() {
    let root = fixtures::root();
    let project = root.root().join("work/calm");
    fixtures::mkdirs(project.join(".ctxloom"));
    fixtures::put_mode(
        project.join(".ctxloom/config.yaml"),
        "schema_version: 8\nsession_purge_age: 45d\n",
        0o644,
    );
    fixtures::mkdirs(root.root().join("home"));
    let mut settings = places(&root).settings(&project.display().to_string(), &[]);
    assert_eq!(settings.purge_age().unwrap(), Some("45d".to_string()));
}

// A session's files are its project's session directory.
#[test]
fn a_sessions_files_are_its_projects() {
    let root = fixtures::root();
    let project = root.root().join("work/calm");
    let dir = project.join(".ctxloom/sessions/amber-swift-owl");
    fixtures::mkdirs(dir.join("native"));
    fixtures::put_mode(dir.join("design-notes.md"), "notes", 0o644);
    let mut files = places(&root).files(&project.display().to_string());
    let rels: Vec<String> = files
        .survey("amber-swift-owl")
        .unwrap()
        .into_iter()
        .map(|item| item.rel)
        .collect();
    assert_eq!(rels, ["design-notes.md"]);
}

// Now is the clock's: after the epoch, and not a fixed instant.
#[test]
fn now_is_the_clocks() {
    let root = fixtures::root();
    assert!(places(&root).now().seconds > 1_700_000_000);
}
