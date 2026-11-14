//! `bundle`: list, show and view, and its writes (B1), each json answer its
//! AIP resource or method response (OD-40, design §5.1).

use library_application::{
    BundleCreated, BundleEdited, BundleExported, BundleImported, BundleList, BundleRemoved,
    BundleRow, BundleShow, BundleView, Removal, Viewed,
};

use crate::catalog::{self, bundle_name, ok};
use crate::prelude::*;

pub(crate) fn read(
    cx: &Context<'_>,
    (verb, sub): (&str, &ArgMatches),
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    match verb {
        "show" => {
            let outcome = cx.library.bundle_show(&cx.flags, &text_arg(sub, "name"));
            catalog::answer(
                cx,
                outcome,
                out,
                err,
                false,
                shown,
                |show, out, notes| {
                    let mut said = text::Notes(Vec::new());
                    let written = text::bundle_show(out, &mut said, show);
                    notes.extend(said.0);
                    written
                },
                ok,
            )
        }
        "view" => {
            let ask = text_arg(sub, "name");
            let outcome = cx
                .library
                .bundle_view(&cx.flags, &ask, flag(sub, "distilled"));
            catalog::answer(
                cx,
                outcome,
                out,
                err,
                false,
                viewed,
                |view, out, notes| {
                    let mut said = text::Notes(Vec::new());
                    let written = text::bundle_view(out, &mut said, view, &ask);
                    notes.extend(said.0);
                    written
                },
                ok,
            )
        }
        _ => {
            let outcome = cx.library.bundle_list(&cx.flags);
            catalog::answer(
                cx,
                outcome,
                out,
                err,
                false,
                listed,
                |list, out, _| text::bundle_list(out, list),
                ok,
            )
        }
    }
}

/// `bundle list`: `ListBundlesResponse`, the whole catalog (design §1.2).
pub(crate) fn listed(parent: &str, list: &BundleList) -> v1::ListBundlesResponse {
    v1::ListBundlesResponse {
        bundles: list.rows.iter().map(|row| bundle(parent, row)).collect(),
        next_page_token: String::new(),
    }
}

/// A `bundle list` row as its `Bundle` (BASIC).
pub(crate) fn bundle(parent: &str, row: &BundleRow) -> v1::Bundle {
    v1::Bundle {
        name: bundle_name(parent, &row.name),
        origin: catalog::origin(&row.name, row.origin == "ctxloom/builtin") as i32,
        reference: row.reference.clone(),
        commit: row.commit.clone(),
        path: row.path.clone(),
        version: row.version.clone(),
        description: row.description.clone(),
        tags: row.tags.clone(),
        fragment_count: count(row.fragment_count),
        command_count: count(row.command_count),
        mcp_server_count: count(row.mcp_count),
        profile_count: count(row.profile_count),
        ..v1::Bundle::default()
    }
}

/// A count as the API's uint32 (OQ-12), saturating.
fn count(n: u64) -> u32 {
    u32::try_from(n).unwrap_or(u32::MAX)
}

