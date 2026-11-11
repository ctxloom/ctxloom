//! Owner rulings 110, 111, narrowed by 117: ctxloom's built-in bundle (its
//! operational prompts) in a project's catalog, read-only, overridden by a
//! project bundle of the same name, and in no session's context.

use angzarr_diagnostic::Diagnostic;
use exit_code::{Exit, ExitClass};

use crate::fakes::{plain, Mem, Setup, CONFIG, ENVELOPE};
use crate::{AppError, AssembleRequest, ItemKind, Library, WriteError};

use std::sync::Arc;

angzarr_presentation::english_for!(&[crate::EN_US.1]);

/// The project bundle `ctxloom init` seeds: its `default` profile, as
/// scaffolded, bare (ruling 117).
const PROJECT: &[(&str, &str)] = &[
    ENVELOPE,
    (
        "profiles/default.yaml",
        library::write::scaffold::SEED_PROFILE,
    ),
];

const BUILT_IN: [&str; 1] = ["ctxloom"];

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
        origin: "ctxloom/builtin".to_string(),
        dir: "/p/.ctxloom/content/bundles/v2".to_string(),
    };
    (refused.code().to_string(), refused.english())
}

// A project lists the built-in bundle under its origin, whether or not it
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
    assert!(!list.no_root, "the built-in bundle is there");
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
                "ctxloom/builtin".to_string(),
                format!("ctxloom+local:{name}"),
                format!("<built-in>/{name}/bundle.yaml"),
            ]
        })
        .collect();
    assert_eq!(rows, want);
    let shown = ok(library.bundle_show(&plain(), "ctxloom"));
    assert_eq!(shown.fragments.len(), 0);
    let viewed = ok(library.bundle_view(&plain(), "ctxloom", false));
    assert!(
        viewed.content.contains("prompts/setup-interview.md"),
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
        ("fragments/ours.md", "Our own rules.\n"),
    ];
    let (library, _) = world(&[("ctxloom", ours)]);
    let list = ok(library.bundle_list(&plain()));
    let named: Vec<_> = list.rows.iter().filter(|r| r.name == "ctxloom").collect();
    assert_eq!(named.len(), 1);
    assert_eq!(named[0].version, "9.9.9");
    assert_eq!(named[0].origin, "");
    let fragments = ok(library.item_list(&plain(), ItemKind::Fragment, "ctxloom"));
    let names: Vec<&str> = fragments.rows.iter().map(|r| r.name.as_str()).collect();
    assert_eq!(names, ["ours"]);
}

// Ruling 117: the seeded profile is bare, and the built-in bundle is in no
// session's context: the default profile assembles nothing, the operational
// prompts least of all, with nothing pulled and nothing missing.
#[test]
fn test_the_seeded_profile_assembles_nothing_and_no_operational_prompt() {
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
        assembled.fragments_loaded.is_empty(),
        "{:?}",
        assembled.fragments_loaded
    );
    assert!(!assembled.context.contains("setup interview"));
    assert!(!assembled.context.contains("text compression assistant"));
    assert!(!notices.contains("not installed"), "{notices}");
    assert!(!notices.contains("does not exist"), "{notices}");
    assert_eq!(mem.writes(), 0);
    let request = crate::RunRequest {
        profile: "default".to_string(),
        ..crate::RunRequest::default()
    };
    let plan = ok(library.dry_run(&plain(), &request)).plan;
    assert!(plan.fragments.is_empty(), "{:?}", plan.fragments);
    let list = ok(library.profile_list(&plain()));
    let names: Vec<&str> = list.rows.iter().map(|r| r.name.as_str()).collect();
    assert_eq!(names, ["default"], "no built-in profile ships");
}

// A built-in prompt is listed by its bundle's name, and read as it ships.
#[test]
fn test_the_built_in_prompts_are_listed_and_read_as_they_ship() {
    let (library, _) = world(&[("project", PROJECT)]);
    let prompts = ok(library.item_list(&plain(), ItemKind::Command, "ctxloom"));
    let names: Vec<&str> = prompts.rows.iter().map(|r| r.name.as_str()).collect();
    assert_eq!(names, ["distill", "setup-interview"]);
}

