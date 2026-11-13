//! The daemon's destroy (slice 2 step 6, owner 2026-11-12): `DeleteSession`
//! (`session remove`) and `SweepSessions` (`session sweep`), run INSIDE the
//! daemon, the one owner of the store (T1, ruling 131), so no client opens
//! the store to destroy a session.
//!
//! A removal is reclaim-application's [`reclaim_application::remove`], the
//! one safe order both verbs share: the session's scratch worktrees
//! classified first, the whole removal refused if one is not safe, else the
//! worktrees removed, then the session destroyed. What it takes is
//! [`Removing`], over the session index the daemon keeps warm, the daemon's
//! one store ([`Purges`]) and the session's own project ([`Places`]: its
//! files and lock, its worktrees, its configuration read from its
//! REGISTERED root). A refused removal answers [`v1::RemovalRefused`],
//! which carries its plan, so the client says what was not done.

use angzarr_embedded::angzarr_router::hpb::ProjectRequest;
use angzarr_embedded::v1::{Cover, EventBook, Uuid};
use angzarr_embedded::{Failure, ProjectorHandler, Timestamp};
use daemon_proto::v1;
use isolation_application::{IsolationError, Report, Verdict as Worktree, WorktreeUse};
use reclaim_application::{purge_cutoff, ReclaimError, Removals, SweepConfig, SweepRequest};
use run_ports::Edge;
use session_index::{Retained, Retention, SessionRecord, View};
use session_ports::{FileItem, SessionFiles};
use sessions::{ProjectName, SessionName, SessionRef, SESSION_DOMAIN};
use sessions_application::{
    AppError, Class, FilePlan, PlanItem, Refusal, Removal, RemoveRequest, State,
    DEFAULT_RETENTION_DAYS, REMOVE_VERB,
};

use crate::refusal::{ApiError, ApiRefusal};
use crate::server::{invalid, Answer, Api, EVERY_PROJECT};

/// A project's places a removal reaches, by its directory (the root's):
/// its sessions' files and locks, its scratch worktrees, its configuration.
pub trait Places: Send + Sync {
    /// The session directories of the project at `project_dir`.
    fn files(&self, project_dir: &str) -> Box<dyn SessionFiles + Send>;
    /// The scratch worktrees of the project at `project_dir`, git run in the
    /// caller's environment `env` (S7).
    fn worktrees(&self, project_dir: &str, env: &[(String, String)])
        -> Box<dyn WorktreeUse + Send>;
    /// The configuration of the project registered at `root`, its
    /// overrides the caller's environment `env`.
    fn settings(&self, root: &str, env: &[(String, String)]) -> Box<dyn SweepConfig + Send>;
    /// Now: the retention's and the purge age's instant.
    fn now(&self) -> Timestamp;
}

/// The daemon's one store, as a destroy purges a stream of it whole.
pub trait Purges: Send + Sync {
    /// Purge the stream `domain`/`root` at the head just read (retried on a
    /// conflict); how many events it removed.
    fn purge<'a>(&'a self, domain: &'a str, root: Uuid) -> Answer<'a, u64>;
}

/// What a removal takes, for the sessions `records`: the daemon's warm
/// index, its one store and each session's own project.
pub(crate) struct Removing<'a, E> {
    api: &'a Api<E>,
    records: Vec<SessionRecord>,
    env: Vec<(String, String)>,
}

