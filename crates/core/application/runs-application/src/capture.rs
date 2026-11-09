//! PROPOSAL (design/hook-capture): the engine's hooks as facts of the
//! session.
//!
//! - [`observe`]: a unified [`HookEvent`] to the typed observations it holds,
//!   engine-neutral (no native payload shape reaches here);
//! - [`Observation::external_id`]: the fact's idempotency key, derived from
//!   the event alone (never a counter, never a clock);
//! - [`Capture`]: the hook-side policy. It delivers each observation through
//!   a [`FactSink`] and never fails: a delivery that did not happen is a
//!   [`Report`] entry, and the hook's answer to its engine is not this
//!   module's to change.
//!
//! Every observation is a RUNNER-authored fact on the SESSION stream (the
//! hook is launched by the runner and holds its credential).

use std::time::Duration;

use angzarr_embedded::markup::DomainMessage;
use angzarr_embedded::v1::{command_page, CommandPage, Cover, Fact, FactOrigin};
use sha2::{Digest, Sha256};

use angzarr_embedded::v1::{CommandBook, Uuid};
use run_ports::{Delivery, FactSink};
use surface_ports::HookEvent;

/// One thing a hook observed, as the fact that records it.
#[derive(Clone, Debug, PartialEq)]
pub enum Observation {
    PromptSubmitted(sessions::RecordPromptSubmitted),
    AssistantText(sessions::RecordAssistantText),
    ToolCall(sessions::RecordToolCall),
    TurnEnded(sessions::RecordTurnEnded),
    SessionEnded(sessions::RecordSessionEnded),
    Compaction(sessions::RecordCompaction),
}

/// The observations `event` holds, in the order they happened. Pure.
///
/// Invariants: every MessageDisplay piece is one observation (none is
/// dropped or merged); a PostToolBatch is one observation per call, in call
/// order, carrying the call's input whole and its result as size and digest
/// only; a turn's end is one observation without text; only PostToolBatch
/// reports tool calls (a single PostToolUse reports none, so a call is never
/// recorded twice); an event of no observable kind holds none.
pub fn observe(event: &HookEvent) -> Vec<Observation> {
    match event.event.as_str() {
        "turn_start" => vec![Observation::PromptSubmitted(
            sessions::RecordPromptSubmitted {
                prompt_id: event.prompt_id.clone(),
                prompt: event.prompt.clone(),
            },
        )],
        "message_display" => event
            .message
            .iter()
            .map(|piece| {
                Observation::AssistantText(sessions::RecordAssistantText {
                    prompt_id: event.prompt_id.clone(),
                    turn_id: piece.turn_id.clone(),
                    message_id: piece.message_id.clone(),
                    index: piece.index,
                    is_final: piece.is_final,
                    text: piece.delta.clone(),
                })
            })
            .collect(),
        "post_tool_batch" => event
            .tool_calls
            .iter()
            .map(|call| {
                Observation::ToolCall(sessions::RecordToolCall {
                    prompt_id: event.prompt_id.clone(),
                    tool_use_id: call.tool_use_id.clone(),
                    tool: call.tool.clone(),
                    input: call.input.clone(),
                    response_bytes: call.response.len() as u64,
                    response_sha256: format!("{:x}", Sha256::digest(call.response.as_bytes())),
                })
            })
            .collect(),
        "turn_end" => vec![Observation::TurnEnded(sessions::RecordTurnEnded {
            prompt_id: event.prompt_id.clone(),
        })],
        "session_end" => vec![Observation::SessionEnded(sessions::RecordSessionEnded {
            native_session: event.native_session.clone(),
            reason: event.reason.clone(),
        })],
        // The engine's compaction events are carried by their native names.
        "PreCompact" => vec![compaction(event, sessions::CompactionPhase::Before)],
        "PostCompact" => vec![compaction(event, sessions::CompactionPhase::After)],
        _ => Vec::new(),
    }
}

fn compaction(event: &HookEvent, phase: sessions::CompactionPhase) -> Observation {
    Observation::Compaction(sessions::RecordCompaction {
        prompt_id: event.prompt_id.clone(),
        phase,
    })
}

