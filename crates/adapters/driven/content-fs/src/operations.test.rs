use std::cell::RefCell;
use std::path::{Path, PathBuf};
use std::sync::Arc;

use ctxloom_ports::{Operated, Operation, Operations, Point, Put};
use file_system::{Call, Fs, Perms, Rooted};
use os::Mode;

use super::TreeOperations;

const TREE: &str = "/proj/.ctxloom";
const REQUEST: &str = "01a1ecdd-5f31-75fa-8782-789facb7078e";

fn tree() -> String {
    os::host().flavor().from_top(TREE)
}

fn at(rel: &str) -> PathBuf {
    rel.split('/')
        .fold(PathBuf::from(tree()), |path, step| path.join(step))
}

fn setup() -> (Arc<Rooted>, TreeOperations) {
    let fs = Arc::new(Rooted::temp().expect("a temporary root"));
    fs.create_dir_all(&at(""), Mode::from_bits(0o755)).unwrap();
    let ops = TreeOperations::new(fs.clone());
    (fs, ops)
}

fn put(fs: &Rooted, rel: &str, bytes: &[u8]) {
    let path = at(rel);
    fs.create_dir_all(path.parent().unwrap(), Mode::from_bits(0o755))
        .unwrap();
    fs.write_atomic(&path, bytes, Perms::Exact(Mode::from_bits(0o644)))
        .unwrap();
}

fn read(fs: &Rooted, rel: &str) -> Option<Vec<u8>> {
    fs.read(&at(rel)).ok()
}

/// Every file below `rel`, relative to it, sorted.
fn files(fs: &Rooted, rel: &str) -> Vec<String> {
    fn walk(fs: &Rooted, dir: &Path, prefix: &str, out: &mut Vec<String>) {
        let Ok(entries) = fs.read_dir(dir) else {
            return;
        };
        for entry in entries {
            let name = entry.name.to_string_lossy().into_owned();
            let rel = if prefix.is_empty() {
                name.clone()
            } else {
                format!("{prefix}/{name}")
            };
            match entry.kind {
                file_system::Kind::Dir => walk(fs, &dir.join(&entry.name), &rel, out),
                _ => out.push(rel),
            }
        }
    }
    let mut out = Vec::new();
    walk(fs, &at(rel), "", &mut out);
    out.sort();
    out
}

/// Run `operation`, recording the points it reached, and at each the
/// files then under the staging.
fn operate(
    fs: &Rooted,
    ops: &TreeOperations,
    operation: &Operation,
) -> (Operated, Vec<(Point, Vec<String>)>) {
    let seen = RefCell::new(Vec::new());
    let done = ops
        .operate(&tree(), REQUEST, operation, &|point| {
            seen.borrow_mut().push((point, files(fs, "state/staging")));
        })
        .map_err(|e| e.to_string())
        .expect("the operation");
    (done, seen.into_inner())
}

fn new_bundle() -> Operation {
    Operation::CreateDir {
        path: "content/bundles/v2/notes".to_string(),
        files: vec![
            Put::file("bundle.yaml", "description: my notes\n"),
            Put::file("fragments/a.md", "a\n"),
        ],
    }
}

// A new directory is built whole under the request's staging, and only
// then renamed in: at its `staged` point the target is not there yet.
#[test]
fn test_a_new_directory_is_staged_whole_under_state_and_then_renamed_in() {
    let (fs, ops) = setup();
    let (done, seen) = operate(&fs, &ops, &new_bundle());
    assert_eq!(done, Operated::Done);
    assert_eq!(
        seen,
        vec![(
            Point::Staged,
            vec![
                format!("{REQUEST}/notes/bundle.yaml"),
                format!("{REQUEST}/notes/fragments/a.md"),
            ]
        )]
    );
    assert_eq!(
        files(&fs, "content/bundles/v2/notes"),
        vec!["bundle.yaml", "fragments/a.md"]
    );
    assert_eq!(
        read(&fs, "content/bundles/v2/notes/bundle.yaml").as_deref(),
        Some(b"description: my notes\n".as_slice())
    );
    assert!(
        files(&fs, "state/staging").is_empty(),
        "nothing staged left"
    );
}

// NOREPLACE (D4): a directory that appeared meanwhile is never renamed
// over; it is left as it was, and the staging is dropped.
#[test]
fn test_a_new_directory_is_never_renamed_over_one_that_appeared_meanwhile() {
    let (fs, ops) = setup();
    let other = RefCell::new(false);
    let done = ops
        .operate(&tree(), REQUEST, &new_bundle(), &|point| {
            if point == Point::Staged {
                put(&fs, "content/bundles/v2/notes/bundle.yaml", b"other\n");
                *other.borrow_mut() = true;
            }
        })
        .map_err(|e| e.to_string())
        .unwrap();
    assert!(other.into_inner());
    assert_eq!(
        done,
        Operated::Exists {
            path: "content/bundles/v2/notes".to_string()
        }
    );
    assert_eq!(files(&fs, "content/bundles/v2/notes"), vec!["bundle.yaml"]);
    assert_eq!(
        read(&fs, "content/bundles/v2/notes/bundle.yaml").as_deref(),
        Some(b"other\n".as_slice())
    );
    assert!(
        files(&fs, "state/staging").is_empty(),
        "nothing staged left"
    );
}

