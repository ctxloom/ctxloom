//! The canonical transcript folded from the session stream's capture events
//! (owner ruling 87a: no file; the index folds it, each read afresh).
//!
//! Invariants: pure (the instant enters as an argument, from the event page's
//! `recorded_at`); `seq` is assigned HERE, from the order this model is fed
//! the stream, never by a hook process; one stream page makes its lines once,
//! however many times it is handed again.

use serde_json::{json, Value};
use sessions::{CompactionPhase, SessionEvent, ToolCallStatus};

use crate::transcript::{entry_line, rfc3339, sorted, Speaker};

/// The transcript of one session, folded page by page.
pub struct Projection {
    harp: String,
    session_id: String,
    engine: String,
    /// How many lines the transcript holds.
    lines: u64,
    /// The last stream page folded; a page at or before it is a repeat.
    high_water: Option<u64>,
}

impl Projection {
    /// A transcript that holds no line yet.
    pub fn new(harp: &str, session_id: &str, engine: &str) -> Projection {
        Projection {
            harp: harp.to_string(),
            session_id: session_id.to_string(),
            engine: engine.to_string(),
            lines: 0,
            high_water: None,
        }
    }

    /// The lines folded after this carry `harp` (a rename).
    pub fn rename(&mut self, harp: &str) {
        self.harp = harp.to_string();
    }

    /// The lines (newline included) `event`, the stream's page
    /// `page_sequence` recorded at `recorded_at` seconds, adds to the
    /// transcript; none for an event that is not a transcript's (or a page
    /// already folded).
    pub fn fold(
        &mut self,
        page_sequence: u64,
        recorded_at: i64,
        event: &SessionEvent,
    ) -> Vec<String> {
        if self
            .high_water
            .is_some_and(|folded| page_sequence <= folded)
        {
            return Vec::new();
        }
        self.high_water = Some(page_sequence);
        let seq = self.lines;
        let line = match event {
            SessionEvent::PromptSubmitted(prompt) => entry_line(
                &self.harp,
                &self.session_id,
                &self.engine,
                seq,
                recorded_at,
                Speaker::User,
                &prompt.prompt,
            ),
            SessionEvent::AssistantTextShown(shown) => self.line(
                seq,
                recorded_at,
                "assistant_delta",
                json!({
                    "prompt_id": shown.prompt_id,
                    "turn_id": shown.turn_id,
                    "delta": {
                        "message_id": shown.message_id,
                        "index": shown.index,
                        "final": shown.is_final,
                        "text": shown.text,
                    },
                }),
            ),
            SessionEvent::ToolCalled(called) => {
                let mut tool = json!({
                    "id": called.tool_use_id,
                    "name": called.tool,
                    "input": called.input,
                    "response_bytes": called.response_bytes,
                    "response_sha256": called.response_sha256,
                });
                // How the call ended, only where the engine said (opencode).
                if let (Some(status), Some(members)) = (called.status, tool.as_object_mut()) {
                    members.insert(
                        "status".to_string(),
                        json!(match status {
                            ToolCallStatus::Completed => "completed",
                            ToolCallStatus::Error => "error",
                        }),
                    );
                }
                self.line(
                    seq,
                    recorded_at,
                    "tool",
                    json!({"prompt_id": called.prompt_id, "tool": tool}),
                )
            }
            SessionEvent::TurnEnded(ended) => self.line(
                seq,
                recorded_at,
                "turn_end",
                json!({"prompt_id": ended.prompt_id}),
            ),
            SessionEvent::SessionEnded(ended) => self.line(
                seq,
                recorded_at,
                "session_end",
                json!({"native_session": ended.native_session, "reason": ended.reason}),
            ),
            SessionEvent::Compacted(compacted) => self.line(
                seq,
                recorded_at,
                "compaction",
                json!({
                    "prompt_id": compacted.prompt_id,
                    "phase": match compacted.phase {
                        CompactionPhase::After => "after",
                        _ => "before",
                    },
                    "trigger": compacted.trigger,
                    "summary_bytes": compacted.summary_bytes,
                    "summary_sha256": compacted.summary_sha256,
                }),
            ),
            _ => return Vec::new(),
        };
        self.lines += 1;
        vec![line]
    }

    /// How many lines the transcript holds: the `seq` the next line takes.
    pub fn lines(&self) -> u64 {
        self.lines
    }

    /// One line in D27's envelope, `extra`'s members beside it.
    fn line(&self, seq: u64, seconds: i64, kind: &str, extra: Value) -> String {
        let mut line = json!({
            "v": 1,
            "harp": self.harp,
            "session_id": self.session_id,
            "engine": self.engine,
            "seq": seq,
            "ts": rfc3339(seconds),
            "kind": kind,
        });
        if let (Some(line), Some(extra)) = (line.as_object_mut(), extra.as_object()) {
            line.extend(extra.clone());
        }
        format!("{}\n", sorted(line))
    }
}

#[cfg(test)]
#[path = "projection.test.rs"]
mod tests;
