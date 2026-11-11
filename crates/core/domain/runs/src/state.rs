//! The run, folded from its events.

use crate::{EndCause, RunEvent, RunMode, RunnerStartedRecorded, TurnUrgency};

/// The runs domain's name: every run stream's `Cover.domain`.
pub const RUN_DOMAIN: &str = "runs";

/// The run, folded from its events.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct RunState {
    pub session: String,
    pub incarnation: u32,
    pub mode: Option<RunMode>,
    /// SHA-256 of the RUNNER principal's token; the token itself is never
    /// journaled (D29).
    pub runner_credential_hash: Vec<u8>,
    /// SHA-256 of the MODEL principal's token. Always different from the
    /// runner's, so a token names exactly one principal (owner ruling
    /// 2026-11-04, item 16).
    pub model_credential_hash: Vec<u8>,
    /// SHA-256 of the HOOK principal's token (owner ruling 2026-11-09): the
    /// engine's hook processes (interactive or one-shot) present it, and its allowlist is
    /// the capture facts alone. Empty on a run enqueued before it existed,
    /// which no token matches.
    pub hook_credential_hash: Vec<u8>,
    /// Set by the fold of `RunEnded`, for every token, the owner's included,
    /// whatever the cause (D29).
    pub credential_revoked: bool,
    pub runner_attached: bool,
    /// What the launcher said of the runner it started (runtime, workspace,
    /// pid), once it did.
    pub runner_started: Option<RunnerStartedRecorded>,
    /// The sequence of the `TurnQueued` event of the turn in flight (plan
    /// 4.3: the turn number is a per-stream position, the cause): the first
    /// of `turns`.
    pub turn_in_flight: Option<u64>,
    /// Every turn queued and not yet ended, oldest first (S-b): the one in
    /// flight, then the steers queued behind it.
    pub turns: Vec<OpenTurn>,
    pub stop_requested: bool,
    /// How many summary claims the model has recorded: advisory evidence, no
    /// transition (owner ruling 2026-11-04, item 16).
    pub summaries: u32,
    /// The turn of the model's final claim, if it made one. A CLAIM: the run
    /// stays live, its credentials valid and its next turn queueable.
    pub final_reported: Option<u64>,
    pub ended: Option<EndCause>,
}

/// A turn queued and not yet ended (S-b).
#[derive(Clone, Debug, PartialEq)]
pub struct OpenTurn {
    /// The sequence of its `TurnQueued` event.
    pub turn: u64,
    /// The session's messages it carries: its key.
    pub message_ids: Vec<String>,
    pub urgency: TurnUrgency,
    /// Whether a steer asked it to be interrupted.
    pub interrupt_requested: bool,
}

/// Fold `event`, the page at `sequence` of the run's stream, into `state`:
/// what the aggregate's appliers do, one place, so a reader of the stream
/// (the credential check) folds as the aggregate does. Pure.
pub fn apply(state: &mut RunState, event: &RunEvent, sequence: u64) {
    fold(state, event, sequence);
    state.turn_in_flight = state.turns.first().map(|open| open.turn);
}

fn fold(state: &mut RunState, event: &RunEvent, sequence: u64) {
    match event {
        RunEvent::RunEnqueued(enqueued) => {
            state.session = enqueued.session.clone();
            state.incarnation = enqueued.incarnation;
            state.mode = Some(enqueued.mode);
            state.runner_credential_hash = enqueued.runner_credential_hash.clone();
            state.model_credential_hash = enqueued.model_credential_hash.clone();
            state.hook_credential_hash = enqueued.hook_credential_hash.clone();
        }
        RunEvent::RunnerAttachedRecorded(_) => state.runner_attached = true,
        RunEvent::RunnerStartedRecorded(started) => state.runner_started = Some(started.clone()),
        // The turn in flight is the sequence of its `TurnQueued` event (plan 4.3).
        RunEvent::TurnQueued(queued) => state.turns.push(OpenTurn {
            turn: sequence,
            message_ids: queued.message_ids.clone(),
            urgency: queued.urgency,
            interrupt_requested: false,
        }),
        RunEvent::InterruptRequested(interrupt) => {
            if let Some(open) = state.turns.iter_mut().find(|t| t.turn == interrupt.turn) {
                open.interrupt_requested = true;
            }
        }
        RunEvent::TurnStartedRecorded(_) => {}
        RunEvent::TurnCompletedRecorded(completed) => {
            free_the_gate(state, completed.turn, &completed.message_ids);
        }
        RunEvent::TurnFailedRecorded(failed) => {
            free_the_gate(state, failed.turn, &failed.message_ids);
        }
        RunEvent::StopRequested(_) => state.stop_requested = true,
        RunEvent::SummaryReportedRecorded(_) => state.summaries += 1,
        RunEvent::FinalReportedRecorded(reported) => state.final_reported = Some(reported.turn),
        // The end revokes every credential, the owner's included, whatever the
        // cause (D29).
        RunEvent::RunEnded(ended) => {
            state.ended = Some(ended.cause);
            state.credential_revoked = true;
        }
    }
}

/// A turn that completed or failed is no longer open: the one holding the
/// first of its messages, else (a turn of no message, the one-shot's) the one
/// of its number; an end matching no open turn frees nothing. The turn in
/// flight is then the first still open.
fn free_the_gate(state: &mut RunState, turn: u64, message_ids: &[String]) {
    let at = match message_ids.first() {
        Some(id) => state.turns.iter().position(|t| t.message_ids.contains(id)),
        None => state.turns.iter().position(|t| t.turn == turn),
    };
    if let Some(at) = at {
        state.turns.remove(at);
    }
}
