//! A view as its API message.

use serde_json::json;

use super::*;

fn json_of<M: serde::Serialize>(message: &M) -> serde_json::Value {
    serde_json::to_value(message).expect("serializes")
}

// A document keeps its paths and its values; numbers are doubles, printed
// whole where they are whole; what is not an object is no Struct.
#[test]
fn test_a_document_is_its_struct() {
    let written = json!({"defaultMode": "plan", "allow": ["Bash(git:*)"], "n": 2, "f": 1.5, "on": true, "none": null});
    let made = document(&written).expect("an object");
    assert_eq!(json_of(&made), written);
    assert!(document(&json!("plan")).is_none());
    assert!(document(&json!([1])).is_none());
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
