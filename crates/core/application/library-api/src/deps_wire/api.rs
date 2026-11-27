//! What every deps message's mapping shares.

use daemon_proto::v1;

/// The kind a view's word names (`mcp` is an MCP server, OQ-B5); a word
/// the API has no kind for is unspecified.
pub fn item_kind(word: &str) -> i32 {
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
pub fn count(n: u64) -> u32 {
    u32::try_from(n).unwrap_or(u32::MAX)
}

#[cfg(test)]
#[path = "api.test.rs"]
mod tests;
