//! The session's directory, written from the events: a [`CatchUp`] over the
//! session and run streams of the sessions it is asked to watch. A session
//! minted is its `metadata.json`; every session and run event is one line of
//! `records/session.jsonl` or `records/runs.jsonl`, keyed by its stream and
//! sequence so a book handed again writes nothing twice.
//!
//! Only watched sessions are written: the store holds the project's whole
//! history, and a session whose directory was deleted (leaving is deleting
//! `.ctxloom`) is not written back from it.

use std::collections::{BTreeMap, BTreeSet};
use std::sync::{Mutex, MutexGuard, PoisonError};

use angzarr_embedded::angzarr_router::error::CodedError;
use angzarr_embedded::angzarr_router::hpb::{ProjectRequest, ProjectResponse};
use angzarr_embedded::v1::{event_page, EventBook};
use angzarr_embedded::{
    Any, Catalog, CatchUp, Payload, Processed, ProjectorHandler, ReadStore, Store,
};
use pending_launches::bug;
use run_ports::SessionRecords;
use runs::{RunEvent, RUN_DOMAIN};
use serde_json::{json, Value};
use sessions::{SessionEvent, SESSION_DOMAIN};

use crate::error::LaunchError;
use crate::projection::Projection;
use crate::transcript::{next_seq, TRANSCRIPT};

/// The session's identity and where it came from.
pub const METADATA: &str = "metadata.json";
/// Every event of the session stream, a line each.
pub const SESSION_RECORD: &str = "records/session.jsonl";
/// Every event of the session's runs, a line each.
pub const RUNS_RECORD: &str = "records/runs.jsonl";
/// How far the transcript is projected: the last session-stream page whose
/// lines it holds. The projector owns this file, so a projector that starts
/// again writes no line twice.
pub const TRANSCRIPT_POSITION: &str = "records/transcript-position.json";

/// What the projector holds: where to write, whom to write for, and the names
/// it has learned.
struct State<R> {
    records: R,
    /// The sessions to write, by id text.
    watched: BTreeSet<String>,
    /// A session's harp, by id text, once its mint was seen.
    harps: BTreeMap<String, String>,
    /// A run stream's session, by root text, once its enqueue was seen.
    runs: BTreeMap<String, String>,
    /// A session's engine, by id text, once its mint was seen.
    engines: BTreeMap<String, String>,
    /// The transcript of each session being written, by id text.
    transcripts: BTreeMap<String, Projection>,
}

/// The projector on angzarr-embedded's projector port.
pub struct RecordingModel<R>(Mutex<State<R>>);

impl<R: SessionRecords> RecordingModel<R> {
    fn state(&self) -> MutexGuard<'_, State<R>> {
        self.0.lock().unwrap_or_else(PoisonError::into_inner)
    }

    /// Write the directory of `session` (an id) from now on.
    pub fn watch(&self, session: &str) {
        self.state().watched.insert(session.to_string());
    }
}

impl<R: SessionRecords + 'static> ProjectorHandler for RecordingModel<R> {
    fn name(&self) -> &str {
        "session-records"
    }

    async fn dispatch(&self, request: &ProjectRequest) -> Result<ProjectResponse, CodedError> {
        let book = request.events.clone().unwrap_or_default();
        self.state().project(&book)?;
        Ok(ProjectResponse { projection: None })
    }
}

/// The seconds the page was recorded at; 0 for a page that says none.
fn recorded_at(page: &angzarr_embedded::v1::EventPage) -> i64 {
    page.meta
        .as_ref()
        .and_then(|meta| meta.recorded_at.as_ref())
        .map_or(0, |at| at.seconds)
}

fn root_text(book: &EventBook) -> String {
    book.cover
        .as_ref()
        .and_then(|cover| cover.root.as_ref())
        .and_then(|root| uuid::Uuid::from_slice(&root.value).ok())
        .map(|id| id.hyphenated().to_string())
        .unwrap_or_default()
}

fn failed(error: impl ToString) -> CodedError {
    CodedError::bug(
        "run-launcher-record",
        [("ext_err_txt".to_string(), error.to_string())],
    )
}

impl<R: SessionRecords> State<R> {
    fn project(&mut self, book: &EventBook) -> Result<(), CodedError> {
        let domain = book.cover.as_ref().map_or("", |c| c.domain.as_str());
        let root = root_text(book);
        let events = book.pages.iter().filter_map(|page| match &page.body {
            Some(event_page::Body::Event(any)) => Some((page.sequence, recorded_at(page), any)),
            _ => None,
        });
        match domain {
            SESSION_DOMAIN if self.watched.contains(&root) => {
                for (sequence, seconds, any) in events {
                    self.session_event(&root, sequence, seconds, any)?;
                }
            }
            RUN_DOMAIN => {
                for (sequence, _, any) in events {
                    self.run_event(&root, sequence, any)?;
                }
            }
            _ => {}
        }
        Ok(())
    }

