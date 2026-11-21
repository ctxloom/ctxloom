//! `skill`: list and show, and its writes (B1), each json answer its AIP
//! resource or method response (OD-40, design §5.3).

use library_application::{SkillExported, SkillImported};

use crate::catalog::{self, ok};
use crate::read_back;
use library_api::resources::{count, skill_name};

use crate::prelude::*;

pub(crate) fn read(
    cx: &Context<'_>,
    (verb, sub): (&str, &ArgMatches),
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    // Each the daemon's read (step 10a U2), its text from the message (D10).
    if verb == "show" {
        let under = library_api::names::reference_target(&text_arg(sub, "ref"));
        let request = v1::GetSkillRequest {
            caller: v1::Caller::of(cx.globals(), None),
            ..v1::GetSkillRequest::default()
        };
        let read = cx.served.get_skill(&under, request);
        return served(cx, read, out, err, false, |skill, out, notes| {
            let mut said = text::Notes(Vec::new());
            let written = text::skill_show(out, &mut said, &read_back::skill_show(skill));
            notes.extend(said.0);
            written
        });
    }
    let bundle = text_arg(sub, "bundle");
    let request = || v1::ListSkillsRequest {
        caller: v1::Caller::of(cx.globals(), None),
        ..v1::ListSkillsRequest::default()
    };
    let under = match bundle.as_str() {
        "" => String::new(),
        bundle => library_api::names::bundle_target(bundle),
    };
    let read = cx.served.list_skills(&under, request());
    served(cx, read, out, err, false, |listed, out, _| {
        // A bundle that holds none: how many the others hold.
        let total = match (bundle.as_str(), listed.skills.len()) {
            ("", n) => n,
            (_, 0) => cx
                .served
                .list_skills("", request())
                .map_or(0, |(every, _)| every.skills.len()),
            (_, n) => n,
        };
        text::skill_list(out, &read_back::skill_list(listed, &bundle, total))
    })
}

pub(crate) fn write(
    cx: &Context<'_>,
    (verb, sub): (&str, &ArgMatches),
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Option<Exit>, Failure> {
    let code = match verb {
        "create" => create(cx, sub, out, err)?,
        "remove" => remove(cx, sub, out, err)?,
        "export" => export(cx, sub, out, err)?,
        "import" => import(cx, sub, out, err)?,
        _ => return Ok(None),
    };
    Ok(Some(code))
}

/// `skill create <bundle>#skills/<name>` (or the bundle and the name):
/// the daemon's `CreateSkill` (step 10b U3), its text from the response.
fn create(
    cx: &Context<'_>,
    sub: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let asked = text_arg(sub, "bundle");
    let named = text_arg(sub, "name");
    let (bundle, id) = match asked.split_once('#') {
        Some((bundle, path)) if named.is_empty() => (
            bundle.to_string(),
            path.strip_prefix("skills/").unwrap_or(path).to_string(),
        ),
        _ => (asked.clone(), named),
    };
    let request = v1::CreateSkillRequest {
        skill_id: id,
        skill: Some(v1::Skill {
            description: text_arg(sub, "description"),
            ..v1::Skill::default()
        }),
        caller: v1::Caller::of(cx.globals(), None),
        ..v1::CreateSkillRequest::default()
    };
    let under = library_api::names::bundle_target(&bundle);
    crate::answer_written(
        cx,
        cx.served.create_skill(&under, request),
        |m| std::mem::take(&mut m.notices),
        out,
        err,
        |m, out| {
            let name = m.skill.as_ref().map_or("", |s| s.name.as_str());
            let rest = name.split_once("/bundles/").map_or(name, |(_, rest)| rest);
            let (bundle, skill) = rest.split_once("/skills/").unwrap_or((rest, ""));
            writeln!(out, "Created skill \"{skill}\" in bundle \"{bundle}\"")?;
            writeln!(out, "  {}", m.path)?;
            writeln!(out, "Edit {}/SKILL.md to describe the skill", m.path)
        },
        |_| Exit::OK,
    )
}

/// `skill remove <ref> [--yes]`: the daemon's `DeleteSkill` (step 10b
/// U3), previewed and confirmed at a terminal.
fn remove(
    cx: &Context<'_>,
    sub: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let under = library_api::names::reference_target(&text_arg(sub, "ref"));
    crate::write::removal_served(
        cx,
        flag(sub, "yes"),
        |validate_only, answers, etag| {
            let request = v1::DeleteSkillRequest {
                validate_only,
                answers,
                etag,
                caller: v1::Caller::of(cx.globals(), None),
                ..v1::DeleteSkillRequest::default()
            };
            cx.served.delete_skill(&under, request)
        },
        out,
        err,
        |gone, out| {
            let name = gone.skill.as_ref().map_or("", |s| s.name.as_str());
            let rest = name.split_once("/bundles/").map_or(name, |(_, rest)| rest);
            let (bundle, skill) = rest.split_once("/skills/").unwrap_or((rest, ""));
            writeln!(out, "Removed skill \"{skill}\" from bundle \"{bundle}\"")?;
            writeln!(out, "  {}", gone.path)
        },
    )
}

fn export(
    cx: &Context<'_>,
    sub: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let reference = text_arg(sub, "ref");
    let outcome = cx
        .library
        .skill_export(&cx.flags, &reference, &text_arg(sub, "output"));
    catalog::answer(
        cx,
        outcome,
        out,
        err,
        false,
        |parent, done| exported(parent, &reference, done),
        |done, out, _| {
            writeln!(
                out,
                "Exported skill \"{}\" to {} ({} bytes)",
                done.name, done.zip_path, done.bytes
            )
        },
        ok,
    )
}

fn import(
    cx: &Context<'_>,
    sub: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let outcome = cx.library.skill_import(
        &cx.flags,
        &text_arg(sub, "archive"),
        &text_arg(sub, "bundle"),
        flag(sub, "force"),
    );
    catalog::answer(
        cx,
        outcome,
        out,
        err,
        false,
        imported,
        |done, out, _| {
            writeln!(
                out,
                "Imported skill \"{}\" into bundle \"{}\" ({} file(s))",
                done.name, done.bundle, done.file_count
            )?;
            writeln!(out, "  {}", done.dir)
        },
        ok,
    )
}

/// `skill export`: `ExportSkillResponse`, the skill named by the reference
/// asked.
pub(crate) fn exported(parent: &str, asked: &str, done: &SkillExported) -> v1::ExportSkillResponse {
    v1::ExportSkillResponse {
        skill: catalog::reference_name(parent, asked).unwrap_or_else(|| done.name.clone()),
        zip_path: done.zip_path.clone(),
        bytes: done.bytes,
        applied: Some(true),
        ..v1::ExportSkillResponse::default()
    }
}

/// `skill import`: `ImportSkillResponse`, the skill with its file count.
pub(crate) fn imported(parent: &str, done: &SkillImported) -> v1::ImportSkillResponse {
    v1::ImportSkillResponse {
        skill: Some(v1::Skill {
            name: skill_name(parent, &done.bundle, &done.name),
            path: done.dir.clone(),
            file_count: count(done.file_count),
            ..v1::Skill::default()
        }),
        path: done.dir.clone(),
        applied: Some(true),
        ..v1::ImportSkillResponse::default()
    }
}

#[cfg(test)]
#[path = "skill.test.rs"]
mod tests;
