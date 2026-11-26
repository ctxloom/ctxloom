//! What the sessions domains run under, and the daemon's retention default.

use angzarr_embedded::Registry;
use sessions::{NameClaimAggregate, ProjectAggregate, ProjectNameAggregate, SessionAggregate};

/// The registry the sessions domains run under: the session aggregate and the
/// name claims'.
pub fn registry() -> Registry {
    let mut registry = Registry::new();
    registry
        .aggregate::<SessionAggregate>(SessionAggregate::default)
        .and_then(|registry| registry.aggregate::<NameClaimAggregate>(NameClaimAggregate::default))
        .and_then(|registry| registry.aggregate::<ProjectAggregate>(ProjectAggregate::default))
        .and_then(|registry| {
            registry.aggregate::<ProjectNameAggregate>(ProjectNameAggregate::default)
        })
        .expect("the sessions, session-names, projects and project-names domains, each once");
    registry
}

/// How long a session's transcript is kept after it ended, unless configured
/// (Go: `session_transcript_retention`, 30 days): the daemon's destroy's.
pub const DEFAULT_RETENTION_DAYS: u64 = 30;
