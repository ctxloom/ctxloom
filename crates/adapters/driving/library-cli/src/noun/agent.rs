//! `agent`: list and show, and its writes (B2).

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
        return answer(cx, outcome, out, err, |show, out, _| {
            emit(cx.format, out, &show, |out| text::agent_show(out, &show))
        });
    }
    let outcome = cx.library.agent_list(&cx.flags);
    answer(cx, outcome, out, err, |rows, out, _| {
        emit(cx.format, out, &rows, |out| text::agent_list(out, &rows))
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
        "create" | "edit" => {
            let request = agent_request(sub)?;
            if verb == "create" {
                let outcome = cx.library.agent_write(&cx.flags, &name, &request, false);
                finish(cx, outcome, out, err, |row, out, _| {
                    agent_written(row, "Created", out)
                })?
            } else {
                let outcome = cx
                    .library
                    .agent_edit(&cx.flags, &name, &request, flag(sub, "yes"));
                crate::answer(cx, outcome, out, err, |value, out, _| {
                    emit(cx.format, out, &value, |out| match &value {
                        AgentWritten::Preview(preview) => swap_preview_text(out, preview),
                        AgentWritten::Done(row) => agent_written(row, "Updated", out),
                    })?;
                    // A preview would change the binding (ruling 153).
                    Ok(match value {
                        AgentWritten::Preview(_) => Exit::WOULD_CHANGE,
                        AgentWritten::Done(_) => Exit::OK,
                    })
                })?
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
            removal(cx, outcome, out, err, |gone, out| {
                writeln!(out, "Removed agent \"{}\"", gone.name)
            })?
        }
        _ => return Ok(None),
    };
    Ok(Some(code))
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
