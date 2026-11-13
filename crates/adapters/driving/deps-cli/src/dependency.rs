//! The `deps` noun's answers as their API messages (OD-40, B4; design
//! §5.10 and the owner's rulings on the B4 draft): the Dependency resource
//! and the custom methods pull, check and upgrade.

use daemon_proto::v1;
use deps_application::{
    Change, CheckReport, CheckedRef, Compat, DepRow, DepsListing, ExecSpec, HoldResult, PinChange,
    PullReport,
};

use crate::api::{count, dependency_name, item_kind, remote_name};
use crate::refresh::{self, Skipped};

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

/// A pin change's difference as its ChangeKind.
fn change_kind(change: Change) -> i32 {
    (match change {
        Change::Added => v1::ChangeKind::Added,
        Change::Removed => v1::ChangeKind::Removed,
        Change::Modified => v1::ChangeKind::Modified,
    }) as i32
}

/// What a move is worth; a first pin, which has nothing to compare, none.
fn compatibility(compat: Option<Compat>) -> i32 {
    use v1::pin_change::Compatibility;
    (match compat {
        None => Compatibility::Unspecified,
        Some(Compat::Breaking) => Compatibility::Breaking,
        Some(Compat::Compatible) => Compatibility::Compatible,
        Some(Compat::Unknown) => Compatibility::Unknown,
    }) as i32
}

fn exec_spec(spec: &ExecSpec) -> v1::ExecSpec {
    v1::ExecSpec {
        command: spec.command.clone(),
        args: spec.args.clone(),
        env: spec.env.clone().into_iter().collect(),
        url: spec.url.clone(),
        headers: spec.headers.clone().into_iter().collect(),
    }
}

/// One pin's move (or first pin) as `PinChange`, shared by a pull and an
/// upgrade; env and header values stay the fingerprints the view holds.
pub(crate) fn pin_change(change: &PinChange) -> v1::PinChange {
    v1::PinChange {
        reference: change.identity.clone(),
        url: change.url.clone(),
        old_commit: change.from_sha.clone(),
        new_commit: change.to_sha.clone(),
        old_version: change.from_version.clone(),
        new_version: change.to_version.clone(),
        compatibility: compatibility(change.compat),
        items: change
            .items
            .iter()
            .map(|item| v1::ItemChange {
                kind: item_kind(item.kind),
                item: item.name.clone(),
                change: change_kind(item.change),
                exec: item.exec.as_ref().map(|delta| v1::ExecDelta {
                    old_spec: delta.before.as_ref().map(exec_spec),
                    new_spec: delta.after.as_ref().map(exec_spec),
                }),
            })
            .collect(),
        files: change
            .files
            .iter()
            .map(|file| v1::FileChange {
                path: file.path.clone(),
                change: change_kind(file.change),
                diff: file.diff.clone(),
            })
            .collect(),
    }
}

/// A pull's row by what it did with the reference.
fn pull_state(status: &str) -> i32 {
    use v1::pull_result::State;
    (match status {
        "installed" => State::Installed,
        "reinstalled" => State::Reinstalled,
        "skipped" => State::Kept,
        "failed" => State::Failed,
        _ => State::Unspecified,
    }) as i32
}

/// `deps pull`: `PullDependenciesResponse`, every reference of the closure
/// one row by its state (OQ-B3), no status, message or count (OQ-B4: an
/// empty pull, which applied nothing, is `{}`), and the refresh it skipped,
/// with the command that applies it when it would write or release.
pub(crate) fn pulled(
    parent: &str,
    report: &PullReport,
    skipped: Option<&Skipped>,
) -> v1::PullDependenciesResponse {
    let row = |reference: &str, kind: &str, status: &str, error: &str, fix: &str| v1::PullResult {
        dependency: dependency_name(parent, reference),
        reference: reference.to_string(),
        kind: item_kind(kind),
        state: pull_state(status),
        error: error.to_string(),
        fix: fix.to_string(),
    };
    let results: Vec<v1::PullResult> = report
        .synced
        .iter()
        .chain(&report.skipped)
        .map(|item| row(&item.reference, item.kind, item.status, &item.error, ""))
        .chain(report.failed.iter().map(|item| {
            row(
                &item.reference,
                item.kind,
                item.status,
                &item.error,
                &item.fix,
            )
        }))
        .collect();
    let refresh = skipped.map(|skipped| refresh::message(parent, skipped));
    let applies = skipped.is_some_and(refresh::applies);
    v1::PullDependenciesResponse {
        applied: (report.status != "empty").then_some(true),
        apply_command: if applies {
            "ctxloom deps pull --yes".to_string()
        } else {
            String::new()
        },
        results,
        removed_references: report.removed.clone(),
        pruned_checkouts: report.pruned_checkouts.clone(),
        unreachable_profiles: report.unreachable.clone(),
        incomplete: report.incomplete,
        constraint_changes: report
            .constraint_changes
            .iter()
            .map(|c| v1::ConstraintChange {
                reference: c.identity.clone(),
                pinned: c.pinned.clone(),
                declared: c.declared.clone(),
                commit: c.sha.clone(),
            })
            .collect(),
        changes: report.changes.iter().map(pin_change).collect(),
        reconciliation: report.reconcile.as_ref().map(|r| v1::Reconciliation {
            gone_references: r.gone.clone(),
            unreachable_remotes: r
                .unreachable
                .iter()
                .map(|u| v1::UnreachableRemote {
                    url: u.url.clone(),
                    reason: u.reason.clone(),
                    references: u.refs.clone(),
                })
                .collect(),
        }),
        skipped_refresh: refresh,
        ..v1::PullDependenciesResponse::default()
    }
}

