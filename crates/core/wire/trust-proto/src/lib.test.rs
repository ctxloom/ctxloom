//! The wire messages carry the names the kernel routes by: a stored event's
//! type URL must never change.

use super::v1;
use prost::Name;

#[test]
fn every_message_is_in_the_trust_package() {
    for url in [
        v1::GrantTrust::type_url(),
        v1::RevokeTrust::type_url(),
        v1::TrustGranted::type_url(),
        v1::TrustRevoked::type_url(),
        v1::PlanStale::type_url(),
        v1::TrustState::type_url(),
    ] {
        assert!(url.starts_with("/ctxloom.trust.v1."), "{url}");
    }
}
