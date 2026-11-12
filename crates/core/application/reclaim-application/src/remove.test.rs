//! `session remove`'s order over fakes of the sessions and worktree use
//! cases: session_worktrees.feature's "Purging a session takes its worktrees
//! only when it can prove that safe" (F2).

use std::future::Future;
use std::pin::pin;
use std::task::{Context, Poll, Waker};

use isolation_application::{IsolationError, Report, Row, Verdict, WorktreeUse};
use session_index::View;
use sessions_application::{
    AppError, BindRequest, Bound, FilePlan, KeepRequest, Kept, ListQuery, Refusal, Removal,
    RemoveRequest, SearchQuery, SessionService, ShowQuery, TitleRequest, Titled, TranscriptQuery,
    TranscriptRow,
};

use super::remove;
use crate::ReclaimError;

const HARP: &str = "amber-swift-owl";

fn ready<T>(future: impl Future<Output = T>) -> T {
    let mut future = pin!(future);
    match future
        .as_mut()
        .poll(&mut Context::from_waker(Waker::noop()))
    {
        Poll::Ready(value) => value,
        Poll::Pending => panic!("a fake never waits"),
    }
}

/// Answers a plan's worktrees with `planned`, an applied one's with
/// `applied` (else `planned`), each removal with `refusal`; logs every call
/// in order.
#[derive(Default)]
struct Fake {
    planned: Option<Vec<Verdict>>,
    applied: Option<Vec<Verdict>>,
    refusal: Option<Refusal>,
    fails: bool,
    log: Vec<&'static str>,
    asked: Vec<RemoveRequest>,
}

fn report(verdicts: &[Verdict], applied: bool) -> Report {
    let rows: Vec<Row> = verdicts
        .iter()
        .enumerate()
        .map(|(n, verdict)| Row {
            harp: HARP.to_string(),
            worktree: format!("agent-{n}"),
            path: format!("/h/.ctxloom/worktrees/calm-otter/{HARP}/agent-{n}"),
            owner_state: "dead",
            verdict: *verdict,
            reason: String::new(),
        })
        .collect();
    let count = |v: Verdict| rows.iter().filter(|r| r.verdict == v).count() as u64;
    Report {
        reaped: count(Verdict::Reaped),
        spared: count(Verdict::Spared),
        skipped: count(Verdict::Skipped),
        worktrees: rows,
        applied,
    }
}

impl WorktreeUse for Fake {
    fn worktrees(&mut self, harp: Option<&str>, apply: bool) -> Result<Report, IsolationError> {
        assert_eq!(harp, Some(HARP));
        self.log.push(if apply {
            "worktrees apply"
        } else {
            "worktrees plan"
        });
        if self.fails {
            return Err(IsolationError::NothingRemoved {
                harp: HARP.to_string(),
                spared: 0,
                skipped: 0,
            });
        }
        let verdicts = if apply {
            self.applied.as_ref().or(self.planned.as_ref())
        } else {
            self.planned.as_ref()
        };
        verdicts
            .map(|v| report(v, apply))
            .ok_or(IsolationError::NoSession {
                harp: HARP.to_string(),
            })
    }
}

fn nothing<T>() -> Result<T, AppError> {
    Err(AppError::NoSession {
        harp: String::new(),
    })
}

impl SessionService for Fake {
    async fn list(&mut self, _: ListQuery) -> Result<Vec<View>, AppError> {
        nothing()
    }
    async fn search(&mut self, _: SearchQuery) -> Result<Vec<View>, AppError> {
        nothing()
    }
    async fn show(&mut self, _: ShowQuery) -> Result<View, AppError> {
        nothing()
    }
    async fn keep(&mut self, _: KeepRequest) -> Result<Kept, AppError> {
        nothing()
    }
    async fn remove(&mut self, request: RemoveRequest) -> Result<Removal, AppError> {
        self.log.push(if request.yes {
            "remove apply"
        } else {
            "remove plan"
        });
        self.asked.push(request.clone());
        Ok(Removal {
            harp: request.harp,
            applied: request.yes && self.refusal.is_none(),
            files: FilePlan::default(),
            events_destroyed: 0,
            refusal: self.refusal.clone(),
            dead_letters: Vec::new(),
        })
    }
    async fn set_title(&mut self, _: TitleRequest) -> Result<Titled, AppError> {
        nothing()
    }
    async fn transcripts(&mut self, _: TranscriptQuery) -> Result<Vec<TranscriptRow>, AppError> {
        nothing()
    }
    async fn transcript(&mut self, _: &str) -> Result<String, AppError> {
        nothing()
    }
    async fn bind(&mut self, _: BindRequest) -> Result<Bound, AppError> {
        nothing()
    }
}

fn ask(yes: bool, even_if_live: bool) -> RemoveRequest {
    RemoveRequest {
        project: None,
        harp: HARP.to_string(),
        yes,
        even_if_live,
    }
}

fn kept_counts(kept: &Option<IsolationError>) -> Option<(u64, u64)> {
    match kept {
        Some(IsolationError::KeepsSession {
            harp,
            spared,
            skipped,
        }) => {
            assert_eq!(harp, HARP);
            Some((*spared, *skipped))
        }
        None => None,
        Some(other) => panic!("{other:?}"),
    }
}

