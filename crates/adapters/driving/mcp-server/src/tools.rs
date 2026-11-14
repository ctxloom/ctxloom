//! The tools (S46) as the engine sees them, and each call translated to the
//! session's client use cases. A tool's arguments are read here; its answer
//! is the daemon's resource as proto3 JSON text.
//!
//! `child_launch` is not offered until it is built (slice 3 step 8, the
//! root session's places), nor `approval_prompt` until approvals are
//! (OD-1): a tool that always refuses would be a lie.

use std::fmt;
use std::sync::Arc;

use angzarr_diagnostic::{code, Arg, Diagnostic};
use daemon_api::{CallError, Caller, Daemons};
use rmcp::model::{JsonObject, Tool};
use serde_json::{json, Value};

/// The names of the tools offered, in the order listed.
pub const TOOL_NAMES: [&str; 7] = [
    "session_list",
    "session_show",
    "transcript_show",
    "context_assemble",
    "child_send",
    "child_close",
    "child_clear",
];

/// A JSON Schema object of `properties`, `required` named.
fn schema(properties: Value, required: &[&str]) -> Arc<JsonObject> {
    let doc = json!({
        "type": "object",
        "properties": properties,
        "required": required,
        "additionalProperties": false,
    });
    match doc {
        Value::Object(map) => Arc::new(map),
        _ => unreachable!("an object was written"),
    }
}

/// A session argument: a harp of the caller's project, or a full name.
fn session_property(what: &str) -> Value {
    json!({
        "type": "string",
        "description": format!("{what}: a session's harp in this project, or its full name projects/<project>/sessions/<session>"),
    })
}

/// The tools offered, each with its input schema (DRAFT descriptions: their
/// wording is the owner's to settle).
pub fn tools() -> Vec<Tool> {
    vec![
        Tool::new(
            "session_list",
            "List this project's sessions (all: every project's), as the daemon's session index has them.",
            schema(
                json!({"all": {"type": "boolean", "description": "Every project's sessions, not only this one's"}}),
                &[],
            ),
        ),
        Tool::new(
            "session_show",
            "Show one session as the daemon has it: its state, owner, children and pending messages.",
            schema(json!({"session": session_property("The session")}), &["session"]),
        ),
        Tool::new(
            "transcript_show",
            "Show a session's transcript, its lines as its hooks have delivered them so far.",
            schema(json!({"session": session_property("The session")}), &["session"]),
        ),
        Tool::new(
            "context_assemble",
            "Load the fragments you chose from this project's premise index, by the names it lists.",
            schema(
                json!({
                    "bundles": {"type": "array", "items": {"type": "string"}, "description": "The fragment names the premise index lists"},
                    "tags": {"type": "array", "items": {"type": "string"}, "description": "Tags whose fragments to load too"}
                }),
                &["bundles"],
            ),
        ),
        Tool::new(
            "child_send",
            "Send one of your children a message: inform (read at its next turn), wake (start a turn) or steer (interrupt its turn).",
            schema(
                json!({
                    "child": session_property("The child"),
                    "urgency": {"type": "string", "enum": ["inform", "wake", "steer"]},
                    "text": {"type": "string", "description": "The message"}
                }),
                &["child", "urgency", "text"],
            ),
        ),
        Tool::new(
            "child_close",
            "Close one of your children: its engine is ended, and it takes no more messages.",
            schema(json!({"child": session_property("The child")}), &["child"]),
        ),
        Tool::new(
            "child_clear",
            "Clear one of your children's conversation, as its engine's own /clear does.",
            schema(json!({"child": session_property("The child")}), &["child"]),
        ),
    ]
}

/// Why a tool's arguments were not taken.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum ToolArgs {
    /// A required argument is absent, or not of its type.
    Missing { tool: String, argument: String },
    /// No tool of that name is offered.
    Unknown { tool: String },
    /// An urgency that is none of the three words.
    Urgency { given: String },
}

impl fmt::Display for ToolArgs {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        angzarr_diagnostic::display(self, f)
    }
}

impl std::error::Error for ToolArgs {}

