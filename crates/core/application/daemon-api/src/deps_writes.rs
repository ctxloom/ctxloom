//! The remotes' and the dependencies' writes the daemon serves (step 10c
//! U4; protos.md §1.2, L9). Each is planned where the caller stands: the
//! deps use case run in its directory, in its environment (git on ITS
//! `PATH`, with its `HOME` and credentials, `GIT_*` scrubbed as ever), over
//! a RECORDING store that writes nothing (the wiring's [`DepsPlans`]).
//! What it recorded is then written as the library's writes are:
//!
//! - a remote's write (`remote create|edit|remove|default`) is ONE
//!   `ReplaceFile` of `remotes.yaml`, previewed, intended, made and recorded
//!   as every library write (`Api::carry_out`);
//! - a pull or an upgrade is ONE retryable sequence (`RunSequence`, verb
//!   `deps pull` or `deps upgrade`) on the checkout's stream: a
//!   `CreateDirectory` per pinned commit's tree, made whole by one atomic
//!   operation into the cache and completed by `RecordTreeInstalled` (git's
//!   own tree id, as remote-git reports it); then `lock.yaml` replaced;
//!   then each tree the pull no longer keeps taken into the sequence's own
//!   trash, which its last step purges; then, for a consented refresh
//!   (`deps pull --yes`), each engine file as a foreign step (T26).
//!
//! The lock is written after every tree it names is installed, so a pull
//! killed between its steps never leaves a lock naming a tree that is not
//! there, and its retry (the same request id) resumes at the first step
//! not done; a re-run converges, for a tree already installed whole is a
//! step done.

use daemon_proto::v1;
use library_api::writes::{command_book, step_book, Intended, Lands, Shape, Shaped};
use library_checkout::End;
use library_tree::{
    CreateDirectory, Evidence, ForeignFile, RecordTreeInstalled, ReplaceFile, RunSequence, Step,
};
use run_ports::Edge;

use crate::foreign::{files_of, ForeignPut};
use crate::library_writes::{landing_of, notice_of, Made};
use crate::server::Api;
use daemon_api_contract::LibraryWriteRequest;
use daemon_api_contract::{ApiError, ApiRefusal};

/// The registry's path in its tree.
const REGISTRY: &str = "remotes.yaml";
/// The lock's path in its tree.
const LOCK: &str = "lock.yaml";

/// What a deps write is asked to do.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum DepsAsk {
    /// `deps pull [--force] [--lock=false] [--yes]`.
    Pull { force: bool, lock: bool, yes: bool },
    /// `deps upgrade [reference] [--yes]`.
    Upgrade {
        reference: Option<String>,
        apply: bool,
    },
    /// `remote create <name> <url> [--forge]`.
    CreateRemote {
        name: String,
        url: String,
        forge: String,
    },
    /// `remote edit <remote> [--name] [--url] [--forge]`.
    EditRemote {
        remote: String,
        name: Option<String>,
        url: Option<String>,
        forge: Option<String>,
    },
    /// `remote remove <remote> [--yes]`.
    RemoveRemote { name: String, apply: bool },
    /// `remote default <remote>`, or (none) `--clear`.
    DefaultRemote { name: Option<String> },
}

/// A deps write's answer, as the API says it.
#[derive(Clone, Debug, PartialEq)]
pub enum DepsAnswer {
    Pulled(v1::PullDependenciesResponse),
    Upgraded(v1::UpgradeDependenciesResponse),
    Created(v1::CreateRemoteResponse),
    Edited(v1::UpdateRemoteResponse),
    Removed(v1::DeleteRemoteResponse),
    Defaulted(v1::SetDefaultRemoteResponse),
}

/// A tree a pull installs: its directory below the tree's `.ctxloom`
/// (`cache/bundles/<segments>/<bundle>/<sha>`), its files, and what its
/// `RecordTreeInstalled` says.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct DepsTree {
    pub path: String,
    pub files: Vec<ctxloom_ports::Put>,
    /// The version-less canonical reference it is the tree of.
    pub identity: String,
    pub sha: String,
    /// git's own id of the tree at `sha`, as remote-git reports it.
    pub tree: String,
}