// A file is written under the staging and renamed over its target; no
// temporary is ever beside the target.
#[test]
fn test_a_file_is_replaced_from_the_staging_never_from_beside_it() {
    let (fs, ops) = setup();
    put(&fs, "content/bundles/v2/demo/fragments/a.md", b"old\n");
    let beside = RefCell::new(Vec::new());
    let done = ops
        .operate(
            &tree(),
            REQUEST,
            &Operation::Replace(Put::file("content/bundles/v2/demo/fragments/a.md", "new\n")),
            &|point| {
                assert_eq!(point, Point::Staged);
                *beside.borrow_mut() = files(&fs, "content/bundles/v2/demo/fragments");
                assert_eq!(files(&fs, "state/staging"), vec![format!("{REQUEST}/a.md")]);
            },
        )
        .map_err(|e| e.to_string())
        .unwrap();
    assert_eq!(done, Operated::Done);
    assert_eq!(
        beside.into_inner(),
        vec!["a.md"],
        "only the target beside it"
    );
    assert_eq!(
        read(&fs, "content/bundles/v2/demo/fragments/a.md").as_deref(),
        Some(b"new\n".as_slice())
    );
    assert!(files(&fs, "state/staging").is_empty());
}

// A file in a directory not yet there (a first fragment) has its
// directory made.
#[test]
fn test_a_file_is_replaced_into_a_directory_not_yet_there() {
    let (fs, ops) = setup();
    let (done, _) = operate(
        &fs,
        &ops,
        &Operation::Replace(Put::file("config.yaml", "x: 1\n")),
    );
    assert_eq!(done, Operated::Done);
    assert_eq!(
        read(&fs, "config.yaml").as_deref(),
        Some(b"x: 1\n".as_slice())
    );
}

// A removal renames the path into its request's trash slot, whole, at
// the path it had below the tree; then its `trashed` point.
#[test]
fn test_a_removal_renames_the_path_into_its_request_s_trash_slot_whole() {
    let (fs, ops) = setup();
    put(&fs, "content/bundles/v2/demo/bundle.yaml", b"d\n");
    put(&fs, "content/bundles/v2/demo/fragments/a.md", b"a\n");
    let (done, seen) = operate(
        &fs,
        &ops,
        &Operation::Trash {
            path: "content/bundles/v2/demo".to_string(),
        },
    );
    assert_eq!(done, Operated::Done);
    assert_eq!(seen, vec![(Point::Trashed, Vec::new())]);
    assert!(files(&fs, "content/bundles/v2/demo").is_empty());
    assert_eq!(
        files(&fs, &format!("state/trash/{REQUEST}")),
        vec![
            "content/bundles/v2/demo/bundle.yaml",
            "content/bundles/v2/demo/fragments/a.md"
        ]
    );
}

// A removal retried after its rename (the path in the slot, gone from the
// tree) is done; one of nothing is missing.
#[test]
fn test_a_removal_already_in_its_slot_is_done_and_one_of_nothing_is_missing() {
    let (fs, ops) = setup();
    put(&fs, "content/bundles/v2/demo/bundle.yaml", b"d\n");
    let trash = Operation::Trash {
        path: "content/bundles/v2/demo".to_string(),
    };
    assert_eq!(operate(&fs, &ops, &trash).0, Operated::Done);
    assert_eq!(operate(&fs, &ops, &trash).0, Operated::Done);
    let other = ops
        .operate(
            &tree(),
            "0199aaaa-0000-7000-8000-000000000000",
            &trash,
            &|_| {},
        )
        .map_err(|e| e.to_string())
        .unwrap();
    assert_eq!(
        other,
        Operated::Missing {
            path: "content/bundles/v2/demo".to_string()
        }
    );
}

// A move is one rename, never over what is at its target.
#[test]
fn test_a_move_is_one_rename_never_over_its_target() {
    let (fs, ops) = setup();
    put(&fs, "content/bundles/v2/a/bundle.yaml", b"a\n");
    put(&fs, "content/bundles/v2/b/bundle.yaml", b"b\n");
    let over = Operation::Move {
        from: "content/bundles/v2/a".to_string(),
        to: "content/bundles/v2/b".to_string(),
        into: None,
    };
    assert_eq!(
        operate(&fs, &ops, &over).0,
        Operated::Exists {
            path: "content/bundles/v2/b".to_string()
        }
    );
    assert_eq!(
        read(&fs, "content/bundles/v2/b/bundle.yaml").as_deref(),
        Some(b"b\n".as_slice())
    );
    let moved = Operation::Move {
        from: "content/bundles/v2/a".to_string(),
        to: "content/bundles/v2/team/c".to_string(),
        into: None,
    };
    assert_eq!(operate(&fs, &ops, &moved).0, Operated::Done);
    assert_eq!(
        read(&fs, "content/bundles/v2/team/c/bundle.yaml").as_deref(),
        Some(b"a\n".as_slice())
    );
    assert!(files(&fs, "content/bundles/v2/a").is_empty());
}