impl<E: Edge> Removing<'_, E> {
    /// The record of `harp` of the project `project` names (any, when none).
    fn find(&self, project: Option<&str>, harp: &str) -> Result<SessionRecord, AppError> {
        self.records
            .iter()
            .find(|r| r.harp == harp && project.is_none_or(|id| r.project_id == id))
            .cloned()
            .ok_or_else(|| AppError::NoSession {
                harp: harp.to_string(),
            })
    }

    /// Before a destroy's purge (rulings 94, 98; D28 K3, M3): a purge emits
    /// no event, so each child `record` holds is closed and its owner
    /// forgets it first, each a keyed command through the core. A refusal
    /// (a child already destroyed holds no session) is passed over.
    async fn unlink(&self, record: &SessionRecord) -> Result<(), AppError> {
        let target = SessionRef {
            project_id: record.project_id.clone(),
            session: record.id.clone(),
        };
        let plan = session_policies::destroy(&target, record.owner.as_ref(), &record.children);
        let books: Vec<_> = plan
            .close
            .iter()
            .map(|keyed| keyed.book())
            .chain(plan.forget.iter().map(|keyed| keyed.book()))
            .collect();
        for book in books {
            match self.api.parts.edge.handle(book).await {
                Ok(_) | Err(Failure::Rejected(_)) => {}
                Err(other) => return Err(app_failed(&format!("{other:?}"))),
            }
        }
        Ok(())
    }

    /// Destroy `record` (owner rulings 94, 94b, 98): its children closed and
    /// its owner told, its stream and each of its runs' (UUIDv5(session,
    /// 1..=n)) purged from the one store, then its directory and lock
    /// removed, and the warm index told the stream is gone. The streams go
    /// first: a directory left by a failure after them names a session
    /// nothing lists. The name's claim is kept (94c).
    async fn destroy(
        &self,
        record: &SessionRecord,
        files: &mut (dyn SessionFiles + Send),
        mut removal: Removal,
    ) -> Result<Removal, AppError> {
        let session = uuid::Uuid::parse_str(&record.id)
            .map_err(|e| app_failed(&format!("the session id {}: {e}", record.id)))?;
        let mut roots = vec![(SESSION_DOMAIN, Uuid::from_bytes(session.into_bytes()))];
        for incarnation in 1..=record.incarnation.max(1) {
            roots.push((runs::RUN_DOMAIN, runs::run_root(&record.id, incarnation)));
        }
        self.unlink(record).await?;
        for (domain, root) in roots {
            removal.events_destroyed += self
                .api
                .parts
                .purges
                .purge(domain, root)
                .await
                .map_err(|e| app_failed(&e.to_string()))?;
        }
        self.forget(&record.id).await;
        removal.files.bytes_freed = files
            .destroy_session(&record.harp)
            .map_err(AppError::Files)?;
        removal.applied = true;
        Ok(removal)
    }

    /// Tell the warm read models the session's stream is gone (a book with
    /// no page at sequence 0, as the projector port says a purge).
    async fn forget(&self, id: &str) {
        let Ok(root) = uuid::Uuid::parse_str(id) else {
            return;
        };
        let request = ProjectRequest {
            events: Some(EventBook {
                cover: Some(Cover {
                    domain: SESSION_DOMAIN.to_string(),
                    root: Some(Uuid::from_bytes(root.into_bytes())),
                    edition: None,
                }),
                ..EventBook::default()
            }),
        };
        // A model that cannot fold it is caught up from the store anyway.
        let _ = self.api.parts.index.dispatch(&request).await;
        let _ = self.api.parts.pending.dispatch(&request).await;
    }
}

impl<E: Edge> Removals for Removing<'_, E> {
    async fn remove(&mut self, request: RemoveRequest) -> Result<Removal, AppError> {
        let record = self.find(request.project.as_deref(), &request.harp)?;
        let mut files = self.api.parts.places.files(&record.project_dir);
        let plan = plan(files.survey(&record.harp).map_err(AppError::Files)?);
        let last_active = files.last_active(&record.harp).map_err(AppError::Files)?;
        let lock = self
            .api
            .parts
            .states
            .state(&record.project_dir, &record.harp);
        let now = self.api.parts.places.now();
        let removal = Removal {
            harp: record.harp.clone(),
            applied: false,
            files: plan,
            events_destroyed: 0,
            refusal: refusal(&record, lock, last_active, now, request.even_if_live),
            dead_letters: Vec::new(),
        };
        if removal.refusal.is_some() || !request.yes {
            return Ok(removal);
        }
        self.destroy(&record, files.as_mut(), removal).await
    }

    fn worktrees(
        &mut self,
        project: Option<&str>,
        harp: &str,
        apply: bool,
    ) -> Result<Report, IsolationError> {
        let Ok(record) = self.find(project, harp) else {
            return Err(IsolationError::NoSession {
                harp: harp.to_string(),
            });
        };
        self.api
            .parts
            .places
            .worktrees(&record.project_dir, &self.env)
            .worktrees(Some(harp), apply)
    }
}

