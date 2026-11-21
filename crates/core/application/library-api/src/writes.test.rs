//! A planned write shaped into its one step, its command named by git's
//! ids, and what it left read back.

use ctxloom_ports::{Change, Put, Scope};
use library_application::write::recording::Planned;
use library_tree::{DirtyPath, Evidence, LibraryTreeRejection};

use super::{
    content_for, expected, expected_step, intent_of, left_by, operation, operation_of, shape,
    Intended, Lands, Shape,
};
use crate::fakes::Tree;

fn put(path: &str, text: &str) -> Put {
    Put {
        path: path.to_string(),
        bytes: text.as_bytes().to_vec(),
        executable: false,
    }
}

fn none(_: &str, _: &str) -> Vec<String> {
    Vec::new()
}

#[test]
fn test_nothing_planned_is_no_step() {
    assert_eq!(shape(Vec::new(), none), Ok(None));
    let empty = Planned::Changed {
        bundle: "demo".to_string(),
        changes: Vec::new(),
    };
    assert_eq!(shape(vec![empty], none), Ok(None));
}

#[test]
fn test_a_new_bundle_is_one_new_directory_of_its_files() {
    let planned = Planned::NewBundle {
        bundle: "notes".to_string(),
        files: vec![put("bundle.yaml", "v: 1\n")],
    };
    let shaped = shape(vec![planned], none).unwrap().unwrap();
    assert_eq!(shaped.lands, Lands::Content);
    assert_eq!(
        shaped.shape,
        Shape::Create {
            path: "content/bundles/v2/notes".to_string(),
            files: vec![put("bundle.yaml", "v: 1\n")],
        }
    );
}

#[test]
fn test_a_layer_is_its_config_file_replaced_in_its_tree() {
    let planned = Planned::Layer {
        scope: Scope::Home,
        text: "a: 1\n".to_string(),
    };
    let shaped = shape(vec![planned], none).unwrap().unwrap();
    assert_eq!(shaped.lands, Lands::Layer(Scope::Home));
    assert_eq!(shaped.shape.path(), "config.yaml");
}

// Step 10c U5: a file the scaffold writes below the tree's `.ctxloom`
// (`manage gitignore install`'s) is that one file replaced in the content
// tree; several are more than one change.
#[test]
fn test_a_scaffold_file_is_that_file_replaced_in_the_content_tree() {
    let one = Planned::TreeFiles {
        files: vec![put(".gitignore", "/cache\n")],
    };
    let shaped = shape(vec![one], none).unwrap().unwrap();
    assert_eq!(shaped.lands, Lands::Content);
    assert_eq!(
        shaped.shape,
        Shape::Replace {
            path: ".gitignore".to_string(),
            bytes: b"/cache\n".to_vec(),
            executable: false,
        }
    );
    let several = Planned::TreeFiles {
        files: vec![put(".gitignore", "a"), put("remotes.yaml", "b")],
    };
    assert!(matches!(
        shape(vec![several], none),
        Err(LibraryTreeRejection::OneChangePerCall { paths }) if paths == [".gitignore", "remotes.yaml"]
    ));
}

// Step 10c U6: a tree written whole where it was not (a first init's
// scaffold) is ONE new directory, the tree's root itself, in the content
// tree.
#[test]
fn test_a_new_tree_is_one_new_directory_at_the_trees_root() {
    let files = vec![put("config.yaml", "a"), put("remotes.yaml", "b")];
    let shaped = shape(
        vec![Planned::NewTree {
            files: files.clone(),
        }],
        none,
    )
    .unwrap()
    .unwrap();
    assert_eq!(shaped.lands, Lands::Content);
    assert_eq!(
        shaped.shape,
        Shape::Create {
            path: String::new(),
            files
        }
    );
}

// What a root create leaves is each of its files at its own path (no
// leading separator), the tree's private state left out.
#[test]
fn test_a_root_create_expects_each_file_at_its_own_path() {
    let root = Shape::Create {
        path: String::new(),
        files: vec![put("config.yaml", "a"), put("state/checkout", "c\n")],
    };
    let Intended::Create(create) = intent_of("t", "init", &root, (0, &|_| 0), evidence(), "r")
    else {
        panic!()
    };
    let step = library_tree::Step {
        create: Some(create),
        replace: None,
        trash: None,
        r#move: None,
        purge: None,
    };
    assert_eq!(
        expected_step(&step, Some(&root)),
        [DirtyPath {
            path: "config.yaml".to_string(),
            oid: library_tree::content_id(b"a"),
        }]
    );
}

