//! A use case over the recording library plans its write and writes
//! nothing; what it planned is the write the use case would have made.

use ctxloom_ports::{Change, Scope};

use super::Planned;
use crate::fakes::{plain, Setup, CONFIG, ENVELOPE};

fn world() -> (crate::Library, std::sync::Arc<crate::fakes::Mem>) {
    Setup {
        bundles: &[("demo", &[ENVELOPE, ("fragments/example.md", "EXAMPLE")])],
        layers: &[CONFIG],
        ..Setup::default()
    }
    .fixture()
}

#[test]
fn test_a_bundle_create_is_planned_as_one_new_directory_and_writes_nothing() {
    let (library, mem) = world();
    let (recording, recorder) = library.recording();
    let created = recording.bundle_create(&plain(), "notes", "my notes");
    assert!(created.result.is_ok(), "{:?}", created.result.err());
    assert_eq!(mem.writes(), 0, "{:?}", mem.log());
    assert!(!mem.has_bundle("notes"));
    let planned = recorder.take();
    let [Planned::NewBundle { bundle, files }] = &planned[..] else {
        panic!("{planned:?}")
    };
    assert_eq!(bundle, "notes");
    let paths: Vec<&str> = files.iter().map(|f| f.path.as_str()).collect();
    assert!(paths.contains(&"bundle.yaml"), "{paths:?}");
}

#[test]
fn test_an_edit_of_one_file_is_planned_as_that_change() {
    let (library, mem) = world();
    let (recording, recorder) = library.recording();
    recording
        .item_create(&plain(), crate::ItemKind::Fragment, "demo", "rules")
        .result
        .unwrap();
    assert_eq!(mem.text("demo", "fragments/rules.md"), None);
    let planned = recorder.take();
    let [Planned::Changed { bundle, changes }] = &planned[..] else {
        panic!("{planned:?}")
    };
    assert_eq!(bundle, "demo");
    let [Change::Write(put)] = &changes[..] else {
        panic!("{changes:?}")
    };
    assert_eq!(put.path, "fragments/rules.md");
}

#[test]
fn test_a_claim_given_back_is_no_write() {
    let recorder = super::Recorder::default();
    recorder.claim("notes");
    recorder.apply("notes", &[]);
    recorder.remove_bundle("notes");
    assert!(recorder.take().is_empty());
    recorder.remove_bundle("demo");
    assert_eq!(
        recorder.take(),
        [Planned::BundleGone {
            bundle: "demo".to_string()
        }]
    );
}

#[test]
fn test_a_layer_written_twice_is_its_last_text_and_reads_back_planned() {
    let recorder = super::Recorder::default();
    recorder.layer(Scope::Home, "a: 1\n");
    recorder.layer(Scope::Home, "a: 2\n");
    assert_eq!(recorder.layer_text(Scope::Home).as_deref(), Some("a: 2\n"));
    assert_eq!(recorder.layer_text(Scope::Project), None);
    assert_eq!(recorder.take().len(), 1);
}

#[test]
fn test_a_bundles_files_are_listed_below_a_directory_of_it() {
    let (library, _) = world();
    assert_eq!(
        library.bundle_files("demo", ""),
        ["bundle.yaml", "fragments/example.md"]
    );
    assert_eq!(library.bundle_files("demo", "fragments/"), ["example.md"]);
    assert!(library.bundle_files("demo", "skills").is_empty());
    assert!(library.bundle_files("nobody", "").is_empty());
}

// Step 10b U9 (T28): a forced import over a bundle is planned as the old
// bundle gone, then a new one holding EVERY file imported (an unchanged
// file too), so the daemon makes it the sequence trash, create, purge.
#[test]
fn test_a_forced_import_over_a_bundle_is_planned_as_it_gone_then_a_new_one() {
    let (library, mem) = world();
    mem.put_user("in/demo/bundle.yaml", ENVELOPE.1.as_bytes(), false);
    mem.put_user("in/demo/fragments/example.md", b"EXAMPLE", false);
    mem.put_user("in/demo/fragments/new.md", b"NEW", false);
    let (recording, recorder) = library.recording();
    let imported = recording.bundle_import(&plain(), "in/demo", true);
    assert!(imported.result.is_ok(), "{:?}", imported.result.err());
    assert_eq!(mem.writes(), 0, "{:?}", mem.log());
    let planned = recorder.take();
    let [Planned::BundleGone { bundle: gone }, Planned::NewBundle { bundle, files }] = &planned[..]
    else {
        panic!("{planned:?}")
    };
    assert_eq!((gone.as_str(), bundle.as_str()), ("demo", "demo"));
    let mut paths: Vec<&str> = files.iter().map(|f| f.path.as_str()).collect();
    paths.sort_unstable();
    assert_eq!(
        paths,
        ["bundle.yaml", "fragments/example.md", "fragments/new.md"]
    );
}

