//! `fragment` and `command`: list, show and `fragment premises`, and their
//! writes (B1), each json answer its AIP resource or method response
//! (OD-40, design §5.2).

use library_application::{
    ItemCreated, ItemEdited, ItemList, ItemRemoved, ItemShow, Premises, Removal,
};

use crate::catalog::{self, ok};
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
        // Text unless json is asked for by name (an agent's shell runs it
        // off a terminal for the body); json is the item's resource.
        "show" => {
            let outcome = cx.library.item_show(
                &cx.flags,
                kind_of,
                &text_arg(sub, "ref"),
                flag(sub, "distilled"),
            );
            catalog::answer(
                cx,
                outcome,
                out,
                err,
                true,
                |parent, show| shown(parent, kind_of, show),
                |show, out, notes| {
                    let mut said = text::Notes(Vec::new());
                    let written = text::item_show(out, &mut said, show);
                    notes.extend(said.0);
                    written
                },
                ok,
            )
        }
        "premises" => {
            let outcome = cx.library.premises(&cx.flags);
            catalog::answer(
                cx,
                outcome,
                out,
                err,
                false,
                premise_index,
                |premises, out, _| text::premises(out, premises),
                ok,
            )
        }
        _ => {
            let outcome = cx
                .library
                .item_list(&cx.flags, kind_of, &text_arg(sub, "bundle"));
            catalog::answer(
                cx,
                outcome,
                out,
                err,
                false,
                listed,
                |list, out, _| text::item_list(out, list),
                ok,
            )
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
            catalog::answer(
                cx,
                outcome,
                out,
                err,
                false,
                created,
                |made, out, _| {
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
                },
                ok,
            )?
        }
        "remove" => {
            let outcome = cx
                .library
                .item_remove(&cx.flags, kind, &reference, flag(sub, "yes"));
            catalog::answer(
                cx,
                outcome,
                out,
                err,
                false,
                |parent, gone| removed(parent, kind, &reference, gone),
                |gone, out, _| {
                    catalog::removal_text(gone, out, |gone, out| {
                        writeln!(
                            out,
                            "Removed {} \"{}\" from bundle \"{}\"",
                            gone.kind, gone.name, gone.bundle
                        )
                    })
                },
                crate::write::removal_exit,
            )?
        }
        "edit" => {
            let no_distill = flag(sub, "no-distill");
            let outcome = cx
                .library
                .item_edit(&cx.flags, kind, &reference, no_distill);
            catalog::answer(
                cx,
                outcome,
                out,
                err,
                false,
                edited,
                |edited, out, notes| {
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
                },
                ok,
            )?
        }
        _ => return Ok(None),
    };
    Ok(Some(code))
}

/// A fragment's message or a command's, as the kind is.
#[derive(serde::Serialize)]
#[serde(untagged)]
pub(crate) enum Either<F, C> {
    Fragment(F),
    Command(C),
}

/// Whether `kind` (the use case's word or its directory) is a command's.
fn is_command(kind: &str) -> bool {
    matches!(kind, "command" | "commands" | "prompt" | "prompts")
}

/// `fragment|command list`: `ListFragmentsResponse` or `ListCommandsResponse`.
pub(crate) fn listed(
    parent: &str,
    list: &ItemList,
) -> Either<v1::ListFragmentsResponse, v1::ListCommandsResponse> {
    let name = |bundle: &str, dir: &str, id: &str| catalog::item_name(parent, bundle, dir, id);
    if is_command(&list.kind) {
        return Either::Command(v1::ListCommandsResponse {
            commands: list
                .rows
                .iter()
                .map(|row| v1::Command {
                    name: name(&row.bundle, "commands", &row.name),
                    reference: row.reference.clone(),
                    tags: row.tags.clone(),
                    ..v1::Command::default()
                })
                .collect(),
            next_page_token: String::new(),
        });
    }
    Either::Fragment(v1::ListFragmentsResponse {
        fragments: list
            .rows
            .iter()
            .map(|row| v1::Fragment {
                name: name(&row.bundle, "fragments", &row.name),
                reference: row.reference.clone(),
                tags: row.tags.clone(),
                ..v1::Fragment::default()
            })
            .collect(),
        next_page_token: String::new(),
    })
}

/// `fragment|command show --format json`: the `Fragment` or `Command`
/// (FULL), its body the one served.
pub(crate) fn shown(
    parent: &str,
    kind: ItemKind,
    show: &ItemShow,
) -> Either<v1::Fragment, v1::Command> {
    let command = kind == ItemKind::Command;
    let dir = if command { "commands" } else { "fragments" };
    let name = catalog::item_name(parent, &show.bundle, dir, &show.name);
    if command {
        return Either::Command(v1::Command {
            name,
            reference: show.reference.clone(),
            body: show.content.clone(),
            body_is_distilled: show.distilled,
            ..v1::Command::default()
        });
    }
    Either::Fragment(v1::Fragment {
        name,
        reference: show.reference.clone(),
        body: show.content.clone(),
        body_is_distilled: show.distilled,
        ..v1::Fragment::default()
    })
}