// A request's earlier staging (a try killed after staging, then retried
// by the same daemon) is dropped before it builds afresh.
#[test]
fn test_a_retry_drops_its_request_s_earlier_staging_first() {
    let (fs, ops) = setup();
    put(
        &fs,
        &format!("state/staging/{REQUEST}/notes/stale.md"),
        b"x\n",
    );
    let (done, seen) = operate(&fs, &ops, &new_bundle());
    assert_eq!(done, Operated::Done);
    assert!(
        !seen[0].1.iter().any(|f| f.ends_with("stale.md")),
        "{seen:?}"
    );
    assert_eq!(
        files(&fs, "content/bundles/v2/notes"),
        vec!["bundle.yaml", "fragments/a.md"]
    );
}

// A daemon's start sweeps the staging whole; the trash is left.
#[test]
fn test_the_sweep_drops_the_staging_and_leaves_the_trash() {
    let (fs, ops) = setup();
    put(&fs, "state/staging/a/notes/bundle.yaml", b"x\n");
    put(
        &fs,
        "state/trash/b/content/bundles/v2/demo/bundle.yaml",
        b"d\n",
    );
    ops.sweep_staging(&tree())
        .map_err(|e| e.to_string())
        .unwrap();
    assert!(files(&fs, "state/staging").is_empty());
    assert_eq!(
        files(&fs, "state/trash"),
        vec!["b/content/bundles/v2/demo/bundle.yaml"]
    );
    ops.sweep_staging(&tree())
        .map_err(|e| e.to_string())
        .expect("a sweep of nothing is done");
}

// A slot is purged whole, and only its own.
#[test]
fn test_a_purge_drops_its_slot_alone() {
    let (fs, ops) = setup();
    put(&fs, "state/trash/a/content/x.md", b"x\n");
    put(&fs, "state/trash/b/content/y.md", b"y\n");
    ops.purge_slot(&tree(), "a")
        .map_err(|e| e.to_string())
        .unwrap();
    assert_eq!(files(&fs, "state/trash"), vec!["b/content/y.md"]);
    ops.purge_slot(&tree(), "a")
        .map_err(|e| e.to_string())
        .expect("a purge of nothing is done");
}

// Step 10b U9 (T28): a sequence's last step purges its OWN slot, whole,
// and a retry of it, the slot gone, is done.
#[test]
fn test_a_purge_operation_drops_its_requests_own_slot() {
    let (fs, ops) = setup();
    put(&fs, &format!("state/trash/{REQUEST}/content/x.md"), b"x\n");
    put(&fs, "state/trash/other/content/y.md", b"y\n");
    assert_eq!(operate(&fs, &ops, &Operation::Purge).0, Operated::Done);
    assert_eq!(files(&fs, "state/trash"), vec!["other/content/y.md"]);
    assert_eq!(operate(&fs, &ops, &Operation::Purge).0, Operated::Done);
}

// A path that climbs out of the tree, or a request id that is no plain
// name, is refused before anything is touched.
#[test]
fn test_a_path_or_request_that_climbs_is_refused_and_touches_nothing() {
    let (fs, ops) = setup();
    put(&fs, "content/x.md", b"x\n");
    for (request, operation) in [
        (REQUEST, Operation::Replace(Put::file("../outside.md", "x"))),
        (
            REQUEST,
            Operation::Trash {
                path: "content/../../x".to_string(),
            },
        ),
        (
            "../escape",
            Operation::Trash {
                path: "content/x.md".to_string(),
            },
        ),
    ] {
        let refused = ops.operate(&tree(), request, &operation, &|_| {});
        assert!(refused.is_err(), "{operation:?} under {request}");
    }
    assert_eq!(
        read(&fs, "content/x.md").as_deref(),
        Some(b"x\n".as_slice())
    );
    assert!(files(&fs, "state").is_empty());
}

