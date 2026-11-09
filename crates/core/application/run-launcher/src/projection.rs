//! PROPOSAL (design/hook-capture): the canonical transcript folded from the
//! session stream's capture events.
//!
//! Invariants: pure (the instant enters as an argument, from the event page's
//! `recorded_at`); `seq` is assigned HERE, from the order this model is fed
//! the stream, never by a hook process; one stream page makes its lines once,
//! however many times it is handed again.

use sessions::SessionEvent;

/// The transcript of one session, folded page by page.
pub struct Projection {
    harp: String,
    session_id: String,
    engine: String,
}

impl Projection {
    /// A transcript that holds no line yet.
    pub fn new(harp: &str, session_id: &str, engine: &str) -> Projection {
        Projection {
            harp: harp.to_string(),
            session_id: session_id.to_string(),
            engine: engine.to_string(),
        }
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
        let _ = (
            page_sequence,
            recorded_at,
            event,
            &self.harp,
            &self.session_id,
            &self.engine,
        );
        todo!("design/hook-capture")
    }

    /// How many lines the transcript holds: the `seq` the next line takes.
    pub fn lines(&self) -> u64 {
        todo!("design/hook-capture")
    }
}

#[cfg(test)]
#[path = "projection.test.rs"]
mod tests;
