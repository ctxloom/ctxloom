//! The layered configuration, loaded: the layers read, judged against the
//! schema, merged, overridden and given the shipped engine registry; and the
//! typed reading of what it says.

use angzarr_yaml::Value;
use ctxloom_ports::{ConfigFiles, LayerText, RegisteredCompanion, Scope, Source};
use library::config::engines;
use library::config::layer::{self, Note};
use library::config::overrides;
use library::config::schema::{self, Violation};
use library::config::CURRENT;
use library::ConfigError;

use crate::error::{AppError, Notice};
use crate::views::{AgentRow, McpRow};

/// The flags and variables that bear on the configuration.
#[derive(Clone, Debug, Default)]
pub struct Flags {
    /// `--degraded` (or `CTXLOOM_DEGRADED=1`).
    pub degraded: bool,
    /// Every `--config-set <path>=<value>`.
    pub config_set: Vec<String>,
    /// `--no-companions`.
    pub no_companions: bool,
}

/// The configuration, read.
#[derive(Clone, Debug)]
pub struct Config {
    pub value: Value,
    /// What the files and overrides set, without the shipped registry.
    pub raw: Value,
    pub source: Source,
}

/// Load the configuration: the layers, the environment's overrides, the
/// flags' overrides, and the shipped registry under what they leave.
pub fn load(
    files: &dyn ConfigFiles,
    environ: &[(String, String)],
    flags: &Flags,
    notices: &mut Vec<Notice>,
) -> Result<Config, AppError> {
    if let Some(root) = files.ignored_root() {
        notices.push(Notice::RootIgnored { root });
    }
    let layers = files.layers().map_err(AppError::Cause)?;
    let project_path = files.project_layer_path();
    let mut violations: Vec<String> = Vec::new();
    let mut shape_failure = false;
    let mut values = Vec::new();
    for layer in &layers {
        let mut value = read_layer(layer, notices)?;
        if project_path.as_deref() == Some(layer.path.as_str()) {
            for key in engines::strip_project_only(&mut value) {
                notices.push(Notice::text(format!(
                    "{key} in {} is ignored: a project's config cannot set it (ruling 46); put it in your home config ({})",
                    layer.path,
                    files.layer_path(Scope::Home)
                )));
            }
        }
        let found = judged(layer, &value, notices);
        shape_failure |= found.iter().any(Violation::is_shape);
        if flags.degraded {
            drop_unknown_keys(&mut value, &found);
        }
        violations.extend(found.into_iter().map(|v| v.text(&layer.path)));
        drop_engineless_agents(&mut value, &layer.path, notices);
        values.push(value);
    }
    if !violations.is_empty() && !flags.degraded {
        return Err(refused(&violations));
    }
    let merged = layer::merge_layers(&values);
    let merged = override_with(merged, environ, flags, notices);
    let mut merged = reduced_when_unreadable(merged, shape_failure);
    stamp(&mut merged);
    let raw = merged.clone();
    Ok(Config {
        value: merged,
        raw,
        source: files.source(),
    })
}

/// The path of the project's own config file, when there is a project and it
/// is a file apart from the home's.
fn read_layer(layer: &LayerText, notices: &mut Vec<Notice>) -> Result<Value, AppError> {
    let read = layer::read(&layer.text).map_err(|e| match e {
        ConfigError::Syntax { cause } => AppError::ConfigUnparsable {
            path: layer.path.clone(),
            cause: Box::new(cause),
        },
        other => AppError::cause(other),
    })?;
    let path = layer.path.clone();
    for note in read.notes {
        match note {
            // D15, owner override of Go (which warned and read on): a layer
            // of another generation is refused.
            Note::Newer { found, current } => {
                return Err(AppError::ConfigNewer {
                    path,
                    found,
                    current,
                })
            }
            Note::Older { found, oldest } => {
                return Err(AppError::ConfigOlder {
                    path,
                    found,
                    oldest,
                })
            }
            // Registered by name alone: refused, never migrated into a
            // registration (a name says nothing of what to run).
            Note::LegacyCompanions { found, names } => {
                return Err(AppError::ConfigCompanionsBare {
                    path,
                    found,
                    names: names.join(", "),
                })
            }
            Note::NotInteger => return Err(AppError::ConfigVersionUnreadable { path }),
            Note::Keyless => notices.push(Notice::ConfigKeyless { path: path.clone() }),
        }
    }
    Ok(read.value)
}

