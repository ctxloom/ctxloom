//! The daemon's destroy (`DeleteSession`, `SweepSessions`) over the real
//! sessions aggregates, an in-memory store purged through the port, the
//! warm session index, and fakes of a project's places (its files, its
//! worktrees, its configuration).

use std::collections::BTreeMap;
use std::sync::{Arc, Mutex};

use angzarr_embedded::v1::{Cover, Uuid};
use angzarr_embedded::{EventStore, StreamRef, Timestamp};
use daemon_proto::v1;
use isolation_application::{IsolationError, Report, Row, Verdict, WorktreeUse};
use reclaim_application::SweepConfig;
use run_ports::Edge;
use session_index::State;
use session_ports::{FileItem, SessionFiles};

use super::{Places, Purges};
use crate::server::tests::{fixture, refused, Fixture, World, PROJECT, ROOT};
use crate::{Answer, ApiError, ApiRefusal};

const NOW: i64 = 1_800_000_000;
const DAY: i64 = 86_400;
const HARP: &str = "ended-amber-owl";
const NAME: &str = "projects/calm-quiet-heron/sessions/ended-amber-owl";

fn at(seconds: i64) -> Timestamp {
    Timestamp { seconds, nanos: 0 }
}

/// What a test's project holds and was asked.
#[derive(Default)]
pub(crate) struct Held {
    /// Each session's files, by harp.
    survey: BTreeMap<String, Vec<FileItem>>,
    /// When each session's engine last wrote, by harp.
    last_active: BTreeMap<String, Timestamp>,
    /// The sessions whose directory was destroyed (taken into the trash),
    /// in order.
    destroyed: Vec<String>,
    /// Each trash: its tree, its request, its path.
    trashed: Vec<(String, String, String)>,
    /// The trash slots purged, by request.
    purged: Vec<String>,
    fail_destroy: bool,
    /// The next purge of a slot fails (once).
    fail_purge: bool,
    /// The verdicts of a session's worktrees, planned and applied.
    planned: Option<Vec<Verdict>>,
    applied: Option<Vec<Verdict>>,
    /// What the worktrees were asked: the directory, the harp, the apply,
    /// and whether the caller's environment came with it.
    worktrees_asked: Vec<(String, String, bool, bool)>,
    /// The configured purge age, and the roots it was read from.
    purge_age: Option<String>,
    settings_asked: Vec<String>,
    /// The roots whose purge age was read.
    purge_age_asked: Vec<String>,
    /// The configured transcript retention, and the roots it was read from.
    retention: Option<String>,
    retention_asked: Vec<String>,
    /// The directories whose files were asked for.
    files_asked: Vec<String>,
}

/// A project's places over [`Held`].
#[derive(Default)]
pub(crate) struct FakePlaces(Mutex<Held>);

impl FakePlaces {
    fn held(&self) -> std::sync::MutexGuard<'_, Held> {
        self.0.lock().unwrap()
    }
}

struct Files(Arc<FakePlaces>);

impl SessionFiles for Files {
    fn last_active(&mut self, harp: &str) -> Result<Option<Timestamp>, coded_diagnostic::Report> {
        Ok(self.0.held().last_active.get(harp).copied())
    }

    fn survey(&mut self, harp: &str) -> Result<Vec<FileItem>, coded_diagnostic::Report> {
        Ok(self.0.held().survey.get(harp).cloned().unwrap_or_default())
    }

    fn size(&mut self, harp: &str) -> Result<u64, coded_diagnostic::Report> {
        Ok(self
            .0
            .held()
            .survey
            .get(harp)
            .map_or(0, |files| files.iter().map(|f| f.bytes).sum()))
    }
}

/// A project's tree's atomic operations: a trash of `sessions/<harp>` is
/// that session's directory taken; a purge drops its request's slot.
struct Operations(Arc<FakePlaces>);

impl ctxloom_ports::Operations for Operations {
    fn operate(
        &self,
        tree: &str,
        request_id: &str,
        operation: &ctxloom_ports::Operation,
        at: &dyn Fn(ctxloom_ports::Point),
    ) -> Result<ctxloom_ports::Operated, ctxloom_ports::Failure> {
        let mut held = self.0.held();
        match operation {
            ctxloom_ports::Operation::Trash { path } => {
                if held.fail_destroy {
                    return Err(Box::new(coded_diagnostic::Report::new("x-destroy-failed")));
                }
                let harp = path.strip_prefix("sessions/").unwrap_or(path).to_string();
                if held.destroyed.contains(&harp) {
                    // Retried after the rename: it is in its slot.
                    return Ok(ctxloom_ports::Operated::Done);
                }
                held.destroyed.push(harp);
                held.trashed
                    .push((tree.to_string(), request_id.to_string(), path.clone()));
                drop(held);
                at(ctxloom_ports::Point::Trashed);
            }
            ctxloom_ports::Operation::Purge => {
                if std::mem::take(&mut held.fail_purge) {
                    return Err(Box::new(coded_diagnostic::Report::new("x-purge-failed")));
                }
                held.purged.push(request_id.to_string());
            }
            other => panic!("a removal makes no {other:?}"),
        }
        Ok(ctxloom_ports::Operated::Done)
    }

    fn sweep_staging(&self, _tree: &str) -> Result<(), ctxloom_ports::Failure> {
        Ok(())
    }

    fn purge_slot(&self, _tree: &str, request_id: &str) -> Result<(), ctxloom_ports::Failure> {
        self.0.held().purged.push(request_id.to_string());
        Ok(())
    }
}

struct Checkouts {
    places: Arc<FakePlaces>,
    dir: String,
    env: bool,
}

impl WorktreeUse for Checkouts {
    fn worktrees(&mut self, harp: Option<&str>, apply: bool) -> Result<Report, IsolationError> {
        let harp = harp.unwrap_or_default().to_string();
        let mut held = self.places.held();
        held.worktrees_asked
            .push((self.dir.clone(), harp.clone(), apply, self.env));
        let verdicts = match apply {
            true => held.applied.clone().or(held.planned.clone()),
            false => held.planned.clone(),
        };
        let Some(verdicts) = verdicts else {
            return Err(IsolationError::NoSession { harp });
        };
        let rows: Vec<Row> = verdicts
            .iter()
            .enumerate()
            .map(|(n, verdict)| Row {
                harp: harp.clone(),
                worktree: format!("ctxloom-wt-{n}"),
                path: format!("{ROOT}/.ctxloom/sessions/{harp}/work/ctxloom-wt-{n}"),
                owner_state: "dead",
                verdict: *verdict,
                reason: String::new(),
            })
            .collect();
        let count = |v: Verdict| rows.iter().filter(|r| r.verdict == v).count() as u64;
        Ok(Report {
            reaped: count(Verdict::Reaped),
            spared: count(Verdict::Spared),
            skipped: count(Verdict::Skipped),
            worktrees: rows,
            applied: apply,
        })
    }
}

