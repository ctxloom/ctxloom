//! `fragment|command distill` served (step 10b; owner ruling 2026-11-18,
//! evening): the DAEMON owns the model call, as a run, and the distilled
//! form's write FOLLOWS from the run's outcome.
//!
//! 1. The ask, served as a write's preview is (its trees observed, the use
//!    case planned over the recording library, nothing written): what the
//!    model is to be asked (`Library::item_distill_ask`), or that nothing
//!    is (the form fresh and no `force`, or the item `no_distill`), which
//!    is answered at once; and the etag of the form's file, the token of
//!    the path the write will change.
//! 2. The run: a ONE_SHOT on no context (`run --no-context`, OD-38), hosted
//!    as `CreateRun` hosts one, in the caller's directory and environment,
//!    carrying the distill's purpose. The call answers with the run's name,
//!    which the client follows with `WatchRun`.
//! 3. The policy on the recorded answer: a run whose spec carries a distill
//!    PURPOSE, once its turn's answer is recorded and its outcome is not
//!    (`daemon_board::Board::distills_due`), has its answer written, served
//!    as every library write is (observed, planned, intended, one atomic
//!    Replace, recorded), under a request id KEYED by the run
//!    (`distill:<run>`), so a redelivery is the same write, a DUPLICATE; against
//!    the etag of step 1, so a form changed by hand meanwhile is refused stale
//!    (`PlanStale`). Its plan reads the item again: a source whose digest moved
//!    since the ask is refused (`library-application-distill-source-moved`),
//!    as is an implausibly short answer. What it came to is recorded on the
//!    run's stream (`RecordDistillWritten`: the end its watchers are told and
//!    the notices the write said), and `WatchRun` of the run waits past the
//!    run's own end for it.
//!
//! Nothing is held in memory: a daemon that restarts mid-write finds the run
//! due again from the record and writes it, once.

use std::sync::Arc;

use angzarr_embedded::Payload as _;

use coded_diagnostic::Report;
use daemon_proto::v1;
use exit_code::{Exit, ExitClass};
use library_application::{DistillAsk, ItemDistilled, ItemKind};
use run_application::{HostedEnd, RunError};
use run_ports::Edge;

use crate::server::Api;
use daemon_api_contract::ApiError;
use daemon_api_contract::LibraryWriteRequest;

/// The verb a distill's write is recorded under, by its kind.
fn verb(kind: ItemKind) -> &'static str {
    match kind {
        ItemKind::Fragment => "fragment distill",
        ItemKind::Command => "command distill",
    }
}

pub(crate) use library_api::distill::reference_of;

/// The word a purpose names an item's kind by.
fn kind_word(kind: ItemKind) -> &'static str {
    match kind {
        ItemKind::Fragment => "fragment",
        ItemKind::Command => "command",
    }
}

/// The kind a purpose's word names.
fn kind_of(word: &str) -> Option<ItemKind> {
    match word {
        "fragment" => Some(ItemKind::Fragment),
        "command" => Some(ItemKind::Command),
        _ => None,
    }
}

/// The key the write that follows from the run `run` is made under: one
/// per run, so the outcome told twice is one write.
pub(crate) fn write_key(run: &str) -> String {
    format!("distill:{run}")
}

/// The request id of the write keyed `key`: its command id, as the
/// policies key theirs (`sessions::command_id`).
fn keyed_request_id(key: &str) -> String {
    let id = sessions::command_id(key);
    uuid::Uuid::from_slice(&id.value)
        .map(|id| id.to_string())
        .unwrap_or_default()
}

/// A distill's write, as the daemon serves it: the ask's preview, and the
/// reaction's apply. Its own type, so it carries the etag the ask
/// answered, which a distill request does not.
#[derive(Clone, Debug, Default)]
pub(crate) struct ItemWrite {
    pub(crate) name: String,
    /// Its caller, as the distill request carried it.
    pub(crate) caller: Option<v1::Caller>,
    pub(crate) request_id: String,
    pub(crate) validate_only: bool,
    pub(crate) etag: String,
}

impl daemon_proto::Carries for ItemWrite {
    const CALLER: bool = true;
    fn caller(&self) -> Option<&v1::Caller> {
        self.caller.as_ref()
    }
    fn caller_field(&mut self) -> Option<&mut Option<v1::Caller>> {
        Some(&mut self.caller)
    }
}

impl daemon_proto::CarriesMut for ItemWrite {
    fn caller_slot(&mut self) -> &mut Option<v1::Caller> {
        &mut self.caller
    }
}

impl LibraryWriteRequest for ItemWrite {
    fn target(&self) -> &str {
        &self.name
    }
    fn set_target(&mut self, target: String) {
        self.name = target;
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
        &self.etag
    }
}

