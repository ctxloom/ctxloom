//! A change is reacted to (step 10c U13, G5-G7; protos.md §2, §3).
//!
//! A change is seen when something needs the tree (owner, 2026-11-18, no
//! polling: observe on need, superseding G5(b)): a read, a preview or a
//! write, or a hook event of a session in it. No round, timer or watcher
//! observes a tree. A hook's observation trusts a scan of the last
//! [`HOOK_TRUST_SECONDS`]; every read, preview and write scans again
//! (through the scanner's stat memo).
//!
//! A live session whose loadout a change touched is handed its context
//! re-assembled at its next event (G6/G7, owner): its hook asks
//! ([`Api::redeliver_context`], `RedeliverContext`), the daemon judges
//! what it is owed (`daemon_application::reactions::owed`, pure, on the
//! record alone), re-assembles it as `context assemble` would for its
//! loadout, frames it (`run_launcher::redelivered_context`), hands it to
//! the session's engine (`run_ports::Engine::deliver_context`, the one
//! polymorphic interface), and records it in the session's record by size
//! and SHA-256 (`RecordContextRedelivered`, keyed by the change's cause,
//! so a second hook of one change is handed nothing). An engine that
//! cannot carry it at this hook records nothing: the session stays owed.

use std::sync::Arc;

use angzarr_embedded::v1::{Cover, DeliveryOutcome, Uuid};
use daemon_proto::v1;
use daemon_proto::Carries as _;
use run_ports::{ContextDelivery, Edge};
use session_index::SessionRecord;
use sessions::SessionName;
use sha2::{Digest, Sha256};

use crate::server::Api;
use crate::{ApiError, ApiRefusal};

/// How long a hook's re-injection trusts a tree's last scan (owner,
/// 2026-11-18): five minutes.
pub const HOOK_TRUST_SECONDS: i64 = 300;

/// The engines a re-assembled context is handed through, by name (step
/// 10c U13): the daemon's own.
pub trait Deliveries: Send + Sync {
    fn engine(&self, name: &str) -> Option<Arc<dyn run_ports::Engine>>;
}

/// The characters a hook's context may take, as its request asks: 0 is
/// the whole hook budget, and none is more.
pub(crate) fn budget_of(asked: u32) -> usize {
    match usize::try_from(asked).unwrap_or(usize::MAX) {
        0 => run_launcher::HOOK_CONTEXT_BUDGET,
        budget => budget.min(run_launcher::HOOK_CONTEXT_BUDGET),
    }
}

/// A library's changes as a watcher drains them (step 10c G7): each call
/// answers those recorded since the last, oldest first, none when none
/// was. It is called when its [`Wake`] is (the catch-up's signal, step 10c
/// U13 rework: no poll, no log).
pub type LibraryPoll = Box<dyn FnMut() -> Vec<v1::LibraryChange> + Send>;

/// What wakes a watcher once a change of the library is recorded.
pub type Wake = library_checkout::Wake;

/// Whether a watcher of `parent` (`projects/{p}`, or `users/me`) is told
/// of the tree `tree`'s changes: every checkout of the project, or the
/// home's library.
pub(crate) fn watches(parent: &str, tree: &str) -> bool {
    match parent {
        "users/me" => tree == "users/me/library",
        project => tree
            .strip_prefix(project)
            .is_some_and(|rest| rest.starts_with("/checkouts/")),
    }
}

/// `recorded` as the thin notice a watcher is told (protos.md §4.1).
pub(crate) fn notice_of(recorded: &library_checkout::Recorded) -> v1::LibraryChange {
    use v1::library_change::Cause;
    let (cause, request_id, verb) = match &recorded.intended {
        Some((request, verb)) => (Cause::Intended, request.clone(), verb.clone()),
        None => (Cause::External, String::new(), String::new()),
    };
    v1::LibraryChange {
        tree: recorded.tree.clone(),
        cause: cause as i32,
        request_id,
        verb,
        changes: recorded
            .changes
            .iter()
            .map(|changed| v1::ChangeSummary {
                path: changed.path.clone(),
                status: changed.status.clone(),
                from_path: changed.from_path.clone(),
            })
            .collect(),
        observed_at: Some(daemon_proto::google::protobuf::Timestamp {
            seconds: recorded.recorded_at.0,
            nanos: recorded.recorded_at.1,
        }),
        sequence: recorded.sequence,
    }
}

impl<E: Edge> Api<E> {
    /// `WatchLibrary` (G7): the changes of the trees `request.parent` names
    /// recorded from now on, drained each time `wake` is called; a daemon
    /// that observes no library fails it. Never content.
    pub fn watch_library(
        &self,
        request: &v1::WatchLibraryRequest,
        wake: Wake,
    ) -> Result<LibraryPoll, ApiError> {
        let (observations, _) = self.observing.as_ref().ok_or_else(|| ApiError::Failed {
            detail: "this daemon observes no library".to_string(),
        })?;
        let parent = request.parent.clone();
        if parent != "users/me" && sessions::ProjectName::parse(&parent).is_err() {
            return Err(ApiError::from(ApiRefusal::of(&v1::NameInvalid {
                name: parent,
                reason: "shape".to_string(),
            })));
        }
        let heard = observations.checkouts().checkouts().watch(wake);
        Ok(Box::new(move || {
            heard
                .try_iter()
                .filter(|recorded| watches(&parent, &recorded.tree))
                .map(|recorded| notice_of(&recorded))
                .collect()
        }))
    }

