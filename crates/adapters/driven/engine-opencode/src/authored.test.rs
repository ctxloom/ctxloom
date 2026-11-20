//! The authored hooks as the plugin reads them.

use std::collections::BTreeMap;

use surface_ports::{Hook, Json};

use super::authored;

fn hook(command: &str, tool: &str) -> Hook {
    Hook {
        command: command.to_string(),
        tool: tool.to_string(),
        timeout: 7,
        ..Hook::default()
    }
}

fn of(pairs: &[(&str, Vec<Hook>)]) -> Json {
    let hooks: BTreeMap<String, Vec<Hook>> = pairs
        .iter()
        .map(|(event, hooks)| (event.to_string(), hooks.clone()))
        .collect();
    Json::parse(&authored(&hooks)).unwrap()
}

/// The command lines of `trigger`, with their tool names.
fn lines(doc: &Json, trigger: &str) -> Vec<String> {
    doc.get(trigger)
        .and_then(Json::as_array)
        .into_iter()
        .flatten()
        .map(|h| {
            let tools: Vec<&str> = h
                .get("tools")
                .and_then(Json::as_array)
                .into_iter()
                .flatten()
                .filter_map(Json::as_str)
                .collect();
            format!(
                "{} [{}]",
                h.get("command").and_then(Json::as_str).unwrap_or_default(),
                tools.join(",")
            )
        })
        .collect()
}

// Each unified event the plugin can hear is its trigger's (the events in
// their names' order, each's hooks in firing order);
// a narrowed one names opencode's tools.
#[test]
fn test_each_authored_hook_is_its_triggers_in_order() {
    let doc = of(&[
        (
            "pre_tool",
            vec![hook("echo PRE", ""), hook("echo SH", "shell")],
        ),
        ("pre_shell", vec![hook("echo PRESHELL", "")]),
        ("post_file_edit", vec![hook("echo EDITED", "")]),
        ("session_start", vec![hook("echo START", "")]),
        ("turn_start", vec![hook("echo ASKED", "")]),
        ("turn_end", vec![hook("echo DONE", "")]),
    ]);
    assert_eq!(
        lines(&doc, "tool.before"),
        ["echo PRESHELL [bash]", "echo PRE []", "echo SH [bash]"]
    );
    assert_eq!(
        lines(&doc, "tool.after"),
        ["echo EDITED [edit,write,patch,multiedit]"]
    );
    assert_eq!(lines(&doc, "session.start"), ["echo START []"]);
    assert_eq!(lines(&doc, "turn.start"), ["echo ASKED []"]);
    assert_eq!(lines(&doc, "turn.end"), ["echo DONE []"]);
    let first = &doc.get("tool.before").unwrap().as_array().unwrap()[0];
    assert_eq!(first.get("timeout"), Some(&Json::Number("7".to_string())));
}

// ctxloom's own callbacks are not authored hooks (the plugin is ctxloom's on
// opencode), and an event the plugin cannot hear has no trigger.
#[test]
fn test_callbacks_and_events_with_no_trigger_are_left_out() {
    let callback = Hook {
        command: "ctxloom".to_string(),
        args: vec!["hook".to_string(), "session-start".to_string()],
        ..Hook::default()
    };
    let doc = of(&[
        ("session_start", vec![callback]),
        ("permission_ask", vec![hook("echo ASK", "")]),
        ("session_end", vec![hook("echo BYE", "")]),
    ]);
    assert_eq!(doc, Json::Object(Vec::new()));
}

// A prompt hook, or one narrowed to a tool class opencode has no tool for,
// is left out: it would never run as written.
#[test]
fn test_a_hook_the_plugin_cannot_run_as_written_is_left_out() {
    let prompt = Hook {
        kind: "prompt".to_string(),
        prompt: "check it".to_string(),
        ..Hook::default()
    };
    let doc = of(&[("pre_tool", vec![prompt, hook("echo X", "nonsense")])]);
    assert_eq!(doc, Json::Object(Vec::new()));
}

// `post_tool` is `tool.after` un-narrowed, and a hook narrowed to `skill`
// runs on opencode's `skill` tool.
#[test]
fn test_post_tool_is_tool_after_and_skill_is_its_tool() {
    let doc = of(&[(
        "post_tool",
        vec![hook("echo POST", ""), hook("echo SK", "skill")],
    )]);
    assert_eq!(
        lines(&doc, "tool.after"),
        ["echo POST []", "echo SK [skill]"]
    );
}

// A hook whose kind says `command` is one; a command hook with no command
// line is left out; a hook with no timeout carries none.
#[test]
fn test_a_command_kind_runs_an_empty_command_does_not_and_no_timeout_is_none() {
    let explicit = Hook {
        kind: "command".to_string(),
        command: "echo KIND".to_string(),
        ..Hook::default()
    };
    let empty = Hook {
        command: String::new(),
        timeout: 3,
        ..Hook::default()
    };
    let doc = of(&[("turn_end", vec![explicit, empty])]);
    assert_eq!(lines(&doc, "turn.end"), ["echo KIND []"]);
    let only = &doc.get("turn.end").unwrap().as_array().unwrap()[0];
    assert_eq!(only.get("timeout"), None);
}
