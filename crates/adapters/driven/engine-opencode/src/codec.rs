//! The plugin's wire in the port's terms: what `plugins/ctxloom.js` writes to
//! a hook's stdin, decoded (owner ruling 58, as the live probe corrected it).
//!
//! The payload is one of opencode's bus events as the plugin received it, the
//! v2 shape `{id, type, properties}` (a trigger hook's arguments are carried
//! the same way, `type` its name), plus a `ctxloom` member holding what the
//! plugin alone knows from the events before it: the prompt a part's step
//! message answers (`prompt_id`), the trigger and the summary text of a
//! compaction. Everything else is derived here, from the event itself:
//!
//! - `chat.message`: the prompt, its id `output.message.id`, its text the
//!   text parts that are neither synthetic nor ignored, by lines;
//! - `message.part.updated`: a text part once it has ended (`time.end`), whole,
//!   its part id the message (no per-delta capture); a tool part that ended
//!   (`completed` or `error`), by its part id, with its status; a compaction
//!   part, the compaction's before (`auto` the trigger); a reasoning part is
//!   nothing (ruling 10: no thinking);
//! - `message.updated`: an assistant message once completed: the compaction's
//!   after when it is the summary (`summary: true`), else the end of its
//!   parent's turn when it finished other than to call tools, or in error;
//! - `session.idle`: the end of the turn the plugin last saw prompted (a turn
//!   whose tools were all refused ends with `tool-calls` and only idles).
//!
//! An event of none of these kinds holds nothing.

use surface_ports::{
    HookCodec, HookEvent, HookResponse, Json, MessageDisplay, SurfaceError, ToolCall, ToolStatus,
};

use crate::NAME;

/// opencode's hook wire, as the plugin forwards it.
#[derive(Default)]
pub struct Codec;

/// The unified event a native name is mapped to before its payload is read;
/// the payload then says which it is (a part may be a text, a tool call or a
/// compaction).
fn unified(native: &str) -> String {
    match native {
        "chat.message" => "turn_start",
        "message.part.updated" => "message_display",
        "message.updated" | "session.idle" => "turn_end",
        other => other,
    }
    .to_string()
}

fn text(doc: Option<&Json>, key: &str) -> String {
    doc.and_then(|d| d.get(key))
        .and_then(Json::as_str)
        .unwrap_or_default()
        .to_string()
}

fn flag(doc: &Json, key: &str) -> bool {
    matches!(doc.get(key), Some(Json::Bool(true)))
}

/// Whether `doc.time.<key>` holds a time.
fn timed(doc: &Json, key: &str) -> bool {
    matches!(
        doc.get("time").and_then(|t| t.get(key)),
        Some(Json::Number(_))
    )
}

/// A text part the conversation shows: neither ctxloom's own (synthetic) nor
/// set aside.
fn shown_text(part: &Json) -> Option<&str> {
    if part.get("type").and_then(Json::as_str) != Some("text")
        || flag(part, "synthetic")
        || flag(part, "ignored")
    {
        return None;
    }
    part.get("text").and_then(Json::as_str)
}

/// The event as its payload says, onto `base` (the session and the prompt).
fn read(kind: &str, props: Option<&Json>, ctx: Option<&Json>, base: HookEvent) -> HookEvent {
    let nothing = HookEvent {
        event: kind.to_string(),
        ..base.clone()
    };
    match kind {
        "chat.message" => {
            let prompt: Vec<&str> = props
                .and_then(|p| p.get("parts"))
                .and_then(Json::as_array)
                .map(|parts| parts.iter().filter_map(shown_text).collect())
                .unwrap_or_default();
            let prompt = prompt.join("\n");
            if prompt.trim().is_empty() {
                return nothing;
            }
            HookEvent {
                event: "turn_start".to_string(),
                prompt_id: text(props.and_then(|p| p.get("message")), "id"),
                prompt,
                ..base
            }
        }
        "message.part.updated" => match props.and_then(|p| p.get("part")) {
            Some(part) => read_part(part, base).unwrap_or(nothing),
            None => nothing,
        },
        "message.updated" => match props.and_then(|p| p.get("info")) {
            Some(info) => read_message(info, ctx, base).unwrap_or(nothing),
            None => nothing,
        },
        "session.idle" if !base.prompt_id.is_empty() => HookEvent {
            event: "turn_end".to_string(),
            ..base
        },
        _ => nothing,
    }
}

