//! A daemon dying under a live TUI (owner, 2026-11-14; U5 found that U6's
//! sweep does not cover it): the daemon that prepared an interactive session
//! ([`crate::Terminals`]) would have ended its incarnation and let its
//! checkout go once its frontend reported the TUI's exit; killed, nobody
//! would. So a new daemon ADOPTS, at its start ([`Daemon::start_up`]), every
//! run its sweep leaves (a run owed a runner or started, whose owner its
//! locks say is alive: a live frontend, holding the session's lock, or the
//! run's own lock it took before it asked, U5's start-report race), and
//! takes over its END PATH, as the original daemon's would have run it:
//!
//! - the frontend's exit report (its `RecordRunExited`, or its `RecordLaunchFailed`)
//!   ends the run: its incarnation is ended (`runner-exit`, or
//!   `launch-failed` for one never started) and its checkout let go
//!   WIP-safe (a clean one removed, one holding work left and said; never
//!   forced, never a prune);
//! - a frontend gone without reporting (the session's lock free and the
//!   run's own lock not held, no end in the run's stream once that was seen:
//!   a frontend records its exit before it lets its locks go) is a kill, as
//!   the original daemon's `Frontended` read it: `RecordRunExited{signal: 9}`,
//!   keyed `exit:<run>` as the frontend's own, so a late report of it is a
//!   DUPLICATE, then the same end;
//! - a run never started whose own lock is left free (its frontend died
//!   before its start) is the sweep's, `launch-orphaned`.
//!
//! Adoption is the start's only: a run a later daemon round sees is this
//! daemon's own or already adopted. A draining daemon hands over its
//! children and leaves its TUIs untouched; the daemon after it adopts them
//! at its start, so the two never act on one run.

use std::collections::BTreeMap;
use std::sync::Arc;

use angzarr_embedded::v1::{event_page, EventPage, Uuid};
use angzarr_embedded::{Catalog, Payload, Processed, ReadStore, Store};
use pending_launches::{PendingLaunch, StartedRun};
use run_ports::{Edge, LockProbe};
use runs_application::{Facts, Identity, RunnerFact};

use crate::daemon::Daemon;
use run_launcher::{Released, Swept};

/// The signal a frontend gone without reporting is said ended by, as the
/// original daemon's `Frontended` says it.
const KILLED: i32 = 9;

/// Why an adopted run's incarnation ended: its engine ran and exited, or it
/// never started.
const RUNNER_EXIT: &str = "runner-exit";
const LAUNCH_FAILED: &str = "launch-failed";

/// A run this daemon adopted: where its owner's locks are, as the store
/// said them at its start (its session's place is its mint's, for a run owed
/// a runner as for one started, so one started and ended before it was ever
/// listed started still lets its checkout go in its project).
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct Adopted {
    pub(crate) run: Uuid,
    pub(crate) session: String,
    pub(crate) incarnation: u32,
    pub(crate) project_dir: String,
    pub(crate) harp: String,
    pub(crate) launcher_lock: String,
}

/// The run's stream, as far as its end path reads it.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
struct Read {
    /// Its `RecordRunnerStarted`'s workspace, once started.
    started: Option<String>,
    /// Its end's cause, once ended.
    ended: Option<runs::EndCause>,
}

fn text(root: &Uuid) -> String {
    uuid::Uuid::from_slice(&root.value)
        .map(|id| id.hyphenated().to_string())
        .unwrap_or_default()
}

fn read(pages: &[EventPage]) -> Read {
    let mut read = Read::default();
    for page in pages {
        let Some(event_page::Body::Event(any)) = &page.body else {
            continue;
        };
        match runs::RunEvent::from_any(any) {
            Ok(runs::RunEvent::RunnerStartedRecorded(started)) => {
                read.started = Some(started.workspace);
            }
            Ok(runs::RunEvent::RunEnded(ended)) => read.ended = Some(ended.cause),
            _ => {}
        }
    }
    read
}

