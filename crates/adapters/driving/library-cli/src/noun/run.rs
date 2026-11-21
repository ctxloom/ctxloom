//! `run --dry-run` (B2): the daemon's `CreateRun` with `validate_only`
//! (step 10a U2; OD-40, design §5.9), its json the `Run` it would create,
//! named under the project the daemon names, its references resource names
//! (design §2), and its text drawn from that message (D10). Every dry run,
//! text or json, asks the daemon (owner ruling 2026-11-14: "dry run
//! *absolutely* calls (and spawns, if necessary) a daemon"). It never mints
//! the project's id: in a project nothing has named, its name prints as
//! null and its references keep their configured text.

use crate::prelude::*;

pub(crate) fn read(
    cx: &Context<'_>,
    sub: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    if !flag(sub, "dry-run") {
        return Err(AppError::RunNeedsDryRun.into_failure());
    }
    let mut prompt = text_arg(sub, "prompt");
    if prompt.is_empty() {
        prompt = many(sub, "prompt_words").join(" ");
    }
    let profile = text_arg(sub, "profile");
    let request = v1::CreateRunRequest {
        mode: v1::run::Mode::Terminal as i32,
        prompt,
        agent: text_arg(sub, "agent"),
        engine: text_arg(sub, "engine"),
        profiles: if profile.is_empty() {
            Vec::new()
        } else {
            vec![profile]
        },
        fragments: many(sub, "fragment"),
        tags: many(sub, "tag"),
        command: text_arg(sub, "command"),
        workspace: text_arg(sub, "workspace"),
        no_context: flag(sub, "no-context"),
        validate_only: true,
        caller: v1::Caller::of(cx.globals(), None),
        ..v1::CreateRunRequest::default()
    };
    let mut run = cx.served.preview_run(request)?;
    let notices: Vec<library_application::Notice> =
        run.notices.iter().map(library_api::reads::said).collect();
    say_notices(&notices, PROG, cx.format, cx.catalog, err);
    run.notices.clear();
    let fatal: Vec<_> = crate::read_back::findings(&run)
        .into_iter()
        .filter(|found| found.fatal)
        .collect();
    if cx.format == Format::Json {
        // Unresolved, the name is null, never left out (owner 2026-11-14).
        daemon_proto::with_unresolved_names(&run, &["name"])
            .map_err(io::Error::other)
            .and_then(|printed| api::print(out, &printed))
    } else {
        text::dry_run(out, &crate::read_back::dry_run(&run))
    }
    .map_err(output_failure)?;
    // A dry run that found nothing fatal would launch (ruling 153): the
    // distinguishing success 1, said only under `--detailed-exitcode`.
    if fatal.is_empty() {
        return Ok(Exit::WOULD_CHANGE);
    }
    Err(AppError::aborted(&fatal).into_failure())
}

#[cfg(test)]
#[path = "run.test.rs"]
mod tests;