#[test]
fn test_one_file_written_or_taken_away_is_that_one_step() {
    let write = Planned::Changed {
        bundle: "demo".to_string(),
        changes: vec![Change::Write(put("fragments/rules.md", "be kind"))],
    };
    assert_eq!(
        shape(vec![write], none).unwrap().unwrap().shape,
        Shape::Replace {
            path: "content/bundles/v2/demo/fragments/rules.md".to_string(),
            bytes: b"be kind".to_vec(),
            executable: false,
        }
    );
    let gone = Planned::Changed {
        bundle: "demo".to_string(),
        changes: vec![Change::Remove {
            path: "fragments/rules.md".to_string(),
        }],
    };
    assert_eq!(
        shape(vec![gone], none).unwrap().unwrap().shape,
        Shape::Trash {
            path: "content/bundles/v2/demo/fragments/rules.md".to_string(),
            files: Vec::new(),
        }
    );
}

#[test]
fn test_a_bundle_taken_away_lists_its_files() {
    let gone = Planned::BundleGone {
        bundle: "demo".to_string(),
    };
    let files = |bundle: &str, dir: &str| {
        assert_eq!((bundle, dir), ("demo", ""));
        vec!["bundle.yaml".to_string()]
    };
    assert_eq!(
        shape(vec![gone], files).unwrap().unwrap().shape,
        Shape::Trash {
            path: "content/bundles/v2/demo".to_string(),
            files: vec!["bundle.yaml".to_string()],
        }
    );
}

#[test]
fn test_several_files_into_a_directory_nobody_has_are_one_new_directory() {
    let skill = Planned::Changed {
        bundle: "demo".to_string(),
        changes: vec![
            Change::Write(put("skills/review/SKILL.md", "s")),
            Change::Write(put("skills/review/.meta.yaml", "m")),
        ],
    };
    assert_eq!(
        shape(vec![skill.clone()], none).unwrap().unwrap().shape,
        Shape::Create {
            path: "content/bundles/v2/demo/skills/review".to_string(),
            files: vec![put("SKILL.md", "s"), put(".meta.yaml", "m"),],
        }
    );
    let there = |_: &str, _: &str| vec!["SKILL.md".to_string()];
    assert_eq!(
        shape(vec![skill], there),
        Err(LibraryTreeRejection::OneChangePerCall {
            paths: vec![
                "skills/review/SKILL.md".to_string(),
                "skills/review/.meta.yaml".to_string()
            ]
        })
    );
}

#[test]
fn test_two_files_of_an_existing_bundle_are_one_change_per_call() {
    let two = Planned::Changed {
        bundle: "demo".to_string(),
        changes: vec![
            Change::Write(put("fragments/a.md", "a")),
            Change::Write(put("bundle.yaml", "y")),
        ],
    };
    assert_eq!(
        shape(vec![two], none),
        Err(LibraryTreeRejection::OneChangePerCall {
            paths: vec!["fragments/a.md".to_string(), "bundle.yaml".to_string()]
        })
    );
    let two_writes = vec![
        Planned::Layer {
            scope: Scope::Home,
            text: String::new(),
        },
        Planned::BundleGone {
            bundle: "demo".to_string(),
        },
    ];
    assert!(matches!(
        shape(two_writes, none),
        Err(LibraryTreeRejection::OneChangePerCall { .. })
    ));
}

fn evidence() -> Evidence {
    Evidence {
        tree_reads: true,
        plan_rejection: None,
        builtin: false,
    }
}

#[test]
fn test_an_intent_names_the_content_ids_of_what_it_writes() {
    let create = Shape::Create {
        path: "content/bundles/v2/notes".to_string(),
        files: vec![put("bundle.yaml", "v")],
    };
    let intended = intent_of("t", "bundle create", &create, (0, &|_| 9), evidence(), "r");
    let Intended::Create(command) = &intended else {
        panic!("{intended:?}")
    };
    assert_eq!(command.verb, "bundle create");
    assert_eq!(
        command.entries,
        vec![DirtyPath {
            path: "bundle.yaml".to_string(),
            oid: library_tree::content_id(b"v"),
        }]
    );
    assert_eq!(
        expected(&intended, &create),
        vec![DirtyPath {
            path: "content/bundles/v2/notes/bundle.yaml".to_string(),
            oid: library_tree::content_id(b"v"),
        }]
    );
    let replace = Shape::Replace {
        path: "config.yaml".to_string(),
        bytes: b"a".to_vec(),
        executable: false,
    };
    let Intended::Replace(command) =
        intent_of("t", "config set", &replace, (4, &|_| 9), evidence(), "r")
    else {
        panic!()
    };
    assert_eq!(command.seen_sequence, 4);
    assert_eq!(command.new_oid, library_tree::content_id(b"a"));
}

