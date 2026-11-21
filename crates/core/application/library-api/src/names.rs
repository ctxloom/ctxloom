//! The library's resource names (OD-40, B1; design §1.2 and §3.2 to §3.6):
//! a bundle's name from its catalog name, with its origin in the pattern
//! (OQ-4), an item's below its bundle's, and a configured reference
//! resolved to a name where its text says which (a reference it cannot
//! place keeps its text, OQ-16). The one naming, the daemon's answers' and
//! (until each noun is served, U2) the CLI's alike.
//!
//! An ID on disk that breaks the item grammar (OQ-6) is named as it is: the
//! grammar is enforced where a write mints a name, and a name already on
//! disk still reads.

use daemon_proto::v1;

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
pub fn bundle_name(parent: &str, catalog: &str) -> String {
    let catalog = catalog.strip_prefix(LOCAL).unwrap_or(catalog);
    if let Some(companion) = catalog.strip_prefix(COMPANION) {
        return format!("{parent}/companions/{companion}/bundles/loadout");
    }
    match catalog.split_once('/') {
        Some((remote, bundle)) => format!("{parent}/remotes/{remote}/bundles/{bundle}"),
        None => format!("{parent}/bundles/{catalog}"),
    }
}

/// The catalog name a bundle's resource name under `parent` stands for
/// ([`bundle_name`] read back): `<b>`, `<r>/<b>` for a remote's, a
/// companion's loadout by its companion; a name under no parent is its
/// text.
pub fn catalog_name(parent: &str, name: &str) -> String {
    let Some(rest) = name
        .strip_prefix(parent)
        .and_then(|rest| rest.strip_prefix('/'))
    else {
        return name.to_string();
    };
    let segments: Vec<&str> = rest.split('/').collect();
    match segments.as_slice() {
        ["bundles", bundle] => (*bundle).to_string(),
        ["remotes", remote, "bundles", bundle] => format!("{remote}/{bundle}"),
        ["companions", companion, "bundles", "loadout"] => format!("{COMPANION}{companion}"),
        _ => name.to_string(),
    }
}

