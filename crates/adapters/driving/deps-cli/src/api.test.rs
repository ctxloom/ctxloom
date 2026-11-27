use library_api::deps::dependency_name;

use super::*;

#[test]
fn test_the_names_are_rooted_at_the_parent() {
    assert_eq!(remote_name("projects/p", "acme"), "projects/p/remotes/acme");
    assert_eq!(bundle_name("users/me", "demo"), "users/me/bundles/demo");
    // OQ-15: 16 hex digits of the identity's SHA-256 (sha256("abc")).
    assert_eq!(
        dependency_name("projects/p", "abc"),
        "projects/p/dependencies/ba7816bf8f01cfea"
    );
}
