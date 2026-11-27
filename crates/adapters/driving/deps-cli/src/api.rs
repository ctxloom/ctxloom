//! A remote's, a dependency's or a bundle's answer as its API message
//! (OD-40, B4; design §5.10 and the owner's rulings on the B4 draft): what
//! every verb's view-to-message mapping shares. The views stay as they are;
//! each verb's is mapped, in `remote.rs` and `dependency.rs`, to its
//! `ctxloom.v1` message (library.proto), named under the parent the names
//! give, and printed as the message's generated proto3 JSON. At T13 the same
//! mapping moves into the daemon.

pub(crate) use library_api::deps::remote_name;

/// A local Bundle's resource name: `<parent>/bundles/<bundle>`.
pub(crate) fn bundle_name(parent: &str, bundle: &str) -> String {
    format!("{parent}/bundles/{bundle}")
}

#[cfg(test)]
#[path = "api.test.rs"]
mod tests;