/// What a deps write planned: its answer, and what it would write.
#[derive(Clone, Debug, PartialEq)]
pub struct DepsPlanned {
    pub answer: DepsAnswer,
    /// `remotes.yaml` as it would be left; none where it is unchanged.
    pub registry: Option<Vec<u8>>,
    /// `lock.yaml` as it would be left; none where it is unchanged.
    pub lock: Option<Vec<u8>>,
    /// Each tree installed, in order.
    pub installs: Vec<DepsTree>,
    /// Each installed tree taken away, below the tree's `.ctxloom`.
    pub removed: Vec<String>,
    /// The engine files a consented refresh writes (`deps pull --yes`).
    pub foreign: Vec<ForeignPut>,
}

/// The deps use cases, run where a process at `place` (a directory and an
/// environment), under
/// `flags`, would run them, WRITING NOTHING, their answers' names under
/// `parent`: the wiring's (over a recording store). A use case's own
/// refusal or failure is the call's, so nothing is written.
pub trait DepsPlans: Send + Sync {
    fn plan(
        &self,
        place: (&str, &[(String, String)]),
        flags: &library_application::Flags,
        parent: &str,
        ask: &DepsAsk,
    ) -> Result<DepsPlanned, ApiError>;
}

/// The kill point after a pull's create step `at` (zero-based) of
/// `creates` is recorded done: the first is `tree-installed`, the last
/// `trees-installed` too (atomic_writes.feature's kill points).
pub(crate) fn tree_points(at: usize, creates: usize) -> Vec<&'static str> {
    let mut points = Vec::new();
    if at == 0 {
        points.push("tree-installed");
    }
    if at + 1 == creates {
        points.push("trees-installed");
    }
    points
}

/// A pull's library steps: its trees, its lock's new id and the token it
/// was planned against, and each tree it takes away with its token.
pub(crate) type PullParts<'a> = (&'a [DepsTree], Option<(String, u64)>, &'a [(String, u64)]);

/// A pull's or an upgrade's sequence on `tree` under `verb`: a create
/// step per tree installed (no entries: the cache is presence and
/// identity, never content), then `lock` (`lock.yaml`'s new id, the token
/// it was planned against), then each removed tree into the trash and the
/// purge of the sequence's own slot, then each foreign file (its step
/// path and its new id, listed in the intent).
pub(crate) fn pull_sequence(
    tree: &library_tree::TreeName,
    verb: &str,
    (installs, lock, removed): PullParts<'_>,
    foreign: &[(String, String)],
    evidence: &Evidence,
) -> RunSequence {
    let name = tree.to_string();
    let step = || Step {
        replace: None,
        create: None,
        trash: None,
        r#move: None,
        purge: None,
    };
    let mut steps: Vec<Step> = installs
        .iter()
        .map(|install| Step {
            create: Some(CreateDirectory {
                name: name.clone(),
                verb: verb.to_string(),
                path: install.path.clone(),
                entries: Vec::new(),
                evidence: evidence.clone(),
            }),
            ..step()
        })
        .collect();
    if let Some((new_oid, seen)) = lock {
        steps.push(Step {
            replace: Some(ReplaceFile {
                foreign: false,
                name: name.clone(),
                verb: verb.to_string(),
                path: LOCK.to_string(),
                seen_sequence: seen,
                new_oid,
                evidence: evidence.clone(),
            }),
            ..step()
        });
    }
    for (path, seen) in removed {
        steps.push(Step {
            trash: Some(library_tree::TrashPath {
                name: name.clone(),
                verb: verb.to_string(),
                path: path.clone(),
                seen_sequence: *seen,
                evidence: evidence.clone(),
                keep: false,
            }),
            ..step()
        });
    }
    if !removed.is_empty() {
        steps.push(Step {
            purge: Some(library_tree::PurgeSlot {
                request_id: String::new(),
            }),
            ..step()
        });
    }
    for (path, new_oid) in foreign {
        steps.push(Step {
            replace: Some(ReplaceFile {
                foreign: true,
                name: name.clone(),
                verb: verb.to_string(),
                path: path.clone(),
                seen_sequence: 0,
                new_oid: new_oid.clone(),
                evidence: evidence.clone(),
            }),
            ..step()
        });
    }
    RunSequence {
        name,
        verb: verb.to_string(),
        steps,
        foreign: foreign
            .iter()
            .map(|(path, new_oid)| ForeignFile {
                path: path.clone(),
                new_oid: new_oid.clone(),
            })
            .collect(),
        evidence: evidence.clone(),
    }
}