impl<E: Edge> Daemon<E> {
    /// Adopt every run the start's sweep left (`owed`, `started`): their
    /// owner is alive, or not proven gone, and none is this daemon's.
    pub(crate) fn adopt(&mut self, owed: &[PendingLaunch], started: &[StartedRun]) {
        for launch in owed {
            self.adopt_one(Adopted {
                run: launch.run.clone(),
                session: launch.session.clone(),
                incarnation: launch.incarnation,
                project_dir: launch.project_dir.clone(),
                harp: launch.harp.clone(),
                launcher_lock: launch.launcher_lock.clone(),
            });
        }
        for run in started {
            self.adopt_one(Adopted {
                run: run.run.clone(),
                session: run.session.clone(),
                incarnation: run.incarnation,
                project_dir: run.project_dir.clone(),
                harp: run.harp.clone(),
                launcher_lock: run.launcher_lock.clone(),
            });
        }
    }

    fn adopt_one(&mut self, adopted: Adopted) {
        let run = text(&adopted.run);
        if self.adopted.contains_key(&run) {
            return;
        }
        (self.say)(&format!(
            "{}: adopted its run {run}: its owner is alive; its end is this daemon's",
            adopted.session
        ));
        self.adopted.insert(run, adopted);
    }

    /// The runs it adopted, by their run's root (text).
    pub(crate) fn adopted_runs(&self) -> &BTreeMap<String, Adopted> {
        &self.adopted
    }

    /// The adopted runs' end path, one round: each whose run ended has its
    /// incarnation ended and its checkout let go; each whose frontend is
    /// gone without reporting is ended as a kill (started) or orphaned
    /// (never started). Whether it did anything.
    pub(crate) async fn end_the_adopted<S: ReadStore + Store + Processed>(
        &mut self,
        store: &S,
        catalog: &Catalog,
    ) -> bool {
        if self.adopted.is_empty() {
            return false;
        }
        // Where a run is, once its session's mint is seen.
        let started = match self.caught_up_launches(store, catalog).await {
            Ok((_, started)) => started,
            Err(why) => {
                (self.say)(&format!("the adopted runs' catch-up failed: {why}"));
                return false;
            }
        };
        let mut busy = false;
        let runs: Vec<String> = self.adopted.keys().cloned().collect();
        for run in runs {
            let Some(mut adopted) = self.adopted.get(&run).cloned() else {
                continue;
            };
            if let Some(now) = started.iter().find(|s| s.run == adopted.run) {
                adopted.project_dir.clone_from(&now.project_dir);
                adopted.harp.clone_from(&now.harp);
                self.adopted.insert(run.clone(), adopted.clone());
            }
            busy |= self.end_one(&run, &adopted).await;
        }
        busy
    }

    /// One adopted run's round: whether it did anything.
    async fn end_one(&mut self, run: &str, adopted: &Adopted) -> bool {
        let Some(read) = self.run_read(adopted).await else {
            return false;
        };
        if let Some(cause) = read.ended {
            self.ended(run, adopted, &read, cause).await;
            return true;
        }
        let own_held = !adopted.launcher_lock.is_empty()
            && self.run_locks.probe(&adopted.launcher_lock) == LockProbe::Held;
        if own_held {
            return false;
        }
        match read.started {
            Some(_) => {
                let session_free = !adopted.harp.is_empty()
                    && self.run_locks.session(&adopted.project_dir, &adopted.harp)
                        == LockProbe::Free;
                if !session_free {
                    return false;
                }
                // A frontend records its exit before it lets its locks go:
                // read once more, after the probe.
                let Some(again) = self.run_read(adopted).await else {
                    return false;
                };
                if again.ended.is_none() {
                    self.killed(adopted).await;
                }
                true
            }
            None => {
                let own_free = !adopted.launcher_lock.is_empty()
                    && self.run_locks.probe(&adopted.launcher_lock) == LockProbe::Free;
                if !own_free {
                    return false;
                }
                // Its frontend died before its start: the sweep's.
                let owed = [PendingLaunch {
                    run: adopted.run.clone(),
                    session: adopted.session.clone(),
                    incarnation: adopted.incarnation,
                    mode: runs::RunMode::Interactive,
                    launcher_lock: adopted.launcher_lock.clone(),
                    project_dir: adopted.project_dir.clone(),
                    harp: adopted.harp.clone(),
                }];
                if let Err(why) = self.swept(&owed, &[]).await {
                    (self.say)(&format!("{}: its sweep failed: {why}", adopted.session));
                    return false;
                }
                self.adopted.remove(run);
                true
            }
        }
    }

