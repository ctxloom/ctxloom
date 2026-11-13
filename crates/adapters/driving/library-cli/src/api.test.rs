//! A view as its API message.

use serde_json::json;

use super::*;

fn json_of<M: serde::Serialize>(message: &M) -> serde_json::Value {
    serde_json::to_value(message).expect("serializes")
}

// OQ-11 (owner ruling 2026-11-13, superseding Struct): a document is a
// `google.protobuf.Value`, so whatever was written is printed as written,
// an object, a list, a word, a number, a yes/no or null alike; numbers are
// doubles, printed whole where they are whole.
#[test]
fn test_a_document_is_its_value_whatever_its_kind() {
    for written in [
        json!({"defaultMode": "plan", "allow": ["Bash(git:*)"], "n": 2, "f": 1.5, "on": true, "none": null}),
        json!(["Bash", 2, {"deny": "WebFetch"}]),
        json!("plan"),
        json!(7),
        json!(false),
        json!(null),
    ] {
        assert_eq!(json_of(&document(&written)), written);
    }
}

// An agent's permissions document that is no object reaches `engine
// show`'s JSON as written, not left out (rulings 133-139: verbatim).
#[test]
fn test_a_permissions_document_that_is_no_object_is_printed_as_written() {
    let agent = |value: serde_json::Value| AgentSettings {
        model: library_application::SourcedValue {
            value: None,
            source: "default",
        },
        effort: library_application::SourcedValue {
            value: None,
            source: "default",
        },
        permissions: library_application::PermissionsSetting {
            value: Some(value),
            source: "agent",
            line: String::new(),
        },
        runtime: library_application::RuntimeSetting {
            value: "host".to_string(),
            source: "default",
            overrides: None,
            overrides_source: None,
            change: None,
        },
        env: library_application::EnvSetting {
            value: Vec::new(),
            source: "default",
            keys: Vec::new(),
        },
    };
    assert_eq!(
        json_of(&settings(&agent(json!(["Bash", "Read"]))))["permissions"],
        json!({"value": ["Bash", "Read"], "source": "agent"})
    );
    assert_eq!(
        json_of(&settings(&agent(json!("plan"))))["permissions"],
        json!({"value": "plan", "source": "agent"})
    );
}

fn setting(value: serde_json::Value, state: &'static str) -> AuthSetting {
    AuthSetting {
        value,
        source: "engine",
        state,
        via: None,
    }
}

// A word is the word; a source its variables and file; each state the
// enum's value, an unknown one unspecified (left out).
#[test]
fn test_an_auth_source_is_its_value_and_its_state_the_enum() {
    assert_eq!(
        json_of(&auth(&setting(json!("login"), "present"))),
        json!({"value": {"word": "login"}, "source": "engine", "state": "AUTH_STATE_PRESENT"})
    );
    let mut source = setting(json!({"env": ["A", "B"], "file": "~/k"}), "missing");
    source.via = Some("A".to_string());
    assert_eq!(
        json_of(&auth(&source)),
        json!({"value": {"env": ["A", "B"], "file": "~/k"}, "source": "engine", "state": "AUTH_STATE_MISSING", "via": "A"})
    );
    assert_eq!(
        json_of(&auth(&setting(json!({"env": ["A"]}), "not-needed"))),
        json!({"value": {"env": ["A"]}, "source": "engine", "state": "AUTH_STATE_NOT_NEEDED"})
    );
    assert_eq!(
        json_of(&auth(&setting(json!(7), "unheard-of"))),
        json!({"value": {}, "source": "engine"})
    );
}