impl Diagnostic for ToolArgs {
    fn code(&self) -> &str {
        match self {
            ToolArgs::Missing { .. } => code!("argument-missing"),
            ToolArgs::Unknown { .. } => code!("unknown-tool"),
            ToolArgs::Urgency { .. } => code!("urgency-invalid"),
        }
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        match self {
            ToolArgs::Missing { tool, argument } => vec![
                ("tool", tool.as_str().into()),
                ("argument", argument.as_str().into()),
            ],
            ToolArgs::Unknown { tool } => vec![("tool", tool.as_str().into())],
            ToolArgs::Urgency { given } => vec![("given", given.as_str().into())],
        }
    }
}

/// Why a call came to nothing, as its tool says it.
pub(crate) type Refused = Box<dyn Diagnostic + Send + Sync>;

/// The text argument `name` of `tool`.
pub(crate) fn text<'a>(tool: &str, args: &'a JsonObject, name: &str) -> Result<&'a str, ToolArgs> {
    args.get(name)
        .and_then(Value::as_str)
        .ok_or_else(|| ToolArgs::Missing {
            tool: tool.to_string(),
            argument: name.to_string(),
        })
}

/// The list-of-text argument `name` (absent: empty unless `required`).
pub(crate) fn texts(
    tool: &str,
    args: &JsonObject,
    name: &str,
    required: bool,
) -> Result<Vec<String>, ToolArgs> {
    let missing = || ToolArgs::Missing {
        tool: tool.to_string(),
        argument: name.to_string(),
    };
    match args.get(name) {
        None if !required => Ok(Vec::new()),
        None => Err(missing()),
        Some(Value::Array(items)) => items
            .iter()
            .map(|item| item.as_str().map(str::to_string).ok_or_else(missing))
            .collect(),
        Some(_) => Err(missing()),
    }
}

/// A message's JSON text (a resource's proto3 JSON, ruling 146).
fn json_text<T: serde::Serialize>(message: &T) -> String {
    serde_json::to_string(message).unwrap_or_default()
}

/// What a call came to: its text contents, or why it was not answered.
pub(crate) async fn dispatch<D: Daemons>(
    caller: &Caller<D>,
    tool: &str,
    args: &JsonObject,
) -> Result<Vec<String>, Refused> {
    let refused = |e: ToolArgs| -> Refused { Box::new(e) };
    let failed = |e: CallError| -> Refused { Box::new(e) };
    match tool {
        "session_list" => {
            let all = args.get("all").and_then(Value::as_bool).unwrap_or(false);
            let listed = caller.list_sessions(all).await.map_err(failed)?;
            Ok(vec![json_text(&listed)])
        }
        "session_show" => {
            let session = text(tool, args, "session").map_err(refused)?;
            let shown = caller.get_session(session).await.map_err(failed)?;
            Ok(vec![json_text(&shown)])
        }
        "transcript_show" => {
            let session = text(tool, args, "session").map_err(refused)?;
            let transcript = caller.get_transcript(session).await.map_err(failed)?;
            Ok(vec![json_text(&transcript)])
        }
        "context_assemble" => {
            let fragments = texts(tool, args, "bundles", true).map_err(refused)?;
            let tags = texts(tool, args, "tags", false).map_err(refused)?;
            let assembled = caller.assemble(fragments, tags).await.map_err(failed)?;
            Ok(vec![json_text(&assembled)])
        }
        "child_send" => {
            let child = text(tool, args, "child").map_err(refused)?;
            let word = text(tool, args, "urgency").map_err(refused)?;
            let urgency = daemon_api::urgency_of(word).ok_or_else(|| {
                refused(ToolArgs::Urgency {
                    given: word.to_string(),
                })
            })?;
            let body = text(tool, args, "text").map_err(refused)?;
            let written = caller.send(child, urgency, body).await.map_err(failed)?;
            Ok(vec![
                json_text(&written.session),
                json!({"message_id": written.id}).to_string(),
            ])
        }
        "child_close" => {
            let child = text(tool, args, "child").map_err(refused)?;
            let written = caller.close(child).await.map_err(failed)?;
            Ok(vec![json_text(&written.session)])
        }
        "child_clear" => {
            let child = text(tool, args, "child").map_err(refused)?;
            let written = caller.clear(child).await.map_err(failed)?;
            Ok(vec![
                json_text(&written.session),
                json!({"clear_id": written.id}).to_string(),
            ])
        }
        other => Err(refused(ToolArgs::Unknown {
            tool: other.to_string(),
        })),
    }
}

#[cfg(test)]
#[path = "tools.test.rs"]
mod tests;
