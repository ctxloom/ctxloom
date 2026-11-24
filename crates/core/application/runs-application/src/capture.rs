//! The engine's hooks as facts of the
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

use std::collections::BTreeMap;
use std::time::Duration;

use angzarr_embedded::markup::DomainMessage;
use angzarr_embedded::v1::{command_page, CommandPage, Cover, Fact, FactOrigin};
use sha2::{Digest, Sha256};

use angzarr_embedded::v1::{CommandBook, Uuid};
use run_ports::{Delivery, FactSink};
use surface_ports::{HookEvent, ToolStatus};

/// One thing a hook observed, as the fact that records it.
#[derive(Clone, Debug, PartialEq)]
pub enum Observation {
    PromptSubmitted(sessions::RecordPromptSubmitted),
    AssistantText(sessions::RecordAssistantText),
    ToolCall(sessions::RecordToolCall),
    TurnEnded(sessions::RecordTurnEnded),
    Compaction(sessions::RecordCompaction),
    /// The engine started a new conversation in the session (claude's
    /// `/clear`: SessionStart `source: clear`).
    Cleared(sessions::RecordConversationCleared),
    /// The engine's own conversation, as SessionStart names it: what a
    /// resume resumes (owner 2026-11-15, H-Q9: delivered and held like every
    /// capture fact, never written in process).
    Bound(sessions::BindEngineSession),
    /// A fact a hook dropped (its hold could not keep it), recorded in its
    /// place (s4-Q14, H3). [`observe`] never makes one: a flush makes it
    /// from the hold's gap record ([`Observation::gap`]).
    CaptureGap(sessions::RecordCaptureGap),
}

impl Observation {
    /// The gap fact of a hold's gap record: which fact was dropped, why,
    /// when (the dropping hook's clock; none read as the epoch) and by
    /// which hook.
    pub fn gap(gap: &run_ports::HeldGap) -> Observation {
        let dropped_at = gap
            .held_at
            .map_or_else(angzarr_embedded::Timestamp::default, |at| {
                angzarr_embedded::Timestamp {
                    seconds: at.seconds,
                    nanos: at.nanos,
                }
            });
        Observation::CaptureGap(sessions::RecordCaptureGap {
            dropped: gap.external_id.clone(),
            why: gap.why.clone(),
            dropped_at,
            stamp: gap.stamp.clone(),
        })
    }
}

/// The observations `event` holds, in the order they happened. Pure.
///
/// Invariants: every MessageDisplay piece is one observation (none is
/// dropped or merged); a PostToolBatch is one observation per call, in call
/// order, carrying the call's input whole and its result as size and digest
/// only; a turn's end is one observation without text; only PostToolBatch
/// reports tool calls (a single PostToolUse reports none, so a call is never
/// recorded twice); a SessionStart is its conversation's bind, after the
/// clear when its source is `clear`; a session's end observes nothing (owner
/// 2026-11-15: its end is its run's); an event of no observable kind holds
/// none.
pub fn observe(event: &HookEvent) -> Vec<Observation> {
    match event.event.as_str() {
        "turn_start" => vec![Observation::PromptSubmitted(
            sessions::RecordPromptSubmitted {
                prompt_id: event.prompt_id.clone(),
                prompt: event.prompt.clone(),
                extra: event.extra.clone(),
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
                    extra: laid_over(&event.extra, &piece.extra),
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
                    input_bytes: None,
                    input_sha256: None,
                    response_bytes: call.response.len() as u64,
                    response_sha256: format!("{:x}", Sha256::digest(call.response.as_bytes())),
                    status: tool_status(call.status),
                    extra: laid_over(&event.extra, &call.extra),
                })
            })
            .collect(),
        "turn_end" => vec![Observation::TurnEnded(sessions::RecordTurnEnded {
            prompt_id: event.prompt_id.clone(),
            extra: event.extra.clone(),
        })],
        "session_start" => session_start(event),
        "compaction_before" => vec![compaction(event, sessions::CompactionPhase::Before)],
        "compaction_after" => vec![compaction(event, sessions::CompactionPhase::After)],
        _ => Vec::new(),
    }
}

/// The engine data of one observation: the event's, an item's own over it.
fn laid_over(
    event: &BTreeMap<String, String>,
    own: &BTreeMap<String, String>,
) -> BTreeMap<String, String> {
    let mut merged = event.clone();
    merged.extend(own.iter().map(|(k, v)| (k.clone(), v.clone())));
    merged
}

/// How a call ended, as its fact says it: nothing when the engine said nothing.
fn tool_status(status: ToolStatus) -> Option<sessions::ToolCallStatus> {
    match status {
        ToolStatus::Unspecified => None,
        ToolStatus::Completed => Some(sessions::ToolCallStatus::Completed),
        ToolStatus::Error => Some(sessions::ToolCallStatus::Error),
    }
}

