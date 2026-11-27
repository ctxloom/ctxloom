//! The hidden `ctxloom test` commands that read or write the store, run as
//! the daemon's entry takes them ([`entry`](crate::entry)): by their verbs.
//! Their command-line lines are the wiring's (`test_commands`) and the
//! acceptance features'.

use ctxloom_compose::fixtures::{World, VERSION};
use ctxloom_compose::{SeedArgs, StoreVerb};

use crate::seed::Seed;
use crate::store_tests::{run, Harness};

/// The verb run for `world`: what it printed, or its exit and code.
fn ran(world: &World, verb: StoreVerb) -> Result<String, (u8, String)> {
    let mut out = Vec::new();
    run(&world.wiring(), VERSION, verb, &mut out)
        .map(|()| String::from_utf8(out).unwrap())
        .map_err(|failed| {
            (
                failed.exit.code(true),
                coded_diagnostic::Diagnostic::code(&*failed.cause).to_string(),
            )
        })
}

fn seed(harp: &str, transcript: bool) -> StoreVerb {
    StoreVerb::SeedSession {
        args: Box::new(SeedArgs {
            harp: harp.to_string(),
            transcript,
            lock: "none".to_string(),
            started_ago: 86_400,
            ..SeedArgs::default()
        }),
        loadout: None,
    }
}

#[test]
fn open_store_prints_the_path_of_the_projects_database() {
    let world = World::new("test-open-store");
    let open = |project: &str| StoreVerb::OpenStore {
        project: project.to_string(),
    };
    let out = ran(&world, open("proj-1")).unwrap();
    let path = Harness(&world.wiring()).open_store("proj-1").unwrap();
    assert_eq!(out, format!("{}\n", path.display()));
    assert!(path.starts_with(world.home()), "{path:?}");
    assert_eq!(ran(&world, open("../escape")).unwrap_err().0, 32);
}

#[test]
fn seed_session_plants_a_session_and_prints_its_id() {
    let world = World::new("test-seed-session");
    let out = ran(&world, seed("jolly-mute-magma", true)).unwrap();
    let id = out.strip_suffix('\n').unwrap();
    assert!(uuid::Uuid::parse_str(id).is_ok(), "{id}");
    let wiring = world.wiring();
    let name = Harness(&wiring)
        .session_resource("jolly-mute-magma")
        .unwrap();
    assert_eq!(
        sessions::SessionName::parse(&name)
            .unwrap()
            .id()
            .to_string(),
        id
    );
}

#[test]
fn a_seed_that_cannot_plant_ends_in_a_failure() {
    let world = World::new("test-seed-fails");
    // The same name twice: the second claim is refused.
    ran(&world, seed("jolly-mute-magma", false)).unwrap();
    assert_eq!(
        ran(&world, seed("jolly-mute-magma", false)).unwrap_err(),
        (32, "ctxloom-compose-seed".to_string())
    );
}

#[test]
fn a_half_named_native_binding_keeps_the_half_it_has() {
    let args = |id: Option<&str>, transcript: Option<&str>, lock: &str| SeedArgs {
        harp: "h".to_string(),
        lock: lock.to_string(),
        native_id: id.map(str::to_string),
        native_transcript: transcript.map(str::to_string),
        ..SeedArgs::default()
    };
    assert_eq!(
        Seed::from(args(Some("n1"), None, "none")).native,
        Some(("n1".to_string(), String::new()))
    );
    assert_eq!(
        Seed::from(args(None, Some("/t"), "none")).native,
        Some((String::new(), "/t".to_string()))
    );
    assert!(!Seed::from(args(None, None, "none")).lock_free);
    assert!(Seed::from(args(None, None, "free")).lock_free);
}