/// What a distill call asks: the item, whether to distill a fresh form
/// again, and where and in what its caller runs.
#[derive(Clone, Debug)]
pub(crate) struct Asked {
    pub(crate) kind: ItemKind,
    pub(crate) write: ItemWrite,
    pub(crate) force: bool,
}

impl Asked {
    fn of_fragment(request: &v1::DistillFragmentRequest) -> Asked {
        Asked {
            kind: ItemKind::Fragment,
            write: ItemWrite {
                name: request.name.clone(),
                caller: request.caller.clone(),
                ..ItemWrite::default()
            },
            force: request.force,
        }
    }

    fn of_command(request: &v1::DistillCommandRequest) -> Asked {
        Asked {
            kind: ItemKind::Command,
            write: ItemWrite {
                name: request.name.clone(),
                caller: request.caller.clone(),
                ..ItemWrite::default()
            },
            force: request.force,
        }
    }
}

/// A run's end in `report`, exiting `exit`.
fn hosted(report: Report, exit: Exit) -> RunError {
    RunError::Hosted(Box::new(HostedEnd {
        report,
        exit,
        refusals: Vec::new(),
    }))
}

/// The end of a run whose write was not made for `error`: a refusal said
/// in its own diagnostic, a decline; anything else a failure.
pub(crate) fn unwritten(error: &ApiError) -> RunError {
    match error {
        ApiError::Refused(refused) => hosted(
            daemon_api_contract::rejection_of(&refused.refusal)
                .map_or_else(|| Report::of(error), |rejection| rejection.report()),
            Exit::REFUSED,
        ),
        other => hosted(Report::of(other), Exit::FAILED),
    }
}

/// What a distill call answers: the item's distillation as asked, the run
/// that asks the model (empty when nothing is asked), and what was said.
#[derive(Clone, Debug)]
pub(crate) struct Distilling {
    pub(crate) done: ItemDistilled,
    pub(crate) run: String,
    pub(crate) notices: Vec<v1::Notice>,
}

/// How long a distill's watch waits, past its run's own end, for what its
/// answer came to before it ends as the run did.
const OUTCOME_WITHIN: std::time::Duration = std::time::Duration::from_secs(120);

/// How often it looks at the run's stream meanwhile.
const OUTCOME_LOOK: std::time::Duration = std::time::Duration::from_millis(100);

/// A distill run's watch: the run's own events, its end held until the
/// policy has recorded what the answer came to, then the notices the write
/// said and the end it came to (the run's own when it was written). A run
/// that ended in failure has no answer to write, and ends as it did.
pub(crate) struct DistillWatch<E> {
    inner: crate::RunStream,
    edge: Arc<E>,
    runtime: tokio::runtime::Handle,
    run: String,
    /// The item's reference, said when its run fails.
    reference: String,
    queued: std::collections::VecDeque<v1::RunEvent>,
    over: bool,
}

