//! `config`: show, as its API resource (OD-40, design §3.9, §5.8: the
//! `Config` singleton), and create and edit, which answer in text only (B2).

use crate::prelude::*;

pub(crate) fn read(
    cx: &Context<'_>,
    sub: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    // The daemon's read (step 10a U2), its text the message's document as
    // YAML, keys sorted (D10; owner ruling 2026-11-17).
    let view = if flag(sub, "raw") {
        v1::config::View::Set
    } else {
        v1::config::View::Effective
    };
    let request = v1::GetConfigRequest {
        view: view as i32,
        section: text_arg(sub, "section"),
        caller: v1::Caller::of(cx.globals(), None),
        ..v1::GetConfigRequest::default()
    };
    let read = cx.served.get_config("/config", request);
    served(cx, read, out, err, false, |config, out, _| {
        write!(out, "{}", crate::read_back::config_text(config))
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
        "set" => {
            // The daemon's `UpdateConfig` (step 10b U3), its text from the
            // response (D10).
            let request = v1::UpdateConfigRequest {
                path: text_arg(sub, "key"),
                value: text_arg(sub, "value"),
                home: flag(sub, "home"),
                caller: v1::Caller::of(cx.globals(), None),
                ..v1::UpdateConfigRequest::default()
            };
            let set = cx.served.update_config("/config", request);
            crate::answer_written(
                cx,
                set,
                |m| std::mem::take(&mut m.notices),
                out,
                err,
                |m, out| writeln!(out, "Set {} in {}", m.changes.join(", "), m.path),
                |_| Exit::OK,
            )
            .map(Some)
        }
        "edit" => {
            // The daemon's `EditConfig` (step 10b U3): its preview's Edit
            // need presented here (the editor launched on the config's
            // text), the apply sent with what it left and the preview's
            // etag. It says nothing on success, as before.
            text_only(cx, "config edit")?;
            let ask = |validate_only: bool, answers: Vec<v1::Answer>, etag: String| {
                v1::EditConfigRequest {
                    validate_only,
                    answers,
                    etag,
                    caller: v1::Caller::of(cx.globals(), cx.presentable()),
                    ..v1::EditConfigRequest::default()
                }
            };
            let preview = cx
                .served
                .edit_config("/config", ask(true, Vec::new(), String::new()))?;
            let answers = crate::noun::item::edit_answers(cx, &preview.needs)?;
            let edited = cx
                .served
                .edit_config("/config", ask(false, answers, preview.etag));
            crate::answer_written(
                cx,
                edited,
                |m| std::mem::take(&mut m.notices),
                out,
                err,
                |_, _| Ok(()),
                |_| Exit::OK,
            )
            .map(Some)
        }
        _ => Ok(None),
    }
}

#[cfg(test)]
#[path = "config.test.rs"]
mod tests;
