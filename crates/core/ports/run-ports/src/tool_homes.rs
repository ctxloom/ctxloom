//! The variables that point HOME-keyed tools at the user's real data.
//!
//! An engine's `HOME` is its session home (owner ruling 112), so rustup,
//! cargo and npm, which find their data through `HOME`, would start from
//! nothing in every session. A launch therefore names their real locations
//! to the engine, and owns the names so no permit replaces them.

use std::collections::BTreeMap;

/// rustup's data directory (toolchains).
pub const RUSTUP_HOME: &str = "RUSTUP_HOME";
/// cargo's data directory (registry, git checkouts, installed binaries).
pub const CARGO_HOME: &str = "CARGO_HOME";
/// npm's cache directory.
pub const NPM_CACHE: &str = "npm_config_cache";

/// Every name a launch sets to the user's real tool location.
pub const TOOL_HOME_VARIABLES: &[&str] = &[RUSTUP_HOME, CARGO_HOME, NPM_CACHE];

/// The tool homes a launch gives its engine.
pub fn tool_homes(
    _caller: &BTreeMap<String, String>,
    _user_home: Option<&str>,
) -> BTreeMap<String, String> {
    BTreeMap::new()
}

#[cfg(test)]
#[path = "tool_homes.test.rs"]
mod tests;
