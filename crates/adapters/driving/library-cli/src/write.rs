//! The write verbs: arguments in, the use case's answer out as json or as the
//! text Go printed. A verb Go answers in text only (its format debt) refuses
//! an explicit `--format json` before it asks the use case anything.

use cli_support::Exit;
use std::collections::BTreeMap;
use std::io::{self, Write};

use angzarr_presentation::Format;
use clap::ArgMatches;
use library_application::{AppError, Outcome, Preview, Removal, WriteError};
use serde::Serialize;

use crate::{emit, failed, Context, Failure, IntoFailure};

/// A flag's value, `None` when the flag was not given.
pub(crate) fn given(sub: &ArgMatches, name: &str) -> Option<String> {
    sub.try_get_one::<String>(name).ok().flatten().cloned()
}

/// A repeatable flag's values, `None` when the flag was not given; an empty
/// value clears (it contributes no entry).
pub(crate) fn given_list(sub: &ArgMatches, name: &str) -> Option<Vec<String>> {
    sub.try_get_many::<String>(name)
        .ok()
        .flatten()
        .map(|values| values.filter(|v| !v.is_empty()).cloned().collect())
}

pub(crate) fn pair(flag_name: &str, value: &str) -> Result<(String, String), Failure> {
    value
        .split_once('=')
        .map(|(k, v)| (k.to_string(), v.to_string()))
        .ok_or_else(|| {
            failed(AppError::from(WriteError::FlagPair {
                flag: format!("--{flag_name}"),
                got: value.to_string(),
            }))
        })
}

pub(crate) fn given_pairs(
    sub: &ArgMatches,
    name: &str,
) -> Result<Option<Vec<(String, String)>>, Failure> {
    given_list(sub, name)
        .map(|values| values.iter().map(|v| pair(name, v)).collect())
        .transpose()
}

pub(crate) fn given_map(
    sub: &ArgMatches,
    name: &str,
) -> Result<Option<BTreeMap<String, String>>, Failure> {
    Ok(given_pairs(sub, name)?.map(|pairs| pairs.into_iter().collect()))
}

/// A verb that answers in text only: json asked for by name is refused.
pub(crate) fn text_only(cx: &Context<'_>, command: &str) -> Result<(), Failure> {
    if cx.explicit && cx.format == Format::Json {
        return Err(AppError::UnsupportedFormat {
            command: command.to_string(),
            format: "json".to_string(),
        }
        .into_failure());
    }
    Ok(())
}

/// Said and written: json, or `text`.
pub(crate) fn finish<T: Serialize>(
    cx: &Context<'_>,
    outcome: Outcome<T>,
    out: &mut dyn Write,
    err: &mut dyn Write,
    text: impl FnOnce(&T, &mut dyn Write, &mut Vec<String>) -> io::Result<()>,
) -> Result<Exit, Failure> {
    crate::answer(cx, outcome, out, err, |value, out, notes| {
        emit(cx.format, out, &value, |out| text(&value, out, notes))
    })
}

/// Said and written as text, whatever the format.
pub(crate) fn finish_text<T>(
    cx: &Context<'_>,
    outcome: Outcome<T>,
    out: &mut dyn Write,
    err: &mut dyn Write,
    text: impl FnOnce(&T, &mut dyn Write, &mut Vec<String>) -> io::Result<()>,
) -> Result<Exit, Failure> {
    crate::answer(cx, outcome, out, err, |value, out, notes| {
        text(&value, out, notes).map(|()| Exit::OK)
    })
}

/// A removal: the preview or the removed.
pub(crate) fn removal<T: Serialize>(
    cx: &Context<'_>,
    outcome: Outcome<Removal<T>>,
    out: &mut dyn Write,
    err: &mut dyn Write,
    done: impl FnOnce(&T, &mut dyn Write) -> io::Result<()>,
) -> Result<Exit, Failure> {
    crate::answer(cx, outcome, out, err, |value, out, _| {
        emit(cx.format, out, &value, |out| match &value {
            Removal::Preview(preview) => preview_text(out, preview),
            Removal::Done(removed) => done(removed, out),
        })?;
        Ok(removal_exit(&value))
    })
}

/// A removal's exit (ruling 153): its preview would change something (the
/// thing it names is there to remove), so it is the distinguishing success
/// 1 under `--detailed-exitcode`; a removal done is ok.
pub(crate) fn removal_exit<T>(removal: &Removal<T>) -> Exit {
    match removal {
        Removal::Preview(_) => Exit::WOULD_CHANGE,
        Removal::Done(_) => Exit::OK,
    }
}

pub(crate) fn preview_text(out: &mut dyn Write, preview: &Preview) -> io::Result<()> {
    if preview.detail.is_empty() {
        writeln!(out, "Would remove {}.", preview.target)?;
    } else {
        writeln!(out, "Would remove {}:", preview.target)?;
        for line in &preview.detail {
            writeln!(out, "  {line}")?;
        }
    }
    writeln!(out)?;
    writeln!(out, "Nothing was removed. Re-run with --yes to apply:")?;
    writeln!(out, "  {}", preview.apply)
}

pub(crate) fn nothing(out: &mut dyn Write) -> io::Result<()> {
    writeln!(out, "No changes made.")
}
