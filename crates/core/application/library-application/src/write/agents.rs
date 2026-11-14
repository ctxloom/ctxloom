//! `agent create|edit|default|remove` (Go's `operations.SetAgent`,
//! `RemoveAgent` and `cli/agent.go`). An agent is a LOCAL binding under the
//! `agents:` key of the project's `config.yaml`; create and edit are not an
//! upsert (each refuses the case the other owns), and every axis is checked
//! before the file is touched.

use angzarr_yaml::Value;
use ctxloom_ports::Scope;

use crate::agents::RUNTIMES;
use crate::config::Agent;
use crate::error::{AppError, Notice};
use crate::project::{Library, Outcome, Project};
use crate::views::AgentRow;
use crate::write::engines;
use crate::write::requests::AgentRequest;
use crate::write::views::{
    AgentRemoved, AgentWritten, DefaultAgent, Preview, Removal, SwapPreview,
};
use crate::write::WriteError;
use crate::Flags;
use library::config::engines::{self as config_engines, Override, Source};

impl Library {
    /// `agent create <name> ...` (`must_exist` false) and `agent edit <name>
    /// ...` (true): only the flags named change; one named empty clears.
    pub fn agent_write(
        &self,
        flags: &Flags,
        name: &str,
        request: &AgentRequest,
        must_exist: bool,
    ) -> Outcome<AgentRow> {
        self.run(flags, |p| p.agent_write(name, request, must_exist))
    }

    /// `agent edit <name> ... [--yes]`: as [`Library::agent_write`] for an
    /// agent that is there, except that a change of its engine to one that
    /// cannot carry what the old one did of its profiles is the swap's
    /// preview without `yes` (nothing written; what would be given up, and
    /// the command that applies it).
    pub fn agent_edit(
        &self,
        flags: &Flags,
        name: &str,
        request: &AgentRequest,
        yes: bool,
    ) -> Outcome<AgentWritten> {
        self.run(flags, |p| p.agent_edit(name, request, yes))
    }

    /// `agent default [name]`: the agent a bare run binds, or bind one (one
    /// not defined yet is bound, and said).
    pub fn agent_default(&self, flags: &Flags, name: Option<&str>) -> Outcome<DefaultAgent> {
        self.run(flags, |p| p.agent_default(name))
    }

    /// `agent remove <name> [--yes]`.
    pub fn agent_remove(
        &self,
        flags: &Flags,
        name: &str,
        yes: bool,
    ) -> Outcome<Removal<AgentRemoved>> {
        self.run(flags, |p| p.agent_remove(name, yes))
    }
}

fn text(value: &str) -> Value {
    Value::Scalar(value.to_string())
}

/// `key` of the mapping set to `value`, or taken out where `value` is none.
fn put(map: &mut Value, key: &str, value: Option<Value>) {
    match value {
        Some(value) => *map.entry(key) = value,
        None => {
            map.remove(key);
        }
    }
}

/// `key` of the agent's block for `engine` (`engines.<engine>.<key>`, owner
/// ruling 57) set to `value`, or taken out where `value` is none; a block left
/// empty is taken out, and so is an `engines` left empty.
fn put_in_block(entry: &mut Value, engine: &str, key: &str, value: Option<Value>) {
    let mut blocks = match entry.get("engines") {
        Some(map @ Value::Map(_)) => map.clone(),
        _ => Value::Map(Vec::new()),
    };
    let mut block = match blocks.get(engine) {
        Some(map @ Value::Map(_)) => map.clone(),
        _ => Value::Map(Vec::new()),
    };
    put(&mut block, key, value);
    let empty = |v: &Value| matches!(v, Value::Map(m) if m.is_empty());
    let keep = (!empty(&block)).then_some(block);
    put(&mut blocks, engine, keep);
    let keep = (!empty(&blocks)).then_some(blocks);
    put(entry, "engines", keep);
}

fn some_text(value: &str) -> Option<Value> {
    (!value.is_empty()).then(|| text(value))
}

fn pairs_map(pairs: &[(String, String)]) -> Option<Value> {
    (!pairs.is_empty()).then(|| {
        Value::Map(
            pairs
                .iter()
                .map(|(k, v)| (k.trim().to_string(), text(v.trim())))
                .collect(),
        )
    })
}

