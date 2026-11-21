//! The write verbs: arguments in, the use case's answer out as json or as the
//! text Go printed. A verb Go answers in text only (its format debt) refuses
//! an explicit `--format json` before it asks the use case anything.

use cli_support::Exit;
use std::collections::BTreeMap;
use std::io::{self, Write};

use angzarr_presentation::Format;
use clap::ArgMatches;
use library_application::{AppError, Outcome, Preview, WriteError};

use daemon_proto::v1;

use crate::{failed, Context, Failure, IntoFailure};

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

/// A served removal's response (step 10b U3), as the CLI confirms and
/// renders it.
pub(crate) trait RemovalResponse: serde::Serialize {
    fn detail(&self) -> &[String];
    fn apply_command(&self) -> &str;
    fn needs(&self) -> &[v1::Need];
    fn etag(&self) -> &str;
    fn notices(&mut self) -> Vec<v1::Notice>;
}

macro_rules! removal_responses {
    ($($response:ident),* $(,)?) => {
        $(
            impl RemovalResponse for v1::$response {
                fn detail(&self) -> &[String] {
                    &self.detail
                }
                fn apply_command(&self) -> &str {
                    &self.apply_command
                }
                fn needs(&self) -> &[v1::Need] {
                    &self.needs
                }
                fn etag(&self) -> &str {
                    &self.etag
                }
                fn notices(&mut self) -> Vec<v1::Notice> {
                    std::mem::take(&mut self.notices)
                }
            }
        )*
    };
}

removal_responses!(
    DeleteBundleResponse,
    DeleteFragmentResponse,
    DeleteCommandResponse,
    DeleteSkillResponse,
    DeleteProfileResponse,
    DeleteAgentResponse,
    DeleteCompanionResponse,
);

/// The refusal of a write planned against what has changed since.
pub(crate) const STALE: &str = "library-tree-plan-stale";

/// A removal preview's Confirm need, if it asks one.
fn confirm_need(needs: &[v1::Need]) -> Option<(&str, &v1::Confirm)> {
    needs.iter().find_map(|need| match &need.kind {
        Some(v1::need::Kind::Confirm(confirm)) => Some((need.id.as_str(), confirm)),
        _ => None,
    })
}

/// A served removal (step 10b U3): with `--yes` one call that plans and
/// applies; else its preview, and at a terminal the preview's Confirm need
/// asked there and, on a yes, the apply sent with the answer and the
/// preview's etag. Off a terminal (or on a no) the preview is printed and
/// nothing is applied. `ask(validate_only, answers, etag)` is the call.
pub(crate) fn removal_served<M: RemovalResponse>(
    cx: &Context<'_>,
    yes: bool,
    ask: impl Fn(bool, Vec<v1::Answer>, String) -> Result<M, Failure>,
    out: &mut dyn Write,
    err: &mut dyn Write,
    done: impl FnOnce(&M, &mut dyn Write) -> io::Result<()>,
) -> Result<Exit, Failure> {
    let mut answered = ask(!yes, Vec::new(), String::new());
    if !yes {
        if let Ok(preview) = &answered {
            if let Some((id, confirm)) = confirm_need(preview.needs()) {
                if cx.served.confirm(&confirm.text) == Some(true) {
                    let answers = vec![v1::Answer {
                        need_id: id.to_string(),
                        answer: Some(v1::answer::Answer::Confirmed(true)),
                    }];
                    answered = ask(false, answers, preview.etag().to_string());
                }
            }
        }
    }
    // What she was shown has changed since: the apply is refused stale,
    // and she is shown the removal as it is now, to look again.
    if let Err(stale) = &answered {
        if stale.cause.code() == STALE {
            let again = ask(true, Vec::new(), String::new());
            if let Ok(mut again) = again {
                let notices = again.notices();
                let said: Vec<library_application::Notice> =
                    notices.iter().map(library_api::reads::said).collect();
                cli_support::say_notices(&said, crate::PROG, cx.format, cx.catalog, err);
                if let Some((_, confirm)) = confirm_need(again.needs()) {
                    removal_preview_text(&again, confirm, out).map_err(crate::output_failure)?;
                }
            }
        }
    }
    crate::answer_written(
        cx,
        answered,
        |m| m.notices(),
        out,
        err,
        |m, out| match confirm_need(m.needs()) {
            Some((_, confirm)) => removal_preview_text(m, confirm, out),
            None => done(m, out),
        },
        |m| match confirm_need(m.needs()) {
            Some(_) => Exit::WOULD_CHANGE,
            None => Exit::OK,
        },
    )
}

/// A served removal's preview, in the words of [`preview_text`]: its
/// target is its Confirm need's arg.
fn removal_preview_text<M: RemovalResponse>(
    m: &M,
    confirm: &v1::Confirm,
    out: &mut dyn Write,
) -> io::Result<()> {
    let target = confirm
        .args
        .iter()
        .find(|arg| arg.name == "target")
        .map_or("", |arg| arg.value.as_str());
    preview_text(
        out,
        &Preview::of(
            target.to_string(),
            m.detail().to_vec(),
            m.apply_command().to_string(),
        ),
    )
}

pub(crate) fn nothing(out: &mut dyn Write) -> io::Result<()> {
    writeln!(out, "No changes made.")
}
