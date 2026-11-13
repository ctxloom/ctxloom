//! `profile`: list and show, and its writes (B1), each json answer its AIP
//! resource or method response (OD-40, design §5.5). A write that answered
//! in text only (create, edit, modify, export, import) still does, unless
//! json is asked for by name.

use library_application::{
    ProfileCreated, ProfileExported, ProfileImported, ProfileList, ProfileModified, ProfileRemoved,
    ProfileRow, ProfileShow, Removal,
};

use crate::catalog::{self, ok};
use crate::prelude::*;

/// The bundle a project's own profiles are items of.
const PROJECT_BUNDLE: &str = "project";

pub(crate) fn read(
    cx: &Context<'_>,
    (verb, sub): (&str, &ArgMatches),
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    if verb == "show" {
        let outcome = cx.library.profile_show(&cx.flags, &text_arg(sub, "name"));
        return catalog::answer(
            cx,
            outcome,
            out,
            err,
            false,
            shown,
            |show, out, _| text::profile_show(out, show),
            ok,
        );
    }
    let outcome = cx.library.profile_list(&cx.flags);
    catalog::answer(
        cx,
        outcome,
        out,
        err,
        false,
        listed,
        |list, out, _| text::profile_list(out, list),
        ok,
    )
}

/// The profile's own name within its key (`<bundle>#profiles/<name>`).
fn id_of(name: &str) -> &str {
    name.split_once("#profiles/").map_or(name, |(_, id)| id)
}

/// A profile's resource name, below the bundle that ships it.
fn profile_name(parent: &str, entry: &str, id: &str) -> String {
    let entry = if entry.is_empty() {
        PROJECT_BUNDLE
    } else {
        entry
    };
    catalog::item_name(parent, entry, "profiles", id)
}

/// `profile list`: `ListProfilesResponse`.
pub(crate) fn listed(parent: &str, list: &ProfileList) -> v1::ListProfilesResponse {
    v1::ListProfilesResponse {
        profiles: list
            .rows
            .iter()
            .map(|row| listed_row(parent, row))
            .collect(),
        next_page_token: String::new(),
    }
}

fn listed_row(parent: &str, row: &ProfileRow) -> v1::Profile {
    v1::Profile {
        name: profile_name(parent, &row.entry, &row.display_name),
        description: row.description.clone(),
        parents: parents(parent, &row.parents),
        tags: row.tags.clone(),
        bundles: bundles(parent, &row.bundles),
        default: Some(row.default),
        path: row.path.clone(),
        ..<v1::Profile as Default>::default()
    }
}

/// A profile's parents as Profile names (OQ-16: one with no place keeps
/// its text).
fn parents(parent: &str, asks: &[String]) -> Vec<String> {
    asks.iter()
        .map(|ask| catalog::profile_ref(parent, ask))
        .collect()
}

/// A profile's bundles as Bundle (or item) names.
fn bundles(parent: &str, asks: &[String]) -> Vec<String> {
    asks.iter()
        .map(|ask| catalog::bundle_ref(parent, ask))
        .collect()
}

/// `profile show`: the `Profile` (FULL).
pub(crate) fn shown(parent: &str, show: &ProfileShow) -> v1::Profile {
    v1::Profile {
        default: Some(show.default),
        ..profile_of(
            parent,
            &profile_name(parent, &show.entry, id_of(&show.name)),
            show,
        )
    }
}

