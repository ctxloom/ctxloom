//! The tools (S46) as the engine sees them, and each call translated to the
//! session's client use cases. A tool's arguments are read here; its answer
//! is the daemon's resource as proto3 JSON text.
//!
//! `child_launch` (slice 4 U3, owner ruling s4-Q1) is the session's own
//! child's launch over the daemon's `CreateHandOff`, as `session launch` is
//! the user's. `approval_prompt` (step 8 U4) is a claude engine's
//! permission prompt, served by the server itself (it waits, and hears a
//! cancel: `crate::approval`).

use std::fmt;
use std::sync::Arc;

use coded_diagnostic::{code, Arg, Diagnostic};
use daemon_api::{CallError, Caller, Daemons, Launch};
use daemon_proto::v1;
use rmcp::model::{JsonObject, Tool};
use serde_json::{json, Value};

/// The approval tool's name: `mcp__ctxloom__approval_prompt` to claude.
pub const APPROVAL_PROMPT: &str = "approval_prompt";

/// The test build's tool whose session removal asks to confirm (step 10c
/// U11): callable, never listed.
#[cfg(feature = "test-support")]
pub const TEST_REMOVE_SESSION: &str = "test_remove_session";

/// The test build's tool whose fragment's creation asks for its text, an
/// Edit need (step 10c U11, QUESTIONS.md 7): callable, never listed.
#[cfg(feature = "test-support")]
pub const TEST_CREATE_FRAGMENT: &str = "test_create_fragment";

