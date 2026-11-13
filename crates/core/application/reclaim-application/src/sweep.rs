//! `ctxloom session sweep`: every session of a project, turned into a row by
//! a fixed table, in order:
//!
//! | the session                                   | the row                     |
//! |-----------------------------------------------|-----------------------------|
//! | running                                       | skipped whole               |
//! | liveness unprovable                           | skipped whole; the manual route is named |
//! | marked keep                                   | held; `session keep --undo` is named |
//! | a scratch worktree must be preserved          | spared from purge, naming it |
//! | no purge age                                  | held (purges have no default) |
//! | last active after the purge age               | left                        |
//! | its transcript within the retention           | held, saying why            |
//! | otherwise                                     | purged (planned without `yes`) |
//!
//! Before its row, each session's scratch worktrees are rows of their own
//! (`worktree`), asked of the worktree leaf with the sweep's own apply
//! decision: reaped (done), reapable (planned), spared in place (held) or
//! skipped, each saying why.
//!
//! Nothing is decided by a model: the same sessions give the same report.
//! The purge acts through `session remove`'s own order ([`crate::remove`],
//! a destroy, owner ruling 98), so every refusal it makes is the sweep's.
//! The sessions are the caller's (the daemon's warm index, slice 2).

use std::collections::BTreeMap;

use isolation_application::{IsolationError, Row, Verdict as Worktree};
use serde::Serialize;
use session_index::{stamp, View};
use sessions_application::{Refusal, Removal, RemoveRequest, State, Timestamp};

use crate::{ReclaimError, Removals};

/// What a sweep is asked.
#[derive(Clone, Debug, Default)]
pub struct SweepRequest {
    /// Sessions last active before this are purged; none holds every purge.
    pub purge_cutoff: Option<Timestamp>,
    /// Act on the plan (`--yes`); without it only report.
    pub yes: bool,
}

/// What became of a session.
#[derive(Clone, Copy, PartialEq, Eq, Debug, Serialize)]
#[serde(rename_all = "lowercase")]
pub enum Verdict {
    Done,
    Planned,
    Held,
    Left,
    Skipped,
    Failed,
}

impl Verdict {
    fn name(self) -> &'static str {
        match self {
            Verdict::Done => "done",
            Verdict::Planned => "planned",
            Verdict::Held => "held",
            Verdict::Left => "left",
            Verdict::Skipped => "skipped",
            Verdict::Failed => "failed",
        }
    }
}

/// One session's row.
#[derive(Clone, PartialEq, Debug, Serialize)]
pub struct SweepRow {
    pub harp: String,
    pub action: &'static str,
    pub verdict: Verdict,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub reason: String,
    /// The command that does what the row could not, or will.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub command: String,
    #[serde(skip_serializing_if = "is_zero")]
    pub bytes: u64,
    /// The files a person wrote that the row's destroy takes (planned or
    /// done), each relative to the session's directory (owner ruling 104).
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub authored: Vec<String>,
    /// The removal a purge row planned or applied, its file plan whole.
    #[serde(skip)]
    pub removal: Option<Removal>,
}

pub(crate) fn is_zero(n: &u64) -> bool {
    *n == 0
}

/// The sweep's answer.
#[derive(Clone, PartialEq, Debug, Serialize)]
pub struct SweepReport {
    /// When purging begins, as a date; none when purges are held.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub purge_cutoff: Option<String>,
    pub rows: Vec<SweepRow>,
    pub counts: BTreeMap<&'static str, u64>,
}

fn row(
    harp: &str,
    action: &'static str,
    verdict: Verdict,
    reason: &str,
    command: String,
) -> SweepRow {
    SweepRow {
        harp: harp.to_string(),
        action,
        verdict,
        reason: reason.to_string(),
        command,
        bytes: 0,
        authored: Vec::new(),
        removal: None,
    }
}