/// A word of a command line as a shell reads it back: quoted where it holds
/// anything but plain characters, or is empty.
fn shell_word(word: &str) -> String {
    let plain = !word.is_empty()
        && word
            .chars()
            .all(|c| c.is_ascii_alphanumeric() || "-_./:=,@+%".contains(c));
    if plain {
        word.to_string()
    } else {
        format!("'{}'", word.replace('\'', r"'\''"))
    }
}

/// The `agent edit` that applies `request` to `name`: its flags as given,
/// then `--yes`.
fn apply_command(name: &str, request: &AgentRequest) -> String {
    let mut words = vec![
        "ctxloom".to_string(),
        "agent".to_string(),
        "edit".to_string(),
        shell_word(name),
    ];
    let mut flag = |flag: &str, value: &str| {
        words.push(flag.to_string());
        words.push(shell_word(value));
    };
    let single = [
        ("--engine", &request.engine),
        ("--model", &request.model),
        ("--effort", &request.effort),
        ("--runtime", &request.runtime),
        ("--session-home", &request.session_home),
    ];
    for (name, value) in single {
        if let Some(value) = value {
            flag(name, value);
        }
    }
    if let Some(profiles) = &request.profiles {
        flag("--profiles", &profiles.join(","));
    }
    for (name, pairs) in [("--surface", &request.surfaces), ("--root", &request.roots)] {
        for (kind, value) in pairs.iter().flatten() {
            flag(name, &format!("{kind}={value}"));
        }
        if pairs.as_ref().is_some_and(Vec::is_empty) {
            flag(name, "");
        }
    }
    if let Some(env) = &request.env {
        if env.is_empty() {
            flag("--env", "");
        }
        for variable in env {
            flag("--env", variable);
        }
    }
    words.push("--yes".to_string());
    words.join(" ")
}

impl Project {
    /// The backend of the engine label a write results in: the request's, else
    /// the one the layer's entry holds, else the one the merged configuration
    /// holds.
    fn resulting_backend(
        &self,
        name: &str,
        request: &AgentRequest,
        held: &Value,
    ) -> Option<String> {
        let label = match &request.engine {
            Some(label) => label.clone(),
            None => {
                let own = held
                    .get("engine")
                    .and_then(Value::as_str)
                    .unwrap_or_default();
                if own.is_empty() {
                    self.config
                        .agent(name)
                        .map(|a| a.engine)
                        .unwrap_or_default()
                } else {
                    own.to_string()
                }
            }
        };
        if label.is_empty() {
            return None;
        }
        engines::known(&label).then_some(label)
    }

    /// The axes of a request, judged before anything is written. An engine's
    /// permissions are never among them: they are the engine's own document,
    /// written in the config by hand (owner ruling 138).
    fn check_agent(
        &self,
        name: &str,
        request: &AgentRequest,
        held: &Value,
    ) -> Result<(), AppError> {
        if let Some(engine) = request.engine.as_deref().filter(|l| !l.is_empty()) {
            if !engines::known(engine) {
                return Err(WriteError::UnknownEngine {
                    name: name.to_string(),
                    engine: engine.to_string(),
                    valid: crate::engine_noun::listed().join(", "),
                }
                .into());
            }
        }
        if let Some(runtime) = request.runtime.as_deref().filter(|r| !r.is_empty()) {
            if !RUNTIMES.contains(&runtime) {
                return Err(WriteError::UnknownRuntime {
                    name: name.to_string(),
                    value: runtime.to_string(),
                }
                .into());
            }
        }
        if let Some(home) = request.session_home.as_deref().filter(|h| !h.is_empty()) {
            if !["session", "host"].contains(&home) {
                return Err(WriteError::UnknownSessionHome {
                    name: name.to_string(),
                    value: home.to_string(),
                }
                .into());
            }
        }
        let backend = self.resulting_backend(name, request, held);
        self.check_surfaces(name, request, backend.as_deref())?;
        self.check_roots(name, request, backend.as_deref())
    }

