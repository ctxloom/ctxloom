//! The daemon's scanner (step 10c U13 rework): a library tree walked and
//! hashed, no git asked, under a temporary root.

use std::time::{Duration, SystemTime};

use ctxloom_ports::LibraryScanner;

use super::{Hashed, TreeScanner};
use crate::fixtures;

// Every file of the library's paths is found, by its path relative to the
// tree with `/` between, its content id the SHA-256 of its bytes; what is
// not the library (state, sessions, cache) is not; a tree with none of
// them lists nothing. No git: the tree is in no repository.
#[test]
fn a_scan_lists_every_library_file_with_its_content_id() {
    let root = fixtures::root();
    let tree = root.root().join(".ctxloom");
    let put = |rel: &str, text: &str| {
        let at = tree.join(rel);
        fixtures::mkdirs(at.parent().unwrap());
        fixtures::put_mode(at, text, 0o644);
    };
    put("config.yaml", "schema_version: 8\n");
    put("content/bundles/v2/demo/bundle.yaml", "name: demo\n");
    put("content/bundles/v2/demo/fragments/a.md", "# A\n");
    put("state/checkout", "brisk-copper-moth\n");
    put("sessions/x/notes.md", "not the library\n");
    let scanner = TreeScanner::new(fixtures::fs());
    let found = scanner.scan(&tree.display().to_string()).unwrap();
    let want: Vec<(String, String)> = [
        ("config.yaml", "schema_version: 8\n"),
        ("content/bundles/v2/demo/bundle.yaml", "name: demo\n"),
        ("content/bundles/v2/demo/fragments/a.md", "# A\n"),
    ]
    .into_iter()
    .map(|(p, t)| (p.to_string(), library_tree::content_id(t.as_bytes())))
    .collect();
    assert_eq!(found.into_iter().collect::<Vec<_>>(), want);
    let ids = scanner
        .content_ids(
            &tree.display().to_string(),
            &["config.yaml", "gone.yaml", "content/bundles/v2/demo"],
        )
        .unwrap();
    assert_eq!(
        ids.keys().collect::<Vec<_>>(),
        ["config.yaml"],
        "a path with no file, or a directory, is absent"
    );
    let empty = root.root().join("bare");
    fixtures::mkdirs(&empty);
    assert!(scanner
        .scan(&empty.display().to_string())
        .unwrap()
        .is_empty());
}

// A file changed since its hash (its length or its time) is hashed again;
// so is one hashed within SETTLED of its change (racily clean: its stat
// may not move on a second change); a settled, unchanged one keeps it.
#[test]
fn a_hash_is_kept_only_for_a_settled_unchanged_file() {
    let at = SystemTime::UNIX_EPOCH + Duration::from_secs(1_000);
    let kept = Hashed {
        len: 4,
        modified: at,
        at: at + Duration::from_secs(5),
        id: "h".to_string(),
    };
    assert!(kept.reusable(4, at));
    assert!(!kept.reusable(5, at), "its length moved");
    assert!(
        !kept.reusable(4, at + Duration::from_secs(1)),
        "its time moved"
    );
    let racy = Hashed {
        at: at + Duration::from_millis(1_999),
        ..kept.clone()
    };
    assert!(!racy.reusable(4, at), "hashed before it settled");
    let just = Hashed {
        at: at + Duration::from_secs(2),
        ..kept
    };
    assert!(just.reusable(4, at), "settled at exactly SETTLED");
}

// A file edited after a scan is seen by the next (its stat moved), and
// `forget` drops every hash kept.
#[test]
fn an_edit_is_seen_and_forget_drops_the_memo() {
    let root = fixtures::root();
    let tree = root.root().join(".ctxloom");
    fixtures::mkdirs(&tree);
    fixtures::put_mode(tree.join("config.yaml"), "a\n", 0o644);
    let scanner = TreeScanner::new(fixtures::fs());
    let dir = tree.display().to_string();
    let first = scanner.scan(&dir).unwrap();
    fixtures::put_mode(tree.join("config.yaml"), "a longer one\n", 0o644);
    let second = scanner.scan(&dir).unwrap();
    assert_ne!(first, second);
    assert_eq!(
        second["config.yaml"],
        library_tree::content_id(b"a longer one\n")
    );
    assert!(!scanner.memo.lock().unwrap().is_empty());
    scanner.forget();
    assert!(scanner.memo.lock().unwrap().is_empty());
}

// Review 2: a link is never followed. A cloned repository's
// `bundles/v2/x -> /` must not hash the disk into the journal, a link loop
// must not fail every read (ELOOP), and a link to a file, or one above a
// library path, reads as nothing. The write side refuses a path through a
// link; a read skips it, so one hostile entry cannot refuse the rest.
#[test]
fn a_scan_skips_every_link_and_still_lists_the_rest() {
    if !fixtures::runs_scripts() {
        return;
    }
    let root = fixtures::root();
    let fs = fixtures::fs();
    let tree = root.root().join(".ctxloom");
    let outside = root.root().join("outside");
    fixtures::mkdirs(&outside);
    fixtures::put_mode(outside.join("secret.md"), "not the library\n", 0o644);
    fixtures::put_mode(outside.join("config.yaml"), "outside config\n", 0o644);
    let bundles = tree.join("content/bundles/v2");
    fixtures::mkdirs(bundles.join("demo"));
    fixtures::put_mode(bundles.join("demo/bundle.yaml"), "name: demo\n", 0o644);
    // A link to a directory outside the tree, one to the root, and a loop.
    fs.symlink_dir(&outside, &bundles.join("escape")).unwrap();
    fs.symlink_dir(std::path::Path::new("/"), &bundles.join("everything"))
        .unwrap();
    fs.symlink_dir(&bundles, &bundles.join("demo/loop"))
        .unwrap();
    // A link to a file.
    fs.symlink_dir(&outside.join("config.yaml"), &tree.join("config.yaml"))
        .unwrap();
    let scanner = TreeScanner::new(fs);
    let dir = tree.display().to_string();
    let found = scanner.scan(&dir).unwrap();
    assert_eq!(
        found.keys().collect::<Vec<_>>(),
        ["content/bundles/v2/demo/bundle.yaml"],
        "only the real file"
    );
    let ids = scanner
        .content_ids(
            &dir,
            &["config.yaml", "content/bundles/v2/demo/bundle.yaml"],
        )
        .unwrap();
    assert_eq!(
        ids.keys().collect::<Vec<_>>(),
        ["content/bundles/v2/demo/bundle.yaml"],
        "a link is no file"
    );
}

// A link ABOVE a library path (`content -> elsewhere`) is a link in the
// path: nothing below it is read.
#[test]
fn a_scan_reads_nothing_through_a_link_above_a_library_path() {
    if !fixtures::runs_scripts() {
        return;
    }
    let root = fixtures::root();
    let fs = fixtures::fs();
    let tree = root.root().join(".ctxloom");
    let outside = root.root().join("outside");
    fixtures::mkdirs(outside.join("bundles/v2/evil"));
    fixtures::put_mode(outside.join("bundles/v2/evil/bundle.yaml"), "x\n", 0o644);
    fixtures::mkdirs(&tree);
    fs.symlink_dir(&outside, &tree.join("content")).unwrap();
    let scanner = TreeScanner::new(fs);
    let dir = tree.display().to_string();
    assert!(scanner.scan(&dir).unwrap().is_empty());
    assert!(scanner
        .content_ids(&dir, &["content/bundles/v2/evil/bundle.yaml"])
        .unwrap()
        .is_empty());
}
