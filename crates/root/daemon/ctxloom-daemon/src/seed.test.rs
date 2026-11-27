//! The seed the acceptance tests plant sessions with, and the sessions use
//! cases it plants them for: what it writes is what the commands then read.

use std::time::{Duration, SystemTime};

use crate::fixtures::fs;
use crate::seed::Seed;
use crate::store_tests::Harness;
use crate::TestError;
use ctxloom_wiring::fixtures::World;

const HARP: &str = "jolly-mute-magma";

fn seed() -> Seed {
    Seed {
        harp: HARP.to_string(),
        started_ago: 7200,
        ended_ago: Some(3600),
        lock_free: true,
        transcript: true,
        native: Some(("native-1".to_string(), "/t/native-1.jsonl".to_string())),
        other_files: vec!["notes/plan.md".to_string()],
        ..Seed::default()
    }
}

fn ago(secs: u64) -> SystemTime {
    SystemTime::now() - Duration::from_secs(secs)
}

fn near(at: SystemTime, expected: SystemTime) {
    let off = at.duration_since(expected).unwrap_or_else(|e| e.duration());
    assert!(
        off < Duration::from_secs(120),
        "{at:?} is not near {expected:?}"
    );
}

// The seed writes beside the session what Go's fixture writes: the authored
// files and the lock, each backdated to the end; no transcript file (ruling
// 87a: its transcript is its events).
#[test]
fn the_seed_plants_the_sessions_files_backdated_to_its_end() {
    let world = World::new("seed-files");
    Harness(&world.wiring()).seed_session(&seed()).unwrap();
    // The seed marks the project in the work directory's `.ctxloom`, which
    // is then the project's directory.
    let sessions = world.work().join(".ctxloom/sessions");
    let dir = sessions.join(HARP);
    assert!(!fs().exists(&dir.join("transcripts")).unwrap());
    assert_eq!(
        fs().read_to_string(&dir.join("notes/plan.md")).unwrap(),
        "authored\n"
    );
    let lock = dir.join("session.lock");
    for path in [dir.join("notes/plan.md"), lock] {
        near(fs().metadata(&path).unwrap().modified, ago(3600));
    }
}

#[test]
fn a_session_that_never_ended_keeps_its_files_as_they_were_written() {
    let world = World::new("seed-live");
    let live = Seed {
        ended_ago: None,
        ..seed()
    };
    Harness(&world.wiring()).seed_session(&live).unwrap();
    let authored = world
        .work()
        .join(".ctxloom/sessions")
        .join(HARP)
        .join("notes/plan.md");
    near(
        fs().metadata(&authored).unwrap().modified,
        SystemTime::now(),
    );
}

#[test]
fn a_name_already_claimed_is_refused_as_a_seed_error() {
    let world = World::new("seed-claimed");
    let wiring = world.wiring();
    Harness(&wiring).seed_session(&seed()).unwrap();
    match Harness(&wiring).seed_session(&seed()) {
        Err(TestError::Seed { detail }) => assert!(!detail.is_empty()),
        other => panic!("{other:?}"),
    }
}

// Step 10c U13's harness: a session seeded on a profile is one whose run
// was planned on it: its loadout, as the daemon assembled the profile, is
// recorded on it, so the index's record carries it.
#[test]
fn a_seed_with_a_loadout_records_it_on_the_session() {
    let world = World::new("seed-profile");
    let wiring = world.wiring();
    let loadout = sessions::SessionLoadout {
        agent: String::new(),
        profiles: vec!["dev".to_string()],
        bundles: vec!["demo".to_string()],
    };
    let on_profile = Seed {
        loadout: Some(loadout.clone()),
        ..seed()
    };
    Harness(&wiring).seed_session(&on_profile).unwrap();
    let events = Harness(&wiring).session_events(HARP, None).unwrap();
    let recorded: Vec<&serde_json::Value> = events
        .as_array()
        .unwrap()
        .iter()
        .filter(|row| row["type"] == "LoadoutRecorded")
        .collect();
    assert_eq!(recorded.len(), 1, "{events}");
    assert_eq!(recorded[0]["bundles"], serde_json::json!(["demo"]));
    assert_eq!(recorded[0]["profiles"], serde_json::json!(["dev"]));
}

fn types(events: &serde_json::Value) -> Vec<String> {
    events
        .as_array()
        .unwrap()
        .iter()
        .map(|row| row["type"].as_str().unwrap().to_string())
        .collect()
}

// What the seed answers is the stable id of the session it minted, which is
// a UUID; the session's stream holds its mint, its engine binding and the
// prompt its fixture's transcript holds, as its hooks would deliver them.
#[test]
fn a_seeded_session_is_minted_bound_and_captured_on_its_stream() {
    let world = World::new("seed-events");
    let wiring = world.wiring();
    let id = Harness(&wiring).seed_session(&seed()).unwrap();
    assert!(uuid::Uuid::parse_str(&id).is_ok(), "{id}");
    let name = Harness(&wiring).session_resource(HARP).unwrap();
    assert_eq!(
        sessions::SessionName::parse(&name)
            .unwrap()
            .id()
            .to_string(),
        id
    );
    let seen = types(&Harness(&wiring).session_events(HARP, None).unwrap());
    for expected in ["SessionMinted", "EngineSessionBound", "PromptSubmitted"] {
        assert!(seen.iter().any(|t| t == expected), "{expected} in {seen:?}");
    }
}