    /// `RedeliverContext`: what the live session `request.name` names is
    /// owed, COLLECTED at its engine's hook `request.carried_by` (step 10c
    /// U13 rework): its trees observed first (a hook event is a need), the
    /// loop's next round awaited (whose OwedContexts handler records what
    /// the observation owes), then every change its index holds owed, in
    /// one re-assembly, handed and recorded as covering the positions read
    /// before it was assembled, so a change recorded meanwhile stays owed.
    /// Nothing when it is owed nothing, has no loadout recorded, or its
    /// engine cannot carry it at this hook (it stays owed). A session the
    /// daemon does not know is refused `SessionNotFound`.
    pub async fn redeliver_context(
        &self,
        request: v1::RedeliverContextRequest,
    ) -> Result<v1::RedeliveredContext, ApiError> {
        let none = v1::RedeliveredContext::default();
        let (Some(deliveries), Some(_)) = (self.deliveries.as_ref(), self.observing.as_ref())
        else {
            return Ok(none);
        };
        let name = SessionName::parse(&request.name).map_err(|_| {
            ApiError::from(ApiRefusal::of(&v1::NameInvalid {
                name: request.name.clone(),
                reason: "shape".to_string(),
            }))
        })?;
        let known = |api: &Self| {
            api.parts
                .index
                .index()
                .find_by_id(&name.uid())
                .ok_or_else(|| {
                    ApiError::from(ApiRefusal::of(&v1::SessionNotFound {
                        name: request.name.clone(),
                    }))
                })
        };
        let record = known(self)?;
        let parent = name.project().to_string();
        let cwd = match request.caller_cwd() {
            "" => record.project_dir.clone(),
            cwd => cwd.to_string(),
        };
        if record.loadout.is_none() {
            return Ok(none);
        }
        // A hook event is a need: the session's trees observed (trusting a
        // scan of the last five minutes); what that owes is recorded by the
        // next round's handler, and collected at a later hook. No round is
        // awaited: what is owed is read from the projection as it is.
        self.session_trees(&parent, &cwd, request.caller_env())
            .await?;
        let Some(loadout) = record.loadout.clone() else {
            return Ok(none);
        };
        if record
            .context
            .as_ref()
            .is_none_or(|handed| handed.owed.is_empty())
        {
            return Ok(none);
        }
        let Some(engine) = deliveries.engine(&record.engine) else {
            return Ok(none);
        };
        let env = request.caller_env().cloned();
        let budget = budget_of(request.budget);
        let reassemble = || self.reassembled(&loadout, (parent, cwd.clone()), env.clone());
        let fragments =
            |owed: Vec<(String, String)>| self.changed_fragments(owed, &cwd, env.clone());
        let collected = self
            .collect(
                &record,
                (engine.as_ref(), &request.carried_by, budget),
                reassemble,
                fragments,
            )
            .await?;
        Ok(
            collected.map_or(none, |(context, changed)| v1::RedeliveredContext {
                context,
                changed,
            }),
        )
    }

    /// The fragments of `owed` (each its tree and path) as the library has
    /// them now, each whole: a path that is no fragment's, or a fragment no
    /// longer there, is left out.
    async fn changed_fragments(
        &self,
        owed: Vec<(String, String)>,
        cwd: &str,
        env: Option<v1::EnvVars>,
    ) -> Vec<run_launcher::ChangedFragment> {
        let mut fragments = Vec::new();
        for (tree, path) in owed {
            let Some(name) = daemon_application::reactions::fragment_name(&tree, &path) else {
                continue;
            };
            let request = v1::GetFragmentRequest {
                name: name.clone(),
                caller: Some(v1::Caller {
                    cwd: cwd.to_string(),
                    env: env.clone(),
                    globals: Some(v1::Globals {
                        degraded: true,
                        ..v1::Globals::default()
                    }),
                    ..v1::Caller::default()
                }),
                ..v1::GetFragmentRequest::default()
            };
            let read = self
                .library_read(&request, |library, flags, parent| {
                    library_api::reads::get_fragment(library, flags, parent, &request)
                })
                .await;
            if let Ok(read) = read {
                fragments.push(run_launcher::ChangedFragment {
                    name,
                    body: read.answer.body,
                });
            }
        }
        fragments
    }

