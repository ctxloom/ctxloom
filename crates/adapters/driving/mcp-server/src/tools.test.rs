//! The tools as an engine lists them, and their arguments as read.

use serde_json::json;

use super::{text, texts, tools, ToolArgs, TOOL_NAMES};

fn args(value: serde_json::Value) -> rmcp::model::JsonObject {
    match value {
        serde_json::Value::Object(map) => map,
        _ => panic!("an object"),
    }
}

// S46 less what is not built (OD-1): the names offered, each an object
// schema whose required arguments are its own properties.
#[test]
fn the_tools_offered_are_the_built_ones_each_with_an_object_schema() {
    let offered = tools();
    let names: Vec<&str> = offered.iter().map(|t| t.name.as_ref()).collect();
    assert_eq!(names, TOOL_NAMES);
    assert!(!names.contains(&"approval_prompt"));
    for tool in &offered {
        let schema = tool.input_schema.as_ref();
        assert_eq!(schema["type"], "object", "{}", tool.name);
        assert!(tool.description.is_some(), "{}", tool.name);
        for required in schema["required"].as_array().unwrap() {
            let name = required.as_str().unwrap();
            assert!(
                schema["properties"].get(name).is_some(),
                "{}: {name}",
                tool.name
            );
        }
    }
    let send = offered.iter().find(|t| t.name == "child_send").unwrap();
    assert_eq!(
        send.input_schema["required"],
        json!(["child", "urgency", "text"])
    );
    let assemble = offered
        .iter()
        .find(|t| t.name == "context_assemble")
        .unwrap();
    assert_eq!(assemble.input_schema["required"], json!(["bundles"]));
}

#[test]
fn a_text_argument_is_read_or_said_missing() {
    let given = args(json!({"child": "brisk-copper-moth", "n": 3}));
    assert_eq!(
        text("child_close", &given, "child"),
        Ok("brisk-copper-moth")
    );
    assert_eq!(
        text("child_close", &given, "n"),
        Err(ToolArgs::Missing {
            tool: "child_close".to_string(),
            argument: "n".to_string()
        })
    );
    assert!(text("child_close", &given, "absent").is_err());
}

#[test]
fn a_list_argument_is_its_texts_and_absent_only_where_optional() {
    let given = args(json!({"bundles": ["a", "b"], "bad": ["a", 1], "one": "a"}));
    assert_eq!(
        texts("t", &given, "bundles", true),
        Ok(vec!["a".to_string(), "b".to_string()])
    );
    assert_eq!(texts("t", &given, "tags", false), Ok(Vec::new()));
    assert!(texts("t", &given, "tags", true).is_err());
    assert!(texts("t", &given, "bad", false).is_err());
    assert!(texts("t", &given, "one", false).is_err());
}

#[test]
fn each_argument_error_reports_its_args() {
    use angzarr_diagnostic::Diagnostic;
    let missing = ToolArgs::Missing {
        tool: "child_send".to_string(),
        argument: "text".to_string(),
    };
    assert_eq!(
        missing.to_string(),
        "mcp-server-argument-missing tool=child_send argument=text"
    );
    assert_eq!(
        ToolArgs::Unknown {
            tool: "x".to_string()
        }
        .args()
        .len(),
        1
    );
    assert_eq!(
        ToolArgs::Urgency {
            given: "shout".to_string()
        }
        .to_string(),
        "mcp-server-urgency-invalid given=shout"
    );
}
