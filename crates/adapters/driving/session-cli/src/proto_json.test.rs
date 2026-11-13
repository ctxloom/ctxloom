//! Proto3 JSON, as ruling 146 has the daemon's answers printed.

use daemon_proto::v1;
use serde_json::json;
use sessions_application::Timestamp;

use super::{daemon, session, sessions};

// A field at its default is left out; an enum is its name; a Timestamp is
// RFC 3339 with Z; a uint32 a number.
#[test]
fn a_session_is_its_proto3_json() {
    let at = Timestamp {
        seconds: 1_784_309_252,
        nanos: 0,
    };
    let s = v1::Session {
        name: "projects/calm-otter/sessions/amber-swift-owl".to_string(),
        uid: "u-1".to_string(),
        state: v1::SessionState::Ended as i32,
        engine: "claude-code".to_string(),
        start_time: Some(at),
        kept: true,
        children: vec!["projects/calm-otter/sessions/brisk-copper-moth".to_string()],
        pending: vec![v1::PendingMessage {
            message_id: "m1".to_string(),
            urgency: "inform".to_string(),
            text: "hi".to_string(),
            sender: String::new(),
        }],
        incarnation: 2,
        title: "the auth refactor".to_string(),
        ..v1::Session::default()
    };
    assert_eq!(
        session(&s),
        json!({
            "name": "projects/calm-otter/sessions/amber-swift-owl",
            "uid": "u-1",
            "state": "SESSION_STATE_ENDED",
            "engine": "claude-code",
            "start_time": "2026-07-17T17:27:32Z",
            "kept": true,
            "children": ["projects/calm-otter/sessions/brisk-copper-moth"],
            "pending": [{"message_id": "m1", "urgency": "inform", "text": "hi"}],
            "incarnation": 2,
            "title": "the auth refactor",
        })
    );
    assert_eq!(session(&v1::Session::default()), json!({}));
}

// An empty listing is the empty object; a listing's sessions are a list.
#[test]
fn a_listing_is_its_proto3_json() {
    assert_eq!(sessions(&v1::ListSessionsResponse::default()), json!({}));
    let listed = v1::ListSessionsResponse {
        sessions: vec![v1::Session {
            name: "projects/a/sessions/b".to_string(),
            ..v1::Session::default()
        }],
        next_page_token: String::new(),
    };
    assert_eq!(
        sessions(&listed),
        json!({"sessions": [{"name": "projects/a/sessions/b"}]})
    );
}

// The daemon: its 64-bit counts are decimal strings, a run's kind its name;
// it holds no bearer to print.
#[test]
fn the_daemon_is_its_proto3_json() {
    let d = v1::Daemon {
        name: "daemon".to_string(),
        pid: 4242,
        endpoint: "https://127.0.0.1:5000".to_string(),
        runs: vec![v1::HostedRun {
            harp: "brisk-copper-moth".to_string(),
            kind: v1::RunKind::Child as i32,
            ..v1::HostedRun::default()
        }],
        idle_seconds: 0,
        idle_limit_seconds: 600,
        ..v1::Daemon::default()
    };
    assert_eq!(
        daemon(&d),
        json!({
            "name": "daemon",
            "pid": 4242,
            "endpoint": "https://127.0.0.1:5000",
            "runs": [{"harp": "brisk-copper-moth", "kind": "RUN_KIND_CHILD"}],
            "idle_limit_seconds": "600",
        })
    );
}
