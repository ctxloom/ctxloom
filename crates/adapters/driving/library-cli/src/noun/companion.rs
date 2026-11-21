//! `companion`: list, and its writes, each as its API resource or its
//! method's response (OD-40, design §3.9, §5.7); `loadout` (B2), the one
//! YAML document, written from the daemon's `Loadout` (step 10a U2).

use crate::prelude::*;

pub(crate) fn read(
    cx: &Context<'_>,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    // The daemon's read (step 10a U2), its text from the message (D10).
    let request = v1::ListCompanionsRequest {
        caller: v1::Caller::of(cx.globals(), None),
        ..v1::ListCompanionsRequest::default()
    };
    let read = cx.served.list_companions("", request);
    served(cx, read, out, err, false, |listed, out, _| {
        text::companion_list(out, &crate::read_back::companions(listed))
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
        // The daemon's `CreateCompanion` (step 10b U3): it probes the
        // program and registers the name in the home config; its text from
        // the response (D10).
        "add" => {
            let request = v1::CreateCompanionRequest {
                companion_id: name.clone(),
                caller: v1::Caller::of(cx.globals(), None),
                ..v1::CreateCompanionRequest::default()
            };
            crate::answer_written(
                cx,
                cx.served.create_companion("", request),
                |m| std::mem::take(&mut m.notices),
                out,
                err,
                |m, out| {
                    let bin = m.companion.as_ref().map_or("", |c| c.bin.as_str());
                    if m.applied.unwrap_or(false) {
                        writeln!(
                            out,
                            "Registered companion '{name}' (its program {bin} answers)"
                        )
                    } else {
                        writeln!(out, "Companion '{name}' is already registered ({bin})")
                    }
                },
                |_| Exit::OK,
            )?
        }
        // The daemon's `EnableCompanion`/`DisableCompanion` (step 10b, owner
        // 2026-11-18, night): the state written to the home config; its
        // text from the response (D10).
        "enable" | "disable" => {
            let under = library_api::names::named_target("companions", &name);
            let word = if verb == "enable" {
                "enabled"
            } else {
                "disabled"
            };
            let said = |applied: Option<bool>, out: &mut dyn Write| {
                if applied.unwrap_or(false) {
                    writeln!(out, "Companion '{name}' is {word}")
                } else {
                    writeln!(out, "Companion '{name}' is already {word}")
                }
            };
            if verb == "enable" {
                let request = v1::EnableCompanionRequest {
                    caller: v1::Caller::of(cx.globals(), None),
                    ..v1::EnableCompanionRequest::default()
                };
                crate::answer_written(
                    cx,
                    cx.served.enable_companion(&under, request),
                    |m| std::mem::take(&mut m.notices),
                    out,
                    err,
                    |m, out| said(m.applied, out),
                    |_| Exit::OK,
                )?
            } else {
                let request = v1::DisableCompanionRequest {
                    caller: v1::Caller::of(cx.globals(), None),
                    ..v1::DisableCompanionRequest::default()
                };
                crate::answer_written(
                    cx,
                    cx.served.disable_companion(&under, request),
                    |m| std::mem::take(&mut m.notices),
                    out,
                    err,
                    |m, out| said(m.applied, out),
                    |_| Exit::OK,
                )?
            }
        }
        "remove" => {
            let under = library_api::names::named_target("companions", &name);
            crate::write::removal_served(
                cx,
                flag(sub, "yes"),
                |validate_only, answers, etag| {
                    // DeleteCompanion carries no answers: its Confirm is
                    // answered by the apply itself.
                    let _ = answers;
                    let request = v1::DeleteCompanionRequest {
                        validate_only,
                        etag,
                        caller: v1::Caller::of(cx.globals(), None),
                        ..v1::DeleteCompanionRequest::default()
                    };
                    cx.served.delete_companion(&under, request)
                },
                out,
                err,
                |_, out| writeln!(out, "Removed companion '{name}'"),
            )?
        }
        _ => return Ok(None),
    };
    Ok(Some(code))
}

/// `loadout`: the daemon's `GetLoadout` (step 10a U2), written as the one
/// YAML document. The value of `--format` is a document format here, and
/// yaml is the only one there is, so the output format is never asked: the
/// document is the answer, json or text alike.
pub(crate) fn loadout(
    cx: &Context<'_>,
    sub: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let asked = given(sub, "format");
    if let Some(format) = asked.filter(|f| f != "yaml") {
        return Err(AppError::from(WriteError::LoadoutFormat { format }).into_failure());
    }
    let request = v1::GetLoadoutRequest {
        caller: v1::Caller::of(cx.globals(), None),
        ..v1::GetLoadoutRequest::default()
    };
    let (loadout, notices) = cx.served.own_loadout(request)?;
    let said: Vec<library_application::Notice> =
        notices.iter().map(library_api::reads::said).collect();
    say_notices(&said, PROG, cx.format, cx.catalog, err);
    let document = library_application::loadout::document(&crate::read_back::loadout(&loadout));
    out.write_all(document.as_bytes())
        .map_err(crate::output_failure)?;
    Ok(Exit::OK)
}

#[cfg(test)]
#[path = "companion.test.rs"]
mod tests;