/// How a constraint selects its commit, from its word.
fn selector(word: &str) -> i32 {
    use v1::dependency_check::Selector;
    (match word {
        "sha" => Selector::Sha,
        "tag" => Selector::Tag,
        "version" => Selector::Version,
        "branch" => Selector::Branch,
        _ => Selector::Unspecified,
    }) as i32
}

/// Why an entry was not checked, from its word.
fn unchecked_reason(word: &str) -> i32 {
    use v1::dependency_check::UncheckedReason;
    (match word {
        "unparseable" => UncheckedReason::Unparseable,
        "unresolvable" => UncheckedReason::Unresolvable,
        "not_refreshed" => UncheckedReason::NotRefreshed,
        _ => UncheckedReason::Unspecified,
    }) as i32
}

fn refresh_failures(failures: &[deps_application::RefreshFailure]) -> Vec<v1::RefreshFailure> {
    failures
        .iter()
        .map(|f| v1::RefreshFailure {
            url: f.url.clone(),
            error: f.error.clone(),
        })
        .collect()
}

/// `deps check`: `CheckDependenciesResponse`, the outdated entries then the
/// unchecked ones (a current entry is not listed, OQ-B6).
pub(crate) fn checked(parent: &str, report: &CheckReport) -> v1::CheckDependenciesResponse {
    use v1::dependency_check::State;
    let outdated = report.updates.iter().map(|row| v1::DependencyCheck {
        dependency: dependency_name(parent, &row.reference),
        reference: row.reference.clone(),
        kind: item_kind(row.kind),
        state: State::Outdated as i32,
        commit: row.current_sha.clone(),
        latest_commit: row.latest_sha.clone(),
        selector: selector(&row.selector),
        constraint: row.requested.clone(),
        resolved_version: row.resolved.clone(),
        ..v1::DependencyCheck::default()
    });
    let unchecked = report.unchecked.iter().map(|row| v1::DependencyCheck {
        dependency: dependency_name(parent, &row.reference),
        reference: row.reference.clone(),
        kind: v1::ItemKind::Bundle as i32,
        state: State::Unchecked as i32,
        constraint: row.constraint.clone(),
        url: row.url.clone(),
        reason: unchecked_reason(row.reason),
        error: row.error.clone(),
        ..v1::DependencyCheck::default()
    });
    v1::CheckDependenciesResponse {
        checks: outdated.chain(unchecked).collect(),
        dependency_count: count(report.entries),
        skipped_count: count(report.skipped_empty),
        refresh_failures: refresh_failures(&report.refresh_failures),
        ..v1::CheckDependenciesResponse::default()
    }
}

/// `deps check <reference>`: `CheckDependenciesResponse` of the one
/// reference, NOT_PINNED (no Dependency) when the lock does not hold it,
/// else CURRENT or OUTDATED.
pub(crate) fn checked_one(parent: &str, checked: &CheckedRef) -> v1::CheckDependenciesResponse {
    use v1::dependency_check::State;
    let state = if !checked.in_lockfile {
        State::NotPinned
    } else if checked.up_to_date {
        State::Current
    } else {
        State::Outdated
    };
    v1::CheckDependenciesResponse {
        checks: vec![v1::DependencyCheck {
            dependency: if checked.in_lockfile {
                dependency_name(parent, &checked.identity)
            } else {
                String::new()
            },
            reference: checked.reference.clone(),
            kind: v1::ItemKind::Bundle as i32,
            state: state as i32,
            commit: checked.current_sha.clone(),
            latest_commit: checked.latest_sha.clone(),
            ..v1::DependencyCheck::default()
        }],
        refresh_failures: refresh_failures(&checked.refresh_failures),
        ..v1::CheckDependenciesResponse::default()
    }
}

#[cfg(test)]
#[path = "dependency.test.rs"]
mod tests;
