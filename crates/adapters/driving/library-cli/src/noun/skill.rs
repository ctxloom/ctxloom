//! `skill`: list and show, and its writes (B1), each json answer its AIP
//! resource or method response (OD-40, design §5.3).

use library_application::{
    Removal, SkillCreated, SkillExported, SkillImported, SkillList, SkillRemoved, SkillShow,
};

use crate::catalog::{self, ok};
use crate::prelude::*;

pub(crate) fn read(
    cx: &Context<'_>,
    (verb, sub): (&str, &ArgMatches),
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    if verb == "show" {
        let outcome = cx.library.skill_show(&cx.flags, &text_arg(sub, "ref"));
        return catalog::answer(
            cx,
            outcome,
            out,
            err,
            false,
            shown,
            |show, out, notes| {
                let mut said = text::Notes(Vec::new());
                let written = text::skill_show(out, &mut said, show);
                notes.extend(said.0);
                written
            },
            ok,
        );
    }
    let outcome = cx.library.skill_list(&cx.flags, &text_arg(sub, "bundle"));
    catalog::answer(
        cx,
        outcome,
        out,
        err,
        false,
        listed,
        |list, out, _| text::skill_list(out, list),
        ok,
    )
}

/// A skill's resource name.
fn skill_name(parent: &str, bundle: &str, id: &str) -> String {
    catalog::item_name(parent, bundle, "skills", id)
}

/// A count as the API's uint32 (OQ-12), saturating.
fn count(n: u64) -> u32 {
    u32::try_from(n).unwrap_or(u32::MAX)
}

/// `skill list`: `ListSkillsResponse`.
pub(crate) fn listed(parent: &str, list: &SkillList) -> v1::ListSkillsResponse {
    v1::ListSkillsResponse {
        skills: list
            .rows
            .iter()
            .map(|row| {
                // A row's name is `<bundle>/<skill>`, its source the bundle.
                let id = row
                    .name
                    .strip_prefix(&format!("{}/", row.source))
                    .unwrap_or(&row.name);
                v1::Skill {
                    name: skill_name(parent, &row.source, id),
                    description: row.description.clone(),
                    tags: row.tags.clone(),
                    file_count: count(row.file_count),
                    ..v1::Skill::default()
                }
            })
            .collect(),
        next_page_token: String::new(),
    }
}

/// `skill show`: the `Skill` (FULL), each file's executability its
/// sidecar's.
pub(crate) fn shown(parent: &str, show: &SkillShow) -> v1::Skill {
    v1::Skill {
        name: skill_name(parent, &show.bundle, &show.name),
        description: show.description.clone(),
        license: show.license.clone(),
        compatibility: show.compatibility.clone(),
        allowed_tools: show.allowed_tools.clone(),
        body: show.body.clone(),
        file_count: count(show.files.len() as u64),
        files: show
            .files
            .iter()
            .map(|file| v1::SkillFile {
                path: file.path.clone(),
                sha256: file.sha256.clone(),
                executable: file.mode == "0755",
            })
            .collect(),
        ..v1::Skill::default()
    }
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

fn create(
    cx: &Context<'_>,
    sub: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let outcome = cx.library.skill_create(
        &cx.flags,
        &text_arg(sub, "bundle"),
        &text_arg(sub, "name"),
        &text_arg(sub, "description"),
    );
    catalog::answer(
        cx,
        outcome,
        out,
        err,
        false,
        created,
        |made, out, _| {
            writeln!(
                out,
                "Created skill \"{}\" in bundle \"{}\"",
                made.name, made.bundle
            )?;
            writeln!(out, "  {}", made.dir)?;
            writeln!(out, "Edit {}/SKILL.md to describe the skill", made.dir)
        },
        ok,
    )
}

fn remove(
    cx: &Context<'_>,
    sub: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let reference = text_arg(sub, "ref");
    let outcome = cx
        .library
        .skill_remove(&cx.flags, &reference, flag(sub, "yes"));
    catalog::answer(
        cx,
        outcome,
        out,
        err,
        false,
        |parent, gone| removed(parent, &reference, gone),
        |gone, out, _| {
            catalog::removal_text(gone, out, |gone, out| {
                writeln!(
                    out,
                    "Removed skill \"{}\" from bundle \"{}\"",
                    gone.name, gone.bundle
                )?;
                writeln!(out, "  {}", gone.dir)
            })
        },
        crate::write::removal_exit,
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

/// `skill create`: `CreateSkillResponse`.
pub(crate) fn created(parent: &str, made: &SkillCreated) -> v1::CreateSkillResponse {
    v1::CreateSkillResponse {
        skill: Some(v1::Skill {
            name: skill_name(parent, &made.bundle, &made.name),
            path: made.dir.clone(),
            ..v1::Skill::default()
        }),
        path: made.dir.clone(),
        applied: Some(true),
        ..v1::CreateSkillResponse::default()
    }
}

/// `skill remove`: `DeleteSkillResponse`, the preview's (named by the
/// reference asked) or the removal's.
pub(crate) fn removed(
    parent: &str,
    asked: &str,
    gone: &Removal<SkillRemoved>,
) -> v1::DeleteSkillResponse {
    catalog::removal(
        gone,
        |preview| v1::DeleteSkillResponse {
            skill: Some(v1::Skill {
                name: catalog::reference_name(parent, asked).unwrap_or_else(|| asked.to_string()),
                ..v1::Skill::default()
            }),
            applied: Some(false),
            detail: preview.detail,
            apply_command: preview.apply_command,
            ..v1::DeleteSkillResponse::default()
        },
        |done| v1::DeleteSkillResponse {
            skill: Some(v1::Skill {
                name: skill_name(parent, &done.bundle, &done.name),
                path: done.dir.clone(),
                ..v1::Skill::default()
            }),
            path: done.dir.clone(),
            applied: Some(true),
            ..v1::DeleteSkillResponse::default()
        },
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
