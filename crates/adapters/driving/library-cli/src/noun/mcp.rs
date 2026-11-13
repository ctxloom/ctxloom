//! `mcp server`: list and show, and its writes (B1).

use crate::prelude::*;

pub(crate) fn read(
    cx: &Context<'_>,
    sub: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let Some(("server", server)) = sub.subcommand() else {
        return Err(mcp_bare(cx));
    };
    let (name, args) = verb(server, "list");
    if let Some(code) = write(cx, (name, args), out, err)? {
        return Ok(code);
    }
    if name == "show" {
        let outcome = cx.library.mcp_show(&cx.flags, &text_arg(args, "name"));
        return answer(cx, outcome, out, err, |show, out, _| {
            emit(cx.format, out, &show, |out| text::mcp_show(out, &show))
        });
    }
    let outcome = cx.library.mcp_list(&cx.flags);
    answer(cx, outcome, out, err, |list, out, _| {
        emit(cx.format, out, &list, |out| text::mcp_list(out, &list))
    })
}

pub(crate) fn write(
    cx: &Context<'_>,
    (verb, sub): (&str, &ArgMatches),
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Option<Exit>, Failure> {
    let reference = text_arg(sub, "ref");
    match verb {
        "edit" => {
            text_only(cx, "mcp server edit")?;
            let outcome = cx.library.mcp_edit(&cx.flags, &reference);
            finish_text(cx, outcome, out, err, |edited, out, _| match edited {
                McpEdited::Unchanged => nothing(out),
                McpEdited::Updated { name, bundle } => {
                    writeln!(out, "Updated MCP server \"{name}\" in bundle \"{bundle}\"")
                }
            })
            .map(Some)
        }
        "set" => {
            let patch = McpPatch {
                command: given(sub, "command"),
                args: given_list(sub, "arg"),
                env: given_map(sub, "env")?,
                url: given(sub, "url"),
                headers: given_map(sub, "header")?,
                tags: given_list(sub, "tag"),
                served_by: given(sub, "served-by"),
                notes: given(sub, "notes"),
                installation: given(sub, "installation"),
            };
            let outcome = cx.library.mcp_set(&cx.flags, &reference, &patch);
            finish(cx, outcome, out, err, |set, out, _| {
                writeln!(
                    out,
                    "Set MCP server \"{}\" in bundle \"{}\"",
                    set.name, set.bundle
                )
            })
            .map(Some)
        }
        _ => Ok(None),
    }
}

/// A bare `mcp`: no stdio server to point a client at.
pub(crate) fn mcp_bare(cx: &Context<'_>) -> Failure {
    if cx.explicit && cx.format == Format::Json {
        return AppError::from(WriteError::McpBareJson {}).into_failure();
    }
    AppError::from(WriteError::McpBare {}).into_failure()
}

#[cfg(test)]
#[path = "mcp.test.rs"]
mod tests;
