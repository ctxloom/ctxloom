//! The library's writes the daemon serves (step 10b U3; protos.md §1.1).
//! A write is, in order:
//!
//! 1. its project resolved and its trees OBSERVED, as a read's are (a
//!    tree outside git, or one git ignores, refuses it);
//! 2. its request id looked up: a write its retry repeats (the same id) is
//!    answered as it was applied, or finished from the step it got to;
//! 3. PLANNED: the use case run over the library's recording ports, which
//!    write nothing (`library_application::write::recording`), and its plan
//!    shaped into the one step it is (`library_api::writes::shape`; more
//!    than one file is `OneChangePerCall`);
//! 4. INTENDED: the step's command (its path, git's ids for what it
//!    writes, the path's token, the evidence) sent under the request id, so
//!    the domain decides it and records `ChangeIntended`;
//! 5. ONE OPERATION: the step made by its atomic operation (U8,
//!    `ctxloom_ports::Operations`: staged under the tree's `state/`, then
//!    renamed in; a removal renamed into its trash slot), each point it
//!    reaches told to the test-support kill switch (`staged`, `trashed`);
//! 6. RE-OBSERVED: what the step left read back and recorded
//!    (`RecordStepDone`), its `StepApplied` the paths' new etag;
//! 7. answered, with that etag.
//!
//! A preview (`validate_only`) is step 1 and the use case's preview alone:
//! nothing is intended or written, and its etag is the token of the path
//! its apply would change.

use std::sync::Arc;

use daemon_proto::v1;
use library_api::served_writes::Wrote;
use library_api::writes::{self, Lands};
use library_api::{Observed, Site};
use library_application::{Flags, Library, Outcome};
use library_checkout::End;
use library_tree::{Evidence, RecordStepDone, RecordStepFailed, TreeName};
use run_ports::Edge;

use crate::library_reads::read_error;
use crate::refusal::{ApiError, ApiRefusal};
use crate::server::Api;

/// What every library write request carries besides its own fields.
/// (its caller: [`daemon_proto::CarriesMut`]).
pub trait LibraryWriteRequest: daemon_proto::CarriesMut {
    /// What it writes: a resource name, or a parent.
    fn target(&self) -> &str;
    /// The request, writing `target` (a client names its parent so).
    fn set_target(&mut self, target: String);
    /// The request, under the request id `id` (AIP-155), where it names none.
    fn set_request_id(&mut self, id: String);
    /// The client's id for the write (AIP-155): the command's id.
    fn request_id(&self) -> &str;
    fn validate_only(&self) -> bool;
    /// The etag the caller's preview answered (AIP-154: the path's last
    /// sequence, as a decimal), sent back with the apply; empty with
    /// `--yes` or for a write that takes none (a create).
    fn etag(&self) -> &str;
}

/// The sequence a write was planned against: the caller's etag (its
/// preview's), else the path's token now (a `--yes`, one call that plans
/// and applies). `decide` refuses the write `PlanStale` when it is not the
/// path's last sequence. An etag that is not a number is refused, the
/// request's own bad argument (`EtagInvalid`, protos.md §1.1).
pub(crate) fn seen_sequence(etag: &str, now: u64) -> Result<u64, ApiError> {
    if etag.is_empty() {
        return Ok(now);
    }
    etag.parse::<u64>().map_err(|_| {
        ApiError::Refused(ApiRefusal::of(&v1::EtagInvalid {
            etag: etag.to_string(),
        }))
    })
}

/// A test-support point of a write the daemon may be killed at (the
/// `test-die-at` switch): the wiring's, called with the point's word.
pub type DiesAt = Arc<dyn Fn(&str) + Send + Sync>;

/// The tree a write lands in, as it was observed.
pub(crate) struct Landing<'a> {
    pub(crate) site: &'a Site,
    pub(crate) observed: &'a Observed,
}

/// The request id as the command id's bytes: the client's UUID, or (a
/// client that named none) one minted for this call alone.
fn request_bytes(request_id: &str) -> [u8; 16] {
    uuid::Uuid::parse_str(request_id)
        .unwrap_or_else(|_| uuid::Uuid::now_v7())
        .into_bytes()
}