/// `bundle show`: the `Bundle` (FULL), each child summarised by its name
/// (OQ-14).
pub(crate) fn shown(parent: &str, show: &BundleShow) -> v1::Bundle {
    let item = |dir: &str, id: &str| catalog::item_name(parent, &show.name, dir, id);
    v1::Bundle {
        author: show.author.clone(),
        notes: show.notes.clone(),
        installation: show.installation.clone(),
        description: show.description.clone(),
        version: show.version.clone(),
        tags: show.tags.clone(),
        path: show.path.clone(),
        skill_count: count(show.skills.len() as u64),
        fragments: show
            .fragments
            .iter()
            .map(|(id, f)| v1::BundleItem {
                item: item("fragments", id),
                tags: f.tags.clone(),
                preview: f.preview.clone(),
                distilled: catalog::shown_distilled(f.distilled, f.stale),
                no_distill: Some(f.no_distill),
                premise: f.premise.clone(),
                ..v1::BundleItem::default()
            })
            .collect(),
        commands: show
            .commands
            .iter()
            .map(|(id, c)| v1::BundleItem {
                item: item("commands", id),
                tags: c.tags.clone(),
                description: c.description.clone(),
                preview: c.preview.clone(),
                distilled: catalog::shown_distilled(c.distilled, c.stale),
                no_distill: Some(c.no_distill),
                ..v1::BundleItem::default()
            })
            .collect(),
        skills: show
            .skills
            .iter()
            .map(|(id, s)| v1::BundleItem {
                item: item("skills", id),
                tags: s.tags.clone(),
                notes: s.notes.clone(),
                path: s.path.clone(),
                ..v1::BundleItem::default()
            })
            .collect(),
        mcp_servers: show
            .mcp
            .iter()
            .map(|(id, m)| v1::BundleItem {
                item: item("mcp", id),
                command: m.command.clone(),
                args: m.args.clone(),
                env: m.env.clone(),
                notes: m.notes.clone(),
                installation: m.installation.clone(),
                ..v1::BundleItem::default()
            })
            .collect(),
        profiles: show
            .profiles
            .iter()
            .map(|id| item("profiles", id))
            .collect(),
        hooks: show.hooks.clone(),
        ..bundle(parent, &named(show))
    }
}

/// The show's listing, under the show's name (a listing the view did not
/// fill is the name alone).
fn named(show: &BundleShow) -> BundleRow {
    BundleRow {
        name: show.name.clone(),
        ..show.listing.clone()
    }
}

/// What `bundle view` answers: the bundle, or the item the path selects.
#[derive(serde::Serialize)]
#[serde(untagged)]
pub(crate) enum ViewedMessage {
    Bundle(v1::Bundle),
    Fragment(v1::Fragment),
    Command(v1::Command),
    Skill(v1::Skill),
    McpServer(v1::McpServer),
    Profile(v1::Profile),
}

