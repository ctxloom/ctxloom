//! `agent`: list and show, and its writes (B2), each as its API resource or
//! its method's response (OD-40, design §3.7, §5.6). `agent default <agent>`
//! answers its response; the bare `agent default` still answers in text only.

use crate::prelude::*;
use library_application::Notice;

pub(crate) fn read(
    cx: &Context<'_>,
    (verb, sub): (&str, &ArgMatches),
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    // The daemon's reads (step 10a U2), their text from the message (D10).
    if verb == "show" {
        let request = v1::GetAgentRequest {
            caller: v1::Caller::of(cx.globals(), None),
            ..v1::GetAgentRequest::default()
        };
        let under = library_api::names::named_target("agents", &text_arg(sub, "name"));
        let read = cx.served.get_agent(&under, request);
        return served(cx, read, out, err, false, |agent, out, _| {
            text::agent_show(out, &crate::read_back::agent_show(agent))
        });
    }
    let request = v1::ListAgentsRequest {
        caller: v1::Caller::of(cx.globals(), None),
        ..v1::ListAgentsRequest::default()
    };
    let read = cx.served.list_agents("", request);
    served(cx, read, out, err, false, |listed, out, _| {
        text::agent_list(out, &crate::read_back::agent_rows(listed))
    })
}

pub(crate) fn write(
    cx: &Context<'_>,
    (verb, sub): (&str, &ArgMatches),
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Option<Exit>, Failure> {
    let name = text_arg(sub, "name");
    let code = match verb {
        "create" => created(cx, &name, sub, out, err)?,
        "edit" => edited(cx, &name, sub, out, err)?,
        "default" if given(sub, "name").is_some() => {
            // The daemon's `SetDefaultAgent` (step 10b U3), its text from
            // the response (D10), its json the response (json off a
            // terminal, as every verb's: smoke 2026-11-19).
            let request = v1::SetDefaultAgentRequest {
                agent: name.clone(),
                caller: v1::Caller::of(cx.globals(), None),
                ..v1::SetDefaultAgentRequest::default()
            };
            let answered = cx.served.set_default_agent("", request);
            let undefined = answered
                .as_ref()
                .ok()
                .filter(|m| !m.defined)
                .map(|m| m.agent.clone());
            let code = crate::answer_written(
                cx,
                answered,
                |m| std::mem::take(&mut m.notices),
                out,
                err,
                |m, out| writeln!(out, "Set default agent to \"{}\".", m.agent),
                |_| Exit::OK,
            )?;
            if let Some(agent) = undefined {
                let note = Notice::text(format!(
                    "agent \"{agent}\" is not defined yet; define it with `ctxloom agent create {agent}`"
                ));
                say_notices(&[note], crate::PROG, cx.format, cx.catalog, err);
            }
            code
        }
        "default" => {
            // The current default: still read in process (a default may name
            // an agent no listing has yet).
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
            let under = library_api::names::named_target("agents", &name);
            crate::write::removal_served(
                cx,
                flag(sub, "yes"),
                |validate_only, answers, etag| {
                    let request = v1::DeleteAgentRequest {
                        validate_only,
                        answers,
                        etag,
                        caller: v1::Caller::of(cx.globals(), None),
                        ..v1::DeleteAgentRequest::default()
                    };
                    cx.served.delete_agent(&under, request)
                },
                out,
                err,
                |_, out| writeln!(out, "Removed agent \"{name}\""),
            )?
        }
        _ => return Ok(None),
    };
    Ok(Some(code))
}

/// The fields `agent create|edit` was given, as an Agent, and the mask
/// naming them (a field given empty is named, and cleared).
pub(crate) fn agent_given(sub: &ArgMatches) -> Result<(v1::Agent, Vec<String>), Failure> {
    let mut agent = <v1::Agent as Default>::default();
    let mut mask = Vec::new();
    let mut text = |path: &str, flag: &str, field: &mut String| {
        if let Some(value) = given(sub, flag) {
            *field = value;
            mask.push(path.to_string());
        }
    };
    text("engine", "engine", &mut agent.engine);
    text("model", "model", &mut agent.model);
    text("effort", "effort", &mut agent.effort);
    if let Some(profiles) = given_list(sub, "profiles") {
        agent.profiles = profiles;
        mask.push("profiles".to_string());
    }
    if let Some(runtime) = given(sub, "runtime") {
        agent.runtime = runtime;
        mask.push("runtime".to_string());
    }
    if let Some(surfaces) = given_pairs(sub, "surface")? {
        agent.surfaces = surfaces.into_iter().collect();
        mask.push("surfaces".to_string());
    }
    if let Some(roots) = given_pairs(sub, "root")? {
        agent.roots = roots.into_iter().collect();
        mask.push("roots".to_string());
    }
    if let Some(env) = given_list(sub, "env") {
        agent.env = env;
        mask.push("env".to_string());
    }
    if let Some(home) = given(sub, "session-home") {
        agent.session_home = home;
        mask.push("session_home".to_string());
    }
    Ok((agent, mask))
}

/// `agent create <name> [flags] [--home]`: the daemon's `CreateAgent` (step
/// 10b U3), its text from the response (D10).
fn created(
    cx: &Context<'_>,
    name: &str,
    sub: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let (agent, _) = agent_given(sub)?;
    let request = v1::CreateAgentRequest {
        agent_id: name.to_string(),
        agent: Some(agent),
        home: flag(sub, "home"),
        caller: v1::Caller::of(cx.globals(), None),
        ..v1::CreateAgentRequest::default()
    };
    crate::answer_written(
        cx,
        cx.served.create_agent("", request),
        |m| std::mem::take(&mut m.notices),
        out,
        err,
        |m, out| {
            let agent = m.agent.clone().unwrap_or_default();
            agent_written(&crate::read_back::agent_row(&agent), "Created", out)
        },
        |_| Exit::OK,
    )
}

/// `agent edit <name> [flags] [--home] [--yes]`: the daemon's `UpdateAgent`
/// (step 10b U3), the fields given named by its mask. Without `--yes` it is
/// previewed first: a swap whose new engine cannot carry what the old one
/// did is that preview, whose Confirm need is asked at a terminal (owner
/// ruling 2026-11-18, evening, as a removal's is) and on a yes applied with
/// the answer and the preview's etag; off a terminal, or on a no, the
/// preview is printed and nothing applied (exit 1 under
/// `--detailed-exitcode`, ruling 153). Any other edit is applied with the
/// preview's etag. An apply refused stale is previewed again and ends in
/// the refusal. `--yes` is one call.
fn edited(
    cx: &Context<'_>,
    name: &str,
    sub: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let (agent, paths) = agent_given(sub)?;
    let under = library_api::names::named_target("agents", name);
    let ask = |validate_only: bool, answers: Vec<v1::Answer>, etag: String| {
        let request = v1::UpdateAgentRequest {
            agent: Some(agent.clone()),
            update_mask: Some(daemon_proto::google::protobuf::FieldMask {
                paths: paths.clone(),
            }),
            home: flag(sub, "home"),
            validate_only,
            etag,
            answers,
            caller: v1::Caller::of(cx.globals(), None),
            ..v1::UpdateAgentRequest::default()
        };
        cx.served.update_agent(&under, request)
    };
    let written = if flag(sub, "yes") {
        ask(false, Vec::new(), String::new())
    } else {
        let preview = ask(true, Vec::new(), String::new())?;
        match (is_swap(&preview), confirmed(cx, &preview)) {
            (false, _) => ask(false, Vec::new(), preview.etag),
            (true, Some(answers)) => ask(false, answers, preview.etag),
            (true, None) => Ok(preview),
        }
    };
    // What she was shown has changed since: the apply is refused stale,
    // and she is shown the edit as it is now, to look again.
    if let Err(stale) = &written {
        if stale.cause.code() == crate::write::STALE {
            if let Ok(again) = ask(true, Vec::new(), String::new()) {
                if is_swap(&again) {
                    swap_preview_text(out, &swap_of(name, &again))
                        .map_err(crate::output_failure)?;
                }
            }
        }
    }
    crate::answer_written(
        cx,
        written,
        |m| std::mem::take(&mut m.notices),
        out,
        err,
        |m, out| {
            if is_swap(m) {
                return swap_preview_text(out, &swap_of(name, m));
            }
            let agent = m.agent.clone().unwrap_or_default();
            agent_written(&crate::read_back::agent_row(&agent), "Updated", out)
        },
        // A preview would change the binding (ruling 153).
        |m| {
            if is_swap(m) {
                Exit::WOULD_CHANGE
            } else {
                Exit::OK
            }
        },
    )
}

/// The answers to a swap preview's Confirm need, when it asks one and she
/// says yes at her terminal; none off a terminal, on a no, or with no need.
fn confirmed(cx: &Context<'_>, preview: &v1::UpdateAgentResponse) -> Option<Vec<v1::Answer>> {
    let (id, confirm) = preview.needs.iter().find_map(|need| match &need.kind {
        Some(v1::need::Kind::Confirm(confirm)) => Some((need.id.as_str(), confirm)),
        _ => None,
    })?;
    (cx.served.confirm(&confirm.text) == Some(true)).then(|| {
        vec![v1::Answer {
            need_id: id.to_string(),
            answer: Some(v1::answer::Answer::Confirmed(true)),
        }]
    })
}

/// Whether an edit's answer is a swap's preview (nothing applied).
fn is_swap(answer: &v1::UpdateAgentResponse) -> bool {
    !answer.to.is_empty() && answer.applied != Some(true)
}

/// A swap's preview as its answer says it.
fn swap_of(agent: &str, answer: &v1::UpdateAgentResponse) -> SwapPreview {
    SwapPreview {
        status: "preview".to_string(),
        applied: false,
        agent: agent.to_string(),
        from: answer.from.clone(),
        to: answer.to.clone(),
        not_carried: answer
            .not_carried
            .iter()
            .map(crate::read_back::not_carried)
            .collect(),
        apply: answer.apply_command.clone(),
    }
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