/// What a write that took paths into the trash to keep says (step 10b
/// texts.md §3, G12): each path and the slot it is kept in, as the tree
/// shows them (the tree directory's own name first, `.ctxloom/...`): a lone
/// trash's, and each of a sequence's that purges no slot of its own
/// (`clean`'s); none for any other step.
pub(crate) fn trashed_notice(
    tree: &str,
    shape: &writes::Shape,
    made_under: &str,
) -> Vec<v1::Notice> {
    let kept: Vec<&str> = match shape {
        writes::Shape::Trash { path, .. } => vec![path.as_str()],
        writes::Shape::Sequence(steps) if !steps.contains(&writes::Shape::Purge) => steps
            .iter()
            .filter_map(|step| match step {
                writes::Shape::Trash { path, .. } => Some(path.as_str()),
                _ => None,
            })
            .collect(),
        _ => Vec::new(),
    };
    let base = tree
        .trim_end_matches(['/', '\\'])
        .rsplit(['/', '\\'])
        .next()
        .unwrap_or_default();
    kept.into_iter()
        .map(|path| {
            notice_of(&library_api::LibraryApiNotice::Trashed {
                path: format!("{base}/{path}"),
                slot: format!("{base}/state/trash/{made_under}/"),
            })
        })
        .collect()
}

pub(crate) fn notice_of(notice: &library_api::LibraryApiNotice) -> v1::Notice {
    v1::Notice {
        code: coded_diagnostic::Diagnostic::code(notice).to_string(),
        args: run_application::client::flat_args(notice),
        text: notice.to_string(),
    }
}

fn refused(rejection: &library_tree::LibraryTreeRejection) -> ApiError {
    ApiError::Refused(crate::library_refusal(rejection))
}

/// The write a step is of: its tree's stream, its directory, its request.
pub(crate) struct Made<'a> {
    pub(crate) tree: &'a TreeName,
    pub(crate) dir: &'a str,
    pub(crate) request_id: &'a str,
}

/// What a use case planned: its outcome, its recorded writes shaped, and
/// a tree it writes outside ctxloom's trees, if any (D2's EXCEPTION:
/// `bundle export`).
pub(crate) struct Planned<T> {
    pub(crate) outcome: Outcome<T>,
    pub(crate) shaped: Result<Option<writes::Shaped>, library_tree::LibraryTreeRejection>,
    outside: Option<Outside>,
}

/// A tree written outside ctxloom's trees: its directory and its files.
type Outside = (String, Vec<ctxloom_ports::Put>);

/// A use case's recorded writes.
type Recorded = Vec<library_application::write::recording::Planned>;

/// A use case's recorded writes, a tree it writes outside ctxloom's trees
/// taken aside: one such tree alone is the write (no step, no intent);
/// beside anything else it is more than one change, refused
/// `OneChangePerCall`.
fn outside_of(
    written: Recorded,
) -> (
    Recorded,
    Result<Option<Outside>, library_tree::LibraryTreeRejection>,
) {
    use library_application::write::recording::Planned as Wrote;
    let (outside, inside): (Vec<_>, Vec<_>) = written
        .into_iter()
        .partition(|planned| matches!(planned, Wrote::Outside { .. }));
    let mut trees: Vec<Outside> = outside
        .into_iter()
        .filter_map(|planned| match planned {
            Wrote::Outside { dir, files } => Some((dir, files)),
            _ => None,
        })
        .collect();
    match (trees.len(), inside.is_empty()) {
        (0, _) => (inside, Ok(None)),
        (1, true) => (inside, Ok(trees.pop())),
        _ => {
            let paths = trees.into_iter().map(|(dir, _)| dir).collect();
            (
                Vec::new(),
                Err(library_tree::LibraryTreeRejection::OneChangePerCall { paths }),
            )
        }
    }
}

