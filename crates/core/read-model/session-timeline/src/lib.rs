//! A session's timeline (`ctxloom top`'s log): one time-ordered list per
//! session of what happened to it, folded from the streams that already
//! record it, with nothing of its own persisted:
//!
//! - its lifecycle, from the session stream (started, exited with its
//!   cause, re-hosted, restarted, closed, reopened, a launch that failed);
//! - its asks, from the approvals streams (asked, settled);
//! - its transcript lines, the same lines the session index folds from the
//!   capture events (`session_index::Projection`), each at its event's
//!   recorded instant.
//!
//! Held in memory on the projector port like the other read models
//! ([`SessionTimelines`]), woken watchers see each change pushed.

use std::sync::Arc;

use angzarr_embedded::angzarr_router::error::CodedError;
use angzarr_embedded::angzarr_router::hpb::{ProjectRequest, ProjectResponse};
use angzarr_embedded::v1::{Cover, EventBook};
use angzarr_embedded::{Any, CatchUp, PayloadError, ProjectorHandler, Timestamp};
use approvals::APPROVALS_DOMAIN;
use held_projection::{Fill, Held, Lock, Model};
use sessions::SESSION_DOMAIN;

/// The projector's name.
pub const SESSION_TIMELINE: &str = "session-timeline";

/// The domains the timeline folds.
pub const SOURCES: [&str; 2] = [SESSION_DOMAIN, APPROVALS_DOMAIN];

/// The most entries kept for one session; the oldest go first.
pub const KEPT: usize = 2000;

/// What an entry says.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Content {
    /// A transcript line, as the transcript has it (no newline).
    Transcript { line: String },
    /// An event of the daemon: a word and its text.
    Daemon { event: String, text: String },
}

/// One entry of a session's timeline.
#[derive(Clone, Debug, PartialEq)]
pub struct Entry {
    /// The order this timeline received it in, from 1: what a watch says it
    /// has told up to.
    pub n: u64,
    pub time: Timestamp,
    pub content: Content,
}

/// Every session's timeline.
#[derive(Default)]
pub struct TimelineIndex {}

impl TimelineIndex {
    pub fn project(&mut self, _book: &EventBook) -> Result<(), PayloadError> {
        Ok(())
    }

    /// The entries of the session with the stable id `session`, oldest first.
    pub fn entries(&self, _session: &str) -> Vec<Entry> {
        Vec::new()
    }

    /// The entries of `session` received after `n`, oldest first.
    pub fn after(&self, session: &str, n: u64) -> Vec<Entry> {
        self.entries(session)
            .into_iter()
            .filter(|entry| entry.n > n)
            .collect()
    }
}

impl Model for TimelineIndex {
    const NAME: &'static str = SESSION_TIMELINE;

    fn project(&mut self, book: &EventBook) -> Result<(), PayloadError> {
        TimelineIndex::project(self, book)
    }

    fn view(&self, _cover: &Cover) -> Option<Any> {
        None
    }
}

/// The timelines on the projector port: every clone is the same fold.
#[derive(Clone, Default)]
pub struct SessionTimelines(Held<TimelineIndex>);

impl SessionTimelines {
    /// An empty index whose gaps are read through `fill`.
    pub fn filled(fill: Arc<dyn Fill>) -> SessionTimelines {
        SessionTimelines(Held::new(Some(fill)))
    }

    /// The index, locked.
    pub fn index(&self) -> Lock<'_, TimelineIndex> {
        self.0.lock()
    }

    /// As the other read models': woken through `wake` each time a fold
    /// changes it.
    pub fn watch_into(&self, wake: std::sync::mpsc::Sender<()>) {
        self.0.watch_into(wake);
    }
}

impl ProjectorHandler for SessionTimelines {
    fn name(&self) -> &str {
        self.0.name()
    }

    async fn dispatch(&self, request: &ProjectRequest) -> Result<ProjectResponse, CodedError> {
        self.0.dispatch(request).await
    }
}

/// `model` caught up from a store, sharing its fold.
pub fn caught_up(model: SessionTimelines) -> CatchUp<SessionTimelines> {
    CatchUp::new(&SOURCES, model).expect("the timelines read the sessions and approvals domains")
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
