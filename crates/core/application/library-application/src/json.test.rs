//! The configuration's values as JSON and in YAML's flow form.

use angzarr_yaml::parse;
use serde_json::json;

use super::{flow, permissions, scalar};

fn doc(text: &str) -> angzarr_yaml::Value {
    parse(text).expect("a document")
}

// mutants: a scalar reads as the type it looks like (Go's yaml decode:
// booleans, integers, else text).
#[test]
fn test_a_scalar_reads_as_what_it_looks_like() {
    assert_eq!(scalar("true"), json!(true));
    assert_eq!(scalar("false"), json!(false));
    assert_eq!(scalar("12"), json!(12));
    assert_eq!(scalar("-3"), json!(-3));
    assert_eq!(scalar("abc"), json!("abc"));
    assert_eq!(scalar("True"), json!("True"));
    assert_eq!(scalar("1.5"), json!("1.5"));
}

// mutants: a permissions block is an object of typed values, nesting kept;
// anything else is `{}`.
#[test]
fn test_a_permissions_block_is_an_object_and_anything_else_is_empty() {
    let block = doc(
        "approver: human\nlimit: 3\nsafe: true\nlist: [a, b]\nnothing:\nclaude-code:\n  mode: plan\n",
    );
    assert_eq!(
        permissions(&block),
        json!({
            "approver": "human",
            "limit": 3,
            "safe": true,
            "list": ["a", "b"],
            "nothing": null,
            "claude-code": {"mode": "plan"},
        })
    );
    assert_eq!(permissions(&doc("[a, b]\n")), json!({}));
    assert_eq!(permissions(&doc("plain\n")), json!({}));
    assert_eq!(permissions(&angzarr_yaml::Value::Null), json!({}));
}

// Ruling 67: a value on one line in YAML's flow form, keys sorted as the
// config writer writes them: what is shown is what is written.
#[test]
fn test_a_value_in_flow_form_sorts_its_keys_as_written() {
    assert_eq!(
        flow(&doc(
            "model: haiku\neffort: low\npermissions:\n  mode: plan\n"
        )),
        "{effort: low, model: haiku, permissions: {mode: plan}}"
    );
    assert_eq!(flow(&doc("[a, b]\n")), "[a, b]");
    assert_eq!(flow(&doc("plain\n")), "plain");
    assert_eq!(flow(&doc("{}\n")), "{}");
    assert_eq!(flow(&doc("a:\n")), "{a: null}");
    assert_eq!(flow(&angzarr_yaml::Value::Null), "null");
}