/// A profile as its `Profile`, named `name`.
pub(crate) fn profile_of(parent: &str, name: &str, show: &ProfileShow) -> v1::Profile {
    v1::Profile {
        name: name.to_string(),
        description: show.description.clone(),
        parents: parents(parent, &show.parents),
        tags: show.tags.clone(),
        bundles: bundles(parent, &show.bundles),
        variables: show.variables.clone(),
        exclude_fragments: show.exclude_fragments.clone(),
        exclude_mcp_servers: show.exclude_mcp.clone(),
        path: show.path.clone(),
        ..<v1::Profile as Default>::default()
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
        "modify" | "edit" if verb == "modify" || patch_asked(sub) => modify(cx, sub, out, err)?,
        "edit" => edit_in_editor(cx, sub, out, err)?,
        "export" => export(cx, sub, out, err)?,
        "import" => import(cx, sub, out, err)?,
        "remove" => remove(cx, sub, out, err)?,
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
    let name = text_arg(sub, "name");
    let request = ProfileCreateRequest {
        name,
        description: text_arg(sub, "description"),
        parents: many(sub, "parent"),
        bundles: many(sub, "include"),
        target: text_arg(sub, "bundle"),
    };
    let outcome = cx.library.profile_create(&cx.flags, &request);
    catalog::answer(
        cx,
        outcome,
        out,
        err,
        true,
        |parent, made| created(parent, &request, made),
        |made, out, _| {
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
        },
        ok,
    )
}

fn modify(
    cx: &Context<'_>,
    sub: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let name = text_arg(sub, "name");
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
    catalog::answer(
        cx,
        outcome,
        out,
        err,
        true,
        |parent, done| modified(parent, &name, done),
        |done, out, _| {
            if done.changes.is_empty() {
                return nothing(out);
            }
            for change in &done.changes {
                writeln!(out, "{change}")?;
            }
            writeln!(out, "Modified profile \"{}\"", done.name)
        },
        ok,
    )
}

fn edit_in_editor(
    cx: &Context<'_>,
    sub: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let name = text_arg(sub, "name");
    let outcome = cx.library.profile_edit(&cx.flags, &name);
    catalog::answer(
        cx,
        outcome,
        out,
        err,
        true,
        |parent, done| edited(parent, &name, done),
        |done, out, _| match done {
            ProfileEdited::Unchanged => nothing(out),
            ProfileEdited::Updated { path } => writeln!(out, "Updated profile: {path}"),
        },
        ok,
    )
}

fn export(
    cx: &Context<'_>,
    sub: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let name = text_arg(sub, "name");
    let outcome = cx
        .library
        .profile_export(&cx.flags, &name, &text_arg(sub, "dest"));
    catalog::answer(
        cx,
        outcome,
        out,
        err,
        true,
        |parent, done| exported(parent, &name, done),
        |done, out, _| writeln!(out, "Exported: {} -> {}", done.source, done.dest),
        ok,
    )
}

fn import(
    cx: &Context<'_>,
    sub: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let target = text_arg(sub, "bundle");
    let outcome = cx.library.profile_import(
        &cx.flags,
        &text_arg(sub, "path"),
        flag(sub, "force"),
        &target,
    );
    catalog::answer(
        cx,
        outcome,
        out,
        err,
        true,
        |parent, done| imported(parent, &target, done),
        |done, out, _| writeln!(out, "Imported: {} -> {}", done.source, done.dest),
        ok,
    )
}

fn remove(
    cx: &Context<'_>,
    sub: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let name = text_arg(sub, "name");
    let outcome = cx
        .library
        .profile_remove(&cx.flags, &name, flag(sub, "yes"));
    catalog::answer(
        cx,
        outcome,
        out,
        err,
        false,
        |parent, gone| removed(parent, &name, gone),
        |gone, out, _| {
            catalog::removal_text(gone, out, |gone, out| {
                writeln!(out, "Removed profile \"{}\"", gone.profile)
            })
        },
        crate::write::removal_exit,
    )
}

/// A profile named and nothing else, as a write answers it.
fn just_named(name: String) -> Option<v1::Profile> {
    Some(v1::Profile {
        name,
        ..<v1::Profile as Default>::default()
    })
}

/// `profile create --format json`: `CreateProfileResponse`, the profile in
/// the bundle it was written into.
pub(crate) fn created(
    parent: &str,
    request: &ProfileCreateRequest,
    made: &ProfileCreated,
) -> v1::CreateProfileResponse {
    v1::CreateProfileResponse {
        profile: Some(v1::Profile {
            name: profile_name(parent, &request.target, &made.name),
            description: request.description.clone(),
            parents: parents(parent, &made.parents),
            bundles: bundles(parent, &made.bundles),
            path: made.path.clone(),
            ..<v1::Profile as Default>::default()
        }),
        path: made.path.clone(),
        applied: Some(true),
        ..v1::CreateProfileResponse::default()
    }
}

/// `profile modify|edit <flags> --format json`: `UpdateProfileResponse`;
/// a modification that changed nothing applied nothing.
pub(crate) fn modified(
    parent: &str,
    asked: &str,
    done: &ProfileModified,
) -> v1::UpdateProfileResponse {
    v1::UpdateProfileResponse {
        profile: just_named(catalog::profile_ref(parent, asked)),
        applied: Some(!done.changes.is_empty()),
        changes: done.changes.clone(),
        ..v1::UpdateProfileResponse::default()
    }
}

/// `profile edit --format json` (the editor's round trip):
/// `UpdateProfileResponse`.
pub(crate) fn edited(parent: &str, asked: &str, done: &ProfileEdited) -> v1::UpdateProfileResponse {
    let (path, applied) = match done {
        ProfileEdited::Unchanged => (String::new(), false),
        ProfileEdited::Updated { path } => (path.clone(), true),
    };
    v1::UpdateProfileResponse {
        profile: just_named(catalog::profile_ref(parent, asked)),
        path,
        applied: Some(applied),
        ..v1::UpdateProfileResponse::default()
    }
}

/// `profile export --format json`: `ExportProfileResponse`.
pub(crate) fn exported(
    parent: &str,
    asked: &str,
    done: &ProfileExported,
) -> v1::ExportProfileResponse {
    v1::ExportProfileResponse {
        profile: catalog::profile_ref(parent, asked),
        source: done.source.clone(),
        destination: done.dest.clone(),
        applied: Some(true),
        ..v1::ExportProfileResponse::default()
    }
}

/// `profile import --format json`: `ImportProfileResponse`, the profile
/// named by the file it landed in.
pub(crate) fn imported(
    parent: &str,
    target: &str,
    done: &ProfileImported,
) -> v1::ImportProfileResponse {
    let file = done.dest.rsplit(['/', '\\']).next().unwrap_or_default();
    let id = file.strip_suffix(".yaml").unwrap_or(file);
    v1::ImportProfileResponse {
        profile: just_named(profile_name(parent, target, id)),
        path: done.dest.clone(),
        applied: Some(true),
        ..v1::ImportProfileResponse::default()
    }
}

/// `profile remove`: `DeleteProfileResponse`, the preview's or the
/// removal's, named by the profile asked.
pub(crate) fn removed(
    parent: &str,
    asked: &str,
    gone: &Removal<ProfileRemoved>,
) -> v1::DeleteProfileResponse {
    let name = catalog::profile_ref(parent, asked);
    catalog::removal(
        gone,
        |preview| v1::DeleteProfileResponse {
            profile: just_named(name.clone()),
            applied: Some(false),
            detail: preview.detail,
            apply_command: preview.apply_command,
            ..v1::DeleteProfileResponse::default()
        },
        |_| v1::DeleteProfileResponse {
            profile: just_named(name.clone()),
            applied: Some(true),
            ..v1::DeleteProfileResponse::default()
        },
    )
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
