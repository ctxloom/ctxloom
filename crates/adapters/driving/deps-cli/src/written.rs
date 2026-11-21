//! A served write's answer read back as the view its text is drawn from
//! (step 10c U4, D10: the text is rendered from the message alone), line
//! for line as it printed the view the use case answered in process: a
//! pull, an upgrade, and the remotes' writes.

use daemon_proto::v1;
use deps_application::{
    Change, Compat, ConstraintChangeView, ExecDelta, ExecSpec, FailedItem, FileChange, ItemChange,
    PinChange, PullReport, ReconcileView, SyncItem, UncheckedRemote, UpgradeReport,
};

/// An item's kind as its view's word (`mcp`, as the pin disclosure says
/// it).
fn kind_word(kind: i32) -> &'static str {
    match v1::ItemKind::try_from(kind) {
        Ok(v1::ItemKind::Bundle) => "bundle",
        Ok(v1::ItemKind::Fragment) => "fragment",
        Ok(v1::ItemKind::Command) => "command",
        Ok(v1::ItemKind::Skill) => "skill",
        Ok(v1::ItemKind::McpServer) => "mcp",
        Ok(v1::ItemKind::Profile) => "profile",
        Ok(v1::ItemKind::Hook) => "hook",
        _ => "",
    }
}

fn change_of(change: i32) -> Change {
    match v1::ChangeKind::try_from(change) {
        Ok(v1::ChangeKind::Removed) => Change::Removed,
        Ok(v1::ChangeKind::Modified) => Change::Modified,
        _ => Change::Added,
    }
}

fn compat_of(compatibility: i32) -> Option<Compat> {
    use v1::pin_change::Compatibility;
    match Compatibility::try_from(compatibility) {
        Ok(Compatibility::Breaking) => Some(Compat::Breaking),
        Ok(Compatibility::Compatible) => Some(Compat::Compatible),
        Ok(Compatibility::Unknown) => Some(Compat::Unknown),
        _ => None,
    }
}

fn exec_of(spec: &v1::ExecSpec) -> ExecSpec {
    ExecSpec {
        command: spec.command.clone(),
        args: spec.args.clone(),
        env: spec.env.clone().into_iter().collect(),
        url: spec.url.clone(),
        headers: spec.headers.clone().into_iter().collect(),
    }
}

/// A pin's move (or first pin) as its view.
pub(crate) fn pin_change(change: &v1::PinChange) -> PinChange {
    PinChange {
        identity: change.reference.clone(),
        url: change.url.clone(),
        from_sha: change.old_commit.clone(),
        to_sha: change.new_commit.clone(),
        from_version: change.old_version.clone(),
        to_version: change.new_version.clone(),
        compat: compat_of(change.compatibility),
        items: change
            .items
            .iter()
            .map(|item| ItemChange {
                kind: kind_word(item.kind),
                name: item.item.clone(),
                change: change_of(item.change),
                exec: item.exec.as_ref().map(|delta| ExecDelta {
                    before: delta.old_spec.as_ref().map(exec_of),
                    after: delta.new_spec.as_ref().map(exec_of),
                }),
            })
            .collect(),
        files: change
            .files
            .iter()
            .map(|file| FileChange {
                path: file.path.clone(),
                change: change_of(file.change),
                diff: file.diff.clone(),
            })
            .collect(),
    }
}

