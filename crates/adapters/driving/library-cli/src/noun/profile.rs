//! `profile`: list and show, and its writes (B1).

use crate::prelude::*;

pub(crate) fn read(
    cx: &Context<'_>,
    (verb, sub): (&str, &ArgMatches),
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    if verb == "show" {
        let outcome = cx.library.profile_show(&cx.flags, &text_arg(sub, "name"));
        return answer(cx, outcome, out, err, |show, out, _| {
            emit(cx.format, out, &show, |out| text::profile_show(out, &show))
        });
    }
    let outcome = cx.library.profile_list(&cx.flags);
    answer(cx, outcome, out, err, |list, out, _| {
        emit(cx.format, out, &list.rows, |out| {
            text::profile_list(out, &list)
        })
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
        "create" => {
            text_only(cx, "profile create")?;
            let request = ProfileCreateRequest {
                name,
                description: text_arg(sub, "description"),
                parents: many(sub, "parent"),
                bundles: many(sub, "include"),
                target: text_arg(sub, "bundle"),
            };
            let outcome = cx.library.profile_create(&cx.flags, &request);
            finish_text(cx, outcome, out, err, |made, out, _| {
                let mut parts = Vec::new();
                if !made.parents.is_empty() {
                    parts.push(format!("parents: {}", made.parents.join(", ")));
                }
                if !made.bundles.is_empty() {
                    parts.push(format!("bundles: {}", made.bundles.join(", ")));
                }
                let with = if parts.is_empty() {
                    String::new()
                } else {
                    format!(" with {}", parts.join("; "))
                };
                writeln!(out, "Created profile \"{}\"{with}", made.name)?;
                writeln!(out, "Saved to: {}", made.path)
            })?
        }
        "modify" | "edit" if verb == "modify" || patch_asked(sub) => {
            text_only(cx, "profile edit")?;
            let edit = ProfileEdit {
                description: given(sub, "description"),
                add_parents: many(sub, "add-parent"),
                remove_parents: many(sub, "remove-parent"),
                add_bundles: many(sub, "add-bundle"),
                remove_bundles: many(sub, "remove-bundle"),
                exclude_fragments: many(sub, "exclude-fragment"),
                include_fragments: many(sub, "include-fragment"),
                exclude_mcp: many(sub, "exclude-mcp"),
                include_mcp: many(sub, "include-mcp"),
            };
            let outcome = cx.library.profile_modify(&cx.flags, &name, &edit);
            finish_text(cx, outcome, out, err, |done, out, _| {
                if done.changes.is_empty() {
                    return nothing(out);
                }
                for change in &done.changes {
                    writeln!(out, "{change}")?;
                }
                writeln!(out, "Modified profile \"{}\"", done.name)
            })?
        }
        "edit" => {
            text_only(cx, "profile edit")?;
            let outcome = cx.library.profile_edit(&cx.flags, &name);
            finish_text(cx, outcome, out, err, |done, out, _| match done {
                ProfileEdited::Unchanged => nothing(out),
                ProfileEdited::Updated { path } => writeln!(out, "Updated profile: {path}"),
            })?
        }
        "export" => {
            text_only(cx, "profile export")?;
            let outcome = cx
                .library
                .profile_export(&cx.flags, &name, &text_arg(sub, "dest"));
            finish_text(cx, outcome, out, err, |done, out, _| {
                writeln!(out, "Exported: {} -> {}", done.source, done.dest)
            })?
        }
        "import" => {
            text_only(cx, "profile import")?;
            let outcome = cx.library.profile_import(
                &cx.flags,
                &text_arg(sub, "path"),
                flag(sub, "force"),
                &text_arg(sub, "bundle"),
            );
            finish_text(cx, outcome, out, err, |done, out, _| {
                writeln!(out, "Imported: {} -> {}", done.source, done.dest)
            })?
        }
        "remove" => {
            let outcome = cx
                .library
                .profile_remove(&cx.flags, &name, flag(sub, "yes"));
            removal(cx, outcome, out, err, |gone, out| {
                writeln!(out, "Removed profile \"{}\"", gone.profile)
            })?
        }
        _ => return Ok(None),
    };
    Ok(Some(code))
}

/// Whether `profile edit` was given a flag to patch with (else it opens the
/// editor).
fn patch_asked(sub: &ArgMatches) -> bool {
    [
        "description",
        "add-parent",
        "remove-parent",
        "add-bundle",
        "remove-bundle",
        "exclude-fragment",
        "include-fragment",
        "exclude-mcp",
        "include-mcp",
    ]
    .iter()
    .any(|name| sub.value_source(name) == Some(clap::parser::ValueSource::CommandLine))
}

#[cfg(test)]
#[path = "profile.test.rs"]
mod tests;
