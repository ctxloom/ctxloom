//! A served read's message read back as the view its text is drawn from
//! (step 10a U2, D10: the text is rendered from the message alone), line
//! for line as it printed the view the use case answered in process.

use daemon_proto::v1;
use deps_application::{BrowseItem, Browsed, DepRow, DepsListing, RemoteListing, RemoteRow};

/// The last segment of a resource name after `/<collection>/`, or the name.
fn id_in<'a>(name: &'a str, collection: &str) -> &'a str {
    name.rsplit_once(&format!("/{collection}/"))
        .map_or(name, |(_, id)| id)
}

/// A `remote list` as its view: each remote by its ID, the default named.
pub(crate) fn remotes(listed: &v1::ListRemotesResponse) -> RemoteListing {
    let remotes: Vec<RemoteRow> = listed
        .remotes
        .iter()
        .map(|remote| RemoteRow {
            name: id_in(&remote.name, "remotes").to_string(),
            url: remote.url.clone(),
            forge: remote.forge.clone(),
        })
        .collect();
    let default = listed
        .remotes
        .iter()
        .find(|remote| remote.default == Some(true))
        .map(|remote| id_in(&remote.name, "remotes").to_string())
        .unwrap_or_default();
    RemoteListing {
        count: remotes.len() as u64,
        remotes,
        default,
    }
}

/// A `remote show` as its view: the remote by its ID, each bundle it
/// publishes by its ID, path and pull reference.
pub(crate) fn browsed(browsed: &v1::BrowseRemoteResponse) -> Browsed {
    let items: Vec<BrowseItem> = browsed
        .items
        .iter()
        .map(|item| BrowseItem {
            name: item.item.clone(),
            kind: match library_api::resources::kind_word(item.kind) {
                "" => "bundle",
                word => word,
            },
            path: item.path.clone(),
            is_dir: true,
            pull_ref: item.pull_reference.clone(),
        })
        .collect();
    Browsed {
        remote: id_in(&browsed.remote, "remotes").to_string(),
        url: browsed.url.clone(),
        count: items.len() as u64,
        items,
        warnings: Vec::new(),
    }
}

/// A `deps list` as its view: each dependency by its bundle's ID and the
/// remote it came from, or its bundle's text where no remote matches.
pub(crate) fn dependencies(listed: &v1::ListDependenciesResponse) -> DepsListing {
    let deps: Vec<DepRow> = listed
        .dependencies
        .iter()
        .map(|dep| {
            let (name, origin) = if dep.remote.is_empty() {
                (dep.bundle.clone(), String::new())
            } else {
                (
                    id_in(&dep.bundle, "bundles").to_string(),
                    id_in(&dep.remote, "remotes").to_string(),
                )
            };
            DepRow {
                name,
                reference: dep.reference.clone(),
                sha: dep.commit.clone(),
                constraint: dep.constraint.clone(),
                held: dep.held == Some(true),
                origin,
                url: dep.url.clone(),
            }
        })
        .collect();
    DepsListing {
        count: deps.len() as u64,
        deps,
    }
}

#[cfg(test)]
#[path = "read_back.test.rs"]
mod tests;
