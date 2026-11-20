//! `opencode run --format json`, read: one JSON event per line,
//! `{type, timestamp, sessionID, part | error}` (Go's codec, 1.18.x). Pure.
//!
//! Every line names the native `sessionID`. A model step is `step_start`, its
//! finished parts (`text`, `reasoning`, `tool_use`), then `step_finish` with
//! its `reason`; a step whose reason is `tool-calls` is not the turn's end.
//! The reply is the text of the final step. A turn whose last step called a
//! tool that was refused ends there, with no final step: it is answered. An
//! `error` line (`{name, data}`) is the turn's failure, classed by its kind
//! and HTTP status. The words are recorded by the plugin, never from here
//! (owner rulings 26 and 51): this is the reply and the outcome only.

use run_ports::{FailureClass, TurnOutcome, TurnOutput};
use surface_ports::Json;

/// How much of stderr a failure's detail keeps.
const STDERR_DETAIL: usize = 400;

/// The reason of a step that stopped to call tools: the turn goes on.
const TOOL_CALLS: &str = "tool-calls";

/// What a finished turn's output meant.
pub(crate) fn read_turn(output: &TurnOutput) -> TurnOutcome {
    let text = String::from_utf8_lossy(&output.stdout);
    let mut native_id: Option<String> = None;
    let mut step_text: Vec<String> = Vec::new();
    let mut answer: Option<String> = None;
    let mut refused = false;
    let mut error: Option<Json> = None;
    for line in text.lines() {
        let Ok(event) = Json::parse(line) else {
            continue;
        };
        if let Some(id) = event.get("sessionID").and_then(Json::as_str) {
            native_id.get_or_insert_with(|| id.to_string());
        }
        let part = event.get("part");
        match event.get("type").and_then(Json::as_str) {
            Some("step_start") => {
                step_text.clear();
                refused = false;
            }
            Some("text") => {
                if let Some(said) = part.and_then(|p| p.get("text")).and_then(Json::as_str) {
                    step_text.push(said.to_string());
                }
            }
            Some("tool_use") => {
                let status = part
                    .and_then(|p| p.get("state"))
                    .and_then(|s| s.get("status"))
                    .and_then(Json::as_str);
                refused |= status == Some("error");
            }
            Some("step_finish") => {
                let reason = part
                    .and_then(|p| p.get("reason"))
                    .and_then(Json::as_str)
                    .unwrap_or_default();
                if reason != TOOL_CALLS {
                    answer = Some(step_text.join("\n"));
                    error = None;
                } else if refused {
                    // A refused call may end the turn here; a later step
                    // answers instead if there is one.
                    answer.get_or_insert_with(String::new);
                }
            }
            Some("error") => error = event.get("error").cloned(),
            _ => {}
        }
    }
    let failed = |class, detail: String| TurnOutcome::Failed {
        class,
        detail,
        native_id: native_id.clone(),
    };
    if output.timed_out {
        return failed(
            FailureClass::Interrupted,
            "the turn passed its deadline and was killed".to_string(),
        );
    }
    if let Some(signal) = output.exit.signal {
        return failed(
            FailureClass::Interrupted,
            format!("the turn was ended by signal {signal}"),
        );
    }
    if let Some(error) = &error {
        return failed(class_of(error), detail_of(error));
    }
    let code = output.exit.code.unwrap_or(0);
    let Some(answer) = answer else {
        let detail = if code != 0 {
            let stderr = String::from_utf8_lossy(&output.stderr);
            format!("exit {code}: {}", bounded(stderr.trim()))
        } else {
            "the stream ended with no final step".to_string()
        };
        return failed(FailureClass::EngineError, detail);
    };
    if code != 0 {
        return failed(
            FailureClass::EngineError,
            format!("exit {code} after the final step"),
        );
    }
    TurnOutcome::Answered { answer, native_id }
}

/// The class of a session error: by its kind, else by the HTTP status the
/// endpoint answered.
fn class_of(error: &Json) -> FailureClass {
    if error.get("name").and_then(Json::as_str) == Some("ProviderAuthError") {
        return FailureClass::CredentialRefused;
    }
    let status = match error.get("data").and_then(|d| d.get("statusCode")) {
        Some(Json::Number(n)) => n.parse::<u16>().ok(),
        _ => None,
    };
    match status {
        Some(429) => FailureClass::RateLimited,
        Some(401 | 403) => FailureClass::CredentialRefused,
        _ => FailureClass::EngineError,
    }
}

/// `<name>: <message>`, as opencode names a session error.
fn detail_of(error: &Json) -> String {
    let name = error.get("name").and_then(Json::as_str).unwrap_or("error");
    let message = error
        .get("data")
        .and_then(|d| d.get("message"))
        .and_then(Json::as_str)
        .unwrap_or_default();
    format!("{name}: {}", bounded(message))
}

/// `text`, cut to [`STDERR_DETAIL`] bytes at a character boundary.
fn bounded(text: &str) -> &str {
    let mut end = text.len().min(STDERR_DETAIL);
    while !text.is_char_boundary(end) {
        end -= 1;
    }
    &text[..end]
}

#[cfg(test)]
#[path = "stream.test.rs"]
mod tests;
