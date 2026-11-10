//! `engine list|show <engine>`: the read-only noun of the programs ctxloom
//! drives, each with the settings its agents will use and the layer that said
//! each.

/// The engines a person is told of: the public set, in name order.
#[cfg(not(feature = "test-support"))]
pub(crate) fn listed() -> Vec<&'static str> {
    crate::agents::ENGINES
        .iter()
        .copied()
        .filter(|engine| !engine.starts_with("mock-"))
        .collect()
}

/// The engines a person is told of: every one, the test engines
/// (`mock-launch`, `mock-lossy`, `mock-noskills`) included under
/// `test-support`.
#[cfg(feature = "test-support")]
pub(crate) fn listed() -> Vec<&'static str> {
    crate::agents::ENGINES.to_vec()
}

use library::config::engines::{
    self, AuthSource, AuthState, Change, Effective, Override, Source, Sourced,
};
use serde::Serialize;

use crate::agents::engine_error;
use crate::error::AppError;
use crate::project::{Library, Outcome, Project};
use crate::Flags;

/// A value and the layer that said it: `agent`, `engine`, `project` or
/// `default`.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct SourcedValue<T> {
    pub value: T,
    pub source: &'static str,
}

/// The runtime an agent runs in, and what its own replaced. An override is
/// never silent: `overrides` is the value that would have stood, whose layer
/// said it (`overrides_source`), and `change` how they differ (`weakens`,
/// `strengthens`, `ownership-swap`).
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct RuntimeSetting {
    pub value: String,
    pub source: &'static str,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub overrides: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub overrides_source: Option<&'static str>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub change: Option<&'static str>,
}

/// What an agent runs with, each setting with the layer that said it. A model
/// or an effort nobody set is `null`: the engine's own default, no flag is
/// passed.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct AgentSettings {
    pub model: SourcedValue<Option<String>>,
    pub effort: SourcedValue<Option<String>>,
    pub permissions: SourcedValue<String>,
    pub runtime: RuntimeSetting,
    pub env_host: SourcedValue<bool>,
    pub env: SourcedValue<Vec<String>>,
}

/// One agent bound to an engine, with its settings.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct EngineAgent {
    pub name: String,
    #[serde(flatten)]
    pub settings: AgentSettings,
}

/// Where an engine's credential comes from and whether it is there. The
/// source is a name or a path, never a value.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct AuthSetting {
    /// `none`, `login`, or `{env: [NAME..], file: PATH}`.
    pub value: serde_json::Value,
    pub source: &'static str,
    /// `present`, `missing` or `not-needed`.
    pub state: &'static str,
    /// The variable's name or the file's path that authenticates.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub via: Option<String>,
}

/// One engine: what its agents share, and what each agent of it resolves to.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct EngineRow {
    pub name: String,
    pub command: SourcedValue<String>,
    pub session_home: SourcedValue<String>,
    pub runtime: SourcedValue<String>,
    pub env_host: SourcedValue<bool>,
    pub env: SourcedValue<Vec<String>>,
    pub auth: AuthSetting,
    pub agents: Vec<EngineAgent>,
}

impl Library {
    /// `engine list`: the engines ctxloom drives.
    pub fn engine_list(&self, flags: &Flags) -> Outcome<Vec<EngineRow>> {
        self.run(flags, |p| {
            listed()
                .into_iter()
                .map(|engine| p.engine_row(engine))
                .collect()
        })
    }

    /// `engine show <engine>`.
    pub fn engine_show(&self, flags: &Flags, name: &str) -> Outcome<EngineRow> {
        self.run(flags, |p| {
            if !listed().contains(&name) {
                return Err(AppError::UnknownEngine {
                    engine: name.to_string(),
                    known: listed().join(", "),
                });
            }
            p.engine_row(name)
        })
    }
}

