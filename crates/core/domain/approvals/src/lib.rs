//! The approvals domain (step 8 U4, ruling 142): the permission ask of a
//! session the daemon runs (a child or a one-shot), routed to the HUMAN
//! (S25), answered once with one of the options it offers, withdrawn when
//! its engine cancels it, or expired when its time runs out (S26: by its alarm,
//! through the scheduler, [`ArmsExpiry`] and [`DeliverToApprovals`], U5). One stream per ask, rooted on the UUIDv5 of its
//! asking session and its key ([`ask_root`]); its name, `ask-<adjective>-
//! <noun>` (D9, Q7), unique among the asks of its project by a claim
//! stream of its own in `approval-names` ([`name_root`]).
//!
//! - [`ApprovalAggregate`]: `RecordApprovalAsked` (a fact: recorded once;
//!   with a timeout of 0, `approval_timeout: now`, expired in the same
//!   decide, D16), `RecordApprovalWithdrawn` (a fact: nothing on a settled
//!   ask), and `AnswerApproval` (refused unknown, settled, an option not
//!   offered, or a message ACP cannot carry, Q6).
//! - [`ApprovalNameAggregate`]: an ask's name claimed for it.
//! - [`claude_options`]: the four options ctxloom builds for a claude ask
//!   (owner, after P0), about the rule the ask implies (Q5).

// Generated code: an ask's fact and event carry its whole ask (options and
// names), so its payload enums' variants differ in size.
#[allow(dead_code, clippy::too_many_lines, clippy::large_enum_variant)]
mod generated {
    include!(concat!(env!("OUT_DIR"), "/ctxloom.approvals.v1.domain.rs"));
}
mod aggregate;
mod expiry;
mod names;
mod options;
mod refusal;
mod rejection;
mod root;
mod state;

/// The approval aggregate's commands, one variant per command type.
pub type ApprovalCommand = ApprovalStateCommand;
/// The approval aggregate's events, one variant per event type.
pub type ApprovalEvent = ApprovalStateEvent;
/// The name claim's commands.
pub type ApprovalNameCommand = ApprovalNameStateCommand;
/// The name claim's events.
pub type ApprovalNameEvent = ApprovalNameStateEvent;

pub use aggregate::ApprovalAggregate;
pub use expiry::{expiry_alarm, expiry_at, ArmsExpiry, DeliverToApprovals, EXPIRY_ALARM_PREFIX};
pub use generated::*;
pub use names::{ApprovalNameAggregate, NameClaim};
pub use options::{
    bounded, claude_options, kind_word, Bounded, ALLOW_ALWAYS, ALLOW_ONCE, ALLOW_SESSION, DENY,
    DENY_ALWAYS, EXCERPT_BYTES,
};
pub use refusal::NotOurs;
#[cfg(any(test, feature = "test-support"))]
pub use rejection::diagnostic_samples;
pub use rejection::{ApprovalsRejection, EN_US};
pub use root::{
    ask_key, ask_name, ask_root, fact_book, is_ask_name, name_root, ApprovalName, ASK_PREFIX,
};
pub use state::{Ask, Status, APPROVALS_DOMAIN, APPROVAL_NAMES_DOMAIN};

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
