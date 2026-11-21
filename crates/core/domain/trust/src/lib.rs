//! The trust domain (step 8 U1, ruling 142): the user's verdict on a
//! project's own settings, one stream per project, named
//! `projects/{harp}/trust` (D2, S58) and rooted on the UUIDv5 of that name.
//! A grant names the etag its preview showed and the etag of the settings
//! now (both the client's: nothing stamps yet, D13), and is refused stale
//! when they differ; a trust lasts until it is revoked (S23). Granting what
//! is trusted, or revoking what is not, records nothing.

#[allow(dead_code)]
mod generated {
    include!(concat!(env!("OUT_DIR"), "/ctxloom.trust.v1.domain.rs"));
}
mod aggregate;
mod name;
mod refusal;
mod rejection;
mod state;

/// The trust aggregate's commands, one variant per command type.
pub type TrustCommand = TrustStateCommand;
/// The trust aggregate's events, one variant per event type.
pub type TrustEvent = TrustStateEvent;

pub use aggregate::TrustAggregate;
pub use generated::*;
pub use name::{trust_name, trust_root, TrustName};
pub use refusal::NotOurs;
#[cfg(any(test, feature = "test-support"))]
pub use rejection::diagnostic_samples;
pub use rejection::{TrustRejection, EN_US};
pub use state::{Trusted, TRUST_DOMAIN};

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