/// What removing a session destroys of its files: the whole directory, the
/// machine-written conversation (the engine's native history, and a
/// transcript an older ctxloom left) and everything else, each named by its
/// class.
fn plan(survey: Vec<FileItem>) -> FilePlan {
    let destroy = survey
        .into_iter()
        .map(|file| {
            let machine = file.rel.starts_with("transcripts/") || file.rel.starts_with("native/");
            PlanItem {
                path: file.path,
                rel: file.rel,
                class: if machine {
                    Class::Machine
                } else {
                    Class::Authored
                },
                bytes: file.bytes,
            }
        })
        .collect();
    FilePlan {
        destroy,
        bytes_freed: 0,
    }
}

/// Why `record` may not be removed, if it may not: its lock (`lock`, the
/// daemon's reading of it) does not prove its owner dead, unless
/// `even_if_live`; then its transcript's retention, from the newest of its
/// engine's native history (`native`) and its own capture.
fn refusal(
    record: &SessionRecord,
    lock: State,
    native: Option<Timestamp>,
    now: Timestamp,
    even_if_live: bool,
) -> Option<Refusal> {
    let harp = record.harp.clone();
    if lock != State::Ended && !even_if_live {
        let reason = match lock {
            State::Live => "its lock is held: the owner is alive and running",
            _ => "it has no lock to prove the owner ended",
        };
        return Some(Refusal::NotProvenDead {
            harp,
            reason: reason.to_string(),
            verb: REMOVE_VERB.to_string(),
        });
    }
    let last_active = [native, record.last_captured]
        .into_iter()
        .flatten()
        .max_by_key(|at| (at.seconds, at.nanos));
    let retention = Retention {
        cutoff: Some(session_index::stamp::days_before(
            now,
            DEFAULT_RETENTION_DAYS,
        )),
    };
    match retention.retains(Some(record), last_active) {
        None => None,
        Some(Retained::Kept) => Some(Refusal::Kept { harp }),
        Some(Retained::Recent { ended }) => Some(Refusal::Retained {
            harp,
            detail: match ended {
                Some(at) => format!("it ended {}", session_index::stamp::rfc3339(at)),
                None => "nothing records when it ended".to_string(),
            },
        }),
        Some(Retained::NoBound) => Some(Refusal::Retained {
            harp,
            detail: "no retention is configured".to_string(),
        }),
    }
}

fn app_failed(detail: &str) -> AppError {
    AppError::Failed(
        angzarr_diagnostic::Report::new("sessions-application-gave-up")
            .arg("attempts", 1_i64)
            .arg("error", detail.to_string()),
    )
}

/// A reclaim use case's failure, as the API answers it.
fn reclaim_failed(error: &ReclaimError) -> ApiError {
    ApiError::Failed {
        detail: error.to_string(),
    }
}

/// The caller's environment (S7), as name and value pairs.
fn env_of(env: Option<&v1::EnvVars>) -> Vec<(String, String)> {
    env.map(|env| {
        env.vars
            .iter()
            .map(|var| (var.name.clone(), var.value.clone()))
            .collect()
    })
    .unwrap_or_default()
}

impl<E: Edge> Api<E> {
    /// `DeleteSession` (`session remove`, a `session-destroy` build's, M5):
    /// the session by its resource name, removed whole or not at all
    /// ([`reclaim_application::remove`]); `validate_only` is the plan.
    /// Refused, it answers [`v1::RemovalRefused`] carrying the plan.
    pub async fn delete_session(
        &self,
        request: v1::DeleteSessionRequest,
    ) -> Result<v1::DeleteSessionResponse, ApiError> {
        let name = SessionName::parse(&request.name)
            .map_err(|rejection| invalid(&request.name, &rejection))?;
        self.parts.hosting.fresh().await;
        let record = self.parts.index.index().find_by_id(&name.uid());
        let record = record.ok_or_else(|| {
            ApiError::from(ApiRefusal::of(&v1::SessionNotFound {
                name: request.name.clone(),
            }))
        })?;
        let mut removing = Removing {
            api: self,
            records: vec![record.clone()],
            env: env_of(request.env.as_ref()),
        };
        let done = reclaim_application::remove(
            &mut removing,
            RemoveRequest {
                project: Some(record.project_id.clone()),
                harp: record.harp.clone(),
                yes: !request.validate_only,
                even_if_live: request.even_if_live,
            },
        )
        .await
        .map_err(|error| reclaim_failed(&error))?;
        let plan = deletion(&request.name, &done.removal, done.worktrees.as_ref());
        let refused = |reason: v1::RemovalRefusal, detail: String, counts: (u64, u64)| {
            ApiError::from(ApiRefusal::of(&v1::RemovalRefused {
                name: request.name.clone(),
                reason: reason as i32,
                detail,
                plan: Some(plan.clone()),
                spared: counts.0,
                skipped: counts.1,
            }))
        };
        match (&done.removal.refusal, &done.kept) {
            (Some(Refusal::NotProvenDead { reason, .. }), _) => Err(refused(
                v1::RemovalRefusal::NotProvenDead,
                reason.clone(),
                (0, 0),
            )),
            (Some(Refusal::Kept { .. }), _) => {
                Err(refused(v1::RemovalRefusal::Kept, String::new(), (0, 0)))
            }
            (Some(Refusal::Retained { detail, .. }), _) => Err(refused(
                v1::RemovalRefusal::Retained,
                detail.clone(),
                (0, 0),
            )),
            (
                None,
                Some(IsolationError::KeepsSession {
                    spared, skipped, ..
                }),
            ) => Err(refused(
                v1::RemovalRefusal::WorktreesKeep,
                String::new(),
                (*spared, *skipped),
            )),
            (None, Some(other)) => Err(ApiError::Failed {
                detail: other.to_string(),
            }),
            (None, None) => Ok(plan),
        }
    }