    fn check_surfaces(
        &self,
        name: &str,
        request: &AgentRequest,
        backend: Option<&str>,
    ) -> Result<(), AppError> {
        let Some(pairs) = request.surfaces.as_ref().filter(|p| !p.is_empty()) else {
            return Ok(());
        };
        let Some(backend) = backend else {
            return Err(WriteError::SurfaceNeedsEngine {
                name: name.to_string(),
            }
            .into());
        };
        for (kind, approach) in pairs {
            let (kind, approach) = (kind.trim(), approach.trim());
            let refuse = |detail: String| -> AppError {
                WriteError::SurfaceRefused {
                    name: name.to_string(),
                    detail,
                }
                .into()
            };
            if !engines::SURFACE_KINDS.contains(&kind) {
                return Err(refuse(format!(
                    "surfaces {kind}={approach}: \"{kind}\" is not a surface kind"
                )));
            }
            if let Some(offered) = engines::approaches(backend, kind) {
                if !offered.contains(&approach) {
                    return Err(refuse(format!(
                        "surfaces {kind}={approach}: {backend} does not support it (supports: {})",
                        offered.join(", ")
                    )));
                }
            }
        }
        Ok(())
    }

    fn check_roots(
        &self,
        name: &str,
        request: &AgentRequest,
        backend: Option<&str>,
    ) -> Result<(), AppError> {
        let Some(pairs) = request.roots.as_ref().filter(|p| !p.is_empty()) else {
            return Ok(());
        };
        if backend.is_none() {
            return Err(WriteError::RootNeedsEngine {
                name: name.to_string(),
            }
            .into());
        }
        for (kind, root) in pairs {
            let (kind, root) = (kind.trim(), root.trim());
            let refuse = |detail: String| -> AppError {
                WriteError::RootRefused {
                    name: name.to_string(),
                    detail,
                }
                .into()
            };
            if !engines::SURFACE_KINDS.contains(&kind) {
                return Err(refuse(format!(
                    "roots {kind}={root}: \"{kind}\" is not a surface kind"
                )));
            }
            if !engines::ROOTS.contains(&root) {
                return Err(refuse(format!(
                    "roots {kind}={root}: \"{root}\" is not a root (session-home, project-root, work-dir)"
                )));
            }
        }
        Ok(())
    }

    fn agent_write(
        &mut self,
        name: &str,
        request: &AgentRequest,
        must_exist: bool,
    ) -> Result<AgentRow, AppError> {
        self.require_project()?;
        let exists = self.config.agent(name).is_some();
        if must_exist && !exists {
            return Err(WriteError::AgentMissing {
                name: name.to_string(),
            }
            .into());
        }
        if !must_exist && exists {
            return Err(WriteError::AgentExists {
                name: name.to_string(),
            }
            .into());
        }
        let mut written = None;
        let mut said = Vec::new();
        let layer_held = |agents: &Option<Value>| {
            agents
                .as_ref()
                .and_then(|a| a.get(name))
                .cloned()
                .unwrap_or_else(|| Value::Map(Vec::new()))
        };
        // The checks need the entry the layer holds; the layer is read inside
        // the update, where it is held, so they are made there.
        self.update_config(Scope::Project, "agents", |current| {
            let held = layer_held(&current);
            self.check_agent(name, request, &held)?;
            let entry = self.agent_entry(name, request, held)?;
            written = Some(Agent::of(name, &entry).row());
            if request.runtime.as_deref().is_some_and(|r| !r.is_empty()) {
                said.extend(self.runtime_notice(name, &entry));
            }
            said.extend(self.default_block_notice(name, request));
            let mut agents = match current {
                Some(map @ Value::Map(_)) => map,
                _ => Value::Map(Vec::new()),
            };
            *agents.entry(name) = entry;
            Ok(Some(agents))
        })?;
        self.notices.extend(said);
        written.ok_or_else(|| WriteError::NotWired {}.into())
    }

    fn agent_edit(
        &mut self,
        name: &str,
        request: &AgentRequest,
        yes: bool,
    ) -> Result<AgentWritten, AppError> {
        if !yes {
            if let Some(preview) = self.swap_preview(name, request)? {
                return Ok(AgentWritten::Preview(preview));
            }
        }
        self.agent_write(name, request, true)
            .map(AgentWritten::Done)
    }