#[test]
fn test_a_trash_expects_each_of_its_files_gone_and_what_is_left_is_read_back() {
    let git = Tree::default().file("content/bundles/v2/demo/kept.md", "k");
    let trash = Shape::Trash {
        path: "content/bundles/v2/demo".to_string(),
        files: vec!["bundle.yaml".to_string(), "kept.md".to_string()],
    };
    let intended = intent_of("t", "bundle remove", &trash, (2, &|_| 9), evidence(), "r");
    let paths = expected(&intended, &trash);
    assert_eq!(paths.len(), 2);
    assert!(paths.iter().all(|p| p.oid.is_empty()));
    let left = left_by(&git, "/d", &paths).unwrap();
    assert_eq!(left[0].oid, "");
    assert_eq!(left[1].oid, "k", "a file still there is read back");
}

#[test]
fn test_a_writes_book_carries_its_request_id_as_the_command_id() {
    use angzarr_embedded::v1::command_page::Body;
    let tree = library_tree::TreeName::home("brisk-copper-moth").unwrap();
    let replace = Shape::Replace {
        path: "config.yaml".to_string(),
        bytes: b"a".to_vec(),
        executable: false,
    };
    let intended = intent_of("t", "config set", &replace, (0, &|_| 9), evidence(), "r");
    let id = [7u8; 16];
    let book = super::command_book(&tree, &intended, id);
    let page = book.page.unwrap();
    assert_eq!(
        page.command_id,
        Some(angzarr_embedded::v1::Uuid::from_bytes(id))
    );
    let Some(Body::Command(command)) = page.body else {
        panic!()
    };
    assert!(command.payload.unwrap().type_url.ends_with("ReplaceFile"));
    assert_eq!(book.cover.unwrap().root, Some(tree.root()));
    let fact = library_tree::RecordStepDone {
        request_id: "r".to_string(),
        step: 0,
        observed: Vec::new(),
    };
    let book = super::step_book(&tree, &fact, "done:r:0");
    let Some(Body::Fact(fact)) = book.page.unwrap().body else {
        panic!()
    };
    assert_eq!(fact.origin.unwrap().external_id, "done:r:0");
}

// The step is made by the one atomic operation of its kind (step 10b U8):
// a file replaced, a new directory created, a path trashed, each at the
// step's path below its tree.
#[test]
fn test_each_step_is_made_by_the_atomic_operation_of_its_kind() {
    use ctxloom_ports::Operation;
    let replace = Shape::Replace {
        path: "config.yaml".to_string(),
        bytes: b"a: 1\n".to_vec(),
        executable: false,
    };
    assert_eq!(
        operation(&replace),
        Some(Operation::Replace(put("config.yaml", "a: 1\n")))
    );
    let create = Shape::Create {
        path: "content/bundles/v2/notes".to_string(),
        files: vec![put("bundle.yaml", "v")],
    };
    assert_eq!(
        operation(&create),
        Some(Operation::CreateDir {
            path: "content/bundles/v2/notes".to_string(),
            files: vec![put("bundle.yaml", "v")],
        })
    );
    let trash = Shape::Trash {
        path: "content/bundles/v2/demo".to_string(),
        files: vec!["bundle.yaml".to_string()],
    };
    assert_eq!(
        operation(&trash),
        Some(Operation::Trash {
            path: "content/bundles/v2/demo".to_string()
        })
    );
    assert_eq!(operation(&Shape::Purge), Some(Operation::Purge));
    assert_eq!(operation(&Shape::Sequence(vec![Shape::Purge])), None);
}

