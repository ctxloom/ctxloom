//! `mcp server`: list and show, and its writes (B1), each json answer its
//! AIP resource or method response (OD-40, design §5.4).

use library_application::{McpList, McpRow, McpSet, McpShow};

use crate::catalog::{self, ok};
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
        return catalog::answer(
            cx,
            outcome,
            out,
            err,
            false,
            shown,
            |show, out, _| text::mcp_show(out, show),
            ok,
        );
    }
    let outcome = cx.library.mcp_list(&cx.flags);
    catalog::answer(
        cx,
        outcome,
        out,
        err,
        false,
        listed,
        |list, out, _| text::mcp_list(out, list),
        ok,
    )
}

/// A server's resource name below the bundle that serves it (`source`, its
/// canonical reference).
fn server_name(parent: &str, source: &str, id: &str) -> String {
    catalog::item_name(parent, source, "mcp", id)
}

/// A server as its `McpServer`, named `name`.
pub(crate) fn server(name: &str, row: &McpRow) -> v1::McpServer {
    v1::McpServer {
        name: name.to_string(),
        command: row.command.clone(),
        args: row.args.clone(),
        env: row.env.clone(),
        url: row.url.clone(),
        served_by: row.served_by.clone(),
        notes: row.notes.clone(),
        installation: row.installation.clone(),
        ..v1::McpServer::default()
    }
}

/// `mcp server list`: `ListMcpServersResponse`, each server below its
/// bundle (no environment, notes or installation, as listed).
pub(crate) fn listed(parent: &str, list: &McpList) -> v1::ListMcpServersResponse {
    v1::ListMcpServersResponse {
        mcp_servers: list
            .servers
            .iter()
            .map(|row| v1::McpServer {
                name: server_name(parent, &row.source, &row.name),
                command: row.command.clone(),
                args: row.args.clone(),
                url: row.url.clone(),
                served_by: row.served_by.clone(),
                ..v1::McpServer::default()
            })
            .collect(),
        next_page_token: String::new(),
    }
}

/// `mcp server show <name>`: `ListMcpServersResponse` filtered by the ID
/// (design §3.5), every bundle's server of that ID.
pub(crate) fn shown(parent: &str, show: &McpShow) -> v1::ListMcpServersResponse {
    v1::ListMcpServersResponse {
        mcp_servers: show
            .entries
            .iter()
            .map(|row| server(&server_name(parent, &row.source, &row.name), row))
            .collect(),
        next_page_token: String::new(),
    }
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
            let outcome = cx.library.mcp_edit(&cx.flags, &reference);
            catalog::answer(
                cx,
                outcome,
                out,
                err,
                true,
                |parent, edited| edited_of(parent, &reference, edited),
                |edited, out, _| match edited {
                    McpEdited::Unchanged => nothing(out),
                    McpEdited::Updated { name, bundle } => {
                        writeln!(out, "Updated MCP server \"{name}\" in bundle \"{bundle}\"")
                    }
                },
                ok,
            )
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
            catalog::answer(
                cx,
                outcome,
                out,
                err,
                false,
                set_of,
                |set, out, _| {
                    writeln!(
                        out,
                        "Set MCP server \"{}\" in bundle \"{}\"",
                        set.name, set.bundle
                    )
                },
                ok,
            )
            .map(Some)
        }
        _ => Ok(None),
    }
}

/// `mcp server set`: `UpdateMcpServerResponse` (AIP-134 `allow_missing`):
/// `created` when the bundle lacked it.
pub(crate) fn set_of(parent: &str, set: &McpSet) -> v1::UpdateMcpServerResponse {
    v1::UpdateMcpServerResponse {
        mcp_server: Some(v1::McpServer {
            name: server_name(parent, &set.bundle, &set.name),
            ..v1::McpServer::default()
        }),
        path: set.path.clone(),
        applied: Some(true),
        created: set.status == "created",
        ..v1::UpdateMcpServerResponse::default()
    }
}

/// `mcp server edit --format json`: `UpdateMcpServerResponse`; an edit
/// that changed nothing applied nothing (named by the reference asked).
pub(crate) fn edited_of(
    parent: &str,
    asked: &str,
    edited: &McpEdited,
) -> v1::UpdateMcpServerResponse {
    let (name, applied) = match edited {
        McpEdited::Unchanged => (
            catalog::reference_name(parent, asked).unwrap_or_else(|| asked.to_string()),
            false,
        ),
        McpEdited::Updated { name, bundle } => (server_name(parent, bundle, name), true),
    };
    v1::UpdateMcpServerResponse {
        mcp_server: Some(v1::McpServer {
            name,
            ..v1::McpServer::default()
        }),
        applied: Some(applied),
        ..v1::UpdateMcpServerResponse::default()
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