struct Settings(Arc<FakePlaces>, String);

impl SweepConfig for Settings {
    fn purge_age(&mut self) -> Result<Option<String>, reclaim_application::Failure> {
        let mut held = self.0.held();
        held.purge_age_asked.push(self.1.clone());
        Ok(held.purge_age.clone())
    }

    fn retention(&mut self) -> Result<Option<String>, reclaim_application::Failure> {
        let mut held = self.0.held();
        held.retention_asked.push(self.1.clone());
        Ok(held.retention.clone())
    }
}

/// The places a test's daemon reaches, shared with the test through an
/// `Arc` of the fixture's.
impl Places for Arc<FakePlaces> {
    fn files(&self, project_dir: &str) -> Box<dyn SessionFiles + Send> {
        self.held().files_asked.push(project_dir.to_string());
        Box::new(Files(Arc::clone(self)))
    }

    fn worktrees(
        &self,
        project_dir: &str,
        _project_id: &str,
        env: &[(String, String)],
    ) -> Box<dyn WorktreeUse + Send> {
        Box::new(Checkouts {
            places: Arc::clone(self),
            dir: project_dir.to_string(),
            env: env.iter().any(|(name, _)| name == "GIT_TRACE"),
        })
    }

    fn settings(&self, root: &str, _env: &[(String, String)]) -> Box<dyn SweepConfig + Send> {
        self.held().settings_asked.push(root.to_string());
        Box::new(Settings(Arc::clone(self), root.to_string()))
    }

    fn now(&self) -> Timestamp {
        at(NOW)
    }

    fn operations(&self) -> Arc<dyn ctxloom_ports::Operations> {
        Arc::new(Operations(Arc::clone(self)))
    }
}

impl Purges for World {
    fn purge<'a>(&'a self, domain: &'a str, root: Uuid) -> Answer<'a, u64> {
        Box::pin(async move {
            let root = uuid::Uuid::from_slice(&root.value).map_err(|e| ApiError::Failed {
                detail: e.to_string(),
            })?;
            sessions_application::purge(&self.store, StreamRef::main(domain, root))
                .await
                .map_err(|e| ApiError::Failed {
                    detail: e.to_string(),
                })
        })
    }
}

fn file(harp: &str, rel: &str, bytes: u64) -> FileItem {
    FileItem {
        path: format!("{ROOT}/.ctxloom/sessions/{harp}/{rel}"),
        rel: rel.to_string(),
        bytes,
    }
}

/// The session's stream root, and its first run's.
fn roots(name: &str) -> (uuid::Uuid, uuid::Uuid) {
    let name = sessions::SessionName::parse(name).unwrap();
    let session = uuid::Uuid::from_slice(&name.root().value).unwrap();
    let run = runs::run_root(&name.uid(), 1);
    (session, uuid::Uuid::from_slice(&run.value).unwrap())
}

async fn head(world: &World, domain: &str, root: uuid::Uuid) -> u64 {
    EventStore::head(&world.store, StreamRef::main(domain, root))
        .await
        .unwrap()
        .next_sequence
}

/// Whether the stream is destroyed: a purge left its tombstone as the last
/// event (the stream stays, its events gone).
async fn purged(world: &World, domain: &str, root: uuid::Uuid) -> bool {
    let slice = EventStore::read(&world.store, StreamRef::main(domain, root), 0, None)
        .await
        .unwrap();
    slice
        .pages
        .last()
        .is_some_and(angzarr_embedded::angzarr_ports::tombstone::is)
}

/// A run stream of the session's first incarnation, with one event: the
/// run of `session` (its id text) enqueued.
async fn a_run_of(world: &World, session: &str, root: uuid::Uuid) {
    use angzarr_embedded::{Payload, Write};
    let enqueued = runs::RunEvent::RunEnqueued(runs::RunEnqueued {
        session: session.to_string(),
        incarnation: 1,
        mode: runs::RunMode::Structured,
        launcher_lock: String::new(),
        grant: None,
        spec: None,
    });
    let page = angzarr_embedded::v1::EventPage {
        sequence: 0,
        body: Some(angzarr_embedded::v1::event_page::Body::Event(
            enqueued.to_any(),
        )),
        meta: None,
    };
    let stream = StreamRef::main(runs::RUN_DOMAIN, root);
    EventStore::write(
        &world.store,
        Write::append(stream, 0, std::slice::from_ref(&page)),
    )
    .await
    .unwrap();
}

/// A fixture whose project is registered, holding the finished session
/// `harp` (its lock free, its engine last active 60 days ago), whose
/// directory holds `files`.
async fn finished(harp: &str, files: Vec<FileItem>) -> Fixture {
    let f = fixture(vec!["calm-quiet-heron"]);
    f.api
        .resolve_project(v1::ResolveProjectRequest {
            caller: Some(v1::Caller {
                cwd: ROOT.to_string(),
                ..v1::Caller::default()
            }),
            project_id: PROJECT.to_string(),
            root: ROOT.to_string(),
            ..v1::ResolveProjectRequest::default()
        })
        .await
        .unwrap();
    add(&f, harp, files).await;
    f
}

/// Another finished session of the fixture's project.
async fn add(f: &Fixture, harp: &str, files: Vec<FileItem>) {
    f.mint(harp).await;
    f.catch_up().await;
    let mut held = f.places.held();
    held.survey.insert(harp.to_string(), files);
    held.last_active
        .insert(harp.to_string(), at(NOW - 60 * DAY));
}

fn delete(name: &str, yes: bool) -> v1::DeleteSessionRequest {
    v1::DeleteSessionRequest {
        name: name.to_string(),
        validate_only: !yes,
        caller: Some(v1::Caller {
            env: Some(v1::EnvVars {
                vars: vec![v1::EnvVar {
                    name: "GIT_TRACE".to_string(),
                    value: "0".to_string(),
                }],
            }),
            ..v1::Caller::default()
        }),
        ..v1::DeleteSessionRequest::default()
    }
}

fn transcript_and_notes(harp: &str) -> Vec<FileItem> {
    vec![
        file(harp, "design-notes.md", 4),
        file(harp, "native/history.jsonl", 3),
    ]
}

