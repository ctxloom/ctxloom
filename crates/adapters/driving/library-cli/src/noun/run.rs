//! `run --dry-run` (B2): its json the `Run` it would create (OD-40, design
//! §5.9: `CreateRun` with `validate_only`), named nothing, since nothing is
//! created, so it asks no daemon for a name.

use library_application::{DryRun, FindingRow};

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
    if cx.format == Format::Json {
        api::print(out, &planned(&previewed.plan))
    } else {
        text::dry_run(out, &previewed.plan)
    }
    .map_err(output_failure)?;
    // A dry run that found nothing fatal would launch (ruling 153): the
    // distinguishing success 1, said only under `--detailed-exitcode`.
    if previewed.fatal.is_empty() {
        return Ok(Exit::WOULD_CHANGE);
    }
    Err(AppError::aborted(&previewed.fatal).into_failure())
}

/// The `Run` a dry run would create: the plan's agent, engine, workspace,
/// runtime, profiles, fragments, context and its token estimate, the prompt,
/// the findings, and the agent's settings on the engine.
pub(crate) fn planned(plan: &DryRun) -> v1::Run {
    v1::Run {
        agent: plan.agent.clone(),
        engine: plan.engine.clone(),
        profiles: plan.profiles.clone(),
        context: plan.context.clone(),
        // A count (OQ-12); no context holds 2^32 tokens.
        tokens: u32::try_from(plan.tokens).unwrap_or(u32::MAX),
        findings: plan.findings.iter().map(finding).collect(),
        settings: plan.settings.as_ref().map(api::settings),
        workspace: plan.workspace.clone(),
        runtime: plan.runtime.clone(),
        prompt: plan.prompt.clone(),
        fragments: plan.fragments.clone(),
        ..v1::Run::default()
    }
}

fn finding(found: &FindingRow) -> v1::Finding {
    v1::Finding {
        kind: found.kind.clone(),
        text: found.text.clone(),
        remedy: found.remedy.clone(),
        fatal: found.fatal,
    }
}

#[cfg(test)]
#[path = "run.test.rs"]
mod tests;