// An import of a bundle the project has none of is one new directory.
#[test]
fn test_an_import_of_a_new_bundle_is_planned_as_one_new_directory() {
    let (library, mem) = world();
    mem.put_user("in/notes/bundle.yaml", ENVELOPE.1.as_bytes(), false);
    let (recording, recorder) = library.recording();
    let imported = recording.bundle_import(&plain(), "in/notes", true);
    assert!(imported.result.is_ok(), "{:?}", imported.result.err());
    let planned = recorder.take();
    let [Planned::NewBundle { bundle, .. }] = &planned[..] else {
        panic!("{planned:?}")
    };
    assert_eq!(bundle, "notes");
}

// Step 10b U3 (D2: an export outside the trees is an EXCEPTION, staged and
// NOREPLACE): over the recording library an export is planned as the tree
// it would write, and writes nothing, so a preview, planned twice, never
// exports.
#[test]
fn test_an_export_is_planned_as_a_tree_outside_and_writes_nothing() {
    let (library, mem) = world();
    let (recording, recorder) = library.recording();
    let exported = recording.bundle_export(&plain(), "demo", "out", "");
    assert!(exported.result.is_ok(), "{:?}", exported.result.err());
    assert_eq!(mem.writes(), 0, "{:?}", mem.log());
    assert!(mem.user_paths().is_empty(), "{:?}", mem.user_paths());
    let planned = recorder.take();
    let [Planned::Outside { dir, files }] = &planned[..] else {
        panic!("{planned:?}")
    };
    assert_eq!(dir, "out/demo");
    let paths: Vec<&str> = files.iter().map(|f| f.path.as_str()).collect();
    assert!(paths.contains(&"fragments/example.md"), "{paths:?}");
}

// An export onto a path that is there is refused, a decline, before
// anything is planned: the tree is put in whole or not at all.
#[test]
fn test_an_export_onto_a_path_that_is_there_is_refused_and_plans_nothing() {
    let (library, mem) = world();
    mem.put_user("out/demo/keep.md", b"hers", false);
    let (recording, recorder) = library.recording();
    let refused = recording.bundle_export(&plain(), "demo", "out", "");
    let error = refused.result.expect_err("refused");
    assert_eq!(
        coded_diagnostic::Diagnostic::code(&error),
        "library-application-write-export-destination-exists"
    );
    assert!(recorder.take().is_empty());
}

// Step 10b U9 (QUESTIONS 1(a)): a local `bundle move` into another project
// is planned as ONE move of the bundle into that project's tree, and moves
// nothing; it answers where the bundle was and where it lands.
#[test]
fn test_a_bundle_move_into_another_project_is_planned_as_one_move() {
    let (library, mem) = world();
    mem.put_user("/work/other/.ctxloom/.keep", b"", false);
    let (recording, recorder) = library.recording();
    let moved = recording
        .bundle_move_into(&plain(), "demo", "/work/other")
        .result
        .unwrap();
    assert!(mem.has_bundle("demo"), "nothing moved");
    assert_eq!(moved.name, "demo");
    assert_eq!(
        moved.source,
        "/p/.ctxloom/content/bundles/v2/demo/bundle.yaml"
    );
    assert_eq!(moved.dest, "/work/other/.ctxloom/content/bundles/v2/demo");
    let planned = recorder.take();
    assert_eq!(
        planned,
        [Planned::MovedInto {
            bundle: "demo".to_string(),
            app_dir: "/work/other/.ctxloom".to_string(),
        }]
    );
}

// The bundle is there already at its new place: refused, a decline, and
// nothing planned (a move never replaces, `--force` or not); its own
// place is no destination either.
#[test]
fn test_a_bundle_move_onto_a_bundle_there_or_onto_itself_is_refused() {
    let (library, mem) = world();
    mem.put_user(
        "/work/other/.ctxloom/content/bundles/v2/demo/bundle.yaml",
        b"theirs",
        false,
    );
    let (recording, recorder) = library.recording();
    let code = |outcome: crate::Outcome<crate::BundleMoved>| {
        coded_diagnostic::Diagnostic::code(&outcome.result.expect_err("refused")).to_string()
    };
    let refused = recording
        .bundle_move_into(&plain(), "demo", "/work/other")
        .result
        .expect_err("refused");
    assert_eq!(
        coded_diagnostic::Diagnostic::code(&refused),
        "library-application-write-move-destination-exists"
    );
    // Its fix names the removal that clears the way, there, then the move
    // again (owner 2026-11-18, night).
    let args: Vec<(String, String)> = coded_diagnostic::Diagnostic::args(&refused)
        .into_iter()
        .filter_map(|(name, value)| match value {
            coded_diagnostic::Arg::Str(text) => Some((name.to_string(), text)),
            _ => None,
        })
        .collect();
    assert!(
        args.contains(&("bundle".to_string(), "demo".to_string())),
        "{args:?}"
    );
    assert!(
        args.contains(&("dir".to_string(), "/work/other".to_string())),
        "{args:?}"
    );
    let fix = crate::EN_US
        .1
        .lines()
        .find(|line| line.starts_with("library-application-write-move-destination-exists-fix ="))
        .expect("a fix");
    assert!(fix.contains("ctxloom bundle remove { $bundle }"), "{fix}");
    assert!(fix.contains("then move it again"), "{fix}");
    assert_eq!(
        code(recording.bundle_move_into(&plain(), "demo", "/p")),
        "library-application-write-move-destination-itself"
    );
    assert!(recorder.take().is_empty());
}

