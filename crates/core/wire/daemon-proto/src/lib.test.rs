//! The resource messages carry the names a projection's `Any` is read by.

use super::v1;
use prost::Name;

#[test]
fn test_a_session_is_named_by_its_full_name() {
    assert_eq!(v1::Session::type_url(), "/ctxloom.v1.Session");
}

// Slice 2 step 5 (S11): a refusal's status is the one its proto declares,
// generated, never spelled in Rust.
#[test]
fn test_each_refusal_declares_the_status_its_proto_says() {
    use crate::Refusal;
    assert_eq!(<v1::DaemonHosting as Refusal>::REFUSAL_STATUS, 9);
    assert_eq!(<v1::SessionNotFound as Refusal>::REFUSAL_STATUS, 5);
    assert_eq!(<v1::ProjectNotFound as Refusal>::REFUSAL_STATUS, 5);
    assert_eq!(<v1::NameInvalid as Refusal>::REFUSAL_STATUS, 3);
    assert_eq!(<v1::PlanStale as Refusal>::REFUSAL_STATUS, 9);
}

fn json<T: serde::Serialize>(value: &T) -> serde_json::Value {
    serde_json::to_value(value).expect("serializes")
}

// OQ-7: a default is left out, but a yes/no fact with presence prints
// false; a count is a JSON number (OQ-12).
#[test]
fn test_a_fact_with_presence_prints_false_and_a_default_is_left_out() {
    let preview = v1::DeleteBundleResponse {
        bundle: Some(v1::Bundle {
            name: "projects/calm-otter/bundles/demo".to_string(),
            origin: v1::Origin::Project as i32,
            fragment_count: 2,
            held: None,
            ..v1::Bundle::default()
        }),
        applied: Some(false),
        apply_command: "ctxloom bundle remove demo --yes".to_string(),
        ..v1::DeleteBundleResponse::default()
    };
    assert_eq!(
        json(&preview),
        serde_json::json!({
            "bundle": {
                "name": "projects/calm-otter/bundles/demo",
                "origin": "PROJECT",
                "fragment_count": 2,
            },
            "applied": false,
            "apply_command": "ctxloom bundle remove demo --yes",
        })
    );
    assert_eq!(
        json(&v1::ListBundlesResponse::default()),
        serde_json::json!({})
    );
}

// OQ-11: a free-form document is the JSON value itself (a Value, owner
// ruling 2026-11-13), so an object's paths keep their shape and what is no
// object is printed as written; OQ-12: a byte size is a decimal string.
#[test]
fn test_a_document_is_the_value_itself_and_bytes_a_decimal_string() {
    use crate::google::protobuf::{value::Kind, ListValue, Struct, Value};
    let word = |text: &str| Value {
        kind: Some(Kind::StringValue(text.to_string())),
    };
    let document = Value {
        kind: Some(Kind::StructValue(Struct {
            fields: [("defaultMode".to_string(), word("acceptEdits"))]
                .into_iter()
                .collect(),
        })),
    };
    let agent = |value: Value| v1::EngineAgent {
        agent: "plain".to_string(),
        permissions: Some(v1::SourcedDocument {
            value: Some(value),
            source: "default".to_string(),
        }),
        ..v1::EngineAgent::default()
    };
    assert_eq!(
        json(&agent(document)),
        serde_json::json!({
            "agent": "plain",
            "permissions": {"value": {"defaultMode": "acceptEdits"}, "source": "default"},
        })
    );
    let listed = Value {
        kind: Some(Kind::ListValue(ListValue {
            values: vec![word("Bash")],
        })),
    };
    assert_eq!(
        json(&agent(listed))["permissions"]["value"],
        serde_json::json!(["Bash"])
    );
    assert_eq!(
        json(&agent(word("plan")))["permissions"]["value"],
        serde_json::json!("plan")
    );
    let exported = v1::ExportSkillResponse {
        bytes: 4096,
        ..v1::ExportSkillResponse::default()
    };
    assert_eq!(json(&exported), serde_json::json!({"bytes": "4096"}));
}
