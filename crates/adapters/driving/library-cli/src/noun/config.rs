//! `config`: show, as its API resource (OD-40, design §3.9, §5.8: the
//! `Config` singleton), and create and edit, which answer in text only (B2).

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
    resource(cx, outcome, out, err, config, |out, shown: &ConfigShown| {
        write!(out, "{}", shown.text)
    })
}

/// `config show [section] [--raw]`: the `Config` singleton under `parent`,
/// its document what the layers give (one section of it, or only what they
/// set), as written.
pub(crate) fn config(parent: &str, shown: &ConfigShown) -> v1::Config {
    v1::Config {
        name: format!("{parent}/config"),
        document: Some(api::document(&shown.json)),
        etag: String::new(),
    }
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