    fn session_event(
        &mut self,
        root: &str,
        sequence: u64,
        seconds: i64,
        any: &Any,
    ) -> Result<(), CodedError> {
        let event = SessionEvent::from_any(any).map_err(|e| bug(&e))?;
        if let SessionEvent::SessionMinted(minted) = &event {
            self.harps.insert(root.to_string(), minted.harp.clone());
            self.engines.insert(root.to_string(), minted.engine.clone());
            let metadata = json!({
                "v": 1,
                "id": root,
                "harp": minted.harp,
                "project_id": minted.project_id,
                "project_dir": minted.project_dir,
                "output_dir": minted.output_dir,
                "engine": minted.engine,
                "origin": format!("{:?}", minted.origin),
            });
            self.records
                .write(&minted.harp, METADATA, format!("{metadata:#}\n").as_bytes())
                .map_err(failed)?;
        }
        let Some(harp) = self.harps.get(root).cloned() else {
            return Ok(());
        };
        let mut line = json!({"v": 1, "seq": sequence, "event": event_name(any)});
        line.as_object_mut()
            .into_iter()
            .for_each(|o| o.extend(session_detail(&event)));
        self.line(&harp, SESSION_RECORD, root, sequence, line)?;
        self.transcribe(root, &harp, sequence, seconds, &event)
    }

    /// Fold `event` into the session's transcript and write the lines it adds
    /// (the one writer of `transcripts/transcript.jsonl`), then the position.
    /// A projector that starts again resumes from the transcript's line count
    /// and the position file.
    fn transcribe(
        &mut self,
        root: &str,
        harp: &str,
        sequence: u64,
        seconds: i64,
        event: &SessionEvent,
    ) -> Result<(), CodedError> {
        if !self.transcripts.contains_key(root) {
            let engine = self.engines.get(root).cloned().unwrap_or_default();
            let lines = self
                .records
                .read(harp, TRANSCRIPT)
                .map_err(failed)?
                .map_or(0, |held| next_seq(&held));
            let position = self
                .records
                .read(harp, TRANSCRIPT_POSITION)
                .map_err(failed)?
                .and_then(|held| serde_json::from_slice::<Value>(&held).ok())
                .and_then(|held| held["page"].as_u64());
            self.transcripts.insert(
                root.to_string(),
                Projection::resume(harp, root, &engine, lines, position),
            );
        }
        let Some(projection) = self.transcripts.get_mut(root) else {
            return Ok(());
        };
        let before = projection.high_water();
        let lines = projection.fold(sequence, seconds, event);
        if projection.high_water() == before {
            return Ok(());
        }
        let page = projection.high_water();
        let written = self.write_transcript(harp, &lines, page);
        if written.is_err() {
            // Held where it was: the next attempt reloads from the files.
            self.transcripts.remove(root);
        }
        written
    }

    fn write_transcript(
        &mut self,
        harp: &str,
        lines: &[String],
        page: Option<u64>,
    ) -> Result<(), CodedError> {
        if !lines.is_empty() {
            self.records
                .append(harp, TRANSCRIPT, lines.concat().as_bytes())
                .map_err(failed)?;
        }
        self.records
            .write(
                harp,
                TRANSCRIPT_POSITION,
                format!("{}\n", json!({"page": page})).as_bytes(),
            )
            .map_err(failed)
    }

    fn run_event(&mut self, root: &str, sequence: u64, any: &Any) -> Result<(), CodedError> {
        let event = RunEvent::from_any(any).map_err(|e| bug(&e))?;
        if let RunEvent::RunEnqueued(enqueued) = &event {
            if self.watched.contains(&enqueued.session) {
                self.runs.insert(root.to_string(), enqueued.session.clone());
            }
        }
        let Some(session) = self.runs.get(root) else {
            return Ok(());
        };
        let Some(harp) = self.harps.get(session).cloned() else {
            return Ok(());
        };
        let mut line = json!({"v": 1, "run": root, "seq": sequence, "event": event_name(any)});
        line.as_object_mut()
            .into_iter()
            .for_each(|o| o.extend(run_detail(&event)));
        self.line(&harp, RUNS_RECORD, root, sequence, line)
    }