/// `fragment premises`: the `PremiseIndex` singleton, each entry naming
/// its fragment.
pub(crate) fn premise_index(parent: &str, premises: &Premises) -> v1::PremiseIndex {
    v1::PremiseIndex {
        name: format!("{parent}/premiseIndex"),
        instruction: premises.instruction.clone(),
        fragments: premises
            .fragments
            .iter()
            .map(|row| v1::PremiseEntry {
                fragment: catalog::reference_name(parent, &row.name)
                    .unwrap_or_else(|| row.name.clone()),
                premise: row.premise.clone(),
            })
            .collect(),
    }
}

/// `fragment|command create`: `CreateFragmentResponse` or
/// `CreateCommandResponse`.
pub(crate) fn created(
    parent: &str,
    made: &ItemCreated,
) -> Either<v1::CreateFragmentResponse, v1::CreateCommandResponse> {
    let name = catalog::item_name(parent, &made.bundle, dir_of(&made.kind), &made.name);
    if is_command(&made.kind) {
        return Either::Command(v1::CreateCommandResponse {
            command: Some(v1::Command {
                name,
                ..v1::Command::default()
            }),
            path: made.path.clone(),
            applied: Some(true),
            bundle_created: made.bundle_created,
            ..v1::CreateCommandResponse::default()
        });
    }
    Either::Fragment(v1::CreateFragmentResponse {
        fragment: Some(v1::Fragment {
            name,
            ..v1::Fragment::default()
        }),
        path: made.path.clone(),
        applied: Some(true),
        bundle_created: made.bundle_created,
        ..v1::CreateFragmentResponse::default()
    })
}

/// `fragment|command edit`: `UpdateFragmentResponse` or
/// `UpdateCommandResponse`; an edit that changed nothing applied nothing.
/// Its distilled form refreshed is `FRESH`; one not refreshed is no claim.
pub(crate) fn edited(
    parent: &str,
    edited: &ItemEdited,
) -> Either<v1::UpdateFragmentResponse, v1::UpdateCommandResponse> {
    let name = catalog::item_name(parent, &edited.bundle, dir_of(&edited.kind), &edited.name);
    let applied = Some(edited.status != "unchanged");
    let distilled = if edited.distilled {
        v1::DistilledState::Fresh as i32
    } else {
        v1::DistilledState::Unspecified as i32
    };
    if is_command(&edited.kind) {
        return Either::Command(v1::UpdateCommandResponse {
            command: Some(v1::Command {
                name,
                distilled,
                ..v1::Command::default()
            }),
            path: edited.path.clone(),
            applied,
            ..v1::UpdateCommandResponse::default()
        });
    }
    Either::Fragment(v1::UpdateFragmentResponse {
        fragment: Some(v1::Fragment {
            name,
            distilled,
            ..v1::Fragment::default()
        }),
        path: edited.path.clone(),
        applied,
        ..v1::UpdateFragmentResponse::default()
    })
}

/// `fragment|command remove`: `DeleteFragmentResponse` or
/// `DeleteCommandResponse`, the preview's (named by the reference asked) or
/// the removal's.
pub(crate) fn removed(
    parent: &str,
    kind: ItemKind,
    asked: &str,
    gone: &Removal<ItemRemoved>,
) -> Either<v1::DeleteFragmentResponse, v1::DeleteCommandResponse> {
    let command = kind == ItemKind::Command;
    let dir = if command { "commands" } else { "fragments" };
    let name = match gone {
        Removal::Done(done) => catalog::item_name(parent, &done.bundle, dir, &done.name),
        Removal::Preview(_) => {
            catalog::reference_name(parent, asked).unwrap_or_else(|| asked.to_string())
        }
    };
    let (path, applied, detail, apply_command) = catalog::removal(
        gone,
        |preview| (String::new(), false, preview.detail, preview.apply_command),
        |done| (done.path.clone(), true, Vec::new(), String::new()),
    );
    if command {
        return Either::Command(v1::DeleteCommandResponse {
            command: Some(v1::Command {
                name,
                ..v1::Command::default()
            }),
            path,
            applied: Some(applied),
            detail,
            apply_command,
            ..v1::DeleteCommandResponse::default()
        });
    }
    Either::Fragment(v1::DeleteFragmentResponse {
        fragment: Some(v1::Fragment {
            name,
            ..v1::Fragment::default()
        }),
        path,
        applied: Some(applied),
        detail,
        apply_command,
        ..v1::DeleteFragmentResponse::default()
    })
}

/// The directory a kind's items live under in a reference.
fn dir_of(kind: &str) -> &'static str {
    if is_command(kind) {
        "commands"
    } else {
        "fragments"
    }
}

#[cfg(test)]
#[path = "item.test.rs"]
mod tests;