// `session remove` without --yes: the plan, every file by its class, the
// authored ones named, nothing destroyed.
#[tokio::test]
async fn a_delete_validate_only_is_the_plan_and_destroys_nothing() {
    let f = finished(HARP, transcript_and_notes(HARP)).await;
    let plan = f.api.delete_session(delete(NAME, false)).await.unwrap();
    assert_eq!(plan.name, NAME);
    assert_eq!(plan.harp, HARP);
    assert_eq!(plan.applied, Some(false));
    let classes: Vec<(&str, i32)> = plan
        .files
        .iter()
        .map(|file| (file.rel.as_str(), file.class))
        .collect();
    assert_eq!(
        classes,
        [
            ("design-notes.md", v1::removal_file::Class::Authored as i32),
            (
                "native/history.jsonl",
                v1::removal_file::Class::Machine as i32
            ),
        ]
    );
    assert_eq!(plan.authored, ["design-notes.md"]);
    assert_eq!((plan.bytes_freed, plan.events_destroyed), (0, 0));
    assert!(f.places.held().destroyed.is_empty());
    let (session, _) = roots(NAME);
    assert!(head(&f.world, sessions::SESSION_DOMAIN, session).await > 0);
    assert_eq!(f.places.held().files_asked, [ROOT]);
}

// Rulings 94, 94b, 98: --yes destroys the session in the daemon's store:
// its stream and its run's purged, counted, its directory removed, and the
// warm index no longer holds it.
#[tokio::test]
async fn a_delete_destroys_the_session_its_runs_and_its_directory() {
    let f = finished(HARP, transcript_and_notes(HARP)).await;
    let (session, run) = roots(NAME);
    a_run_of(&f.world, &uid_of(HARP), run).await;
    let events = head(&f.world, sessions::SESSION_DOMAIN, session).await
        + head(&f.world, runs::RUN_DOMAIN, run).await;
    let done = f.api.delete_session(delete(NAME, true)).await.unwrap();
    assert_eq!(done.applied, Some(true));
    assert_eq!(done.events_destroyed, events);
    assert_eq!(done.bytes_freed, 7);
    assert!(purged(&f.world, sessions::SESSION_DOMAIN, session).await);
    assert!(purged(&f.world, runs::RUN_DOMAIN, run).await);
    assert_eq!(f.places.held().destroyed, [HARP]);
    let gone = f
        .api
        .get_session(v1::GetSessionRequest {
            name: NAME.to_string(),
        })
        .await;
    let (_, missing): (_, v1::SessionNotFound) = refused(gone);
    assert_eq!(missing.name, NAME);
}

/// The apply of `plan`, the preview of `NAME`: its etag sent back, and
/// `answers`.
fn apply_of(
    plan: &v1::DeleteSessionResponse,
    answers: Vec<v1::Answer>,
) -> v1::DeleteSessionRequest {
    v1::DeleteSessionRequest {
        etag: plan.etag.clone(),
        answers,
        ..delete(NAME, true)
    }
}

/// The removal's confirmation, answered `yes`.
fn confirm_answer(yes: bool) -> Vec<v1::Answer> {
    vec![v1::Answer {
        need_id: "confirm".to_string(),
        answer: Some(v1::answer::Answer::Confirmed(yes)),
    }]
}

// Step 10c U11: the preview asks its one question, a Confirm of the
// session's removal, and tags what it showed.
#[tokio::test]
async fn a_removal_preview_asks_its_confirmation_and_tags_its_plan() {
    let f = finished(HARP, transcript_and_notes(HARP)).await;
    let plan = f.api.delete_session(delete(NAME, false)).await.unwrap();
    let asked: Vec<(&str, &str)> = plan
        .needs
        .iter()
        .filter_map(|need| match &need.kind {
            Some(v1::need::Kind::Confirm(c)) if !need.optional => {
                Some((need.id.as_str(), c.text.as_str()))
            }
            _ => None,
        })
        .collect();
    let question = format!("Remove session \"{HARP}\"?");
    assert_eq!(asked, [("confirm", question.as_str())]);
    assert!(!plan.etag.is_empty());
    let again = f.api.delete_session(delete(NAME, false)).await.unwrap();
    assert_eq!(again.etag, plan.etag, "the same plan, the same tag");
}

// Its apply, with the preview's tag and a yes, destroys the session.
#[tokio::test]
async fn an_answered_apply_of_the_preview_destroys_the_session() {
    let f = finished(HARP, transcript_and_notes(HARP)).await;
    let plan = f.api.delete_session(delete(NAME, false)).await.unwrap();
    let done = f
        .api
        .delete_session(apply_of(&plan, confirm_answer(true)))
        .await
        .unwrap();
    assert_eq!(done.applied, Some(true));
    assert_eq!(f.places.held().destroyed, [HARP]);
}

// An apply of the preview that does not say yes to its question is
// refused `NeedsUnanswered`, and destroys nothing.
#[tokio::test]
async fn an_apply_not_confirmed_is_refused_and_destroys_nothing() {
    let f = finished(HARP, transcript_and_notes(HARP)).await;
    let plan = f.api.delete_session(delete(NAME, false)).await.unwrap();
    for answers in [Vec::new(), confirm_answer(false)] {
        let (_, unanswered): (_, v1::NeedsUnanswered) =
            refused(f.api.delete_session(apply_of(&plan, answers)).await);
        assert_eq!(unanswered.need_ids, ["confirm"]);
    }
    assert!(f.places.held().destroyed.is_empty());
}

// What the preview showed changed before the answer (a file grew): the apply is refused
// `PlanStale` (ABORTED), naming both tags, and destroys nothing.
#[tokio::test]
async fn an_apply_of_a_plan_that_changed_since_is_refused_stale() {
    let f = finished(HARP, transcript_and_notes(HARP)).await;
    let plan = f.api.delete_session(delete(NAME, false)).await.unwrap();
    f.places.held().survey.insert(
        HARP.to_string(),
        vec![
            file(HARP, "design-notes.md", 9),
            file(HARP, "native/history.jsonl", 3),
        ],
    );
    let (code, stale): (_, v1::PlanStale) = refused(
        f.api
            .delete_session(apply_of(&plan, confirm_answer(true)))
            .await,
    );
    assert_eq!(
        code,
        <v1::PlanStale as daemon_proto::Refusal>::REFUSAL_STATUS
    );
    assert_eq!(stale.seen, plan.etag);
    assert_ne!(stale.now, plan.etag);
    assert!(f.places.held().destroyed.is_empty());
}

