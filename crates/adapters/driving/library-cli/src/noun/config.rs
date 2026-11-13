//! `config`: show, and create and edit (B2).

use crate::prelude::*;

pub(crate) fn read(
    cx: &Context<'_>,
    sub: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let section = text_arg(sub, "section");
    let outcome = cx.library.config_show(
        &cx.flags,
        (!section.is_empty()).then_some(section.as_str()),
        flag(sub, "raw"),
    );
    answer(cx, outcome, out, err, |shown: ConfigShown, out, _| {
        emit(cx.format, out, &shown.json, |out| {
            write!(out, "{}", shown.text)
        })
    })
}

pub(crate) fn write(
    cx: &Context<'_>,
    (verb, sub): (&str, &ArgMatches),
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Option<Exit>, Failure> {
    match verb {
        "create" => {
            text_only(cx, "config create")?;
            let outcome = cx.library.config_create(&text_arg(sub, "engine"));
            finish_text(cx, outcome, out, err, |made, out, _| {
                writeln!(out, "Wrote {}", made.path)
            })
            .map(Some)
        }
        "edit" => {
            text_only(cx, "config edit")?;
            let outcome = cx.library.config_edit();
            finish_text(cx, outcome, out, err, |(), _, _| Ok(())).map(Some)
        }
        _ => Ok(None),
    }
}

#[cfg(test)]
#[path = "config.test.rs"]
mod tests;
