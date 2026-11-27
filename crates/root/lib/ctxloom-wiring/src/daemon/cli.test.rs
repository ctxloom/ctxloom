//! The `daemon` noun's renders: what `status` and `stop` say.

/// What `render` says of `value` in text.
fn said_text(value: &impl presentation::Render) -> String {
    let mut out = Vec::new();
    presentation::render(&mut out, presentation::Format::Text, value).expect("rendered");
    String::from_utf8(out).expect("utf-8")
}

// Smoke 2026-11-19: `daemon stop` says what it stopped (DRAFT text), or
// that nothing was serving, and its json is the stopped daemon's message.
#[test]
fn daemon_stop_says_what_it_stopped_and_when_nothing_served() {
    use super::Stopped;
    let daemon = daemon_proto::v1::Daemon {
        pid: 4242,
        version: "v0.1.0-abc1234".to_string(),
        ..daemon_proto::v1::Daemon::default()
    };
    assert_eq!(
        said_text(&Stopped(Some(daemon.clone()), false)),
        "stopped the ctxloom daemon (pid 4242), ctxloom v0.1.0-abc1234\n"
    );
    assert_eq!(
        said_text(&Stopped(Some(daemon), true)),
        "drained and stopped the ctxloom daemon (pid 4242), ctxloom v0.1.0-abc1234\n"
    );
    assert_eq!(
        said_text(&Stopped(None, false)),
        "no ctxloom daemon was serving: nothing to stop\n"
    );
}

/// What `render` says of `value` in json.
fn said_json(value: &impl presentation::Render) -> serde_json::Value {
    let mut out = Vec::new();
    presentation::render(&mut out, presentation::Format::Json, value).expect("rendered");
    serde_json::from_slice(&out).expect("json")
}

// Dogfood fix 3 (owner): ONE json schema for `daemon status` and `daemon
// stop`: `{"daemons": [...]}`, the daemon records as the proto3 JSON, an
// empty list when none served (the text carries the message).
#[test]
fn daemon_status_and_stop_json_are_a_list_of_daemons() {
    use super::{Status, Stopped};
    assert_eq!(
        said_json(&Status(None, Vec::new())),
        serde_json::json!({"daemons": []})
    );
    assert_eq!(
        said_json(&Stopped(None, false)),
        serde_json::json!({"daemons": []})
    );
    let daemon = daemon_proto::v1::Daemon {
        pid: 4242,
        version: "v0.1.0-abc1234".to_string(),
        ..daemon_proto::v1::Daemon::default()
    };
    for json in [
        said_json(&Status(Some(daemon.clone()), Vec::new())),
        said_json(&Stopped(Some(daemon.clone()), true)),
    ] {
        let listed = json["daemons"].as_array().expect("a list");
        assert_eq!(listed.len(), 1, "{json}");
        assert_eq!(listed[0]["pid"], 4242, "{json}");
        assert_eq!(json.as_object().unwrap().len(), 1, "only `daemons`: {json}");
    }
}