/// The refusal a delete answered, decoded.
fn removal_refused(result: Result<v1::DeleteSessionResponse, ApiError>) -> v1::RemovalRefusal {
    let (code, refused): (_, v1::RemovalRefusal) = refused(result);
    assert_eq!(
        code,
        <v1::RemovalRefusal as daemon_proto::Refusal>::REFUSAL_STATUS
    );
    refused
}

// A session whose lock does not prove it ended is refused, carrying the
// plan: held says its owner is alive, none that nothing proves it ended.
#[tokio::test]
async fn a_session_its_lock_cannot_prove_ended_is_refused_with_its_plan() {
    for (state, says) in [
        (State::Live, "owner is alive"),
        (State::Unknown, "no lock to prove"),
    ] {
        let f = finished(HARP, transcript_and_notes(HARP)).await;
        f.locks.0.lock().unwrap().insert(HARP.to_string(), state);
        let refused = removal_refused(f.api.delete_session(delete(NAME, true)).await);
        assert_eq!(
            refused.reason,
            v1::removal_refusal::Reason::NotProvenDead as i32
        );
        assert!(refused.detail.contains(says), "{}", refused.detail);
        let plan = refused.plan.expect("the plan");
        assert_eq!(plan.files.len(), 2, "the plan is carried");
        assert_eq!(plan.authored, ["design-notes.md"]);
        assert_eq!(plan.applied, Some(false));
        assert!(f.places.held().destroyed.is_empty());
    }
}

// --even-if-live: the daemon judges by the lock, and the override lets an
// unproven one go.
#[tokio::test]
async fn even_if_live_destroys_a_session_its_lock_cannot_prove_ended() {
    let f = finished(HARP, transcript_and_notes(HARP)).await;
    f.locks
        .0
        .lock()
        .unwrap()
        .insert(HARP.to_string(), State::Unknown);
    let done = f
        .api
        .delete_session(v1::DeleteSessionRequest {
            even_if_live: true,
            ..delete(NAME, true)
        })
        .await
        .unwrap();
    assert_eq!(done.applied, Some(true));
    assert_eq!(f.places.held().destroyed, [HARP]);
}

// D18: on Windows a held lock keeps the session's directory from being
// renamed into the trash, so it blocks the removal there, and only a held
// one; on every other platform nothing does.
#[test]
fn a_held_lock_blocks_a_removal_on_windows_alone() {
    let lock = || "C:/p/.ctxloom/sessions/h/session.lock".to_string();
    assert_eq!(super::blocking_lock("windows", State::Live, lock), lock());
    for state in [State::Ended, State::Unknown] {
        assert_eq!(
            super::blocking_lock("windows", state, lock),
            "",
            "{state:?}"
        );
    }
    for os in ["linux", "macos", "freebsd", ""] {
        assert_eq!(super::blocking_lock(os, State::Live, lock), "", "{os}");
    }
}

// D18: on Windows `--even-if-live` of a session whose lock is held is
// refused up front, naming the lock, with its plan; nothing is destroyed.
#[tokio::test]
async fn even_if_live_of_a_held_lock_is_refused_on_windows_naming_the_lock() {
    let f = finished(HARP, transcript_and_notes(HARP)).await;
    f.locks
        .0
        .lock()
        .unwrap()
        .insert(HARP.to_string(), State::Live);
    let api = f.api.on_os("windows");
    let refused = removal_refused(
        api.delete_session(v1::DeleteSessionRequest {
            even_if_live: true,
            ..delete(NAME, true)
        })
        .await,
    );
    assert_eq!(refused.reason, v1::removal_refusal::Reason::LockHeld as i32);
    assert_eq!(
        refused.detail,
        format!("{ROOT}/.ctxloom/sessions/{HARP}/session.lock")
    );
    assert_eq!(refused.plan.expect("the plan").applied, Some(false));
    assert!(f.places.held().destroyed.is_empty());
}

// Unix is unchanged: `--even-if-live` removes a session whose lock is held.
#[tokio::test]
async fn even_if_live_of_a_held_lock_destroys_it_off_windows() {
    let f = finished(HARP, transcript_and_notes(HARP)).await;
    f.locks
        .0
        .lock()
        .unwrap()
        .insert(HARP.to_string(), State::Live);
    let api = f.api.on_os("linux");
    let done = api
        .delete_session(v1::DeleteSessionRequest {
            even_if_live: true,
            ..delete(NAME, true)
        })
        .await
        .unwrap();
    assert_eq!(done.applied, Some(true));
    assert_eq!(f.places.held().destroyed, [HARP]);
}

// A kept session is refused at any age; one within its retention says when
// it ended.
#[tokio::test]
async fn a_kept_or_retained_session_is_refused() {
    let f = finished(HARP, transcript_and_notes(HARP)).await;
    let (session, _) = roots(NAME);
    let cover = Cover {
        domain: sessions::SESSION_DOMAIN.to_string(),
        root: Some(Uuid::from_bytes(session.into_bytes())),
        edition: None,
    };
    let keep = sessions::keyed_book(cover, &sessions::SetKept { kept: true }, "keep");
    f.world.handle(keep).await.unwrap();
    f.catch_up().await;
    let refused = removal_refused(f.api.delete_session(delete(NAME, true)).await);
    assert_eq!(refused.reason, v1::removal_refusal::Reason::Kept as i32);

    let f = finished(HARP, transcript_and_notes(HARP)).await;
    f.places
        .held()
        .last_active
        .insert(HARP.to_string(), at(NOW - 3600));
    let refused = removal_refused(f.api.delete_session(delete(NAME, true)).await);
    assert_eq!(refused.reason, v1::removal_refusal::Reason::Retained as i32);
    assert!(refused.detail.contains("it ended"), "{}", refused.detail);
    assert!(f.places.held().destroyed.is_empty());
}

