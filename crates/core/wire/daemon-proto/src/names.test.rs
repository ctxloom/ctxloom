//! An unresolved name prints as `null` (owner ruling 2026-11-14).

use super::with_unresolved_names;
use crate::v1;

// A name that resolved prints as it is; one that did not is `null`, first,
// never left out.
#[test]
fn test_an_unresolved_name_is_null_and_a_resolved_one_is_kept() {
    let unresolved = v1::Dependency::default();
    assert_eq!(
        with_unresolved_names(&unresolved, &["name"]).unwrap(),
        serde_json::json!({"name": null})
    );
    let resolved = v1::Dependency {
        name: "projects/p/dependencies/0123456789abcdef".to_string(),
        held: Some(true),
        ..v1::Dependency::default()
    };
    assert_eq!(
        with_unresolved_names(&resolved, &["name"]).unwrap(),
        serde_json::json!({"name": "projects/p/dependencies/0123456789abcdef", "held": true})
    );
    let held = v1::Dependency {
        held: Some(false),
        ..v1::Dependency::default()
    };
    let printed = with_unresolved_names(&held, &["name"]).unwrap();
    let keys: Vec<&String> = printed.as_object().unwrap().keys().collect();
    assert_eq!(keys.first().map(|k| k.as_str()), Some("name"), "{printed}");
    assert_eq!(printed["name"], serde_json::Value::Null);
}

// A name below a message that is not there: the message is there, holding
// the null name, first (`bundle.name` of a move into a directory).
#[test]
fn test_a_nested_name_brings_its_message() {
    let moved = v1::MoveBundleResponse {
        path: "other/.ctxloom/content/bundles/v2/demo".to_string(),
        applied: Some(true),
        ..v1::MoveBundleResponse::default()
    };
    let printed = with_unresolved_names(&moved, &["bundle.name"]).unwrap();
    assert_eq!(
        printed,
        serde_json::json!({
            "bundle": {"name": null},
            "path": "other/.ctxloom/content/bundles/v2/demo",
            "applied": true,
        })
    );
    let keys: Vec<&String> = printed.as_object().unwrap().keys().collect();
    assert_eq!(
        keys.first().map(|k| k.as_str()),
        Some("bundle"),
        "{printed}"
    );
}