    /// The preview of an edit that moves the agent to an engine that cannot
    /// carry what its engine did of its profiles; none for any other edit
    /// (one that does not change the engine, or loses nothing, is written as
    /// ever). The edit is judged first as the write would judge it, so a
    /// preview is never shown for an edit the write would refuse.
    fn swap_preview(
        &mut self,
        name: &str,
        request: &AgentRequest,
    ) -> Result<Option<SwapPreview>, AppError> {
        let Some(agent) = self.config.agent(name) else {
            return Ok(None);
        };
        let Some(to) = request.engine.as_deref().filter(|e| engines::known(e)) else {
            return Ok(None);
        };
        let from = crate::losses::engine_or_default(&agent.engine);
        if to == from {
            return Ok(None);
        }
        let profiles = request
            .profiles
            .clone()
            .unwrap_or_else(|| agent.profiles.clone());
        let lost = self.swap_losses(&profiles, &from, to);
        if lost.is_empty() {
            return Ok(None);
        }
        self.require_project()?;
        let held = self.held_agent(name)?;
        self.check_agent(name, request, &held)?;
        self.agent_entry(name, request, held)?;
        Ok(Some(SwapPreview::of(
            name,
            &from,
            to,
            lost,
            apply_command(name, request),
        )))
    }

    /// The entry the project's config holds for `name`; an empty mapping for
    /// none.
    fn held_agent(&self, name: &str) -> Result<Value, AppError> {
        let text = self
            .files
            .layer(Scope::Project)
            .map_err(AppError::Cause)?
            .map(|layer| layer.text)
            .unwrap_or_default();
        let agents = library::write::section::section(&text, "agents").map_err(|e| {
            WriteError::ConfigCorrupt {
                path: self.files.layer_path(Scope::Project),
                cause: e.to_string(),
            }
        })?;
        Ok(agents
            .as_ref()
            .and_then(|a| a.get(name))
            .cloned()
            .unwrap_or_else(|| Value::Map(Vec::new())))
    }

    /// What the runtime an entry names replaces, said where it is written: an
    /// override is exact and never silent.
    fn runtime_notice(&self, name: &str, entry: &Value) -> Option<Notice> {
        let mut config = self.config.value.clone();
        let agents = config.entry("agents");
        if !matches!(agents, Value::Map(_)) {
            *agents = Value::Map(Vec::new());
        }
        *agents.entry(name) = entry.clone();
        let effective = config_engines::effective(&config, name).ok()?;
        let Override::Replaced { replaced, change } = effective.runtime_override else {
            return None;
        };
        Some(Notice::RuntimeOverride {
            runtime: effective.runtime.value,
            replaced_key: match replaced.source {
                Source::Engine => format!("engines.{}.runtime", effective.engine.value),
                _ => "runtime".to_string(),
            },
            replaced: replaced.value,
            change,
        })
    }

    /// The engine whose block model and effort are written into: the engine
    /// named, else the one the agent binds, else the default one (ruling 57),
    /// and whether it is the default because nothing named one (an engine
    /// taken out, `--engine ''`, leaves the default one).
    fn block_of(&self, name: &str, request: &AgentRequest) -> (String, bool) {
        let named = match request.engine.as_deref() {
            Some(engine) => engine.to_string(),
            None => self
                .config
                .agent(name)
                .map(|a| a.engine)
                .unwrap_or_default(),
        };
        if named.is_empty() {
            (config_engines::DEFAULT_ENGINE.to_string(), true)
        } else {
            (named, false)
        }
    }

    /// Owner ruling 68: a model or effort written for an agent that names no
    /// engine lands in the default engine's block, and the write says which.
    fn default_block_notice(&self, name: &str, request: &AgentRequest) -> Option<Notice> {
        let settings = request.model.is_some() || request.effort.is_some();
        let (engine, defaulted) = self.block_of(name, request);
        (settings && defaulted).then(|| Notice::DefaultEngineBlock {
            agent: name.to_string(),
            block: format!("engines.{engine}"),
        })
    }