// A path through a link inside the tree is refused, so a rename never
// lands outside it.
#[test]
fn test_a_path_through_a_link_is_refused() {
    if os::host().flavor() != os::Flavor::Unix {
        let test = std::thread::current();
        eprintln!(
            "SKIP {}: a directory link is a junction there, made with a privilege",
            test.name().unwrap_or("a test")
        );
        return;
    }
    let (fs, ops) = setup();
    fs.create_dir_all(
        &PathBuf::from(os::host().flavor().from_top("/elsewhere")),
        Mode::from_bits(0o755),
    )
    .unwrap();
    fs.create_dir_all(&at("content"), Mode::from_bits(0o755))
        .unwrap();
    fs.symlink_dir(
        &PathBuf::from(os::host().flavor().from_top("/elsewhere")),
        &at("content/link"),
    )
    .unwrap();
    let refused = ops.operate(
        &tree(),
        REQUEST,
        &Operation::Replace(Put::file("content/link/x.md", "x")),
        &|_| {},
    );
    assert!(refused.is_err());
    assert!(fs
        .read(&PathBuf::from(
            os::host().flavor().from_top("/elsewhere/x.md")
        ))
        .is_err());
}

// Step 10b (QUESTIONS 1(a)): a move into ANOTHER tree is one rename, its
// target below that tree, never over what is there; a retry after it is
// done.
#[test]
fn test_a_move_into_another_tree_is_one_rename_below_it_never_over_its_target() {
    let (fs, ops) = setup();
    let other = os::host().flavor().from_top("/other/.ctxloom");
    fs.create_dir_all(Path::new(&other), Mode::from_bits(0o755))
        .unwrap();
    put(&fs, "content/bundles/v2/demo/bundle.yaml", b"d\n");
    let moved = Operation::Move {
        from: "content/bundles/v2/demo".to_string(),
        to: "content/bundles/v2/demo".to_string(),
        into: Some(other.clone()),
    };
    assert_eq!(operate(&fs, &ops, &moved).0, Operated::Done);
    let landed = Path::new(&other).join("content/bundles/v2/demo/bundle.yaml");
    assert_eq!(fs.read(&landed).unwrap(), b"d\n");
    assert!(files(&fs, "content/bundles/v2/demo").is_empty());
    // Retried after its rename: done, the bundle where it went.
    assert_eq!(operate(&fs, &ops, &moved).0, Operated::Done);
    put(&fs, "content/bundles/v2/demo/bundle.yaml", b"again\n");
    assert_eq!(
        operate(&fs, &ops, &moved).0,
        Operated::Exists {
            path: "content/bundles/v2/demo".to_string()
        }
    );
    assert_eq!(fs.read(&landed).unwrap(), b"d\n");
}

// ---------------------------------------------------------------- durability (owner ruling 2026-11-18, evening)

/// Operations over a recording of a temporary root: what they renamed,
/// synced and removed, in order.
fn recorded() -> (Arc<Rooted>, Arc<file_system::Recording>, TreeOperations) {
    let fs = Arc::new(Rooted::temp().expect("a temporary root"));
    fs.create_dir_all(&at(""), Mode::from_bits(0o755)).unwrap();
    let recording = Arc::new(file_system::Recording::over(fs.clone()));
    let ops = TreeOperations::new(recording.clone());
    (fs, recording, ops)
}

/// The renames, directory syncs and removals of `recording`, in order.
fn durability(recording: &file_system::Recording) -> Vec<Call> {
    recording
        .calls()
        .into_iter()
        .filter(|call| {
            matches!(
                call,
                Call::Rename { .. }
                    | Call::RenameNoreplace { .. }
                    | Call::SyncDir(_)
                    | Call::RemoveDirAll(_)
            )
        })
        .collect()
}

/// `rel` below `under` (`rel` empty: `under` itself).
fn below(under: &str, rel: &str) -> PathBuf {
    if rel.is_empty() {
        at(under)
    } else {
        at(&format!("{under}/{rel}"))
    }
}

fn staged(rel: &str) -> PathBuf {
    below(&format!("state/staging/{REQUEST}"), rel)
}

fn slot(rel: &str) -> PathBuf {
    below(&format!("state/trash/{REQUEST}"), rel)
}

// A replaced file's directory is synced after the rename, and the
// staging's (the rename's other directory), before the staging goes.
#[test]
fn test_a_replace_syncs_both_directories_of_its_rename_after_it() {
    let (_fs, recording, ops) = recorded();
    let target = "content/bundles/v2/demo/fragments/a.md";
    ops.operate(
        &tree(),
        REQUEST,
        &Operation::Replace(Put::file(target, "new\n")),
        &|_| {},
    )
    .map_err(|e| e.to_string())
    .unwrap();
    assert_eq!(
        durability(&recording),
        vec![
            Call::RemoveDirAll(staged("")),
            Call::Rename {
                from: staged("a.md"),
                to: at(target)
            },
            Call::SyncDir(at("content/bundles/v2/demo/fragments")),
            Call::SyncDir(staged("")),
            Call::RemoveDirAll(staged("")),
        ]
    );
}

