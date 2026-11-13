//! `engine`: list and show, each as its API resource (OD-40, the spine's worked example).

use crate::prelude::*;

pub(crate) fn read(
    cx: &Context<'_>,
    (verb, sub): (&str, &ArgMatches),
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    if verb == "show" {
        let outcome = cx.library.engine_show(&cx.flags, &text_arg(sub, "engine"));
        return resource(cx, outcome, out, err, engine, text::engine_show);
    }
    let outcome = cx.library.engine_list(&cx.flags);
    resource(
        cx,
        outcome,
        out,
        err,
        |parent, rows: &Vec<_>| engines(parent, rows),
        |out, rows| text::engine_list(out, rows),
    )
}

/// An engine's resource name under `parent`.
fn engine_name(parent: &str, engine: &str) -> String {
    format!("{parent}/engines/{engine}")
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
        command: api::text(&row.command.value, row.command.source),
        session_home: api::text(&row.session_home.value, row.session_home.source),
        runtime: api::text(&row.runtime.value, row.runtime.source),
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
    let settings = api::settings(&agent.settings);
    v1::EngineAgent {
        agent: agent.name.clone(),
        model: settings.model,
        effort: settings.effort,
        permissions: settings.permissions,
        runtime: settings.runtime,
        env: settings.env,
        session_home: None,
    }
}

#[cfg(test)]
#[path = "engine.test.rs"]
mod tests;