/// The evidence a deps write is decided on: the tree read (the library is
/// git-free, owner 2026-11-18, so no git evidence).
fn evidence() -> Evidence {
    Evidence {
        tree_reads: true,
        plan_rejection: None,
        builtin: false,
    }
}

impl<E: Edge> Api<E> {
    /// `CreateRemote` (step 10c U4): `remotes.yaml`, one library write; init
    /// makes it for each `--remote` too.
    pub(crate) async fn create_remote_write(
        &self,
        request: &v1::CreateRemoteRequest,
    ) -> Result<v1::CreateRemoteResponse, ApiError> {
        let remote = request.remote.clone().unwrap_or_default();
        let ask = DepsAsk::CreateRemote {
            name: request.remote_id.clone(),
            url: remote.url,
            forge: remote.forge,
        };
        match self.deps_write(request, "remote create", &ask).await? {
            (DepsAnswer::Created(answer), etag) => Ok(v1::CreateRemoteResponse { etag, ..answer }),
            other => Err(crate::server::misanswered("remote create", &other)),
        }
    }

    /// The deps write `request` served (step 10c U4), as `verb`: planned
    /// where the caller stands, then written as the module says.
    pub(crate) async fn deps_write<Q: LibraryWriteRequest>(
        &self,
        request: &Q,
        verb: &str,
        ask: &DepsAsk,
    ) -> Result<(DepsAnswer, String), ApiError> {
        self.open()?;
        let plans = self.deps.as_ref().ok_or_else(|| ApiError::Failed {
            detail: "this daemon writes no dependency".to_string(),
        })?;
        let (site, trees) = self.write_site(request).await?;
        let (observations, _) = self.observing.as_ref().ok_or_else(|| ApiError::Failed {
            detail: "this daemon observes no library".to_string(),
        })?;
        let request_id = request.request_id();
        let known = (!request_id.is_empty())
            .then(|| observations.known(request_id))
            .flatten();
        match known.as_ref().map(|known| &known.end) {
            Some(End::Failed | End::Superseded) => {
                return Err(ApiError::Failed {
                    detail: format!("the write {request_id} was tried before and did not complete"),
                })
            }
            Some(End::Done) => {
                // Its retry: it wrote what it wrote; the answer is the
                // plan of the tree it left.
                let planned = plans.plan((&site.at, &site.env), &site.flags, &site.parent, ask)?;
                return Ok((saying(planned.answer, &trees), String::new()));
            }
            _ => {}
        }
        let planned = plans.plan((&site.at, &site.env), &site.flags, &site.parent, ask)?;
        let landing = landing_of(&trees.seen, Lands::Content).ok_or_else(|| ApiError::Failed {
            detail: "the tree a dependency write lands in was not observed".to_string(),
        })?;
        let pending = known.as_ref().and_then(|known| match known.end {
            End::Pending { done } => Some((done, known.intent.steps.clone())),
            _ => None,
        });
        let answer = saying(planned.answer.clone(), &trees);
        let is_pull = matches!(ask, DepsAsk::Pull { .. } | DepsAsk::Upgrade { .. });
        if !is_pull {
            // A remote's write: `remotes.yaml`, one library write.
            let held = observations.held(&landing.observed.tree);
            let Some(bytes) = planned.registry.clone() else {
                return Ok((answer, held.token(REGISTRY).to_string()));
            };
            if request.validate_only() {
                return Ok((answer, held.token(REGISTRY).to_string()));
            }
            let shaped = Shaped {
                lands: Lands::Content,
                shape: Shape::Replace {
                    path: REGISTRY.to_string(),
                    bytes,
                    executable: false,
                },
            };
            let (etag, _) = self
                .carry_out(
                    &landing,
                    (verb, request.etag()),
                    &shaped,
                    (request_id, pending),
                )
                .await?;
            return Ok((answer, etag));
        }
        let nothing = planned.installs.is_empty()
            && planned.lock.is_none()
            && planned.removed.is_empty()
            && planned.foreign.is_empty();
        if request.validate_only() && !matches!(ask, DepsAsk::Pull { .. })
            || (nothing && pending.is_none())
        {
            return Ok((answer, String::new()));
        }
        self.pull_steps(&landing, (verb, request_id), &planned, pending)
            .await?;
        Ok((answer, String::new()))
    }