impl<E: Edge> Api<E> {
    /// Serve the write `request`, its use case `plan` (given whether it is
    /// to apply: false for a preview), recorded as `verb` (step 10b U3).
    pub(crate) async fn library_write<Q: LibraryWriteRequest, T>(
        &self,
        request: &Q,
        verb: &str,
        plan: impl Fn(&Library, &Flags, bool) -> Outcome<T>,
    ) -> Result<Wrote<T>, ApiError> {
        self.open()?;
        self.write_planned(request, verb, plan).await
    }

    /// The write `request` served as [`Api::library_write`] serves it,
    /// whether or not the daemon is open: a write that follows from work it
    /// already took (a distill's, once its run ended) is made while it
    /// drains too.
    pub(crate) async fn write_planned<Q: LibraryWriteRequest, T>(
        &self,
        request: &Q,
        verb: &str,
        plan: impl Fn(&Library, &Flags, bool) -> Outcome<T>,
    ) -> Result<Wrote<T>, ApiError> {
        let (site, trees) = self.write_site(request).await?;
        let (observations, _) = self.observing.as_ref().ok_or_else(|| ApiError::Failed {
            detail: "this daemon observes no library".to_string(),
        })?;
        let mut notices: Vec<v1::Notice> = trees.notices.iter().map(notice_of).collect();
        let request_id = request.request_id();
        let known = (!request_id.is_empty())
            .then(|| observations.known(request_id))
            .flatten();
        if let Some(before) = answered_before(
            (&trees, observations),
            (request_id, known.as_ref()),
            &site.parent,
            &mut notices,
        ) {
            return before;
        }
        let apply = !request.validate_only();
        let (answer, shaped, outside) = {
            let planned = self.planned(&site, apply, &plan)?;
            let said = planned.outcome.notices.iter();
            notices.extend(said.map(run_application::client::notice_of));
            let answer = planned.outcome.result.map_err(|error| read_error(&error));
            (answer, planned.shaped, planned.outside)
        };
        let answer = answer?;
        let shaped = shaped.map_err(|rejection| refused(&rejection))?;
        if let Some(outside) = outside {
            let destination = outside.0.clone();
            let wrote = self.made_outside(&site, apply, outside, (answer, notices))?;
            if apply && !request_id.is_empty() {
                self.record_export(&site, (&trees, request), &destination)
                    .await?;
            }
            if apply {
                // Made and recorded, not yet answered: the kill point every
                // write's retry is forced at.
                self.dies_at("done");
            }
            return Ok(wrote);
        }
        let Some(shaped) = shaped else {
            // A preview, or a write that changes nothing: nothing to intend.
            return Ok(Wrote {
                answer: Some(answer),
                applied: false,
                etag: String::new(),
                at: String::new(),
                parent: site.parent.clone(),
                notices,
                source: String::new(),
            });
        };
        let landing = landing_of(&trees.seen, shaped.lands).ok_or_else(|| ApiError::Failed {
            detail: "the tree this write lands in was not observed".to_string(),
        })?;
        let path = shaped.shape.path().to_string();
        let held = observations.held(&landing.observed.tree);
        let at = format!("{}/{path}", landing.site.tree());
        if !apply {
            return Ok(Wrote {
                answer: Some(answer),
                applied: false,
                etag: held.token(&path).to_string(),
                at,
                parent: site.parent.clone(),
                notices,
                source: String::new(),
            });
        }
        let pending = known.as_ref().and_then(|known| match known.end {
            End::Pending { done } => Some((done, known.intent.steps.clone())),
            _ => None,
        });
        let (etag, trashed) = self
            .carry_out(
                &landing,
                (verb, request.etag()),
                &shaped,
                (request_id, pending),
            )
            .await?;
        notices.extend(trashed);
        Ok(Wrote {
            answer: Some(answer),
            applied: true,
            etag,
            at,
            parent: site.parent.clone(),
            notices,
            source: String::new(),
        })
    }

    /// Where a write is made, and its trees observed (in any directory: the
    /// library is git-free, owner 2026-11-18).
    pub(crate) async fn write_site<Q: LibraryWriteRequest>(
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
        let trees = observations
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
        Ok((site, trees))
    }

