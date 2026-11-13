use std::collections::BTreeMap;

use super::*;

#[test]
fn test_a_server_s_debug_names_its_variables_and_headers_and_withholds_their_values() {
    let server = SessionMcp {
        name: "srv".into(),
        command: "tool".into(),
        env: BTreeMap::from([("TOKEN".to_string(), "s3cret-env".to_string())]),
        headers: BTreeMap::from([(
            "Authorization".to_string(),
            "Bearer s3cret-header".to_string(),
        )]),
        ..SessionMcp::default()
    };
    let shown = format!("{server:?}");
    assert!(
        shown.contains("TOKEN") && shown.contains("Authorization"),
        "{shown}"
    );
    assert!(!shown.contains("s3cret"), "{shown}");
}

#[test]
fn test_items_are_empty_until_any_kind_has_one() {
    assert!(SessionItems::default().is_empty());
    for items in [
        SessionItems {
            commands: vec![SessionCommand::default()],
            ..SessionItems::default()
        },
        SessionItems {
            mcp: vec![SessionMcp::default()],
            ..SessionItems::default()
        },
        SessionItems {
            hooks: vec![SessionHook::default()],
            ..SessionItems::default()
        },
    ] {
        assert!(!items.is_empty());
    }
}