    /// The entry a request leaves: the flags named change their keys.
    fn agent_entry(
        &self,
        name: &str,
        request: &AgentRequest,
        mut entry: Value,
    ) -> Result<Value, AppError> {
        if !matches!(entry, Value::Map(_)) {
            entry = Value::Map(Vec::new());
        }
        if let Some(engine) = &request.engine {
            put(&mut entry, "engine", some_text(engine));
        }
        let (runs_on, _) = self.block_of(name, request);
        if let Some(model) = &request.model {
            put_in_block(&mut entry, &runs_on, "model", some_text(model));
        }
        if let Some(effort) = &request.effort {
            put_in_block(&mut entry, &runs_on, "effort", some_text(effort));
        }
        if let Some(profiles) = &request.profiles {
            let list = Value::Seq(
                profiles
                    .iter()
                    .filter(|p| !p.is_empty())
                    .map(|p| text(p))
                    .collect(),
            );
            put(
                &mut entry,
                "profiles",
                (!matches!(&list, Value::Seq(l) if l.is_empty())).then_some(list),
            );
        }
        if let Some(runtime) = &request.runtime {
            put(&mut entry, "runtime", some_text(runtime));
        }
        if let Some(home) = &request.session_home {
            put(&mut entry, "session_home", some_text(home));
        }
        if let Some(surfaces) = &request.surfaces {
            put(&mut entry, "surfaces", pairs_map(surfaces));
        }
        if let Some(roots) = &request.roots {
            put(&mut entry, "roots", pairs_map(roots));
        }
        if let Some(env) = &request.env {
            let names: Vec<Value> = env
                .iter()
                .filter(|n| !n.is_empty())
                .map(|n| text(n))
                .collect();
            let list = Value::Seq(names);
            put(
                &mut entry,
                "env",
                (!matches!(&list, Value::Seq(l) if l.is_empty())).then_some(list),
            );
        }
        self.check_written(name, &entry)?;
        Ok(entry)
    }

    /// A record that cannot launch as written is refused.
    fn check_written(&self, name: &str, entry: &Value) -> Result<(), AppError> {
        let agent = Agent::of(name, entry);
        if agent.engine.is_empty() && agent.profiles.is_empty() {
            return Err(WriteError::AgentWithoutEngine {
                name: name.to_string(),
            }
            .into());
        }
        let bad = |detail: String| -> AppError {
            WriteError::BadEnv {
                name: name.to_string(),
                detail,
            }
            .into()
        };
        if let Some(entry) = agent.env.iter().find(|e| e.contains('=')) {
            return Err(bad(format!(
                "env entry \"{entry}\" must be a bare variable name, not NAME=value"
            )));
        }
        Ok(())
    }

    fn agent_default(&self, name: Option<&str>) -> Result<DefaultAgent, AppError> {
        let Some(name) = name else {
            return Ok(DefaultAgent::Current(self.config.default_agent()));
        };
        self.require_project()?;
        let defined = self.config.agent(name).is_some();
        self.update_config(Scope::Project, "default_agent", |_| Ok(Some(text(name))))?;
        Ok(DefaultAgent::Set {
            name: name.to_string(),
            defined,
        })
    }

    fn agent_remove(&self, name: &str, yes: bool) -> Result<Removal<AgentRemoved>, AppError> {
        self.require_project()?;
        if !yes {
            if self.config.agent(name).is_none() {
                return Err(WriteError::AgentNotDefined {
                    name: name.to_string(),
                }
                .into());
            }
            return Ok(Removal::Preview(Preview::of(
                format!("agent \"{name}\""),
                Vec::new(),
                format!("ctxloom agent remove {name} --yes"),
            )));
        }
        self.update_config(Scope::Project, "agents", |current| {
            let Some(mut agents) = current.filter(|a| a.get(name).is_some()) else {
                return Err(WriteError::AgentNotInConfig {
                    name: name.to_string(),
                }
                .into());
            };
            agents.remove(name);
            let empty = matches!(&agents, Value::Map(m) if m.is_empty());
            Ok((!empty).then_some(agents))
        })?;
        Ok(Removal::Done(AgentRemoved {
            status: "removed".to_string(),
            name: name.to_string(),
        }))
    }
}

#[cfg(test)]
#[path = "agents.test.rs"]
mod tests;