// A file the use case wrote executable stays so, written whole or in a
// new directory.
#[test]
fn test_an_executable_file_stays_executable_in_its_operation() {
    use ctxloom_ports::Operation;
    let run = Put {
        executable: true,
        ..put("skills/review/scripts/run.sh", "#!/bin/sh\n")
    };
    let write = Planned::Changed {
        bundle: "demo".to_string(),
        changes: vec![Change::Write(run.clone())],
    };
    let shaped = shape(vec![write], none).unwrap().unwrap();
    let Some(Operation::Replace(made)) = operation(&shaped.shape) else {
        panic!("{shaped:?}")
    };
    assert!(made.executable);
    let new = Planned::Changed {
        bundle: "demo".to_string(),
        changes: vec![
            Change::Write(put("skills/review/SKILL.md", "s")),
            Change::Write(run),
        ],
    };
    let shaped = shape(vec![new], none).unwrap().unwrap();
    let Some(Operation::CreateDir { files, .. }) = operation(&shaped.shape) else {
        panic!("{shaped:?}")
    };
    assert_eq!(
        files
            .iter()
            .map(|f| (f.path.as_str(), f.executable))
            .collect::<Vec<_>>(),
        vec![("SKILL.md", false), ("scripts/run.sh", true)]
    );
}

// G12: a lone removal's trash is kept 7 days (then purged by the daemon);
// only a sequence's own trash, which its last step removes, is not.
#[test]
fn test_a_lone_removal_is_kept_in_the_trash() {
    let trash = Shape::Trash {
        path: "content/bundles/v2/demo".to_string(),
        files: vec!["bundle.yaml".to_string()],
    };
    let Intended::Trash(command) =
        intent_of("t", "bundle remove", &trash, (3, &|_| 9), evidence(), "r")
    else {
        panic!("not a trash")
    };
    assert!(command.keep);
    assert_eq!(command.seen_sequence, 3);
}

/// The files of the bundle `demo` now, below it.
fn demo_files(bundle: &str, dir: &str) -> Vec<String> {
    match (bundle, dir) {
        ("demo", "") => vec!["bundle.yaml".to_string(), "fragments/old.md".to_string()],
        _ => Vec::new(),
    }
}

/// A forced import's plan: the old bundle gone, a new one of every file.
fn forced_import() -> Vec<Planned> {
    vec![
        Planned::BundleGone {
            bundle: "demo".to_string(),
        },
        Planned::NewBundle {
            bundle: "demo".to_string(),
            files: vec![put("bundle.yaml", "v: 2\n")],
        },
    ]
}

// Step 10b U9 (T28): a bundle taken away and made anew in one plan (a
// forced import) is the sequence: the old tree into the trash, the new
// one renamed in, then the trash slot removed.
#[test]
fn test_a_bundle_replaced_whole_is_the_sequence_trash_create_purge() {
    let shaped = shape(forced_import(), demo_files).unwrap().unwrap();
    assert_eq!(shaped.lands, Lands::Content);
    assert_eq!(
        shaped.shape,
        Shape::Sequence(vec![
            Shape::Trash {
                path: "content/bundles/v2/demo".to_string(),
                files: vec!["bundle.yaml".to_string(), "fragments/old.md".to_string()],
            },
            Shape::Create {
                path: "content/bundles/v2/demo".to_string(),
                files: vec![put("bundle.yaml", "v: 2\n")],
            },
            Shape::Purge,
        ])
    );
    assert_eq!(shaped.shape.path(), "content/bundles/v2/demo");
    // A bundle gone and ANOTHER made is two changes, refused as ever.
    let two = vec![
        Planned::BundleGone {
            bundle: "demo".to_string(),
        },
        Planned::NewBundle {
            bundle: "notes".to_string(),
            files: vec![put("bundle.yaml", "v")],
        },
    ];
    assert!(matches!(
        shape(two, demo_files),
        Err(LibraryTreeRejection::OneChangePerCall { .. })
    ));
}