// The clean worktrees go, and only then the session.
#[test]
fn the_clean_worktrees_are_removed_and_only_then_the_session_destroyed() {
    let mut fake = Fake {
        planned: Some(vec![Verdict::Reapable]),
        applied: Some(vec![Verdict::Reaped]),
        ..Fake::default()
    };
    let done = ready(remove(&mut fake, ask(true, false))).unwrap();
    assert_eq!(
        fake.log,
        [
            "worktrees plan",
            "remove plan",
            "worktrees apply",
            "remove apply"
        ]
    );
    assert!(done.removal.applied);
    assert_eq!(done.worktrees.unwrap().reaped, 1);
    assert!(done.kept.is_none());
}

// The safety scenario: a worktree with work in it keeps its whole session.
#[test]
fn a_spared_worktree_keeps_the_whole_session_and_nothing_is_applied() {
    let mut fake = Fake {
        planned: Some(vec![Verdict::Reapable, Verdict::Spared]),
        ..Fake::default()
    };
    let done = ready(remove(&mut fake, ask(true, false))).unwrap();
    assert_eq!(kept_counts(&done.kept), Some((1, 0)));
    assert!(!done.removal.applied);
    assert_eq!(fake.log, ["worktrees plan", "remove plan"]);
}

// --even-if-live is the session's override; it reaches no worktree.
#[test]
fn a_skipped_worktree_keeps_the_session_even_if_live() {
    for even_if_live in [false, true] {
        let mut fake = Fake {
            planned: Some(vec![Verdict::Skipped]),
            ..Fake::default()
        };
        let done = ready(remove(&mut fake, ask(true, even_if_live))).unwrap();
        assert_eq!(kept_counts(&done.kept), Some((0, 1)), "{even_if_live}");
        assert_eq!(fake.log, ["worktrees plan", "remove plan"]);
        assert!(fake.asked.iter().all(|r| r.even_if_live == even_if_live));
    }
}

// A preview refuses as the removal would.
#[test]
fn a_preview_a_worktree_keeps_is_kept_and_applies_nothing() {
    let mut fake = Fake {
        planned: Some(vec![Verdict::Spared]),
        ..Fake::default()
    };
    let done = ready(remove(&mut fake, ask(false, false))).unwrap();
    assert_eq!(kept_counts(&done.kept), Some((1, 0)));
    assert_eq!(fake.log, ["worktrees plan", "remove plan"]);
}

// A clean preview plans both, and applies neither.
#[test]
fn a_clean_preview_applies_nothing() {
    let mut fake = Fake {
        planned: Some(vec![Verdict::Reapable]),
        ..Fake::default()
    };
    let done = ready(remove(&mut fake, ask(false, false))).unwrap();
    assert!(done.kept.is_none());
    assert_eq!(fake.log, ["worktrees plan", "remove plan"]);
}

// A worktree that went dirty between the plan and its removal keeps the
// session.
#[test]
fn a_worktree_left_at_its_removal_keeps_the_session() {
    let mut fake = Fake {
        planned: Some(vec![Verdict::Reapable]),
        applied: Some(vec![Verdict::Spared]),
        ..Fake::default()
    };
    let done = ready(remove(&mut fake, ask(true, false))).unwrap();
    assert_eq!(kept_counts(&done.kept), Some((1, 0)));
    assert!(!done.removal.applied);
    assert_eq!(
        fake.log,
        ["worktrees plan", "remove plan", "worktrees apply"]
    );
}

// A session the sessions use case refuses reaps nothing; its worktrees are
// still reported.
#[test]
fn a_refused_session_reaps_nothing_and_reports_its_worktrees() {
    let mut fake = Fake {
        planned: Some(vec![Verdict::Reapable]),
        refusal: Some(Refusal::Kept {
            harp: HARP.to_string(),
        }),
        ..Fake::default()
    };
    let done = ready(remove(&mut fake, ask(true, false))).unwrap();
    assert!(done.removal.refusal.is_some());
    assert!(done.kept.is_none());
    assert_eq!(done.worktrees.unwrap().worktrees.len(), 1);
    assert_eq!(fake.log, ["worktrees plan", "remove plan"]);
}

// A session with no directory has no worktrees, and is removed as ever.
#[test]
fn a_session_with_no_worktrees_is_the_sessions_removal_alone() {
    let mut fake = Fake::default();
    let done = ready(remove(&mut fake, ask(true, false))).unwrap();
    assert!(done.worktrees.is_none());
    assert!(done.removal.applied);
    assert_eq!(fake.log, ["worktrees plan", "remove plan", "remove apply"]);
}

// An empty report is no worktrees either.
#[test]
fn an_empty_worktree_report_is_none() {
    let mut fake = Fake {
        planned: Some(Vec::new()),
        ..Fake::default()
    };
    let done = ready(remove(&mut fake, ask(true, false))).unwrap();
    assert!(done.worktrees.is_none());
    assert_eq!(fake.log, ["worktrees plan", "remove plan", "remove apply"]);
}

// A worktree leaf that failed is the removal's failure, and destroys nothing.
#[test]
fn a_worktree_failure_is_the_removals_and_destroys_nothing() {
    let mut fake = Fake {
        fails: true,
        ..Fake::default()
    };
    let error = ready(remove(&mut fake, ask(true, false))).unwrap_err();
    assert!(matches!(error, ReclaimError::Isolation(_)), "{error:?}");
    assert!(!fake.log.contains(&"remove apply"));
}
