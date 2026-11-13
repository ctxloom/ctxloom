//! The resource messages carry the names a projection's `Any` is read by.

use super::v1;
use prost::Name;

#[test]
fn test_a_session_is_named_by_its_full_name() {
    assert_eq!(v1::Session::type_url(), "/ctxloom.v1.Session");
}

// Slice 2 step 5 (S11): a refusal's status is the one its proto declares,
// generated, never spelled in Rust.
#[test]
fn test_each_refusal_declares_the_status_its_proto_says() {
    use crate::Refusal;
    assert_eq!(<v1::DaemonHosting as Refusal>::REFUSAL_STATUS, 9);
    assert_eq!(<v1::SessionNotFound as Refusal>::REFUSAL_STATUS, 5);
    assert_eq!(<v1::ProjectNotFound as Refusal>::REFUSAL_STATUS, 5);
    assert_eq!(<v1::NameInvalid as Refusal>::REFUSAL_STATUS, 3);
    assert_eq!(<v1::PlanStale as Refusal>::REFUSAL_STATUS, 9);
}