#[test]
fn test_a_new_directory_syncs_both_directories_of_its_rename_after_it() {
    let (_fs, recording, ops) = recorded();
    ops.operate(&tree(), REQUEST, &new_bundle(), &|_| {})
        .map_err(|e| e.to_string())
        .unwrap();
    assert_eq!(
        durability(&recording),
        vec![
            Call::RemoveDirAll(staged("")),
            Call::RenameNoreplace {
                from: staged("notes"),
                to: at("content/bundles/v2/notes")
            },
            Call::SyncDir(at("content/bundles/v2")),
            Call::SyncDir(staged("")),
            Call::RemoveDirAll(staged("")),
        ]
    );
}

// A rename refused (something at the target) changed no directory, so
// none is synced.
#[test]
fn test_a_new_directory_refused_noreplace_syncs_nothing() {
    let (fs, recording, ops) = recorded();
    put(&fs, "content/bundles/v2/notes/bundle.yaml", b"other\n");
    ops.operate(&tree(), REQUEST, &new_bundle(), &|_| {})
        .map_err(|e| e.to_string())
        .unwrap();
    assert!(!durability(&recording)
        .iter()
        .any(|call| matches!(call, Call::SyncDir(_))));
}

#[test]
fn test_a_removal_syncs_the_slot_s_directory_then_the_path_s_after_its_rename() {
    let (fs, recording, ops) = recorded();
    put(&fs, "content/bundles/v2/demo/bundle.yaml", b"d\n");
    let path = "content/bundles/v2/demo";
    ops.operate(
        &tree(),
        REQUEST,
        &Operation::Trash {
            path: path.to_string(),
        },
        &|_| {},
    )
    .map_err(|e| e.to_string())
    .unwrap();
    assert_eq!(
        durability(&recording),
        vec![
            Call::RenameNoreplace {
                from: at(path),
                to: slot(path)
            },
            Call::SyncDir(slot("content/bundles/v2")),
            Call::SyncDir(at("content/bundles/v2")),
        ]
    );
}

#[test]
fn test_a_move_syncs_both_directories_of_its_rename_and_one_shared_once() {
    let (fs, recording, ops) = recorded();
    put(&fs, "content/bundles/v2/a/bundle.yaml", b"a\n");
    let across = Operation::Move {
        from: "content/bundles/v2/a".to_string(),
        to: "content/bundles/v2/team/c".to_string(),
        into: None,
    };
    ops.operate(&tree(), REQUEST, &across, &|_| {})
        .map_err(|e| e.to_string())
        .unwrap();
    let beside = Operation::Move {
        from: "content/bundles/v2/team/c".to_string(),
        to: "content/bundles/v2/team/d".to_string(),
        into: None,
    };
    ops.operate(&tree(), REQUEST, &beside, &|_| {})
        .map_err(|e| e.to_string())
        .unwrap();
    assert_eq!(
        durability(&recording),
        vec![
            Call::RenameNoreplace {
                from: at("content/bundles/v2/a"),
                to: at("content/bundles/v2/team/c")
            },
            Call::SyncDir(at("content/bundles/v2/team")),
            Call::SyncDir(at("content/bundles/v2")),
            Call::RenameNoreplace {
                from: at("content/bundles/v2/team/c"),
                to: at("content/bundles/v2/team/d")
            },
            Call::SyncDir(at("content/bundles/v2/team")),
        ]
    );
}

// A purge takes its slot out of the trash directory, which is synced
// after; a purge of nothing (a retry, a tree with no trash) syncs nothing.
#[test]
fn test_a_purge_syncs_the_trash_directory_after_its_slot_goes() {
    let (fs, recording, ops) = recorded();
    put(&fs, &format!("state/trash/{REQUEST}/content/x.md"), b"x\n");
    put(&fs, "state/trash/older/content/y.md", b"y\n");
    ops.operate(&tree(), REQUEST, &Operation::Purge, &|_| {})
        .map_err(|e| e.to_string())
        .unwrap();
    ops.purge_slot(&tree(), "older")
        .map_err(|e| e.to_string())
        .unwrap();
    ops.operate(&tree(), REQUEST, &Operation::Purge, &|_| {})
        .map_err(|e| e.to_string())
        .unwrap();
    assert_eq!(
        durability(&recording),
        vec![
            Call::RemoveDirAll(slot("")),
            Call::SyncDir(at("state/trash")),
            Call::RemoveDirAll(at("state/trash/older")),
            Call::SyncDir(at("state/trash")),
            Call::RemoveDirAll(slot("")),
        ]
    );
}

#[test]
fn test_a_purge_in_a_tree_with_no_trash_is_done_and_syncs_nothing() {
    let (_fs, recording, ops) = recorded();
    ops.purge_slot(&tree(), "older")
        .map_err(|e| e.to_string())
        .expect("a purge of nothing is done");
    assert_eq!(
        durability(&recording),
        vec![Call::RemoveDirAll(at("state/trash/older"))]
    );
}

