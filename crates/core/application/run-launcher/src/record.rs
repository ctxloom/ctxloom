//! A launch's inputs as the stream records them (P3, owner rulings
//! 2026-11-25 and 2026-11-27): what a run's plan decided is the `spec` of its
//! `RunEnqueued`, and a launch is made from that record plus what the
//! session's own events say (its mint's engine and workspace axis, its
//! loadout's agent, the native session an engine resumes). [`spec_of`] writes
//! the record from an [`Order`], sealing every environment value and every
//! MCP credential; [`order_of`] reads it back, opening them. Nothing is
//! handed to a launcher in memory.

use std::collections::BTreeMap;
use std::time::Duration;

use pending_launches::PendingLaunch;
use run_ports::{
    AuthSetting, EngineSettings, GitUser, LaunchSurfaces, Sealing, SealingError, SessionHome,
    APPROVAL_TIMEOUT_DEFAULT,
};
use serde_json::{json, Value};
use surface_ports::{Command, Hook, Inputs, McpServer, Skill, SkillFile};

use crate::order::Order;
use crate::workspace::{Axis, Workspace};

/// Why a record could not be read back into an order.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum RecordError {
    /// The run was enqueued with no spec.
    NoSpec,
    /// A sealed value did not open.
    Sealing(SealingError),
    /// The hooks document is not a document of hooks.
    Hooks(String),
}

impl From<SealingError> for RecordError {
    fn from(error: SealingError) -> RecordError {
        RecordError::Sealing(error)
    }
}

fn sealed_entry(
    (sealing, project): (&dyn Sealing, &str),
    scope: &str,
    name: &str,
    value: &str,
) -> Result<runs::EnvEntry, SealingError> {
    Ok(runs::EnvEntry {
        name: name.to_string(),
        value: runs::SealedValue {
            sealed: sealing.seal(project, &format!("{scope}{name}"), value.as_bytes())?,
        },
    })
}

fn opened(
    (sealing, project): (&dyn Sealing, &str),
    scope: &str,
    entries: &[runs::EnvEntry],
) -> Result<BTreeMap<String, String>, RecordError> {
    entries
        .iter()
        .map(|entry| {
            let plain = sealing.open(
                project,
                &format!("{scope}{}", entry.name),
                &entry.value.sealed,
            )?;
            Ok((
                entry.name.clone(),
                String::from_utf8_lossy(&plain).into_owned(),
            ))
        })
        .collect()
}

fn sealed_map(
    keyed: (&dyn Sealing, &str),
    scope: &str,
    map: &BTreeMap<String, String>,
) -> Result<Vec<runs::EnvEntry>, SealingError> {
    map.iter()
        .map(|(name, value)| sealed_entry(keyed, scope, name, value))
        .collect()
}

fn hook_value(hook: &Hook) -> Value {
    json!({
        "kind": hook.kind,
        "command": hook.command,
        "args": hook.args,
        "prompt": hook.prompt,
        "timeout": hook.timeout,
        "is_async": hook.is_async,
        "tool": hook.tool,
        "matcher": hook.matcher,
    })
}

fn hook_of(value: &Value) -> Option<Hook> {
    let text = |key: &str| value.get(key)?.as_str().map(str::to_string);
    Some(Hook {
        kind: text("kind")?,
        command: text("command")?,
        args: value
            .get("args")?
            .as_array()?
            .iter()
            .map(|a| a.as_str().map(str::to_string))
            .collect::<Option<_>>()?,
        prompt: text("prompt")?,
        timeout: value.get("timeout")?.as_i64()?,
        is_async: value.get("is_async")?.as_bool()?,
        tool: text("tool")?,
        matcher: text("matcher")?,
    })
}

fn hooks_json(hooks: &BTreeMap<String, Vec<Hook>>) -> String {
    let document: serde_json::Map<String, Value> = hooks
        .iter()
        .map(|(event, hooks)| {
            (
                event.clone(),
                Value::Array(hooks.iter().map(hook_value).collect()),
            )
        })
        .collect();
    if document.is_empty() {
        return String::new();
    }
    Value::Object(document).to_string()
}

fn hooks_of(text: &str) -> Result<BTreeMap<String, Vec<Hook>>, RecordError> {
    if text.is_empty() {
        return Ok(BTreeMap::new());
    }
    let bad = |why: &str| RecordError::Hooks(why.to_string());
    let document: Value =
        serde_json::from_str(text).map_err(|e| RecordError::Hooks(e.to_string()))?;
    let Value::Object(events) = document else {
        return Err(bad("not an object"));
    };
    events
        .into_iter()
        .map(|(event, hooks)| {
            let hooks = hooks
                .as_array()
                .ok_or_else(|| bad("an event's hooks are not a list"))?
                .iter()
                .map(|h| hook_of(h).ok_or_else(|| bad("a hook is not a hook")))
                .collect::<Result<Vec<_>, _>>()?;
            Ok((event, hooks))
        })
        .collect()
}