// A sequence's intent lists its steps in order: its trash NOT kept (its
// last step removes it, G12(a)), its last the purge of the request's own
// slot, its first path judged on the token the caller saw.
#[test]
fn test_a_sequences_intent_lists_its_steps_its_trash_its_own() {
    let shaped = shape(forced_import(), demo_files).unwrap().unwrap();
    let intended = intent_of(
        "t",
        "bundle import",
        &shaped.shape,
        (5, &|_| 9),
        evidence(),
        "r-1",
    );
    let Intended::Sequence(command) = &intended else {
        panic!("{intended:?}")
    };
    assert_eq!(command.verb, "bundle import");
    let [trash, create, purge] = &command.steps[..] else {
        panic!("{:?}", command.steps)
    };
    let trash = trash.trash.as_ref().expect("a trash first");
    assert_eq!(
        (trash.path.as_str(), trash.keep, trash.seen_sequence),
        ("content/bundles/v2/demo", false, 5)
    );
    let create = create.create.as_ref().expect("a create second");
    assert_eq!(
        create.entries,
        vec![DirtyPath {
            path: "bundle.yaml".to_string(),
            oid: library_tree::content_id(b"v: 2\n"),
        }]
    );
    assert_eq!(
        purge.purge.as_ref().map(|p| p.request_id.as_str()),
        Some("r-1")
    );
    // What each step leaves: the old files gone, the new one's id, nothing.
    let Shape::Sequence(shapes) = &shaped.shape else {
        panic!()
    };
    let left: Vec<Vec<DirtyPath>> = command
        .steps
        .iter()
        .zip(shapes)
        .map(|(step, shape)| expected_step(step, Some(shape)))
        .collect();
    assert_eq!(
        left[0],
        vec![
            DirtyPath {
                path: "content/bundles/v2/demo/bundle.yaml".to_string(),
                oid: String::new(),
            },
            DirtyPath {
                path: "content/bundles/v2/demo/fragments/old.md".to_string(),
                oid: String::new(),
            },
        ]
    );
    assert_eq!(left[1][0].path, "content/bundles/v2/demo/bundle.yaml");
    assert!(left[2].is_empty(), "a purge leaves nothing in the library");
    // A trash step resumed with no shape expects its path alone gone.
    assert_eq!(
        expected_step(&command.steps[0], None),
        vec![DirtyPath {
            path: "content/bundles/v2/demo".to_string(),
            oid: String::new(),
        }]
    );
    let book = super::command_book(
        &library_tree::TreeName::home("brisk-copper-moth").unwrap(),
        &intended,
        [1; 16],
    );
    let Some(angzarr_embedded::v1::command_page::Body::Command(sent)) = book.page.unwrap().body
    else {
        panic!()
    };
    assert!(sent.payload.unwrap().type_url.ends_with("RunSequence"));
}

// A resumed sequence (T28) makes its steps from the intent it recorded;
// the content a step writes is the re-plan's step of the same kind at the
// same path, none when the re-plan has none there.
#[test]
fn test_a_resumed_step_takes_its_content_from_the_replans_step_at_its_path() {
    let shaped = shape(forced_import(), demo_files).unwrap().unwrap();
    let Intended::Sequence(command) = intent_of(
        "t",
        "bundle import",
        &shaped.shape,
        (5, &|_| 9),
        evidence(),
        "r-1",
    ) else {
        panic!()
    };
    // Re-planned after the trash: the bundle is gone, so the plan is a new
    // directory alone.
    let replanned = Shape::Create {
        path: "content/bundles/v2/demo".to_string(),
        files: vec![put("bundle.yaml", "v: 2\n")],
    };
    assert_eq!(content_for(&command.steps[1], &replanned), Some(&replanned));
    assert_eq!(content_for(&command.steps[0], &replanned), None);
    // Re-planned before the trash, each step finds its own, a trash's with
    // its files.
    let [trash, create, _] = &shaped.shape.steps()[..] else {
        panic!()
    };
    assert_eq!(content_for(&command.steps[0], &shaped.shape), Some(*trash));
    assert_eq!(content_for(&command.steps[1], &shaped.shape), Some(*create));
    assert_eq!(content_for(&command.steps[2], &replanned), None);
    let elsewhere = Shape::Create {
        path: "content/bundles/v2/other".to_string(),
        files: Vec::new(),
    };
    assert_eq!(content_for(&command.steps[1], &elsewhere), None);
}

// A recorded step's operation: its own path for a trash, a move or a
// purge, which need no content; the plan's step's content for a file or a
// new directory, none (it cannot be made) without one.
#[test]
fn test_a_recorded_step_is_made_with_the_plans_content() {
    use ctxloom_ports::Operation;
    let shaped = shape(forced_import(), demo_files).unwrap().unwrap();
    let Intended::Sequence(command) = intent_of(
        "t",
        "bundle import",
        &shaped.shape,
        (5, &|_| 9),
        evidence(),
        "r-1",
    ) else {
        panic!()
    };
    assert_eq!(
        operation_of(&command.steps[0], None),
        Some(Operation::Trash {
            path: "content/bundles/v2/demo".to_string()
        })
    );
    assert_eq!(
        operation_of(&command.steps[2], None),
        Some(Operation::Purge)
    );
    assert_eq!(operation_of(&command.steps[1], None), None);
    let create = content_for(&command.steps[1], &shaped.shape);
    assert_eq!(
        operation_of(&command.steps[1], create),
        Some(Operation::CreateDir {
            path: "content/bundles/v2/demo".to_string(),
            files: vec![put("bundle.yaml", "v: 2\n")],
        })
    );
}

