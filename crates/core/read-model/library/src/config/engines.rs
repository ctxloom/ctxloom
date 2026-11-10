//! The engines block and an agent's effective settings.
//!
//! Owner rulings 40-44 (2026-11-09): `model`, `effort` and `permissions` are an
//! AGENT's settings; the `engines.<engine>` block holds what is cross-agent:
//! `command`, `session_home`, `auth`, and the default `runtime` /
//! `env` an agent may override. An unset `model` / `effort` passes no flag;
//! `permissions` has an explicit ctxloom default. Everything here is a pure
//! function of the merged config tree: [`effective`] answers "what does this
//! agent run with, and which layer said so", [`runtime_change`] is the
//! isolation guard, [`AuthSource`] is where an engine's credential comes from
//! (a source, never a value).
//!
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
        match self {
            Source::Agent => "agent",
            Source::Engine => "engine",
            Source::Project => "project",
            Source::Default => "default",
        }
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
    match (from == HOST, to == HOST) {
        _ if from == to => None,
        (false, true) => Some(Change::Weakens),
        (true, false) => Some(Change::Strengthens),
        _ => Some(Change::OwnershipSwap),
    }
}

const HOST: &str = "host";

/// Whether `name` is an environment variable name:
/// `[A-Za-z_][A-Za-z0-9_]*`.
pub fn is_env_name(name: &str) -> bool {
    let mut chars = name.chars();
    chars
        .next()
        .is_some_and(|c| c.is_ascii_alphabetic() || c == '_')
        && chars.all(|c| c.is_ascii_alphanumeric() || c == '_')
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
        let entries = match value {
            Value::Scalar(word) => {
                return match word.as_str() {
                    "none" => Ok(AuthSource::None),
                    "login" => Ok(AuthSource::Login),
                    "token" => Ok(AuthSource::Token),
                    _ => Err(AuthError::Shape),
                }
            }
            Value::Map(entries) => entries,
            _ => return Err(AuthError::Shape),
        };
        if let Some((key, _)) = entries.iter().find(|(k, _)| k != "env" && k != "file") {
            return Err(AuthError::UnknownKey { key: key.clone() });
        }
        if entries.is_empty() {
            return Err(AuthError::Empty);
        }
        let mut env = Vec::new();
        if let Some(named) = value.get("env") {
            let one = [named.clone()];
            let names = match named {
                Value::Seq(names) => names.as_slice(),
                _ => &one,
            };
            for (index, name) in names.iter().enumerate() {
                match name.as_str() {
                    Some(name) if is_env_name(name) => env.push(name.to_string()),
                    _ => return Err(AuthError::NotAName { index }),
                }
            }
        }
        let file = match value.get("file") {
            None => None,
            Some(path) => match path.as_str() {
                Some(path) if !path.is_empty() => Some(path.to_string()),
                _ => return Err(AuthError::EmptyFile),
            },
        };
        Ok(AuthSource::Credential { env, file })
    }

    /// The built-in source of `engine`: claude-code takes
    /// `CLAUDE_CODE_OAUTH_TOKEN` or `ANTHROPIC_API_KEY`; opencode the keys of
    /// its Anthropic and OpenAI providers (owner rulings 69 and 79; each
    /// optional, see [`AuthSource::required`]); the mocks need none.
    pub fn builtin(engine: &str) -> AuthSource {
        let env = match engine {
            "claude-code" => vec!["CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"],
            "opencode" => vec!["ANTHROPIC_API_KEY", "OPENAI_API_KEY"],
            _ => return AuthSource::None,
        };
        AuthSource::Credential {
            env: env.into_iter().map(ToString::to_string).collect(),
            file: None,
        }
    }

    /// Whether a run is refused for lacking the credential of `engine`'s
    /// built-in source: opencode's keys are optional (a keyless run, on its
    /// free models, is allowed); every other source is required. Not
    /// user-visible.
    pub fn required(engine: &str) -> bool {
        engine != "opencode"
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
        match self {
            AuthSource::None | AuthSource::Login => AuthState::NotNeeded,
            // `token` is spelled out by `effective`; unresolved it names
            // nothing to find.
            AuthSource::Token => AuthState::Missing,
            AuthSource::Credential { env, file } => {
                if let Some(name) = env.iter().find(|name| exported(name)) {
                    return AuthState::Present { via: name.clone() };
                }
                match file {
                    Some(path) if for_primary && file_present(path) => {
                        AuthState::Present { via: path.clone() }
                    }
                    _ => AuthState::Missing,
                }
            }
        }
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
    pub env: Sourced<Vec<String>>,
    pub command: Sourced<String>,
    pub session_home: Sourced<String>,
    pub auth: Sourced<AuthSource>,
}

