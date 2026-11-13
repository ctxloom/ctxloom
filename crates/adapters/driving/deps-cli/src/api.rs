//! A remote's, a dependency's or a bundle's answer as its API message
//! (OD-40, B4; design §5.10 and the owner's rulings on the B4 draft): what
//! every verb's view-to-message mapping shares. The views stay as they are;
//! each verb's is mapped, in `remote.rs` and `dependency.rs`, to its
//! `ctxloom.v1` message (library.proto), named under the parent the names
//! give, and printed as the message's generated proto3 JSON. At T13 the same
//! mapping moves into the daemon.

use daemon_proto::v1;

/// A Remote's resource name: `<parent>/remotes/<remote>`.
pub(crate) fn remote_name(parent: &str, remote: &str) -> String {
    format!("{parent}/remotes/{remote}")
}

/// The kind a view's word names (`mcp` is an MCP server, OQ-B5); a word
/// the API has no kind for is unspecified.
pub(crate) fn item_kind(word: &str) -> i32 {
    (match word {
        "bundle" => v1::ItemKind::Bundle,
        "fragment" => v1::ItemKind::Fragment,
        "command" => v1::ItemKind::Command,
        "skill" => v1::ItemKind::Skill,
        "mcp" => v1::ItemKind::McpServer,
        "profile" => v1::ItemKind::Profile,
        "hook" => v1::ItemKind::Hook,
        _ => v1::ItemKind::Unspecified,
    }) as i32
}

/// A count as the API's 32-bit one (OQ-12), saturating.
pub(crate) fn count(n: u64) -> u32 {
    u32::try_from(n).unwrap_or(u32::MAX)
}

#[cfg(test)]
#[path = "api.test.rs"]
mod tests;
