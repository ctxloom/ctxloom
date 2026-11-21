//! An ask, folded from its events.

use crate::{ApprovalOption, SessionRef};

/// The approvals domain's name: every ask stream's `Cover.domain`.
pub const APPROVALS_DOMAIN: &str = "approvals";

/// The ask names' claim domain (Q7): one stream per name, per project.
pub const APPROVAL_NAMES_DOMAIN: &str = "approval-names";

/// Where an ask stands.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub enum Status {
    /// Nothing asked on the stream.
    #[default]
    Unasked,
    /// Asked, and waiting for its answer.
    Waiting,
    /// Answered with the option of this id.
    Answered(String),
    /// Denied for want of an answer (its time ran out, or it was 0).
    Expired,
    /// Withdrawn: its engine cancelled the call (a steer).
    Withdrawn,
}

impl Status {
    /// How a settled ask was settled, as `approvals-approval-settled` says
    /// it: `answered (<option>)`, `expired` or `withdrawn`; none while it
    /// waits or was never asked.
    pub fn how(&self) -> Option<String> {
        match self {
            Status::Answered(option) => Some(format!("answered ({option})")),
            Status::Expired => Some("expired".to_string()),
            Status::Withdrawn => Some("withdrawn".to_string()),
            Status::Unasked | Status::Waiting => None,
        }
    }
}

/// An ask as its stream holds it.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Ask {
    /// `ask-<adjective>-<noun>`.
    pub ask_id: String,
    pub child: Option<SessionRef>,
    pub root: Option<SessionRef>,
    pub run: String,
    pub engine: String,
    pub tool_name: String,
    pub options: Vec<ApprovalOption>,
    pub status: Status,
    /// Its expiry alarm's id (`approval-expiry:<ask>`); empty for an ask
    /// with no time.
    pub alarm_id: String,
    /// When it runs out: recorded plus its time; none with no time.
    pub expires: Option<angzarr_embedded::Timestamp>,
}