/// The settings agent `agent` runs with on its own engine, over the merged
/// config `config`.
pub fn effective(config: &Value, agent: &str) -> Result<Effective, EngineError> {
    resolve(config, agent, None)
}

/// The settings agent `agent` runs with on `engine` (`run --engine`, owner
/// ruling 57): its block for that engine (else the engine's defaults: no
/// model, no effort, the engine's permission default), its own runtime and
/// env (engine-neutral), and that engine's block for the rest.
pub fn effective_on(config: &Value, agent: &str, engine: &str) -> Result<Effective, EngineError> {
    resolve(config, agent, Some(engine))
}

/// The agent's own block for `engine` (`agents.<agent>.engines.<engine>`).
fn agent_block<'a>(binding: &'a Value, engine: &str) -> Option<&'a Value> {
    binding.get("engines").and_then(|blocks| blocks.get(engine))
}

/// The engines agent `agent` has a block for, in the config's order.
pub fn agent_blocks(config: &Value, agent: &str) -> Vec<String> {
    match config
        .get("agents")
        .and_then(|agents| agents.get(agent))
        .and_then(|binding| binding.get("engines"))
    {
        Some(Value::Map(blocks)) => blocks.iter().map(|(name, _)| name.clone()).collect(),
        _ => Vec::new(),
    }
}

fn resolve(config: &Value, agent: &str, on: Option<&str>) -> Result<Effective, EngineError> {
    let binding = config
        .get("agents")
        .and_then(|agents| agents.get(agent))
        .ok_or_else(|| EngineError::AgentNotFound {
            name: agent.to_string(),
        })?;
    // An engine asked for is the run's (said as the agent's); the default
    // engine asked for by an agent that names none is still the default.
    let engine = match (on, text_of(binding, "engine")) {
        (Some(asked), None) if asked == DEFAULT_ENGINE => default(asked.to_string()),
        (Some(asked), _) => agent_said(asked.to_string()),
        (None, Some(name)) => agent_said(name),
        (None, None) => default(DEFAULT_ENGINE.to_string()),
    };
    if !ENGINES.contains(&engine.value.as_str()) {
        return Err(EngineError::UnknownEngine {
            engine: engine.value,
            known: ENGINES.iter().map(ToString::to_string).collect(),
        });
    }
    let name = engine.value.clone();
    let block = config.get("engines").and_then(|engines| engines.get(&name));
    let from_block = |key: &str| block.and_then(|b| text_of(b, key));

    let own = agent_block(binding, &name);
    let from_own = |key: &str| own.and_then(|b| text_of(b, key)).map(agent_said);
    let permissions = match own
        .and_then(|b| b.get("permissions"))
        .and_then(|p| text_of(p, "mode"))
    {
        Some(mode) => Sourced {
            value: mode,
            source: Source::Agent,
        },
        None => default(
            if name == DEFAULT_ENGINE {
                "acceptEdits"
            } else {
                ""
            }
            .to_string(),
        ),
    };

    let (runtime, runtime_override) = runtime_of(config, binding, block);
    let env = pick(
        names_of(binding, "env"),
        block.and_then(|b| names_of(b, "env")),
        Vec::new(),
    );

    let auth = engine_auth(config, &name).map_err(|why| EngineError::Auth {
        engine: name.clone(),
        why,
    })?;

    Ok(Effective {
        agent: agent.to_string(),
        model: from_own("model"),
        effort: from_own("effort"),
        permissions,
        runtime,
        runtime_override,
        env,
        command: from_block("command").map_or_else(
            || default(default_command(&name)),
            |value| Sourced {
                value,
                source: Source::Engine,
            },
        ),
        session_home: from_block("session_home").map_or_else(
            || default("session".to_string()),
            |value| Sourced {
                value,
                source: Source::Engine,
            },
        ),
        auth,
        engine,
    })
}