    /// `SweepSessions` (`session sweep`, a `session-destroy` build's, M5):
    /// every session under `parent` by the sweep's table, its purges through
    /// the same removal as `DeleteSession`; the purge age the request's, else
    /// the `session_purge_age` of its project, read from its registered
    /// root; `validate_only` is the report.
    pub async fn sweep_sessions(
        &self,
        request: v1::SweepSessionsRequest,
    ) -> Result<v1::SweepSessionsResponse, ApiError> {
        let env = env_of(request.env.as_ref());
        let views: Vec<View> = self
            .under(&request.parent)
            .await?
            .into_iter()
            .map(|(_, view)| view)
            .collect();
        let names: Vec<(String, String)> = {
            let index = self.parts.index.index();
            views
                .iter()
                .map(|view| (view.record.id.clone(), index.name_of(&view.record)))
                .collect()
        };
        let now = self.parts.places.now();
        let flag = Some(request.older_than.as_str()).filter(|age| !age.trim().is_empty());
        // The flag given, the configuration is never read (a broken one
        // cannot stop a sweep that does not need it).
        let mut settings = match flag {
            Some(_) => Box::new(Unconfigured),
            None => self.settings(&request, &env),
        };
        let cutoff = match purge_cutoff(flag, settings.as_mut(), now) {
            Ok(cutoff) => cutoff,
            Err(ReclaimError::BadAge { flag, value, .. }) => {
                return Err(ApiRefusal::of(&v1::AgeInvalid { key: flag, value }).into())
            }
            Err(ReclaimError::Config(failure)) => {
                return Err(ApiRefusal::of(&v1::ConfigUnreadable {
                    root: self.config_root(&request).unwrap_or_default(),
                    detail: failure.to_string(),
                })
                .into())
            }
            Err(other) => return Err(reclaim_failed(&other)),
        };
        let mut removing = Removing {
            api: self,
            records: views.iter().map(|view| view.record.clone()).collect(),
            env,
        };
        let report = reclaim_application::sweep(
            &mut removing,
            &views,
            &SweepRequest {
                purge_cutoff: cutoff,
                yes: !request.validate_only,
            },
        )
        .await
        .map_err(|error| reclaim_failed(&error))?;
        let name_of = |harp: &str| {
            views
                .iter()
                .find(|view| view.record.harp == harp)
                .and_then(|view| names.iter().find(|(id, _)| *id == view.record.id))
                .map(|(_, name)| name.clone())
                .unwrap_or_default()
        };
        let count = |verdict: &str| report.counts.get(verdict).copied().unwrap_or(0);
        Ok(v1::SweepSessionsResponse {
            sessions: report
                .rows
                .iter()
                .filter_map(|row| {
                    let removal = row.removal.as_ref()?;
                    Some(deletion(&name_of(&row.harp), removal, None))
                })
                .collect(),
            rows: report
                .rows
                .iter()
                .map(|row| v1::SweepRow {
                    harp: row.harp.clone(),
                    action: row.action.to_string(),
                    verdict: verdict_word(row.verdict).to_string(),
                    reason: row.reason.clone(),
                    command: row.command.clone(),
                    bytes: row.bytes,
                    authored: row.authored.clone(),
                })
                .collect(),
            purge_cutoff: report.purge_cutoff.clone().unwrap_or_default(),
            counts: Some(v1::SweepCounts {
                done: count("done"),
                planned: count("planned"),
                held: count("held"),
                left: count("left"),
                skipped: count("skipped"),
                failed: count("failed"),
            }),
            ..v1::SweepSessionsResponse::default()
        })
    }

