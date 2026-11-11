//! The git identity a session home is seeded with (owner ruling 112).
//!
//! An engine's `HOME` is its session home, so git, run by the engine's tools,
//! reads its global configuration from `<session home>/.gitconfig` and none of
//! the user's own. A commit there would have no author. The home is seeded
//! with the user's identity alone: the `[user]` `name` and `email` git
//! resolves for her in the project, and nothing else of her configuration
//! (no signing key, credential helper or alias crosses, so a `gpgsign` that
//! needs her keyring cannot break a commit). A file, not `GIT_AUTHOR_*`
//! variables: the environment would outrank a repository's own `user`, and the
//! file sits below it, as her global configuration does. Pure.

/// The user's git identity; either part may be unknown (empty).
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct GitUser {
    pub name: String,
    pub email: String,
}

/// Where the identity is written, relative to the session home.
pub const GIT_CONFIG_FILE: &str = ".gitconfig";

/// The `.gitconfig` that carries `user`, none when it knows neither part.
pub fn git_config(user: &GitUser) -> Option<String> {
    let _ = user;
    None
}

#[cfg(test)]
#[path = "git.test.rs"]
mod tests;
