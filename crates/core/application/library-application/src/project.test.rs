//! Owner rulings 110, 111: ctxloom's built-in bundles in a project's
//! catalog, read-only, overridden by a project bundle of the same name.

use angzarr_diagnostic::Diagnostic;

use crate::fakes::{plain, Mem, Setup, CONFIG, ENVELOPE};
use crate::{AppError, AssembleRequest, ItemKind, Library, ProfileEdit, WriteError};

use std::sync::Arc;

angzarr_presentation::english_for!(&[crate::EN_US.1]);

/// The project bundle `ctxloom init` seeds: its `default` profile, as
/// scaffolded, inheriting the built-in developer profile.
const PROJECT: &[(&str, &str)] = &[
    ENVELOPE,
    (
        "profiles/default.yaml",
        library::write::scaffold::SEED_PROFILE,
    ),
];

const BUILT_IN: [&str; 4] = ["ai-coding-general", "ai-developer", "git", "testing"];

fn world(bundles: &[(&str, &[(&str, &str)])]) -> (Library, Arc<Mem>) {
    Setup {
        bundles,
        layers: &[CONFIG],
        builtins: true,
        ..Setup::default()
    }
    .fixture()
}

fn ok<T>(outcome: crate::Outcome<T>) -> T {
    match outcome.result {
        Ok(value) => value,
        Err(e) => panic!("{e:?}"),
    }
}

/// What a refusal says: its code and its English.
fn refusal<T>(outcome: crate::Outcome<T>) -> (String, String) {
    match outcome.result {
        Err(AppError::Cause(e)) => (e.code().to_string(), e.english()),
        Err(other) => panic!("not a write refusal: {other:?}"),
        Ok(_) => panic!("not refused"),
    }
}

/// The refusal of a write to the built-in `bundle`.
fn read_only(bundle: &str) -> (String, String) {
    let refused = WriteError::BuiltinReadOnly {
        bundle: bundle.to_string(),
        origin: "ctxloom/default".to_string(),
        dir: "/p/.ctxloom/content/bundles/v2".to_string(),
    };
    (refused.code().to_string(), refused.english())
}

// A project lists the built-in bundles under their origin, whether or not it
// has a bundles directory of its own; with no project there are none.
#[test]
fn test_a_project_lists_the_built_in_bundles_under_their_origin() {
    let (library, _) = Setup {
        layers: &[CONFIG],
        no_root: true,
        builtins: true,
        ..Setup::default()
    }
    .fixture();
    let list = ok(library.bundle_list(&plain()));
    assert!(!list.no_root, "the built-in bundles are there");
    let rows: Vec<[String; 4]> = list
        .rows
        .iter()
        .map(|r| {
            [
                r.name.clone(),
                r.origin.clone(),
                r.reference.clone(),
                r.path.clone(),
            ]
        })
        .collect();
    let want: Vec<[String; 4]> = BUILT_IN
        .iter()
        .map(|name| {
            [
                name.to_string(),
                "ctxloom/default".to_string(),
                format!("ctxloom+local:{name}"),
                format!("<built-in ctxloom/default>/{name}/bundle.yaml"),
            ]
        })
        .collect();
    assert_eq!(rows, want);
    let shown = ok(library.bundle_show(&plain(), "testing"));
    assert_eq!(shown.fragments.len(), 3);
    let viewed = ok(library.bundle_view(&plain(), "git", false));
    assert!(
        viewed.content.contains("fragments/git.md"),
        "{}",
        viewed.content
    );
    let home = Setup {
        layers: &[CONFIG],
        home_only: true,
        builtins: true,
        ..Setup::default()
    }
    .library();
    assert!(ok(home.bundle_list(&plain())).rows.is_empty());
}

// Without the composition root's say-so the catalog is the project's alone.
#[test]
fn test_the_built_in_bundles_join_only_a_library_composed_with_them() {
    let library = Setup {
        layers: &[CONFIG],
        ..Setup::default()
    }
    .library();
    assert!(ok(library.bundle_list(&plain())).rows.is_empty());
}

// A project bundle of a built-in one's name overrides it: one row of the
// name, the project's own.
#[test]
fn test_a_project_bundle_of_the_same_name_overrides_the_built_in_one() {
    let ours: &[(&str, &str)] = &[
        ("bundle.yaml", "schema_version: 4\nversion: 9.9.9\n"),
        ("fragments/ours.md", "Our testing rules.\n"),
    ];
    let (library, _) = world(&[("testing", ours)]);
    let list = ok(library.bundle_list(&plain()));
    let testing: Vec<_> = list.rows.iter().filter(|r| r.name == "testing").collect();
    assert_eq!(testing.len(), 1);
    assert_eq!(testing[0].version, "9.9.9");
    assert_eq!(testing[0].origin, "");
    let fragments = ok(library.item_list(&plain(), ItemKind::Fragment, "testing"));
    let names: Vec<&str> = fragments.rows.iter().map(|r| r.name.as_str()).collect();
    assert_eq!(names, ["ours"]);
}