// The safe order (owner, 2026-11-12): a worktree spared or skipped keeps
// the whole session, even with --even-if-live; the plan carries the
// worktrees, classified in the session's project with the caller's
// environment, and nothing is removed.
#[tokio::test]
async fn a_worktree_that_is_not_safe_keeps_the_whole_session() {
    for even_if_live in [false, true] {
        let f = finished(HARP, transcript_and_notes(HARP)).await;
        f.places.held().planned = Some(vec![Verdict::Reapable, Verdict::Spared]);
        let refused = removal_refused(
            f.api
                .delete_session(v1::DeleteSessionRequest {
                    even_if_live,
                    ..delete(NAME, true)
                })
                .await,
        );
        assert_eq!(
            refused.reason,
            v1::removal_refusal::Reason::WorktreesKeep as i32
        );
        assert_eq!((refused.spared, refused.skipped), (1, 0));
        let worktrees = refused.plan.and_then(|plan| plan.worktrees).unwrap();
        assert_eq!(worktrees.worktrees.len(), 2);
        assert_eq!(worktrees.worktrees[1].verdict, "spared");
        assert_eq!(
            f.places.held().worktrees_asked,
            [(ROOT.to_string(), HARP.to_string(), false, true)]
        );
        assert!(f.places.held().destroyed.is_empty());
    }
}

// The clean worktrees are removed, and only then the session destroyed.
#[tokio::test]
async fn the_clean_worktrees_go_and_then_the_session() {
    let f = finished(HARP, transcript_and_notes(HARP)).await;
    {
        let mut held = f.places.held();
        held.planned = Some(vec![Verdict::Reapable]);
        held.applied = Some(vec![Verdict::Reaped]);
    }
    let done = f.api.delete_session(delete(NAME, true)).await.unwrap();
    assert_eq!(done.applied, Some(true));
    let worktrees = done.worktrees.unwrap();
    assert_eq!((worktrees.reaped, worktrees.applied), (1, Some(true)));
    let asked: Vec<bool> = f
        .places
        .held()
        .worktrees_asked
        .iter()
        .map(|asked| asked.2)
        .collect();
    assert_eq!(asked, [false, true]);
    assert_eq!(f.places.held().destroyed, [HARP]);
}

// A name no session holds is refused, naming it.
#[tokio::test]
async fn a_delete_of_a_session_nobody_holds_is_not_found() {
    let f = finished(HARP, Vec::new()).await;
    let name = "projects/calm-quiet-heron/sessions/no-such-harp";
    let (_, missing): (_, v1::SessionNotFound) =
        refused(f.api.delete_session(delete(name, true)).await);
    assert_eq!(missing.name, name);
}

// Step 10b O2 (T28): the directory, its lock inside, goes into the
// checkout's trash FIRST, then the streams, so a directory that cannot be
// taken leaves the session whole: listed, its stream as it was.
#[tokio::test]
async fn a_directory_that_cannot_be_trashed_leaves_the_session_whole() {
    let f = finished(HARP, transcript_and_notes(HARP)).await;
    f.places.held().fail_destroy = true;
    let failed = f.api.delete_session(delete(NAME, true)).await;
    assert!(failed.is_err(), "{failed:?}");
    let (session, _) = roots(NAME);
    assert!(head(&f.world, sessions::SESSION_DOMAIN, session).await > 0);
}

// The trash is the checkout's own (QUESTIONS 8): the session's directory,
// `sessions/<harp>` of its project's `.ctxloom`, into the request's slot,
// which the removal's last step purges, so none is left.
#[tokio::test]
async fn a_removal_trashes_the_directory_in_its_projects_tree_and_purges_its_slot() {
    let f = finished(HARP, transcript_and_notes(HARP)).await;
    f.api.delete_session(delete(NAME, true)).await.unwrap();
    let held = f.places.held();
    let [(tree, request, path)] = &held.trashed[..] else {
        panic!("{:?}", held.trashed)
    };
    assert_eq!(tree, &format!("{ROOT}/.ctxloom"));
    assert_eq!(path, &format!("sessions/{HARP}"));
    assert_eq!(
        held.purged,
        std::slice::from_ref(request),
        "its own slot, purged"
    );
}

fn sweep(older_than: &str, yes: bool) -> v1::SweepSessionsRequest {
    v1::SweepSessionsRequest {
        parent: "projects/calm-quiet-heron".to_string(),
        older_than: older_than.to_string(),
        project: "projects/calm-quiet-heron".to_string(),
        validate_only: !yes,
        ..v1::SweepSessionsRequest::default()
    }
}

// `session sweep` without --yes: each session's row by the table, a purge
// planned through the removal, its plan whole; nothing destroyed.
#[tokio::test]
async fn a_sweep_reports_and_plans_through_the_removal() {
    let f = finished(HARP, transcript_and_notes(HARP)).await;
    add(&f, "ended-brisk-moth", Vec::new()).await;
    f.places
        .held()
        .last_active
        .insert("ended-brisk-moth".to_string(), at(NOW - 3600));
    let report = f.api.sweep_sessions(sweep("30d", false)).await.unwrap();
    let row = |harp: &str| {
        report
            .rows
            .iter()
            .find(|row| row.harp == harp && row.action == "purge")
            .cloned()
            .unwrap()
    };
    assert_eq!(row(HARP).verdict, "planned");
    assert_eq!(row(HARP).authored, ["design-notes.md"]);
    assert_eq!(
        row(HARP).command,
        format!("ctxloom session remove {HARP} --yes")
    );
    // Its engine wrote an hour ago: within its transcript's retention.
    assert_eq!(row("ended-brisk-moth").verdict, "held");
    assert!(row("ended-brisk-moth").reason.contains("retention"));
    assert_eq!(report.counts.unwrap().planned, 1);
    assert_eq!(report.sessions.len(), 1);
    assert_eq!(report.sessions[0].name, NAME);
    assert!(!report.purge_cutoff.is_empty());
    assert!(f.places.held().destroyed.is_empty());
    // The flag given, the configured purge age is not read.
    assert!(f.places.held().purge_age_asked.is_empty());
}

// The purge age a sweep is not given is its project's configured one, read
// from its REGISTERED root; one that is neither an age nor a date is
// refused, naming the key.
#[tokio::test]
async fn a_sweep_reads_the_configured_age_from_the_registered_root() {
    let f = finished(HARP, transcript_and_notes(HARP)).await;
    f.places.held().purge_age = Some("30d".to_string());
    let report = f.api.sweep_sessions(sweep("", true)).await.unwrap();
    assert_eq!(f.places.held().purge_age_asked, [ROOT]);
    assert_eq!(report.counts.unwrap().done, 1);
    assert_eq!(f.places.held().destroyed, [HARP]);

    let f = finished(HARP, transcript_and_notes(HARP)).await;
    f.places.held().purge_age = Some("soon".to_string());
    let (_, invalid): (_, v1::AgeInvalid) = refused(f.api.sweep_sessions(sweep("", true)).await);
    assert_eq!(
        (invalid.key.as_str(), invalid.value.as_str()),
        ("session_purge_age", "soon")
    );
    assert!(f.places.held().destroyed.is_empty());
}

