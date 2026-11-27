//! The daemon's API (slice 2 step 5; daemon.proto's `Ctxloom` service):
//!
//! - [`Api`], the use cases the daemon serves: `GetDaemon`, `StopDaemon`,
//!   `ResolveProject` (register-or-recheck, owner 2026-11-12), `GetProject`,
//!   `GetSession`, `ListSessions` and `SearchSessions`, each read from the
//!   daemon's WARM read models (the session index its kernel runs, S32),
//!   never by replaying the store per call; a project's own stream alone is
//!   read when it is registered;
//! - `DeleteSession` and `SweepSessions` (slice 2 step 6, owner
//!   2026-11-12): the destroy, run inside the daemon over its one store
//!   ([`Purges`]) and each session's project ([`Places`]), through
//!   reclaim-application's one safe order; a refused removal carries its
//!   plan;
//! - [`ApiError`] and [`ApiRefusal`]: a refusal is a typed refusal message
//!   and the gRPC code its `(refusal_status)` declares (S11), the daemon's
//!   own (`ctxloom.v1`, [`daemon_proto::Refusal`]) or a domain's (the
//!   kernel's rejection, as it refused);
//! - [`rejection_of`]: a typed refusal a client was answered with, as the
//!   diagnostic a person is told;
//! - [`reconcile`]: the client's version-skew rule (M3) over the
//!   `daemon-ports` API;
//! - [`Client`]: a command as the daemon's client (slice 2 step 6):
//!   `daemon status|stop` and the session reads ([`SessionReads`]), each
//!   reaching a daemon, resolving its project and sending its environment,
//!   retried once on a new daemon (S8);
//! - `CreateRun`, `GetRun` and `WatchRun` (U4, T7): a one-shot hosted by
//!   the daemon's one-shots, answered once placed, its events streamed
//!   ([`RunStream`]); a TERMINAL run (U5) prepared by the daemon's
//!   terminals, answered with its one launch for the frontend to start;
//! - [`launch`]: the one launch (T10, `run_ports::Launch`) to the wire
//!   (`ctxloom.v1.Launch`) and back;
//! - [`Caller`]: a SESSION as the daemon's client (slice 3, ctxloom's MCP
//!   server's): its project its name's (OD-2), its reads, and its writes
//!   through the coordinator's `HandleSync`, the requester itself; or the
//!   USER's child verbs (slice 4 U3: `session launch|send|clear|close`),
//!   naming no requester;
//! - `CreateHandOff` and `DeleteHandOff` (slice 4 U3, T6, S10): a child's
//!   launch environment kept by the daemon ([`HandOffs`]), its owner and
//!   credential checked first, so a refused launch writes nothing;
//! - [`StartUpIncomplete`]: a daemon whose start-up catch-up or sweep
//!   failed serves anyway and says so (OD-52), in `GetDaemon`'s
//!   `Daemon.warnings`;
//! - [`HandingOver`]: a daemon asked to drain while it hosts children
//!   warns at once what it does with them (OD-54), in the same
//!   `Daemon.warnings`;
//! - `GetTrust` and `ListTrusts` (step 8 U1): a project's trust previewed
//!   at its registered root ([`Trusts`]) and the projects trusted now, from
//!   the warm trust index; the user's grant and revoke ([`TrustWrites`])
//!   through the coordinator's `HandleSync` on `trust-index`;
//! - `CreateApproval`, `GetApproval`, `ListApprovals` and `WaitApproval`
//!   (step 8 U4): a session's permission ask recorded and read from the
//!   warm pending approvals ([`Approving`]), served while draining (D6);
//!   the user's answer ([`ApprovalAnswers`]) through `HandleSync` on
//!   `pending-approvals`, and the MCP server's ask, wait and withdrawal.

mod admit;
mod approvals;
mod clean;
mod deps_writes;
#[cfg(feature = "purge")]
mod destroy;
mod distill;
mod foreign;
mod hand_off;
mod hand_over;
mod hook_context;
mod imports;
mod init;
mod legacy_stores;
mod library_reads;
pub mod library_writes;
mod reactions;
mod runs;
mod server;
mod setup_reads;
mod shown;
mod start_up;
mod terminal;
mod top;
mod trust;
mod watch;

pub use ::runs::{LaunchFinding, RunRejection};
pub use admit::{admit, program_of};
pub use approvals::{Approving, AskNames};
pub use clean::Caches;
pub use daemon_api_contract::{
    app_error, deps_error, launch, library_refusal, rejection_of, setup_error, ApiError,
    ApiRefusal, ApiRejection, Rejection, RemovalRejection, TRUST_INDEX,
};
pub use deps_writes::{DepsAnswer, DepsAsk, DepsPlanned, DepsPlans, DepsTree};
#[cfg(feature = "purge")]
pub use destroy::{Places, Purges};
pub use foreign::{ForeignAnswer, ForeignAsk, ForeignPlanned, ForeignPlans, ForeignPut};
pub use hand_off::{FatalFinding, HandOffContent, HandOffs, HandingOff, Planned};
pub use hand_over::HandingOver;
pub use imports::Imports;
pub use legacy_stores::LegacyStores;
pub use library_api::premise::{render_premise_index, Shown};
pub use library_reads::{Libraries, LibraryRequest};
pub use reactions::{Deliveries, LibraryPoll, Wake};
pub use run_ports::{wake_line, wake_nonce, WakePost};
/// The infrastructure tree's one root (owner, 2026-11-14), for the daemon's
/// service, which answers an error that is no refusal through it.
pub use run_ports::{Infrastructure, DIAGNOSED};
pub use runs::RunStream;
pub use server::{About, Answer, Api, Calls, Harps, Hosting, Observing, Parts, SessionStates};
/// The one joiner and placer of a log's words, for the screen and `session
/// transcript show` alike (`session_index::conversation`).
pub use session_index::conversation;
pub use setup_reads::{SetupAnswer, SetupRead, SetupReads};
pub use start_up::StartUpIncomplete;
pub use top::{TopStop, TopStream};
pub use trust::{etag_of, Trusting, Trusts};
pub use watch::SessionStream;

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
    let mut all = start_up::samples();
    all.extend(hand_over::samples());
    all
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