    /// The write `shaped` intended under `request_id` (unless its intent
    /// is recorded already: `pending`, the step it got to and its recorded
    /// steps), made one step at a time from the first not done, each step
    /// one atomic operation recorded done (T28: a sequence resumed on a
    /// retry where it stopped): the path's new token, and, for a lone step
    /// that took a path into the trash, the notice of where it is kept.
    pub(crate) async fn carry_out(
        &self,
        landing: &Landing<'_>,
        (verb, etag): (&str, &str),
        shaped: &writes::Shaped,
        (request_id, pending): (&str, Option<(u32, Vec<library_tree::Step>)>),
    ) -> Result<(String, Vec<v1::Notice>), ApiError> {
        let (observations, observers) =
            self.observing.as_ref().ok_or_else(|| ApiError::Failed {
                detail: "this daemon observes no library".to_string(),
            })?;
        let path = shaped.shape.path().to_string();
        let held = observations.held(&landing.observed.tree);
        let scanner = observers.scanner();
        let evidence = Evidence {
            tree_reads: true,
            plan_rejection: None,
            builtin: false,
        };
        let id = request_bytes(request_id);
        let request_id = uuid::Uuid::from_bytes(id).to_string();
        let intended = writes::intent_of(
            &landing.observed.tree.to_string(),
            verb,
            &shaped.shape,
            (seen_sequence(etag, held.token(&path))?, &|other: &str| {
                held.token(other)
            }),
            evidence,
            &request_id,
        );
        let (start, steps) = match pending {
            Some((done, steps)) => (done, steps),
            None => {
                let book = writes::command_book(&landing.observed.tree, &intended, id);
                observations
                    .send(book)
                    .await
                    .map_err(|failure| match failure {
                        angzarr_embedded::Failure::Rejected(rejected) => {
                            ApiError::Refused(ApiRefusal::rejected(&rejected))
                        }
                        other => ApiError::failed(other),
                    })?;
                let done = observations
                    .known(&request_id)
                    .and_then(|known| match known.end {
                        End::Pending { done } => Some(done),
                        _ => None,
                    })
                    .unwrap_or(0);
                (done, intended.steps(&request_id))
            }
        };
        self.dies_at("intended");
        let tree = &landing.observed.tree;
        let operations = observations.operations().ok_or_else(|| ApiError::Failed {
            detail: "this daemon makes no library write".to_string(),
        })?;
        for (at, step) in steps.iter().enumerate().skip(start as usize) {
            let at = u32::try_from(at).unwrap_or(u32::MAX);
            let step_of = Made {
                tree,
                dir: landing.site.tree(),
                request_id: &request_id,
            };
            self.make_step(
                &step_of,
                scanner.as_ref(),
                operations.as_ref(),
                (at, step),
                &shaped.shape,
            )
            .await?;
        }
        let notices = trashed_notice(landing.site.tree(), &shaped.shape, &request_id);
        self.dies_at("done");
        Ok((observations.held(tree).token(&path).to_string(), notices))
    }

    /// The step `step` (number `at`) of the write `made` makes: its one
    /// atomic operation, its content the plan `planned`'s step for it,
    /// then what it left read back and recorded done. A step that cannot
    /// be made, or made nothing, is recorded failed (final).
    pub(crate) async fn make_step(
        &self,
        made: &Made<'_>,
        scanner: &dyn ctxloom_ports::LibraryScanner,
        operations: &dyn ctxloom_ports::Operations,
        (at, step): (u32, &library_tree::Step),
        planned: &writes::Shape,
    ) -> Result<(), ApiError> {
        let (observations, _) = self.observing.as_ref().ok_or_else(|| ApiError::Failed {
            detail: "this daemon observes no library".to_string(),
        })?;
        let request_id = made.request_id;
        // The plan's step for it (this call's plan: a fresh write's, or
        // the retry's re-plan of the tree as the earlier try left it).
        let shape = writes::content_for(step, planned);
        let Some(operation) = writes::operation_of(step, shape) else {
            let why = ApiError::Failed {
                detail: format!(
                    "the write {request_id} cannot be resumed: its step {at} writes what the plan no longer holds"
                ),
            };
            self.step_failed(made.tree, request_id, at, &why).await?;
            return Err(why);
        };
        let operated = operations
            .operate(made.dir, request_id, &operation, &|point| {
                self.dies_at(point.word())
            })
            .map_err(|failure| ApiError::diagnosed(failure.as_ref()));
        let refused = match operated {
            Ok(operated) => not_made(operated),
            Err(failure) => Some(failure),
        };
        if let Some(why) = refused {
            self.step_failed(made.tree, request_id, at, &why).await?;
            return Err(why);
        }
        self.dies_at("renamed");
        let observed = writes::left_by(scanner, made.dir, &writes::expected_step(step, shape))
            .map_err(|failure| ApiError::diagnosed(failure.as_ref()))?;
        let fact = RecordStepDone {
            request_id: request_id.to_string(),
            step: at,
            observed,
        };
        let key = format!("done:{request_id}:{at}");
        observations
            .send(writes::step_book(made.tree, &fact, &key))
            .await
            .map_err(ApiError::failed)?;
        Ok(())
    }