// Step 10b U9 (G12): `clean` is a sequence of trashes with no purge, so
// each is KEPT 7 days; only a sequence that ends purging its own slot
// keeps none.
#[test]
fn test_a_sequence_with_no_purge_keeps_its_trash() {
    let clean = Shape::Sequence(vec![
        Shape::Trash {
            path: "cache/repos".to_string(),
            files: Vec::new(),
        },
        Shape::Trash {
            path: "cache/context".to_string(),
            files: Vec::new(),
        },
    ]);
    let Intended::Sequence(command) = intent_of("t", "clean", &clean, (0, &|_| 9), evidence(), "r")
    else {
        panic!()
    };
    assert!(command
        .steps
        .iter()
        .all(|step| step.trash.as_ref().is_some_and(|t| t.keep)));
}

// A step on a path outside the library's pathspec (`cache/`, `sessions/`,
// `state/`) leaves nothing the record follows: git never sees it.
#[test]
fn test_a_step_outside_the_library_expects_nothing() {
    let cache = Shape::Trash {
        path: "cache/context".to_string(),
        files: Vec::new(),
    };
    let Intended::Trash(trash) = intent_of("t", "clean", &cache, (0, &|_| 9), evidence(), "r")
    else {
        panic!()
    };
    let step = library_tree::Step {
        trash: Some(trash),
        replace: None,
        create: None,
        r#move: None,
        purge: None,
    };
    assert!(expected_step(&step, Some(&cache)).is_empty());
    for inside in [
        "content/bundles/v2/demo",
        "config.yaml",
        "remotes.yaml",
        "lock.yaml",
    ] {
        assert!(super::in_library(inside), "{inside}");
    }
    for outside in [
        "cache/context",
        "sessions/x",
        "state/trash/r",
        "configs.yaml",
    ] {
        assert!(!super::in_library(outside), "{outside}");
    }
}

// Step 10b U9 (T28): an item taken away with its distilled form is several
// removals: the sequence of their trashes (each kept, as a removal's is),
// never one change per call. (A skill is one directory, its metadata inside
// it, owner T27: its removal is one trash.)
#[test]
fn test_several_removals_are_the_sequence_of_their_trashes() {
    let files = |bundle: &str, dir: &str| match (bundle, dir) {
        ("demo", "skills/review") => vec!["SKILL.md".to_string()],
        _ => Vec::new(),
    };
    let item = Planned::Changed {
        bundle: "demo".to_string(),
        changes: vec![
            Change::Remove {
                path: "fragments/rules.md".to_string(),
            },
            Change::Remove {
                path: "fragments/rules.distilled.md".to_string(),
            },
        ],
    };
    assert_eq!(
        shape(vec![item], files).unwrap().unwrap().shape,
        Shape::Sequence(vec![
            Shape::Trash {
                path: "content/bundles/v2/demo/fragments/rules.md".to_string(),
                files: Vec::new(),
            },
            Shape::Trash {
                path: "content/bundles/v2/demo/fragments/rules.distilled.md".to_string(),
                files: Vec::new(),
            },
        ])
    );
    let skill = Planned::Changed {
        bundle: "demo".to_string(),
        changes: vec![Change::RemoveDir {
            path: "skills/review".to_string(),
        }],
    };
    assert_eq!(
        shape(vec![skill], files).unwrap().unwrap().shape,
        Shape::Trash {
            path: "content/bundles/v2/demo/skills/review".to_string(),
            files: vec!["SKILL.md".to_string()],
        }
    );
    // A removal beside a write is still two changes, refused.
    let mixed = Planned::Changed {
        bundle: "demo".to_string(),
        changes: vec![
            Change::Remove {
                path: "fragments/rules.md".to_string(),
            },
            Change::Write(put("fragments/other.md", "x")),
        ],
    };
    assert!(matches!(
        shape(vec![mixed], files),
        Err(LibraryTreeRejection::OneChangePerCall { .. })
    ));
}

