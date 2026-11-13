//! `skill`: list and show, and its writes (B1).

use crate::prelude::*;

pub(crate) fn read(
    cx: &Context<'_>,
    (verb, sub): (&str, &ArgMatches),
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    if verb == "show" {
        let outcome = cx.library.skill_show(&cx.flags, &text_arg(sub, "ref"));
        return answer(cx, outcome, out, err, |show, out, notes| {
            emit(cx.format, out, &show, |out| {
                let mut said = text::Notes(Vec::new());
                let written = text::skill_show(out, &mut said, &show);
                notes.extend(said.0);
                written
            })
        });
    }
    let outcome = cx.library.skill_list(&cx.flags, &text_arg(sub, "bundle"));
    answer(cx, outcome, out, err, |list, out, _| {
        emit(cx.format, out, &list.rows, |out| {
            text::skill_list(out, &list)
        })
    })
}

pub(crate) fn write(
    cx: &Context<'_>,
    (verb, sub): (&str, &ArgMatches),
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Option<Exit>, Failure> {
    let reference = text_arg(sub, "ref");
    let code = match verb {
        "create" => {
            let outcome = cx.library.skill_create(
                &cx.flags,
                &text_arg(sub, "bundle"),
                &text_arg(sub, "name"),
                &text_arg(sub, "description"),
            );
            finish(cx, outcome, out, err, |made, out, _| {
                writeln!(
                    out,
                    "Created skill \"{}\" in bundle \"{}\"",
                    made.name, made.bundle
                )?;
                writeln!(out, "  {}", made.dir)?;
                writeln!(out, "Edit {}/SKILL.md to describe the skill", made.dir)
            })?
        }
        "remove" => {
            let outcome = cx
                .library
                .skill_remove(&cx.flags, &reference, flag(sub, "yes"));
            removal(cx, outcome, out, err, |gone, out| {
                writeln!(
                    out,
                    "Removed skill \"{}\" from bundle \"{}\"",
                    gone.name, gone.bundle
                )?;
                writeln!(out, "  {}", gone.dir)
            })?
        }
        "export" => {
            let outcome = cx
                .library
                .skill_export(&cx.flags, &reference, &text_arg(sub, "output"));
            finish(cx, outcome, out, err, |done, out, _| {
                writeln!(
                    out,
                    "Exported skill \"{}\" to {} ({} bytes)",
                    done.name, done.zip_path, done.bytes
                )
            })?
        }
        "import" => {
            let outcome = cx.library.skill_import(
                &cx.flags,
                &text_arg(sub, "archive"),
                &text_arg(sub, "bundle"),
                flag(sub, "force"),
            );
            finish(cx, outcome, out, err, |done, out, _| {
                writeln!(
                    out,
                    "Imported skill \"{}\" into bundle \"{}\" ({} file(s))",
                    done.name, done.bundle, done.file_count
                )?;
                writeln!(out, "  {}", done.dir)
            })?
        }
        _ => return Ok(None),
    };
    Ok(Some(code))
}

#[cfg(test)]
#[path = "skill.test.rs"]
mod tests;