    /// `adopted`'s run stream, as its end path reads it; none (said) when it
    /// cannot be read.
    async fn run_read(&mut self, adopted: &Adopted) -> Option<Read> {
        let cover = angzarr_embedded::v1::Cover {
            domain: runs::RUN_DOMAIN.to_string(),
            root: Some(adopted.run.clone()),
            edition: None,
        };
        match self.edge.events(&cover, 0).await {
            Ok(pages) => Some(read(&pages)),
            Err(e) => {
                (self.say)(&format!("{}: its run's stream: {e}", adopted.session));
                None
            }
        }
    }

    /// The frontend gone without reporting: its run ended as a kill, keyed
    /// as the frontend's own exit report.
    async fn killed(&mut self, adopted: &Adopted) {
        let session = uuid::Uuid::parse_str(&adopted.session).unwrap_or_default();
        let who = Identity {
            run: adopted.run.clone(),
            session: Uuid::from_bytes(*session.as_bytes()),
            incarnation: adopted.incarnation,
            harp: adopted.harp.clone(),
            depth: 0,
        };
        let fact = RunnerFact::RunExited(runs::RecordRunExited {
            code: 0,
            signal: KILLED,
        });
        match Facts::new(Arc::clone(&self.edge)).record(&who, fact).await {
            Ok(_) => (self.say)(&format!(
                "{}: its frontend is gone without reporting; its run ended as a kill",
                adopted.session
            )),
            Err(e) => (self.say)(&format!("{}: its kill not recorded: {e}", adopted.session)),
        }
    }

    /// The adopted run `run` ended (`cause`): its incarnation ended, its
    /// checkout let go WIP-safe, and it is no longer adopted.
    async fn ended(&mut self, run: &str, adopted: &Adopted, read: &Read, cause: runs::EndCause) {
        let orphaned = cause == runs::EndCause::LaunchOrphaned;
        if !orphaned {
            let why = match read.started {
                Some(_) => RUNNER_EXIT,
                None => LAUNCH_FAILED,
            };
            let facts = Facts::new(Arc::clone(&self.edge));
            match facts
                .incarnation_ended(&adopted.session, adopted.incarnation, why)
                .await
            {
                Ok(_) => (self.say)(&format!(
                    "{}: its adopted run ended; its incarnation {} ended {why}",
                    adopted.session, adopted.incarnation
                )),
                Err(e) => (self.say)(&format!(
                    "{}: its incarnation's end not recorded: {e}",
                    adopted.session
                )),
            }
            let workspace = read.started.clone().unwrap_or_default();
            self.let_go_of_checkout(adopted, workspace);
        }
        self.adopted.remove(run);
    }

    /// An adopted run's checkout let go, WIP-safe, as the sweep lets a lost
    /// run's go (only one under ctxloom's own `worktrees`).
    fn let_go_of_checkout(&mut self, adopted: &Adopted, workspace: String) {
        let Some(releases) = self.releases() else {
            return;
        };
        let swept = Swept {
            run: adopted.run.clone(),
            session: adopted.session.clone(),
            incarnation: adopted.incarnation,
            started: true,
            project_dir: adopted.project_dir.clone(),
            workspace,
            released: Released::Nothing,
        };
        match releases.release(&swept) {
            Released::Nothing => {}
            Released::Removed(path) => {
                (self.say)(&format!("{}: its checkout {path} removed", adopted.session));
            }
            Released::Left { path, why } => {
                (self.say)(&format!(
                    "{}: its checkout {path} left: {why}",
                    adopted.session
                ));
            }
        }
    }
}

#[cfg(test)]
#[path = "adopt.test.rs"]
mod tests;