// No write reaches a built-in bundle: each is refused, naming where a project
// bundle that overrides it would go, and nothing is written.
#[test]
fn test_every_write_to_a_built_in_bundle_is_refused_and_writes_nothing() {
    let (library, mem) = world(&[("project", PROJECT)]);
    let flags = plain();
    assert_eq!(
        refusal(library.item_create(&flags, ItemKind::Fragment, "ctxloom", "mine")),
        read_only("ctxloom")
    );
    assert_eq!(
        refusal(library.item_remove(&flags, ItemKind::Command, "ctxloom#prompts/distill", true)),
        read_only("ctxloom")
    );
    assert_eq!(
        refusal(library.item_edit(
            &flags,
            ItemKind::Command,
            "ctxloom#prompts/setup-interview",
            false
        )),
        read_only("ctxloom")
    );
    for yes in [false, true] {
        assert_eq!(
            refusal(library.bundle_remove(&flags, "ctxloom", yes)),
            read_only("ctxloom")
        );
    }
    assert_eq!(
        refusal(library.bundle_edit(
            &flags,
            &crate::BundleEditRequest {
                name: "ctxloom".to_string(),
                description: Some("mine".to_string()),
                ..Default::default()
            }
        )),
        read_only("ctxloom")
    );
    assert_eq!(mem.writes(), 0);
    let (code, said) = read_only("ctxloom");
    assert_eq!(code, "library-application-write-builtin-read-only");
    assert_eq!(
        said,
        "bundle \"ctxloom\" is built into ctxloom (ctxloom/builtin) and is read-only; a project bundle of its name, in /p/.ctxloom/content/bundles/v2, overrides it"
    );
}

// Reading one out is no write: an export copies the built-in files, and is
// the way to a project bundle that overrides it.
#[test]
fn test_a_built_in_bundle_exports_its_shipped_files() {
    let (library, mem) = world(&[]);
    let done = ok(library.bundle_export(&plain(), "ctxloom", "out", ""));
    assert_eq!(done.dest, "out/ctxloom");
    assert_eq!(done.source, "<built-in>/ctxloom/bundle.yaml");
    let paths = mem.user_paths();
    assert_eq!(paths.len(), 3, "{paths:?}");
    assert_eq!(
        mem.user_text("out/ctxloom/prompts/distill.md").as_deref(),
        Some(library::builtin::DISTILL_PROMPT)
    );
}

// An import of a bundle named as a built-in one makes the project's own,
// which overrides it: there is nothing of the project's to refuse over.
#[test]
fn test_an_import_of_a_built_in_name_makes_the_overriding_project_bundle() {
    let (library, mem) = world(&[]);
    ok(library.bundle_export(&plain(), "ctxloom", "out", ""));
    let imported = ok(library.bundle_import(&plain(), "out/ctxloom", false));
    assert_eq!(imported.dest, "/p/.ctxloom/content/bundles/v2/ctxloom");
    assert_eq!(
        mem.log(),
        ["write_tree out/ctxloom", "claim ctxloom", "apply ctxloom"]
    );
}

// A built-in bundle has no executable file, whatever the project's files say.
#[test]
fn test_a_built_in_bundle_has_no_executable_file() {
    let library = Setup {
        layers: &[CONFIG],
        executable: &["prompts/distill.md"],
        builtins: true,
        ..Setup::default()
    }
    .library();
    let found = library.run(&plain(), |p| {
        let entry = p.catalog.lookup("ctxloom").expect("built in").clone();
        p.executables_of(&entry)
    });
    assert!(ok(found).is_empty());
}

// An import of a name no bundle has is made, whatever other bundles (the
// built-in ones among them) the project holds.
#[test]
fn test_an_import_of_a_new_name_beside_other_bundles_is_not_refused() {
    let (library, mem) = world(&[("demo", &[ENVELOPE])]);
    mem.put_user("in/mine/bundle.yaml", ENVELOPE.1.as_bytes(), false);
    let imported = ok(library.bundle_import(&plain(), "in/mine", false));
    assert_eq!(imported.dest, "/p/.ctxloom/content/bundles/v2/mine");
    assert_eq!(mem.log(), ["claim mine", "apply mine"]);
}