/// A layer's violations of the schema, each also said as a warning.
fn judged(layer: &LayerText, value: &Value, notices: &mut Vec<Notice>) -> Vec<Violation> {
    let found = schema::validate(value);
    for violation in &found {
        notices.push(Notice::Finding {
            text: violation.text(&layer.path),
            remedy: remedy_of(violation).to_string(),
        });
    }
    found
}

fn remedy_of(violation: &Violation) -> &'static str {
    match violation {
        Violation::UnknownKey { .. } => {
            "remove or rename the key in config.yaml (ctxloom config edit)"
        }
        _ => "give the key a value the schema accepts, or remove it (ctxloom config edit)",
    }
}

fn refused(violations: &[String]) -> AppError {
    let lines: String = violations
        .iter()
        .map(|v| format!("\n  - [config] {v}"))
        .collect();
    if violations
        .first()
        .is_some_and(|v| v.starts_with("unknown key"))
    {
        AppError::ConfigUnknownKey { violations: lines }
    } else {
        AppError::ConfigBreaksSchema { violations: lines }
    }
}

/// `--degraded`: a key the schema does not know is dropped.
fn drop_unknown_keys(value: &mut Value, found: &[Violation]) {
    for violation in found {
        if let Violation::UnknownKey { path, .. } = violation {
            let keys: Vec<&str> = path.split('.').collect();
            remove_at(value, &keys);
        }
    }
}

fn remove_at(value: &mut Value, keys: &[&str]) {
    match keys {
        [] => {}
        [last] => {
            value.remove(last);
        }
        [first, rest @ ..] => {
            if let Some(child) = value.get_mut(first) {
                remove_at(child, rest);
            }
        }
    }
}

/// Go drops an agent that declares neither an engine nor a profile, and says so.
fn drop_engineless_agents(value: &mut Value, path: &str, notices: &mut Vec<Notice>) {
    let Some(Value::Map(agents)) = value.get_mut("agents") else {
        return;
    };
    let mut names: Vec<String> = agents.iter().map(|(name, _)| name.clone()).collect();
    names.sort();
    for name in names {
        let body = agents.iter().find(|(n, _)| *n == name).map(|(_, v)| v);
        let binds = body.is_some_and(|body| {
            body.get("engine")
                .and_then(Value::as_str)
                .is_some_and(|s| !s.is_empty())
                || body
                    .get("profiles")
                    .and_then(Value::as_seq)
                    .is_some_and(|p| !p.is_empty())
        });
        if !binds {
            agents.retain(|(n, _)| *n != name);
            notices.push(Notice::text(format!(
                "agents.{name} in {path} declares neither an engine nor profiles, so nothing could resolve an engine for it — an agent bound to nothing is not an agent; the entry is ignored"
            )));
        }
    }
    if agents.is_empty() {
        value.remove("agents");
    }
}

/// The environment's and the flags' overrides laid over `merged`.
fn override_with(
    merged: Value,
    environ: &[(String, String)],
    flags: &Flags,
    notices: &mut Vec<Notice>,
) -> Value {
    let (found, problems) = overrides::read(environ, &flags.config_set);
    for problem in problems {
        notices.push(Notice::text(format!(
            "config override resolution: {problem}"
        )));
    }
    if found.env.is_empty() && found.flags.is_empty() {
        return merged;
    }
    let known = |path: &[String]| schema::known_path(path);
    let validate = |path: &[String], value: &Value| schema::validate_at(path, value);
    let applied = overrides::apply(&merged, &found, &known, &validate);
    for error in applied.errors {
        notices.push(Notice::text(format!("config override resolution: {error}")));
    }
    for warning in applied.warnings {
        notices.push(Notice::text(warning));
    }
    applied.value
}

/// An `agents.<name>` binding.
#[derive(Clone, Debug)]
pub struct Agent {
    pub name: String,
    /// The engine the binding names; empty is the default (`claude-code`).
    pub engine: String,
    pub model: String,
    pub effort: String,
    pub profiles: Vec<String>,
    pub runtime: String,
    pub permissions: Value,
    pub driving: String,
    pub env_host: Option<bool>,
    pub env: Vec<String>,
}

fn text_at(value: &Value, key: &str) -> String {
    value
        .get(key)
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_string()
}

fn list_at(value: &Value, key: &str) -> Vec<String> {
    value
        .get(key)
        .and_then(Value::as_seq)
        .map(|items| {
            items
                .iter()
                .filter_map(|i| i.as_str().map(ToString::to_string))
                .collect()
        })
        .unwrap_or_default()
}

impl Config {
    /// The text at a dotted path, empty where there is none.
    pub fn text(&self, path: &str) -> String {
        let mut here = Some(&self.value);
        for key in path.split('.') {
            here = here.and_then(|v| v.get(key));
        }
        here.and_then(Value::as_str).unwrap_or_default().to_string()
    }