impl Observation {
    /// The fact's `origin.external_id` for `session`: deterministic from the
    /// event's own identifiers, and distinct across kinds.
    pub fn external_id(&self, session: &Uuid) -> String {
        let session = uuid::Uuid::from_slice(&session.value)
            .map(|id| id.to_string())
            .unwrap_or_default();
        match self {
            Observation::PromptSubmitted(f) => format!("hook:prompt:{session}:{}", f.prompt_id),
            Observation::AssistantText(f) => {
                format!("hook:text:{session}:{}:{}", f.message_id, f.index)
            }
            Observation::ToolCall(f) => format!("hook:tool:{session}:{}", f.tool_use_id),
            Observation::TurnEnded(f) => format!("hook:turn-end:{session}:{}", f.prompt_id),
            Observation::SessionEnded(f) => {
                format!("hook:session-end:{session}:{}", f.native_session)
            }
            Observation::Compaction(f) => {
                let phase = match f.phase {
                    sessions::CompactionPhase::After => "after",
                    _ => "before",
                };
                format!("hook:compact:{session}:{}:{phase}", f.prompt_id)
            }
        }
    }

    /// The type URL of the fact's wire message.
    pub fn type_url(&self) -> String {
        self.payload().type_url
    }

    fn payload(&self) -> angzarr_embedded::Any {
        fn any<M: DomainMessage>(message: &M) -> angzarr_embedded::Any {
            angzarr_embedded::Any::from_msg(&message.to_wire()).expect("a wire message encodes")
        }
        match self {
            Observation::PromptSubmitted(f) => any(f),
            Observation::AssistantText(f) => any(f),
            Observation::ToolCall(f) => any(f),
            Observation::TurnEnded(f) => any(f),
            Observation::SessionEnded(f) => any(f),
            Observation::Compaction(f) => any(f),
        }
    }

    /// The fact as the `CommandBook` the edge (or a gRPC `Handle`) takes: on
    /// `session`'s stream, a Fact body with its origin.
    pub fn book(&self, session: &Uuid) -> CommandBook {
        CommandBook {
            cover: Some(Cover {
                domain: sessions::SESSION_DOMAIN.to_string(),
                root: Some(session.clone()),
                edition: None,
            }),
            page: Some(CommandPage {
                body: Some(command_page::Body::Fact(Fact {
                    payload: Some(self.payload()),
                    origin: Some(FactOrigin {
                        external_id: self.external_id(session),
                        description: String::new(),
                    }),
                })),
                ..CommandPage::default()
            }),
        }
    }
}

/// How long a hook may spend delivering, by the engine's native hook name:
/// strictly under the engine's own timeout for it, so the engine never kills
/// the hook mid-answer (UserPromptSubmit 30s, MessageDisplay 10s).
pub fn delivery_budget(native: &str) -> Duration {
    match native {
        "UserPromptSubmit" => Duration::from_secs(20),
        // The engine gives a SessionEnd hook little time: take a fraction of it.
        "SessionEnd" => Duration::from_secs(1),
        "MessageDisplay" => Duration::from_secs(5),
        _ => Duration::from_secs(8),
    }
}

/// One delivery tried (or skipped). A delivery that did not happen is dropped,
/// never kept for later: the report is its only trace.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Attempt {
    pub external_id: String,
    /// `None`: not attempted, because an earlier one found the sink
    /// unavailable or out of time (the budget is shared, not multiplied).
    pub delivery: Option<Delivery>,
}

/// What a hook's capture did.
#[derive(Clone, Debug, PartialEq, Eq, Default)]
pub struct Report {
    pub attempts: Vec<Attempt>,
}

impl Report {
    /// The attempts that left a fact unrecorded and unsettled.
    pub fn unrecorded(&self) -> Vec<&Attempt> {
        self.attempts
            .iter()
            .filter(|attempt| attempt.delivery.as_ref().is_none_or(|d| !d.settled()))
            .collect()
    }
}

/// The hook-side policy over a sink.
pub struct Capture<S: FactSink> {
    sink: S,
}

impl<S: FactSink> Capture<S> {
    pub fn new(sink: S) -> Capture<S> {
        Capture { sink }
    }

    /// Deliver `event`'s observations to `session`'s stream, within `budget`
    /// in all. Infallible by type: no panic, no error, no wait past the
    /// budget.
    pub fn record(&self, session: &Uuid, event: &HookEvent, budget: Duration) -> Report {
        let observed = observe(event);
        // The core reads no clock, so the budget is shared by division: each
        // delivery waits for its share, and the whole cannot pass the budget.
        let share = budget / u32::try_from(observed.len().max(1)).unwrap_or(u32::MAX);
        let mut report = Report::default();
        let mut reachable = true;
        for observation in &observed {
            let external_id = observation.external_id(session);
            let delivery = if reachable {
                let delivery = self.sink.deliver(observation.book(session), share);
                reachable = !matches!(delivery, Delivery::Unavailable { .. } | Delivery::TimedOut);
                Some(delivery)
            } else {
                None
            };
            report.attempts.push(Attempt {
                external_id,
                delivery,
            });
        }
        report
    }
}

#[cfg(test)]
#[path = "capture.test.rs"]
mod tests;