/// Where a bundle of `catalog` name comes from; `builtin` when the catalog
/// says it is ctxloom's own.
pub fn origin(catalog: &str, builtin: bool) -> v1::bundle::Origin {
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
pub fn item_name(parent: &str, bundle: &str, dir: &str, id: &str) -> String {
    let collection = collection(dir).unwrap_or(dir);
    format!("{}/{collection}/{id}", bundle_name(parent, bundle))
}

/// The resource name an item reference (`<bundle>#<kind>/<name>`, its
/// bundle any catalog or local canonical name) names, or none when it is
/// not one (a bundle on a remote's URL keeps its text, OQ-16).
pub fn reference_name(parent: &str, reference: &str) -> Option<String> {
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
pub fn profile_ref(parent: &str, text: &str) -> String {
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
pub fn bundle_ref(parent: &str, text: &str) -> String {
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

/// The kind directory a reference names an item of in `collection` ([`collection`]
/// read back): `mcpServers` is `mcp`, the rest their own word.
fn kind_dir(collection: &str) -> Option<&'static str> {
    Some(match collection {
        "fragments" => "fragments",
        "commands" => "commands",
        "skills" => "skills",
        "mcpServers" => "mcp",
        "profiles" => "profiles",
        _ => return None,
    })
}

/// The short library reference a resource name stands for (owner ruling
/// 2026-11-17, step 10a U2): harp-free, as a person types it on the
/// command line and text prints it: a bundle by its catalog name (`demo`,
/// `<r>/<b>`, a companion's loadout by its companion), an item as
/// `<bundle>#<kind>/<id>` (`project#profiles/dev`, `demo#mcp/x`). A name
/// of any other shape (configured text that names no place, OQ-16; an
/// engine, an agent) is its text.
pub fn short_reference(name: &str) -> String {
    let parent = match name.match_indices('/').nth(1) {
        Some((offset, _)) => &name[..offset],
        None => return name.to_string(),
    };
    let Some((bundle, rest)) = split_bundle(parent, name) else {
        return name.to_string();
    };
    match rest.as_slice() {
        [] => bundle,
        [collection, id] => match kind_dir(collection) {
            Some(dir) => format!("{bundle}#{dir}/{id}"),
            None => name.to_string(),
        },
        _ => name.to_string(),
    }
}

/// What a read's target says after its parent (step 10a U2): the bundle a
/// command names, by its text as typed, so the daemon reads it back
/// exactly ([`bundle_under`]): `/bundles/<b>`, `/remotes/<r>/bundles/<b>`
/// for a remote's, `/companions/<c>/bundles/loadout` for a companion's
/// loadout, as [`bundle_name`] names them; any other text (a canonical
/// reference, one with more than one `/`) is `/bundles/<text>` with its `%`
/// and `/` escaped, so no text a command takes is lost on the way.
pub fn bundle_target(text: &str) -> String {
    let plain = |part: &str| !part.is_empty() && !part.contains(['/', '%']);
    if let Some(companion) = text.strip_prefix(COMPANION) {
        if plain(companion) {
            return format!("/companions/{companion}/bundles/loadout");
        }
    }
    if !text.contains('#') && !text.starts_with(COMPANION) {
        if let Some((remote, bundle)) = text.split_once('/') {
            if plain(remote) && plain(bundle) {
                return format!("/remotes/{remote}/bundles/{bundle}");
            }
        }
    }
    if plain(text) {
        return format!("/bundles/{text}");
    }
    format!("/bundles/{}", escaped(text))
}

/// What a read's target says after its parent for an item a command names
/// by its reference (`<bundle>#<kind>/<name>`), as typed: its bundle's
/// [`bundle_target`], then `/<kind>/<name>`; a path of any other shape
/// (none, one segment, more) is `/-/<path>` escaped, and a reference with
/// no `#` is its bundle's target alone. [`reference_under`] reads it back.
pub fn reference_target(reference: &str) -> String {
    let Some((bundle, path)) = reference.split_once('#') else {
        return bundle_target(reference);
    };
    let plain = |part: &str| !part.is_empty() && !part.contains(['/', '%']);
    let item = match path.split_once('/') {
        Some((kind, id)) if plain(kind) && plain(id) && kind != "-" => format!("/{kind}/{id}"),
        _ => format!("/-/{}", escaped(path)),
    };
    format!("{}{item}", bundle_target(bundle))
}

/// What a read's target says after its parent for a profile a command
/// names, as typed: a reference (`<bundle>#profiles/<name>`) is its
/// [`reference_target`]; any other text (a project profile's bare name, or
/// text of no shape) is `/profiles/<text>` escaped, so [`profile_under`]
/// reads it back exactly and the use case refuses it in its own words.
pub fn profile_target(text: &str) -> String {
    if text.contains('#') {
        return reference_target(text);
    }
    named_target("profiles", text)
}

/// The profile text a target under `parent` names ([`profile_target`] read
/// back), or none when it names none.
pub fn profile_under(parent: &str, name: &str) -> Option<String> {
    named_under(parent, "profiles", name).or_else(|| reference_under(parent, name))
}

/// What a read's target says after its parent for a resource of
/// `collection` directly under the parent (an agent, an engine), named by
/// its text as typed: `/<collection>/<text>`, the text escaped as one
/// segment. [`named_under`] reads it back.
pub fn named_target(collection: &str, text: &str) -> String {
    format!("/{collection}/{}", escaped(text))
}

/// The text a target under `parent` names in `collection`
/// ([`named_target`] read back), or none when it names none there.
pub fn named_under(parent: &str, collection: &str, name: &str) -> Option<String> {
    let rest = name.strip_prefix(parent)?.strip_prefix('/')?;
    let id = rest.strip_prefix(collection)?.strip_prefix('/')?;
    (!id.contains('/')).then(|| unescaped(id))
}

/// The bundle text a target under `parent` names ([`bundle_target`] read
/// back), or none when it names no bundle.
pub fn bundle_under(parent: &str, name: &str) -> Option<String> {
    let (bundle, rest) = split_bundle(parent, name)?;
    rest.is_empty().then_some(bundle)
}

/// The item reference a target under `parent` names ([`reference_target`]
/// read back), or none when it names no bundle.
pub fn reference_under(parent: &str, name: &str) -> Option<String> {
    let (bundle, rest) = split_bundle(parent, name)?;
    match rest.as_slice() {
        [] => Some(bundle),
        ["-", path] => Some(format!("{bundle}#{}", unescaped(path))),
        [kind, id] => Some(format!("{bundle}#{kind}/{id}")),
        _ => None,
    }
}

/// A target's bundle text and the segments after its bundle.
fn split_bundle<'a>(parent: &str, name: &'a str) -> Option<(String, Vec<&'a str>)> {
    let rest = name.strip_prefix(parent)?.strip_prefix('/')?;
    let segments: Vec<&str> = rest.split('/').collect();
    match segments.as_slice() {
        ["companions", companion, "bundles", "loadout", rest @ ..] => {
            Some((format!("{COMPANION}{companion}"), rest.to_vec()))
        }
        ["remotes", remote, "bundles", bundle, rest @ ..] => Some((
            format!("{}/{}", unescaped(remote), unescaped(bundle)),
            rest.to_vec(),
        )),
        ["bundles", bundle, rest @ ..] => Some((unescaped(bundle), rest.to_vec())),
        _ => None,
    }
}

/// `text` with its `%` and `/` escaped, as one segment of a name.
fn escaped(text: &str) -> String {
    text.replace('%', "%25").replace('/', "%2F")
}

/// [`escaped`] read back.
fn unescaped(text: &str) -> String {
    text.replace("%2F", "/").replace("%25", "%")
}

#[cfg(test)]
#[path = "names.test.rs"]
mod tests;
