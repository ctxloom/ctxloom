//! `agent`: list and show, and its writes (B2), each as its API resource or
//! its method's response (OD-40, design §3.7, §5.6). `agent default` still
//! answers in text only.

use std::collections::BTreeMap;

use library_application::{AgentShow, Removal, Requirement, ResolvedAgent};

use crate::prelude::*;

pub(crate) fn read(
    cx: &Context<'_>,
    (verb, sub): (&str, &ArgMatches),
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    if verb == "show" {
        let catalog = cx.catalog;
        let say = |d: &dyn Diagnostic| catalog.message(d);
        let outcome = cx
            .library
            .agent_show(&cx.flags, &text_arg(sub, "name"), &say);
        return resource(
            cx,
            outcome,
            out,
            err,
            |parent, show| agent_show(parent, show, &default_agent(cx)),
            text::agent_show,
        );
    }
    let outcome = cx.library.agent_list(&cx.flags);
    resource(
        cx,
        outcome,
        out,
        err,
        |parent, rows: &Vec<AgentRow>| agents(parent, rows, &default_agent(cx)),
        |out, rows| text::agent_list(out, rows),
    )
}

pub(crate) fn write(
    cx: &Context<'_>,
    (verb, sub): (&str, &ArgMatches),
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Option<Exit>, Failure> {
    let name = text_arg(sub, "name");
    let code = match verb {
        "create" | "edit" => {
            let request = agent_request(sub)?;
            if verb == "create" {
                let outcome = cx.library.agent_write(&cx.flags, &name, &request, false);
                api::written(
                    cx,
                    outcome,
                    out,
                    err,
                    |parent, row| v1::CreateAgentResponse {
                        agent: Some(agent(parent, row, &default_agent(cx))),
                        applied: Some(true),
                        ..v1::CreateAgentResponse::default()
                    },
                    |row, out, _| agent_written(row, "Created", out),
                    |_| Exit::OK,
                )?
            } else {
                let outcome = cx
                    .library
                    .agent_edit(&cx.flags, &name, &request, flag(sub, "yes"));
                api::written(
                    cx,
                    outcome,
                    out,
                    err,
                    |parent, value| edited(parent, value, &default_agent(cx)),
                    |value, out, _| match value {
                        AgentWritten::Preview(preview) => swap_preview_text(out, preview),
                        AgentWritten::Done(row) => agent_written(row, "Updated", out),
                    },
                    // A preview would change the binding (ruling 153).
                    |value| match value {
                        AgentWritten::Preview(_) => Exit::WOULD_CHANGE,
                        AgentWritten::Done(_) => Exit::OK,
                    },
                )?
            }
        }
        "default" => {
            text_only(cx, "agent default")?;
            let asked = given(sub, "name");
            let outcome = cx.library.agent_default(&cx.flags, asked.as_deref());
            finish_text(cx, outcome, out, err, |done, out, notes| match done {
                DefaultAgent::Current(current) if current.is_empty() => {
                    writeln!(out, "No default agent set.")
                }
                DefaultAgent::Current(current) => writeln!(out, "Default agent: {current}"),
                DefaultAgent::Set { name, defined } => {
                    if !defined {
                        notes.push(format!(
                            "agent \"{name}\" is not defined yet; define it with `ctxloom agent create {name}`"
                        ));
                    }
                    writeln!(out, "Set default agent to \"{name}\".")
                }
            })?
        }
        "remove" => {
            let outcome = cx.library.agent_remove(&cx.flags, &name, flag(sub, "yes"));
            api::written(
                cx,
                outcome,
                out,
                err,
                |parent, removal| removed(parent, &name, removal),
                |removal, out, _| match removal {
                    Removal::Preview(preview) => crate::write::preview_text(out, preview),
                    Removal::Done(gone) => writeln!(out, "Removed agent \"{}\"", gone.name),
                },
                crate::write::removal_exit,
            )?
        }
        _ => return Ok(None),
    };
    Ok(Some(code))
}

/// The agent a bare run binds, empty when none: what each agent's `default`
/// says (OQ-7). Asked only for a json answer, after the answer itself was
/// had, so its notices are the answer's own and are not said again.
fn default_agent(cx: &Context<'_>) -> String {
    match cx.library.agent_default(&cx.flags, None).result {
        Ok(DefaultAgent::Current(name)) => name,
        _ => String::new(),
    }
}

/// An agent's resource name under `parent`.
fn agent_name(parent: &str, agent: &str) -> String {
    format!("{parent}/agents/{agent}")
}

/// An agent known by its name alone (a preview's, a removal's).
fn named(parent: &str, agent: &str) -> v1::Agent {
    v1::Agent {
        name: agent_name(parent, agent),
        ..Default::default()
    }
}

/// `agent list`: `ListAgentsResponse`, every agent under `parent`.
pub(crate) fn agents(parent: &str, rows: &[AgentRow], default: &str) -> v1::ListAgentsResponse {
    v1::ListAgentsResponse {
        agents: rows.iter().map(|row| agent(parent, row, default)).collect(),
        next_page_token: String::new(),
    }
}

/// An agent as its binding declares it (the BASIC view): its engine blocks
/// keyed by engine, so `engines.<e>.model` keeps its path.
pub(crate) fn agent(parent: &str, row: &AgentRow, default: &str) -> v1::Agent {
    v1::Agent {
        name: agent_name(parent, &row.name),
        engine: row.engine.clone(),
        profiles: row.profiles.clone(),
        runtime: row.runtime.clone(),
        driving: row.driving.clone(),
        env: row.env.clone(),
        session_home: row.session_home.clone(),
        engines: blocks(&row.engines),
        default: Some(row.name == default),
        ..Default::default()
    }
}

/// `agents.<n>.engines` as the config holds it: each engine's model, effort
/// and permissions document, the document as written (rulings 133-139).
fn blocks(engines: &serde_json::Value) -> BTreeMap<String, v1::AgentEngine> {
    engines
        .as_object()
        .into_iter()
        .flatten()
        .map(|(engine, block)| {
            let word = |key: &str| {
                block
                    .get(key)
                    .and_then(serde_json::Value::as_str)
                    .unwrap_or_default()
                    .to_string()
            };
            let lined = v1::AgentEngine {
                model: word("model"),
                effort: word("effort"),
                permissions: block.get("permissions").map(api::document),
            };
            (engine.clone(), lined)
        })
        .collect()
}

/// `agent show <agent>`: the `Agent` resource (the FULL view), its
/// definition the resource and what it resolves to beside it.
pub(crate) fn agent_show(parent: &str, show: &AgentShow, default: &str) -> v1::Agent {
    v1::Agent {
        resolved: show.resolved.as_ref().map(resolved),
        resolution_error: show.resolution_error.clone(),
        requirements: show.requirements.iter().map(requirement).collect(),
        requirements_error: show.requirements_error.clone(),
        not_carried: show.not_carried.iter().map(api::not_carried).collect(),
        ..agent(parent, &show.definition, default)
    }
}

/// What an agent resolves to, its settings flat (as today).
fn resolved(agent: &ResolvedAgent) -> v1::ResolvedAgent {
    let settings = api::settings(&agent.settings);
    v1::ResolvedAgent {
        profiles: agent.profiles.clone(),
        engine: agent.engine.clone(),
        model: settings.model,
        effort: settings.effort,
        permissions: settings.permissions,
        runtime: settings.runtime,
        env: settings.env,
        context: agent.context.clone(),
        fragments: agent.fragments.clone(),
        driving: agent.driving.clone(),
        session_home: settings.session_home,
        command: agent.command.clone(),
    }
}

fn requirement(needed: &Requirement) -> v1::Requirement {
    v1::Requirement {
        command: needed.command.clone(),
        sources: needed
            .sources
            .iter()
            .map(|source| v1::RequirementSource {
                kind: source.kind.clone(),
                reference: source.reference.clone(),
            })
            .collect(),
    }
}

/// `agent edit`: the binding written, or the swap's preview (its engines and
/// what the new one gives up; nothing written).
fn edited(parent: &str, value: &AgentWritten, default: &str) -> v1::UpdateAgentResponse {
    match value {
        AgentWritten::Preview(preview) => v1::UpdateAgentResponse {
            agent: Some(named(parent, &preview.agent)),
            applied: Some(false),
            apply_command: preview.apply.clone(),
            from: preview.from.clone(),
            to: preview.to.clone(),
            not_carried: preview.not_carried.iter().map(api::not_carried).collect(),
            ..v1::UpdateAgentResponse::default()
        },
        AgentWritten::Done(row) => v1::UpdateAgentResponse {
            agent: Some(agent(parent, row, default)),
            applied: Some(true),
            ..v1::UpdateAgentResponse::default()
        },
    }
}

/// `agent remove`: the preview (what would go and the command that applies
/// it), or the removal done.
fn removed<T>(parent: &str, name: &str, removal: &Removal<T>) -> v1::DeleteAgentResponse {
    let (applied, detail, apply_command) = match removal {
        Removal::Preview(preview) => (false, preview.detail.clone(), preview.apply.clone()),
        Removal::Done(_) => (true, Vec::new(), String::new()),
    };
    v1::DeleteAgentResponse {
        agent: Some(named(parent, name)),
        applied: Some(applied),
        detail,
        apply_command,
        ..v1::DeleteAgentResponse::default()
    }
}

fn agent_request(sub: &ArgMatches) -> Result<AgentRequest, Failure> {
    Ok(AgentRequest {
        engine: given(sub, "engine"),
        model: given(sub, "model"),
        effort: given(sub, "effort"),
        profiles: given_list(sub, "profiles"),
        runtime: given(sub, "runtime"),
        surfaces: given_pairs(sub, "surface")?,
        roots: given_pairs(sub, "root")?,
        env: given_list(sub, "env"),
        session_home: given(sub, "session-home"),
    })
}

/// The binding a create or an edit wrote, in the config's shape.
fn agent_written(row: &AgentRow, verb: &str, out: &mut dyn Write) -> io::Result<()> {
    let mut parts = Vec::new();
    if !row.engine.is_empty() {
        parts.push(format!("engine: {}", row.engine));
    }
    if !row.profiles.is_empty() {
        parts.push(format!("profiles: {}", row.profiles.join(", ")));
    }
    if !row.runtime.is_empty() {
        parts.push(format!("runtime: {}", row.runtime));
    }
    // Ruling 67: what is shown is what is written; ruling 139:
    // permissions are said to be passed on unchecked.
    for line in &row.engine_lines {
        parts.push(line.line.clone());
    }
    for line in row.engine_lines.iter().filter(|l| l.permissions) {
        parts.push(format!(
            "permissions {}",
            library_application::passed_verbatim(&line.engine)
        ));
    }
    if !row.env.is_empty() {
        parts.push(format!("env: {}", row.env.join(", ")));
    }
    if !row.session_home.is_empty() {
        parts.push(format!("session_home: {}", row.session_home));
    }
    writeln!(out, "{verb} agent \"{}\" ({})", row.name, parts.join(", "))
}

/// The swap's preview: what the new engine cannot carry, and how to apply it.
fn swap_preview_text(out: &mut dyn Write, preview: &SwapPreview) -> io::Result<()> {
    writeln!(
        out,
        "Moving agent \"{}\" from {} to {} gives up what {} cannot carry:",
        preview.agent, preview.from, preview.to, preview.to
    )?;
    for lost in &preview.not_carried {
        writeln!(out, "  {}", lost.line())?;
    }
    writeln!(out)?;
    writeln!(out, "Nothing was changed. Re-run with --yes to apply:")?;
    writeln!(out, "  {}", preview.apply)
}

#[cfg(test)]
#[path = "agent.test.rs"]
mod tests;
