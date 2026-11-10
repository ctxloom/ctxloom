//! How an engine is run, as the config resolved it (`engines.<engine>.*`,
//! owner rulings 44, 46 and 48): the program, where the engine's home is, where
//! its credential comes from, and which of the caller's variables it is given.
//!
//! Owned and secret-free: a credential is named by its SOURCE (variable names,
//! a file path), never carried as a value, so a `Debug` of these is safe to log.
//! The default is the engine's own: its built-in program, a session home of its
//! own, its built-in credential source and the curated environment.

use crate::engine::Credentials;

/// Where the engine's configuration directory is.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum SessionHome {
    /// A directory of the session's own: the user's configuration is never named.
    #[default]
    Session,
    /// The user's real directory (`session_home: host`). Nothing is linked into
    /// it and no hook of the session's is installed in it.
    Host,
}

/// Where the engine's credential comes from.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub enum AuthSetting {
    /// The engine's built-in source ([`Credentials`]).
    #[default]
    Builtin,
    /// The engine needs no credential.
    None,
    /// The human's own login is shared in place; nothing is read from the
    /// environment.
    Login,
    /// Any one of the named variables, and/or a credentials file the human's
    /// primary session alone may use.
    Source {
        env: Vec<String>,
        file: Option<String>,
    },
}

/// The resolved `engines.<engine>` settings of one launch.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct EngineSettings {
    /// The program to run; empty is the engine's own.
    pub command: String,
    pub session_home: SessionHome,
    pub auth: AuthSetting,
    /// Pass the caller's whole environment (less what a child is never given).
    pub env_host: bool,
    /// Names of the caller's variables to pass on besides the curated ones.
    pub env: Vec<String>,
}

/// Who a run authenticates as, owned: [`Credentials`] met with the configured
/// source and session home.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct CredentialPolicy {
    /// Any one of these, set and non-empty, authenticates a CHILD's run.
    pub child_env: Vec<String>,
    /// Any one of these authenticates the human's PRIMARY session.
    pub primary_env: Vec<String>,
    /// The engine's configuration directory under the user's home.
    pub user_dir: String,
    /// The login file's name in a session home, which the primary may share.
    pub login_file: Option<String>,
    /// Where the login file the primary shares is, when the config names it
    /// (`~/` is the caller's home); none is `<home>/<user_dir>/<login_file>`.
    pub login_source: Option<String>,
}

impl From<Credentials> for CredentialPolicy {
    fn from(base: Credentials) -> CredentialPolicy {
        CredentialPolicy {
            child_env: base.child_env.iter().map(ToString::to_string).collect(),
            primary_env: base.primary_env.iter().map(ToString::to_string).collect(),
            user_dir: base.user_dir.to_string(),
            login_file: base.login_file.map(ToString::to_string),
            login_source: None,
        }
    }
}

impl EngineSettings {
    /// The credential policy of `base` under these settings. A child is only
    /// ever satisfied by the environment; the primary may also share a login
    /// file, and not when the engine's home is the host's own.
    pub fn credentials(&self, base: Credentials) -> CredentialPolicy {
        CredentialPolicy::from(base)
    }
}

#[cfg(test)]
#[path = "settings.test.rs"]
mod tests;
