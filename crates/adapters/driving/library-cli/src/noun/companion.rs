//! `companion`: list, and its writes, each as its API resource or its
//! method's response (OD-40, design §3.9, §5.7); `loadout` (B2), the one
//! fixed YAML document, unchanged.

use library_application::{
    CompanionAdded, CompanionRemoved, CompanionRow, CompanionState, CompanionStateSet, Removal,
};

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
            let outcome = cx.library.companion_add(&cx.flags, &name);
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
                            "Registered companion '{}' (its program {} answers)",
                            done.name, done.program
                        )
                    } else {
                        writeln!(
                            out,
                            "Companion '{}' is already registered ({})",
                            done.name, done.program
                        )
                    }
                },
                |_| Exit::OK,
            )?
        }
        "enable" | "disable" => {
            let state = if verb == "enable" {
                CompanionState::Enabled
            } else {
                CompanionState::Disabled
            };
            let outcome = cx.library.companion_set_state(&cx.flags, &name, state);
            api::written(
                cx,
                outcome,
                out,
                err,
                set,
                |done, out, _| {
                    let word = done.state.word();
                    if done.changed {
                        writeln!(out, "Companion '{}' is {word}", done.name)
                    } else {
                        writeln!(out, "Companion '{}' is already {word}", done.name)
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
                bin: row.program.clone(),
                resolves: Some(row.exists),
                state: state_of(row.state).into(),
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
            bin: done.program.clone(),
            resolves: Some(true),
            state: v1::companion::State::Enabled.into(),
        }),
        applied: Some(done.added),
        ..v1::CreateCompanionResponse::default()
    }
}

/// The resource's state for a registration's.
fn state_of(state: CompanionState) -> v1::companion::State {
    match state {
        CompanionState::Enabled => v1::companion::State::Enabled,
        CompanionState::Disabled => v1::companion::State::Disabled,
    }
}

/// `companion enable|disable`: the companion, as it is now (AIP-136).
fn set(parent: &str, done: &CompanionStateSet) -> v1::Companion {
    v1::Companion {
        name: companion_name(parent, &done.name),
        bin: done.program.clone(),
        resolves: None,
        state: state_of(done.state).into(),
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
