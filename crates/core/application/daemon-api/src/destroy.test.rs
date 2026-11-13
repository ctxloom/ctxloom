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
    /// The sessions whose directory was destroyed, in order.
    destroyed: Vec<String>,
    fail_destroy: bool,
    /// The verdicts of a session's worktrees, planned and applied.
    planned: Option<Vec<Verdict>>,
    applied: Option<Vec<Verdict>>,
    /// What the worktrees were asked: the directory, the harp, the apply,
    /// and whether the caller's environment came with it.
    worktrees_asked: Vec<(String, String, bool, bool)>,
    /// The configured purge age, and the roots it was read from.
    purge_age: Option<String>,
    settings_asked: Vec<String>,
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
    fn last_active(&mut self, harp: &str) -> Result<Option<Timestamp>, angzarr_diagnostic::Report> {
        Ok(self.0.held().last_active.get(harp).copied())
    }

    fn survey(&mut self, harp: &str) -> Result<Vec<FileItem>, angzarr_diagnostic::Report> {
        Ok(self.0.held().survey.get(harp).cloned().unwrap_or_default())
    }

    fn destroy_session(&mut self, harp: &str) -> Result<u64, angzarr_diagnostic::Report> {
        let mut held = self.0.held();
        if held.fail_destroy {
            return Err(angzarr_diagnostic::Report::new("x-destroy-failed"));
        }
        held.destroyed.push(harp.to_string());
        Ok(held
            .survey
            .get(harp)
            .map_or(0, |files| files.iter().map(|f| f.bytes).sum()))
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

struct Settings(Arc<FakePlaces>);

impl SweepConfig for Settings {
    fn purge_age(&mut self) -> Result<Option<String>, reclaim_application::Failure> {
        Ok(self.0.held().purge_age.clone())
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
        Box::new(Settings(Arc::clone(self)))
    }

    fn now(&self) -> Timestamp {
        at(NOW)
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

/// A run stream of the session's first incarnation, with one event.
async fn a_run_of(world: &World, root: uuid::Uuid) {
    use angzarr_embedded::Write;
    let page = angzarr_embedded::v1::EventPage {
        sequence: 0,
        body: Some(angzarr_embedded::v1::event_page::Body::Event(
            angzarr_embedded::Any {
                type_url: "type.googleapis.com/ctxloom.runs.v1.RunExited".to_string(),
                value: Vec::new(),
            },
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
            cwd: ROOT.to_string(),
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
        env: Some(v1::EnvVars {
            vars: vec![v1::EnvVar {
                name: "GIT_TRACE".to_string(),
                value: "0".to_string(),
            }],
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
    assert!(!plan.applied);
    let classes: Vec<(&str, i32)> = plan
        .files
        .iter()
        .map(|file| (file.rel.as_str(), file.class))
        .collect();
    assert_eq!(
        classes,
        [
            ("design-notes.md", v1::FileClass::Authored as i32),
            ("native/history.jsonl", v1::FileClass::Machine as i32),
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
    a_run_of(&f.world, run).await;
    let events = head(&f.world, sessions::SESSION_DOMAIN, session).await
        + head(&f.world, runs::RUN_DOMAIN, run).await;
    let done = f.api.delete_session(delete(NAME, true)).await.unwrap();
    assert!(done.applied);
    assert_eq!(done.events_destroyed, events);
    assert_eq!(done.bytes_freed, 7);
    assert_eq!(head(&f.world, sessions::SESSION_DOMAIN, session).await, 0);
    assert_eq!(head(&f.world, runs::RUN_DOMAIN, run).await, 0);
    assert_eq!(f.places.held().destroyed, [HARP]);
    let gone = f
        .api
        .get_session(v1::GetSessionRequest {
            name: NAME.to_string(),
            env: None,
        })
        .await;
    let (_, missing): (_, v1::SessionNotFound) = refused(gone);
    assert_eq!(missing.name, NAME);
}

/// The refusal a delete answered, decoded.
fn removal_refused(result: Result<v1::DeleteSessionResponse, ApiError>) -> v1::RemovalRefused {
    let (code, refused): (_, v1::RemovalRefused) = refused(result);
    assert_eq!(
        code,
        <v1::RemovalRefused as daemon_proto::Refusal>::REFUSAL_STATUS
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
        assert_eq!(refused.reason, v1::RemovalRefusal::NotProvenDead as i32);
        assert!(refused.detail.contains(says), "{}", refused.detail);
        let plan = refused.plan.expect("the plan");
        assert_eq!(plan.files.len(), 2, "the plan is carried");
        assert_eq!(plan.authored, ["design-notes.md"]);
        assert!(!plan.applied);
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
    assert!(done.applied);
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
    assert_eq!(refused.reason, v1::RemovalRefusal::Kept as i32);

    let f = finished(HARP, transcript_and_notes(HARP)).await;
    f.places
        .held()
        .last_active
        .insert(HARP.to_string(), at(NOW - 3600));
    let refused = removal_refused(f.api.delete_session(delete(NAME, true)).await);
    assert_eq!(refused.reason, v1::RemovalRefusal::Retained as i32);
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
        assert_eq!(refused.reason, v1::RemovalRefusal::WorktreesKeep as i32);
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
    assert!(done.applied);
    let worktrees = done.worktrees.unwrap();
    assert_eq!((worktrees.reaped, worktrees.applied), (1, true));
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

// Ruling 94b: the streams go before the directory, so a directory that
// cannot be removed leaves a session nothing lists.
#[tokio::test]
async fn the_streams_go_before_the_directory() {
    let f = finished(HARP, transcript_and_notes(HARP)).await;
    f.places.held().fail_destroy = true;
    let failed = f.api.delete_session(delete(NAME, true)).await;
    assert!(matches!(failed, Err(ApiError::Failed { .. })), "{failed:?}");
    let (session, _) = roots(NAME);
    assert_eq!(head(&f.world, sessions::SESSION_DOMAIN, session).await, 0);
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
    // The flag given, the configuration is not read.
    assert!(f.places.held().settings_asked.is_empty());
}

// The purge age a sweep is not given is its project's configured one, read
// from its REGISTERED root; one that is neither an age nor a date is
// refused, naming the key.
#[tokio::test]
async fn a_sweep_reads_the_configured_age_from_the_registered_root() {
    let f = finished(HARP, transcript_and_notes(HARP)).await;
    f.places.held().purge_age = Some("30d".to_string());
    let report = f.api.sweep_sessions(sweep("", true)).await.unwrap();
    assert_eq!(f.places.held().settings_asked, [ROOT]);
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

// A refusal the daemon answers is its own message, under its status.
#[test]
fn a_removal_refused_is_a_failed_precondition() {
    let refusal = ApiRefusal::of(&v1::RemovalRefused::default());
    assert_eq!(
        refusal.code,
        <v1::RemovalRefused as daemon_proto::Refusal>::REFUSAL_STATUS
    );
}
