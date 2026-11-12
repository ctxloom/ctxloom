//! The resource messages carry the names a projection's `Any` is read by.

use super::v1;
use prost::Name;

#[test]
fn test_a_session_is_named_by_its_full_name() {
    assert_eq!(v1::Session::type_url(), "/ctxloom.daemon.v1.Session");
}