// A sequence's later paths are judged on their own tokens now, its first
// on the caller's etag: an item's distilled form removed beside it is not
// refused stale for an etag that was never its own.
#[test]
fn test_a_sequences_other_paths_are_judged_on_their_own_tokens() {
    let item = Shape::Sequence(vec![
        Shape::Trash {
            path: "content/bundles/v2/demo/fragments/rules.md".to_string(),
            files: Vec::new(),
        },
        Shape::Trash {
            path: "content/bundles/v2/demo/fragments/rules.distilled.md".to_string(),
            files: Vec::new(),
        },
    ]);
    let token = |path: &str| {
        if path.ends_with(".distilled.md") {
            5
        } else {
            1
        }
    };
    let Intended::Sequence(command) =
        intent_of("t", "fragment remove", &item, (3, &token), evidence(), "r")
    else {
        panic!()
    };
    let seen: Vec<u64> = command
        .steps
        .iter()
        .map(|step| step.trash.as_ref().unwrap().seen_sequence)
        .collect();
    assert_eq!(seen, [3, 5]);
}

// Step 10b U9 (QUESTIONS 1(a)): a bundle moved into another project is one
// Move out of this tree, naming the other tree; its intent's `to_tree` says
// so, it expects only its own files gone (the arrival is the other tree's
// to record), and its operation renames into that tree.
#[test]
fn test_a_bundle_moved_into_another_tree_is_one_move_out_of_this_one() {
    let planned = Planned::MovedInto {
        bundle: "demo".to_string(),
        app_dir: "/other/.ctxloom".to_string(),
    };
    let files = |bundle: &str, dir: &str| {
        assert_eq!((bundle, dir), ("demo", ""));
        vec!["bundle.yaml".to_string(), "fragments/a.md".to_string()]
    };
    let shaped = shape(vec![planned], files).unwrap().unwrap();
    assert_eq!(shaped.lands, Lands::Content);
    let Shape::Move { from, to, into, .. } = &shaped.shape else {
        panic!("{shaped:?}")
    };
    assert_eq!(from, "content/bundles/v2/demo");
    assert_eq!(to, "content/bundles/v2/demo");
    assert_eq!(into.as_deref(), Some("/other/.ctxloom"));
    assert_eq!(
        operation(&shaped.shape),
        Some(ctxloom_ports::Operation::Move {
            from: from.clone(),
            to: to.clone(),
            into: into.clone(),
        })
    );
    let intended = intent_of(
        "t",
        "bundle move",
        &shaped.shape,
        (3, &|_| 9),
        evidence(),
        "r",
    );
    let Intended::Move(command) = &intended else {
        panic!("{intended:?}")
    };
    assert_eq!(command.to_tree, "/other/.ctxloom");
    assert_eq!(command.seen_sequence, 3);
    let paths: Vec<String> = expected(&intended, &shaped.shape)
        .into_iter()
        .map(|p| p.path)
        .collect();
    assert_eq!(
        paths,
        [
            "content/bundles/v2/demo/bundle.yaml",
            "content/bundles/v2/demo/fragments/a.md"
        ]
    );
    let step = intended.step("");
    assert_eq!(
        operation_of(&step, None),
        Some(ctxloom_ports::Operation::Move {
            from: from.clone(),
            to: to.clone(),
            into: into.clone(),
        })
    );
}

// T29 (S52, owner): several removals are one change only when they are one
// item's (its file and its distilled form); a fragment and a prompt taken
// away together are two, refused, each named (bundle.feature's
// `--remove-fragment example --remove-prompt example`).
#[test]
fn test_two_items_removed_together_are_two_changes_refused() {
    let two = Planned::Changed {
        bundle: "demo".to_string(),
        changes: vec![
            Change::Remove {
                path: "fragments/example.md".to_string(),
            },
            Change::Remove {
                path: "fragments/example.distilled.md".to_string(),
            },
            Change::Remove {
                path: "prompts/example.md".to_string(),
            },
        ],
    };
    let Err(LibraryTreeRejection::OneChangePerCall { paths }) =
        shape(vec![two], |_: &str, _: &str| Vec::new())
    else {
        panic!("two items' removals were let through");
    };
    assert!(
        paths.contains(&"fragments/example.md".to_string()),
        "{paths:?}"
    );
    assert!(
        paths.contains(&"prompts/example.md".to_string()),
        "{paths:?}"
    );
}
