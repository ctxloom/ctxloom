//! `bundle push` and `bundle move`'s answers as their API messages (OD-40,
//! B4; the owner's rulings on the B4 draft).

use daemon_proto::v1;
use deps_application::{Moved, Pushed};

use crate::api::{bundle_name, remote_name};

/// What a push did: a pull request opened (`pr-created`), else a commit.
fn push_state(status: &str) -> v1::push_bundle_response::State {
    if status == "pr-created" {
        v1::push_bundle_response::State::PullRequestOpened
    } else {
        v1::push_bundle_response::State::Committed
    }
}

/// `bundle push`: `PushBundleResponse`, the local bundle `bundle` and the
/// remote by their names, the outcome a state.
pub(crate) fn pushed(parent: &str, bundle: &str, pushed: &Pushed) -> v1::PushBundleResponse {
    v1::PushBundleResponse {
        bundle: bundle_name(parent, bundle),
        path: pushed.path.clone(),
        applied: Some(true),
        remote: remote_name(parent, &pushed.remote),
        target_path: pushed.target_path.clone(),
        title: pushed.title.clone(),
        state: push_state(pushed.status) as i32,
        size_bytes: pushed.size_bytes,
        commit: pushed.commit_sha.clone(),
        pull_request_url: pushed.pr_url.clone(),
        ..v1::PushBundleResponse::default()
    }
}

/// `bundle move`: `MoveBundleResponse`. To a remote, the bundle under its
/// new name there, with the commit it landed in, and its path in the
/// remote's repository; to a directory (another project, whose name this
/// project cannot give), the path it landed at alone.
pub(crate) fn moved(parent: &str, moved: &Moved) -> v1::MoveBundleResponse {
    let bundle = (moved.dest_kind == "remote").then(|| v1::Bundle {
        name: format!(
            "{}/bundles/{}",
            remote_name(parent, &moved.remote),
            moved.name
        ),
        origin: v1::bundle::Origin::Remote as i32,
        commit: moved.commit_sha.clone(),
        ..v1::Bundle::default()
    });
    v1::MoveBundleResponse {
        bundle,
        path: moved.dest.clone(),
        applied: Some(true),
        ..v1::MoveBundleResponse::default()
    }
}