fn surfaces_of(
    inputs: &Inputs,
    keyed: (&dyn Sealing, &str),
) -> Result<runs::LaunchSurfaces, SealingError> {
    let mcp_servers = inputs
        .mcp
        .iter()
        .map(|server| {
            let scope = format!("mcp:{}:", server.name);
            Ok(runs::McpSpec {
                name: server.name.clone(),
                command: server.command.clone(),
                args: server.args.clone(),
                url: server.url.clone(),
                env: sealed_map(keyed, &format!("{scope}env:"), &server.env)?,
                headers: sealed_map(keyed, &format!("{scope}header:"), &server.headers)?,
            })
        })
        .collect::<Result<_, SealingError>>()?;
    Ok(runs::LaunchSurfaces {
        hooks_json: hooks_json(&inputs.hooks),
        statusline: if inputs.statusline { "ctxloom" } else { "none" }.to_string(),
        deny_tools: inputs.deny_tools.clone(),
        commands: inputs
            .commands
            .iter()
            .map(|c| runs::SurfaceCommand {
                name: c.name.clone(),
                description: c.description.clone(),
                body: c.body.clone(),
            })
            .collect(),
        skills: inputs
            .skills
            .iter()
            .map(|skill| runs::SurfaceSkill {
                name: skill.name.clone(),
                description: skill.description.clone(),
                files: skill
                    .files
                    .iter()
                    .map(|f| runs::SurfaceFile {
                        path: f.path.clone(),
                        content: f.bytes.clone(),
                        executable: f.executable,
                    })
                    .collect(),
            })
            .collect(),
        mcp_servers,
        no_context: inputs.no_context,
    })
}

fn inputs_of(spec: &runs::LaunchSpec, keyed: (&dyn Sealing, &str)) -> Result<Inputs, RecordError> {
    let surfaces = &spec.surfaces;
    let mcp = surfaces
        .mcp_servers
        .iter()
        .map(|server| {
            let scope = format!("mcp:{}:", server.name);
            Ok(McpServer {
                name: server.name.clone(),
                command: server.command.clone(),
                args: server.args.clone(),
                env: opened(keyed, &format!("{scope}env:"), &server.env)?,
                url: server.url.clone(),
                headers: opened(keyed, &format!("{scope}header:"), &server.headers)?,
            })
        })
        .collect::<Result<_, RecordError>>()?;
    Ok(Inputs {
        // The surfaces carry no context of their own: the launch's is the
        // spec's, in the order.
        context: String::new(),
        context_file: None,
        mcp,
        hooks: hooks_of(&surfaces.hooks_json)?,
        deny_tools: surfaces.deny_tools.clone(),
        statusline: surfaces.statusline == "ctxloom",
        commands: surfaces
            .commands
            .iter()
            .map(|c| Command {
                name: c.name.clone(),
                description: c.description.clone(),
                body: c.body.clone(),
            })
            .collect(),
        skills: surfaces
            .skills
            .iter()
            .map(|skill| Skill {
                name: skill.name.clone(),
                description: skill.description.clone(),
                files: skill
                    .files
                    .iter()
                    .map(|f| SkillFile {
                        path: f.path.clone(),
                        bytes: f.content.clone(),
                        executable: f.executable,
                    })
                    .collect(),
            })
            .collect(),
        no_context: surfaces.no_context,
    })
}

fn auth_word(auth: &AuthSetting) -> &'static str {
    match auth {
        AuthSetting::Builtin => "builtin",
        AuthSetting::None => "none",
        AuthSetting::Login => "login",
        AuthSetting::Source { .. } => "source",
    }
}

fn auth_of(spec: &runs::LaunchSpec) -> AuthSetting {
    match spec.auth.as_str() {
        "none" => AuthSetting::None,
        "login" => AuthSetting::Login,
        "source" => AuthSetting::Source {
            env: spec.auth_variables.clone(),
            file: (!spec.auth_file.is_empty()).then(|| spec.auth_file.clone()),
        },
        _ => AuthSetting::Builtin,
    }
}