// A directory that cannot be synced fails the operation, said in this
// crate's words with the OS's. Unix: a directory one may rename into but
// not open (write and search, no read) is renamed into, then not synced.
#[test]
fn test_a_directory_that_cannot_be_synced_fails_the_operation() {
    if os::host().flavor() != os::Flavor::Unix {
        return;
    }
    let (fs, ops) = setup();
    put(&fs, "content/bundles/v2/a/bundle.yaml", b"a\n");
    let locked = at("content/bundles/v2/locked");
    fs.create_dir_all(&locked, Mode::from_bits(0o755)).unwrap();
    fs.set_permissions(&locked, Mode::from_bits(0o300)).unwrap();
    let failed = ops.operate(
        &tree(),
        REQUEST,
        &Operation::Move {
            from: "content/bundles/v2/a".to_string(),
            to: "content/bundles/v2/locked/a".to_string(),
            into: None,
        },
        &|_| {},
    );
    fs.set_permissions(&locked, Mode::from_bits(0o755)).unwrap();
    let failed = failed.expect_err("the sync fails the move");
    assert_eq!(failed.code(), "content-fs-sync-dir");
    assert!(failed.to_string().contains("locked"), "{failed}");
}

// region another tool's files (step 10c U10, L15)

/// `rel` below the project (the tree's parent), as an absolute path.
fn in_project(rel: &str) -> String {
    rel.split('/')
        .fold(
            PathBuf::from(tree()).parent().unwrap().to_path_buf(),
            |path, step| path.join(step),
        )
        .display()
        .to_string()
}

fn outside(rel: &str, bytes: Option<&[u8]>) -> Operation {
    Operation::Outside {
        path: in_project(rel),
        bytes: bytes.map(<[u8]>::to_vec),
        executable: false,
        keep: in_project(""),
    }
}

#[test]
fn another_tools_file_is_written_whole_where_it_is_named_and_stages_nothing_in_the_tree() {
    let (fs, ops) = setup();
    let (done, points) = operate(&fs, &ops, &outside("out/CLAUDE.md", Some(b"# ctx\n")));
    assert_eq!(done, Operated::Done);
    assert!(points.is_empty(), "{points:?}");
    assert_eq!(
        fs.read(Path::new(&in_project("out/CLAUDE.md"))).ok(),
        Some(b"# ctx\n".to_vec())
    );
    assert!(files(&fs, "state").is_empty(), "{:?}", files(&fs, "state"));
    // Written again: the same bytes, nothing else changed.
    let (again, _) = operate(&fs, &ops, &outside("out/CLAUDE.md", Some(b"# ctx\n")));
    assert_eq!(again, Operated::Done);
    let (replaced, _) = operate(&fs, &ops, &outside("out/CLAUDE.md", Some(b"# two\n")));
    assert_eq!(replaced, Operated::Done);
    assert_eq!(
        fs.read(Path::new(&in_project("out/CLAUDE.md"))).ok(),
        Some(b"# two\n".to_vec())
    );
}

#[test]
fn another_tools_file_removed_takes_the_directories_it_leaves_empty_but_never_what_it_keeps() {
    let (fs, ops) = setup();
    operate(&fs, &ops, &outside("a/b/c/settings.json", Some(b"{}")));
    operate(&fs, &ops, &outside("a/other.md", Some(b"x")));
    let (done, _) = operate(&fs, &ops, &outside("a/b/c/settings.json", None));
    assert_eq!(done, Operated::Done);
    assert!(
        fs.metadata(Path::new(&in_project("a/b"))).is_err(),
        "a/b is left"
    );
    assert!(fs.metadata(Path::new(&in_project("a/other.md"))).is_ok());
    // Removed again: nothing there is done.
    assert_eq!(
        operate(&fs, &ops, &outside("a/b/c/settings.json", None)).0,
        Operated::Done
    );
    operate(&fs, &ops, &outside("a/other.md", None));
    assert!(
        fs.metadata(Path::new(&in_project("a"))).is_err(),
        "a is left"
    );
    assert!(
        fs.metadata(Path::new(&in_project(""))).is_ok(),
        "the kept directory went"
    );
}

#[test]
fn another_tools_file_must_be_named_absolutely() {
    let (_fs, ops) = setup();
    let relative = Operation::Outside {
        path: "CLAUDE.md".to_string(),
        bytes: Some(b"x".to_vec()),
        executable: false,
        keep: String::new(),
    };
    assert!(ops.operate(&tree(), REQUEST, &relative, &|_| {}).is_err());
}