/// A SessionStart's observations: the clear, when it started a new
/// conversation by one, then the bind of the conversation it names (none
/// when it names neither an id nor a transcript).
fn session_start(event: &HookEvent) -> Vec<Observation> {
    let mut held = Vec::new();
    if event.source == CLEAR && !event.native_session.is_empty() {
        held.push(Observation::Cleared(sessions::RecordConversationCleared {
            native_session: event.native_session.clone(),
            trigger: CLEAR.to_string(),
            extra: event.extra.clone(),
        }));
    }
    if !(event.native_session.is_empty() && event.transcript.is_empty()) {
        held.push(Observation::Bound(sessions::BindEngineSession {
            native_id: event.native_session.clone(),
            transcript_path: event.transcript.clone(),
            engine_version: None,
        }));
    }
    held
}

/// The SessionStart source of a clear (claude's `/clear`).
const CLEAR: &str = "clear";

fn compaction(event: &HookEvent, phase: sessions::CompactionPhase) -> Observation {
    Observation::Compaction(sessions::RecordCompaction {
        prompt_id: event.prompt_id.clone(),
        phase,
        trigger: event.trigger.clone(),
        // The summary is conversation content: its size and digest only.
        summary_bytes: event.compact_summary.len() as u64,
        summary_sha256: if event.compact_summary.is_empty() {
            String::new()
        } else {
            format!("{:x}", Sha256::digest(event.compact_summary.as_bytes()))
        },
        extra: event.extra.clone(),
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
            Observation::Cleared(f) => format!("hook:clear:{session}:{}", f.native_session),
            // As `sessions::bind_book` keys it, so the hook's bind and any
            // other's of the same conversation are one fact.
            Observation::Bound(f) if f.native_id.is_empty() => {
                format!("bind:{session}:path:{}", f.transcript_path)
            }
            Observation::Bound(f) => format!("bind:{session}:{}", f.native_id),
            Observation::Compaction(f) => {
                let phase = match f.phase {
                    sessions::CompactionPhase::After => "after",
                    _ => "before",
                };
                let trigger = if f.trigger.is_empty() {
                    "unknown"
                } else {
                    f.trigger.as_str()
                };
                format!("hook:compact:{session}:{}:{trigger}:{phase}", f.prompt_id)
            }
            Observation::CaptureGap(f) => format!("hook:gap:{}", f.dropped),
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
            Observation::Compaction(f) => any(f),
            Observation::Cleared(f) => any(f),
            Observation::Bound(f) => any(f),
            Observation::CaptureGap(f) => any(f),
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

/// Whether `answered` to `book` says the daemon has no record of the session
/// it is addressed to (s4-Q14, H3 follow-up): `book` is a capture fact of
/// the session domain, and the kernel answered it applied with no event (a
/// capture fact records its event on any live session, so none recorded
/// means the stream holds no session). Its distinct answer is
/// [`run_ports::NO_SUCH_SESSION`] ([`run_ports::Delivery::NoSuchSession`]).
///
/// INVARIANT (owner's condition on the answer): this is safe only because
/// the kernel decides every fact on its aggregate's stream folded FRESH
/// from the store (angzarr-embedded's step 3, `read_history`: the stream
/// read from the store for each input; the session aggregate keeps no
/// snapshot), so a session another process or daemon minted a moment
/// earlier is in the fold. A kernel that decided on a cached or older fold
/// would make "applied with no event" a lie; held by the wiring's
/// `a_session_another_process_minted_a_moment_ago_is_seen_by_the_listener`.
pub fn no_such_session(book: &CommandBook, answered: &angzarr_embedded::Answered) -> bool {
    let recorded = answered
        .events
        .as_ref()
        .is_some_and(|events| !events.pages.is_empty());
    answered.delivery == angzarr_embedded::v1::DeliveryOutcome::Applied
        && !recorded
        && is_capture_fact(book)
}

/// Whether `book` is a capture fact of the session domain: one of the facts
/// an [`Observation`] is that records its event on every live session. Not
/// the bind: it records nothing on a live session already bound to its
/// conversation (or one it may not displace), so its silence says nothing
/// of whether the session exists.
fn is_capture_fact(book: &CommandBook) -> bool {
    fn url<M: DomainMessage>() -> String {
        <M::Wire as prost::Name>::type_url()
    }
    let on_sessions = book
        .cover
        .as_ref()
        .is_some_and(|cover| cover.domain == sessions::SESSION_DOMAIN);
    let Some(command_page::Body::Fact(fact)) = book.page.as_ref().and_then(|p| p.body.as_ref())
    else {
        return false;
    };
    let Some(payload) = fact.payload.as_ref() else {
        return false;
    };
    on_sessions
        && [
            url::<sessions::RecordPromptSubmitted>(),
            url::<sessions::RecordAssistantText>(),
            url::<sessions::RecordToolCall>(),
            url::<sessions::RecordTurnEnded>(),
            url::<sessions::RecordCompaction>(),
            url::<sessions::RecordConversationCleared>(),
            url::<sessions::RecordCaptureGap>(),
        ]
        .contains(&payload.type_url)
}

/// How long a hook may spend delivering, by the engine's native hook name:
/// strictly under the engine's own timeout for it, so the engine never kills
/// the hook mid-answer (UserPromptSubmit 30s, MessageDisplay 10s).
pub fn delivery_budget(native: &str) -> Duration {
    match native {
        "UserPromptSubmit" => Duration::from_secs(20),
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