/// What a spec records beside the order: what the plan decided that no order
/// field holds.
pub struct Recording<'a> {
    /// The running binary's directory, which leads the engine's `PATH`.
    pub program_dir: &'a str,
    /// The session's checkouts directory, empty where there is none.
    pub workspace_dir: &'a str,
    /// What the run isolates by, as the plan settled it.
    pub runtime: &'a str,
    /// The run's purpose, when it is a distill's.
    pub distill: Option<runs::DistillPurpose>,
    /// The project the run belongs to (its harp or id): whose seal key seals
    /// its secrets.
    pub project: &'a str,
}

/// The spec `order` records: its settings, context, prompt, identities and
/// surfaces, its environment and MCP credentials sealed. `distill`, when the
/// run is a distill's, is its purpose.
pub fn spec_of(
    order: &Order,
    recording: Recording<'_>,
    sealing: &dyn Sealing,
) -> Result<runs::LaunchSpec, SealingError> {
    let Recording {
        program_dir,
        workspace_dir,
        runtime,
        distill,
        project,
    } = recording;
    let keyed = (sealing, project);
    let settings = &order.settings;
    let (auth_variables, auth_file) = match &settings.auth {
        AuthSetting::Source { env, file } => (env.clone(), file.clone().unwrap_or_default()),
        _ => (Vec::new(), String::new()),
    };
    Ok(runs::LaunchSpec {
        model: order.model.clone(),
        effort: order.effort.clone(),
        session_home: match settings.session_home {
            SessionHome::Session => "session",
            SessionHome::Host => "host",
        }
        .to_string(),
        approval_timeout_seconds: u32::try_from(settings.approval_timeout.as_secs()).ok(),
        engine_command: settings.command.clone(),
        auth_variables,
        auth_file,
        auth: auth_word(&settings.auth).to_string(),
        env: sealed_map(keyed, "env:", &order.env)?,
        context: order.context.clone(),
        prompt: order.prompt.clone(),
        surfaces: surfaces_of(&order.surfaces.0, keyed)?,
        git_user: order
            .git_user
            .as_ref()
            .map(|user| runs::GitIdentity {
                name: user.name.clone(),
                email: user.email.clone(),
            })
            .unwrap_or_default(),
        runtime: runtime.to_string(),
        client_program: runs::ClientProgram {
            name: "ctxloom".to_string(),
            directory: program_dir.to_string(),
        },
        user_settings_sequence: None,
        workspace_dir: workspace_dir.to_string(),
        engine_env: settings.env.clone(),
        distill,
        owned: order.owned.clone(),
        user_home: order.user_home.clone().unwrap_or_default(),
    })
}

/// The order a launch is made from: the run's recorded spec and what its
/// session's events say, the environment's values opened.
pub fn order_of(launch: &PendingLaunch, sealing: &dyn Sealing) -> Result<Order, RecordError> {
    let spec = launch.spec.as_ref().ok_or(RecordError::NoSpec)?;
    let keyed = (sealing, launch.project_id.as_str());
    let env = opened(keyed, "env:", &spec.env)?;
    Ok(Order {
        harp: launch.harp.clone(),
        engine: launch.engine.clone(),
        context: spec.context.clone(),
        prompt: spec.prompt.clone(),
        env,
        user_home: (!spec.user_home.is_empty()).then(|| spec.user_home.clone()),
        cwd: (!launch.project_dir.is_empty()).then(|| launch.project_dir.clone()),
        workspace: Workspace {
            axis: Axis::of(launch.workspace.as_deref().unwrap_or("none")),
            work_dir: spec.workspace_dir.clone(),
            agent: launch.agent.clone(),
        },
        interactive: launch.mode == runs::RunMode::Interactive,
        model: spec.model.clone(),
        effort: spec.effort.clone(),
        permissions: launch
            .grant
            .as_ref()
            .map(|grant| grant.permissions.clone())
            .filter(|document| !document.is_empty()),
        settings: EngineSettings {
            command: spec.engine_command.clone(),
            session_home: if spec.session_home == "host" {
                SessionHome::Host
            } else {
                SessionHome::Session
            },
            auth: auth_of(spec),
            env: spec.engine_env.clone(),
            approval_timeout: spec
                .approval_timeout_seconds
                .map_or(APPROVAL_TIMEOUT_DEFAULT, |s| {
                    Duration::from_secs(u64::from(s))
                }),
        },
        owned: spec.owned.clone(),
        git_user: (!spec.git_user.name.is_empty() || !spec.git_user.email.is_empty()).then(|| {
            GitUser {
                name: spec.git_user.name.clone(),
                email: spec.git_user.email.clone(),
            }
        }),
        resume: launch.resume.clone(),
        surfaces: LaunchSurfaces(inputs_of(spec, keyed)?),
    })
}

#[cfg(test)]
#[path = "record.test.rs"]
mod tests;