// Step 10c U5: `manage gitignore install` writes `.ctxloom/.gitignore`
// through the scaffold: planned as that one file of the tree, writing
// nothing; one already as ctxloom writes it plans nothing.
#[test]
fn test_the_nested_gitignore_is_planned_as_one_file_of_the_tree_when_it_differs() {
    let want = library::write::scaffold::nested_gitignore();
    let (library, mem) = world();
    let (recording, recorder) = library.recording();
    assert!(recording.gitignore_install().result.unwrap().written);
    assert_eq!(mem.scaffold_text(".gitignore"), None);
    let planned = recorder.take();
    let [Planned::TreeFiles { files }] = &planned[..] else {
        panic!("{planned:?}")
    };
    let [put] = &files[..] else {
        panic!("{files:?}")
    };
    assert_eq!(
        (put.path.as_str(), put.bytes.as_slice()),
        (".gitignore", want.as_bytes())
    );
    let (library, mem) = world();
    mem.set_scaffold_for_test(".gitignore", &want);
    let (recording, recorder) = library.recording();
    assert!(!recording.gitignore_install().result.unwrap().written);
    assert!(recorder.take().is_empty());
}

// Not recording, the same use case writes the file through the scaffold.
#[test]
fn test_the_nested_gitignore_is_written_through_the_scaffold() {
    let (library, mem) = world();
    let installed = library.gitignore_install().result.unwrap();
    assert_eq!(installed.app_dir, "/work/.ctxloom");
    assert!(installed.written);
    assert_eq!(
        mem.scaffold_text(".gitignore"),
        Some(library::write::scaffold::nested_gitignore())
    );
}

// Step 10c U6: a first init's scaffold, its directory not there yet, is
// planned as ONE new tree holding every file (the config with init's
// answers in it), writing nothing.
#[test]
fn test_a_first_init_is_planned_as_one_new_tree_and_writes_nothing() {
    use library::write::scaffold::{self, Answered};
    let (library, mem) = world();
    mem.set_fresh_for_test();
    let answered = Answered {
        output_dir: Some("out/".to_string()),
        premised: None,
    };
    let (recording, recorder) = library.recording();
    let init = recording.project_init("", &answered).result.unwrap();
    assert_eq!(init.app_dir, "/work/.ctxloom");
    assert!(!init.existed);
    assert_eq!(mem.scaffold_text("config.yaml"), None);
    let planned = recorder.take();
    let [Planned::NewTree { files }] = &planned[..] else {
        panic!("{planned:?}")
    };
    let paths: Vec<&str> = files.iter().map(|put| put.path.as_str()).collect();
    assert_eq!(
        paths,
        [
            "config.yaml",
            "remotes.yaml",
            "content/bundles/v2/project/bundle.yaml",
            ".gitignore",
            "content/bundles/v2/project/profiles/default.yaml",
        ]
    );
    assert_eq!(
        files[0].bytes,
        scaffold::initial_config_with("claude-code", &answered).into_bytes()
    );
}

// A tree that is there is init's re-run: nothing is planned or written.
#[test]
fn test_init_of_a_project_there_plans_nothing() {
    let (library, mem) = world();
    let (recording, recorder) = library.recording();
    let init = recording
        .project_init("", &library::write::scaffold::Answered::default())
        .result
        .unwrap();
    assert!(init.existed);
    assert!(recorder.take().is_empty());
    assert_eq!(mem.writes(), 0);
}

// `manage install --engine` (step 10c U6): the new tree's default agent
// runs that engine; one this build does not know is refused, nothing
// planned.
#[test]
fn a_new_tree_s_engine_is_the_one_asked_and_an_unknown_one_is_refused() {
    use library::write::scaffold::{self, Answered};
    let (library, mem) = world();
    mem.set_fresh_for_test();
    let (recording, recorder) = library.clone().recording();
    recording
        .project_init("opencode", &Answered::default())
        .result
        .unwrap();
    let planned = recorder.take();
    let [Planned::NewTree { files }] = &planned[..] else {
        panic!("{planned:?}")
    };
    assert_eq!(
        files[0].bytes,
        scaffold::initial_config_with("opencode", &Answered::default()).into_bytes()
    );
    let (recording, recorder) = library.recording();
    let refused = recording.project_init("totally-bogus", &Answered::default());
    assert!(refused.result.is_err());
    assert!(recorder.take().is_empty());
}
