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

#[test]
fn test_each_word_is_its_kind_and_mcp_is_an_mcp_server() {
    let kinds = [
        ("bundle", v1::ItemKind::Bundle),
        ("fragment", v1::ItemKind::Fragment),
        ("command", v1::ItemKind::Command),
        ("skill", v1::ItemKind::Skill),
        ("mcp", v1::ItemKind::McpServer),
        ("profile", v1::ItemKind::Profile),
        ("hook", v1::ItemKind::Hook),
        ("prompt", v1::ItemKind::Unspecified),
    ];
    for (word, kind) in kinds {
        assert_eq!(item_kind(word), kind as i32, "{word}");
    }
}

#[test]
fn test_a_count_saturates_at_the_32_bit_limit() {
    assert_eq!(count(7), 7);
    assert_eq!(count(u64::from(u32::MAX) + 1), u32::MAX);
}
