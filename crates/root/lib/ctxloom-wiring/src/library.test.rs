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
        let library = super::build(&wiring, None).unwrap();
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