/// Sweep `views`, a project's sessions (or every project's), each in its
/// state.
pub async fn sweep<S: Removals + Send>(
    service: &mut S,
    views: &[View],
    request: &SweepRequest,
) -> Result<SweepReport, ReclaimError> {
    let mut rows = Vec::new();
    for view in views {
        let harp = view.record.harp.as_str();
        let project = Some(view.record.project_id.as_str()).filter(|id| !id.is_empty());
        let checkouts = worktree_rows(service, project, harp, request.yes)?;
        let preserved = preserved_worktree(&checkouts).map(str::to_string);
        rows.extend(checkouts.into_iter().map(|c| c.row));
        let purge = format!("ctxloom session remove {harp} --yes");
        let decided = match view.state {
            State::Live => Some(row(
                harp,
                "skip",
                Verdict::Skipped,
                "it is running",
                String::new(),
            )),
            State::Unknown => Some(row(
                harp,
                "skip",
                Verdict::Skipped,
                "its liveness cannot be proven",
                format!("ctxloom session remove {harp} --even-if-live"),
            )),
            State::Ended if view.record.kept => Some(row(
                harp,
                "keep",
                Verdict::Held,
                "it is marked keep: its transcript is kept at any age",
                format!("ctxloom session keep {harp} --undo"),
            )),
            State::Ended => None,
        };
        if let Some(decided) = decided {
            rows.push(decided);
            continue;
        }
        // Go's DecideSweep: a checkout that must be preserved spares its
        // session from purge too, whatever the purge age says.
        if let Some(worktree) = preserved {
            rows.push(row(
                harp,
                "spare",
                Verdict::Left,
                &format!(
                    "its scratch worktree {worktree} must be preserved, so the session is spared from reclaim and purge and the work stays where it is"
                ),
                String::new(),
            ));
            continue;
        }
        let Some(cutoff) = request.purge_cutoff else {
            rows.push(row(
                harp,
                "purge",
                Verdict::Held,
                "no --purge-older-than or session_purge_age is set, so nothing is purged",
                String::new(),
            ));
            continue;
        };
        let active = view.end().unwrap_or(view.record.started_at);
        if (active.seconds, active.nanos) > (cutoff.seconds, cutoff.nanos) {
            rows.push(row(
                harp,
                "purge",
                Verdict::Left,
                "it was active after the purge age",
                String::new(),
            ));
            continue;
        }
        rows.push(purge_row(service, project, harp, &purge, request.yes).await);
    }
    let counts = tally(&rows);
    Ok(SweepReport {
        purge_cutoff: request
            .purge_cutoff
            .map(|at| stamp::rfc3339(at)[..10].to_string()),
        rows,
        counts,
    })
}

/// How many rows of each verdict.
fn tally(rows: &[SweepRow]) -> BTreeMap<&'static str, u64> {
    let mut counts: BTreeMap<&'static str, u64> = [
        Verdict::Done,
        Verdict::Planned,
        Verdict::Held,
        Verdict::Left,
        Verdict::Skipped,
        Verdict::Failed,
    ]
    .iter()
    .map(|v| (v.name(), 0))
    .collect();
    for r in rows {
        *counts.entry(r.verdict.name()).or_default() += 1;
    }
    counts
}

/// One checkout's row, and whether it must be preserved.
struct Checkout {
    row: SweepRow,
    preserved: Option<String>,
}

/// The first checkout that must be preserved (spared in place, or skipped:
/// not the sweep's to remove), by name.
fn preserved_worktree(checkouts: &[Checkout]) -> Option<&str> {
    checkouts.iter().find_map(|c| c.preserved.as_deref())
}

/// A session's scratch checkouts, through the worktree leaf and with the
/// sweep's own apply decision: reaped, planned, spared in place, or skipped,
/// each with its reason. A session with no directory has none.
fn worktree_rows<S: Removals>(
    service: &mut S,
    project: Option<&str>,
    harp: &str,
    apply: bool,
) -> Result<Vec<Checkout>, ReclaimError> {
    let report = match service.worktrees(project, harp, apply) {
        Ok(report) => report,
        Err(IsolationError::NoSession { .. }) => return Ok(Vec::new()),
        Err(error) => return Err(ReclaimError::Isolation(error)),
    };
    Ok(report
        .worktrees
        .iter()
        .map(|checkout| Checkout {
            row: worktree_row(harp, checkout),
            preserved: matches!(checkout.verdict, Worktree::Spared | Worktree::Skipped)
                .then(|| checkout.worktree.clone()),
        })
        .collect())
}

