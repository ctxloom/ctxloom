//! The scaffold init asks for, wired: `config create` over the real files.

use init_application::Create;

use crate::commands::tests::World;
use crate::fixtures::exists;

use super::ConfigCreate;

fn scaffold(world: &World) -> ConfigCreate {
    let wiring = world.wiring();
    ConfigCreate(
        crate::library::build(&wiring, None).unwrap(),
        std::sync::Arc::clone(wiring.env()),
    )
}

// A directory with no `.ctxloom` gets one: the scaffold is on disk afterwards.
#[test]
fn create_scaffolds_ctxloom_in_the_working_directory() {
    let world = World::new("setup-create");
    let app = world.dir().join("work").join(".ctxloom");
    assert!(!exists(&app));
    scaffold(&world).create().expect("scaffolded");
    assert!(exists(&app), "{} was not made", app.display());
}

// A scaffold that cannot be made is the library's own refusal, not Ok.
#[test]
fn create_fails_when_the_scaffold_cannot_be_made() {
    let world = World::new("setup-create-fails");
    let app = world.dir().join("work").join(".ctxloom");
    crate::fixtures::put_mode(&app, "in the way", 0o644);
    assert!(scaffold(&world).create().is_err());
}

// Rulings 89, 106: the output directory init asks for lands in the config of
// the project it has just scaffolded, through the same library, built before
// the scaffold was there.
#[test]
fn the_output_directory_is_written_into_the_new_projects_config() {
    let world = World::new("setup-output");
    let create = scaffold(&world);
    create.create().expect("scaffolded");
    create.set_output_dir("docs/sessions").expect("written");
    let bytes = crate::fixtures::fs()
        .read(&world.dir().join("work/.ctxloom/config.yaml"))
        .unwrap();
    let config = String::from_utf8(bytes).unwrap();
    assert!(
        config.contains("output:\n  dir: docs/sessions\n"),
        "{config}"
    );
}

// Without a project there is nowhere to write it: refused, not Ok.
#[test]
fn an_output_directory_without_a_project_is_refused() {
    let world = World::new("setup-output-none");
    assert!(scaffold(&world).set_output_dir("docs").is_err());
}
