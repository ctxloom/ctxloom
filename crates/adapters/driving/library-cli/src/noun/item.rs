//! `fragment` and `command`: list, show and `fragment premises`, and their writes (B1).

use crate::prelude::*;

pub(crate) fn read(
    cx: &Context<'_>,
    kind: &str,
    (verb, sub): (&str, &ArgMatches),
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let kind_of = if kind == "fragment" {
        ItemKind::Fragment
    } else {
        ItemKind::Command
    };
    match verb {
        "show" => {
            if cx.explicit && cx.format == Format::Json {
                return Err(AppError::UnsupportedFormat {
                    command: format!("{kind} show"),
                    format: "json".to_string(),
                }
                .into_failure());
            }
            let outcome = cx.library.item_show(
                &cx.flags,
                kind_of,
                &text_arg(sub, "ref"),
                flag(sub, "distilled"),
            );
            answer(cx, outcome, out, err, |show, out, notes| {
                let mut said = text::Notes(Vec::new());
                let written = text::item_show(out, &mut said, &show);
                notes.extend(said.0);
                written.map(|()| Exit::OK)
            })
        }
        "premises" => {
            let outcome = cx.library.premises(&cx.flags);
            answer(cx, outcome, out, err, |premises, out, _| {
                emit(cx.format, out, &premises, |out| {
                    text::premises(out, &premises)
                })
            })
        }
        _ => {
            let outcome = cx
                .library
                .item_list(&cx.flags, kind_of, &text_arg(sub, "bundle"));
            answer(cx, outcome, out, err, |list, out, _| {
                emit(cx.format, out, &list.rows, |out| {
                    text::item_list(out, &list)
                })
            })
        }
    }
}

pub(crate) fn write(
    cx: &Context<'_>,
    kind: ItemKind,
    (verb, sub): (&str, &ArgMatches),
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Option<Exit>, Failure> {
    let reference = text_arg(sub, "ref");
    let code = match verb {
        "create" => {
            let outcome = match given(sub, "name") {
                Some(name) => {
                    cx.library
                        .item_create(&cx.flags, kind, &text_arg(sub, "bundle"), &name)
                }
                None => cx
                    .library
                    .item_create_at(&cx.flags, kind, &text_arg(sub, "bundle")),
            };
            finish(cx, outcome, out, err, |made, out, _| {
                if made.bundle_created {
                    writeln!(out, "Created bundle \"{}\"", made.bundle)?;
                }
                writeln!(
                    out,
                    "Created {} \"{}\" in bundle \"{}\"",
                    made.kind, made.name, made.bundle
                )?;
                writeln!(
                    out,
                    "Edit with: ctxloom {} edit {}#{}/{}",
                    made.kind,
                    made.bundle,
                    dir_of(&made.kind),
                    made.name
                )
            })?
        }
        "remove" => {
            let outcome = cx
                .library
                .item_remove(&cx.flags, kind, &reference, flag(sub, "yes"));
            removal(cx, outcome, out, err, |gone, out| {
                writeln!(
                    out,
                    "Removed {} \"{}\" from bundle \"{}\"",
                    gone.kind, gone.name, gone.bundle
                )
            })?
        }
        "edit" => {
            let no_distill = flag(sub, "no-distill");
            let outcome = cx
                .library
                .item_edit(&cx.flags, kind, &reference, no_distill);
            finish(cx, outcome, out, err, |edited, out, notes| {
                if edited.status == "unchanged" {
                    return nothing(out);
                }
                if edited.distill_skipped {
                    notes.push("distilled form not refreshed (--no-distill)".to_string());
                }
                writeln!(
                    out,
                    "Updated {} \"{}\" in bundle \"{}\"",
                    edited.kind, edited.name, edited.bundle
                )?;
                writeln!(out)?;
                writeln!(out, "Bundle modified. To publish changes:")?;
                writeln!(out, "  ctxloom bundle push {} [remote]", edited.bundle)
            })?
        }
        _ => return Ok(None),
    };
    Ok(Some(code))
}

/// The directory a kind's items live under in a reference.
fn dir_of(kind: &str) -> &'static str {
    if kind == "fragment" {
        "fragments"
    } else {
        "commands"
    }
}

#[cfg(test)]
#[path = "item.test.rs"]
mod tests;