fn worktree_row(harp: &str, checkout: &Row) -> SweepRow {
    let (verdict, command) = match checkout.verdict {
        Worktree::Reaped => (Verdict::Done, String::new()),
        Worktree::Reapable => (
            Verdict::Planned,
            // The one verb that takes a session's worktrees (`session worktrees
            // purge` is retired): whole or not at all.
            format!("ctxloom session remove {harp} --yes"),
        ),
        Worktree::Spared => (Verdict::Held, String::new()),
        Worktree::Skipped => (Verdict::Skipped, String::new()),
    };
    let reason = match (checkout.verdict, checkout.reason.is_empty()) {
        // The worktree leaf's own word for it: left in place, uncommitted
        // work and all.
        (Worktree::Spared, _) => {
            format!("{} spared in place: {}", checkout.worktree, checkout.reason)
        }
        (_, true) => checkout.worktree.clone(),
        (_, false) => format!("{}: {}", checkout.worktree, checkout.reason),
    };
    row(harp, "worktree", verdict, &reason, command)
}

/// The purge of one session, planned and, on `yes`, applied, through
/// `session remove`'s own order ([`crate::remove`]).
async fn purge_row<S: Removals + Send>(
    service: &mut S,
    project: Option<&str>,
    harp: &str,
    command: &str,
    yes: bool,
) -> SweepRow {
    let ask = |yes| RemoveRequest {
        project: project.map(str::to_string),
        harp: harp.to_string(),
        yes,
        even_if_live: false,
    };
    // Its words alone are held across the next wait: an isolation error is
    // not `Send`.
    let (removal, kept) = match crate::remove(service, ask(false)).await {
        Ok(planned) => (planned.removal, planned.kept.map(|kept| kept.to_string())),
        Err(error) => return failed(harp, &error),
    };
    if let Some(refusal) = &removal.refusal {
        let (verdict, reason, command) = match refusal {
            Refusal::Retained { detail, .. } => (
                Verdict::Held,
                format!("its transcript is within the retention period ({detail})"),
                String::new(),
            ),
            Refusal::Kept { .. } => (
                Verdict::Held,
                "it is marked keep: its transcript is kept at any age".to_string(),
                format!("ctxloom session keep {harp} --undo"),
            ),
            Refusal::NotProvenDead { reason, .. } => (
                Verdict::Skipped,
                reason.clone(),
                format!("ctxloom session remove {harp} --even-if-live"),
            ),
        };
        return row(harp, "purge", verdict, &reason, command);
    }
    if let Some(kept) = kept {
        return row(harp, "purge", Verdict::Held, &kept, String::new());
    }
    // A session whose directory holds no file is purged too: its record is
    // its stream (ruling 87a), which the destroy takes.
    let bytes: u64 = removal.files.destroy.iter().map(|i| i.bytes).sum();
    if !yes {
        let mut r = row(harp, "purge", Verdict::Planned, "", command.to_string());
        r.bytes = bytes;
        r.authored = removal.authored();
        r.removal = Some(removal);
        return r;
    }
    match crate::remove(service, ask(true)).await {
        Ok(done) if done.removal.applied => {
            let mut r = row(harp, "purge", Verdict::Done, "", String::new());
            r.bytes = done.removal.files.bytes_freed;
            r.authored = done.removal.authored();
            r.removal = Some(done.removal);
            r
        }
        // Something changed between the plan and the apply: what it says.
        Ok(done) => match (&done.kept, &done.removal.refusal) {
            (Some(kept), _) => row(
                harp,
                "purge",
                Verdict::Held,
                &kept.to_string(),
                String::new(),
            ),
            (None, Some(refusal)) => row(
                harp,
                "purge",
                Verdict::Held,
                &refusal.to_string(),
                String::new(),
            ),
            (None, None) => row(
                harp,
                "purge",
                Verdict::Left,
                "nothing to purge",
                String::new(),
            ),
        },
        Err(error) => failed(harp, &error),
    }
}

fn failed(harp: &str, error: &ReclaimError) -> SweepRow {
    row(
        harp,
        "purge",
        Verdict::Failed,
        &error.to_string(),
        String::new(),
    )
}