// With no purge age at all, every purge is held.
#[tokio::test]
async fn a_sweep_with_no_purge_age_holds_every_purge() {
    let f = finished(HARP, transcript_and_notes(HARP)).await;
    let report = f.api.sweep_sessions(sweep("", true)).await.unwrap();
    assert_eq!(report.counts.unwrap().held, 1);
    assert!(report.purge_cutoff.is_empty());
    assert!(f.places.held().destroyed.is_empty());
}

// Every project's sessions (`projects/-`).
#[tokio::test]
async fn a_sweep_of_every_project_reaches_each_session() {
    let f = finished(HARP, transcript_and_notes(HARP)).await;
    let report = f
        .api
        .sweep_sessions(v1::SweepSessionsRequest {
            parent: "projects/-".to_string(),
            ..sweep("30d", false)
        })
        .await
        .unwrap();
    assert!(report.rows.iter().any(|row| row.harp == HARP));
}

// ------------------------------------------- the children and the owner

/// The name of `harp` in the fixture's project.
fn name_of(harp: &str) -> String {
    format!("projects/calm-quiet-heron/sessions/{harp}")
}

/// The stream id text of `harp`'s session.
fn uid_of(harp: &str) -> String {
    sessions::SessionName::parse(&name_of(harp)).unwrap().uid()
}

fn cover_of(harp: &str) -> Cover {
    Cover {
        domain: sessions::SESSION_DOMAIN.to_string(),
        root: Some(sessions::SessionName::parse(&name_of(harp)).unwrap().root()),
        edition: None,
    }
}

fn at_session(harp: &str) -> sessions::SessionRef {
    sessions::SessionRef {
        project_id: PROJECT.to_string(),
        session: uid_of(harp),
    }
}

/// The owner `owner` (minted, its incarnation open) launches the child
/// `child`, which is minted for it, its directory holding a file.
async fn launched_child(f: &Fixture, owner: &str, child: &str) {
    let launch = sessions::LaunchChild {
        child_project_id: PROJECT.to_string(),
        child_session: uid_of(child),
        child_root: ROOT.to_string(),
        harp: child.to_string(),
        engine: "claude-code".to_string(),
        trust: sessions::TargetTrust {
            same_project: true,
            trusted: true,
        },
        delegation_limit: 4,
        requester: None,
        prompt: String::new(),
        agent: String::new(),
        client_program: Default::default(),
        settings_places: Vec::new(),
        env: Vec::new(),
    };
    let book = sessions::keyed_book(cover_of(owner), &launch, &format!("launch:{child}"));
    f.world.handle(book).await.unwrap();
    let mint = sessions::MintSession {
        harp: child.to_string(),
        project_id: PROJECT.to_string(),
        project_dir: ROOT.to_string(),
        output_dir: format!("{ROOT}/ctxloom/{child}"),
        engine: "claude-code".to_string(),
        origin: sessions::Origin::Agent,
        started_at: None,
        depth: 1,
        owner: Some(at_session(owner)),
        workspace: None,
    };
    let book = sessions::keyed_book(cover_of(child), &mint, &format!("mint:{child}"));
    f.world.handle(book).await.unwrap();
    f.catch_up().await;
    let mut held = f.places.held();
    held.survey.insert(
        child.to_string(),
        vec![file(child, "native/history.jsonl", 1)],
    );
    held.last_active
        .insert(child.to_string(), at(NOW - 60 * DAY));
}

/// The warm index's record of `harp`.
fn record_of(f: &Fixture, harp: &str) -> session_index::SessionRecord {
    f.index
        .index()
        .find_by_id(&uid_of(harp))
        .unwrap_or_else(|| panic!("{harp} is indexed"))
}

const KID: &str = "ended-kid-near";
const OTHER_KID: &str = "ended-kid-far";

// Rulings 94, 98, D28 K3/M3: a purge emits no event, so a destroy closes each
// child its session holds BEFORE it purges, through the core, and a
// redelivered close step (its keyed command again) records nothing.
#[tokio::test]
async fn a_destroy_closes_the_children_of_the_session_first() {
    let f = finished(HARP, transcript_and_notes(HARP)).await;
    launched_child(&f, HARP, KID).await;
    launched_child(&f, HARP, OTHER_KID).await;
    assert_eq!(record_of(&f, HARP).children.len(), 2);
    let done = f.api.delete_session(delete(NAME, true)).await.unwrap();
    assert_eq!(done.applied, Some(true));
    f.catch_up().await;
    for kid in [KID, OTHER_KID] {
        assert!(record_of(&f, kid).closed, "{kid} is closed");
    }
    let again = session_policies::destroy(
        &at_session(HARP),
        None,
        &[sessions::Child {
            session: Some(at_session(KID)),
            root: String::new(),
        }],
    );
    let kid = uuid::Uuid::parse_str(&uid_of(KID)).unwrap();
    let before = head(&f.world, sessions::SESSION_DOMAIN, kid).await;
    let _ = f.world.handle(again.close[0].book()).await;
    assert_eq!(
        head(&f.world, sessions::SESSION_DOMAIN, kid).await,
        before,
        "a duplicate"
    );
}

// M3: a destroyed child is forgotten by its owner before its purge; the
// owner stays open.
#[tokio::test]
async fn a_destroyed_child_is_forgotten_by_its_owner() {
    let f = finished(HARP, transcript_and_notes(HARP)).await;
    launched_child(&f, HARP, KID).await;
    assert_eq!(record_of(&f, HARP).children.len(), 1);
    let done = f
        .api
        .delete_session(delete(&name_of(KID), true))
        .await
        .unwrap();
    assert_eq!(done.applied, Some(true));
    f.catch_up().await;
    let owner = record_of(&f, HARP);
    assert!(owner.children.is_empty(), "{:?}", owner.children);
    assert!(!owner.closed);
}

// A destroy that is only planned sends nothing to the children.
#[tokio::test]
async fn a_destroy_planned_and_not_applied_closes_no_child() {
    let f = finished(HARP, transcript_and_notes(HARP)).await;
    launched_child(&f, HARP, KID).await;
    f.api.delete_session(delete(NAME, false)).await.unwrap();
    f.catch_up().await;
    assert!(!record_of(&f, KID).closed);
    assert_eq!(record_of(&f, HARP).children.len(), 1);
}

// ------------------------------------------------------------- the board

