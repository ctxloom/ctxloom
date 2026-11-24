//! `profile`: list and show, and its writes (B1), each json answer its AIP
//! resource or method response (OD-40, design §5.5). A write that answered
//! in text only (create, edit, modify, export, import) still does, unless
//! json is asked for by name.

use library_application::{ProfileExported, ProfileImported};

use crate::catalog::{self, ok};
use library_api::resources::profile_name;

use crate::prelude::*;

pub(crate) fn read(
    cx: &Context<'_>,
    (verb, sub): (&str, &ArgMatches),
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    // The daemon's reads (step 10a U2), their text from the message (D10).
    if verb == "show" {
        let request = v1::GetProfileRequest {
            caller: v1::Caller::of(cx.globals(), None),
            ..v1::GetProfileRequest::default()
        };
        let under = library_api::names::profile_target(&text_arg(sub, "name"));
        let read = cx.served.get_profile(&under, request);
        return served(cx, read, out, err, false, |profile, out, _| {
            text::profile_show(out, &crate::read_back::profile_show(profile))
        });
    }
    let request = v1::ListProfilesRequest {
        caller: v1::Caller::of(cx.globals(), None),
        ..v1::ListProfilesRequest::default()
    };
    let in_project = cx.served.in_project();
    let read = cx.served.list_profiles("", request);
    served(cx, read, out, err, false, |listed, out, _| {
        text::profile_list(out, &crate::read_back::profile_list(listed, in_project))
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
        "modify" | "edit" if verb == "modify" || patch_asked(sub) => modify(cx, sub, out, err)?,
        "edit" => edit_in_editor(cx, sub, out, err)?,
        "export" => export(cx, sub, out, err)?,
        "import" => import(cx, sub, out, err)?,
        "remove" => remove(cx, sub, out, err)?,
        _ => return Ok(None),
    };
    Ok(Some(code))
}

/// `profile create <name> [flags] [--bundle B]`: the daemon's
/// `CreateProfile` (step 10b U3), under the bundle `--bundle` names (none:
/// the project's own, made with it the first time). Its text from the
/// response (D10); json off a terminal, as every verb's (smoke 2026-11-19).
fn create(
    cx: &Context<'_>,
    sub: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let target = text_arg(sub, "bundle");
    let under = if target.is_empty() {
        String::new()
    } else {
        library_api::names::bundle_target(&target)
    };
    let request = v1::CreateProfileRequest {
        profile_id: text_arg(sub, "name"),
        profile: Some(v1::Profile {
            description: text_arg(sub, "description"),
            parents: many(sub, "parent"),
            // `--include`, and the vocabulary's `--add-bundle`, in that order.
            bundles: [many(sub, "include"), many(sub, "add-bundle")].concat(),
            skills: many(sub, "add-skill"),
            ..<v1::Profile as Default>::default()
        }),
        caller: v1::Caller::of(cx.globals(), None),
        ..v1::CreateProfileRequest::default()
    };
    let asked = request.profile.clone().unwrap_or_default();
    crate::answer_written(
        cx,
        cx.served.create_profile(&under, request),
        |m| std::mem::take(&mut m.notices),
        out,
        err,
        |m, out| {
            // Said as she wrote them (the response names them as resources).
            let mut parts = Vec::new();
            if !asked.parents.is_empty() {
                parts.push(format!("parents: {}", asked.parents.join(", ")));
            }
            if !asked.bundles.is_empty() {
                parts.push(format!("bundles: {}", asked.bundles.join(", ")));
            }
            let with = if parts.is_empty() {
                String::new()
            } else {
                format!(" with {}", parts.join("; "))
            };
            let name = m.profile.as_ref().map_or("", |p| p.name.as_str());
            let id = name.rsplit('/').next().unwrap_or(name);
            writeln!(out, "Created profile \"{id}\"{with}")?;
            writeln!(out, "Saved to: {}", m.path)
        },
        |_| Exit::OK,
    )
}

/// The context of a write that answers in text unless json is asked for
/// by name (OD-40).
fn text_unless_asked<'a>(cx: &'a Context<'a>) -> Context<'a> {
    let json_by_name = cx.explicit && cx.format == presentation::Format::Json;
    cx.formatted(if json_by_name {
        presentation::Format::Json
    } else {
        presentation::Format::Text
    })
}

/// `profile modify|edit <name> <flags>`: the daemon's `UpdateProfile` as a
/// patch (step 10b U3), one call: its lists as given, `description` named
/// by its mask (a mask sent even when it names none, so nothing given is
/// a patch of nothing, never the editor).
fn modify(
    cx: &Context<'_>,
    sub: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let name = text_arg(sub, "name");
    let mut profile = <v1::Profile as Default>::default();
    let mut paths = Vec::new();
    if let Some(description) = given(sub, "description") {
        profile.description = description;
        paths.push("description".to_string());
    }
    let request = v1::UpdateProfileRequest {
        profile: Some(profile),
        update_mask: Some(daemon_proto::google::protobuf::FieldMask { paths }),
        add_parents: many(sub, "add-parent"),
        remove_parents: many(sub, "remove-parent"),
        add_bundles: many(sub, "add-bundle"),
        remove_bundles: many(sub, "remove-bundle"),
        exclude_fragments: many(sub, "exclude-fragment"),
        include_fragments: many(sub, "include-fragment"),
        exclude_mcp_servers: many(sub, "exclude-mcp"),
        include_mcp_servers: many(sub, "include-mcp"),
        caller: v1::Caller::of(cx.globals(), None),
        ..v1::UpdateProfileRequest::default()
    };
    let under = library_api::names::profile_target(&name);
    let cx = text_unless_asked(cx);
    crate::answer_written(
        &cx,
        cx.served.update_profile(&under, request),
        |m| std::mem::take(&mut m.notices),
        out,
        err,
        |m, out| {
            if m.changes.is_empty() {
                return nothing(out);
            }
            for change in &m.changes {
                writeln!(out, "{change}")?;
            }
            writeln!(out, "Modified profile \"{name}\"")
        },
        |_| Exit::OK,
    )
}

/// `profile edit <name>` with no flag: the daemon's `UpdateProfile` as the
/// editor's round trip (step 10b U3): its preview's Edit need presented
/// here (the EDITOR launched on the profile's text), the apply sent with
/// what it left and the preview's etag.
fn edit_in_editor(
    cx: &Context<'_>,
    sub: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let under = library_api::names::profile_target(&text_arg(sub, "name"));
    let ask =
        |validate_only: bool, answers: Vec<v1::Answer>, etag: String| v1::UpdateProfileRequest {
            validate_only,
            answers,
            etag,
            caller: v1::Caller::of(cx.globals(), cx.presentable()),
            ..v1::UpdateProfileRequest::default()
        };
    let preview = cx
        .served
        .update_profile(&under, ask(true, Vec::new(), String::new()))?;
    let answers = crate::noun::item::edit_answers(cx, &preview.needs)?;
    let edited = cx
        .served
        .update_profile(&under, ask(false, answers, preview.etag));
    let cx = text_unless_asked(cx);
    crate::answer_written(
        &cx,
        edited,
        |m| std::mem::take(&mut m.notices),
        out,
        err,
        |m, out| {
            if !m.applied.unwrap_or(false) {
                return nothing(out);
            }
            writeln!(out, "Updated profile: {}", m.path)
        },
        |_| Exit::OK,
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

/// `profile remove <name> [--yes]`: the daemon's `DeleteProfile` (step
/// 10b U3), previewed and confirmed at a terminal.
fn remove(
    cx: &Context<'_>,
    sub: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let name = text_arg(sub, "name");
    let under = library_api::names::profile_target(&name);
    crate::write::removal_served(
        cx,
        flag(sub, "yes"),
        |validate_only, answers, etag| {
            let request = v1::DeleteProfileRequest {
                validate_only,
                answers,
                etag,
                caller: v1::Caller::of(cx.globals(), None),
                ..v1::DeleteProfileRequest::default()
            };
            cx.served.delete_profile(&under, request)
        },
        out,
        err,
        |_, out| writeln!(out, "Removed profile \"{name}\""),
    )
}

/// A profile named and nothing else, as a write answers it.
fn just_named(name: String) -> Option<v1::Profile> {
    Some(v1::Profile {
        name,
        ..<v1::Profile as Default>::default()
    })
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
