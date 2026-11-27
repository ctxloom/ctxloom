//! The terminal wake's API side (owner ruling 2026-11-18): `WatchSession`,
//! which a primary terminal session's ctxloom MCP server holds open for its
//! own session, and what `GetSession` says of the wake. The checks are
//! `GetSession`'s: the daemon open, the name a session's, a session the
//! warm index holds (the bearer is the edge's).

use std::sync::Arc;

use daemon_proto::v1;
use run_ports::Edge;
use sessions::{SessionName, SessionState};

use crate::server::{invalid, Api};
use daemon_api_contract::{ApiError, ApiRefusal};

/// A watched session's signals, as `WatchSession` streams them: the one it
/// opened with, then each change, and the same again each keepalive;
/// waited for (the API's edge runs it on a blocking thread). Its session is
/// watched until it is dropped.
pub type SessionStream = Box<dyn Iterator<Item = v1::SessionSignal> + Send>;

/// What `session show` says of a session's wake: `outstanding`, `not
/// answered`, or nothing.
pub(crate) fn wake_of(state: Option<&SessionState>) -> &'static str {
    match state {
        Some(state) if !state.wake_outstanding.is_empty() => "outstanding",
        Some(state) if !state.wake_unanswered.is_empty() => "not answered",
        _ => "",
    }
}

impl<E: Edge> Api<E> {
    /// The API, its `WatchSession` watching through `watches` (shared with
    /// the daemon's loop, which publishes).
    pub fn watching(self, watches: Arc<daemon_application::SessionWatches>) -> Api<E> {
        Api {
            watches: Some(watches),
            ..self
        }
    }

    /// Whether `session`'s (its stable id) ctxloom MCP server watches it.
    pub(crate) fn wakeable(&self, session: &str) -> bool {
        self.watches
            .as_ref()
            .is_some_and(|watches| watches.is_watched(session))
    }

    /// `WatchSession`: the session `request` names, watched from now.
    pub async fn watch_session(
        &self,
        request: v1::WatchSessionRequest,
    ) -> Result<SessionStream, ApiError> {
        let watches = self.watches.as_ref().ok_or_else(|| ApiError::Failed {
            detail: "this daemon watches no session".to_string(),
        })?;
        let name = SessionName::parse(&request.name)
            .map_err(|rejection| invalid(&request.name, &rejection))?;
        self.parts.hosting.fresh().await;
        let session = name.uid();
        if self.parts.index.index().find_by_id(&session).is_none() {
            return Err(ApiError::from(ApiRefusal::of(&v1::SessionNotFound {
                name: request.name.clone(),
            })));
        }
        // Lock order: the daemon's loop holds the board and then reads the
        // pending messages, so this takes them one at a time, never the
        // pending messages while it waits for the board (a deadlock, which
        // also parked every blocking thread behind it).
        let pending = self.parts.pending.pending().pending(&session);
        let now =
            daemon_application::signal_of(&pending, self.parts.board.board().session(&session));
        Ok(Box::new(watches.watch(&session, now)))
    }
}

#[cfg(test)]
#[path = "watch.test.rs"]
mod tests;
