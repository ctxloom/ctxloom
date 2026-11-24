//! The library's nouns as the command line mounts them.

use std::sync::Arc;

use ctxloom_cli::Extension;
use process_env::EnvSnapshot;

use super::LibraryCommands;

#[test]
fn the_nouns_are_the_library_clis() {
    let commands = LibraryCommands::new(Arc::new(crate::tests::host_snapshot()));
    let ours: Vec<String> = commands
        .nouns()
        .iter()
        .map(|n| n.get_name().to_string())
        .collect();
    let theirs: Vec<String> = library_cli::nouns()
        .iter()
        .map(|n| n.get_name().to_string())
        .collect();
    assert!(!theirs.is_empty());
    assert_eq!(ours, theirs);
}

// Owner rulings 110, 111 (narrowed by 117): the composed library lists
// ctxloom's built-in bundle in a project; the test-only variable withholds it.
#[test]
fn the_composed_library_has_the_built_in_bundles_unless_the_tests_withhold_them() {
    let mut world = crate::commands::tests::world_without_programs("builtins");
    let names = |env: Arc<EnvSnapshot>| -> Vec<String> {
        let wiring = crate::Wiring::new(env).unwrap();
        let library = super::build(&wiring).unwrap();
        library
            .bundle_list(&library_application::Flags::default())
            .result
            .unwrap()
            .rows
            .into_iter()
            .map(|row| row.name)
            .collect()
    };
    let listed = names(world.env());
    assert!(listed.contains(&"ctxloom".to_string()), "{listed:?}");
    assert!(!listed.contains(&"testing".to_string()), "{listed:?}");
    assert!(listed.contains(&"demo".to_string()), "{listed:?}");
    world.set("CTXLOOM_TEST_NO_BUILTINS", "1");
    assert_eq!(names(world.env()), ["demo", "project"]);
}

// Owner ruling 168: a plan is shown to the engines the run drives, so what
// they say of it (opencode's word on a missing model) is said.
#[test]
fn the_plan_is_shown_to_the_engines_the_run_drives() {
    let names: Vec<String> = super::advising_engines()
        .iter()
        .map(|engine| engine.name().to_string())
        .collect();
    assert_eq!(names, ["claude-code", "opencode"]);
}

// Owner ruling (2026-11-21): every path that evaluates a library sets it up
// as a run does, so a library WITHOUT the remotes is unrepresentable:
// `build` makes the dependency use cases itself and `build_over` takes them
// as required, never an `Option`. A bundle installed from a remote resolves
// in a library built either way.
#[test]
fn a_library_built_with_no_ceremony_reads_the_bundles_installed_from_remotes() {
    let (root, dir) = crate::fixtures::project_with_a_remote_bundle();
    let home = root.root().join("home");
    let wiring = crate::daemon::caller_wiring(
        &home,
        std::path::Path::new("/tmp"),
        (&dir, Some(&dir)),
        [(
            crate::fixtures::home_var(),
            home.display().to_string().as_str(),
        )],
    );
    let library = super::build(&wiring).expect("builds");
    let listed: Vec<String> = library
        .bundle_list(&library_application::Flags::default())
        .result
        .expect("lists")
        .rows
        .into_iter()
        .map(|row| row.name)
        .collect();
    assert!(listed.contains(&"hub/demo".to_string()), "{listed:?}");
}

#[test]
fn the_library_builder_has_no_way_to_leave_the_remotes_out() {
    let source = include_str!("library.rs");
    assert!(
        !source.contains("remotes: Option<Rc<dyn RemoteLibrary>>,\n) -> Result<Library"),
        "library::build must take its remotes unconditionally"
    );
}

// Owner ruling 2026-11-21 (DRAFT wording): a fatal finding is said as the
// library said it, its remedy as the fix line, and one with no remedy has none.
#[test]
fn a_fatal_finding_renders_its_text_and_its_remedy_as_the_fix() {
    let catalog = presentation::Catalog::en_us(&crate::catalog_sources()).expect("builds");
    let with = runs::LaunchFinding {
        text: "failed to load bundle \"hub/demo\"".to_string(),
        remedy: "run `ctxloom deps pull`".to_string(),
    };
    assert_eq!(catalog.message(&with), "failed to load bundle \"hub/demo\"");
    // The fix line is the `-fix` id of the same code, over the same args.
    #[derive(Debug)]
    struct Fix(runs::LaunchFinding);
    impl std::fmt::Display for Fix {
        fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
            coded_diagnostic::display(self, f)
        }
    }
    impl coded_diagnostic::Diagnostic for Fix {
        fn code(&self) -> &str {
            "runs-fatal-finding-fix"
        }
        fn args(&self) -> Vec<(&str, coded_diagnostic::Arg)> {
            coded_diagnostic::Diagnostic::args(&self.0)
        }
    }
    assert_eq!(catalog.message(&Fix(with)), "run `ctxloom deps pull`");
    let bare = runs::LaunchFinding {
        text: "second".to_string(),
        remedy: String::new(),
    };
    assert_eq!(catalog.message(&bare), "second");
}