#[test]
fn another_tools_file_removed_never_takes_the_directory_it_keeps_though_it_is_left_empty() {
    let (fs, ops) = setup();
    let in_kept = |bytes: Option<&[u8]>| Operation::Outside {
        path: in_project("kept/inner/x.md"),
        bytes: bytes.map(<[u8]>::to_vec),
        executable: false,
        keep: in_project("kept"),
    };
    operate(&fs, &ops, &in_kept(Some(b"x")));
    operate(&fs, &ops, &in_kept(None));
    assert!(fs.metadata(Path::new(&in_project("kept/inner"))).is_err());
    assert!(
        fs.metadata(Path::new(&in_project("kept"))).is_ok(),
        "the kept directory went"
    );
}

/// The staging a root create uses, beside the tree (step 10c U6).
fn beside(rel: &str) -> PathBuf {
    let flavor = os::host().flavor();
    rel.split('/').fold(
        PathBuf::from(flavor.from_top(&format!("{TREE}.staging"))),
        |path, step| path.join(step),
    )
}

/// Every file below `dir`, relative to it, sorted.
fn files_at(fs: &Rooted, dir: &Path) -> Vec<String> {
    fn walk(fs: &Rooted, dir: &Path, prefix: &str, out: &mut Vec<String>) {
        let Ok(entries) = fs.read_dir(dir) else {
            return;
        };
        for entry in entries {
            let name = entry.name.to_string_lossy().into_owned();
            let rel = if prefix.is_empty() {
                name.clone()
            } else {
                format!("{prefix}/{name}")
            };
            match entry.kind {
                file_system::Kind::Dir => walk(fs, &dir.join(&entry.name), &rel, out),
                _ => out.push(rel),
            }
        }
    }
    let mut out = Vec::new();
    walk(fs, dir, "", &mut out);
    out.sort();
    out
}

fn new_tree() -> Operation {
    Operation::CreateDir {
        path: String::new(),
        files: vec![
            Put::file("config.yaml", "schema_version: 9\n"),
            Put::file("state/checkout", "amber-swift-owl\n"),
        ],
    }
}

// Step 10c U6: a tree created whole (a first init's `.ctxloom`) cannot be
// staged inside itself: it is built under `<tree>.staging/<request>/`,
// beside it, and renamed in; at its `staged` point the tree is not there.
#[test]
fn test_a_new_tree_is_staged_whole_beside_it_and_then_renamed_in() {
    let (fs, ops) = setup();
    fs.remove_dir_all(&at("")).unwrap();
    let seen = RefCell::new(Vec::new());
    let done = ops
        .operate(&tree(), REQUEST, &new_tree(), &|point| {
            seen.borrow_mut().push((
                point,
                files_at(&fs, &beside("")),
                fs.exists(&at("")).unwrap(),
            ));
        })
        .map_err(|e| e.to_string())
        .unwrap();
    assert_eq!(done, Operated::Done);
    assert_eq!(
        seen.into_inner(),
        vec![(
            Point::Staged,
            vec![
                format!("{REQUEST}/config.yaml"),
                format!("{REQUEST}/state/checkout"),
            ],
            false
        )]
    );
    assert_eq!(files(&fs, ""), vec!["config.yaml", "state/checkout"]);
    assert!(!fs.exists(&beside("")).unwrap(), "nothing staged left");
}

// NOREPLACE: a tree that appeared meanwhile is never renamed over.
#[test]
fn test_a_new_tree_is_never_renamed_over_one_that_appeared_meanwhile() {
    let (fs, ops) = setup();
    fs.remove_dir_all(&at("")).unwrap();
    let done = ops
        .operate(&tree(), REQUEST, &new_tree(), &|point| {
            if point == Point::Staged {
                put(&fs, "config.yaml", b"theirs\n");
            }
        })
        .map_err(|e| e.to_string())
        .unwrap();
    assert_eq!(
        done,
        Operated::Exists {
            path: String::new()
        }
    );
    assert_eq!(files(&fs, ""), vec!["config.yaml"]);
    assert_eq!(
        read(&fs, "config.yaml").as_deref(),
        Some(b"theirs\n".as_slice())
    );
    assert!(!fs.exists(&beside("")).unwrap(), "nothing staged left");
}

// What a killed root create left beside the tree is swept with its
// staging.
#[test]
fn test_the_sweep_drops_a_new_tree_s_staging_beside_it() {
    let (fs, ops) = setup();
    let left = beside(&format!("{REQUEST}/config.yaml"));
    fs.create_dir_all(left.parent().unwrap(), Mode::from_bits(0o755))
        .unwrap();
    fs.write_atomic(&left, b"x\n", Perms::Exact(Mode::from_bits(0o644)))
        .unwrap();
    ops.sweep_staging(&tree())
        .map_err(|e| e.to_string())
        .unwrap();
    assert!(!fs.exists(&beside("")).unwrap());
}

/// A directory outside the tree, and `rel` (below the tree) a link to it.
fn linked(fs: &Rooted, rel: &str) -> PathBuf {
    let elsewhere = PathBuf::from(os::host().flavor().from_top("/elsewhere"));
    fs.create_dir_all(&elsewhere, Mode::from_bits(0o755))
        .unwrap();
    if let Some(parent) = at(rel).parent() {
        fs.create_dir_all(parent, Mode::from_bits(0o755)).unwrap();
    }
    fs.symlink_dir(&elsewhere, &at(rel)).unwrap();
    elsewhere
}

