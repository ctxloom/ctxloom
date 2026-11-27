//! What records a hook's observations, and what it did not record: the
//! application's side of the hook, which the CLI's `execute_recording` runs
//! and the daemon's tests drive (a backend crate: it names no frontend).
//! Fail-open is by type: [`Recording::record`] cannot fail, only report what
//! it did not record ([`Unrecorded`]: dropped, or HELD for later, s4-Q14).

use crate::HookAnswer;

/// One observation that was not recorded.
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct NotRecorded {
    /// The fact's external id.
    pub what: String,
    /// The delivery's text (unavailable, timed out, refused ...).
    pub why: String,
}

/// One observation the hook did not record now (s4-Q14, H2).
#[derive(Clone, PartialEq, Eq, Debug)]
pub enum Unrecorded {
    /// Dropped: neither recorded nor held.
    Dropped(NotRecorded),
    /// Kept in the session's hold, for `why` (the delivery's or the
    /// daemon's text).
    Held {
        what: String,
        harp: String,
        why: String,
    },
    /// Kept in the session's hold behind the facts it already holds, so the
    /// session's facts are recorded in the order they happened.
    HeldBehind { what: String, harp: String },
}

impl From<NotRecorded> for Unrecorded {
    fn from(dropped: NotRecorded) -> Unrecorded {
        Unrecorded::Dropped(dropped)
    }
}

/// What records the hook's observations: the application's, behind the hook.
pub trait Recording {
    /// Record what `answer.event` holds for the session `harp`, within the
    /// hook's own delivery budget for `native`. Returns what was not
    /// recorded now.
    fn record(&self, native: &str, harp: &str, answer: &HookAnswer) -> Vec<Unrecorded>;
}