/// What a launch of an engine is told of how to run it: the engine block's
/// `command`, `session_home` and `auth`, and the `env` passthrough
/// the launching agent settled on. A source, never a value.
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct Drive {
    pub command: String,
    pub session_home: String,
    pub auth: Sourced<AuthSource>,
    pub env: Vec<String>,
}

impl Default for Drive {
    fn default() -> Drive {
        Drive {
            command: String::new(),
            session_home: "session".to_string(),
            auth: default_source(AuthSource::None),
            env: Vec::new(),
        }
    }
}

fn default_source(value: AuthSource) -> Sourced<AuthSource> {
    Sourced {
        value,
        source: Source::Default,
    }
}

impl From<&Effective> for Drive {
    /// An agent's launch: its own `env`, the engine's other keys.
    fn from(effective: &Effective) -> Drive {
        Drive {
            command: effective.command.value.clone(),
            session_home: effective.session_home.value.clone(),
            auth: effective.auth.clone(),
            env: effective.env.value.clone(),
        }
    }
}

/// A launch with no agent (hand-assembled): `engine`'s own block alone.
pub fn engine_drive(config: &Value, engine: &str) -> Result<Drive, EngineError> {
    if !ENGINES.contains(&engine) {
        return Err(EngineError::UnknownEngine {
            engine: engine.to_string(),
            known: ENGINES.iter().map(ToString::to_string).collect(),
        });
    }
    let block = config
        .get("engines")
        .and_then(|engines| engines.get(engine));
    let auth = engine_auth(config, engine).map_err(|why| EngineError::Auth {
        engine: engine.to_string(),
        why,
    })?;
    Ok(Drive {
        command: block
            .and_then(|b| text_of(b, "command"))
            .unwrap_or_else(|| default_command(engine)),
        session_home: block
            .and_then(|b| text_of(b, "session_home"))
            .unwrap_or_else(|| "session".to_string()),
        auth,
        env: block.and_then(|b| names_of(b, "env")).unwrap_or_default(),
    })
}

/// What every agent of `engine` shares, each with the layer that said it: the
/// settings of [`effective`] for an agent that says nothing itself, so only
/// the engine block, the project and the defaults speak. `model` and `effort`
/// are then `None` and `permissions` the engine's default.
pub fn engine_settings(config: &Value, engine: &str) -> Result<Effective, EngineError> {
    const PROBE: &str = "(engine)";
    let mut probe = config.clone();
    let bound = Value::Map(vec![(
        PROBE.to_string(),
        Value::Map(vec![(
            "engine".to_string(),
            Value::Scalar(engine.to_string()),
        )]),
    )]);
    *probe.entry("agents") = bound;
    effective(&probe, PROBE)
}

/// The credential source of `engine` over the merged config: its block's
/// `auth` (`token` spelled out as the engine's built-in variables), else the
/// engine's built-in source. Engine-wide: an agent cannot override it.
pub fn engine_auth(config: &Value, engine: &str) -> Result<Sourced<AuthSource>, AuthError> {
    let Some(value) = config
        .get("engines")
        .and_then(|engines| engines.get(engine))
        .and_then(|block| block.get("auth"))
    else {
        return Ok(Sourced {
            value: AuthSource::builtin(engine),
            source: Source::Default,
        });
    };
    Ok(Sourced {
        value: match AuthSource::parse(value)? {
            AuthSource::Token => AuthSource::builtin(engine),
            other => other,
        },
        source: Source::Engine,
    })
}

/// The engines the agents are bound to, each once, in agent-name order; the
/// default engine when no agent is declared.
pub fn engines_in_use(config: &Value) -> Vec<String> {
    let mut named: Vec<(&String, String)> = match config.get("agents") {
        Some(Value::Map(agents)) => agents
            .iter()
            .map(|(name, body)| {
                (
                    name,
                    text_of(body, "engine").unwrap_or_else(|| DEFAULT_ENGINE.to_string()),
                )
            })
            .collect(),
        _ => Vec::new(),
    };
    named.sort_by(|a, b| a.0.cmp(b.0));
    let mut out: Vec<String> = Vec::new();
    for (_, engine) in named {
        if !out.contains(&engine) {
            out.push(engine);
        }
    }
    if out.is_empty() {
        out.push(DEFAULT_ENGINE.to_string());
    }
    out
}