    /// A pull's or an upgrade's plan made as its sequence (intended under
    /// `request_id` unless `pending` holds its recorded steps already),
    /// each step from the first not done.
    async fn pull_steps(
        &self,
        landing: &crate::library_writes::Landing<'_>,
        (verb, request_id): (&str, &str),
        planned: &DepsPlanned,
        pending: Option<(u32, Vec<Step>)>,
    ) -> Result<(), ApiError> {
        let (observations, observers) =
            self.observing.as_ref().ok_or_else(|| ApiError::Failed {
                detail: "this daemon observes no library".to_string(),
            })?;
        let operations = observations.operations().ok_or_else(|| ApiError::Failed {
            detail: "this daemon makes no library write".to_string(),
        })?;
        let tree = &landing.observed.tree;
        let dir = landing.site.tree();
        let root = match landing.site {
            library_api::Site::Checkout { root, .. } => root.clone(),
            library_api::Site::Home { .. } => String::new(),
        };
        let scanner = observers.scanner();
        let foreign = files_of(&root, &planned.foreign);
        let id = uuid::Uuid::parse_str(request_id).unwrap_or_else(|_| uuid::Uuid::now_v7());
        let request_id = id.to_string();
        let (start, steps) = match pending {
            Some(pending) => pending,
            None => {
                let parts = (tree, root.as_str());
                let steps = self
                    .intend_pull(parts, (verb, id), planned, &foreign)
                    .await?;
                (0, steps)
            }
        };
        self.dies_at("intended");
        // The plan's content for the library's own steps: the lock, and
        // each tree into the trash.
        let shape = Shape::Sequence(
            planned
                .lock
                .iter()
                .map(|bytes| Shape::Replace {
                    path: LOCK.to_string(),
                    bytes: bytes.clone(),
                    executable: false,
                })
                .chain(planned.removed.iter().map(|path| Shape::Trash {
                    path: path.clone(),
                    files: Vec::new(),
                }))
                .collect(),
        );
        let creates = steps.iter().filter(|step| step.create.is_some()).count();
        let made = Made {
            tree,
            dir,
            request_id: &request_id,
        };
        for (at, step) in steps.iter().enumerate().skip(start as usize) {
            let at_step = u32::try_from(at).unwrap_or(u32::MAX);
            if let Some(create) = &step.create {
                self.install_step(&made, operations.as_ref(), (at_step, create), planned)
                    .await?;
                for point in tree_points(at, creates) {
                    self.dies_at(point);
                }
            } else if step.replace.as_ref().is_some_and(|replace| replace.foreign) {
                self.foreign_step(
                    (tree, dir, &root),
                    (&request_id, operations.as_ref()),
                    (at_step, step),
                    &foreign,
                )
                .await?;
            } else {
                self.make_step(
                    &made,
                    scanner.as_ref(),
                    operations.as_ref(),
                    (at_step, step),
                    &shape,
                )
                .await?;
            }
        }
        self.dies_at("done");
        Ok(())
    }

