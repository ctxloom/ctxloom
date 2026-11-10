//! RED (awaiting review): the engines block and an agent's effective settings.
//!
//! Owner rulings 40-44 (2026-11-09): `model`, `effort` and `permissions` are an
//! AGENT's settings; the `engines.<engine>` block holds what is cross-agent:
//! `command`, `session_home`, `auth`, and the default `runtime` / `env_host` /
//! `env` an agent may override. An unset `model` / `effort` passes no flag;
//! `permissions` has an explicit ctxloom default. Everything here is a pure
//! function of the merged config tree: [`effective`] answers "what does this
//! agent run with, and which layer said so", [`runtime_change`] is the
//! isolation guard, [`AuthSource`] is where an engine's credential comes from
//! (a source, never a value).
//!
//! The functions below are STUBS that answer inertly, so the tests beside them
//! fail by assertion; the proposal (`engines-schema.proposal.md`) says what
//! each is to do.

use angzarr_yaml::Value;

/// Which layer said a value.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Source {
    /// The agent binding (`agents.<name>.<key>`).
    Agent,
    /// The engine block (`engines.<engine>.<key>`).
    Engine,
    /// The project-wide key (`runtime:`).
    Project,
    /// Nothing said it: ctxloom's built-in.
    Default,
}

impl Source {
    /// The word `engine show` and `agent show` print: `agent`, `engine`,
    /// `project` or `default`.
    pub fn word(self) -> &'static str {
        ""
    }
}

/// A value and the layer that said it.
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct Sourced<T> {
    pub value: T,
    pub source: Source,
}

/// How a runtime differs from the one another layer would have given.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Change {
    /// A container runtime replaced by the host: nothing contains the engine.
    Weakens,
    /// The host replaced by a container.
    Strengthens,
    /// `container-rootless` and `container-rootful` swapped: the two are not
    /// interchangeable (who owns the daemon), so this is neither stronger nor
    /// weaker, and is never a fallback.
    OwnershipSwap,
}

/// The isolation guard: what moving from runtime `from` to runtime `to` is,
/// `None` when they are the same. Pure; names are
/// `host | container-rootless | container-rootful`.
pub fn runtime_change(from: &str, to: &str) -> Option<Change> {
    let _ = (from, to);
    None
}

/// What the agent did to the runtime the lower layers would have given.
#[derive(Clone, PartialEq, Eq, Debug)]
pub enum Override {
    /// The agent said nothing: the engine default (else the project key, else
    /// `host`) stands.
    Inherited,
    /// The agent said exactly what the lower layers say.
    Restated,
    /// The agent said something else. `replaced` is what would have stood
    /// (with the layer that said it); `change` is how the two differ. Shown
    /// loudly by `agent show` / `engine show`, never silent, never merged.
    Replaced {
        replaced: Sourced<String>,
        change: Change,
    },
}

/// Where an engine's credential comes from. A SOURCE, never a secret.
#[derive(Clone, PartialEq, Eq, Debug)]
pub enum AuthSource {
    /// The engine needs no credential (`auth: none`).
    None,
    /// `auth: login`: the human's own login is shared in place; nothing is
    /// read from the environment.
    Login,
    /// `auth: token`: the engine's built-in variables (resolved by
    /// [`effective`] to [`AuthSource::builtin`]).
    Token,
    /// Any one of the named variables, non-empty, authenticates; and/or a
    /// credentials file, which only the human's own primary session may share.
    Credential {
        env: Vec<String>,
        file: Option<String>,
    },
}

/// Why an `auth` value is refused. It never carries the offending text: a
/// secret written where a name belongs must not be echoed back.
#[derive(Clone, PartialEq, Eq, Debug)]
pub enum AuthError {
    /// `auth:` is neither `none` nor a mapping.
    Shape,
    /// A key other than `env` and `file`.
    UnknownKey { key: String },
    /// A mapping with neither `env` nor `file`.
    Empty,
    /// The `index`th `env` entry is not an environment variable NAME
    /// (`[A-Za-z_][A-Za-z0-9_]*`): a value, or a flag, was written there.
    NotAName { index: usize },
    /// `file` is empty.
    EmptyFile,
}

/// What `auth status` / doctor find of a source.
#[derive(Clone, PartialEq, Eq, Debug)]
pub enum AuthState {
    /// `auth: none`.
    NotNeeded,
    /// Present, and through which source (the variable's name or the file's
    /// path, never a value).
    Present { via: String },
    /// Nothing the run may use is there.
    Missing,
}

impl AuthSource {
    /// Read the value of an `auth:` key.
    pub fn parse(value: &Value) -> Result<AuthSource, AuthError> {
        let _ = value;
        Ok(AuthSource::None)
    }

    /// The built-in source of `engine`: claude-code takes
    /// `CLAUDE_CODE_OAUTH_TOKEN` or `ANTHROPIC_API_KEY`; the mocks need none.
    pub fn builtin(engine: &str) -> AuthSource {
        let _ = engine;
        AuthSource::None
    }

    /// Whether the source is satisfied. `exported(name)` says a variable is
    /// set and non-empty; `file_present(path)` says the file exists. A CHILD
    /// agent is satisfied by `env` alone (never a shared file); the human's
    /// PRIMARY session also by `file`.
    pub fn status(
        &self,
        for_primary: bool,
        exported: &dyn Fn(&str) -> bool,
        file_present: &dyn Fn(&str) -> bool,
    ) -> AuthState {
        let _ = (for_primary, exported, file_present);
        AuthState::NotNeeded
    }
}

/// Why an agent's settings cannot be resolved.
#[derive(Clone, PartialEq, Eq, Debug)]
pub enum EngineError {
    /// No `agents.<name>`.
    AgentNotFound { name: String },
    /// The agent (or the default) names an engine this build does not know.
    UnknownEngine { engine: String, known: Vec<String> },
    /// The engine block's `auth` is refused.
    Auth { engine: String, why: AuthError },
}

/// An agent's settings as they will be used, each with the layer that said it.
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct Effective {
    pub agent: String,
    pub engine: Sourced<String>,
    /// `None` is the engine's own default: no flag is passed.
    pub model: Option<Sourced<String>>,
    /// `None` is the engine's own default: no flag is passed.
    pub effort: Option<Sourced<String>>,
    /// The permission mode in the engine's vocabulary; never unset.
    pub permissions: Sourced<String>,
    pub runtime: Sourced<String>,
    pub runtime_override: Override,
    pub env_host: Sourced<bool>,
    pub env: Sourced<Vec<String>>,
    pub command: Sourced<String>,
    pub session_home: Sourced<String>,
    pub auth: Sourced<AuthSource>,
}

/// The settings agent `agent` runs with, over the merged config `config`.
pub fn effective(config: &Value, agent: &str) -> Result<Effective, EngineError> {
    let _ = config;
    let default = |value: &str| Sourced {
        value: value.to_string(),
        source: Source::Default,
    };
    Ok(Effective {
        agent: agent.to_string(),
        engine: default("claude-code"),
        model: None,
        effort: None,
        permissions: default(""),
        runtime: default("host"),
        runtime_override: Override::Inherited,
        env_host: Sourced {
            value: false,
            source: Source::Default,
        },
        env: Sourced {
            value: Vec::new(),
            source: Source::Default,
        },
        command: default(""),
        session_home: default(""),
        auth: Sourced {
            value: AuthSource::None,
            source: Source::Default,
        },
    })
}

#[cfg(test)]
#[path = "engines.test.rs"]
mod tests;
