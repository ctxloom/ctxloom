//! `bundle`: list, show and view, and its writes (B1), each json answer its
//! AIP resource or method response (OD-40, design §5.1).

use library_application::BundleView;

use crate::catalog;
use crate::prelude::*;

pub(crate) fn read(
    cx: &Context<'_>,
    (verb, sub): (&str, &ArgMatches),
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    match verb {
        "show" => {
            // The daemon's read (step 10a U2), its text from the message (D10).
            let request = v1::GetBundleRequest {
                view: v1::bundle::View::Full as i32,
                caller: v1::Caller::of(cx.globals(), None),
                ..v1::GetBundleRequest::default()
            };
            let under = library_api::names::bundle_target(&text_arg(sub, "name"));
            let read = cx.served.get_bundle(&under, request);
            served(cx, read, out, err, false, |bundle, out, notes| {
                let mut said = text::Notes(Vec::new());
                let written =
                    text::bundle_show(out, &mut said, &crate::read_back::bundle_show(bundle));
                notes.extend(said.0);
                written
            })
        }
        "view" => viewed_by_the_daemon(cx, sub, out, err),
        _ => listed_by_the_daemon(cx, out, err),
    }
}

/// `bundle list`, the daemon's `ListBundles` (step 10a U2), its text
/// rendered from the message alone (D10).
fn listed_by_the_daemon(
    cx: &Context<'_>,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let request = v1::ListBundlesRequest {
        caller: v1::Caller::of(cx.globals(), None),
        ..v1::ListBundlesRequest::default()
    };
    let in_project = cx.served.in_project();
    served(
        cx,
        cx.served.list_bundles("", request),
        out,
        err,
        false,
        |listed, out, _| text::bundles(out, listed, in_project),
    )
}

/// `bundle view <bundle>#<path>`, one item, the daemon's read of its kind
/// (step 10a U2, owner ruling 2026-11-17): a fragment's or a command's
/// body, or a skill's, an MCP server's or a profile's fields as YAML, keys
/// sorted and references short, each rendered from the item's message
/// alone. A whole bundle (no `#`), or a path no item kind serves, is the
/// bundle's CONTENT read of it, which the daemon refuses as the view
/// refuses a path it does not hold.
fn viewed_by_the_daemon(
    cx: &Context<'_>,
    sub: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let ask = text_arg(sub, "name");
    let prefer_distilled = flag(sub, "distilled");
    let under = library_api::names::reference_target(&ask);
    let kind = ask
        .split_once('#')
        .and_then(|(_, path)| path.split_once('/'))
        .map_or("", |(kind, _)| kind);
    let render = |view: BundleView, out: &mut dyn Write, notes: &mut Vec<String>| {
        let mut said = text::Notes(Vec::new());
        let written = text::bundle_view(out, &mut said, &view, &ask);
        notes.extend(said.0);
        written
    };
    match kind {
        "fragments" => {
            let request = v1::GetFragmentRequest {
                prefer_distilled,
                caller: v1::Caller::of(cx.globals(), None),
                ..v1::GetFragmentRequest::default()
            };
            let read = cx.served.get_fragment(&under, request);
            served(cx, read, out, err, false, |f, out, notes| {
                render(
                    crate::read_back::body_view(&ask, &f.body, f.body_is_distilled),
                    out,
                    notes,
                )
            })
        }
        "commands" | "prompts" => {
            let request = v1::GetCommandRequest {
                prefer_distilled,
                caller: v1::Caller::of(cx.globals(), None),
                ..v1::GetCommandRequest::default()
            };
            let read = cx.served.get_command(&under, request);
            served(cx, read, out, err, false, |c, out, notes| {
                render(
                    crate::read_back::body_view(&ask, &c.body, c.body_is_distilled),
                    out,
                    notes,
                )
            })
        }
        "skills" => {
            let request = v1::GetSkillRequest {
                caller: v1::Caller::of(cx.globals(), None),
                ..v1::GetSkillRequest::default()
            };
            let read = cx.served.get_skill(&under, request);
            served(cx, read, out, err, false, |skill, out, notes| {
                render(crate::read_back::skill_view(&ask, skill), out, notes)
            })
        }
        "mcp" => {
            let request = v1::GetMcpServerRequest {
                caller: v1::Caller::of(cx.globals(), None),
                ..v1::GetMcpServerRequest::default()
            };
            let read = cx.served.get_mcp_server(&under, request);
            served(cx, read, out, err, false, |server, out, notes| {
                render(crate::read_back::mcp_view(&ask, server), out, notes)
            })
        }
        "profiles" => {
            let request = v1::GetProfileRequest {
                caller: v1::Caller::of(cx.globals(), None),
                ..v1::GetProfileRequest::default()
            };
            let read = cx
                .served
                .get_profile(&library_api::names::profile_target(&ask), request);
            served(cx, read, out, err, false, |profile, out, notes| {
                render(crate::read_back::profile_view(&ask, profile), out, notes)
            })
        }
        _ => {
            let request = v1::GetBundleRequest {
                view: v1::bundle::View::Content as i32,
                prefer_distilled,
                caller: v1::Caller::of(cx.globals(), None),
                ..v1::GetBundleRequest::default()
            };
            let read = cx.served.get_bundle(&under, request);
            served(cx, read, out, err, false, |bundle, out, notes| {
                render(crate::read_back::bundle_view(bundle), out, notes)
            })
        }
    }
}

