//! `run --dry-run` (B2).

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
    let request = RunRequest {
        agent: text_arg(sub, "agent"),
        profile: text_arg(sub, "profile"),
        fragments: many(sub, "fragment"),
        tags: many(sub, "tag"),
        engine: text_arg(sub, "engine"),
        prompt,
        saved_command: text_arg(sub, "command"),
        workspace: text_arg(sub, "workspace"),
    };
    let outcome = cx.library.dry_run(&cx.flags, &request);
    say_notices(&outcome.notices, PROG, cx.format, cx.catalog, err);
    let previewed = outcome.result.map_err(failed)?;
    emit(cx.format, out, &previewed.plan, |out| {
        text::dry_run(out, &previewed.plan)
    })
    .map_err(output_failure)?;
    // A dry run that found nothing fatal would launch (ruling 153): the
    // distinguishing success 1, said only under `--detailed-exitcode`.
    if previewed.fatal.is_empty() {
        return Ok(Exit::WOULD_CHANGE);
    }
    Err(AppError::aborted(&previewed.fatal).into_failure())
}

#[cfg(test)]
#[path = "run.test.rs"]
mod tests;
