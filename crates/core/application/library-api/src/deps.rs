//! The remotes' and the dependencies' reads as the daemon answers them
//! (step 10a U2): `remote list`, `remote show` and `deps list`, each a use case of the
//! project's dependencies opened for the read, its view mapped to the API's
//! message under the parent it was asked under. As [`crate::reads`], each
//! is `fn(deps, parent, request)`, listed once in [`deps_served!`], which
//! every layer generates its part from (D12).

use daemon_proto::v1;
use deps_application::{Browsed, DepRow, Deps, DepsListing, Outcome, RemoteListing};

/// The deps reads the daemon serves (step 10a U2, D12), in the shape of
/// `daemon_proto::library_reads!`'s rows, handed to `$each`: each row's
/// `deps::$method` answers it.
#[macro_export]
macro_rules! deps_served {
    ($each:ident) => {
        $each! {
            list_remotes (ListRemotes) ListRemotesRequest.parent => ListRemotesResponse,
            browse_remote (BrowseRemote) BrowseRemoteRequest.name => BrowseRemoteResponse,
            list_dependencies (ListDependencies) ListDependenciesRequest.parent => ListDependenciesResponse,
        }
    };
}

/// A Remote's resource name: `<parent>/remotes/<remote>`.
pub fn remote_name(parent: &str, remote: &str) -> String {
    format!("{parent}/remotes/{remote}")
}

/// A Dependency's resource name, from its lock identity (OQ-15).
pub fn dependency_name(parent: &str, identity: &str) -> String {
    format!(
        "{parent}/dependencies/{}",
        deps_application::dependency_id(identity)
    )
}

/// `outcome`'s answer as `f` maps it, its notices kept.
fn mapped<T, U>(outcome: Outcome<T>, f: impl FnOnce(T) -> U) -> Outcome<U> {
    Outcome {
        result: outcome.result.map(f),
        notices: outcome.notices,
    }
}

/// `remote list`: `ListRemotesResponse`, each remote saying whether it is
/// the default (OQ-7: printed true or false).
pub fn remotes(parent: &str, listing: &RemoteListing) -> v1::ListRemotesResponse {
    v1::ListRemotesResponse {
        remotes: listing
            .remotes
            .iter()
            .map(|row| v1::Remote {
                name: remote_name(parent, &row.name),
                url: row.url.clone(),
                forge: row.forge.clone(),
                default: Some(row.name == listing.default),
                ..Default::default()
            })
            .collect(),
        ..v1::ListRemotesResponse::default()
    }
}

/// One lock entry as its Dependency: named by its identity (OQ-15), its
/// bundle under the remote it came from, or its text where no remote
/// matches (OQ-16).
pub fn dependency(parent: &str, row: &DepRow) -> v1::Dependency {
    let (bundle, remote) = if row.origin.is_empty() {
        (row.name.clone(), String::new())
    } else {
        let remote = remote_name(parent, &row.origin);
        (format!("{remote}/bundles/{}", row.name), remote)
    };
    v1::Dependency {
        name: dependency_name(parent, &row.reference),
        reference: row.reference.clone(),
        bundle,
        commit: row.sha.clone(),
        constraint: row.constraint.clone(),
        held: Some(row.held),
        remote,
        url: row.url.clone(),
    }
}

/// `deps list`: `ListDependenciesResponse`.
pub fn dependencies(parent: &str, listing: &DepsListing) -> v1::ListDependenciesResponse {
    v1::ListDependenciesResponse {
        dependencies: listing
            .deps
            .iter()
            .map(|row| dependency(parent, row))
            .collect(),
        ..v1::ListDependenciesResponse::default()
    }
}

/// `ListRemotes`: the project's registered remotes, the default marked.
pub fn list_remotes(
    deps: &Deps,
    parent: &str,
    _: &v1::ListRemotesRequest,
) -> Outcome<v1::ListRemotesResponse> {
    mapped(deps.remote_list(), |listing| remotes(parent, &listing))
}

/// `remote show`: `BrowseRemoteResponse`; a browsed bundle is not
/// installed, so it has no resource name (OQ-B2), only its ID and its pull
/// reference.
pub fn browsed(parent: &str, browsed: &Browsed) -> v1::BrowseRemoteResponse {
    v1::BrowseRemoteResponse {
        remote: remote_name(parent, &browsed.remote),
        url: browsed.url.clone(),
        items: browsed
            .items
            .iter()
            .map(|item| v1::BrowsedItem {
                item: item.name.clone(),
                kind: crate::resources::item_kind(item.kind).0 as i32,
                path: item.path.clone(),
                pull_reference: item.pull_ref.clone(),
            })
            .collect(),
        ..v1::BrowseRemoteResponse::default()
    }
}

/// `BrowseRemote`: what the remote the request names publishes, read from
/// its cached clone as `remote show` reads it (step 10a U2, owner ruling
/// 2026-11-17). The remote's ID is read back from its name exactly; a name
/// of no remote's shape is its text, so the use case refuses it in its
/// own words.
pub fn browse_remote(
    deps: &Deps,
    parent: &str,
    request: &v1::BrowseRemoteRequest,
) -> Outcome<v1::BrowseRemoteResponse> {
    let remote = crate::names::named_under(parent, "remotes", &request.name)
        .unwrap_or_else(|| request.name.clone());
    mapped(deps.remote_show(&remote), |shown| browsed(parent, &shown))
}

/// `ListDependencies`: the lock's entries.
pub fn list_dependencies(
    deps: &Deps,
    parent: &str,
    _: &v1::ListDependenciesRequest,
) -> Outcome<v1::ListDependenciesResponse> {
    mapped(deps.list(), |listing| dependencies(parent, &listing))
}

#[cfg(test)]
#[path = "deps.test.rs"]
mod tests;
