//! The `remote` noun's answers as their API messages (OD-40, B4; design
//! §5.10): the registry's resources and its two custom methods, browse and
//! discover.

use daemon_proto::v1;
use deps_application::{
    Browsed, Created, DefaultSet, Discovered, EditedRemote, RemoteListing, RemoveResult,
};

use crate::api::{count, item_kind, remote_name};

/// `remote list`: `ListRemotesResponse`, each remote saying whether it is
/// the default (OQ-7: printed true or false).
pub(crate) fn listed(parent: &str, listing: &RemoteListing) -> v1::ListRemotesResponse {
    v1::ListRemotesResponse {
        remotes: listing
            .remotes
            .iter()
            .map(|row| v1::Remote {
                name: remote_name(parent, &row.name),
                url: row.url.clone(),
                default: Some(row.name == listing.default),
                ..Default::default()
            })
            .collect(),
        ..v1::ListRemotesResponse::default()
    }
}

/// `remote create`: `CreateRemoteResponse`, the remote with the forge it was
/// bound to (`forge`, as asked); a clone that failed is a notice alone.
pub(crate) fn created(parent: &str, made: &Created, forge: &str) -> v1::CreateRemoteResponse {
    v1::CreateRemoteResponse {
        remote: Some(v1::Remote {
            name: remote_name(parent, &made.name),
            url: made.url.clone(),
            forge: forge.to_string(),
            ..Default::default()
        }),
        applied: Some(true),
        ..v1::CreateRemoteResponse::default()
    }
}

/// `remote edit`: `UpdateRemoteResponse`, the remote after the write and its
/// `changes` (OQ-18: no `before`); an edit that changed nothing applied
/// nothing. A default that followed a rename is the remote's `default`.
pub(crate) fn edited(parent: &str, edited: &EditedRemote) -> v1::UpdateRemoteResponse {
    let after = &edited.after;
    v1::UpdateRemoteResponse {
        remote: Some(v1::Remote {
            name: remote_name(parent, &after.name),
            url: after.url.clone(),
            forge: after.forge.clone(),
            default: edited.default_pointer_updated.then_some(true),
            ..Default::default()
        }),
        applied: Some(!edited.changed.is_empty()),
        changes: edited.changed.clone(),
        ..v1::UpdateRemoteResponse::default()
    }
}

/// `remote remove`: `DeleteRemoteResponse`, the preview's (what would go,
/// and the command that applies it) or the removal's.
pub(crate) fn removed(
    parent: &str,
    asked: &str,
    removal: &RemoveResult,
) -> v1::DeleteRemoteResponse {
    let named = |remote: &str| {
        Some(v1::Remote {
            name: remote_name(parent, remote),
            ..Default::default()
        })
    };
    match removal {
        RemoveResult::Preview { target, apply, .. } => v1::DeleteRemoteResponse {
            remote: named(asked),
            applied: Some(false),
            detail: vec![target.clone()],
            apply_command: apply.clone(),
            ..v1::DeleteRemoteResponse::default()
        },
        RemoveResult::Removed { name, .. } => v1::DeleteRemoteResponse {
            remote: named(name),
            applied: Some(true),
            ..v1::DeleteRemoteResponse::default()
        },
    }
}

/// `remote default`: `SetDefaultRemoteResponse`, the remote now the default
/// (none, cleared).
pub(crate) fn defaulted(parent: &str, set: &DefaultSet) -> v1::SetDefaultRemoteResponse {
    v1::SetDefaultRemoteResponse {
        remote: if set.name.is_empty() {
            String::new()
        } else {
            remote_name(parent, &set.name)
        },
        applied: Some(true),
        ..v1::SetDefaultRemoteResponse::default()
    }
}

/// `remote show`: `BrowseRemoteResponse`; a browsed bundle is not installed,
/// so it has no resource name (OQ-B2), only its ID and its pull reference.
pub(crate) fn browsed(parent: &str, browsed: &Browsed) -> v1::BrowseRemoteResponse {
    v1::BrowseRemoteResponse {
        remote: remote_name(parent, &browsed.remote),
        url: browsed.url.clone(),
        items: browsed
            .items
            .iter()
            .map(|item| v1::BrowsedItem {
                item: item.name.clone(),
                kind: item_kind(item.kind),
                path: item.path.clone(),
                pull_reference: item.pull_ref.clone(),
            })
            .collect(),
        ..v1::BrowseRemoteResponse::default()
    }
}

/// `remote discover`: `DiscoverRemotesResponse`; a repository found is not
/// registered, so it has no resource name.
pub(crate) fn discovered(found: &Discovered) -> v1::DiscoverRemotesResponse {
    v1::DiscoverRemotesResponse {
        repositories: found
            .repositories
            .iter()
            .map(|repo| v1::DiscoveredRepository {
                owner: repo.owner.clone(),
                repository: repo.name.clone(),
                description: repo.description.clone(),
                star_count: count(repo.stars),
                url: repo.url.clone(),
                forge: repo.forge.to_string(),
                add_command: repo.add_command.clone(),
            })
            .collect(),
        ..v1::DiscoverRemotesResponse::default()
    }
}
