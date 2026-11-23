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

/// npm reads its cache from either spelling; the lower-case one is what
/// the launch sets.
const NPM_CACHE_UPPER: &str = "NPM_CONFIG_CACHE";

/// What each variable defaults to under the user's home when the user's own
/// environment does not set it: the tool's own default location.
const DEFAULTS: &[(&str, &str)] = &[
    (RUSTUP_HOME, ".rustup"),
    (CARGO_HOME, ".cargo"),
    (NPM_CACHE, ".npm"),
];

/// The tool homes a launch gives its engine: for each name, the value the
/// user's own environment (`caller`) sets when it is not empty, else the
/// tool's default under `user_home`; none of it when neither exists. Never
/// the engine's relocated `HOME`: these name where the user's tools keep
/// their data.
pub fn tool_homes(
    caller: &BTreeMap<String, String>,
    user_home: Option<&str>,
) -> BTreeMap<String, String> {
    let set = |name: &str| caller.get(name).filter(|v| !v.is_empty()).cloned();
    DEFAULTS
        .iter()
        .filter_map(|(name, default)| {
            let given =
                set(name).or_else(|| (*name == NPM_CACHE).then(|| set(NPM_CACHE_UPPER)).flatten());
            let value = given.or_else(|| {
                user_home
                    .filter(|h| !h.is_empty())
                    .map(|h| format!("{}/{default}", h.trim_end_matches('/')))
            })?;
            Some((name.to_string(), value))
        })
        .collect()
}

#[cfg(test)]
#[path = "tool_homes.test.rs"]
mod tests;
