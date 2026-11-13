//! A library view as its API message (OD-40; design §6): the ONE path from
//! what library-application answers to what `--format json` prints. Each
//! noun's view is mapped here to its `ctxloom.v1` message (library.proto),
//! named under the parent the daemon's `ResolveProject` gives (OQ-2;
//! `users/me` outside a project, OQ-3), and printed as the message's
//! generated proto3 JSON (OQ-21). The views stay as they are; at T13 the
//! same mapping moves into the daemon.
//!
//! Converted so far: the `engine` noun (the spine's worked example). The
//! other nouns still print their views, until their branch converts them.

use std::collections::BTreeMap;
use std::io::{self, Write};

use daemon_proto::google::protobuf::{value::Kind, ListValue, Struct, Value};
use daemon_proto::v1;
use library_application::{AgentSettings, AuthSetting, EngineAgent, EngineRow};

/// Write `message`'s proto3 JSON, as every json answer is written.
pub(crate) fn print<M: serde::Serialize>(out: &mut dyn Write, message: &M) -> io::Result<()> {
    angzarr_presentation::write_json(out, message)
}

/// An engine's resource name under `parent`.
fn engine_name(parent: &str, engine: &str) -> String {
    format!("{parent}/engines/{engine}")
}

fn text(value: &str, source: &str) -> Option<v1::SourcedText> {
    Some(v1::SourcedText {
        value: value.to_string(),
        source: source.to_string(),
    })
}

/// `engine list`: `ListEnginesResponse`, every engine under `parent`.
pub(crate) fn engines(parent: &str, rows: &[EngineRow]) -> v1::ListEnginesResponse {
    v1::ListEnginesResponse {
        engines: rows.iter().map(|row| engine(parent, row)).collect(),
        next_page_token: String::new(),
    }
}

/// `engine show <engine>`: the `Engine` resource.
pub(crate) fn engine(parent: &str, row: &EngineRow) -> v1::Engine {
    v1::Engine {
        name: engine_name(parent, &row.name),
        command: text(&row.command.value, row.command.source),
        session_home: text(&row.session_home.value, row.session_home.source),
        runtime: text(&row.runtime.value, row.runtime.source),
        env: Some(v1::SourcedList {
            value: row.env.value.clone(),
            source: row.env.source.to_string(),
        }),
        auth: Some(auth(&row.auth)),
        agents: row.agents.iter().map(engine_agent).collect(),
    }
}

/// The auth source: its word, or its variables and file; its state the
/// enum's value.
fn auth(setting: &AuthSetting) -> v1::EngineAuth {
    let value = match &setting.value {
        serde_json::Value::String(word) => v1::AuthValue {
            word: word.clone(),
            ..v1::AuthValue::default()
        },
        serde_json::Value::Object(source) => v1::AuthValue {
            word: String::new(),
            env: source
                .get("env")
                .and_then(serde_json::Value::as_array)
                .map(|names| {
                    names
                        .iter()
                        .filter_map(|n| n.as_str().map(String::from))
                        .collect()
                })
                .unwrap_or_default(),
            file: source
                .get("file")
                .and_then(serde_json::Value::as_str)
                .unwrap_or_default()
                .to_string(),
        },
        _ => v1::AuthValue::default(),
    };
    let state = match setting.state {
        "present" => v1::engine_auth::State::Present,
        "missing" => v1::engine_auth::State::Missing,
        "not-needed" => v1::engine_auth::State::NotNeeded,
        _ => v1::engine_auth::State::Unspecified,
    };
    v1::EngineAuth {
        value: Some(value),
        source: setting.source.to_string(),
        state: state as i32,
        via: setting.via.clone().unwrap_or_default(),
    }
}

/// One agent of an engine, its settings flat (design §3.8).
fn engine_agent(agent: &EngineAgent) -> v1::EngineAgent {
    let settings = settings(&agent.settings);
    v1::EngineAgent {
        agent: agent.name.clone(),
        model: settings.model,
        effort: settings.effort,
        permissions: settings.permissions,
        runtime: settings.runtime,
        env: settings.env,
    }
}

/// What an agent resolves to on an engine. A model or effort nobody set is
/// no value (the engine's own default), its source still said.
pub(crate) fn settings(settings: &AgentSettings) -> v1::AgentSettings {
    let optional =
        |value: &Option<String>, source: &str| text(value.as_deref().unwrap_or_default(), source);
    let runtime = &settings.runtime;
    v1::AgentSettings {
        model: optional(&settings.model.value, settings.model.source),
        effort: optional(&settings.effort.value, settings.effort.source),
        permissions: Some(v1::SourcedDocument {
            value: settings.permissions.value.as_ref().map(document),
            source: settings.permissions.source.to_string(),
        }),
        runtime: Some(v1::RuntimeSetting {
            value: runtime.value.clone(),
            source: runtime.source.to_string(),
            overrides: runtime.overrides.clone().unwrap_or_default(),
            overrides_source: runtime.overrides_source.unwrap_or_default().to_string(),
            change: runtime.change.unwrap_or_default().to_string(),
        }),
        env: Some(v1::EnvSetting {
            value: settings.env.value.clone(),
            source: settings.env.source.to_string(),
            keys: settings
                .env
                .keys
                .iter()
                .map(|key| v1::EnvKey {
                    variable: key.name.clone(),
                    source: key.source.to_string(),
                })
                .collect(),
        }),
    }
}

/// A free-form document as a `google.protobuf.Value` (OQ-11, owner ruling
/// 2026-11-13): whatever was written, an object or not, for display.
pub(crate) fn document(json: &serde_json::Value) -> Value {
    value(json)
}

/// A JSON value as a `google.protobuf.Value` (numbers as doubles, OQ-11).
fn value(json: &serde_json::Value) -> Value {
    let kind = match json {
        serde_json::Value::Null => Kind::NullValue(0),
        serde_json::Value::Bool(b) => Kind::BoolValue(*b),
        serde_json::Value::Number(n) => Kind::NumberValue(n.as_f64().unwrap_or_default()),
        serde_json::Value::String(s) => Kind::StringValue(s.clone()),
        serde_json::Value::Array(items) => Kind::ListValue(ListValue {
            values: items.iter().map(value).collect(),
        }),
        serde_json::Value::Object(fields) => Kind::StructValue(Struct {
            fields: fields
                .iter()
                .map(|(key, field)| (key.clone(), value(field)))
                .collect::<BTreeMap<_, _>>(),
        }),
    };
    Value { kind: Some(kind) }
}

#[cfg(test)]
#[path = "api.test.rs"]
mod tests;
