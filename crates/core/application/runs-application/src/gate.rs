//! What the gate and the use cases share: a message's type URL, and `owns`, the addressing predicate (a
//! caller's own run and session). The gate itself is angzarr's `Gate`
//! ([`crate::UserGate`]); the local gate, its two
//! principals and their allowlists are gone (slice 2, the separation model:
//! one principal, the user).

use angzarr_embedded::v1::Cover;
use angzarr_embedded::StoreError;

use crate::error::AppError;
use crate::identity::Identity;

/// The type URL of a domain message's wire form.
#[cfg(test)]
pub(crate) fn url<M: angzarr_embedded::markup::DomainMessage>() -> String {
    <M::Wire as prost::Name>::type_url()
}

/// A store that could not be read.
pub(crate) fn failed(error: StoreError) -> AppError {
    AppError::Failed {
        reason: error.to_string(),
    }
}

/// Whether `identity` owns the stream `cover` names: its own run, its own
/// session. A pure addressing predicate (listener plan 4.5), not a
/// permission.
pub fn owns(identity: &Identity, cover: &Cover) -> bool {
    *cover == identity.run_cover() || *cover == identity.session_cover()
}
