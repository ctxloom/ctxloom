//! The daemon API's shared vocabulary (owner ruling 2026-11-27, plan
//! backend-below-frontend step 7): what the daemon's server (`daemon-api`) and
//! the client half (`daemon-api-client`) both speak, so neither depends on the
//! other.
//!
//! - [`ApiError`], [`ApiRefusal`]: a refusal is a typed refusal message and the
//!   gRPC code its `(refusal_status)` declares (S11), the daemon's own or a
//!   domain's;
//! - [`rejection_of`]: a typed refusal a client was answered with, as the
//!   diagnostic a person is told ([`Rejection`], [`ApiRejection`],
//!   [`RemovalRejection`]); [`setup_error`], [`deps_error`] and
//!   [`library_refusal`] are the same mapping's other direction;
//! - [`launch`]: the one launch (T10, `run_ports::Launch`) to the wire
//!   (`ctxloom.v1.Launch`) and back.
//!
//! Its diagnostics are `daemon-api-contract-*`.

pub mod launch;
mod library_refusals;
mod refusal;
mod write_request;

pub use refusal::{
    app_error, deps_error, library_refusal, rejection_of, setup_error, ApiError, ApiRefusal,
    ApiRejection, Rejection, RemovalRejection,
};
pub use write_request::LibraryWriteRequest;

/// This crate's en-US messages, its name first.
pub const EN_US: (&str, &str) = (
    env!("CARGO_PKG_NAME"),
    include_str!("../locales/en-US/messages.ftl"),
);

/// One value of every diagnostic of the crate, for the catalog test.
#[cfg(any(test, feature = "test-support"))]
pub fn diagnostic_samples() -> Vec<Box<dyn coded_diagnostic::Diagnostic>> {
    refusal::samples()
}

/// The read model a trust command's answer is projected by.
pub const TRUST_INDEX: &str = "trust-index";

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
