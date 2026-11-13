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
        // A compaction's hooks, by the unified name or the engine's own: no
        // matcher.
        "compaction_before" | "PreCompact" => ("PreCompact", None, false),
        "compaction_after" | "PostCompact" => ("PostCompact", None, false),
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

/// Each hook as a claim on its entry in the group its matcher selects, a
/// ctxloom callback calling `program`.
pub(crate) fn ops(
    hooks: &BTreeMap<String, Vec<Hook>>,
    program: &str,
) -> Result<Vec<JsonOp>, SurfaceError> {
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
                value: value(&hook.installed_as(ENGINE, event, program)),
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
    "PreCompact",
    "PostCompact",
];

/// The unified events a child's home is installed to call ctxloom for (owner
/// ruling 51): the per-turn ones. `SessionStart` and `SessionEnd` fire once
/// per `claude -p` PROCESS, that is every turn, so they say nothing of the
/// session and are not registered.
const CHILD_EVENTS: &[&str] = &[
    "turn_start",
    "message_display",
    "post_tool_batch",
    "turn_end",
    "PreCompact",
    "PostCompact",
];

/// The `settings.json` of a session home: every one of [`HOME_EVENTS`] (the
/// terminal session's) or [`CHILD_EVENTS`] (a child's turn) calls the binary
/// `program`'s `hook <engine> execute <Event>`.
pub(crate) fn home_settings(interactive: bool, program: &str) -> String {
    let callback = Hook {
        command: surface_ports::CALLBACK_PROGRAM.to_string(),
        args: vec!["hook".to_string()],
        ..Hook::default()
    };
    let registered = if interactive {
        HOME_EVENTS
    } else {
        CHILD_EVENTS
    };
    let hooks: BTreeMap<String, Vec<Hook>> = registered
        .iter()
        .map(|event| (event.to_string(), vec![callback.clone()]))
        .collect();
    let mut events: Vec<(String, Json)> = Vec::new();
    for op in ops(&hooks, program).unwrap_or_default() {
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

/// The native events a child's turn never registers (owner ruling 51):
/// `claude -p` fires them once per process, that is every turn.
const PER_PROCESS: &[&str] = &["SessionStart", "SessionEnd"];

/// What a launch's selection carries of hooks (owner ruling 2026-11-12: the
/// authored hooks and ctxloom's own callbacks ride the launch), as claims on
/// the hooks file: each hook its own claim, one claude cannot route (a tool
/// class it has no matcher for) left out; for a child's turn none of the
/// per-process events.
pub(crate) fn launch_ops(
    hooks: &BTreeMap<String, Vec<Hook>>,
    program: &str,
    interactive: bool,
) -> Vec<JsonOp> {
    let mut all: Vec<JsonOp> = Vec::new();
    for (event, list) in hooks {
        for hook in list {
            let one = BTreeMap::from([(event.clone(), vec![hook.clone()])]);
            for op in ops(&one, program).unwrap_or_default() {
                let per_process = matches!(&op,
                    JsonOp::HookEntry { event, .. } if PER_PROCESS.contains(&event.as_str()));
                if (interactive || !per_process) && !all.contains(&op) {
                    all.push(op);
                }
            }
        }
    }
    all
}

/// `op` (a hook entry) laid into the settings document `doc`: the entry in
/// the group of its event whose matcher is its own (made where there is
/// none), once.
pub(crate) fn lay(doc: &mut Json, op: JsonOp) {
    let JsonOp::HookEntry {
        event,
        matcher,
        value,
    } = op
    else {
        return;
    };
    if !matches!(doc.get("hooks"), Some(Json::Object(_))) {
        doc.set("hooks", Json::Object(Vec::new()));
    }
    let Some(hooks) = doc.get_mut("hooks") else {
        return;
    };
    if !matches!(hooks.get(&event), Some(Json::Array(_))) {
        hooks.set(&event, Json::Array(Vec::new()));
    }
    let Some(Json::Array(groups)) = hooks.get_mut(&event) else {
        return;
    };
    let ours = |group: &Json| group.get("matcher").and_then(Json::as_str) == matcher.as_deref();
    if !groups.iter().any(ours) {
        let mut members = Vec::new();
        if let Some(matcher) = &matcher {
            members.push(("matcher", Json::text(matcher.as_str())));
        }
        members.push(("hooks", Json::Array(Vec::new())));
        groups.push(Json::object(members));
    }
    let Some(group) = groups.iter_mut().find(|g| ours(g)) else {
        return;
    };
    if let Some(Json::Array(entries)) = group.get_mut("hooks") {
        if !entries.contains(&value) {
            entries.push(value);
        }
    }
}

/// Whether the args are `hook <this engine> execute <event>`.
fn is_execute_form(args: Option<&Json>) -> bool {
    let args: Vec<&str> = args
        .and_then(Json::as_array)
        .map(|a| a.iter().filter_map(Json::as_str).collect())
        .unwrap_or_default();
    matches!(args.as_slice(), ["hook", engine, "execute", _] if *engine == ENGINE)
}

/// Whether the settings document registers a callback of the binary
/// `program` in any event.
pub(crate) fn has_callback(doc: &Json, program: &str) -> bool {
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
                            hook.get("command").and_then(Json::as_str) == Some(program)
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
