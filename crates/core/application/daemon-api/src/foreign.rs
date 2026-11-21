//! Other tools' files, written by the daemon (step 10c U10; L15, T26,
//! protos.md §1.1). `materialize`, `manage uninstall` and `doctor --fix`
//! write files that are not the library's: an engine's own context file,
//! settings and hooks, at rest. Such a write is planned where the caller
//! stands (its directory, its environment), writing nothing, then made as
//! ONE retryable sequence on the checkout's stream: each file a step of its
//! own (`ReplaceFile` marked `foreign`), the writer's own record of what it
//! claims FIRST, every path and the content id of what it will hold listed
//! in the intent. Each step, once made, is recorded done, so a write killed
//! between its steps is finished by its retry, from the first step not
//! done; and a re-run converges, for a step whose file already holds what
//! it would write is made by writing nothing.
//!
//! No foreign file is ever observed as the library: it is outside the
//! tree's pathspec, and its step's record is kept apart (the state's
//! `foreign`), never a token a client is given.

use daemon_proto::v1;
use library_api::writes::{command_book, step_book, Intended, Lands};
use library_checkout::End;
use library_tree::{Evidence, ForeignFile, RecordStepDone, ReplaceFile, RunSequence, Step};
use run_ports::Edge;

use crate::library_writes::{landing_of, notice_of, LibraryWriteRequest};
use crate::refusal::{ApiError, ApiRefusal};
use crate::server::Api;

/// One file a foreign writer would leave, as it would be left.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ForeignPut {
    /// Its absolute path.
    pub path: String,
    /// Its bytes; none where it is removed.
    pub bytes: Option<Vec<u8>>,
    pub executable: bool,
    /// The directory its removal empties nothing above.
    pub keep: String,
}

/// What a foreign writer is asked to do.
#[derive(Clone, Copy, Debug)]
pub enum ForeignAsk<'a> {
    Materialize(&'a v1::MaterializeRequest),
    Uninstall,
    DoctorFix,
}

/// A foreign writer's answer, as the API says it.
#[derive(Clone, Debug, PartialEq)]
pub enum ForeignAnswer {
    Materialized(v1::MaterializeResponse),
    Uninstalled(v1::UninstallManageResponse),
    Fixed(v1::RunDoctorResponse),
}

impl ForeignAnswer {
    /// The answer, `notices` said before what it says itself.
    fn saying(self, notices: Vec<v1::Notice>) -> ForeignAnswer {
        let said = |own: Vec<v1::Notice>| notices.iter().cloned().chain(own).collect();
        match self {
            ForeignAnswer::Materialized(answer) => {
                ForeignAnswer::Materialized(v1::MaterializeResponse {
                    notices: said(answer.notices.clone()),
                    ..answer
                })
            }
            ForeignAnswer::Uninstalled(answer) => {
                ForeignAnswer::Uninstalled(v1::UninstallManageResponse {
                    notices: said(answer.notices.clone()),
                    ..answer
                })
            }
            ForeignAnswer::Fixed(answer) => ForeignAnswer::Fixed(v1::RunDoctorResponse {
                notices: said(answer.notices.clone()),
                ..answer
            }),
        }
    }
}

/// What a foreign writer planned: its answer, and the files it would leave
/// (its own record first).
#[derive(Clone, Debug, PartialEq)]
pub struct ForeignPlanned {
    pub answer: ForeignAnswer,
    pub files: Vec<ForeignPut>,
}

/// The setup use cases that write other tools' files, run where a process
/// at `dir`, in `env`, under `flags`, would run them, WRITING NOTHING: the
/// wiring's (over a recording store). A use case's own refusal or failure
/// is the call's ([`crate::setup_error`]), so nothing is written.
pub trait ForeignPlans: Send + Sync {
    fn plan(
        &self,
        dir: &str,
        env: &[(String, String)],
        flags: &library_application::Flags,
        ask: ForeignAsk<'_>,
    ) -> Result<ForeignPlanned, ApiError>;
}

/// The path a foreign step names: relative to the project's `root` when it
/// is below it (protos.md §1.1), else as it is (the home's record).
pub(crate) fn step_path(root: &str, path: &str) -> String {
    let root = root.trim_end_matches(['/', '\\']);
    path.strip_prefix(root)
        .and_then(|rest| rest.strip_prefix(['/', '\\']))
        .filter(|rest| !rest.is_empty() && !root.is_empty())
        .map_or_else(|| path.to_string(), |rest| rest.replace('\\', "/"))
}

/// The removal of the foreign file a step names (`path`, as [`step_path`]
/// names it), from its intent alone: below the project's `root`, nothing
/// above the root emptied; elsewhere, nothing above its own directory.
pub(crate) fn removal(root: &str, path: &str) -> ctxloom_ports::Operation {
    let absolute = path.starts_with('/') || path.contains(":\\") || root.is_empty();
    let (path, keep) = if absolute {
        let keep = path
            .rsplit_once(['/', '\\'])
            .map_or_else(String::new, |(dir, _)| dir.to_string());
        (path.to_string(), keep)
    } else {
        let root = root.trim_end_matches(['/', '\\']);
        (format!("{root}/{path}"), root.to_string())
    };
    ctxloom_ports::Operation::Outside {
        path,
        bytes: None,
        executable: false,
        keep,
    }
}