/// A pull's answer as its report: each row by its state, the counts read
/// from the rows.
pub(crate) fn pulled(answered: &v1::PullDependenciesResponse) -> PullReport {
    use v1::pull_result::State;
    let mut synced = Vec::new();
    let mut skipped = Vec::new();
    let mut failed = Vec::new();
    let (mut installed, mut reinstalled) = (0, 0);
    for row in &answered.results {
        let kind = match kind_word(row.kind) {
            "" => "bundle",
            word => word,
        };
        let item = |status: &'static str| SyncItem {
            reference: row.reference.clone(),
            kind,
            status,
            error: String::new(),
        };
        match State::try_from(row.state) {
            Ok(State::Installed) => {
                installed += 1;
                synced.push(item("installed"));
            }
            Ok(State::Reinstalled) => {
                reinstalled += 1;
                synced.push(item("reinstalled"));
            }
            Ok(State::Failed) => failed.push(FailedItem {
                reference: row.reference.clone(),
                kind,
                status: "failed",
                error: row.error.clone(),
                fix: row.fix.clone(),
            }),
            _ => skipped.push(item("skipped")),
        }
    }
    let total = answered.results.len() as u64;
    let errors = failed.len() as u64;
    PullReport {
        status: match (total, errors) {
            (0, _) => "empty",
            (_, 0) => "completed",
            _ => "completed_with_errors",
        },
        total,
        installed,
        reinstalled,
        errors,
        message: String::new(),
        synced,
        skipped,
        failed,
        removed: answered.removed_references.clone(),
        pruned_checkouts: answered.pruned_checkouts.clone(),
        incomplete: answered.incomplete,
        unreachable: answered.unreachable_profiles.clone(),
        constraint_changes: answered
            .constraint_changes
            .iter()
            .map(|change| ConstraintChangeView {
                identity: change.reference.clone(),
                pinned: change.pinned.clone(),
                declared: change.declared.clone(),
                sha: change.commit.clone(),
            })
            .collect(),
        changes: answered.changes.iter().map(pin_change).collect(),
        reconcile: answered.reconciliation.as_ref().map(|r| ReconcileView {
            gone: r.gone_references.clone(),
            unreachable: r
                .unreachable_remotes
                .iter()
                .map(|u| UncheckedRemote {
                    url: u.url.clone(),
                    reason: u.reason.clone(),
                    refs: u.references.clone(),
                })
                .collect(),
        }),
    }
}

/// An upgrade's answer as its report.
pub(crate) fn upgraded(answered: &v1::UpgradeDependenciesResponse) -> UpgradeReport {
    UpgradeReport {
        applied: answered.applied == Some(true),
        changes: answered.changes.iter().map(pin_change).collect(),
        incomplete: answered.incomplete,
        nothing_declared: answered.nothing_declared,
        removed: answered.removed_references.clone(),
    }
}

/// A remote's edit as its view: `asked`, the remote it edited, as the
/// listing showed it `before` (none, its after: a remote not listed), and
/// after as the answer names it.
pub(crate) fn edited(
    asked: &str,
    before: Option<deps_application::RemoteState>,
    answered: &v1::UpdateRemoteResponse,
) -> deps_application::EditedRemote {
    let remote = answered.remote.clone().unwrap_or_default();
    let after = deps_application::RemoteState {
        name: remote_id(&remote.name).to_string(),
        url: remote.url.clone(),
        forge: remote.forge.clone(),
    };
    let before = before.unwrap_or_else(|| after.clone());
    deps_application::EditedRemote {
        status: if answered.changes.is_empty() {
            "unchanged"
        } else {
            "edited"
        },
        // The remote as it is named now (a rename's new name).
        name: if after.name.is_empty() {
            asked.to_string()
        } else {
            after.name.clone()
        },
        before,
        after,
        changed: answered.changes.clone(),
        default_pointer_updated: remote.default == Some(true),
    }
}

/// A remote's removal as its view: the preview (what would go, and the
/// command that applies it) or the removal.
pub(crate) fn removed(
    asked: &str,
    answered: &v1::DeleteRemoteResponse,
) -> deps_application::RemoveResult {
    if answered.applied == Some(true) {
        let name = answered
            .remote
            .as_ref()
            .map_or(asked, |remote| remote_id(&remote.name));
        return deps_application::RemoveResult::Removed {
            status: "removed",
            name: name.to_string(),
        };
    }
    deps_application::RemoveResult::Preview {
        status: "preview",
        applied: false,
        target: answered.detail.first().cloned().unwrap_or_default(),
        apply: answered.apply_command.clone(),
    }
}

/// The last segment of a resource name after `/remotes/`, or the name.
pub(crate) fn remote_id(name: &str) -> &str {
    name.rsplit_once("/remotes/").map_or(name, |(_, id)| id)
}

#[cfg(test)]
#[path = "written.test.rs"]
mod tests;
