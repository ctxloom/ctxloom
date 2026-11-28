//! The steps of the launch spec's spec (`cli/launch_spec.feature`, P3, owner
//! rulings 2026-11-25): what the daemon's record says of a run, its turns,
//! the library's files and the user's settings. The record is read as
//! `ctxloom test record` and `ctxloom test library-events` print it; the rows
//! carry each event's fields as JSON (the test build's, additive: a run's
//! `RunEnqueued` row has its `spec` and `grant`, a `TurnCompletedRecorded` row
//! its `answer`, a library `TreeObserved` row its `contents`, each with
//! `{path, content_id, content, sealed_fields}`). Written first, RED: no row
//! says any of it yet.

use cucumber::gherkin::Step;
use cucumber::{given, then};
use serde_json::Value;

use super::World;
use crate::tests::files;
use crate::tests::gherkin_common::doc;

/// `ctxloom args`, now, with the scenario's environment.
fn run_now(w: &World, args: &[&str]) -> crate::tests::Ran {
    let env = super::daemon_spec::env_of(w);
    let env: Vec<(&str, &str)> = env.iter().map(|(k, v)| (k.as_str(), v.as_str())).collect();
    w.common.project.run_with(&env, args)
}

/// Every stream of the one store, as `ctxloom test record` prints it.
fn record(w: &World) -> Vec<Value> {
    let ran = run_now(w, &["test", "record"]);
    assert_eq!(ran.code, 0, "{}", ran.said());
    ran.json().as_array().cloned().unwrap_or_default()
}

/// The events of each stream of `domain`, as (root, events).
fn streams<'a>(record: &'a [Value], domain: &str) -> Vec<(&'a str, &'a [Value])> {
    record
        .iter()
        .filter(|s| s["domain"] == domain)
        .map(|s| {
            (
                s["root"].as_str().unwrap_or_default(),
                s["events"].as_array().map_or(&[][..], Vec::as_slice),
            )
        })
        .collect()
}

/// The event of type `kind` of the (last) run stream that has one.
fn run_event(record: &[Value], kind: &str) -> Option<Value> {
    streams(record, "runs")
        .into_iter()
        .filter_map(|(_, events)| events.iter().find(|e| e["type"] == kind))
        .next_back()
        .cloned()
}

/// The value at the dotted `path` of `value` (an index is a number).
fn at<'a>(value: &'a Value, path: &str) -> Option<&'a Value> {
    path.split('.')
        .try_fold(value, |value, step| match step.parse::<usize>() {
            Ok(n) => value.get(n),
            Err(_) => value.get(step),
        })
}

/// The text of the JSON value `found`, as a person would write it.
fn text_of(found: &Value) -> String {
    match found {
        Value::String(s) => s.clone(),
        other => other.to_string(),
    }
}

#[then(expr = "the record's {word} event says {string} is {string}")]
fn event_says(w: &mut World, kind: String, path: String, expected: String) {
    let record = record(w);
    let event = run_event(&record, &kind)
        .unwrap_or_else(|| panic!("no run recorded a {kind}: {}", w.common.ran().said()));
    let found =
        at(&event, &path).unwrap_or_else(|| panic!("the {kind} row says no {path}: {event}"));
    assert_eq!(text_of(found), expected, "{kind} {path}: {event}");
}

#[then(expr = "the record's {word} event says {string} contains {string}")]
fn event_contains(w: &mut World, kind: String, path: String, expected: String) {
    let record = record(w);
    let event = run_event(&record, &kind)
        .unwrap_or_else(|| panic!("no run recorded a {kind}: {}", w.common.ran().said()));
    let found =
        at(&event, &path).unwrap_or_else(|| panic!("the {kind} row says no {path}: {event}"));
    assert!(
        text_of(found).contains(&expected),
        "{kind} {path} lacks {expected:?}: {event}"
    );
}

/// The environment's variable NAMES, as the spec says them.
#[then(expr = "the record's RunEnqueued event names the environment variable {string}")]
fn event_names_variable(w: &mut World, name: String) {
    let record = record(w);
    let event = run_event(&record, "RunEnqueued").expect("a run was enqueued");
    let names: Vec<&str> = at(&event, "spec.env")
        .and_then(Value::as_array)
        .map_or(Vec::new(), |env| {
            env.iter().filter_map(|e| e["name"].as_str()).collect()
        });
    assert!(names.contains(&name.as_str()), "{names:?}: {event}");
}