/// Each of `puts` as its step names it: its path ([`step_path`], from the
/// project's `root`), the content id of what it leaves
/// (`library_tree::content_id`; none for a removal), and the put itself.
pub(crate) fn files_of<'a>(
    root: &str,
    puts: &'a [ForeignPut],
) -> Vec<(String, String, &'a ForeignPut)> {
    puts.iter()
        .map(|put| {
            let oid = put
                .bytes
                .as_deref()
                .map(library_tree::content_id)
                .unwrap_or_default();
            (step_path(root, &put.path), oid, put)
        })
        .collect()
}

/// The sequence of a foreign writer's `files` (each its step's path, git's
/// id for what it leaves, and its put), on `tree` under `verb`: one foreign
/// `ReplaceFile` each, every one listed in the intent.
fn sequence_of(
    tree: &library_tree::TreeName,
    verb: &str,
    files: &[(String, String, &ForeignPut)],
    evidence: Evidence,
) -> RunSequence {
    RunSequence {
        name: tree.to_string(),
        verb: verb.to_string(),
        steps: files
            .iter()
            .map(|(path, oid, _)| Step {
                replace: Some(ReplaceFile {
                    foreign: true,
                    name: tree.to_string(),
                    verb: verb.to_string(),
                    path: path.clone(),
                    seen_sequence: 0,
                    new_oid: oid.clone(),
                    evidence: evidence.clone(),
                }),
                create: None,
                trash: None,
                r#move: None,
                purge: None,
            })
            .collect(),
        foreign: files
            .iter()
            .map(|(path, oid, _)| ForeignFile {
                path: path.clone(),
                new_oid: oid.clone(),
            })
            .collect(),
        evidence,
    }
}

/// The kill point after a foreign writer's `n`th step (one-based) is
/// recorded done (the `test-die-at` switch's word).
pub(crate) fn after_step(n: usize) -> String {
    format!("step-{n}")
}

impl<E: Edge> Api<E> {
    /// A foreign writer `ask` served (step 10c U10): observed as a write
    /// is, planned in the caller's place, then made as one sequence of its
    /// files, intended under the request's id and resumed on its retry.
    pub(crate) async fn foreign_write<Q: LibraryWriteRequest>(
        &self,
        request: &Q,
        verb: &str,
        ask: ForeignAsk<'_>,
        dry: bool,
    ) -> Result<ForeignAnswer, ApiError> {
        self.open()?;
        let plans = self.foreign.as_ref().ok_or_else(|| ApiError::Failed {
            detail: "this daemon writes no other tool's file".to_string(),
        })?;
        let (site, trees) = self.foreign_site(request).await?;
        let (observations, _) = self.observing.as_ref().ok_or_else(|| ApiError::Failed {
            detail: "this daemon observes no library".to_string(),
        })?;
        let notices: Vec<v1::Notice> = trees.notices.iter().map(notice_of).collect();
        let planned = plans.plan(&site.at, &site.env, &site.flags, ask)?;
        let answer = planned.answer.clone().saying(notices);
        let request_id = request.request_id();
        let known = (!request_id.is_empty())
            .then(|| observations.known(request_id))
            .flatten();
        // A retry of a sequence left pending resumes it though its re-plan
        // plans nothing: what an earlier step did (its record given back
        // first) can leave the plan nothing to say of the files after it.
        let resuming = known
            .as_ref()
            .is_some_and(|known| matches!(known.end, End::Pending { .. }));
        if dry || request.validate_only() || (planned.files.is_empty() && !resuming) {
            return Ok(answer);
        }
        let landing = landing_of(&trees.seen, Lands::Content).ok_or_else(|| ApiError::Failed {
            detail: "the tree a foreign writer records in was not observed".to_string(),
        })?;
        let pending = match known.as_ref().map(|known| &known.end) {
            // A retry of a write done: it wrote what it wrote.
            Some(End::Done) => return Ok(answer),
            Some(End::Failed | End::Superseded) => {
                return Err(ApiError::Failed {
                    detail: format!("the write {request_id} was tried before and did not complete"),
                })
            }
            Some(End::Pending { done }) => known
                .as_ref()
                .map(|known| (*done, known.intent.steps.clone())),
            None => None,
        };
        let root = match landing.site {
            library_api::Site::Checkout { root, .. } => root.clone(),
            library_api::Site::Home { .. } => String::new(),
        };
        let dir = landing.site.tree();
        let files = files_of(&root, &planned.files);
        let evidence = Evidence {
            tree_reads: true,
            plan_rejection: None,
            builtin: false,
        };
        let tree = &landing.observed.tree;
        let id = uuid::Uuid::parse_str(request_id).unwrap_or_else(|_| uuid::Uuid::now_v7());
        let request_id = id.to_string();
        let (start, steps) = match pending {
            Some(pending) => pending,
            None => {
                let sequence = sequence_of(tree, verb, &files, evidence);
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
                (0, steps)
            }
        };
        self.dies_at("intended");
        self.foreign_steps(tree, (dir, &root), &request_id, (start, &steps), &files)
            .await?;
        self.dies_at("done");
        Ok(answer)
    }

