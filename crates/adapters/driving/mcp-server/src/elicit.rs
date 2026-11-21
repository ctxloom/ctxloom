//! A preview's needs as MCP elicitations (step 10c U11, needs.proto): the
//! daemon owns every question, and this server, an eliciting client,
//! presents each to its engine as a form elicitation, then sends the
//! engine's answer back with the apply. Translation only:
//!
//! - a `Confirm` is a required boolean, `confirmed`;
//! - a `Choose` is a string among its choices, `choice`;
//! - an `Ask` is a string, `text`, its suggestion the default;
//! - an `Edit` is a string, `text`, the document's text the default, and
//!   answered with the document's new text (API Q12): no editor is started.
//!
//! An `optional` need's field is not required, and a need the engine
//! declines is skipped when optional; a required one declined, or a
//! confirmation not given, declines the write.

use daemon_proto::v1;
use serde_json::{json, Value};

/// The form field a need's answer is given in.
const CONFIRMED: &str = "confirmed";
const CHOICE: &str = "choice";
const TEXT: &str = "text";

/// What the engine's answer to one need comes to.
#[derive(Clone, Debug, PartialEq)]
pub(crate) enum Answered {
    /// The answer the apply carries.
    Answer(v1::Answer),
    /// An optional need left unanswered: the apply takes its suggestion.
    Skipped,
    /// A need the write cannot go without, not answered (or not yes): the
    /// write is declined, and nothing is applied.
    Declined,
}

/// The elicitation's params (form mode) presenting `need`, as MCP sends
/// them; none for a need of no kind this server knows.
pub(crate) fn request_of(need: &v1::Need) -> Option<Value> {
    let (message, field, schema) = match need.kind.as_ref()? {
        v1::need::Kind::Confirm(confirm) => (
            confirm.text.clone(),
            CONFIRMED,
            json!({"type": "boolean", "title": confirm.text}),
        ),
        v1::need::Kind::Choose(choose) => (
            choose.text.clone(),
            CHOICE,
            json!({"type": "string", "enum": choose.choices}),
        ),
        v1::need::Kind::Ask(ask) => (
            ask.text.clone(),
            TEXT,
            json!({"type": "string", "default": ask.suggestion}),
        ),
        v1::need::Kind::Edit(edit) => (
            format!("Edit {}: answer with its new text.", edit.file_name),
            TEXT,
            json!({"type": "string", "default": edit.text}),
        ),
    };
    let required: Vec<&str> = if need.optional { vec![] } else { vec![field] };
    Some(json!({
        "mode": "form",
        "message": message,
        "requestedSchema": {
            "type": "object",
            "properties": {field: schema},
            "required": required,
        },
    }))
}

/// What the engine's elicitation result (`action`, and the `content` it
/// accepted with) answers `need`.
pub(crate) fn answer_of(need: &v1::Need, action: &str, content: Option<&Value>) -> Answered {
    let not_given = || match need.optional {
        true => Answered::Skipped,
        false => Answered::Declined,
    };
    let content = match (action, content) {
        ("accept", Some(content)) => content,
        _ => return not_given(),
    };
    let text = |field: &str| {
        content
            .get(field)
            .and_then(Value::as_str)
            .map(str::to_string)
    };
    let answer = match need.kind.as_ref() {
        Some(v1::need::Kind::Confirm(_)) => match content.get(CONFIRMED).and_then(Value::as_bool) {
            Some(true) => Some(v1::answer::Answer::Confirmed(true)),
            _ => None,
        },
        Some(v1::need::Kind::Choose(choose)) => text(CHOICE)
            .filter(|chosen| choose.choices.contains(chosen))
            .map(v1::answer::Answer::Chosen),
        Some(v1::need::Kind::Ask(_)) => text(TEXT).map(v1::answer::Answer::Text),
        Some(v1::need::Kind::Edit(_)) => text(TEXT).map(v1::answer::Answer::Edited),
        None => None,
    };
    match answer {
        Some(answer) => Answered::Answer(v1::Answer {
            need_id: need.id.clone(),
            answer: Some(answer),
        }),
        None => not_given(),
    }
}

#[cfg(test)]
#[path = "elicit.test.rs"]
mod tests;
