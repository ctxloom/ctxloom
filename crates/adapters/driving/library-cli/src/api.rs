//! A library view as its API message (OD-40; design §6): what every noun's
//! view-to-message mapping shares. Each noun's view is mapped, in its own
//! module (`noun/<noun>.rs`, design §8.4), to its `ctxloom.v1` message
//! (library.proto), named under the parent the daemon's `ResolveProject`
//! gives (OQ-2; `users/me` outside a project, OQ-3), and printed here as the
//! message's generated proto3 JSON (OQ-21): the sourced values, an agent's
//! settings (an engine's agents, and agent and `run --dry-run` once B2
//! converts them) and a free-form document. The views stay as they are; at
//! T13 the same mapping moves into the daemon.
//!
//! Converted so far: the `engine` noun (the spine's worked example,
//! `noun/engine.rs`), `agent` and `companion` (whose writes answer
//! through [`written`]). The other nouns still print their views, until
//! their branch converts them.

use std::collections::BTreeMap;
use std::io::{self, Write};

use angzarr_presentation::Format;
use cli_support::{say_notices, Exit};
use daemon_proto::google::protobuf::{value::Kind, ListValue, Struct, Value};
use daemon_proto::v1;
use library_application::{AgentSettings, Notice, Outcome};
use serde::Serialize;

use crate::{failed, output_failure, Context, Failure, PROG};

/// Write `message`'s proto3 JSON, as every json answer is written.
pub(crate) fn print<M: serde::Serialize>(out: &mut dyn Write, message: &M) -> io::Result<()> {
    angzarr_presentation::write_json(out, message)
}

pub(crate) fn text(value: &str, source: &str) -> Option<v1::SourcedText> {
    Some(v1::SourcedText {
        value: value.to_string(),
        source: source.to_string(),
    })
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

/// A converted write's answer (OD-40; OQ-8, OQ-9): the notices, said; then
/// its error, or its json, the method's response named under the parent the
/// names give (asked only here, after the write answered), or its text, with
/// what the text noted said after it; the exit `exit` gives (a preview's
/// would-change, ruling 153).
pub(crate) fn written<T, M: Serialize>(
    cx: &Context<'_>,
    outcome: Outcome<T>,
    out: &mut dyn Write,
    err: &mut dyn Write,
    message: impl FnOnce(&str, &T) -> M,
    text: impl FnOnce(&T, &mut dyn Write, &mut Vec<String>) -> io::Result<()>,
    exit: impl FnOnce(&T) -> Exit,
) -> Result<Exit, Failure> {
    say_notices(&outcome.notices, PROG, cx.format, cx.catalog, err);
    let value = outcome.result.map_err(failed)?;
    let mut noted = Vec::new();
    if cx.format == Format::Json {
        let parent = cx.names.parent()?;
        print(out, &message(&parent, &value)).map_err(output_failure)?;
    } else {
        text(&value, out, &mut noted).map_err(output_failure)?;
    }
    let said: Vec<Notice> = noted.into_iter().map(Notice::text).collect();
    say_notices(&said, PROG, cx.format, cx.catalog, err);
    Ok(exit(&value))
}

/// What an engine cannot carry of what an agent's profiles configure.
pub(crate) fn not_carried(lost: &library_application::NotCarried) -> v1::NotCarried {
    v1::NotCarried {
        engine: lost.engine.clone(),
        surface: lost.surface.clone(),
        event: lost.event.clone(),
        reason: lost.reason.clone(),
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
pub(crate) mod tests;