    /// The registered root whose configuration a sweep reads: its
    /// `project`'s, else its parent's when that is one project.
    fn config_root(&self, request: &v1::SweepSessionsRequest) -> Option<String> {
        let project = [request.project.as_str(), request.parent.as_str()]
            .into_iter()
            .find(|name| !name.is_empty() && *name != EVERY_PROJECT)?;
        let harp = ProjectName::parse(project).ok()?.harp().to_string();
        let row = self.parts.index.index().project_named_by(&harp)?;
        Some(row.root).filter(|root| !root.is_empty())
    }

    /// The sweep's configuration: its project's, read from its registered
    /// root; none outside one (the age the request names alone).
    fn settings(
        &self,
        request: &v1::SweepSessionsRequest,
        env: &[(String, String)],
    ) -> Box<dyn SweepConfig + Send> {
        match self.config_root(request) {
            Some(root) => self.parts.places.settings(&root, env),
            None => Box::new(Unconfigured),
        }
    }
}

/// No project's configuration: no purge age.
struct Unconfigured;

impl SweepConfig for Unconfigured {
    fn purge_age(&mut self) -> Result<Option<String>, reclaim_application::Failure> {
        Ok(None)
    }

    fn retention(&mut self) -> Result<Option<String>, reclaim_application::Failure> {
        Ok(None)
    }
}

/// A sweep row's verdict, by its word.
fn verdict_word(verdict: reclaim_application::Verdict) -> &'static str {
    use reclaim_application::Verdict;
    match verdict {
        Verdict::Done => "done",
        Verdict::Planned => "planned",
        Verdict::Held => "held",
        Verdict::Left => "left",
        Verdict::Skipped => "skipped",
        Verdict::Failed => "failed",
    }
}

/// A removal (and its worktrees), as `DeleteSession` answers it.
pub(crate) fn deletion(
    name: &str,
    removal: &Removal,
    worktrees: Option<&Report>,
) -> v1::DeleteSessionResponse {
    v1::DeleteSessionResponse {
        name: name.to_string(),
        authored: removal.authored(),
        harp: removal.harp.clone(),
        applied: removal.applied,
        files: removal
            .files
            .destroy
            .iter()
            .map(|item| v1::RemovalFile {
                path: item.path.clone(),
                rel: item.rel.clone(),
                class: match item.class {
                    Class::Machine => v1::FileClass::Machine,
                    Class::Authored => v1::FileClass::Authored,
                } as i32,
                bytes: item.bytes,
            })
            .collect(),
        bytes_freed: removal.files.bytes_freed,
        events_destroyed: removal.events_destroyed,
        worktrees: worktrees.map(worktree_report),
        ..v1::DeleteSessionResponse::default()
    }
}

/// A worktree leaf's report, as the API says it.
fn worktree_report(report: &Report) -> v1::WorktreeReport {
    v1::WorktreeReport {
        worktrees: report
            .worktrees
            .iter()
            .map(|row| v1::ScratchWorktree {
                harp: row.harp.clone(),
                worktree: row.worktree.clone(),
                path: row.path.clone(),
                owner_state: row.owner_state.to_string(),
                verdict: match row.verdict {
                    Worktree::Reapable => "reapable",
                    Worktree::Reaped => "reaped",
                    Worktree::Spared => "spared",
                    Worktree::Skipped => "skipped",
                }
                .to_string(),
                reason: row.reason.clone(),
            })
            .collect(),
        reaped: report.reaped,
        spared: report.spared,
        skipped: report.skipped,
        applied: report.applied,
    }
}

#[cfg(test)]
#[path = "destroy.test.rs"]
pub(crate) mod tests;