    /// A pull's plan intended as its sequence under the request `id` on
    /// `tree` (the project's root beside it unread: the library is
    /// git-free, so no evidence names it): each id taken of
    /// its content (the lock's) or given (`foreign`'s), each token the
    /// tree's now. Its recorded steps.
    async fn intend_pull(
        &self,
        (tree, _root): (&library_tree::TreeName, &str),
        (verb, id): (&str, uuid::Uuid),
        planned: &DepsPlanned,
        foreign: &[(String, String, &ForeignPut)],
    ) -> Result<Vec<Step>, ApiError> {
        let (observations, _) = self.observing.as_ref().ok_or_else(|| ApiError::Failed {
            detail: "this daemon observes no library".to_string(),
        })?;
        let held = observations.held(tree);
        let request_id = id.to_string();
        let lock = planned
            .lock
            .as_ref()
            .map(|bytes| (library_tree::content_id(bytes), held.token(LOCK)));
        let removed: Vec<(String, u64)> = planned
            .removed
            .iter()
            .map(|path| (path.clone(), held.token(path)))
            .collect();
        let listed: Vec<(String, String)> = foreign
            .iter()
            .map(|(path, oid, _)| (path.clone(), oid.clone()))
            .collect();
        let mut sequence = pull_sequence(
            tree,
            verb,
            (&planned.installs, lock, &removed),
            &listed,
            &evidence(),
        );
        for step in &mut sequence.steps {
            if let Some(purge) = step.purge.as_mut() {
                purge.request_id.clone_from(&request_id);
            }
        }
        let steps = sequence.steps.clone();
        observations
            .send(command_book(
                tree,
                &Intended::Sequence(sequence),
                id.into_bytes(),
            ))
            .await
            .map_err(|failure| match failure {
                angzarr_embedded::Failure::Rejected(rejected) => {
                    ApiError::Refused(ApiRefusal::rejected(&rejected))
                }
                other => ApiError::failed(other),
            })?;
        Ok(steps)
    }

    /// The create step `create` (number `at`): its tree put in whole, by
    /// one atomic operation into the cache (a tree already there, whole by
    /// its rename, is done), then recorded installed with git's tree id.
    async fn install_step(
        &self,
        made: &Made<'_>,
        operations: &dyn ctxloom_ports::Operations,
        (at, create): (u32, &CreateDirectory),
        planned: &DepsPlanned,
    ) -> Result<(), ApiError> {
        let (observations, _) = self.observing.as_ref().ok_or_else(|| ApiError::Failed {
            detail: "this daemon observes no library".to_string(),
        })?;
        let request_id = made.request_id;
        let install = planned
            .installs
            .iter()
            .find(|install| install.path == create.path)
            .ok_or_else(|| ApiError::Failed {
                detail: format!(
                    "the write {request_id} cannot be resumed: its step {at} installs what the plan no longer holds"
                ),
            })?;
        let operation = ctxloom_ports::Operation::CreateDir {
            path: install.path.clone(),
            files: install.files.clone(),
        };
        // A tree already there was renamed in whole: the step is done.
        operations
            .operate(made.dir, request_id, &operation, &|point| {
                self.dies_at(point.word());
            })
            .map_err(|failure| ApiError::diagnosed(failure.as_ref()))?;
        let fact = RecordTreeInstalled {
            request_id: request_id.to_string(),
            step: at,
            identity: install.identity.clone(),
            sha: install.sha.clone(),
            tree: install.tree.clone(),
        };
        observations
            .send(step_book(
                made.tree,
                &fact,
                &format!("installed:{request_id}:{at}"),
            ))
            .await
            .map_err(ApiError::failed)?;
        Ok(())
    }
}

/// `answer`, the trees' notices said before its own.
fn saying(answer: DepsAnswer, trees: &library_api::Trees) -> DepsAnswer {
    let said: Vec<v1::Notice> = trees.notices.iter().map(notice_of).collect();
    let with =
        |own: &[v1::Notice]| -> Vec<v1::Notice> { said.iter().chain(own).cloned().collect() };
    match answer {
        DepsAnswer::Pulled(mut a) => {
            a.notices = with(&a.notices);
            DepsAnswer::Pulled(a)
        }
        DepsAnswer::Upgraded(mut a) => {
            a.notices = with(&a.notices);
            DepsAnswer::Upgraded(a)
        }
        DepsAnswer::Created(mut a) => {
            a.notices = with(&a.notices);
            DepsAnswer::Created(a)
        }
        DepsAnswer::Edited(mut a) => {
            a.notices = with(&a.notices);
            DepsAnswer::Edited(a)
        }
        DepsAnswer::Removed(mut a) => {
            a.notices = with(&a.notices);
            DepsAnswer::Removed(a)
        }
        DepsAnswer::Defaulted(mut a) => {
            a.notices = with(&a.notices);
            DepsAnswer::Defaulted(a)
        }
    }
}

#[cfg(test)]
#[path = "deps_writes.test.rs"]
mod tests;
