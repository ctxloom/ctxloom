//! The daemon API's client half (owner ruling 2026-11-27, plan
//! backend-below-frontend step 7): what a command, a session's MCP server or a
//! hook is as the daemon's client. It depends on the shared vocabulary
//! (`daemon-api-contract`) and not on the server (`daemon-api`), so a frontend
//! reaches the daemon without the code that serves it.
//!
//! - [`Client`]: a command as the daemon's client: `daemon status|stop` and
//!   the session reads ([`SessionReads`]), the removals ([`SessionRemovals`])
//!   and the library's reads and writes ([`LibraryReads`], [`LibraryWrites`]),
//!   each reaching a daemon, resolving its project and sending its
//!   environment, retried once on a new daemon (S8);
//! - [`reconcile`]: the client's version-skew rule (M3) over the
//!   `daemon-ports` API;
//! - [`Caller`]: a SESSION as the daemon's client (slice 3, ctxloom's MCP
//!   server's), or the USER's child verbs (slice 4 U3), the user's trust
//!   ([`TrustWrites`]) and approvals ([`ApprovalAnswers`]);
//! - [`Waker`]: a primary terminal session's wake (step 9);
//! - [`CallError`]: what a call came to.
//!
//! Its diagnostics are `daemon-api-client-*`; the refusals it reads back are
//! the contract's.

mod approvals;
mod caller;
mod caller_wake;
mod client;
mod session_client;
mod trust;
mod waker;

pub use ::runs::{LaunchFinding, RunRejection};
pub use approvals::{ask_of, settled_how, settled_of, settled_refusal, Answered, ApprovalAnswers};
pub use caller::{
    launch_lock_name, urgency_of, Caller, ChildWrites, Launch, ProjectResolver, Written,
    DELEGATION_LIMIT,
};
pub use client::{
    newer, reconcile, reconcile_session, resolve, stop, CallError, Reconciled, SkewError,
};
pub use daemon_api_contract::{
    deps_error, launch, library_refusal, rejection_of, setup_error, ApiError, ApiRefusal,
    ApiRejection, LibraryWriteRequest, Rejection, RemovalRejection, TRUST_INDEX,
};
pub use library_api::premise::{render_premise_index, Shown};
pub use run_ports::{wake_line, wake_nonce, WakePost};
pub use session_client::{
    Client, LibraryReads, LibraryWrites, Removed, RequestIds, SessionReads, SessionRemovals,
};
/// The one joiner and placer of a log's words, for the screen and `session
/// transcript show` alike (`session_index::conversation`).
pub use session_index::conversation;
pub use trust::{TrustWrites, Trusted};
pub use waker::{WakeAct, Waker, DISARM_AFTER};

/// What a call can come to, and why no daemon could be called: the port's,
/// as a driving adapter names them (it sees the application alone).
pub use daemon_ports::{
    Called, CommandBook, DaemonApi, Daemons, NextSignal, Read, SessionSignals, Synced, Unreached,
    Until,
};

/// This crate's en-US messages, its name first.
pub const EN_US: (&str, &str) = (
    env!("CARGO_PKG_NAME"),
    include_str!("../locales/en-US/messages.ftl"),
);

/// One value of every diagnostic of the crate, for the catalog test.
#[cfg(any(test, feature = "test-support"))]
pub fn diagnostic_samples() -> Vec<Box<dyn coded_diagnostic::Diagnostic>> {
    client::samples()
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