fn skip_off_unix() -> bool {
    if os::host().flavor() == os::Flavor::Unix {
        return false;
    }
    let test = std::thread::current();
    eprintln!(
        "SKIP {}: a directory link is a junction there, made with a privilege",
        test.name().unwrap_or("a test")
    );
    true
}

// Review 9: a cloned repository's `.ctxloom/state -> /elsewhere` (or its
// `state/staging`, `state/trash`) must make ctxloom create nothing and remove
// nothing there: every operation that builds a path below `state/` refuses a
// link at it, with a typed diagnostic and a fix.
#[test]
fn test_a_link_at_state_is_refused_by_every_operation_that_uses_it() {
    if skip_off_unix() {
        return;
    }
    for link in ["state", "state/staging", "state/trash"] {
        let (fs, ops) = setup();
        let elsewhere = linked(&fs, link);
        fs.write_atomic(
            &elsewhere.join("keep.txt"),
            b"theirs",
            Perms::Exact(Mode::from_bits(0o644)),
        )
        .unwrap();
        put(&fs, "content/bundles/v2/demo/bundle.yaml", b"d\n");
        let stages = [
            Operation::Replace(Put::file("config.yaml", "x")),
            Operation::CreateDir {
                path: "content/bundles/v2/new".to_string(),
                files: vec![Put::file("bundle.yaml", "n")],
            },
        ];
        let trashes = [
            Operation::Trash {
                path: "content/bundles/v2/demo".to_string(),
            },
            Operation::Move {
                from: "content/bundles/v2/demo".to_string(),
                to: "content/bundles/v2/other".to_string(),
                into: None,
            },
            Operation::Purge,
        ];
        // Each operation is refused for the links it goes through: staging
        // writes use `state` and `state/staging`, the trash `state` and
        // `state/trash`.
        let refused_here: Vec<&Operation> = match link {
            "state" => stages.iter().chain(trashes.iter()).collect(),
            "state/staging" => stages.iter().collect(),
            _ => trashes.iter().collect(),
        };
        for operation in refused_here {
            let refused = ops
                .operate(&tree(), REQUEST, operation, &|_| {})
                .expect_err(&format!("{link}: {operation:?}"));
            assert_eq!(
                refused.code(),
                "content-fs-state-link",
                "{link}: {operation:?}"
            );
            assert!(refused.to_string().contains(link), "{refused}");
        }
        if link != "state/trash" {
            let swept = ops.sweep_staging(&tree()).expect_err(link);
            assert_eq!(swept.code(), "content-fs-state-link");
        }
        if link != "state/staging" {
            let purged = ops.purge_slot(&tree(), REQUEST).expect_err(link);
            assert_eq!(purged.code(), "content-fs-state-link");
        }
        // Nothing was made or removed through the link, and the tree's own
        // content was not touched.
        assert_eq!(fs.read(&elsewhere.join("keep.txt")).unwrap(), b"theirs");
        assert_eq!(fs.read_dir(&elsewhere).unwrap().len(), 1, "{link}");
        assert!(read(&fs, "content/bundles/v2/demo/bundle.yaml").is_some());
    }
}

// The same for the staging beside the tree (`<tree>.staging`, a first init's
// whole tree): a link there is refused, and swept by nobody.
#[test]
fn test_a_link_at_the_staging_beside_the_tree_is_refused() {
    if skip_off_unix() {
        return;
    }
    let (fs, ops) = setup();
    let elsewhere = PathBuf::from(os::host().flavor().from_top("/elsewhere"));
    fs.create_dir_all(&elsewhere, Mode::from_bits(0o755))
        .unwrap();
    fs.write_atomic(
        &elsewhere.join("keep.txt"),
        b"theirs",
        Perms::Exact(Mode::from_bits(0o644)),
    )
    .unwrap();
    let beside = PathBuf::from(format!("{}.staging", tree()));
    fs.symlink_dir(&elsewhere, &beside).unwrap();
    let whole = Operation::CreateDir {
        path: String::new(),
        files: vec![Put::file("config.yaml", "c")],
    };
    let refused = ops
        .operate(&tree(), REQUEST, &whole, &|_| {})
        .expect_err("a made whole tree stages beside it");
    assert_eq!(refused.code(), "content-fs-state-link");
    let swept = ops.sweep_staging(&tree()).expect_err("sweeping it");
    assert_eq!(swept.code(), "content-fs-state-link");
    assert_eq!(fs.read(&elsewhere.join("keep.txt")).unwrap(), b"theirs");
    assert_eq!(fs.read_dir(&elsewhere).unwrap().len(), 1);
}