    /// Everything `record`'s session is owed, collected: what the delivery
    /// covers read BEFORE `reassemble` runs (a change recorded while it is
    /// assembled or handed is past it, and stays owed), the context framed
    /// within `budget` characters (over it, only the changed `fragments`,
    /// asked only then, else a pointer: `run_launcher::redelivered_within`),
    /// handed to `engine` at `carried_by`, and recorded keyed by the
    /// positions. The text and what changed; none when nothing is owed, the
    /// budget leaves no room, the engine cannot carry it here, or another
    /// hook was handed it first.
    pub(crate) async fn collect<F, A, G, B>(
        &self,
        record: &SessionRecord,
        (engine, carried_by, budget): (&dyn run_ports::Engine, &str, usize),
        reassemble: F,
        fragments: G,
    ) -> Result<Option<(String, Vec<String>)>, ApiError>
    where
        F: FnOnce() -> A,
        A: std::future::Future<Output = Result<String, ApiError>>,
        G: FnOnce(Vec<(String, String)>) -> B,
        B: std::future::Future<Output = Vec<run_launcher::ChangedFragment>>,
    {
        let Some(handed) = record
            .context
            .as_ref()
            .filter(|handed| !handed.owed.is_empty())
        else {
            return Ok(None);
        };
        let covered = handed.owed_positions();
        let changed = handed.owed_bundles();
        let context = reassemble().await?;
        let whole = run_launcher::redelivered_context(&changed, &context);
        let framed = match whole.chars().count() <= budget {
            true => whole,
            false => {
                let changed_fragments = fragments(handed.owed_paths()).await;
                run_launcher::redelivered_within(&changed, &context, &changed_fragments, budget)
            }
        };
        if framed.is_empty() {
            return Ok(None);
        }
        let ContextDelivery::HookContext(text) = engine.deliver_context(carried_by, &framed) else {
            return Ok(None);
        };
        let fact = sessions::RecordContextRedelivered {
            bundles: changed.clone(),
            cause: sessions::positions_text(&covered),
            size: u64::try_from(text.len()).unwrap_or(u64::MAX),
            sha256: hex::encode(Sha256::digest(text.as_bytes())),
            carried_by: carried_by.to_string(),
            covered,
        };
        if !self.redelivered(record, &fact).await? {
            // Another hook of the same batch was handed it first.
            return Ok(None);
        }
        Ok(Some((text, changed)))
    }

    /// The trees a session at `cwd` of the project `parent` draws on (its
    /// checkout's and the home's), observed as a hook observes them: each
    /// scanned again only past [`HOOK_TRUST_SECONDS`].
    async fn session_trees(
        &self,
        parent: &str,
        cwd: &str,
        env: Option<&v1::EnvVars>,
    ) -> Result<(), ApiError> {
        let (observations, observers) =
            self.observing.as_ref().ok_or_else(|| ApiError::Failed {
                detail: "this daemon observes no library".to_string(),
            })?;
        let (_, project) = self.resolve(parent, cwd, env, None).await?;
        observations
            .observe_trees_trusting(
                observers.as_ref(),
                project
                    .as_ref()
                    .map(|(name, root)| (name.clone(), root.as_str())),
                HOOK_TRUST_SECONDS,
            )
            .await
            .map_err(|library_api::ReadError::Failed(failure)| {
                ApiError::diagnosed(failure.as_ref())
            })
    }

    /// `loadout`'s context, assembled at `cwd` of the project `parent` as
    /// `context assemble` would assemble it (degraded: a config a strict
    /// load refuses must not silence a hook), its premise index rendered
    /// for a launch.
    async fn reassembled(
        &self,
        loadout: &sessions::SessionLoadout,
        (parent, cwd): (String, String),
        env: Option<v1::EnvVars>,
    ) -> Result<String, ApiError> {
        let mut assembled = self
            .assemble_context(v1::AssembleContextRequest {
                name: parent,
                agent: match loadout.profiles.is_empty() {
                    true => loadout.agent.clone(),
                    false => String::new(),
                },
                profiles: loadout.profiles.clone(),
                caller: Some(v1::Caller {
                    cwd,
                    env,
                    globals: Some(v1::Globals {
                        degraded: true,
                        ..v1::Globals::default()
                    }),
                    ..v1::Caller::default()
                }),
                ..v1::AssembleContextRequest::default()
            })
            .await?;
        crate::render_premise_index(&mut assembled, crate::Shown::Launch);
        Ok(assembled.context)
    }

    /// `fact` recorded on `record`'s session, keyed by the positions it
    /// covers: whether this was its first delivery (a batch handed again is
    /// a DUPLICATE).
    async fn redelivered(
        &self,
        record: &SessionRecord,
        fact: &sessions::RecordContextRedelivered,
    ) -> Result<bool, ApiError> {
        let root = uuid::Uuid::parse_str(&record.id).map_err(|_| ApiError::Failed {
            detail: format!("the session id {} is not a UUID", record.id),
        })?;
        let cover = Cover {
            domain: sessions::SESSION_DOMAIN.to_string(),
            root: Some(Uuid::from_bytes(*root.as_bytes())),
            edition: None,
        };
        let key = sessions::redelivery_key(&record.id, &sessions::positions_text(&fact.covered));
        let answered = self
            .parts
            .edge
            .handle(sessions::fact_book(cover, fact, &key))
            .await
            .map_err(ApiError::failed)?;
        Ok(answered.delivery != DeliveryOutcome::Duplicate)
    }
}

#[cfg(test)]
#[path = "reactions.test.rs"]
mod tests;
