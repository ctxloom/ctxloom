//! `approval_prompt` (step 8 U4, S27): the permission prompt a claude child
//! or one-shot is launched with (`--permission-prompt-tool
//! mcp__ctxloom__approval_prompt`, U6). claude calls it with
//! `{tool_name, input, tool_use_id}` (P0, verified on 2.1.322) and waits;
//! the call is the daemon's `CreateApproval` (D10), then its `WaitApproval`
//! under a deadline of its own (the ask's time and a margin, never a read's),
//! and its answer is claude's verified shape, as text:
//!
//! - an allow: `{"behavior":"allow","updatedInput":<input>}`, the input
//!   unchanged, and for `allow-session` and `allow-always` the session rule
//!   too (`updatedPermissions`, destination `session`, verified honoured);
//! - a deny: `{"behavior":"deny","message":<text>}`, the user's message, or
//!   ctxloom's words when she gave none or no answer came in time.
//!
//! A call claude cancels (`notifications/cancelled`, a steer: P0 Q5) is
//! withdrawn at once (`RecordApprovalWithdrawn`, its reason claude's words),
//! and nothing is answered for it. claude's `server/discover`, sent before
//! `initialize` with a string id, is answered `{}` (P0 Q11) by the
//! transport's own reader ([`intercepted`]), as the probe verified.

use std::collections::HashMap;
use std::fmt;
use std::sync::{Arc, Mutex, PoisonError};

use coded_diagnostic::{code, Arg, Diagnostic};
use daemon_proto::v1;
use serde_json::{json, Value};

use crate::Say;

/// How long past an ask's time its wait lasts, so the daemon's own answer
/// (its expiry, U5) comes before the wait's deadline.
pub const WAIT_MARGIN: std::time::Duration = std::time::Duration::from_secs(60);

/// When the wait for `asked` gives up: its recorded expiry and
/// [`WAIT_MARGIN`], one instant however often the call is retried (U5: the
/// expiry is the daemon's alarm, so this is only a backstop). Every ask the
/// daemon answers has an expiry; one without would end its wait at once.
pub(crate) fn wait_until(asked: &v1::Approval) -> daemon_api::Until {
    let mut until = asked.expire_time.unwrap_or_default();
    until.seconds += i64::try_from(WAIT_MARGIN.as_secs()).unwrap_or(i64::MAX);
    until
}

/// The reasons claude gave for the calls it cancelled, by the call's
/// JSON-RPC id (its text): heard on the transport, read by the call that
/// was cancelled.
#[derive(Clone, Default)]
pub(crate) struct Cancels(Arc<Mutex<HashMap<String, String>>>);

impl Cancels {
    pub(crate) fn heard(&self, id: String, reason: String) {
        self.0
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .insert(id, reason);
    }

    /// The reason the call `id` was cancelled with, taken; none heard.
    pub(crate) fn reason(&self, id: &str) -> String {
        self.0
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .remove(id)
            .unwrap_or_default()
    }
}

/// What one line of the engine's input is to the transport.
#[derive(Clone, Debug, PartialEq)]
pub(crate) enum Intercepted {
    /// Answered here, never passed on: this line is the answer.
    Answer(String),
    /// A cancel, passed on, its call's id and reason heard.
    Cancelled { id: String, reason: String },
    /// Passed on as it is.
    Pass,
}

/// What `line` is: claude's `server/discover` request answered `{}`; a
/// `notifications/cancelled` heard; anything else passed on.
pub(crate) fn intercepted(line: &str) -> Intercepted {
    let Ok(message) = serde_json::from_str::<Value>(line) else {
        return Intercepted::Pass;
    };
    match message.get("method").and_then(Value::as_str) {
        Some("server/discover") => match message.get("id") {
            Some(id) => {
                Intercepted::Answer(json!({"jsonrpc": "2.0", "id": id, "result": {}}).to_string())
            }
            None => Intercepted::Pass,
        },
        Some("notifications/cancelled") => {
            let params = message.get("params");
            match params.and_then(|p| p.get("requestId")) {
                Some(id) => Intercepted::Cancelled {
                    id: id.to_string(),
                    reason: params
                        .and_then(|p| p.get("reason"))
                        .and_then(Value::as_str)
                        .unwrap_or_default()
                        .to_string(),
                },
                None => Intercepted::Pass,
            }
        }
        _ => Intercepted::Pass,
    }
}

