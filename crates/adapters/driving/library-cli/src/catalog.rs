//! The catalog's resource names (OD-40, B1; design §1.2 and §3.2 to §3.6):
//! a bundle's name from its catalog name, with its origin in the pattern
//! (OQ-4), an item's below its bundle's, a configured reference resolved to
//! a name where its text says which (a reference it cannot place keeps its
//! text, OQ-16), and the write path every catalog write answers through.
//!
//! An ID on disk that breaks the item grammar (OQ-6) is named as it is: the
//! grammar is enforced where a write mints a name, and a name already on
//! disk still reads.

use std::io::{self, Write};

use cli_support::{say_notices, Exit};
use daemon_proto::v1;
use library_application::{Notice, Outcome, Preview, Removal};
use serde::Serialize;

use crate::{failed, output_failure, Context, Failure, PROG};

/// A companion's loadout's catalog name begins so (`Catalog::with_companions`).
const COMPANION: &str = "ctxloom+companion:";
/// A project bundle's canonical reference begins so.
const LOCAL: &str = "ctxloom+local:";
/// The bundle a project's own profiles are items of.
const PROJECT_BUNDLE: &str = "project";

/// The resource name of the bundle a catalog (or local canonical) name
/// names: `<parent>/bundles/<b>`, `<parent>/remotes/<r>/bundles/<b>` for one
/// installed from a remote, `<parent>/companions/<c>/bundles/loadout` for a
/// companion's loadout.
pub(crate) fn bundle_name(parent: &str, catalog: &str) -> String {
    let catalog = catalog.strip_prefix(LOCAL).unwrap_or(catalog);
    if let Some(companion) = catalog.strip_prefix(COMPANION) {
        return format!("{parent}/companions/{companion}/bundles/loadout");
    }
    match catalog.split_once('/') {
        Some((remote, bundle)) => format!("{parent}/remotes/{remote}/bundles/{bundle}"),
        None => format!("{parent}/bundles/{catalog}"),
    }
}

/// Where a bundle of `catalog` name comes from; `builtin` when the catalog
/// says it is ctxloom's own.
pub(crate) fn origin(catalog: &str, builtin: bool) -> v1::bundle::Origin {
    let catalog = catalog.strip_prefix(LOCAL).unwrap_or(catalog);
    if builtin {
        v1::bundle::Origin::Builtin
    } else if catalog.starts_with(COMPANION) {
        v1::bundle::Origin::Companion
    } else if catalog.contains('/') {
        v1::bundle::Origin::Remote
    } else {
        v1::bundle::Origin::Project
    }
}

/// The collection a reference's kind directory names (`prompts` is
/// `commands`, `mcp` `mcpServers`), or none for a kind with no resource.
fn collection(dir: &str) -> Option<&'static str> {
    Some(match dir {
        "fragments" => "fragments",
        "commands" | "prompts" => "commands",
        "skills" => "skills",
        "mcp" => "mcpServers",
        "profiles" => "profiles",
        _ => return None,
    })
}

/// An item's resource name: its bundle's, then `<collection>/<id>`.
pub(crate) fn item_name(parent: &str, bundle: &str, dir: &str, id: &str) -> String {
    let collection = collection(dir).unwrap_or(dir);
    format!("{}/{collection}/{id}", bundle_name(parent, bundle))
}

/// The resource name an item reference (`<bundle>#<kind>/<name>`, its
/// bundle any catalog or local canonical name) names, or none when it is
/// not one (a bundle on a remote's URL keeps its text, OQ-16).
pub(crate) fn reference_name(parent: &str, reference: &str) -> Option<String> {
    let (bundle, path) = reference.split_once('#')?;
    let (dir, id) = path.split_once('/')?;
    if bundle.is_empty() || id.is_empty() || id.contains('/') || bundle.contains("://") {
        return None;
    }
    collection(dir)?;
    Some(item_name(parent, bundle, dir, id))
}

/// A profile as a profile's `parents` (or a command line) names it: a bare
/// name is the project's own, a reference names its bundle's. One that
/// names no place keeps its text (OQ-16).
pub(crate) fn profile_ref(parent: &str, text: &str) -> String {
    if text.contains('#') {
        return reference_name(parent, text).unwrap_or_else(|| text.to_string());
    }
    if text.is_empty() || text.contains('/') || text.contains(':') {
        return text.to_string();
    }
    item_name(parent, PROJECT_BUNDLE, "profiles", text)
}

