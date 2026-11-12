//! The daemon's API (slice 2 step 5; daemon.proto's `Ctxloom` service):
//!
//! - [`Api`], the use cases the daemon serves: `GetDaemon`, `StopDaemon`,
//!   `ResolveProject` (register-or-recheck, owner 2026-11-12), `GetProject`,
//!   `GetSession`, `ListSessions` and `SearchSessions`, each read from the
//!   daemon's WARM read models (the session index its kernel runs, S32),
//!   never by replaying the store per call; a project's own stream alone is
//!   read when it is registered;
//! - [`ApiError`] and [`ApiRefusal`]: a refusal is a typed refusal message
//!   and the gRPC code its `(refusal_status)` declares (S11), the daemon's
//!   own (`ctxloom.daemon.v1`, [`daemon_proto::Refusal`]) or a domain's (the
//!   kernel's rejection, as it refused);
//! - [`rejection_of`]: a typed refusal a client was answered with, as the
//!   diagnostic a person is told;
//! - [`reconcile`]: the client's version-skew rule (M3) over the
//!   `daemon-ports` API.

mod client;
mod refusal;
mod server;

pub use client::{reconcile, resolve, stop, CallError, Reconciled, SkewError};
pub use refusal::{rejection_of, ApiError, ApiRefusal, ApiRejection, Rejection};
pub use server::{About, Answer, Api, Calls, Harps, Hosting, Parts, SessionStates};

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
