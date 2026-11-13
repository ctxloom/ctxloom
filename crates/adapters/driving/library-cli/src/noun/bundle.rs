//! `bundle`: list, show and view, and its writes (B1).

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
            answer(cx, outcome, out, err, |show, out, notes| {
                emit(cx.format, out, &show, |out| {
                    let mut said = text::Notes(Vec::new());
                    let written = text::bundle_show(out, &mut said, &show);
                    notes.extend(said.0);
                    written
                })
            })
        }
        "view" => {
            let ask = text_arg(sub, "name");
            let outcome = cx
                .library
                .bundle_view(&cx.flags, &ask, flag(sub, "distilled"));
            answer(cx, outcome, out, err, |view, out, notes| {
                emit(cx.format, out, &view, |out| {
                    let mut said = text::Notes(Vec::new());
                    let written = text::bundle_view(out, &mut said, &view, &ask);
                    notes.extend(said.0);
                    written
                })
            })
        }
        _ => {
            let outcome = cx.library.bundle_list(&cx.flags);
            answer(cx, outcome, out, err, |list, out, _| {
                emit(cx.format, out, &list.rows, |out| {
                    text::bundle_list(out, &list)
                })
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
    let name = text_arg(sub, "name");
    let code = match verb {
        "create" => {
            let outcome = cx
                .library
                .bundle_create(&cx.flags, &name, &text_arg(sub, "description"));
            finish(cx, outcome, out, err, |made, out, _| {
                writeln!(out, "Created bundle: {}", made.path)?;
                writeln!(
                    out,
                    "Edit its item files to add your fragments and prompts."
                )
            })?
        }
        "edit" => {
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
            finish(cx, outcome, out, err, |edited, out, _| {
                if edited.changes.is_empty() {
                    return nothing(out);
                }
                for change in &edited.changes {
                    writeln!(out, "{change}")?;
                }
                writeln!(out, "Updated bundle: {}", edited.path)
            })?
        }
        "remove" => {
            let outcome = cx.library.bundle_remove(&cx.flags, &name, flag(sub, "yes"));
            removal(cx, outcome, out, err, |gone, out| {
                writeln!(out, "Removed bundle: {}", gone.path)
            })?
        }
        "export" => {
            let outcome = cx.library.bundle_export(
                &cx.flags,
                &name,
                &text_arg(sub, "dest"),
                &text_arg(sub, "output"),
            );
            finish(cx, outcome, out, err, |done, out, _| {
                writeln!(out, "Exported: {} -> {}", done.source, done.dest)
            })?
        }
        "import" => {
            let outcome =
                cx.library
                    .bundle_import(&cx.flags, &text_arg(sub, "path"), flag(sub, "force"));
            finish(cx, outcome, out, err, |done, out, _| {
                writeln!(out, "Imported: {} -> {}", done.source, done.dest)?;
                writeln!(out, "  Version: {}", done.version)?;
                writeln!(
                    out,
                    "  Fragments: {}, Commands: {}, MCP: {}",
                    done.fragments, done.commands, done.mcp
                )
            })?
        }
        _ => return Ok(None),
    };
    Ok(Some(code))
}

#[cfg(test)]
#[path = "bundle.test.rs"]
mod tests;
