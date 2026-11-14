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
//! (`worktree`), classified by the worktree leaf first, changing nothing:
//! reapable, spared in place (held) or skipped, each saying why. Checkouts
//! go ONLY as part of destroying their session (owner rulings 2026-11-13
//! and 2026-11-14, as `session remove <harp>`): a session the sweep keeps,
//! for ANY reason (skipped, marked keep, spared by a checkout, no purge
//! age, active since it, within its retention, or its purge refused or
//! failed), keeps every checkout, its clean ones held, saying why the
//! session was kept. A session it purges has them planned, and on `yes`
//! removed by the purge itself, through `session remove`'s order (done).
//!
//! Nothing is decided by a model: the same sessions give the same report.
//! The purge acts through `session remove`'s own order ([`crate::remove`],
//! a destroy, owner ruling 98), so every refusal it makes is the sweep's.
//! The sessions are the caller's (the daemon's warm index, slice 2).

use std::collections::BTreeMap;

use isolation_application::{IsolationError, Report, Row, Verdict as Worktree};
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
        // Classified first, changing nothing. Checkouts go ONLY as part of
        // destroying their session (owner rulings 2026-11-13 and
        // 2026-11-14, `session remove`'s whole or not at all): the purge
        // removes them through `session remove`'s own order, and a session
        // the sweep keeps, for any reason, keeps every one.
        let checkouts = worktree_rows(service, project, harp)?;
        let preserved = preserved_worktree(&checkouts).map(str::to_string);
        let (decided, outcome) =
            decide(service, view, project, preserved.as_deref(), request).await;
        let checkouts = match outcome {
            Outcome::Classified => checkouts,
            Outcome::Applied(report) => report
                .map(|report| checkout_rows(harp, &report))
                .unwrap_or_default(),
            // What is left after a purge that failed part way.
            Outcome::Failed => worktree_rows(service, project, harp).unwrap_or(checkouts),
        };
        let destroyed = decided.action == "purge"
            && matches!(decided.verdict, Verdict::Planned | Verdict::Done);
        let why = match &preserved {
            Some(worktree) => format!(
                "its scratch worktree {worktree} must be preserved, so the session is spared"
            ),
            None if decided.verdict == Verdict::Failed => {
                format!("its purge failed ({})", decided.reason)
            }
            None => decided.reason.clone(),
        };
        rows.extend(checkouts.into_iter().map(|c| {
            if c.reapable && !destroyed {
                kept_whole(harp, &c.worktree, &why)
            } else {
                c.row
            }
        }));
        rows.push(decided);
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

/// What the purge did to the session's checkouts.
enum Outcome {
    /// Nothing was applied: the classification stands.
    Classified,
    /// `session remove`'s order ran on `yes`: its worktrees' report (none
    /// when the session has none).
    Applied(Option<Report>),
    /// The purge failed: what is left is read again.
    Failed,
}