    /// The agent bindings, by name.
    pub fn agents(&self) -> Vec<Agent> {
        let Some(Value::Map(agents)) = self.value.get("agents") else {
            return Vec::new();
        };
        let mut out: Vec<Agent> = agents
            .iter()
            .map(|(name, body)| Agent::of(name, body))
            .collect();
        out.sort_by(|a, b| a.name.cmp(&b.name));
        out
    }

    /// The agent named `name`.
    pub fn agent(&self, name: &str) -> Option<Agent> {
        self.agents().into_iter().find(|a| a.name == name)
    }

    pub fn default_agent(&self) -> String {
        self.text("default_agent")
    }

    /// The profiles of the default agent: what a bare run binds.
    pub fn default_agent_profiles(&self) -> Vec<String> {
        self.agent(&self.default_agent())
            .map(|a| a.profiles)
            .unwrap_or_default()
    }

    /// Whether distilled forms are preferred (`config.use_distilled`, on
    /// unless turned off).
    pub fn use_distilled(&self) -> bool {
        self.text("config.use_distilled") != "false"
    }

    /// Whether ctxloom manages the HUD statusline (`config.statusline`, on
    /// unless turned off).
    pub fn statusline(&self) -> bool {
        self.text("config.statusline") != "false"
    }

    /// The engines the project's agents are bound to, each once; claude-code,
    /// the default, when there is no agent.
    pub fn configured_engines(&self) -> Vec<String> {
        library::config::engines::engines_in_use(&self.value)
    }

    /// The registered companions, by name: each with the command that runs
    /// it (empty where the registration holds none).
    pub fn companions(&self) -> Vec<RegisteredCompanion> {
        let Some(Value::Map(registered)) = self.value.get("companions") else {
            return Vec::new();
        };
        let mut all: Vec<RegisteredCompanion> = registered
            .iter()
            .map(|(name, body)| RegisteredCompanion {
                name: name.clone(),
                command: list_at(body, "command"),
            })
            .collect();
        all.sort_by(|a, b| a.name.cmp(&b.name));
        all
    }
}

impl Agent {
    /// The binding `name` as its mapping declares it.
    pub fn of(name: &str, body: &Value) -> Agent {
        Agent {
            name: name.to_string(),
            engine: text_at(body, "engine"),
            model: text_at(body, "model"),
            effort: text_at(body, "effort"),
            profiles: list_at(body, "profiles"),
            runtime: text_at(body, "runtime"),
            permissions: body.get("permissions").cloned().unwrap_or(Value::Null),
            driving: text_at(body, "driving"),
            env_host: body
                .get("env_host")
                .and_then(Value::as_str)
                .map(|s| s == "true"),
            env: list_at(body, "env"),
        }
    }

    /// The binding as the listing shows it.
    pub fn row(&self) -> AgentRow {
        AgentRow {
            name: self.name.clone(),
            engine: self.engine.clone(),
            model: self.model.clone(),
            effort: self.effort.clone(),
            profiles: self.profiles.clone(),
            runtime: self.runtime.clone(),
            permissions: crate::json::permissions(&self.permissions),
            driving: self.driving.clone(),
            env_host: self.env_host,
            env: self.env.clone(),
            permissions_line: crate::json::permissions_line(&self.permissions),
        }
    }
}

/// An MCP server of a bundle, as a row.
pub fn mcp_row(name: &str, server: &library::model::McpServer, source: &str) -> McpRow {
    McpRow {
        name: name.to_string(),
        command: server.command.clone(),
        args: server.args.clone(),
        env: server.env.clone(),
        url: server.url.clone(),
        served_by: server.served_by.clone(),
        source: source.to_string(),
        notes: server.notes.clone(),
        installation: server.installation.clone(),
    }
}

/// The configuration as the model reads it fails to decode where a value
/// holds the wrong shape: it is then the defaults alone.
fn reduced_when_unreadable(merged: Value, shape_failure: bool) -> Value {
    let overridden_shape = schema::validate(&merged).iter().any(Violation::is_shape);
    if shape_failure || overridden_shape {
        return Value::Map(Vec::new());
    }
    merged
}

/// The document says the generation this build writes.
fn stamp(value: &mut Value) {
    if !matches!(value, Value::Map(_)) {
        *value = Value::Map(Vec::new());
    }
    *value.entry("schema_version") = Value::Scalar(CURRENT.to_string());
}

#[cfg(test)]
#[path = "config.test.rs"]
mod tests;