// The seeded profile assembles from the built-in bundles, with nothing pulled
// and nothing missing.
#[test]
fn test_the_seeded_profile_assembles_from_the_built_in_bundles() {
    let (library, mem) = world(&[("project", PROJECT)]);
    let out = library.assemble(
        &plain(),
        &AssembleRequest {
            profiles: vec!["default".to_string()],
            ..AssembleRequest::default()
        },
    );
    let notices = format!("{:?}", out.notices);
    let assembled = ok(out);
    assert!(
        assembled
            .fragments_loaded
            .contains(&"ctxloom+local:testing#fragments/tdd".to_string()),
        "{:?}",
        assembled.fragments_loaded
    );
    assert_eq!(assembled.fragments_loaded.len(), 13);
    assert!(assembled.context.contains("Red-green-refactor"));
    assert!(!notices.contains("not installed"), "{notices}");
    assert!(!notices.contains("does not exist"), "{notices}");
    assert_eq!(mem.writes(), 0);
}

// No write reaches a built-in bundle: each is refused, naming where a project
// bundle that overrides it would go, and nothing is written.
#[test]
fn test_every_write_to_a_built_in_bundle_is_refused_and_writes_nothing() {
    let (library, mem) = world(&[("project", PROJECT)]);
    let flags = plain();
    assert_eq!(
        refusal(library.item_create(&flags, ItemKind::Fragment, "testing", "mine")),
        read_only("testing")
    );
    assert_eq!(
        refusal(library.item_remove(&flags, ItemKind::Fragment, "testing#fragments/tdd", true)),
        read_only("testing")
    );
    assert_eq!(
        refusal(library.item_edit(&flags, ItemKind::Fragment, "git#fragments/git", false)),
        read_only("git")
    );
    for yes in [false, true] {
        assert_eq!(
            refusal(library.bundle_remove(&flags, "testing", yes)),
            read_only("testing")
        );
        assert_eq!(
            refusal(library.profile_remove(&flags, "ai-developer#profiles/developer", yes)),
            read_only("ai-developer")
        );
    }
    assert_eq!(
        refusal(library.bundle_edit(
            &flags,
            &crate::BundleEditRequest {
                name: "git".to_string(),
                description: Some("mine".to_string()),
                ..Default::default()
            }
        )),
        read_only("git")
    );
    assert_eq!(
        refusal(library.profile_modify(
            &flags,
            "ai-developer#profiles/developer",
            &ProfileEdit {
                add_bundles: vec!["project".to_string()],
                ..ProfileEdit::default()
            }
        )),
        read_only("ai-developer")
    );
    assert_eq!(mem.writes(), 0);
    let (code, said) = read_only("testing");
    assert_eq!(code, "library-application-write-builtin-read-only");
    assert_eq!(
        said,
        "bundle \"testing\" is built into ctxloom (ctxloom/default) and is read-only"
    );
}

// Reading one out is no write: an export copies the built-in files, and is
// the way to a project bundle that overrides it.
#[test]
fn test_a_built_in_bundle_exports_its_shipped_files() {
    let (library, mem) = world(&[]);
    let done = ok(library.bundle_export(&plain(), "testing", "out", ""));
    assert_eq!(done.dest, "out/testing");
    assert_eq!(
        done.source,
        "<built-in ctxloom/default>/testing/bundle.yaml"
    );
    let paths = mem.user_paths();
    assert_eq!(paths.len(), 7, "{paths:?}");
    assert_eq!(
        mem.user_text("out/testing/fragments/tdd.md").as_deref(),
        std::str::from_utf8(
            &library::builtin::files("testing").expect("built in")["fragments/tdd.md"]
        )
        .ok()
    );
}

// An import of a bundle named as a built-in one makes the project's own,
// which overrides it: there is nothing of the project's to refuse over.
#[test]
fn test_an_import_of_a_built_in_name_makes_the_overriding_project_bundle() {
    let (library, mem) = world(&[]);
    ok(library.bundle_export(&plain(), "git", "out", ""));
    let imported = ok(library.bundle_import(&plain(), "out/git", false));
    assert_eq!(imported.dest, "/p/.ctxloom/content/bundles/v2/git");
    assert_eq!(mem.log(), ["claim git", "apply git"]);
}
