//! The `deps` noun's answers as their API messages (OD-40, B4; design
//! §5.10 and the owner's rulings on the B4 draft): the Dependency resource
//! and the custom methods pull, check and upgrade.

use daemon_proto::v1;
use deps_application::{DepRow, DepsListing, HoldResult};

use crate::api::{dependency_name, remote_name};

/// One lock entry as its Dependency: named by its identity (OQ-15), its
/// bundle under the remote it came from, or its text where no remote
/// matches (OQ-16).
pub(crate) fn dependency(parent: &str, row: &DepRow) -> v1::Dependency {
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
pub(crate) fn listed(parent: &str, listing: &DepsListing) -> v1::ListDependenciesResponse {
    v1::ListDependenciesResponse {
        dependencies: listing
            .deps
            .iter()
            .map(|row| dependency(parent, row))
            .collect(),
        ..v1::ListDependenciesResponse::default()
    }
}

/// `deps hold` and `deps unhold`: the Dependency after the write (AIP-136),
/// read from the listing that followed it; a bundle of this project is no
/// dependency, so nothing (its notice says why).
pub(crate) fn held(parent: &str, held: &HoldResult, listing: &DepsListing) -> v1::Dependency {
    listing
        .deps
        .iter()
        .find(|row| !held.identity.is_empty() && row.reference == held.identity)
        .map(|row| dependency(parent, row))
        .unwrap_or_default()
}