    /// The use case planned over the recording library at `site`: its
    /// outcome, and what it would write shaped into its one step. A
    /// preview's etag is its apply's path's, so its apply is planned too
    /// (recorded, never performed) to name that path.
    pub(crate) fn planned<T>(
        &self,
        site: &crate::library_reads::ReadSite<'_>,
        apply: bool,
        plan: &impl Fn(&Library, &Flags, bool) -> Outcome<T>,
    ) -> Result<Planned<T>, ApiError> {
        let mut planned = None;
        site.libraries.read(&site.at, &site.env, &mut |library| {
            if planned.is_some() {
                return;
            }
            let (recording, recorder) = library.clone().recording();
            let outcome = plan(&recording, &site.flags, apply);
            let mut written = recorder.take();
            if !apply && outcome.result.is_ok() {
                let (recording, recorder) = library.clone().recording();
                let _ = plan(&recording, &site.flags, true);
                written = recorder.take();
            }
            let (written, outside) = outside_of(written);
            let (shaped, outside) = match outside {
                Ok(outside) => (
                    writes::shape(written, |bundle, dir| library.bundle_files(bundle, dir)),
                    outside,
                ),
                Err(rejection) => (Err(rejection), None),
            };
            planned = Some(Planned {
                outcome,
                shaped,
                outside,
            });
        })?;
        planned.ok_or_else(|| ApiError::Failed {
            detail: "the library was not read".to_string(),
        })
    }

    /// A write that is a tree outside ctxloom's trees (D2's EXCEPTION): no
    /// step of any tree's stream; made, on an apply, by the user files'
    /// staged NOREPLACE put, whole or not at all.
    fn made_outside<T>(
        &self,
        site: &crate::library_reads::ReadSite<'_>,
        apply: bool,
        (dir, files): Outside,
        (answer, notices): (T, Vec<v1::Notice>),
    ) -> Result<Wrote<T>, ApiError> {
        if apply {
            self.write_outside(site, &dir, &files)?;
        }
        Ok(Wrote {
            answer: Some(answer),
            applied: apply,
            etag: String::new(),
            at: dir,
            parent: site.parent.clone(),
            notices,
            source: String::new(),
        })
    }

    /// The export `request` made into `destination` recorded on its SOURCE
    /// tree's stream (AIP-155, owner 2026-11-18, night), keyed by its
    /// request id: the observed tree whose library holds the bundle's
    /// manifest (the checkout's first), as git sees it.
    async fn record_export<Q: LibraryWriteRequest>(
        &self,
        site: &crate::library_reads::ReadSite<'_>,
        (trees, request): (&library_api::Trees, &Q),
        destination: &str,
    ) -> Result<(), ApiError> {
        let Some((observations, observers)) = self.observing.as_ref() else {
            return Ok(());
        };
        let bundle = library_api::names::bundle_under(&site.parent, request.target())
            .unwrap_or_else(|| request.target().to_string());
        let manifest = format!("{}/{bundle}/bundle.yaml", writes::BUNDLES);
        let scanner = observers.scanner();
        let holds = |dir: &str| {
            scanner
                .content_ids(dir, &[manifest.as_str()])
                .is_ok_and(|ids| ids.contains_key(&manifest))
        };
        let Some((source, observed)) = trees.seen.iter().find(|(seen, _)| holds(seen.tree()))
        else {
            return Ok(());
        };
        let request_id = request.request_id();
        let fact = library_tree::RecordBundleExported {
            request_id: request_id.to_string(),
            bundle,
            source: export_source(source.tree(), &manifest),
            destination: destination.to_string(),
        };
        observations
            .send(writes::step_book(
                &observed.tree,
                &fact,
                &format!("export:{request_id}"),
            ))
            .await
            .map(|_| ())
            .map_err(ApiError::failed)
    }