/// A part of a message: a finished text, an ended tool call, a compaction.
fn read_part(part: &Json, base: HookEvent) -> Option<HookEvent> {
    match part.get("type").and_then(Json::as_str)? {
        "text" => {
            let said = shown_text(part)?;
            if !timed(part, "end") || said.is_empty() {
                return None;
            }
            Some(HookEvent {
                event: "message_display".to_string(),
                message: Some(MessageDisplay {
                    turn_id: text(Some(part), "messageID"),
                    message_id: text(Some(part), "id"),
                    index: 0,
                    is_final: true,
                    delta: said.to_string(),
                }),
                ..base
            })
        }
        "tool" => {
            let state = part.get("state");
            let (status, response) = match text(state, "status").as_str() {
                "completed" => (ToolStatus::Completed, text(state, "output")),
                "error" => (ToolStatus::Error, text(state, "error")),
                _ => return None,
            };
            let tool = text(Some(part), "tool");
            Some(HookEvent {
                event: "post_tool_batch".to_string(),
                tool: tool.clone(),
                tool_calls: vec![ToolCall {
                    tool,
                    tool_use_id: text(Some(part), "id"),
                    input: state
                        .and_then(|s| s.get("input"))
                        .map(compact)
                        .unwrap_or_default(),
                    response,
                    status,
                }],
                ..base
            })
        }
        "compaction" => Some(HookEvent {
            event: "compaction_before".to_string(),
            // The compaction's own message: each compaction has one.
            prompt_id: text(Some(part), "messageID"),
            trigger: match part.get("auto") {
                Some(Json::Bool(true)) => "auto",
                Some(Json::Bool(false)) => "manual",
                _ => "",
            }
            .to_string(),
            ..base
        }),
        _ => None,
    }
}

/// A message that completed: an assistant's turn's end, or a compaction's
/// summary.
fn read_message(info: &Json, ctx: Option<&Json>, base: HookEvent) -> Option<HookEvent> {
    if info.get("role").and_then(Json::as_str) != Some("assistant") || !timed(info, "completed") {
        return None;
    }
    let parent = text(Some(info), "parentID");
    if flag(info, "summary") {
        return Some(HookEvent {
            event: "compaction_after".to_string(),
            prompt_id: parent,
            trigger: text(ctx, "trigger"),
            compact_summary: text(ctx, "summary"),
            ..base
        });
    }
    let finish = text(Some(info), "finish");
    let finished = !finish.is_empty() && finish != "tool-calls";
    if !(finished || info.get("error").is_some()) {
        return None;
    }
    Some(HookEvent {
        event: "turn_end".to_string(),
        prompt_id: parent,
        ..base
    })
}

/// The session an event is of, wherever its kind keeps it.
fn session_of(props: Option<&Json>) -> String {
    let direct = text(props, "sessionID");
    if !direct.is_empty() {
        return direct;
    }
    ["info", "part", "message"]
        .iter()
        .map(|key| text(props.and_then(|p| p.get(key)), "sessionID"))
        .find(|id| !id.is_empty())
        .unwrap_or_default()
}

impl HookCodec for Codec {
    fn unified_event(&self, native: &str) -> String {
        unified(native)
    }

    fn decode(&self, _event: &str, payload: &str) -> Result<HookEvent, SurfaceError> {
        let doc = Json::parse(payload).map_err(|detail| SurfaceError::Payload {
            engine: NAME.to_string(),
            detail,
        })?;
        let props = doc.get("properties");
        let ctx = doc.get("ctxloom");
        let base = HookEvent {
            native_session: session_of(props),
            prompt_id: text(ctx, "prompt_id"),
            ..HookEvent::default()
        };
        let kind = doc.get("type").and_then(Json::as_str).unwrap_or_default();
        Ok(read(kind, props, ctx, base))
    }

    /// The plugin reads no answer: an empty object, and nothing it would
    /// have to carry.
    fn encode(&self, event: &str, response: &HookResponse) -> Result<String, SurfaceError> {
        if *response != HookResponse::default() {
            return Err(SurfaceError::Unsupported {
                engine: NAME.to_string(),
                what: format!("its plugin reads no hook answer, so {event} can carry none"),
            });
        }
        Ok("{}\n".to_string())
    }

    /// opencode keeps no transcript ctxloom reads.
    // `vec![]` is `Vec::new()`: the mutant is the body itself.
    #[cfg_attr(test, mutants::skip)]
    fn invoked_skills(&self, _transcript: &str) -> Vec<String> {
        Vec::new()
    }
}

/// The document on one line.
fn compact(doc: &Json) -> String {
    match doc {
        Json::Array(items) => {
            let parts: Vec<String> = items.iter().map(compact).collect();
            format!("[{}]", parts.join(","))
        }
        Json::Object(members) => {
            let parts: Vec<String> = members
                .iter()
                .map(|(k, v)| format!("{}:{}", Json::text(k.as_str()).pretty(), compact(v)))
                .collect();
            format!("{{{}}}", parts.join(","))
        }
        other => other.pretty(),
    }
}

#[cfg(test)]
#[path = "codec.test.rs"]
mod tests;
