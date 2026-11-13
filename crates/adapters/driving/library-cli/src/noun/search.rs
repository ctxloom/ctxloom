//! `search` (B3): its json the `SearchLibraryResponse` of
//! `projects/{project}:searchLibrary` (OD-40, design §5.9).

use crate::prelude::*;
use library_application::{Located, RemoteFound, SearchHit, SearchResults};

pub(crate) fn read(
    cx: &Context<'_>,
    sub: &ArgMatches,
    out: &mut dyn Write,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
    let request = SearchRequest {
        query: text_arg(sub, "query"),
        tags: many(sub, "tag"),
        kind: text_arg(sub, "type"),
        local: flag(sub, "local"),
        remote: flag(sub, "remote"),
    };
    let outcome = cx.library.search(&cx.flags, &request);
    resource(cx, outcome, out, err, response, text::search)
}

/// `search`: `SearchLibraryResponse`, each local hit named under `parent`.
pub(crate) fn response(parent: &str, results: &SearchResults) -> v1::SearchLibraryResponse {
    v1::SearchLibraryResponse {
        query: results.query.clone(),
        local: results.local.iter().map(|hit| local(parent, hit)).collect(),
        remote: results.remote.iter().map(remote).collect(),
        count: count(results.count),
        hidden_local: count(results.hidden_local),
        next_page_token: String::new(),
    }
}

/// A local hit, named by its resource; one whose bundle the catalog does
/// not hold keeps the name it was found by (OQ-16).
fn local(parent: &str, hit: &SearchHit) -> v1::SearchHit {
    let (kind, collection) = kind(&hit.kind);
    v1::SearchHit {
        resource: hit.located.as_ref().map_or_else(
            || hit.name.clone(),
            |located| item_name(parent, located, collection),
        ),
        kind: kind as i32,
        tags: hit.tags.clone(),
        source: hit.source.clone(),
        r#match: hit.r#match.clone(),
    }
}

/// A remote's bundle, not installed: no resource name, its `item` the name
/// it has in the remote.
fn remote(found: &RemoteFound) -> v1::RemoteFound {
    v1::RemoteFound {
        remote: found.remote.clone(),
        item: found.name.clone(),
        kind: kind(&found.kind).0 as i32,
        tags: found.tags.clone(),
        description: found.description.clone(),
        author: found.author.clone(),
        pull_reference: found.pull_ref.clone(),
    }
}

/// A search's kind word as its `ItemKind`, and the collection its items are
/// named in.
fn kind(word: &str) -> (v1::ItemKind, &'static str) {
    match word {
        "fragment" => (v1::ItemKind::Fragment, "fragments"),
        "command" => (v1::ItemKind::Command, "commands"),
        "skill" => (v1::ItemKind::Skill, "skills"),
        "profile" => (v1::ItemKind::Profile, "profiles"),
        "mcp_server" => (v1::ItemKind::McpServer, "mcpServers"),
        "bundle" => (v1::ItemKind::Bundle, "bundles"),
        _ => (v1::ItemKind::Unspecified, ""),
    }
}

/// A bundle's resource name under `parent`, from the name the catalog gives
/// it (design §1.2, OQ-4): a project's or the built-in `<bundle>` is
/// `{parent}/bundles/<bundle>`, one installed from a remote, `<remote>/
/// <bundle>`, is `{parent}/remotes/<remote>/bundles/<bundle>`, and a
/// companion's loadout, `ctxloom+companion:<bin>`, is `{parent}/companions/
/// <bin>/bundles/loadout`. (A nested local bundle is refused, OQ-5, so a
/// `/` is a remote's.) B1's `catalog::bundle_name` names them alike; one
/// of the two goes when both land.
pub(crate) fn bundle_name(parent: &str, bundle: &str) -> String {
    if let Some(bin) = bundle.strip_prefix(COMPANION_PREFIX) {
        return format!("{parent}/companions/{bin}/bundles/loadout");
    }
    match bundle.split_once('/') {
        Some((remote, bundle)) => format!("{parent}/remotes/{remote}/bundles/{bundle}"),
        None => format!("{parent}/bundles/{bundle}"),
    }
}

/// An item's resource name under `parent`: its bundle's, then its
/// `collection` (`fragments`, `commands`, `skills`, `mcpServers`,
/// `profiles`) and its own name.
pub(crate) fn item_name(parent: &str, located: &Located, collection: &str) -> String {
    format!(
        "{}/{collection}/{}",
        bundle_name(parent, &located.bundle),
        located.item
    )
}

/// A count or token estimate as the API's `uint32` (OQ-12), saturating: no
/// list or context comes near it.
pub(crate) fn count(n: u64) -> u32 {
    u32::try_from(n).unwrap_or(u32::MAX)
}

/// How a companion's loadout's catalog name begins.
const COMPANION_PREFIX: &str = "ctxloom+companion:";

#[cfg(test)]
#[path = "search.test.rs"]
mod tests;