/// The `ctxloom` bundle a project holds of its own, with `prompts`.
fn own_ctxloom(prompts: &[(&'static str, &'static str)]) -> Vec<(&'static str, &'static str)> {
    let mut files = vec![("bundle.yaml", "schema_version: 4\nversion: 9.9.9\n")];
    files.extend_from_slice(prompts);
    files
}

// Owner ruling 128: an activity reads its prompt THROUGH the catalog. With no
// project bundle of the name, it is the built-in one's, as it ships.
#[test]
fn test_an_operational_prompt_is_the_built_in_one_as_it_ships() {
    let (library, mem) = world(&[("project", PROJECT)]);
    for (name, text) in [
        (
            library::builtin::SETUP_INTERVIEW_NAME,
            library::builtin::SETUP_INTERVIEW,
        ),
        (
            library::builtin::DISTILL_NAME,
            library::builtin::DISTILL_PROMPT,
        ),
    ] {
        assert_eq!(ok(library.operational_prompt(&plain(), name)), text);
    }
    assert_eq!(mem.writes(), 0);
}

// With no project (the home's configuration alone), there is nothing to
// override it: `init prompt` anywhere is the built-in one.
#[test]
fn test_an_operational_prompt_outside_a_project_is_the_built_in_one() {
    let library = Setup {
        layers: &[CONFIG],
        home_only: true,
        builtins: true,
        ..Setup::default()
    }
    .library();
    assert_eq!(
        ok(library.operational_prompt(&plain(), library::builtin::SETUP_INTERVIEW_NAME)),
        library::builtin::SETUP_INTERVIEW
    );
}

// A project bundle named `ctxloom` that holds the prompt replaces the
// embedded text (the trust cost the owner accepted).
#[test]
fn test_a_project_bundle_of_the_name_replaces_the_prompt() {
    let ours = own_ctxloom(&[("prompts/setup-interview.md", "OUR OWN INTERVIEW\n")]);
    let (library, _) = world(&[("ctxloom", &ours)]);
    assert_eq!(
        ok(library.operational_prompt(&plain(), "setup-interview")),
        "OUR OWN INTERVIEW\n"
    );
}

// It overrides the built-in one WHOLE: a prompt it lacks is not taken from
// the built-in bundle. It is refused, naming the prompt and where it goes.
#[test]
fn test_a_project_bundle_of_the_name_lacking_the_prompt_is_refused() {
    let ours = own_ctxloom(&[("prompts/setup-interview.md", "OUR OWN INTERVIEW\n")]);
    let (library, _) = world(&[("ctxloom", &ours)]);
    let refused = library.operational_prompt(&plain(), "distill").result;
    let Err(e) = refused else {
        panic!("not refused: {refused:?}")
    };
    assert_eq!(e.code(), "library-application-operational-prompt-missing");
    assert_eq!(e.exit(), Exit::REFUSED);
    assert_eq!(
        e.english(),
        "the project's bundle \"ctxloom\" (/p/.ctxloom/content/bundles/v2/ctxloom) overrides ctxloom's built-in one whole, and has no prompt \"distill\""
    );
}

// One that is there and cannot be read overrides it too: refused for what it
// is, never answered with the built-in text.
#[test]
fn test_an_unreadable_project_bundle_of_the_name_is_refused() {
    let broken: &[(&str, &str)] = &[("bundle.yaml", "schema_version: 4\nversion: [\n")];
    let (library, _) = world(&[("ctxloom", broken)]);
    let refused = library
        .operational_prompt(&plain(), "setup-interview")
        .result;
    let Err(e) = refused else {
        panic!("not refused: {refused:?}")
    };
    assert_eq!(e.code(), "library-application-bundle-unreadable");
}

// Distill is not built yet; its prompt resolves through the catalog, as the
// command listing reads it.
#[test]
fn test_the_distill_prompt_resolves() {
    let (library, _) = world(&[("project", PROJECT)]);
    let shown = ok(library.item_show(
        &plain(),
        ItemKind::Command,
        "ctxloom#prompts/distill",
        false,
    ));
    assert_eq!(shown.content, library::builtin::DISTILL_PROMPT);
}
