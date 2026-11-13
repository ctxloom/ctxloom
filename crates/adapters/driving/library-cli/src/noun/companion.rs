//! `companion`: list, and its writes; `loadout` (B2).

use crate::prelude::*;

pub(crate) fn read(
    cx: &Context<'_>,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let outcome = cx.library.companion_list(&cx.flags);
    answer(cx, outcome, out, err, |rows, out, _| {
        emit(cx.format, out, &rows, |out| {
            text::companion_list(out, &rows)
        })
    })
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
            finish(cx, outcome, out, err, |done, out, _| {
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
            })?
        }
        "remove" => {
            let outcome = cx
                .library
                .companion_remove(&cx.flags, &name, flag(sub, "yes"));
            removal(cx, outcome, out, err, |gone, out| {
                writeln!(out, "Removed companion '{}'", gone.name)
            })?
        }
        _ => return Ok(None),
    };
    Ok(Some(code))
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
