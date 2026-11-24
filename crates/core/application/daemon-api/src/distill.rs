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
//!    reacting ([`DistillReaction`]). The call answers with the run's name,
//!    which the client follows with `WatchRun`.
//! 3. The reaction, once the run's engine is done ([`follows`], pure): no
//!    answer is a distill that failed (`library-application-distill-
//!    failed`, its cause the run's end); an answer is the write, served as
//!    every library write is (observed, planned, intended, one atomic
//!    Replace, recorded), under a request id KEYED by the run
//!    (`distill:<run>`), so a redelivery of the outcome is the same write,
//!    a DUPLICATE, answered as applied; against the etag of step 1, so a
//!    form changed by hand meanwhile is refused stale (`PlanStale`). Its
//!    plan reads the item again: a source whose digest moved since the ask
//!    is refused (`library-application-distill-source-moved`), as is an
//!    implausibly short answer. What the write came to is the run's end,
//!    which its watchers see only after it (`daemon_application::Reaction`).
//!
//! The reaction is in memory, as the one-shot is: a daemon that dies
//! during the run loses the run (the sweep records it lost) and writes
//! nothing, so the form there before is kept; the client is told the run
//! ended without its end.

use std::sync::{Arc, Weak};

use coded_diagnostic::Report;
use daemon_application::{Reacted, Reaction};
use daemon_proto::v1;
use exit_code::{Exit, ExitClass};
use library_application::{AppError, DistillAsk, ItemDistilled, ItemKind};
use run_application::{Answer, Done, HostedEnd, Placement, RunError};
use run_ports::Edge;

use crate::library_writes::LibraryWriteRequest;
use crate::refusal::ApiError;
use crate::server::Api;

/// The verb a distill's write is recorded under, by its kind.
fn verb(kind: ItemKind) -> &'static str {
    match kind {
        ItemKind::Fragment => "fragment distill",
        ItemKind::Command => "command distill",
    }
}

pub(crate) use library_api::distill::reference_of;

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

/// What a distill call answers: the item's distillation as asked, the run
/// that asks the model (empty when nothing is asked), and what was said.
#[derive(Clone, Debug)]
pub(crate) struct Distilling {
    pub(crate) done: ItemDistilled,
    pub(crate) run: String,
    pub(crate) notices: Vec<v1::Notice>,
}

/// What follows from a distill run's outcome: the reaction's pure half.
#[derive(Debug)]
pub(crate) enum Follows {
    /// The answer written as the item's form, under the write's key.
    Write { key: String, answer: String },
    /// The run came to no answer: the distill failed, nothing is written.
    Failed(AppError),
}

/// What follows from the distill of `reference`'s run, placed `placed`,
/// ending in `result`.
pub(crate) fn follows(
    reference: &str,
    placed: Option<&Placement>,
    result: &Result<Answer, RunError>,
) -> Follows {
    let failed = |cause: &RunError| {
        Follows::Failed(AppError::DistillFailed {
            reference: reference.to_string(),
            cause: Report::of(cause),
        })
    };
    match (result, placed) {
        (Ok(answer), Some(placed)) => Follows::Write {
            key: write_key(&placed.run),
            answer: answer.answer.clone(),
        },
        // An answer from a run never placed is no run's: nothing to key it by.
        (Ok(_), None) => failed(&RunError::NotStarted {
            detail: "the distill's run answered before it was placed".to_string(),
        }),
        (Err(error), _) => failed(error),
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
            crate::refusal::rejection_of(&refused.refusal)
                .map_or_else(|| Report::of(error), |rejection| rejection.report()),
            Exit::REFUSED,
        ),
        other => hosted(Report::of(other), Exit::FAILED),
    }
}

/// What follows from a distill's run: its answer written as the item's
/// distilled form, through the API that hosts it.
pub(crate) struct DistillReaction<E> {
    pub(crate) api: Weak<Api<E>>,
    pub(crate) asked: Asked,
    /// The digest of the source the model was sent.
    pub(crate) digest: String,
}

impl<E: Edge + 'static> Reaction for DistillReaction<E> {
    fn react<'a>(&'a self, placed: Option<&'a Placement>, done: Done<Answer>) -> Reacted<'a> {
        Box::pin(async move {
            let Done {
                result,
                mut notices,
            } = done;
            let reference = reference_of(&self.asked.write.name);
            let result = match follows(&reference, placed, &result) {
                Follows::Failed(error) => {
                    let exit = error.exit();
                    Err(hosted(Report::of(&error), exit))
                }
                Follows::Write { key, answer } => match self.write(&key, &answer).await {
                    Ok(said) => {
                        notices.extend(said.iter().map(run_application::client::noticed));
                        result
                    }
                    Err(error) => Err(unwritten(&error)),
                },
            };
            Done { result, notices }
        })
    }
}

impl<E: Edge + 'static> DistillReaction<E> {
    /// The answer written, under `key`: what the write said.
    async fn write(&self, key: &str, answer: &str) -> Result<Vec<v1::Notice>, ApiError> {
        let api = self.api.upgrade().ok_or_else(|| ApiError::Failed {
            detail: "the daemon that hosted the distill is gone".to_string(),
        })?;
        let write = ItemWrite {
            request_id: keyed_request_id(key),
            validate_only: false,
            ..self.asked.write.clone()
        };
        let (kind, reference) = (self.asked.kind, reference_of(&write.name));
        let digest = self.digest.as_str();
        let wrote = api
            .write_planned(&write, verb(kind), |library, flags, _| {
                library.item_distilled(flags, kind, &reference, (digest, answer))
            })
            .await?;
        Ok(wrote.notices)
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
        let me = self.me.get().cloned().ok_or_else(|| ApiError::Failed {
            detail: "this daemon writes no distilled form".to_string(),
        })?;
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
        let host = self.one_shot_request(&run_request).await?;
        let reaction = Arc::new(DistillReaction {
            api: me,
            asked: Asked {
                write: ItemWrite {
                    etag: previewed.etag.clone(),
                    ..asked.write.clone()
                },
                ..asked
            },
            digest: model.digest.clone(),
        });
        let run = crate::runs::run_created(&run_request, one_shots.create_reacting(host, reaction));
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

#[cfg(test)]
#[path = "distill.test.rs"]
mod tests;
