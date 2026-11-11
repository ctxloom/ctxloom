//! The gate-side stamping seam (D28 S-f): the listener resolves a credential
//! to an [`Identity`] and stamps the requester it names into every command the
//! owner rule guards, whatever the payload said; the session aggregate judges
//! it. The daemon's listener (a later part) calls this on each command its
//! orchestration scope admits; nothing reads a requester from a payload.

use sessions::{
    ClearChild, CloseSession, LaunchChild, SendMessage, SessionCommand, SessionRef, StopSession,
};

use crate::identity::Identity;

impl Identity {
    /// The session this identity speaks for, as the owner rule names it: its
    /// project (the store the listener serves) and its session's stable id.
    pub fn requester(&self, project_id: &str) -> SessionRef {
        let session = uuid::Uuid::from_slice(&self.session.value)
            .map(|id| id.hyphenated().to_string())
            .unwrap_or_default();
        SessionRef {
            project_id: project_id.to_string(),
            session,
        }
    }
}

/// `command` with its `requester` replaced by `requester` when it is one the
/// owner rule guards (close, send, clear, launch, stop); any other command as
/// it was.
pub fn stamped(command: SessionCommand, requester: &SessionRef) -> SessionCommand {
    let requester = Some(requester.clone());
    match command {
        SessionCommand::CloseSession(close) => CloseSession { requester, ..close }.into(),
        SessionCommand::SendMessage(send) => SendMessage { requester, ..send }.into(),
        SessionCommand::ClearChild(clear) => ClearChild { requester, ..clear }.into(),
        SessionCommand::LaunchChild(launch) => LaunchChild {
            requester,
            ..launch
        }
        .into(),
        SessionCommand::StopSession(stop) => StopSession { requester, ..stop }.into(),
        other => other,
    }
}

#[cfg(test)]
#[path = "stamp.test.rs"]
mod tests;