/// A secret rides a message only sealed (angzarr-sealed): its plain text is
/// in no event of any stream, as text or as the bytes the store holds.
#[then(expr = "the record holds {string} in no event, in the clear")]
fn record_holds_no_secret(w: &mut World, secret: String) {
    let record = record(w);
    let hex: String = secret.bytes().map(|b| format!("{b:02x}")).collect();
    let text = serde_json::to_string(&record).expect("JSON");
    assert!(!text.contains(&secret), "the record says {secret:?}");
    assert!(!text.contains(&hex), "an event's bytes hold {secret:?}");
}

// ------------------------------------------------------- the library's bytes

/// The library tree's record, as `ctxloom test library-events` prints it.
fn library_events(w: &World) -> Value {
    let ran = run_now(w, &["test", "library-events", "--format", "json"]);
    assert_eq!(ran.code, 0, "{}", ran.said());
    ran.json()
}

/// The `contents` entry for the path ending `suffix`, the latest recorded.
fn content_of(events: &Value, suffix: &str) -> Option<Value> {
    events["events"]
        .as_array()?
        .iter()
        .filter_map(|e| e["contents"].as_array())
        .flatten()
        .rfind(|c| c["path"].as_str().is_some_and(|p| p.ends_with(suffix)))
        .cloned()
}

#[then(expr = "the library's record holds the bytes of {string}, containing {string}")]
fn library_holds_bytes(w: &mut World, suffix: String, expected: String) {
    let events = library_events(w);
    let content = content_of(&events, &suffix)
        .unwrap_or_else(|| panic!("the tree's record holds no bytes of {suffix}: {events}"));
    assert!(
        text_of(&content["content"]).contains(&expected),
        "{suffix}: {content}"
    );
    assert_eq!(
        content["content_id"].as_str().map(str::len),
        Some(64),
        "its content id is the SHA-256: {content}"
    );
}

#[then(expr = "the library's record holds the bytes of {string} once")]
fn library_holds_bytes_once(w: &mut World, suffix: String) {
    let events = library_events(w);
    let count = events["events"].as_array().map_or(0, |events| {
        events
            .iter()
            .filter_map(|e| e["contents"].as_array())
            .flatten()
            .filter(|c| c["path"].as_str().is_some_and(|p| p.ends_with(&suffix)))
            .count()
    });
    assert_eq!(count, 1, "{suffix} recorded {count} times: {events}");
}

#[then(
    expr = "the library's record holds {string} with {int} sealed value and not {string} in the clear"
)]
fn library_seals(w: &mut World, suffix: String, sealed: usize, secret: String) {
    let events = library_events(w);
    let content = content_of(&events, &suffix)
        .unwrap_or_else(|| panic!("the tree's record holds no bytes of {suffix}: {events}"));
    let fields = content["sealed_fields"].as_array().map_or(0, Vec::len);
    assert_eq!(fields, sealed, "{content}");
    assert!(
        !events.to_string().contains(&secret),
        "{secret:?} is in the record"
    );
}

// ------------------------------------------------------- the user's settings

#[given(expr = "Alice's claude settings, in her own home, say:")]
fn claude_settings(w: &mut World, step: &Step) {
    files::put(
        w.common.project.home().join(".claude/settings.json"),
        doc(step),
    );
}

#[then(expr = "the record holds a {string} stream whose event says {string} is {string}")]
fn settings_stream(w: &mut World, domain: String, path: String, expected: String) {
    let record = record(w);
    let found = streams(&record, &domain)
        .into_iter()
        .flat_map(|(_, events)| events.iter())
        .find_map(|event| at(event, &path).map(text_of));
    assert_eq!(found.as_deref(), Some(expected.as_str()), "{record:?}");
}

#[then(expr = "the record's RunEnqueued event names the user settings it read")]
fn run_names_settings(w: &mut World) {
    let record = record(w);
    let event = run_event(&record, "RunEnqueued").expect("a run was enqueued");
    assert!(
        at(&event, "spec.user_settings_sequence").is_some(),
        "the spec names no user-settings sequence: {event}"
    );
}

// ------------------------------------------------------------------ hand-off

#[then(expr = "no hand-off file is kept for {string}")]
fn no_hand_off_file(w: &mut World, _harp: String) {
    let launch = w.common.project.home().join(".ctxloom/daemon/launch");
    let kept = files::children(&launch);
    assert!(
        kept.is_empty(),
        "a child's launch inputs are in its stream, not a file: {kept:?}"
    );
}