// The board the daemon's policies plan from is told the session's stream
// and its runs' are gone, as the index is: a destroyed session leaves no
// board row.
#[tokio::test]
async fn a_destroy_leaves_no_board_row() {
    let f = finished(HARP, transcript_and_notes(HARP)).await;
    let (_, run) = roots(NAME);
    a_run_of(&f.world, &uid_of(HARP), run).await;
    f.catch_up().await;
    let id = uid_of(HARP);
    assert!(f.board.board().session(&id).is_some());
    assert!(f.board.board().run(&id, 1).is_some());
    f.api.delete_session(delete(NAME, true)).await.unwrap();
    assert!(f.board.board().session(&id).is_none(), "the session row");
    assert!(f.board.board().run(&id, 1).is_none(), "its run's row");
}

// ------------------------------------------------- the request id (S8)

fn delete_as(name: &str, request_id: &str) -> v1::DeleteSessionRequest {
    v1::DeleteSessionRequest {
        request_id: request_id.to_string(),
        ..delete(name, true)
    }
}

/// `harp`'s name claimed by its session, as a mint's claim leaves it.
async fn claimed(f: &Fixture, harp: &str) {
    let name = sessions::SessionName::parse(&name_of(harp)).unwrap();
    let cover = Cover {
        domain: sessions::NAMES_DOMAIN.to_string(),
        root: Some(name.root()),
        edition: None,
    };
    let claim = sessions::ClaimName {
        project_id: PROJECT.to_string(),
        harp: harp.to_string(),
        session: name.uid(),
    };
    let book = sessions::keyed_book(cover, &claim, &format!("claim:{harp}"));
    f.world.handle(book).await.unwrap();
}

// S8, O2: a destroy retried with its request id, already applied, is
// answered applied (from the store: its name claimed, its stream empty),
// not `SessionNotFound`, and nothing is destroyed twice.
#[tokio::test]
async fn a_destroy_retried_with_its_request_id_answers_its_first_result() {
    let f = finished(HARP, transcript_and_notes(HARP)).await;
    claimed(&f, HARP).await;
    let first = f
        .api
        .delete_session(delete_as(NAME, "req-1"))
        .await
        .unwrap();
    assert_eq!(first.applied, Some(true));
    assert!(first.events_destroyed > 0);
    let again = f
        .api
        .delete_session(delete_as(NAME, "req-1"))
        .await
        .unwrap();
    assert_eq!(
        (again.name.as_str(), again.harp.as_str(), again.applied),
        (NAME, HARP, Some(true))
    );
    assert_eq!(f.places.held().destroyed, [HARP], "destroyed once");
}

/// The events of the project stream, by type.
async fn project_events(f: &Fixture) -> Vec<String> {
    let cover = Cover {
        domain: sessions::PROJECTS_DOMAIN.to_string(),
        root: Some(sessions::project_id_root(PROJECT)),
        edition: None,
    };
    f.world
        .events(&cover, 0)
        .await
        .unwrap()
        .into_iter()
        .filter_map(|page| match page.body {
            Some(angzarr_embedded::v1::event_page::Body::Event(event)) => Some(
                event
                    .type_url
                    .rsplit('.')
                    .next()
                    .unwrap_or_default()
                    .to_string(),
            ),
            _ => None,
        })
        .collect()
}

// O2 (T28): the removal is a command of the PROJECT stream, its steps
// listed in its intent, each recorded done; one interrupted (its last
// step failed) is resumed by its retry from the step it got to: nothing
// done again, nothing intended twice.
#[tokio::test]
async fn a_removal_is_intended_on_the_project_stream_and_resumed_by_its_retry() {
    let f = finished(HARP, transcript_and_notes(HARP)).await;
    claimed(&f, HARP).await;
    f.places.held().fail_purge = true;
    let failed = f.api.delete_session(delete_as(NAME, "req-1")).await;
    assert!(failed.is_err(), "{failed:?}");
    let (session, _) = roots(NAME);
    assert!(
        purged(&f.world, sessions::SESSION_DOMAIN, session).await,
        "purged"
    );
    let restarted = f.restarted().await;
    let again = restarted
        .delete_session(delete_as(NAME, "req-1"))
        .await
        .unwrap();
    assert_eq!(again.applied, Some(true));
    let (destroyed, purged) = {
        let held = f.places.held();
        (held.destroyed.clone(), held.purged.len())
    };
    assert_eq!(destroyed, [HARP], "trashed once");
    assert_eq!(purged, 1, "its slot purged on the retry");
    let events = project_events(&f).await;
    assert_eq!(
        events
            .iter()
            .filter(|e| *e == "SessionRemovalIntended")
            .count(),
        1,
        "{events:?}"
    );
    assert_eq!(
        events.iter().filter(|e| *e == "RemovalStepDone").count(),
        4,
        "{events:?}"
    );
}

// Without a request id, a destroyed session is a name nobody holds.
#[tokio::test]
async fn a_destroy_with_no_request_id_is_not_answered_twice() {
    let f = finished(HARP, transcript_and_notes(HARP)).await;
    claimed(&f, HARP).await;
    f.api.delete_session(delete(NAME, true)).await.unwrap();
    let (_, missing): (_, v1::SessionNotFound) =
        refused(f.api.delete_session(delete(NAME, true)).await);
    assert_eq!(missing.name, NAME);
}

// After a restart the daemon holds no first result: a retried destroy of a
// session the store says was destroyed (its name claimed, its stream empty)
// is answered applied from those facts; a name never claimed is not found.
#[tokio::test]
async fn after_a_restart_a_retried_destroy_is_answered_from_the_stores_facts() {
    let f = finished(HARP, transcript_and_notes(HARP)).await;
    claimed(&f, HARP).await;
    f.api
        .delete_session(delete_as(NAME, "req-1"))
        .await
        .unwrap();
    let restarted = f.restarted().await;
    let again = restarted
        .delete_session(delete_as(NAME, "req-1"))
        .await
        .unwrap();
    assert_eq!(
        (again.name.as_str(), again.harp.as_str(), again.applied),
        (NAME, HARP, Some(true))
    );
    assert_eq!((again.bytes_freed, again.events_destroyed), (0, 0));
    assert_eq!(f.places.held().destroyed, [HARP], "destroyed once");

    let never = name_of("ended-never-owl");
    let (_, missing): (_, v1::SessionNotFound) =
        refused(restarted.delete_session(delete_as(&never, "req-2")).await);
    assert_eq!(missing.name, never);
}

