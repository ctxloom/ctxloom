//! Acceptance: what reclaims a session's HOLD (s4-Q14, unit H4), through the
//! real binary: `session remove` warns of the held facts it discards (owner
//! H-Q8 (a)), doctor reports what is held (`DOCTOR-CHECK-HELD-FACTS-h1`),
//! and `clean` never takes a hold (it is no cache).
//!
//! The hold is written as a hook writes it, through fact-spool, under the
//! test's ctxloom home. Its facts are of a NEWER ctxloom than the binary
//! (`v99.0.0`), so no command's flush on its way in delivers them (H-Q1
//! (b)): they stay held, as a downgrade's do (H-Q7 (a)).

use std::path::PathBuf;

use daemon_proto::google::protobuf::Timestamp;
use run_ports::{Held, HeldFact, HeldFacts};
use serde_json::Value;

use super::{Env, Ran};
use crate::tests::files;

const HARP: &str = "amber-swift-owl";
const NEWER: &str = "v99.0.0-abcdef0";
const MARKER: &str = "DOCTOR-CHECK-HELD-FACTS-h1";

/// A home with an ended session, its lock free, past its retention.
fn ended() -> Env {
    let env = Env::new();
    let ran = env.run(&[
        "test",
        "seed-session",
        HARP,
        "--lock",
        "free",
        "--transcript",
        "--started-ago",
        "6048000",
        "--ended-ago",
        "5184000",
    ]);
    assert_eq!(ran.code, 0, "seed: {}", ran.said());
    env
}

/// The session's resource name, as the daemon lists it.
fn name_of(env: &Env, harp: &str) -> String {
    let listed = env.run(&["session", "list", "--all", "--format", "json"]);
    assert_eq!(listed.code, 0, "{}", listed.said());
    listed.json()["sessions"]
        .as_array()
        .expect("sessions")
        .iter()
        .filter_map(|s| s["name"].as_str())
        .find(|name| name.ends_with(&format!("/sessions/{harp}")))
        .unwrap_or_else(|| panic!("no {harp}: {}", listed.said()))
        .to_string()
}

/// The home's holds.
fn spool(env: &Env) -> fact_spool::Spool {
    fact_spool::Spool::at_home(files::fs(), env.home())
}

/// Hold `count` facts of the session `name` by a newer ctxloom, held now.
fn hold(env: &Env, name: &str, count: usize) -> PathBuf {
    let spool = spool(env);
    // The session's stream root, which its hold is named by.
    let root = sessions::SessionName::parse(name).expect("a name").id();
    let now = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap()
        .as_secs();
    for n in 0..count {
        let held = spool
            .hold(
                &root,
                HeldFact {
                    stamp: NEWER.to_string(),
                    session: name.to_string(),
                    external_id: format!("hook:prompt:{root}:p{n}"),
                    book: Vec::new(),
                    held_at: Some(Timestamp {
                        seconds: i64::try_from(now).unwrap(),
                        nanos: 0,
                    }),
                    incarnation: 0,
                },
            )
            .expect("held");
        assert_eq!(held, Held::Kept);
    }
    spool.hold_path(&root)
}

/// The warning a json run said on stderr with `code`, if any.
fn warned(ran: &Ran, code: &str) -> Option<Value> {
    // Each envelope is a pretty-printed document of its own.
    serde_json::Deserializer::from_str(&ran.stderr)
        .into_iter::<Value>()
        .map_while(Result::ok)
        .find(|doc| doc["code"] == code)
}

/// Doctor's held-facts row.
fn held_row(env: &Env) -> Value {
    let ran = env.run(&["doctor", "--format", "json"]);
    let doc = ran.json();
    doc["checks"]
        .as_array()
        .expect("checks")
        .iter()
        .find(|c| c["marker"] == MARKER)
        .unwrap_or_else(|| panic!("no {MARKER} row: {}", ran.said()))
        .clone()
}

// Proposed session_remove.feature: "Removing a session warns that its held
// facts go with it" (H-Q8 (a)).
#[test]
fn test_removing_a_session_warns_that_its_held_facts_go_with_it() {
    if !crate::tests::files::needs_purge() {
        return;
    }
    let env = ended();
    let name = name_of(&env, HARP);
    let held = hold(&env, &name, 3);
    let ran = env.run(&["session", "remove", HARP, "--yes", "--format", "json"]);
    assert_eq!(ran.code, 0, "{}", ran.said());
    let warning = warned(&ran, "sessions-application-destroys-held")
        .unwrap_or_else(|| panic!("no held-facts warning: {}", ran.said()));
    let said = warning.to_string();
    assert!(said.contains(HARP) && said.contains('3'), "{said}");
    assert!(!files::exists(&held), "the hold goes with the session");
}

// A removal's report warns of the held facts, and keeps them.
#[test]
fn test_a_removal_report_warns_of_the_held_facts_and_keeps_them() {
    if !crate::tests::files::needs_purge() {
        return;
    }
    let env = ended();
    let name = name_of(&env, HARP);
    let held = hold(&env, &name, 2);
    let ran = env.run(&["session", "remove", HARP, "--format", "json"]);
    assert_eq!(ran.code, 0, "{}", ran.said());
    assert!(
        warned(&ran, "sessions-application-destroys-held").is_some(),
        "{}",
        ran.said()
    );
    assert!(files::exists(&held), "a report discards nothing");
}

// Proposed doctor.feature: "Doctor reports facts held that no daemon of this
// version can take" (H-Q7 (a)).
#[test]
fn test_doctor_reports_facts_held_that_no_daemon_of_this_version_can_take() {
    let env = ended();
    let name = name_of(&env, HARP);
    hold(&env, &name, 3);
    let row = held_row(&env);
    assert_eq!(row["status"], "warn", "{row}");
    let detail = row["detail"].as_str().unwrap_or_default();
    for said in [HARP, "3 fact", NEWER] {
        assert!(detail.contains(said), "{said}: {row}");
    }
}

// Proposed doctor.feature: a hold's set-aside part (what a flush could never
// record) is warned of until it goes.
#[test]
fn test_doctor_warns_of_a_set_aside_hold() {
    let env = ended();
    let name = name_of(&env, HARP);
    let aside = spool(&env).dir().join(format!(
        "{}.held.corrupt-1",
        sessions::SessionName::parse(&name).unwrap().id()
    ));
    files::put(&aside, "not a record");
    let row = held_row(&env);
    assert_eq!(row["status"], "warn", "{row}");
    let detail = row["detail"].as_str().unwrap_or_default();
    assert!(detail.contains("set-aside"), "{row}");
}

// Nothing held is ok.
#[test]
fn test_doctor_says_nothing_is_held_where_nothing_is() {
    let env = Env::new();
    let row = held_row(&env);
    assert_eq!(row["status"], "ok", "{row}");
}

// Design §6: `ctxloom clean` takes the project's regenerable cache, never a
// hold, which is no cache (the record not yet recorded).
#[test]
fn test_clean_never_takes_a_hold() {
    let env = ended();
    let name = name_of(&env, HARP);
    let held = hold(&env, &name, 1);
    let ran = env.run(&["clean", "--yes", "--format", "json"]);
    assert_eq!(ran.code, 0, "{}", ran.said());
    assert!(files::exists(&held), "clean left the hold: {}", ran.said());
}