    /// The tree `files` put in at `dir`, outside ctxloom's trees, through
    /// the site's library (its user files).
    fn write_outside(
        &self,
        site: &crate::library_reads::ReadSite<'_>,
        dir: &str,
        files: &[ctxloom_ports::Put],
    ) -> Result<(), ApiError> {
        let mut made = None;
        site.libraries.read(&site.at, &site.env, &mut |library| {
            if made.is_none() {
                made = Some(library.write_outside(dir, files));
            }
        })?;
        match made {
            Some(Ok(())) => Ok(()),
            Some(Err(error)) => Err(read_error(&error)),
            None => Err(ApiError::Failed {
                detail: "the library was not read".to_string(),
            }),
        }
    }

    /// The step `step` of the write `request_id` failed (final): recorded,
    /// so no intent stays owed.
    async fn step_failed(
        &self,
        tree: &TreeName,
        request_id: &str,
        step: u32,
        why: &ApiError,
    ) -> Result<(), ApiError> {
        let Some((observations, _)) = &self.observing else {
            return Ok(());
        };
        let fact = RecordStepFailed {
            request_id: request_id.to_string(),
            step,
            ext_err_txt: format!("{why:?}"),
        };
        let key = format!("failed:{request_id}:{step}");
        observations
            .send(writes::step_book(tree, &fact, &key))
            .await
            .map(|_| ())
            .map_err(ApiError::failed)
    }

    /// The test-support kill point `point` of a write, when the wiring
    /// set one.
    pub(crate) fn dies_at(&self, point: &str) {
        if let Some(dies) = &self.write_dies_at {
            dies(point);
        }
    }
}

/// Why an operation made no change, as the domain refuses it: something at
/// the path it creates (`ResourceExists`, D4's NOREPLACE), or nothing at the
/// path it takes (`NoSuchResource`). None when it is made.
pub(crate) fn not_made(operated: ctxloom_ports::Operated) -> Option<ApiError> {
    let rejection = match operated {
        ctxloom_ports::Operated::Done => return None,
        ctxloom_ports::Operated::Exists { path } => {
            library_tree::LibraryTreeRejection::ResourceExists { path }
        }
        ctxloom_ports::Operated::Missing { path } => {
            library_tree::LibraryTreeRejection::NoSuchResource { path }
        }
    };
    Some(ApiError::Refused(crate::library_refusal(&rejection)))
}

/// Where an export read its bundle's manifest, `manifest` (`/`-separated)
/// under the tree's directory `tree`: spelled as content-fs spells the
/// manifest path the export answered, the directory joined segment by
/// segment, so the platform's separator throughout (AIP-155: a retry answers
/// what the export answered, on Windows too).
fn export_source(tree: &str, manifest: &str) -> String {
    manifest
        .split('/')
        .fold(std::path::PathBuf::from(tree), |path, part| path.join(part))
        .display()
        .to_string()
}

