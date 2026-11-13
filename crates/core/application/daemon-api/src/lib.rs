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
//!   retried once on a new daemon (S8).

mod client;
mod destroy;
mod refusal;
mod server;
mod session_client;

pub use client::{reconcile, resolve, stop, CallError, Reconciled, SkewError};
pub use destroy::{Places, Purges};
pub use refusal::{rejection_of, ApiError, ApiRefusal, ApiRejection, Rejection, RemovalRejection};
pub use server::{About, Answer, Api, Calls, Harps, Hosting, Parts, SessionStates};
pub use session_client::{Client, Removed, RequestIds, SessionReads, SessionRemovals};

/// What a call can come to, and why no daemon could be called: the port's,
/// as a driving adapter names them (it sees the application alone).
pub use daemon_ports::{Called, Unreached};

/// This crate's en-US messages, its name first.
pub const EN_US: (&str, &str) = (
    env!("CARGO_PKG_NAME"),
    include_str!("../locales/en-US/messages.ftl"),
);

/// One value of every diagnostic of the crate, for the catalog test.
#[cfg(any(test, feature = "test-support"))]
pub fn diagnostic_samples() -> Vec<Box<dyn angzarr_diagnostic::Diagnostic>> {
    let mut all = refusal::samples();
    all.extend(client::samples());
    all
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
