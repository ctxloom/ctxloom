//! Hooks in claude's `settings.json`: `hooks.<NativeEvent>[{matcher, hooks:
//! [...]}]`.

use std::collections::BTreeMap;

use surface_ports::{Hook, Json, JsonOp, SurfaceError};

/// The engine whose callbacks these are: written into every ctxloom
/// callback so it decodes the payload with this engine's codec.
const ENGINE: &str = crate::NAME;

/// Claude's native event for each unified one, and the matcher an event
/// narrowed by its name carries.
fn route(unified: &str) -> Option<(&'static str, Option<&'static str>, bool)> {
    Some(match unified {
        "pre_tool" => ("PreToolUse", None, true),
        "post_tool" => ("PostToolUse", None, true),
        "session_start" => ("SessionStart", None, true),
        "session_end" => ("SessionEnd", None, true),
        // Stop and UserPromptSubmit take no matcher: neither has a tool.
        "turn_end" => ("Stop", None, false),
        "turn_start" => ("UserPromptSubmit", None, false),
        "pre_shell" => ("PreToolUse", Some(SHELL), true),
        "post_file_edit" => ("PostToolUse", Some(FILE_EDIT), true),
        "permission_ask" => ("PermissionRequest", None, true),
        // Neither carries a tool matcher: the whole batch, the whole message.
        "post_tool_batch" => ("PostToolBatch", None, false),
        "message_display" => ("MessageDisplay", None, false),
        _ => return None,
    })
}

const SHELL: &str = "Bash|PowerShell";
const FILE_EDIT: &str = "Edit|Write|MultiEdit|NotebookEdit";
const SKILL: &str = "Skill";

/// Claude's matcher for a tool class.
fn matcher_of(class: &str) -> Option<&'static str> {
    match class {
        "shell" => Some(SHELL),
        "file_edit" => Some(FILE_EDIT),
        "skill" => Some(SKILL),
        _ => None,
    }
}

/// The hook as `settings.json` spells it; the type defaults to `command`.
fn value(hook: &Hook) -> Json {
    let mut members = hook.json_members();
    if hook.is_async {
        members.push(("async", Json::Bool(true)));
    }
    Json::object(members)
}

/// Each hook as a claim on its entry in the group its matcher selects.
pub(crate) fn ops(hooks: &BTreeMap<String, Vec<Hook>>) -> Result<Vec<JsonOp>, SurfaceError> {
    let mut ops: Vec<JsonOp> = Vec::new();
    for (unified, list) in hooks {
        let Some((event, default, takes_matcher)) = route(unified) else {
            continue;
        };
        for hook in list {
            let matcher = if !hook.matcher.is_empty() {
                Some(hook.matcher.clone())
            } else if !hook.tool.is_empty() {
                Some(
                    matcher_of(&hook.tool)
                        .ok_or_else(|| SurfaceError::Unsupported {
                            engine: ENGINE.to_string(),
                            what: format!(
                                "it has no {:?} tool, so the {unified} hook {:?} cannot be narrowed to it",
                                hook.tool,
                                if hook.command.is_empty() { &hook.prompt } else { &hook.command }
                            ),
                        })?
                        .to_string(),
                )
            } else {
                default.map(str::to_string)
            };
            let op = JsonOp::HookEntry {
                event: event.to_string(),
                matcher: matcher.filter(|_| takes_matcher),
                value: value(&hook.installed_as(ENGINE, event)),
            };
            // A hook routed twice is claimed once.
            if !ops.contains(&op) {
                ops.push(op);
            }
        }
    }
    Ok(ops)
}

/// The unified events the terminal session's home is installed to call
/// ctxloom for: the primary agent's data comes from them.
const HOME_EVENTS: &[&str] = &[
    "session_start",
    "turn_start",
    "message_display",
    "post_tool_batch",
    "turn_end",
    "session_end",
];

/// The `settings.json` of a terminal session's home: every one of
/// [`HOME_EVENTS`] calls ctxloom's `hook <engine> execute <Event>`.
pub(crate) fn home_settings() -> String {
    let callback = Hook {
        command: "ctxloom".to_string(),
        args: vec!["hook".to_string()],
        ..Hook::default()
    };
    let hooks: BTreeMap<String, Vec<Hook>> = HOME_EVENTS
        .iter()
        .map(|event| (event.to_string(), vec![callback.clone()]))
        .collect();
    let mut events: Vec<(String, Json)> = Vec::new();
    for op in ops(&hooks).unwrap_or_default() {
        if let JsonOp::HookEntry { event, value, .. } = op {
            events.push((
                event,
                Json::Array(vec![Json::object([("hooks", Json::Array(vec![value]))])]),
            ));
        }
    }
    let doc = Json::object([("hooks", Json::object(events))]);
    format!("{}\n", doc.pretty())
}

/// Whether the args are `hook <this engine> execute <event>`.
fn is_execute_form(args: Option<&Json>) -> bool {
    let args: Vec<&str> = args
        .and_then(Json::as_array)
        .map(|a| a.iter().filter_map(Json::as_str).collect())
        .unwrap_or_default();
    matches!(args.as_slice(), ["hook", engine, "execute", _] if *engine == ENGINE)
}

/// Whether the settings document registers a ctxloom callback in any event.
pub(crate) fn has_callback(doc: &Json) -> bool {
    let Some(Json::Object(events)) = doc.get("hooks") else {
        return false;
    };
    events.iter().any(|(_, groups)| {
        groups.as_array().is_some_and(|groups| {
            groups.iter().any(|group| {
                group
                    .get("hooks")
                    .and_then(Json::as_array)
                    .is_some_and(|hooks| {
                        hooks.iter().any(|hook| {
                            hook.get("command").and_then(Json::as_str) == Some("ctxloom")
                                && is_execute_form(hook.get("args"))
                        })
                    })
            })
        })
    })
}

#[cfg(test)]
#[path = "hooks.test.rs"]
mod tests;