/// The answer of a write tried before under `request_id` (its intent
/// `known`, if any): a write done, from its tree as observed now; an export
/// made, from its record (AIP-155: its destination is there now, which a
/// fresh plan would refuse); one that did not complete, failed. None: plan
/// it (a fresh write, or one owed more steps).
fn answered_before<T>(
    (trees, observations): (&library_api::Trees, &library_api::Observations<impl Edge>),
    (request_id, known): (&str, Option<&library_checkout::Known>),
    parent: &str,
    notices: &mut Vec<v1::Notice>,
) -> Option<Result<Wrote<T>, ApiError>> {
    let done = |etag: String, at: String, source: String, notices: Vec<v1::Notice>| Wrote {
        answer: None,
        applied: true,
        etag,
        at,
        parent: parent.to_string(),
        notices,
        source,
    };
    if let Some(known) = known {
        match known.end {
            End::Done => {
                let (etag, at) = done_before(&trees.seen, known, observations);
                return Some(Ok(done(etag, at, String::new(), std::mem::take(notices))));
            }
            End::Superseded => {
                // An outside change overtook it (step 10c U13): its retry
                // is refused stale, and performs nothing.
                return Some(Err(superseded(&trees.seen, known, observations)));
            }
            End::Failed => {
                return Some(Err(ApiError::Failed {
                    detail: format!("the write {request_id} was tried before and did not complete"),
                }));
            }
            End::Pending { .. } => return None,
        }
    }
    let exported = (!request_id.is_empty())
        .then(|| observations.exported(request_id))
        .flatten()?;
    Some(Ok(done(
        String::new(),
        exported.destination,
        exported.source,
        std::mem::take(notices),
    )))
}

/// The refusal of a retry whose intent was superseded (step 10c U13,
/// protos.md §1.3): `PlanStale` of its first step's path, `seen` the token
/// the intent was decided on and `now` the path's token in its tree now.
fn superseded(
    seen: &[(Site, Observed)],
    known: &library_checkout::Known,
    observations: &library_api::Observations<impl Edge>,
) -> ApiError {
    let step = known.intent.steps.first();
    let path = step.map(step_path).unwrap_or_default();
    let decided = step.map_or(0, seen_on);
    let now = seen
        .iter()
        .find(|(_, observed)| observed.tree.root().value.to_vec() == known.root)
        .map_or(0, |(_, observed)| {
            observations.held(&observed.tree).token(&path)
        });
    refused(&library_tree::LibraryTreeRejection::PlanStale {
        path,
        seen: decided,
        now,
    })
}

/// The token a recorded step was decided on.
fn seen_on(step: &library_tree::Step) -> u64 {
    if let Some(replace) = &step.replace {
        return replace.seen_sequence;
    }
    if let Some(trash) = &step.trash {
        return trash.seen_sequence;
    }
    step.r#move.as_ref().map_or(0, |moved| moved.seen_sequence)
}

/// The token and the place of the path a write already done changed (its
/// retry's answer): from its tree as observed now.
fn done_before(
    seen: &[(Site, Observed)],
    known: &library_checkout::Known,
    observations: &library_api::Observations<impl Edge>,
) -> (String, String) {
    let landing = seen
        .iter()
        .find(|(_, observed)| observed.tree.root().value.to_vec() == known.root);
    landing.map_or_else(Default::default, |(site, observed)| {
        let path = known
            .intent
            .steps
            .first()
            .map(step_path)
            .unwrap_or_default();
        let token = observations.held(&observed.tree).token(&path);
        (token.to_string(), format!("{}/{path}", site.tree()))
    })
}

/// The path a recorded step changes.
fn step_path(step: &library_tree::Step) -> String {
    if let Some(replace) = &step.replace {
        return replace.path.clone();
    }
    if let Some(create) = &step.create {
        return create.path.clone();
    }
    if let Some(trash) = &step.trash {
        return trash.path.clone();
    }
    if let Some(purged) = &step.purge {
        return format!("{}/{}", ctxloom_ports::TRASH_DIR, purged.request_id);
    }
    step.r#move
        .as_ref()
        .map(|moved| moved.from.clone())
        .unwrap_or_default()
}