// A plan is no write: its request id keeps nothing.
#[tokio::test]
async fn a_planned_destroy_keeps_no_result_for_its_request_id() {
    let f = finished(HARP, transcript_and_notes(HARP)).await;
    let plan = f
        .api
        .delete_session(v1::DeleteSessionRequest {
            validate_only: true,
            ..delete_as(NAME, "req-1")
        })
        .await
        .unwrap();
    assert_eq!(plan.applied, Some(false));
    let done = f
        .api
        .delete_session(delete_as(NAME, "req-1"))
        .await
        .unwrap();
    assert_eq!(done.applied, Some(true));
}

// A sweep retried with its request id destroys nothing twice: each of its
// removals is its own, keyed `{request_id}:{harp}` on the project stream.
#[tokio::test]
async fn a_sweep_retried_with_its_request_id_destroys_nothing_twice() {
    let f = finished(HARP, transcript_and_notes(HARP)).await;
    let request = v1::SweepSessionsRequest {
        request_id: "sweep-1".to_string(),
        ..sweep("30d", true)
    };
    let first = f.api.sweep_sessions(request.clone()).await.unwrap();
    assert_eq!(first.counts.unwrap().done, 1);
    let again = f.api.sweep_sessions(request).await.unwrap();
    assert_eq!(again.counts.unwrap().done, 0);
    assert_eq!(f.places.held().destroyed, [HARP]);
    let events = project_events(&f).await;
    assert_eq!(
        events
            .iter()
            .filter(|e| *e == "SessionRemovalIntended")
            .count(),
        1,
        "{events:?}"
    );
}

// ------------------------------------------------ the transcript retention

// The retention a removal is refused within is the project's configured
// `session_transcript_retention`, read from its registered root (30 days
// unless configured).
#[tokio::test]
async fn a_removal_honours_the_configured_transcript_retention() {
    // Ended 60 days ago, kept for 90: refused.
    let f = finished(HARP, transcript_and_notes(HARP)).await;
    f.places.held().retention = Some("90d".to_string());
    let refused = removal_refused(f.api.delete_session(delete(NAME, true)).await);
    assert_eq!(refused.reason, v1::removal_refusal::Reason::Retained as i32);
    assert_eq!(f.places.held().retention_asked, [ROOT]);
    assert!(f.places.held().destroyed.is_empty());

    // Ended 10 days ago, kept for 7: destroyed (the default would refuse).
    let f = finished(HARP, transcript_and_notes(HARP)).await;
    {
        let mut held = f.places.held();
        held.retention = Some("7d".to_string());
        held.last_active
            .insert(HARP.to_string(), at(NOW - 10 * DAY));
    }
    let done = f.api.delete_session(delete(NAME, true)).await.unwrap();
    assert_eq!(done.applied, Some(true));

    // None configured: 30 days.
    let f = finished(HARP, transcript_and_notes(HARP)).await;
    f.places
        .held()
        .last_active
        .insert(HARP.to_string(), at(NOW - 10 * DAY));
    let refused = removal_refused(f.api.delete_session(delete(NAME, true)).await);
    assert_eq!(refused.reason, v1::removal_refusal::Reason::Retained as i32);
}

// A configured retention that is no age is refused, naming the key, and
// nothing is removed.
#[tokio::test]
async fn a_configured_retention_that_is_no_age_is_refused() {
    let f = finished(HARP, transcript_and_notes(HARP)).await;
    f.places.held().retention = Some("forever".to_string());
    let (_, invalid): (_, v1::AgeInvalid) = refused(f.api.delete_session(delete(NAME, true)).await);
    assert_eq!(
        (invalid.key.as_str(), invalid.value.as_str()),
        ("session_transcript_retention", "forever")
    );
    assert!(f.places.held().destroyed.is_empty());
}

// A sweep holds a purge within its session's project's configured
// retention.
#[tokio::test]
async fn a_sweep_holds_a_purge_within_the_configured_retention() {
    let f = finished(HARP, transcript_and_notes(HARP)).await;
    f.places.held().retention = Some("90d".to_string());
    let report = f.api.sweep_sessions(sweep("30d", true)).await.unwrap();
    let row = report
        .rows
        .iter()
        .find(|row| row.harp == HARP && row.action == "purge")
        .cloned()
        .unwrap();
    assert_eq!(row.verdict, "held");
    assert!(row.reason.contains("retention"), "{}", row.reason);
    assert!(f.places.held().destroyed.is_empty());
}

// A refusal the daemon answers is its own message, under its status.
#[test]
fn a_removal_refused_is_a_failed_precondition() {
    let refusal = ApiRefusal::of(&v1::RemovalRefusal::default());
    assert_eq!(
        refusal.code,
        <v1::RemovalRefusal as daemon_proto::Refusal>::REFUSAL_STATUS
    );
}

// A blank configured retention is none: the default 30 days applies, never
// a refusal of the blank as no age.
#[tokio::test]
async fn a_blank_configured_retention_is_the_default() {
    let f = finished(HARP, transcript_and_notes(HARP)).await;
    f.places.held().retention = Some("  ".to_string());
    let done = f.api.delete_session(delete(NAME, true)).await.unwrap();
    assert_eq!(done.applied, Some(true));
    assert_eq!(f.places.held().destroyed, [HARP]);
}

// A sweep that names no project reads the configuration of its parent,
// when that is one project.
#[tokio::test]
async fn a_sweep_of_one_projects_parent_reads_its_configuration() {
    let f = finished(HARP, transcript_and_notes(HARP)).await;
    f.places.held().purge_age = Some("30d".to_string());
    let report = f
        .api
        .sweep_sessions(v1::SweepSessionsRequest {
            project: String::new(),
            ..sweep("", true)
        })
        .await
        .unwrap();
    assert_eq!(f.places.held().purge_age_asked, [ROOT]);
    assert_eq!(report.counts.unwrap().done, 1);
}

// A sweep of every project names no one configuration: with no age given,
// it reads none and holds every purge.
#[tokio::test]
async fn a_sweep_of_every_project_with_no_age_holds_every_purge() {
    let f = finished(HARP, transcript_and_notes(HARP)).await;
    f.places.held().purge_age = Some("30d".to_string());
    let report = f
        .api
        .sweep_sessions(v1::SweepSessionsRequest {
            parent: "projects/-".to_string(),
            project: "projects/-".to_string(),
            ..sweep("", true)
        })
        .await
        .unwrap();
    assert!(f.places.held().purge_age_asked.is_empty());
    assert!(report.purge_cutoff.is_empty());
    assert!(f.places.held().destroyed.is_empty());
}