/// The session's own row, by the module's table, and what became of its
/// checkouts.
async fn decide<S: Removals + Send>(
    service: &mut S,
    view: &View,
    project: Option<&str>,
    preserved: Option<&str>,
    request: &SweepRequest,
) -> (SweepRow, Outcome) {
    let harp = view.record.harp.as_str();
    let kept = |r| (r, Outcome::Classified);
    match view.state {
        State::Live => {
            return kept(row(
                harp,
                "skip",
                Verdict::Skipped,
                "it is running",
                String::new(),
            ))
        }
        State::Unknown => {
            return kept(row(
                harp,
                "skip",
                Verdict::Skipped,
                "its liveness cannot be proven",
                format!("ctxloom session remove {harp} --even-if-live"),
            ))
        }
        State::Ended if view.record.kept => {
            return kept(row(
                harp,
                "keep",
                Verdict::Held,
                "it is marked keep: its transcript is kept at any age",
                format!("ctxloom session keep {harp} --undo"),
            ))
        }
        State::Ended => {}
    }
    // Go's DecideSweep: a checkout that must be preserved spares its
    // session from purge too, whatever the purge age says.
    if let Some(worktree) = preserved {
        return kept(row(
            harp,
            "spare",
            Verdict::Left,
            &format!(
                "its scratch worktree {worktree} must be preserved, so the session is spared from reclaim and purge and the work stays where it is"
            ),
            String::new(),
        ));
    }
    let Some(cutoff) = request.purge_cutoff else {
        return kept(row(
            harp,
            "purge",
            Verdict::Held,
            "no --purge-older-than or session_purge_age is set, so nothing is purged",
            String::new(),
        ));
    };
    let active = view.end().unwrap_or(view.record.started_at);
    if (active.seconds, active.nanos) > (cutoff.seconds, cutoff.nanos) {
        return kept(row(
            harp,
            "purge",
            Verdict::Left,
            "it was active after the purge age",
            String::new(),
        ));
    }
    let purge = format!("ctxloom session remove {harp} --yes");
    purge_row(service, project, harp, &purge, request.yes).await
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

/// One checkout's row, whether it must be preserved, and whether it was
/// classified reapable (clean, its owner dead, not yet removed).
struct Checkout {
    row: SweepRow,
    preserved: Option<String>,
    reapable: bool,
    /// The checkout's directory name.
    worktree: String,
}

/// The row of a clean checkout its kept session keeps: held, saying `why`
/// the session was kept, and naming no command (checkouts go only with
/// their session).
fn kept_whole(harp: &str, clean: &str, why: &str) -> SweepRow {
    row(
        harp,
        "worktree",
        Verdict::Held,
        &format!("{clean} kept: its session is kept whole, every checkout with it: {why}"),
        String::new(),
    )
}

/// The first checkout that must be preserved (spared in place, or skipped:
/// not the sweep's to remove), by name.
fn preserved_worktree(checkouts: &[Checkout]) -> Option<&str> {
    checkouts.iter().find_map(|c| c.preserved.as_deref())
}

/// A session's scratch checkouts, classified through the worktree leaf,
/// changing nothing: planned, spared in place, or skipped, each with its
/// reason. A session with no directory has none.
fn worktree_rows<S: Removals>(
    service: &mut S,
    project: Option<&str>,
    harp: &str,
) -> Result<Vec<Checkout>, ReclaimError> {
    match service.worktrees(project, harp, false) {
        Ok(report) => Ok(checkout_rows(harp, &report)),
        Err(IsolationError::NoSession { .. }) => Ok(Vec::new()),
        Err(error) => Err(ReclaimError::Isolation(error)),
    }
}

/// The checkouts of a worktree leaf's report.
fn checkout_rows(harp: &str, report: &Report) -> Vec<Checkout> {
    report
        .worktrees
        .iter()
        .map(|checkout| Checkout {
            row: worktree_row(harp, checkout),
            preserved: matches!(checkout.verdict, Worktree::Spared | Worktree::Skipped)
                .then(|| checkout.worktree.clone()),
            reapable: checkout.verdict == Worktree::Reapable,
            worktree: checkout.worktree.clone(),
        })
        .collect()
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
) -> (SweepRow, Outcome) {
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
        Err(error) => return (failed(harp, &error), Outcome::Failed),
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
        return (
            row(harp, "purge", verdict, &reason, command),
            Outcome::Classified,
        );
    }
    if let Some(kept) = kept {
        return (
            row(harp, "purge", Verdict::Held, &kept, String::new()),
            Outcome::Classified,
        );
    }
    // A session whose directory holds no file is purged too: its record is
    // its stream (ruling 87a), which the destroy takes.
    let bytes: u64 = removal.files.destroy.iter().map(|i| i.bytes).sum();
    if !yes {
        let mut r = row(harp, "purge", Verdict::Planned, "", command.to_string());
        r.bytes = bytes;
        r.authored = removal.authored();
        r.removal = Some(removal);
        return (r, Outcome::Classified);
    }
    let done = match crate::remove(service, ask(true)).await {
        Ok(done) => done,
        Err(error) => return (failed(harp, &error), Outcome::Failed),
    };
    let r = match &done {
        done if done.removal.applied => {
            let mut r = row(harp, "purge", Verdict::Done, "", String::new());
            r.bytes = done.removal.files.bytes_freed;
            r.authored = done.removal.authored();
            r.removal = Some(done.removal.clone());
            r
        }
        // Something changed between the plan and the apply: what it says.
        done => match (&done.kept, &done.removal.refusal) {
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
    };
    (r, Outcome::Applied(done.worktrees))
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