pub(crate) fn write(
    cx: &Context<'_>,
    (verb, sub): (&str, &ArgMatches),
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Option<Exit>, Failure> {
    let code = match verb {
        "create" => create(cx, sub, out, err)?,
        "edit" => edit(cx, sub, out, err)?,
        "remove" => remove(cx, sub, out, err)?,
        "export" => export(cx, sub, out, err)?,
        "import" => import(cx, sub, out, err)?,
        "distill" => distill(cx, sub, out, err)?,
        _ => return Ok(None),
    };
    Ok(Some(code))
}

/// `bundle distill <bundle> [-f]` (OD-39, by name; step 10b): each of the
/// bundle's fragments, then each of its commands, as the daemon reads
/// them, distilled as `fragment|command distill` distills one: one run per
/// item, each its own write following from it (the plan's U13 notes do
/// not decide the shape; this is the simpler one), each followed to its
/// end before the next is asked. The first that fails fails the command,
/// and what was distilled before it stays.
fn distill(
    cx: &Context<'_>,
    sub: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let force = flag(sub, "force");
    // Its items as `bundle show` reads them.
    let request = v1::GetBundleRequest {
        view: v1::bundle::View::Full as i32,
        caller: v1::Caller::of(cx.globals(), None),
        ..v1::GetBundleRequest::default()
    };
    let target = library_api::names::bundle_target(&text_arg(sub, "name"));
    let (bundle, mut notices) = cx.served.get_bundle(&target, request)?;
    let items = bundle
        .fragments
        .iter()
        .map(|item| (ItemKind::Fragment, item))
        .chain(bundle.commands.iter().map(|item| (ItemKind::Command, item)));
    let mut answers = Vec::new();
    for (kind, item) in items {
        let under = item
            .item
            .find("/bundles/")
            .map_or(item.item.as_str(), |at| &item.item[at..]);
        match super::item::distill_followed(cx, kind, under, force) {
            Ok(mut answer) => {
                notices.append(answer.notices_mut());
                answers.push(answer);
            }
            Err((failure, said)) => {
                notices.extend(said);
                super::item::say(cx, &notices, err);
                return Err(failure);
            }
        }
    }
    super::item::say(cx, &notices, err);
    let entry = |answer: &super::item::DistillAnswer| {
        let (name, no_distill) = match answer {
            super::item::Either::Fragment(f) => (
                f.fragment.as_ref().map(|f| f.name.clone()),
                answer.no_distill(),
            ),
            super::item::Either::Command(c) => (
                c.command.as_ref().map(|c| c.name.clone()),
                answer.no_distill(),
            ),
        };
        v1::BundleItem {
            item: name.unwrap_or_default(),
            distilled: catalog::distilled(!no_distill),
            no_distill: Some(no_distill),
            ..v1::BundleItem::default()
        }
    };
    if cx.format == Format::Json {
        let is_command = |answer: &&super::item::DistillAnswer| {
            matches!(answer, super::item::Either::Command(_))
        };
        let message = v1::Bundle {
            name: bundle.name.clone(),
            fragments: answers
                .iter()
                .filter(|a| !is_command(a))
                .map(entry)
                .collect(),
            commands: answers.iter().filter(is_command).map(entry).collect(),
            ..v1::Bundle::default()
        };
        api::print(out, &message).map_err(output_failure)?;
    } else {
        for answer in &answers {
            super::item::distilled_text(&answer.item(), out).map_err(output_failure)?;
        }
    }
    Ok(Exit::OK)
}

/// `bundle create <name> [-d description]`: the daemon's `CreateBundle`
/// (step 10b U3), its text from the response (D10).
fn create(
    cx: &Context<'_>,
    sub: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let request = v1::CreateBundleRequest {
        bundle_id: text_arg(sub, "name"),
        bundle: Some(v1::Bundle {
            description: text_arg(sub, "description"),
            ..v1::Bundle::default()
        }),
        caller: v1::Caller::of(cx.globals(), None),
        ..v1::CreateBundleRequest::default()
    };
    crate::answer_written(
        cx,
        cx.served.create_bundle("", request),
        |made| std::mem::take(&mut made.notices),
        out,
        err,
        |made, out| {
            writeln!(out, "Created bundle: {}", made.path)?;
            writeln!(
                out,
                "Edit its item files to add your fragments and prompts."
            )
        },
        |_| Exit::OK,
    )
}

/// `bundle edit <name> [flags]`: the daemon's `UpdateBundle` (step 10b
/// U3), its mask naming the fields given (`description`, `version`) and
/// its add and remove lists as given: several mask paths, one step to the
/// bundle's one file (owner ruling 2026-11-18); a change to more than one
/// file is refused `OneChangePerCall`. Its text from the response (D10).
fn edit(
    cx: &Context<'_>,
    sub: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let mut bundle = v1::Bundle::default();
    let mut paths = Vec::new();
    if let Some(description) = given(sub, "description") {
        bundle.description = description;
        paths.push("description".to_string());
    }
    if let Some(version) = given(sub, "version") {
        bundle.version = version;
        paths.push("version".to_string());
    }
    let request = v1::UpdateBundleRequest {
        bundle: Some(bundle),
        update_mask: (!paths.is_empty())
            .then_some(daemon_proto::google::protobuf::FieldMask { paths }),
        add_tags: many(sub, "add-tag"),
        remove_tags: many(sub, "remove-tag"),
        add_fragments: many(sub, "add-fragment"),
        remove_fragments: many(sub, "remove-fragment"),
        add_commands: many(sub, "add-prompt"),
        remove_commands: many(sub, "remove-prompt"),
        add_mcp_servers: many(sub, "add-mcp"),
        remove_mcp_servers: many(sub, "remove-mcp"),
        caller: v1::Caller::of(cx.globals(), None),
        ..v1::UpdateBundleRequest::default()
    };
    let under = library_api::names::bundle_target(&text_arg(sub, "name"));
    crate::answer_written(
        cx,
        cx.served.update_bundle(&under, request),
        |m| std::mem::take(&mut m.notices),
        out,
        err,
        |m, out| {
            if m.changes.is_empty() {
                return nothing(out);
            }
            for change in &m.changes {
                writeln!(out, "{change}")?;
            }
            writeln!(out, "Updated bundle: {}", m.path)
        },
        |_| Exit::OK,
    )
}

/// `bundle remove <name> [--yes]`: the daemon's `DeleteBundle` (step 10b
/// U3), previewed and confirmed at a terminal, its text from the response.
fn remove(
    cx: &Context<'_>,
    sub: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let under = library_api::names::bundle_target(&text_arg(sub, "name"));
    crate::write::removal_served(
        cx,
        flag(sub, "yes"),
        |validate_only, answers, etag| {
            let request = v1::DeleteBundleRequest {
                validate_only,
                answers,
                etag,
                caller: v1::Caller::of(cx.globals(), None),
                ..v1::DeleteBundleRequest::default()
            };
            cx.served.delete_bundle(&under, request)
        },
        out,
        err,
        |gone, out| writeln!(out, "Removed bundle: {}", gone.path),
    )
}

/// `bundle export <name> <dest-dir>` (or `-o <path>`): the daemon's
/// `ExportBundle` (step 10b, D2's EXCEPTION): the bundle's tree put in at
/// her path, below this command's working directory (the request's `cwd`),
/// whole or not at all and never over what is there. Its text from the
/// response (D10).
fn export(
    cx: &Context<'_>,
    sub: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let request = v1::ExportBundleRequest {
        destination: text_arg(sub, "dest"),
        output: text_arg(sub, "output"),
        caller: v1::Caller::of(cx.globals(), None),
        ..v1::ExportBundleRequest::default()
    };
    let under = library_api::names::bundle_target(&text_arg(sub, "name"));
    crate::answer_written(
        cx,
        cx.served.export_bundle(&under, request),
        |m| std::mem::take(&mut m.notices),
        out,
        err,
        |m, out| writeln!(out, "Exported: {} -> {}", m.source, m.destination),
        |_| Exit::OK,
    )
}

/// `bundle import <path> [--force] [--yes]`: the daemon's `ImportBundle`
/// (step 10b U9), its source read from this command's working directory
/// (the request's `cwd`); a forced import over a bundle is its T28
/// sequence. An import asks nothing, so `--yes` changes nothing (scripts
/// may say it).
fn import(
    cx: &Context<'_>,
    sub: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let source = text_arg(sub, "path");
    let request = v1::ImportBundleRequest {
        source: source.clone(),
        force: flag(sub, "force"),
        caller: v1::Caller::of(cx.globals(), None),
        ..v1::ImportBundleRequest::default()
    };
    crate::answer_written(
        cx,
        cx.served.import_bundle("", request),
        |made| std::mem::take(&mut made.notices),
        out,
        err,
        |made, out| {
            let bundle = made.bundle.clone().unwrap_or_default();
            writeln!(out, "Imported: {source} -> {}", made.path)?;
            writeln!(out, "  Version: {}", bundle.version)?;
            writeln!(
                out,
                "  Fragments: {}, Commands: {}, MCP: {}",
                bundle.fragment_count, bundle.command_count, bundle.mcp_server_count
            )
        },
        |_| Exit::OK,
    )
}

#[cfg(test)]
#[path = "bundle.test.rs"]
mod tests;
