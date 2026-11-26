//! The hidden `ctxloom test` commands that read or write the store, run
//! through the command line as the binary hands them in ([`hosting`](crate::hosting)).

use ctxloom_wiring::fixtures::{run_line, try_line, World};
use ctxloom_wiring::SeedArgs;
use presentation::Format;
use sessions_application::{ListQuery, SessionService};

use crate::seed::Seed;
use crate::store_tests::Harness;

fn commands(world: &World) -> ctxloom_wiring::Commands {
    world.commands().hosted_by(crate::hosting())
}

fn failure(world: &World, line: &[&str]) -> (u8, String) {
    let failed = try_line(&mut commands(world), Format::Json, line).unwrap_err();
    (
        failed.exit.code(true),
        coded_diagnostic::Diagnostic::code(&*failed.cause).to_string(),
    )
}

fn block<T>(future: impl std::future::Future<Output = T>) -> T {
    tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
        .unwrap()
        .block_on(future)
}

#[test]
fn open_store_prints_the_path_of_the_projects_database() {
    let world = World::new("test-open-store");
    let (_, out, _) = run_line(&mut commands(&world), &["test", "open-store", "proj-1"]);
    let path = Harness(&world.wiring()).open_store("proj-1").unwrap();
    assert_eq!(out, format!("{}\n", path.display()));
    assert!(path.starts_with(world.home()), "{path:?}");
    assert_eq!(failure(&world, &["test", "open-store", "../escape"]).0, 32);
}

#[test]
fn seed_session_plants_a_session_and_prints_its_id() {
    let world = World::new("test-seed-session");
    let (_, out, _) = run_line(
        &mut commands(&world),
        &["test", "seed-session", "jolly-mute-magma", "--transcript"],
    );
    let id = out.strip_suffix('\n').unwrap();
    assert!(uuid::Uuid::parse_str(id).is_ok(), "{id}");
    let mut sessions = {
        let w = world.wiring();
        Harness(&w).sessions().use_cases
    };
    let views = block(sessions.list(ListQuery::default())).unwrap();
    assert_eq!(views.len(), 1);
    assert_eq!(views[0].record.id, id);
    assert_eq!(views[0].record.harp, "jolly-mute-magma");
}

#[test]
fn a_seed_that_cannot_plant_ends_in_a_failure() {
    let world = World::new("test-seed-fails");
    // The same name twice: the second claim is refused.
    let line = ["test", "seed-session", "jolly-mute-magma"];
    run_line(&mut commands(&world), &line);
    assert_eq!(
        failure(&world, &line),
        (32, "ctxloom-wiring-seed".to_string())
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

// T17, S48: a command in a project that still has a store of its own is
// refused, exit 48, before it runs; the store is named for its removal.
#[test]
fn a_command_in_a_project_with_a_store_of_its_own_is_refused() {
    let world = World::new("per-project-store");
    let work = world.dir().join("work");
    ctxloom_wiring::fixtures::mkdirs(work.join(".ctxloom"));
    ctxloom_wiring::fixtures::mkdirs(world.dir().join("home/.ctxloom/projects/old-proj"));
    ctxloom_wiring::fixtures::put_mode(work.join(".ctxloom/project-id"), "old-proj\n", 0o644);
    ctxloom_wiring::fixtures::put_mode(
        world
            .dir()
            .join("home/.ctxloom/projects/old-proj/ctxloom.db"),
        "an old store",
        0o644,
    );
    let failed =
        try_line(&mut commands(&world), Format::Json, &["session", "list"]).expect_err("refused");
    assert_eq!(failed.exit.code(true), 48);
    assert_eq!(
        coded_diagnostic::Diagnostic::code(&*failed.cause),
        "ctxloom-store-sqlite-per-project-store"
    );
    assert!(!ctxloom_wiring::fixtures::exists(
        world.dir().join("home/.ctxloom/ctxloom.db")
    ));
}
