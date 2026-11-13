//! `companion`: list, and its writes, each as its API resource or its
//! method's response (OD-40, design §3.9, §5.7); `loadout` (B2), the one
//! fixed YAML document, unchanged.

use library_application::{CompanionAdded, CompanionRemoved, CompanionRow, Removal};

use crate::prelude::*;

pub(crate) fn read(
    cx: &Context<'_>,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let outcome = cx.library.companion_list(&cx.flags);
    resource(
        cx,
        outcome,
        out,
        err,
        |parent, rows: &Vec<CompanionRow>| companions(parent, rows),
        |out, rows| text::companion_list(out, rows),
    )
}

pub(crate) fn write(
    cx: &Context<'_>,
    (verb, sub): (&str, &ArgMatches),
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Option<Exit>, Failure> {
    let name = text_arg(sub, "name");
    let code = match verb {
        "add" => {
            let command: Vec<String> = sub
                .try_get_many::<String>("command")
                .ok()
                .flatten()
                .into_iter()
                .chain(sub.try_get_many::<String>("args").ok().flatten())
                .flatten()
                .cloned()
                .collect();
            let outcome = cx.library.companion_add(&cx.flags, &name, &command);
            api::written(
                cx,
                outcome,
                out,
                err,
                added,
                |done, out, _| {
                    if done.added {
                        writeln!(
                            out,
                            "Registered companion '{}' (answers: {})",
                            done.name,
                            done.command.join(" ")
                        )
                    } else {
                        writeln!(
                            out,
                            "Companion '{}' is already registered ({})",
                            done.name,
                            done.command.join(" ")
                        )
                    }
                },
                |_| Exit::OK,
            )?
        }
        "remove" => {
            let outcome = cx
                .library
                .companion_remove(&cx.flags, &name, flag(sub, "yes"));
            api::written(
                cx,
                outcome,
                out,
                err,
                |parent, removal| removed(parent, &name, removal),
                |removal, out, _| match removal {
                    Removal::Preview(preview) => crate::write::preview_text(out, preview),
                    Removal::Done(gone) => writeln!(out, "Removed companion '{}'", gone.name),
                },
                crate::write::removal_exit,
            )?
        }
        _ => return Ok(None),
    };
    Ok(Some(code))
}

/// A companion's resource name under `parent`.
fn companion_name(parent: &str, companion: &str) -> String {
    format!("{parent}/companions/{companion}")
}

/// `companion list`: `ListCompanionsResponse`, every registered companion
/// under `parent`, whether its program is there `resolves` (looked for,
/// never run).
pub(crate) fn companions(parent: &str, rows: &[CompanionRow]) -> v1::ListCompanionsResponse {
    v1::ListCompanionsResponse {
        companions: rows
            .iter()
            .map(|row| v1::Companion {
                name: companion_name(parent, &row.name),
                command: row.command.clone(),
                resolves: Some(row.exists),
                ..v1::Companion::default()
            })
            .collect(),
        next_page_token: String::new(),
    }
}

/// `companion add`: the companion as registered (its program answered the
/// probe, so it resolves), applied when the registration is new or changed
/// one; the same registration again changes nothing.
fn added(parent: &str, done: &CompanionAdded) -> v1::CreateCompanionResponse {
    v1::CreateCompanionResponse {
        companion: Some(v1::Companion {
            name: companion_name(parent, &done.name),
            command: done.command.clone(),
            resolves: Some(true),
            ..v1::Companion::default()
        }),
        applied: Some(done.added),
        ..v1::CreateCompanionResponse::default()
    }
}

/// `companion remove`: the preview (the command that applies it), or the
/// removal done.
fn removed(
    parent: &str,
    name: &str,
    removal: &Removal<CompanionRemoved>,
) -> v1::DeleteCompanionResponse {
    let (applied, detail, apply_command) = match removal {
        Removal::Preview(preview) => (false, preview.detail.clone(), preview.apply.clone()),
        Removal::Done(gone) => (gone.removed, Vec::new(), String::new()),
    };
    v1::DeleteCompanionResponse {
        companion: Some(v1::Companion {
            name: companion_name(parent, name),
            ..v1::Companion::default()
        }),
        applied: Some(applied),
        detail,
        apply_command,
        ..v1::DeleteCompanionResponse::default()
    }
}

/// `loadout`: the one fixed YAML document. The value of `--format` is a
/// document format here, and yaml is the only one there is.
pub(crate) fn loadout(sub: &ArgMatches, out: &mut dyn Write) -> Result<Exit, Failure> {
    let asked = given(sub, "format");
    if let Some(format) = asked.filter(|f| f != "yaml") {
        return Err(AppError::from(WriteError::LoadoutFormat { format }).into_failure());
    }
    out.write_all(LOADOUT.as_bytes())
        .map_err(crate::output_failure)?;
    Ok(Exit::OK)
}

#[cfg(test)]
#[path = "companion.test.rs"]
mod tests;