impl<E: Edge + 'static> DistillWatch<E> {
    pub(crate) fn new(
        inner: crate::RunStream,
        edge: Arc<E>,
        (run, reference): (&str, &str),
    ) -> DistillWatch<E> {
        DistillWatch {
            inner,
            edge,
            runtime: tokio::runtime::Handle::current(),
            run: run.to_string(),
            reference: reference_of(reference),
            queued: std::collections::VecDeque::new(),
            over: false,
        }
    }

    /// What the run's stream records of the write, when it does: its end
    /// (empty: written) and its notices.
    fn outcome(&self) -> Option<(Vec<u8>, Vec<Vec<u8>>)> {
        let root = uuid::Uuid::parse_str(&self.run).ok()?;
        let cover = angzarr_embedded::v1::Cover {
            domain: runs::RUN_DOMAIN.to_string(),
            root: Some(angzarr_embedded::v1::Uuid::from_bytes(*root.as_bytes())),
            edition: None,
        };
        let edge = Arc::clone(&self.edge);
        let (tell, heard) = std::sync::mpsc::channel();
        self.runtime.spawn(async move {
            let _ = tell.send(edge.events(&cover, 0).await.unwrap_or_default());
        });
        heard.recv().ok()?.into_iter().find_map(|page| {
            let Some(angzarr_embedded::v1::event_page::Body::Event(any)) = page.body else {
                return None;
            };
            match runs::RunEvent::from_any(&any) {
                Ok(runs::RunEvent::DistillWritten(written)) => Some((written.end, written.notices)),
                _ => None,
            }
        })
    }

    /// The events that stand for the run's end `end`, once what the write
    /// came to is in.
    fn ending(&mut self, end: v1::RunEvent) {
        let failed = matches!(
            &end.event,
            Some(v1::run_event::Event::End(run_end)) if run_end.exit != 0
        );
        let outcome = (!failed).then(|| self.waited_for_outcome()).flatten();
        let Some((written_end, notices)) = outcome else {
            let end = match failed {
                true => self.failed(end),
                false => end,
            };
            self.queued.push_back(end);
            return;
        };
        for notice in notices {
            if let Ok(notice) = <v1::Notice as prost::Message>::decode(notice.as_slice()) {
                self.queued.push_back(v1::RunEvent {
                    event: Some(v1::run_event::Event::Notice(notice)),
                });
            }
        }
        let replaced = <v1::RunEnd as prost::Message>::decode(written_end.as_slice())
            .ok()
            .filter(|_| !written_end.is_empty());
        self.queued.push_back(match replaced {
            Some(run_end) => v1::RunEvent {
                event: Some(v1::run_event::Event::End(run_end)),
            },
            None => end,
        });
    }

    /// A run that came to no answer is a distill that failed, its cause the
    /// run's end.
    fn failed(&self, end: v1::RunEvent) -> v1::RunEvent {
        let Some(v1::run_event::Event::End(run_end)) = &end.event else {
            return end;
        };
        let Some(cause) = run_application::client::error_of(run_end) else {
            return end;
        };
        let failed = hosted(
            Report::of(&library_application::AppError::DistillFailed {
                reference: self.reference.clone(),
                cause: Report::of(&cause),
            }),
            cause.exit(),
        );
        v1::RunEvent {
            event: Some(v1::run_event::Event::End(run_application::client::end_of(
                &failed,
            ))),
        }
    }

    /// What the write came to, once the policy records it, within
    /// [`OUTCOME_WITHIN`].
    fn waited_for_outcome(&self) -> Option<(Vec<u8>, Vec<Vec<u8>>)> {
        let looks = OUTCOME_WITHIN.as_millis() / OUTCOME_LOOK.as_millis();
        for _ in 0..looks {
            if let Some(outcome) = self.outcome() {
                return Some(outcome);
            }
            std::thread::sleep(OUTCOME_LOOK);
        }
        None
    }
}

impl<E: Edge + 'static> Iterator for DistillWatch<E> {
    type Item = v1::RunEvent;

    fn next(&mut self) -> Option<v1::RunEvent> {
        if let Some(event) = self.queued.pop_front() {
            return Some(event);
        }
        if self.over {
            return None;
        }
        let event = self.inner.next()?;
        if matches!(event.event, Some(v1::run_event::Event::End(_))) {
            self.over = true;
            self.ending(event);
            return self.queued.pop_front();
        }
        Some(event)
    }
}

impl<E: Edge + 'static> Api<E> {
    /// `DistillFragment`.
    pub async fn distill_fragment(
        &self,
        request: v1::DistillFragmentRequest,
    ) -> Result<v1::DistillFragmentResponse, ApiError> {
        if request.clear {
            return Err(ApiError::Unimplemented {
                method: "DistillFragment(clear)".to_string(),
            });
        }
        let distilling = self.distill(Asked::of_fragment(&request)).await?;
        Ok(library_api::distill::fragment_response(
            &request.name,
            &distilling.done,
            distilling.run,
            distilling.notices,
        ))
    }

    /// `DistillCommand`.
    pub async fn distill_command(
        &self,
        request: v1::DistillCommandRequest,
    ) -> Result<v1::DistillCommandResponse, ApiError> {
        if request.clear {
            return Err(ApiError::Unimplemented {
                method: "DistillCommand(clear)".to_string(),
            });
        }
        let distilling = self.distill(Asked::of_command(&request)).await?;
        Ok(library_api::distill::command_response(
            &request.name,
            &distilling.done,
            distilling.run,
            distilling.notices,
        ))
    }

    /// The distill `asked`: answered at once when nothing is to be asked,
    /// else its model asked as a hosted run that reacts with the write.
    async fn distill(&self, asked: Asked) -> Result<Distilling, ApiError> {
        self.open()?;
        let one_shots = Arc::clone(self.one_shots()?);
        let (kind, force) = (asked.kind, asked.force);
        let reference = reference_of(&asked.write.name);
        let preview = ItemWrite {
            validate_only: true,
            ..asked.write.clone()
        };
        // The ask, as a preview: its apply is planned too (recorded, never
        // made) to name the path it changes, so its etag is that path's.
        let previewed = self
            .library_write(&preview, verb(kind), |library, flags, apply| {
                let ask = library.item_distill_ask(flags, kind, &reference, force);
                if let (true, Ok(DistillAsk::Model(model))) = (apply, &ask.result) {
                    let probe = library.item_distilled(
                        flags,
                        kind,
                        &reference,
                        (&model.digest, &model.request),
                    );
                    if let Err(error) = probe.result {
                        return library_application::Outcome {
                            result: Err(error),
                            notices: ask.notices,
                        };
                    }
                }
                ask
            })
            .await?;
        let notices = previewed.notices.clone();
        let model = match previewed.answer {
            Some(DistillAsk::Model(model)) => model,
            Some(DistillAsk::Done(done)) => {
                return Ok(Distilling {
                    done,
                    run: String::new(),
                    notices,
                })
            }
            None => {
                return Err(ApiError::Failed {
                    detail: "the distill was answered with nothing".to_string(),
                })
            }
        };
        let run_request = v1::CreateRunRequest {
            parent: previewed.parent.clone(),
            mode: v1::run::Mode::OneShot as i32,
            prompt: model.request.clone(),
            no_context: true,
            caller: asked.write.caller.clone(),
            ..v1::CreateRunRequest::default()
        };
        let mut host = self.one_shot_request(&run_request).await?;
        // The write follows from the run's recorded answer, by this purpose,
        // so it survives a daemon that restarts (owner 2026-11-28).
        host.request.distill = Some(runs::DistillPurpose {
            reference: asked.write.name.clone(),
            kind: kind_word(kind).to_string(),
            digest: model.digest.clone(),
            etag: previewed.etag.clone(),
            cwd: asked
                .write
                .caller
                .as_ref()
                .map(|c| c.cwd.clone())
                .unwrap_or_default(),
        });
        let run = crate::runs::run_created(&run_request, one_shots.create(host));
        if let Some(end) = run.end.as_ref() {
            let error = run_application::client::error_of(end);
            return Err(error.map_or_else(
                || ApiError::Failed {
                    detail: "the distill's run ended before it was placed".to_string(),
                },
                |error| ApiError::diagnosed(&error),
            ));
        }
        let mut notices = notices;
        notices.extend(run.notices.iter().cloned());
        Ok(Distilling {
            done: model.done,
            run: run.name,
            notices,
        })
    }
}