/// Why an ask was denied, said to the model (texts.md §2, accepted verbatim (owner, 2026-11-16; reviewed before release)).
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Denied {
    /// The ask's time ran out; `timeout` as the config writes a span.
    Expired { timeout: String },
    /// `engines.<engine>.approval_timeout: now` (D16).
    AtOnce { engine: String },
    /// The user denied it and said nothing more.
    ByUser,
}

impl fmt::Display for Denied {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        coded_diagnostic::display(self, f)
    }
}

impl Diagnostic for Denied {
    fn code(&self) -> &str {
        match self {
            Denied::Expired { .. } => code!("approval-expired"),
            Denied::AtOnce { .. } => code!("approval-denied-at-once"),
            Denied::ByUser => code!("approval-denied"),
        }
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        match self {
            Denied::Expired { timeout } => vec![("timeout", timeout.as_str().into())],
            Denied::AtOnce { engine } => vec![("engine", engine.as_str().into())],
            Denied::ByUser => Vec::new(),
        }
    }
}

/// `seconds` as the config's span grammar writes it: `1h`, `5m`, `90s`.
pub(crate) fn span(seconds: u32) -> String {
    match seconds {
        0 => "0s".to_string(),
        s if s % 3600 == 0 => format!("{}h", s / 3600),
        s if s % 60 == 0 => format!("{}m", s / 60),
        s => format!("{s}s"),
    }
}

/// The session rule claude keeps for `rule` (`Bash(rm -rf build/)`): its
/// tool and its content, destination `session` (P0 Q9); none for a rule
/// not of that shape.
pub(crate) fn session_rule(rule: &str) -> Option<Value> {
    let (tool, content) = rule.strip_suffix(')')?.split_once('(')?;
    Some(json!({
        "type": "addRules",
        "rules": [{"toolName": tool, "ruleContent": content}],
        "behavior": "allow",
        "destination": "session",
    }))
}

/// claude's answer for the settled `approval` of the call whose input was
/// `input`, its deny texts said by `say`.
pub(crate) fn answer_of(approval: &v1::Approval, input: &Value, say: &Say) -> Value {
    let deny = |denied: Denied| json!({"behavior": "deny", "message": say(&denied)});
    match (approval.state.as_str(), approval.option_kind.as_str()) {
        ("answered", "allow_once") => json!({"behavior": "allow", "updatedInput": input}),
        ("answered", "allow_session" | "allow_always") => {
            let rule = approval
                .options
                .iter()
                .find(|option| option.id == approval.option)
                .and_then(|option| session_rule(&option.rule));
            let mut allowed = json!({"behavior": "allow", "updatedInput": input});
            if let Some(rule) = rule {
                allowed["updatedPermissions"] = json!([rule]);
            }
            allowed
        }
        ("answered", _) if !approval.message.is_empty() => {
            json!({"behavior": "deny", "message": approval.message})
        }
        ("expired", _) if approval.timeout_seconds == 0 => deny(Denied::AtOnce {
            engine: approval.engine.clone(),
        }),
        ("expired", _) => deny(Denied::Expired {
            timeout: span(approval.timeout_seconds),
        }),
        _ => deny(Denied::ByUser),
    }
}

/// One value of every diagnostic here.
#[cfg(any(test, feature = "test-support"))]
pub(crate) fn samples() -> Vec<Box<dyn Diagnostic>> {
    vec![
        Box::new(Denied::Expired {
            timeout: "5m".to_string(),
        }),
        Box::new(Denied::AtOnce {
            engine: "claude-code".to_string(),
        }),
        Box::new(Denied::ByUser),
    ]
}

#[cfg(test)]
#[path = "approval.test.rs"]
mod tests;