    /// Where a foreign writer writes, and its trees observed, in any
    /// directory (the library is git-free, owner 2026-11-18): another
    /// tool's files are recorded everywhere.
    async fn foreign_site<Q: LibraryWriteRequest>(
        &self,
        request: &Q,
    ) -> Result<(crate::library_reads::ReadSite<'_>, library_api::Trees), ApiError> {
        let (site, project) = self
            .resolve(
                request.target(),
                request.caller_cwd(),
                request.caller_env(),
                request.caller_globals(),
            )
            .await?;
        let (observations, observers) =
            self.observing.as_ref().ok_or_else(|| ApiError::Failed {
                detail: "this daemon observes no library".to_string(),
            })?;
        let observed = observations
            .observe_trees(
                observers.as_ref(),
                project
                    .as_ref()
                    .map(|(name, root)| (name.clone(), root.as_str())),
            )
            .await
            .map_err(|library_api::ReadError::Failed(failure)| {
                ApiError::diagnosed(failure.as_ref())
            })?;
        Ok((site, observed))
    }

    /// The steps of a foreign writer's intent from `start`, each made by its
    /// operation (its content the plan's `files`, or, for a removal the
    /// plan no longer holds, its intent alone) and recorded done.
    async fn foreign_steps(
        &self,
        tree: &library_tree::TreeName,
        (dir, root): (&str, &str),
        request_id: &str,
        (start, steps): (u32, &[Step]),
        files: &[(String, String, &ForeignPut)],
    ) -> Result<(), ApiError> {
        let (observations, _) = self.observing.as_ref().ok_or_else(|| ApiError::Failed {
            detail: "this daemon observes no library".to_string(),
        })?;
        let operations = observations.operations().ok_or_else(|| ApiError::Failed {
            detail: "this daemon makes no write".to_string(),
        })?;
        for (at, step) in steps.iter().enumerate().skip(start as usize) {
            let at = u32::try_from(at).unwrap_or(u32::MAX);
            self.foreign_step(
                (tree, dir, root),
                (request_id, operations.as_ref()),
                (at, step),
                files,
            )
            .await?;
            self.dies_at(&after_step(at as usize + 1));
        }
        Ok(())
    }

    /// The foreign step `step` (number `at`) of the write `request_id`
    /// made by its operation (its content the plan's `files`, or, for a
    /// removal the plan no longer holds, its intent alone) and recorded
    /// done. A step that replaces no file is none of a foreign writer's.
    pub(crate) async fn foreign_step(
        &self,
        (tree, dir, root): (&library_tree::TreeName, &str, &str),
        (request_id, operations): (&str, &dyn ctxloom_ports::Operations),
        (at, step): (u32, &Step),
        files: &[(String, String, &ForeignPut)],
    ) -> Result<(), ApiError> {
        let (observations, _) = self.observing.as_ref().ok_or_else(|| ApiError::Failed {
            detail: "this daemon observes no library".to_string(),
        })?;
        {
            let Some(path) = step.replace.as_ref().map(|replace| replace.path.clone()) else {
                return Ok(());
            };
            let intended = step
                .replace
                .as_ref()
                .map(|r| r.new_oid.clone())
                .unwrap_or_default();
            let (oid, operation) = match files.iter().find(|(named, _, _)| *named == path) {
                Some((_, oid, put)) => (
                    oid.clone(),
                    ctxloom_ports::Operation::Outside {
                        path: put.path.clone(),
                        bytes: put.bytes.clone(),
                        executable: put.executable,
                        keep: put.keep.clone(),
                    },
                ),
                // A removal the re-plan no longer holds needs no content:
                // its intent says it all.
                None if intended.is_empty() => (String::new(), removal(root, &path)),
                None => {
                    return Err(ApiError::Failed {
                        detail: format!(
                            "the write {request_id} cannot be resumed: its step {at} writes what the plan no longer holds"
                        ),
                    })
                }
            };
            operations
                .operate(dir, request_id, &operation, &|_| {})
                .map_err(|failure| ApiError::diagnosed(failure.as_ref()))?;
            let fact = RecordStepDone {
                request_id: request_id.to_string(),
                step: at,
                observed: vec![library_tree::DirtyPath {
                    path: path.clone(),
                    oid,
                }],
            };
            observations
                .send(step_book(tree, &fact, &format!("done:{request_id}:{at}")))
                .await
                .map_err(ApiError::failed)?;
        }
        Ok(())
    }
}

#[cfg(test)]
#[path = "foreign.test.rs"]
mod tests;