/// The observed tree a write landing `lands` changes: the content's is the
/// project's checkout (else the home's), a layer's its scope's.
pub(crate) fn landing_of(seen: &[(Site, Observed)], lands: Lands) -> Option<Landing<'_>> {
    let checkout = || {
        seen.iter()
            .find(|(site, _)| matches!(site, Site::Checkout { .. }))
    };
    let home = || {
        seen.iter()
            .find(|(site, _)| matches!(site, Site::Home { .. }))
    };
    let found = match lands {
        Lands::Content => checkout().or_else(home),
        Lands::Layer(writes::Scope::Project) => checkout(),
        Lands::Layer(writes::Scope::Home) => home(),
    };
    found.map(|(site, observed)| Landing { site, observed })
}

/// Each write request's target, as its message names it.
macro_rules! write_requests {
    ($($request:ident . $($field:ident).+ $(, $etag:ident)? ;)*) => {
        $(
            impl LibraryWriteRequest for v1::$request {
                // What the request writes, its resource or parent: a
                // request with a field of its own named `target` (a
                // materialize's, where it writes on disk) is not that.
                #[allow(clippy::misnamed_getters)]
                fn target(&self) -> &str {
                    write_requests!(@get self $($field).+)
                }
                fn set_target(&mut self, target: String) {
                    write_requests!(@set self target $($field).+)
                }
                fn set_request_id(&mut self, id: String) {
                    self.request_id = id;
                }
                fn request_id(&self) -> &str {
                    &self.request_id
                }
                fn validate_only(&self) -> bool {
                    self.validate_only
                }
                fn etag(&self) -> &str {
                    write_requests!(@etag self $($etag)?)
                }
            }
        )*
    };
    (@etag $self:ident etag) => { &$self.etag };
    (@etag $self:ident) => { "" };
    (@get $self:ident $field:ident) => { &$self.$field };
    (@get $self:ident $outer:ident . $inner:ident) => {
        $self.$outer.as_ref().map_or("", |outer| outer.$inner.as_str())
    };
    (@set $self:ident $target:ident $field:ident) => { $self.$field = $target };
    (@set $self:ident $target:ident $outer:ident . $inner:ident) => {
        $self.$outer.get_or_insert_with(Default::default).$inner = $target
    };
}

write_requests! {
    CreateBundleRequest.parent;
    ExportBundleRequest.name;
    MoveBundleRequest.name;
    UpdateBundleRequest.bundle.name, etag;
    CreateFragmentRequest.parent;
    UpdateFragmentRequest.fragment.name, etag;
    CreateCommandRequest.parent;
    UpdateCommandRequest.command.name, etag;
    UpdateConfigRequest.name, etag;
    EditConfigRequest.name, etag;
    CreateSkillRequest.parent;
    UpdateMcpServerRequest.mcp_server.name, etag;
    CreateProfileRequest.parent;
    UpdateProfileRequest.profile.name, etag;
    CreateCompanionRequest.parent;
    EnableCompanionRequest.name;
    DisableCompanionRequest.name;
    SetDefaultAgentRequest.parent;
    CreateAgentRequest.parent;
    UpdateAgentRequest.agent.name, etag;
    ImportBundleRequest.parent, etag;
    DeleteBundleRequest.name, etag;
    DeleteFragmentRequest.name, etag;
    DeleteCommandRequest.name, etag;
    DeleteSkillRequest.name, etag;
    DeleteProfileRequest.name, etag;
    DeleteAgentRequest.name, etag;
    DeleteCompanionRequest.name, etag;
    CleanRequest.project, etag;
    MaterializeRequest.project, etag;
    UninstallManageRequest.project, etag;
    RunDoctorRequest.project, etag;
    GetAuthStatusRequest.project;
    CheckManageRequest.project;
    GetInitPromptRequest.project;
    InstallGitignoreRequest.project, etag;
    InitProjectRequest.name, etag;
    InstallManageRequest.project, etag;
    DistillFragmentRequest.name;
    DistillCommandRequest.name;
    CreateRemoteRequest.parent;
    UpdateRemoteRequest.remote.name, etag;
    DeleteRemoteRequest.name, etag;
    SetDefaultRemoteRequest.parent;
    PullDependenciesRequest.parent;
    UpgradeDependenciesRequest.parent;
}

#[cfg(test)]
#[path = "library_writes.test.rs"]
mod tests;
