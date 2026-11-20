//! The client's TLS (owner, 2026-11-12): rustls on ring, the one crypto
//! provider (ruling 35, never aws-lc), trusting the roots of the system's own
//! certificate store, so no OpenSSL is linked. reqwest is built without a
//! provider and handed this config whole.

use std::sync::Arc;

use deps_ports::Why;
use rustls::pki_types::CertificateDer;
use rustls::{ClientConfig, RootCertStore};

/// The config over the roots of the system's store.
pub(crate) fn platform() -> Result<ClientConfig, Why> {
    let found = rustls_native_certs::load_native_certs();
    let errors = found.errors.iter().map(ToString::to_string).collect();
    config(found.certs, errors)
}

/// The config trusting `roots`, those that parse; none is refused, with what
/// the store answered (`errors`).
fn config(roots: Vec<CertificateDer<'static>>, errors: Vec<String>) -> Result<ClientConfig, Why> {
    let mut store = RootCertStore::empty();
    store.add_parsable_certificates(roots);
    if store.is_empty() {
        let mut why =
            "cannot make an HTTPS client: the system's certificate store holds no trusted root"
                .to_string();
        for error in errors {
            why.push_str(": ");
            why.push_str(&error);
        }
        return Err(why);
    }
    let config =
        ClientConfig::builder_with_provider(Arc::new(rustls::crypto::ring::default_provider()))
            .with_safe_default_protocol_versions()
            .map_err(|e| format!("cannot make an HTTPS client: {e}"))?
            .with_root_certificates(store)
            .with_no_client_auth();
    Ok(config)
}

#[cfg(test)]
#[path = "tls.test.rs"]
mod tests;