/// `bundle view`: the whole bundle as a `Bundle` (CONTENT), or the one item
/// the path selects as its own resource (design §5.1).
pub(crate) fn viewed(parent: &str, view: &BundleView) -> ViewedMessage {
    let bundle = bundle_name(parent, &view.bundle);
    let item = |dir: &str, id: &str| catalog::item_name(parent, &view.bundle, dir, id);
    match &view.viewed {
        Viewed::Body {
            kind,
            name,
            body,
            distilled,
        } if kind == "commands" => ViewedMessage::Command(v1::Command {
            name: item("commands", name),
            body: body.clone(),
            body_is_distilled: *distilled,
            ..v1::Command::default()
        }),
        Viewed::Body {
            name,
            body,
            distilled,
            ..
        } => ViewedMessage::Fragment(v1::Fragment {
            name: item("fragments", name),
            body: body.clone(),
            body_is_distilled: *distilled,
            ..v1::Fragment::default()
        }),
        Viewed::Skill { name, skill } => ViewedMessage::Skill(v1::Skill {
            name: item("skills", name),
            tags: skill.tags.clone(),
            notes: skill.notes.clone(),
            path: skill.path.clone(),
            ..v1::Skill::default()
        }),
        Viewed::Mcp(row) => {
            ViewedMessage::McpServer(crate::noun::mcp::server(&item("mcp", &row.name), row))
        }
        Viewed::Profile(profile) => ViewedMessage::Profile(crate::noun::profile::profile_of(
            parent,
            &item("profiles", &profile.name),
            profile,
        )),
        Viewed::Files(files) => ViewedMessage::Bundle(v1::Bundle {
            name: bundle,
            files: files
                .iter()
                .map(|(path, content)| v1::BundleFile {
                    path: path.clone(),
                    content: content.clone(),
                })
                .collect(),
            distilled: Some(view.distilled),
            ..v1::Bundle::default()
        }),
        Viewed::Nothing => ViewedMessage::Bundle(v1::Bundle {
            name: bundle,
            ..v1::Bundle::default()
        }),
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

/// `bundle distill <bundle> [-f]` (OD-39: by name): each item's line in
/// text; in json the `Bundle`, each of its items with the distilled state
/// it was left in.
fn distill(
    cx: &Context<'_>,
    sub: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let outcome = cx
        .library
        .bundle_distill(&cx.flags, &text_arg(sub, "name"), flag(sub, "force"));
    catalog::answer(
        cx,
        outcome,
        out,
        err,
        false,
        |parent, done| {
            let entry = |i: &library_application::ItemDistilled, dir: &str| v1::BundleItem {
                item: catalog::item_name(parent, &done.bundle, dir, &i.name),
                distilled: catalog::distilled(i.status != "no_distill"),
                no_distill: Some(i.status == "no_distill"),
                ..v1::BundleItem::default()
            };
            v1::Bundle {
                name: catalog::bundle_name(parent, &done.bundle),
                fragments: done
                    .items
                    .iter()
                    .filter(|i| i.kind == "fragment")
                    .map(|i| entry(i, "fragments"))
                    .collect(),
                commands: done
                    .items
                    .iter()
                    .filter(|i| i.kind == "command")
                    .map(|i| entry(i, "commands"))
                    .collect(),
                ..v1::Bundle::default()
            }
        },
        |done, out, _| {
            for item in &done.items {
                super::item::distilled_text(item, out)?;
            }
            Ok(())
        },
        ok,
    )
}

fn create(
    cx: &Context<'_>,
    sub: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let name = text_arg(sub, "name");
    let outcome = cx
        .library
        .bundle_create(&cx.flags, &name, &text_arg(sub, "description"));
    catalog::answer(
        cx,
        outcome,
        out,
        err,
        false,
        created,
        |made, out, _| {
            writeln!(out, "Created bundle: {}", made.path)?;
            writeln!(
                out,
                "Edit its item files to add your fragments and prompts."
            )
        },
        catalog::ok,
    )
}

fn edit(
    cx: &Context<'_>,
    sub: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let name = text_arg(sub, "name");
    let request = BundleEditRequest {
        name,
        description: given(sub, "description"),
        version: given(sub, "version"),
        add_tags: many(sub, "add-tag"),
        remove_tags: many(sub, "remove-tag"),
        add_fragments: many(sub, "add-fragment"),
        remove_fragments: many(sub, "remove-fragment"),
        add_prompts: many(sub, "add-prompt"),
        remove_prompts: many(sub, "remove-prompt"),
        add_mcp: many(sub, "add-mcp"),
        remove_mcp: many(sub, "remove-mcp"),
    };
    let outcome = cx.library.bundle_edit(&cx.flags, &request);
    catalog::answer(
        cx,
        outcome,
        out,
        err,
        false,
        edited,
        |edited, out, _| {
            if edited.changes.is_empty() {
                return nothing(out);
            }
            for change in &edited.changes {
                writeln!(out, "{change}")?;
            }
            writeln!(out, "Updated bundle: {}", edited.path)
        },
        catalog::ok,
    )
}

fn remove(
    cx: &Context<'_>,
    sub: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let name = text_arg(sub, "name");
    let outcome = cx.library.bundle_remove(&cx.flags, &name, flag(sub, "yes"));
    catalog::answer(
        cx,
        outcome,
        out,
        err,
        false,
        |parent, gone| removed(parent, &name, gone),
        |gone, out, _| {
            catalog::removal_text(gone, out, |gone, out| {
                writeln!(out, "Removed bundle: {}", gone.path)
            })
        },
        crate::write::removal_exit,
    )
}

fn export(
    cx: &Context<'_>,
    sub: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let name = text_arg(sub, "name");
    let outcome = cx.library.bundle_export(
        &cx.flags,
        &name,
        &text_arg(sub, "dest"),
        &text_arg(sub, "output"),
    );
    catalog::answer(
        cx,
        outcome,
        out,
        err,
        false,
        exported,
        |done, out, _| writeln!(out, "Exported: {} -> {}", done.source, done.dest),
        catalog::ok,
    )
}

fn import(
    cx: &Context<'_>,
    sub: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let outcome = cx
        .library
        .bundle_import(&cx.flags, &text_arg(sub, "path"), flag(sub, "force"));
    catalog::answer(
        cx,
        outcome,
        out,
        err,
        false,
        imported,
        |done, out, _| {
            writeln!(out, "Imported: {} -> {}", done.source, done.dest)?;
            writeln!(out, "  Version: {}", done.version)?;
            writeln!(
                out,
                "  Fragments: {}, Commands: {}, MCP: {}",
                done.fragments, done.commands, done.mcp
            )
        },
        catalog::ok,
    )
}

/// A bundle named and nothing else, as a write answers it.
fn just_named(parent: &str, catalog: &str) -> Option<v1::Bundle> {
    Some(v1::Bundle {
        name: bundle_name(parent, catalog),
        ..v1::Bundle::default()
    })
}

/// `bundle create`: `CreateBundleResponse`.
pub(crate) fn created(parent: &str, made: &BundleCreated) -> v1::CreateBundleResponse {
    v1::CreateBundleResponse {
        bundle: just_named(parent, &made.name),
        path: made.path.clone(),
        applied: Some(true),
        ..v1::CreateBundleResponse::default()
    }
}

/// `bundle edit`: `UpdateBundleResponse`; an edit that changed nothing
/// applied nothing and lists no change.
pub(crate) fn edited(parent: &str, edited: &BundleEdited) -> v1::UpdateBundleResponse {
    v1::UpdateBundleResponse {
        bundle: just_named(parent, &edited.name),
        path: edited.path.clone(),
        applied: Some(!edited.changes.is_empty()),
        changes: edited.changes.clone(),
        ..v1::UpdateBundleResponse::default()
    }
}

/// `bundle remove`: `DeleteBundleResponse`, the preview's or the removal's.
pub(crate) fn removed(
    parent: &str,
    asked: &str,
    gone: &Removal<BundleRemoved>,
) -> v1::DeleteBundleResponse {
    catalog::removal(
        gone,
        |preview| v1::DeleteBundleResponse {
            bundle: just_named(parent, asked),
            applied: Some(false),
            detail: preview.detail,
            apply_command: preview.apply_command,
            ..v1::DeleteBundleResponse::default()
        },
        |done| v1::DeleteBundleResponse {
            bundle: just_named(parent, &done.name),
            path: done.path.clone(),
            applied: Some(true),
            ..v1::DeleteBundleResponse::default()
        },
    )
}

/// `bundle export`: `ExportBundleResponse`.
pub(crate) fn exported(parent: &str, done: &BundleExported) -> v1::ExportBundleResponse {
    v1::ExportBundleResponse {
        bundle: bundle_name(parent, &done.name),
        source: done.source.clone(),
        destination: done.dest.clone(),
        applied: Some(true),
        ..v1::ExportBundleResponse::default()
    }
}

/// `bundle import`: `ImportBundleResponse`, the bundle with its version and
/// counts, named by the directory it landed in.
pub(crate) fn imported(parent: &str, done: &BundleImported) -> v1::ImportBundleResponse {
    let name = done
        .dest
        .trim_end_matches(['/', '\\'])
        .rsplit(['/', '\\'])
        .next()
        .unwrap_or_default();
    v1::ImportBundleResponse {
        bundle: Some(v1::Bundle {
            name: bundle_name(parent, name),
            version: done.version.clone(),
            fragment_count: count(done.fragments),
            command_count: count(done.commands),
            mcp_server_count: count(done.mcp),
            ..v1::Bundle::default()
        }),
        path: done.dest.clone(),
        applied: Some(true),
        ..v1::ImportBundleResponse::default()
    }
}

#[cfg(test)]
#[path = "bundle.test.rs"]
mod tests;
