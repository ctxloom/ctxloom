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

use serde::Serialize;

use crate::error::AppError;
use crate::project::{Library, Outcome};
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
    pub fn engine_list(&self, _flags: &Flags) -> Outcome<Vec<EngineRow>> {
        Outcome {
            result: Ok(Vec::new()),
            notices: Vec::new(),
        }
    }

    /// `engine show <engine>`.
    pub fn engine_show(&self, _flags: &Flags, name: &str) -> Outcome<EngineRow> {
        Outcome {
            result: Err(AppError::UnknownEngine {
                engine: name.to_string(),
                known: String::new(),
            }),
            notices: Vec::new(),
        }
    }
}

#[cfg(test)]
#[path = "engine_noun.test.rs"]
mod tests;
