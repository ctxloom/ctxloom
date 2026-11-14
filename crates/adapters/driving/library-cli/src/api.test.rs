//! A view as its API message.

use serde_json::json;

use super::*;

pub(crate) fn json_of<M: serde::Serialize>(message: &M) -> serde_json::Value {
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
        session_home: library_application::SourcedValue {
            value: "session".to_string(),
            source: "default",
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