impl Project {
    fn engine_row(&self, engine: &str) -> Result<EngineRow, AppError> {
        let shared = engines::engine_settings(&self.config.value, engine).map_err(engine_error)?;
        let mut agents = Vec::new();
        for agent in self.config.agents() {
            let effective = self.effective_of(&agent)?;
            if effective.engine.value == engine {
                agents.push(EngineAgent {
                    name: agent.name,
                    settings: settings_of(&effective),
                });
            }
        }
        Ok(EngineRow {
            name: engine.to_string(),
            command: sourced(&shared.command),
            session_home: sourced(&shared.session_home),
            runtime: sourced(&shared.runtime),
            env_host: sourced(&shared.env_host),
            env: sourced(&shared.env),
            auth: self.auth_setting(&shared.auth),
            agents,
        })
    }

    /// Whether the source is there, by the environment the process has (and,
    /// for a file only the human's own session may share, the user's files).
    /// Never a value.
    fn auth_setting(&self, auth: &Sourced<AuthSource>) -> AuthSetting {
        let exported = |name: &str| {
            self.environ
                .iter()
                .any(|(key, value)| key == name && !value.is_empty())
        };
        let file_present = |path: &str| {
            let home = self
                .environ
                .iter()
                .find(|(key, _)| key == "HOME")
                .map(|(_, value)| value.as_str());
            let path = match (path.strip_prefix("~/"), home) {
                (Some(rest), Some(home)) => format!("{home}/{rest}"),
                _ => path.to_string(),
            };
            self.ports
                .as_ref()
                .is_some_and(|ports| ports.user.exists(&path).unwrap_or(false))
        };
        let (state, via) = match auth.value.status(true, &exported, &file_present) {
            AuthState::NotNeeded => ("not-needed", None),
            AuthState::Present { via } => ("present", Some(via)),
            AuthState::Missing => ("missing", None),
        };
        AuthSetting {
            value: auth_value(&auth.value),
            source: auth.source.word(),
            state,
            via,
        }
    }
}

fn auth_value(source: &AuthSource) -> serde_json::Value {
    match source {
        AuthSource::None => "none".into(),
        AuthSource::Login => "login".into(),
        AuthSource::Token => "token".into(),
        AuthSource::Credential { env, file } => {
            let mut map = serde_json::Map::new();
            map.insert("env".to_string(), env.clone().into());
            if let Some(file) = file {
                map.insert("file".to_string(), file.clone().into());
            }
            map.into()
        }
    }
}

fn sourced<T: Clone>(from: &Sourced<T>) -> SourcedValue<T> {
    SourcedValue {
        value: from.value.clone(),
        source: from.source.word(),
    }
}

/// What the agent runs with, as `agent show` and `engine show` print it.
pub(crate) fn settings_of(effective: &Effective) -> AgentSettings {
    let optional = |setting: &Option<Sourced<String>>| match setting {
        Some(set) => SourcedValue {
            value: Some(set.value.clone()),
            source: set.source.word(),
        },
        None => SourcedValue {
            value: None,
            source: Source::Default.word(),
        },
    };
    let mut runtime = RuntimeSetting {
        value: effective.runtime.value.clone(),
        source: effective.runtime.source.word(),
        overrides: None,
        overrides_source: None,
        change: None,
    };
    if let Override::Replaced { replaced, change } = &effective.runtime_override {
        runtime.overrides = Some(replaced.value.clone());
        runtime.overrides_source = Some(replaced.source.word());
        runtime.change = Some(change_word(*change));
    }
    AgentSettings {
        model: optional(&effective.model),
        effort: optional(&effective.effort),
        permissions: sourced(&effective.permissions),
        runtime,
        env_host: sourced(&effective.env_host),
        env: sourced(&effective.env),
    }
}

/// The word the JSON carries for how a runtime differs from the one it
/// replaced.
pub(crate) fn change_word(change: Change) -> &'static str {
    match change {
        Change::Weakens => "weakens",
        Change::Strengthens => "strengthens",
        Change::OwnershipSwap => "ownership-swap",
    }
}

#[cfg(test)]
#[path = "engine_noun.test.rs"]
mod tests;