/// The names of the tools offered, in the order listed.
pub const TOOL_NAMES: [&str; 9] = [
    "session_list",
    "session_show",
    "transcript_show",
    "context_assemble",
    "child_launch",
    "child_send",
    "child_close",
    "child_clear",
    APPROVAL_PROMPT,
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

/// Each tool's description, the owner's approved text (2026-11-14), kept as
/// Markdown beside the crate (`tools/<tool>.md`) and embedded when it is
/// compiled: nothing is read at run time.
const SESSION_LIST: &str = include_str!("../tools/session_list.md");
const SESSION_SHOW: &str = include_str!("../tools/session_show.md");
const TRANSCRIPT_SHOW: &str = include_str!("../tools/transcript_show.md");
const CONTEXT_ASSEMBLE: &str = include_str!("../tools/context_assemble.md");
// The owner's approved text (slice 4 U3), with step 8's sentence on
// `project` (texts.md §7, D14), accepted verbatim (owner, 2026-11-16; reviewed before release).
const CHILD_LAUNCH: &str = include_str!("../tools/child_launch.md");
const CHILD_SEND: &str = include_str!("../tools/child_send.md");
const CHILD_CLOSE: &str = include_str!("../tools/child_close.md");
const CHILD_CLEAR: &str = include_str!("../tools/child_clear.md");
// texts.md §1 (D14), accepted verbatim (owner, 2026-11-16; reviewed before release).
const APPROVAL_PROMPT_TEXT: &str = include_str!("../tools/approval_prompt.md");

/// A description as offered: the file's text without the newline that ends
/// it, so it is the approved text byte for byte.
fn describe(markdown: &'static str) -> &'static str {
    markdown.trim_end_matches(['\n', '\r'])
}

/// The tools offered, each with its input schema.
pub fn tools() -> Vec<Tool> {
    vec![
        Tool::new(
            "session_list",
            describe(SESSION_LIST),
            schema(
                json!({"all": {"type": "boolean", "description": "Every project's sessions, not only this one's"}}),
                &[],
            ),
        ),
        Tool::new(
            "session_show",
            describe(SESSION_SHOW),
            schema(
                json!({"session": session_property("The session")}),
                &["session"],
            ),
        ),
        Tool::new(
            "transcript_show",
            describe(TRANSCRIPT_SHOW),
            schema(
                json!({"session": session_property("The session")}),
                &["session"],
            ),
        ),
        Tool::new(
            "context_assemble",
            describe(CONTEXT_ASSEMBLE),
            schema(
                json!({
                    "bundles": {"type": "array", "items": {"type": "string"}, "description": "The fragment names the premise index lists"},
                    "tags": {"type": "array", "items": {"type": "string"}, "description": "Tags whose fragments to load too"}
                }),
                &["bundles"],
            ),
        ),
        Tool::new(
            "child_launch",
            describe(CHILD_LAUNCH),
            schema(
                json!({
                    "name": {"type": "string", "description": "The child's name, a harp (default: one minted)"},
                    "agent": {"type": "string", "description": "The agent it is planned for (default: the project's)"},
                    "task": {"type": "string", "description": "Its first task (default: none; it waits for its first message)"},
                    "project": {"type": "string", "description": "The directory of the ctxloom project the child works in, relative to your project's root; omit it for your own project."}
                }),
                &[],
            ),
        ),
        Tool::new(
            "child_send",
            describe(CHILD_SEND),
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
            describe(CHILD_CLOSE),
            schema(json!({"child": session_property("The child")}), &["child"]),
        ),
        Tool::new(
            "child_clear",
            describe(CHILD_CLEAR),
            schema(json!({"child": session_property("The child")}), &["child"]),
        ),
        Tool::new(
            APPROVAL_PROMPT,
            describe(APPROVAL_PROMPT_TEXT),
            schema(
                json!({
                    "tool_name": {"type": "string", "description": "The tool your engine asks to run"},
                    "input": {"type": "object", "description": "Its input, as your engine would run it"},
                    "tool_use_id": {"type": "string", "description": "Your engine's id of the call"}
                }),
                &["tool_name", "input", "tool_use_id"],
            ),
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
        coded_diagnostic::display(self, f)
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

/// The engine, asked a question (step 10c U11): the rmcp peer of the call,
/// or a test's stand-in.
pub(crate) trait Elicit: Sync {
    /// Whether the engine said it can be asked (MCP's `elicitation`
    /// capability, form mode).
    fn can(&self) -> bool;

    /// The elicitation `params` (`elicitation/create`'s) asked of the
    /// engine: its `action` and the `content` it answered with; none when
    /// the question could not be put.
    fn ask(
        &self,
        params: Value,
    ) -> impl std::future::Future<Output = Option<(String, Option<Value>)>> + Send;
}

/// `needs`, a preview's, presented to the engine through `elicit`, each as
/// an elicitation: the apply's answers, or none when the write is declined
/// (a required need the engine cannot be asked, or did not answer).
pub(crate) async fn answers<E: Elicit>(elicit: &E, needs: &[v1::Need]) -> Option<Vec<v1::Answer>> {
    let mut answers = Vec::new();
    for need in needs {
        let asked = crate::elicit::request_of(need).filter(|_| elicit.can());
        let answered = match asked {
            Some(params) => match elicit.ask(params).await {
                Some((action, content)) => {
                    crate::elicit::answer_of(need, &action, content.as_ref())
                }
                None => crate::elicit::answer_of(need, "cancel", None),
            },
            None => crate::elicit::answer_of(need, "cancel", None),
        };
        match answered {
            crate::elicit::Answered::Answer(answer) => answers.push(answer),
            crate::elicit::Answered::Skipped => {}
            crate::elicit::Answered::Declined => return None,
        }
    }
    Some(answers)
}

/// What a client that can ask a person can present (needs.proto): it asks,
/// and never puts a program on a terminal.
#[cfg(feature = "test-support")]
fn presentable() -> Option<v1::Presentable> {
    Some(v1::Presentable {
        can_ask: true,
        can_launch: false,
    })
}

/// What a call came to: its text contents, or why it was not answered;
/// a question its daemon call asks is put to the engine through `elicit`.
pub(crate) async fn dispatch<D: Daemons, E: Elicit>(
    caller: &Caller<D>,
    elicit: &E,
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
            let mut assembled = caller.assemble(fragments, tags).await.map_err(failed)?;
            // The index is this edge's to render (E2), on the launch whose
            // session calls the tool (s3-Q5).
            daemon_api::render_premise_index(&mut assembled, daemon_api::Shown::Launch);
            Ok(vec![json_text(&assembled)])
        }
        "child_launch" => {
            let optional = |name: &str| args.get(name).and_then(Value::as_str);
            let launch = Launch {
                owner: None,
                harp: optional("name").map(str::to_string),
                engine: String::new(),
                agent: optional("agent").unwrap_or_default().to_string(),
                task: optional("task").unwrap_or_default().to_string(),
                project: optional("project").map(str::to_string),
            };
            let written = caller.launch(&launch).await.map_err(failed)?;
            Ok(vec![json_text(&written.session)])
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
            // D16 (S40): a close that takes children with it asks first.
            let needs = caller.close_needs(child).await.map_err(failed)?;
            if answers(elicit, &needs).await.is_none() {
                return Ok(vec![format!(
                    "\"{child}\" was not closed: its close takes its children with it, and that was not confirmed"
                )]);
            }
            let written = caller.close(child).await.map_err(failed)?;
            Ok(vec![json_text(&written.session)])
        }
        // The test build's tools (step 10c U11, QUESTIONS.md 7).
        #[cfg(feature = "test-support")]
        TEST_REMOVE_SESSION | TEST_CREATE_FRAGMENT => test_tool(caller, elicit, tool, args).await,
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

/// The test build's tools (step 10c U11, QUESTIONS.md 7): a write whose
/// preview asks, presented to the engine and applied with its answers.
/// Never listed.
#[cfg(feature = "test-support")]
async fn test_tool<D: Daemons, E: Elicit>(
    caller: &Caller<D>,
    elicit: &E,
    tool: &str,
    args: &JsonObject,
) -> Result<Vec<String>, Refused> {
    let refused = |e: ToolArgs| -> Refused { Box::new(e) };
    let failed = |e: CallError| -> Refused { Box::new(e) };
    match tool {
        TEST_REMOVE_SESSION => {
            let harp = text(tool, args, "session").map_err(refused)?;
            let asked = v1::DeleteSessionRequest {
                validate_only: true,
                caller: v1::Caller::of(None, presentable()),
                ..v1::DeleteSessionRequest::default()
            };
            let preview = caller.delete_session(harp, asked).await.map_err(failed)?;
            let Some(answers) = answers(elicit, &preview.needs).await else {
                return Ok(vec![format!("\"{harp}\" was not removed")]);
            };
            let apply = v1::DeleteSessionRequest {
                etag: preview.etag,
                answers,
                caller: v1::Caller::of(None, presentable()),
                ..v1::DeleteSessionRequest::default()
            };
            let done = caller.delete_session(harp, apply).await.map_err(failed)?;
            Ok(vec![json_text(&done)])
        }
        TEST_CREATE_FRAGMENT => {
            let bundle = text(tool, args, "bundle").map_err(refused)?;
            let id = text(tool, args, "fragment").map_err(refused)?;
            let ask = |validate_only: bool, answers: Vec<v1::Answer>| v1::CreateFragmentRequest {
                fragment_id: id.to_string(),
                fragment: Some(v1::Fragment::default()),
                validate_only,
                answers,
                caller: v1::Caller::of(None, presentable()),
                ..v1::CreateFragmentRequest::default()
            };
            let preview = caller
                .create_fragment(bundle, ask(true, Vec::new()))
                .await
                .map_err(failed)?;
            let Some(answers) = answers(elicit, &preview.needs).await else {
                return Ok(vec![format!("\"{id}\" was not created")]);
            };
            let made = caller
                .create_fragment(bundle, ask(false, answers))
                .await
                .map_err(failed)?;
            Ok(vec![json_text(&made)])
        }
        other => Err(refused(ToolArgs::Unknown {
            tool: other.to_string(),
        })),
    }
}

#[cfg(test)]
#[path = "tools.test.rs"]
mod tests;