impl<E: Edge + 'static> Api<E> {
    /// The write a distill run's recorded answer is due (the policy,
    /// owner 2026-11-28): the answer written as the item's distilled form,
    /// under the request id keyed by the run, and what it came to recorded
    /// on the run's stream (`RecordDistillWritten`: the end its watchers are
    /// told, empty when it was written, and the notices the write said).
    pub async fn write_distilled(&self, due: &daemon_board::DistillDue) -> Result<(), ApiError> {
        let purpose = &due.purpose;
        let kind = kind_of(&purpose.kind).ok_or_else(|| ApiError::Failed {
            detail: format!("a distill of the kind {}", purpose.kind),
        })?;
        let write = ItemWrite {
            name: purpose.reference.clone(),
            caller: Some(v1::Caller {
                cwd: purpose.cwd.clone(),
                ..v1::Caller::default()
            }),
            request_id: keyed_request_id(&write_key(&due.run)),
            validate_only: false,
            etag: purpose.etag.clone(),
        };
        let reference = reference_of(&write.name);
        let (digest, answer) = (purpose.digest.as_str(), due.answer.as_str());
        let wrote = self
            .write_planned(&write, verb(kind), |library, flags, _| {
                library.item_distilled(flags, kind, &reference, (digest, answer))
            })
            .await;
        self.record_outcome(&due.run, wrote.map(|wrote| wrote.notices))
            .await
    }

    /// What a distill's write came to, recorded on its run's stream
    /// (`RecordDistillWritten`): the notices it said, or the end its refusal
    /// makes of the run, which the run's watchers are told.
    pub(crate) async fn record_outcome(
        &self,
        run: &str,
        wrote: Result<Vec<v1::Notice>, ApiError>,
    ) -> Result<(), ApiError> {
        let (end, notices) = match wrote {
            Ok(notices) => (Vec::new(), notices),
            Err(error) => {
                let end = run_application::client::end_of(&unwritten(&error));
                (prost::Message::encode_to_vec(&end), Vec::new())
            }
        };
        let fact = runs::RecordDistillWritten {
            end,
            notices: notices.iter().map(prost::Message::encode_to_vec).collect(),
        };
        let root = uuid::Uuid::parse_str(run).map_err(ApiError::failed)?;
        let cover = angzarr_embedded::v1::Cover {
            domain: runs::RUN_DOMAIN.to_string(),
            root: Some(angzarr_embedded::v1::Uuid::from_bytes(*root.as_bytes())),
            edition: None,
        };
        self.parts
            .edge
            .handle(sessions::fact_book(cover, &fact, &format!("distill:{run}")))
            .await
            .map(|_| ())
            .map_err(ApiError::failed)
    }
}

#[cfg(test)]
#[path = "distill.test.rs"]
mod tests;