/// The merged configuration of layers' texts, lowest first; a layer that does
/// not read adds nothing (the loader refuses it where it matters).
pub fn merged(layers: &[String]) -> Value {
    let read: Vec<Value> = layers
        .iter()
        .filter_map(|text| super::layer::read(text).ok())
        .map(|read| read.value)
        .collect();
    super::layer::merge_layers(&read)
}

/// The agent's value, else the engine block's, else `fallback`: each key
/// resolved on its own, a list replaced whole.
fn pick<T>(agent: Option<T>, engine: Option<T>, fallback: T) -> Sourced<T> {
    match (agent, engine) {
        (Some(value), _) => Sourced {
            value,
            source: Source::Agent,
        },
        (None, Some(value)) => Sourced {
            value,
            source: Source::Engine,
        },
        (None, None) => Sourced {
            value: fallback,
            source: Source::Default,
        },
    }
}

/// The runtime an agent runs in: its own, exactly, else the engine block's,
/// else the project's, else the host; and what its own replaced.
fn runtime_of(
    config: &Value,
    binding: &Value,
    block: Option<&Value>,
) -> (Sourced<String>, Override) {
    let below = match (
        block.and_then(|b| text_of(b, "runtime")),
        text_of(config, "runtime"),
    ) {
        (Some(value), _) => Sourced {
            value,
            source: Source::Engine,
        },
        (None, Some(value)) => Sourced {
            value,
            source: Source::Project,
        },
        (None, None) => default(HOST.to_string()),
    };
    let Some(value) = text_of(binding, "runtime") else {
        return (below, Override::Inherited);
    };
    let kept = match runtime_change(&below.value, &value) {
        None => Override::Restated,
        Some(change) => Override::Replaced {
            replaced: below,
            change,
        },
    };
    (agent_said(value), kept)
}

/// Ruling 46: what a PROJECT layer may not say. `engines.<e>.command` and
/// `engines.<e>.auth.file` are honoured only from the home-layer config, and
/// `session_home: host` only through a trust check no layer reading can make:
/// each is taken out of `layer` and its dotted path returned, so the loader
/// can warn. Nothing else is touched.
pub fn strip_project_only(layer: &mut Value) -> Vec<String> {
    let mut removed = Vec::new();
    let Some(Value::Map(engines)) = layer.get_mut("engines") else {
        return removed;
    };
    for (engine, block) in engines.iter_mut() {
        let at = |key: &str| format!("engines.{engine}.{key}");
        if block.get("command").is_some() {
            block.remove("command");
            removed.push(at("command"));
        }
        if block.get("session_home").and_then(Value::as_str) == Some("host") {
            block.remove("session_home");
            removed.push(at("session_home"));
        }
        if let Some(auth @ Value::Map(_)) = block.get_mut("auth") {
            if auth.get("file").is_some() {
                auth.remove("file");
                removed.push(at("auth.file"));
            }
            if auth.as_map().is_some_and(<[_]>::is_empty) {
                block.remove("auth");
            }
        }
    }
    removed
}

/// The engine an agent that names none is bound to.
pub const DEFAULT_ENGINE: &str = "claude-code";

/// The engines ctxloom drives (the schema's).
const ENGINES: &[&str] = super::schema::ENGINES;

/// The program `engine` runs when the engine block names none: claude's is
/// `claude`; every other engine's is its own name.
fn default_command(engine: &str) -> String {
    match engine {
        DEFAULT_ENGINE => "claude".to_string(),
        other => other.to_string(),
    }
}

fn default(value: String) -> Sourced<String> {
    Sourced {
        value,
        source: Source::Default,
    }
}

fn agent_said(value: String) -> Sourced<String> {
    Sourced {
        value,
        source: Source::Agent,
    }
}

/// A non-empty scalar under `key`.
fn text_of(from: &Value, key: &str) -> Option<String> {
    from.get(key)
        .and_then(Value::as_str)
        .filter(|text| !text.is_empty())
        .map(ToString::to_string)
}

/// The names under `key`; a list that is there but empty is still said.
fn names_of(from: &Value, key: &str) -> Option<Vec<String>> {
    from.get(key).and_then(Value::as_seq).map(|items| {
        items
            .iter()
            .filter_map(|i| i.as_str().map(ToString::to_string))
            .collect()
    })
}

#[cfg(test)]
#[path = "engines.test.rs"]
mod tests;
