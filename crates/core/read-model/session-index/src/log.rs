//! The session's log entries folded from the session stream's capture
//! events: one engine-neutral [`LogEntry`] per transcript event, typed, with
//! nothing that names the session, its harp or its engine (the log is one
//! session's, already selected).
//!
//! Invariants: pure (the instant enters as an argument, from the event
//! page's `recorded_at`); `seq` is assigned HERE, from the order this model
//! is fed the stream, never by a hook process; one stream page makes its
//! entry once, however many times it is handed again; an engine's own data
//! the adapter attached to the observation is the entry's `extra`, never a
//! field of an arm.

use std::collections::BTreeMap;

use angzarr_embedded::Timestamp;
use daemon_proto::v1::{self, log_compaction, log_entry::Entry as Arm, log_tool};
use sessions::{CompactionPhase, SessionEvent, ToolCallStatus};

/// A session's log entries, folded page by page.
#[derive(Default)]
pub struct Projection {
    /// How many entries the log holds.
    entries: u64,
    /// The last stream page folded; a page at or before it is a repeat.
    high_water: Option<u64>,
}

impl Projection {
    /// A log that holds no entry yet.
    pub fn new() -> Projection {
        Projection::default()
    }

    /// The entry `event`, the stream's page `page_sequence` recorded at
    /// `recorded_at`, adds to the log; none for an event that is not a log's
    /// (or a page already folded).
    pub fn fold(
        &mut self,
        page_sequence: u64,
        recorded_at: Timestamp,
        event: &SessionEvent,
    ) -> Option<v1::LogEntry> {
        if self
            .high_water
            .is_some_and(|folded| page_sequence <= folded)
        {
            return None;
        }
        self.high_water = Some(page_sequence);
        let (arm, extra) = arm_of(event)?;
        let entry = v1::LogEntry {
            time: Some(recorded_at.into()),
            seq: self.entries,
            entry: Some(arm),
            extra,
        };
        self.entries += 1;
        Some(entry)
    }

    /// How many entries the log holds: the `seq` the next takes.
    pub fn entries(&self) -> u64 {
        self.entries
    }
}

/// The arm an event is, with the engine data its observation carried; none
/// for an event that is not the log's.
fn arm_of(event: &SessionEvent) -> Option<(Arm, BTreeMap<String, String>)> {
    Some(match event {
        SessionEvent::PromptSubmitted(prompt) => (
            Arm::Prompt(v1::LogPrompt {
                prompt_id: prompt.prompt_id.clone(),
                text: prompt.prompt.clone(),
            }),
            prompt.extra.clone().into_iter().collect(),
        ),
        SessionEvent::AssistantTextShown(shown) => (
            Arm::Words(v1::LogWords {
                prompt_id: shown.prompt_id.clone(),
                turn_id: shown.turn_id.clone(),
                message_id: shown.message_id.clone(),
                index: shown.index,
                is_final: shown.is_final,
                text: shown.text.clone(),
            }),
            shown.extra.clone().into_iter().collect(),
        ),
        SessionEvent::ToolCalled(called) => (
            Arm::Tool(v1::LogTool {
                prompt_id: called.prompt_id.clone(),
                tool_use_id: called.tool_use_id.clone(),
                name: called.tool.clone(),
                input: called.input.clone(),
                response_bytes: called.response_bytes,
                response_sha256: called.response_sha256.clone(),
                // How the call ended, only where the engine said.
                status: called.status.map(|status| match status {
                    ToolCallStatus::Completed => log_tool::Status::Completed as i32,
                    ToolCallStatus::Error => log_tool::Status::Error as i32,
                }),
            }),
            called.extra.clone().into_iter().collect(),
        ),
        SessionEvent::TurnEnded(ended) => (
            Arm::TurnEnd(v1::LogTurnEnd {
                prompt_id: ended.prompt_id.clone(),
            }),
            ended.extra.clone().into_iter().collect(),
        ),
        SessionEvent::ConversationCleared(cleared) => (
            Arm::Clear(v1::LogClear {
                native_session: cleared.native_session.clone(),
                trigger: cleared.trigger.clone(),
            }),
            cleared.extra.clone().into_iter().collect(),
        ),
        SessionEvent::Compacted(compacted) => (
            Arm::Compaction(v1::LogCompaction {
                prompt_id: compacted.prompt_id.clone(),
                phase: match compacted.phase {
                    CompactionPhase::After => log_compaction::Phase::After,
                    _ => log_compaction::Phase::Before,
                } as i32,
                trigger: compacted.trigger.clone(),
                summary_bytes: compacted.summary_bytes,
                summary_sha256: compacted.summary_sha256.clone(),
            }),
            compacted.extra.clone().into_iter().collect(),
        ),
        // A fact a hook dropped: one gap is one fact missing here, dropped at
        // the hook's clock, not this entry's `time`.
        SessionEvent::CaptureGapped(gap) => (
            Arm::Gap(v1::LogGap {
                dropped: gap.dropped.clone(),
                why: gap.why.clone(),
                dropped_at: Some(gap.dropped_at.into()),
            }),
            BTreeMap::new(),
        ),
        _ => return None,
    })
}

#[cfg(test)]
#[path = "log.test.rs"]
mod tests;