/// A bundle as a profile's `bundles` names it: a catalog or local canonical
/// name, or one item of one (`<bundle>#<kind>/<name>`). One on a remote's
/// URL keeps its text (OQ-16).
pub(crate) fn bundle_ref(parent: &str, text: &str) -> String {
    if text.contains('#') {
        return reference_name(parent, text).unwrap_or_else(|| text.to_string());
    }
    if text.is_empty() || text.contains("://") || text.contains('@') {
        return text.to_string();
    }
    let bare = text.strip_prefix(LOCAL).unwrap_or(text);
    if bare.contains(':') && !bare.starts_with(COMPANION) {
        return text.to_string();
    }
    bundle_name(parent, text)
}

/// A distilled form there or not.
pub(crate) fn distilled(there: bool) -> i32 {
    if there {
        v1::DistilledState::Fresh as i32
    } else {
        v1::DistilledState::None as i32
    }
}

/// A distilled form as judged against its source now (T30, S51).
pub(crate) fn distilled_state(state: library_application::DistilledState) -> i32 {
    use library_application::DistilledState as State;
    let api = match state {
        State::None => v1::DistilledState::None,
        State::Fresh => v1::DistilledState::Fresh,
        State::Stale => v1::DistilledState::Stale,
    };
    api as i32
}

/// A `bundle show` entry's distilled form: stale where it no longer matches
/// its source, else there or not.
pub(crate) fn shown_distilled(there: bool, stale: bool) -> i32 {
    if stale {
        v1::DistilledState::Stale as i32
    } else {
        distilled(there)
    }
}

/// A catalog answer: the notices, said; then its error, or its json (the
/// view as its API message, named under the parent the names give, asked
/// only here), or its text with the notes it adds. `explicit_json` keeps a
/// verb that answered in text only answering in text unless json is asked
/// for by name. The exit is `exit`'s.
#[allow(clippy::too_many_arguments)]
pub(crate) fn answer<T, M: Serialize>(
    cx: &Context<'_>,
    outcome: Outcome<T>,
    out: &mut dyn Write,
    err: &mut dyn Write,
    explicit_json: bool,
    message: impl FnOnce(&str, &T) -> M,
    text: impl FnOnce(&T, &mut dyn Write, &mut Vec<String>) -> io::Result<()>,
    exit: impl FnOnce(&T) -> Exit,
) -> Result<Exit, Failure> {
    say_notices(&outcome.notices, PROG, cx.format, cx.catalog, err);
    let value = outcome.result.map_err(failed)?;
    let json = cx.format == angzarr_presentation::Format::Json && (cx.explicit || !explicit_json);
    let mut notes = Vec::new();
    if json {
        let parent = cx.names.parent()?;
        crate::api::print(out, &message(&parent, &value)).map_err(output_failure)?;
    } else {
        text(&value, out, &mut notes).map_err(output_failure)?;
    }
    let said: Vec<Notice> = notes.into_iter().map(Notice::text).collect();
    say_notices(&said, PROG, cx.format, cx.catalog, err);
    Ok(exit(&value))
}

/// A read or a write that answers ok.
pub(crate) fn ok<T>(_: &T) -> Exit {
    Exit::OK
}

/// What a removal's preview answers: nothing applied, what would go, and
/// the command that applies it.
pub(crate) struct Previewed {
    pub(crate) detail: Vec<String>,
    pub(crate) apply_command: String,
}

/// A removal as its API response: the preview's, or the removal done's.
pub(crate) fn removal<T, M>(
    removal: &Removal<T>,
    preview: impl FnOnce(Previewed) -> M,
    done: impl FnOnce(&T) -> M,
) -> M {
    match removal {
        Removal::Preview(Preview { detail, apply, .. }) => preview(Previewed {
            detail: detail.clone(),
            apply_command: apply.clone(),
        }),
        Removal::Done(removed) => done(removed),
    }
}

/// A removal's text: the preview, or `done`'s.
pub(crate) fn removal_text<T>(
    removal: &Removal<T>,
    out: &mut dyn Write,
    done: impl FnOnce(&T, &mut dyn Write) -> io::Result<()>,
) -> io::Result<()> {
    match removal {
        Removal::Preview(preview) => crate::write::preview_text(out, preview),
        Removal::Done(removed) => done(removed, out),
    }
}

#[cfg(test)]
#[path = "catalog.test.rs"]
mod tests;
