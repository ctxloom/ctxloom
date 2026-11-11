//! `claude -p --output-format stream-json --verbose`, read: one JSON event
//! per line, the last `result` event the turn's end. Pure.
//!
//! What a turn needs is on two events: `system`/`init` and every later event
//! carry the native `session_id`; `result` carries `is_error`, the answer in
//! `result`, and for an API failure `api_error_status`. Every other line
//! (assistant text, tool use and results, hook and rate-limit notices) is
//! skipped, as is a line that is not JSON.

use run_ports::{FailureClass, TurnOutcome, TurnOutput};
use surface_ports::Json;

/// How much of stderr a failure's detail keeps.
const STDERR_DETAIL: usize = 400;

/// What a finished turn's output meant.
pub(crate) fn read_turn(output: &TurnOutput) -> TurnOutcome {
    let text = String::from_utf8_lossy(&output.stdout);
    let mut native_id: Option<String> = None;
    let mut result: Option<Json> = None;
    for line in text.lines() {
        let Ok(event) = Json::parse(line) else {
            continue;
        };
        if let Some(id) = event.get("session_id").and_then(Json::as_str) {
            native_id = Some(id.to_string());
        }
        if event.get("type").and_then(Json::as_str) == Some("result") {
            result = Some(event);
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
    let code = output.exit.code.unwrap_or(0);
    let Some(result) = result else {
        let detail = if code != 0 {
            let stderr = String::from_utf8_lossy(&output.stderr);
            format!("exit {code}: {}", bounded(stderr.trim()))
        } else {
            "the stream ended with no result".to_string()
        };
        return failed(FailureClass::EngineError, detail);
    };
    let said = result
        .get("result")
        .and_then(Json::as_str)
        .unwrap_or_default()
        .to_string();
    if matches!(result.get("is_error"), Some(Json::Bool(true))) {
        return failed(class_of(&result), said);
    }
    if code != 0 {
        return failed(
            FailureClass::EngineError,
            format!("exit {code} after a result"),
        );
    }
    TurnOutcome::Answered {
        answer: said,
        native_id,
    }
}

/// The class of an error result: by the API status claude reports.
pub(crate) fn class_of(result: &Json) -> FailureClass {
    let status = match result.get("api_error_status") {
        Some(Json::Number(n)) => n.parse::<u16>().ok(),
        _ => None,
    };
    match status {
        Some(429) => FailureClass::RateLimited,
        Some(401 | 403) => FailureClass::CredentialRefused,
        _ => FailureClass::EngineError,
    }
}

/// `text`, cut to [`STDERR_DETAIL`] bytes at a character boundary.
pub(crate) fn bounded(text: &str) -> &str {
    let mut end = text.len().min(STDERR_DETAIL);
    while !text.is_char_boundary(end) {
        end -= 1;
    }
    &text[..end]
}

#[cfg(test)]
#[path = "stream.test.rs"]
mod tests;