    /// Append `line`, unless the file already holds `stream`'s `sequence`.
    fn line(
        &mut self,
        harp: &str,
        file: &str,
        stream: &str,
        sequence: u64,
        line: Value,
    ) -> Result<(), CodedError> {
        let held = self.records.read(harp, file).map_err(failed)?;
        let seen = held.as_deref().is_some_and(|held| {
            String::from_utf8_lossy(held)
                .lines()
                .filter_map(|l| serde_json::from_str::<Value>(l).ok())
                .any(|l| l["seq"] == sequence && l.get("run").is_none_or(|run| run == stream))
        });
        if seen {
            return Ok(());
        }
        self.records
            .append(harp, file, format!("{line}\n").as_bytes())
            .map_err(failed)
    }
}

/// `any`'s message name: the type URL after its last dot.
fn event_name(any: &Any) -> &str {
    any.type_url
        .rsplit(['.', '/'])
        .next()
        .unwrap_or(&any.type_url)
}

/// The fields of a session event a record carries beside its name.
fn session_detail(event: &SessionEvent) -> serde_json::Map<String, Value> {
    let detail = match event {
        SessionEvent::SessionMinted(minted) => json!({"harp": minted.harp}),
        SessionEvent::IncarnationOwed(owed) => json!({"incarnation": owed.incarnation}),
        SessionEvent::EngineSessionBound(bound) => {
            json!({"native_id": bound.native_id, "transcript_path": bound.transcript_path})
        }
        _ => json!({}),
    };
    detail.as_object().cloned().unwrap_or_default()
}

/// The fields of a run event a record carries beside its name. A credential's
/// digest is never one.
fn run_detail(event: &RunEvent) -> serde_json::Map<String, Value> {
    let detail = match event {
        RunEvent::RunEnqueued(e) => json!({
            "session": e.session,
            "incarnation": e.incarnation,
            "mode": format!("{:?}", e.mode),
        }),
        RunEvent::RunnerAttachedRecorded(e) => {
            json!({"runner_id": e.runner_id, "protocol": e.protocol})
        }
        RunEvent::RunnerStartedRecorded(e) => {
            json!({"runtime": e.runtime, "workspace": e.workspace, "pid": e.pid})
        }
        RunEvent::TurnStartedRecorded(e) => json!({"turn": e.turn}),
        RunEvent::TurnCompletedRecorded(e) => json!({
            "turn": e.turn,
            "native_key": e.native_key,
            "answer_ref": e.answer_ref,
            "exit_code": e.exit_code,
        }),
        RunEvent::TurnFailedRecorded(e) => json!({
            "turn": e.turn,
            "class": format!("{:?}", e.class),
            "detail": e.detail,
        }),
        RunEvent::RunEnded(e) => json!({
            "cause": format!("{:?}", e.cause),
            "exit_code": e.exit_code,
            "detail": e.detail,
        }),
        _ => json!({}),
    };
    detail.as_object().cloned().unwrap_or_default()
}

/// The recorder: the projector, caught up from a store a step at a time.
pub struct Recorder<R: SessionRecords + 'static> {
    catch: CatchUp<RecordingModel<R>>,
}

impl<R: SessionRecords + 'static> Recorder<R> {
    /// A recorder that writes through `records`; it watches no session yet.
    pub fn new(records: R) -> Recorder<R> {
        let model = RecordingModel(Mutex::new(State {
            records,
            watched: BTreeSet::new(),
            harps: BTreeMap::new(),
            runs: BTreeMap::new(),
            engines: BTreeMap::new(),
            transcripts: BTreeMap::new(),
        }));
        Recorder {
            catch: CatchUp::new(&[SESSION_DOMAIN, RUN_DOMAIN], model)
                .expect("the records read the session and run domains"),
        }
    }

    /// Write the directory of `session` (an id) from now on.
    pub fn watch(&self, session: &str) {
        self.catch.projector().watch(session);
    }

    /// One step: write what the streams hold that was not yet written. A book
    /// that could not be written is held where it was and is the error.
    pub async fn step<S: ReadStore + Store + Processed>(
        &mut self,
        store: &S,
        catalog: &Catalog,
    ) -> Result<(), LaunchError> {
        let caught = self
            .catch
            .step(store, catalog)
            .await
            .map_err(|e| LaunchError::Record {
                ext_err_txt: e.to_string(),
            })?;
        match caught.failures().first() {
            Some((_, failure)) => Err(LaunchError::Record {
                ext_err_txt: failure.to_string(),
            }),
            None => Ok(()),
        }
    }
}

#[cfg(test)]
#[path = "recorder.test.rs"]
mod tests;
