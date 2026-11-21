//! `mcp server`: list and show, and its writes (B1), each json answer its
//! AIP resource or method response (OD-40, design §5.4).

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
    // The daemon's read (step 10a U2), `show` its listing of one ID (design
    // §3.5), the text from the message (D10).
    let request = v1::ListMcpServersRequest {
        mcp_server_id: if name == "show" {
            text_arg(args, "name")
        } else {
            String::new()
        },
        caller: v1::Caller::of(cx.globals(), None),
        ..v1::ListMcpServersRequest::default()
    };
    let read = cx.served.list_mcp_servers("", request);
    served(cx, read, out, err, false, |listed, out, _| {
        if name == "show" {
            text::mcp_show(out, &crate::read_back::mcp_show(listed))
        } else {
            text::mcp_list(out, &crate::read_back::mcp_list(listed))
        }
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
        "edit" => edit_served(cx, &reference, out, err).map(Some),
        "set" => set_served(cx, &reference, sub, out, err).map(Some),
        _ => Ok(None),
    }
}

/// The fields `mcp server set` was given, as a server and the mask naming
/// them (a field given empty is named, and cleared).
pub(crate) fn server_set(sub: &ArgMatches) -> Result<(v1::McpServer, Vec<String>), Failure> {
    let mut server = v1::McpServer::default();
    let mut mask = Vec::new();
    if let Some(command) = given(sub, "command") {
        server.command = command;
        mask.push("command".to_string());
    }
    if let Some(args) = given_list(sub, "arg") {
        server.args = args;
        mask.push("args".to_string());
    }
    if let Some(env) = given_map(sub, "env")? {
        server.env = env.into_iter().collect();
        mask.push("env".to_string());
    }
    if let Some(url) = given(sub, "url") {
        server.url = url;
        mask.push("url".to_string());
    }
    if let Some(notes) = given(sub, "notes") {
        server.notes = notes;
        mask.push("notes".to_string());
    }
    if let Some(installation) = given(sub, "installation") {
        server.installation = installation;
        mask.push("installation".to_string());
    }
    Ok((server, mask))
}

/// The bundle and server a server's resource name names.
fn bundle_and_server(name: &str) -> (String, String) {
    let rest = name.split_once("/bundles/").map_or(name, |(_, rest)| rest);
    let mut parts = rest.splitn(3, '/');
    let bundle = parts.next().unwrap_or_default().to_string();
    let server = parts.nth(1).unwrap_or_default().to_string();
    (bundle, server)
}

/// `mcp server set <bundle>#mcp/<name> [flags]`: the daemon's
/// `UpdateMcpServer` (step 10b U3), the fields given named by its mask; a
/// name the bundle lacks is created (`allow_missing`).
fn set_served(
    cx: &Context<'_>,
    reference: &str,
    sub: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let (server, paths) = server_set(sub)?;
    let request = v1::UpdateMcpServerRequest {
        mcp_server: Some(server),
        update_mask: Some(daemon_proto::google::protobuf::FieldMask { paths }),
        allow_missing: true,
        caller: v1::Caller::of(cx.globals(), None),
        ..v1::UpdateMcpServerRequest::default()
    };
    let under = library_api::names::reference_target(reference);
    crate::answer_written(
        cx,
        cx.served.update_mcp_server(&under, request),
        |m| std::mem::take(&mut m.notices),
        out,
        err,
        |m, out| {
            let name = m.mcp_server.as_ref().map_or("", |s| s.name.as_str());
            let (bundle, server) = bundle_and_server(name);
            writeln!(out, "Set MCP server \"{server}\" in bundle \"{bundle}\"")
        },
        |_| Exit::OK,
    )
}

/// `mcp server edit <bundle>#mcp/<name>`: the daemon's `UpdateMcpServer`
/// with no mask, the whole server through the editor (step 10b U3): its
/// preview's Edit need presented (the editor launched here), the apply
/// sent with what it left and the preview's etag. It answers in text unless
/// json is asked for by name (Go's format debt).
fn edit_served(
    cx: &Context<'_>,
    reference: &str,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let under = library_api::names::reference_target(reference);
    let ask =
        |validate_only: bool, answers: Vec<v1::Answer>, etag: String| v1::UpdateMcpServerRequest {
            validate_only,
            answers,
            etag,
            caller: v1::Caller::of(cx.globals(), cx.presentable()),
            ..v1::UpdateMcpServerRequest::default()
        };
    let preview = cx
        .served
        .update_mcp_server(&under, ask(true, Vec::new(), String::new()))?;
    let answers = crate::noun::item::edit_answers(
        cx,
        &preview.needs,
        library_application::Notice::NoEditor {
            verb: "mcp edit".to_string(),
            instead: "change it with `ctxloom mcp set`".to_string(),
            template: false,
        },
        err,
    )?;
    let edited = cx
        .served
        .update_mcp_server(&under, ask(false, answers, preview.etag));
    let json_by_name = cx.explicit && cx.format == Format::Json;
    let cx = cx.formatted(if json_by_name {
        Format::Json
    } else {
        Format::Text
    });
    crate::answer_written(
        &cx,
        edited,
        |m| std::mem::take(&mut m.notices),
        out,
        err,
        |m, out| {
            if !m.applied.unwrap_or(false) {
                return nothing(out);
            }
            let name = m.mcp_server.as_ref().map_or("", |s| s.name.as_str());
            let (bundle, server) = bundle_and_server(name);
            writeln!(
                out,
                "Updated MCP server \"{server}\" in bundle \"{bundle}\""
            )
        },
        |_| Exit::OK,
    )
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
