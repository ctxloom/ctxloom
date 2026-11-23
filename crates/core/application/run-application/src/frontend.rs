//! The interactive session's FRONTEND (U5: T9, S3, S17): what `ctxloom run`
//! does once the user's daemon has planned and prepared the session and
//! answered its one `Launch` (T10). The daemon decided everything about the
//! session; the frontend only puts the engine on the user's terminal, exactly
//! as the launch says, and tells the daemon what became of it:
//!
//! 1. it takes the session's lock (at once or refused, `sessions-session-
//!    running`, exit 48) and holds it for the TUI's whole life (rulings 85,
//!    92), so the session reads live;
//! 2. it starts the launch on the terminal (its program, arguments, WHOLE
//!    environment and directory, nothing inherited);
//! 3. it reports the start (`RecordRunnerStarted`, with the pid the spawn gave),
//!    waits for the TUI's exit, and reports it (`RecordRunExited`), or reports a
//!    launch that never started (`RecordLaunchFailed`);
//! 4. it lets the lock go, only after the exit was reported.
//!
//! Each report is a runs fact keyed by its run (`started:<run>`,
//! `exit:<run>`, `launch-failed:<run>`), so a report delivered twice, or one
//! the daemon also records, is a DUPLICATE. Each is ONE attempt (slice 4, a
//! daemon dying under a live TUI, owner 2026-11-14, replacing U5's 60 s
//! retry): the adapter behind [`Reports`] finds the daemon serving, or
//! starts one, and delivers it behind what the session's hold already holds,
//! or HOLDS it there (as a hook's facts are) when no up-to-date daemon takes
//! it; the frontend's next report, or any command's flush, delivers what is
//! held, in order. A report held or not delivered is said, and changes
//! nothing of the TUI.
//!
//! The start-report race (U5, owner 2026-11-14): the run's OWN lock (its
//! launcher lock) is the frontend's, taken BEFORE it asked the daemon to
//! plan ([`Frontend::run_lock`]), so a daemon started between the plan and
//! the start report reads the run's owner alive and sweeps nothing; it is
//! let go once the start report is recorded, or, when it never is, at the
//! frontend's end.
//!
//! The concurrency (the TUI waited for while the daemon's watch is read for
//! a stop) is the composition root's: this is the order of the steps, over
//! ports, so a test forces each race it is about.

use library_application::Notice;
use run_ports::{
    BoxFuture, Delivery, HeldLock, Launch, LaunchFailure, Limits, Running, TurnOutput,
};
use session_ports::{Held, SessionLock};

use crate::error::RunError;
use crate::one_shot::{Done, Ended};

/// What the frontend tells the daemon of its run.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Report {
    /// The engine was spawned, with this pid.
    Started { pid: u32 },
    /// The engine exited: its code, or the signal that ended it.
    Exited {
        code: Option<i32>,
        signal: Option<i32>,
    },
    /// The engine was never started: `deterministic` says a retry would fail
    /// the same way.
    NotStarted { deterministic: bool, detail: String },
}

/// What the daemon prepared: the session's run, its resource names, and the
/// one launch the frontend starts.
#[derive(Clone, Debug)]
pub struct Prepared {
    /// The run's resource name (`projects/{project}/runs/{run}`).
    pub run: String,
    /// The session's resource name (`projects/{project}/sessions/{harp}`).
    pub session: String,
    pub launch: Launch,
}

/// What became of one report.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Reported {
    /// The daemon answered it, finally: applied, a duplicate, parked or
    /// refused.
    Delivered(Delivery),
    /// It is in the session's hold, for the next attempt (the frontend's
    /// next report, or any command's flush) to deliver in order: why.
    Held { why: String },
    /// It is neither recorded nor held (the hold could not keep it): why.
    Lost { why: String },
}

/// Where the frontend's reports go: ONE attempt at `report` for the run
/// `launch` starts, to the daemon serving now (found, or started when none
/// serves), behind what the session's hold holds; held when no up-to-date
/// daemon takes it.
pub trait Reports: Send + Sync {
    fn deliver<'a>(&'a self, launch: &'a Launch, report: &'a Report) -> BoxFuture<'a, Reported>;
}

/// The terminal the launch is started on: spawned (an `Err` started
/// nothing), then waited for to its exit. The root's is `run_terminal`'s,
/// its wait on a blocking thread; a stop it was made with asks the TUI to
/// stop, then kills it after the grace (s4-Q10).
pub trait Terminal: Send + Sync {
    fn start(&self, launch: &Launch, limits: &Limits) -> Result<Box<dyn Running>, LaunchFailure>;
    fn wait(&self, running: Box<dyn Running>) -> BoxFuture<'_, Result<TurnOutput, LaunchFailure>>;
}

/// What the frontend runs over.
pub struct Frontend<'a> {
    pub locks: &'a mut dyn SessionLock,
    pub terminal: &'a dyn Terminal,
    pub reports: &'a dyn Reports,
    pub limits: Limits,
    /// The run's own lock, which the frontend took before it asked the
    /// daemon to plan (the start-report race, U5): let go once the start
    /// report is recorded, else at the frontend's end. None: the daemon
    /// holds its own.
    pub run_lock: Option<HeldLock>,
}

/// What the TUI came to, before the daemon's last word: its end, and what the
/// frontend itself noticed (a report it could not deliver).
pub type Started = Done<Ended>;

impl Frontend<'_> {
    /// Run `prepared` on the terminal: the lock taken, the engine started and
    /// waited for, each step reported, the lock let go.
    pub async fn run(&mut self, prepared: &Prepared) -> Started {
        let mut notices = Vec::new();
        let result = self.session(prepared, &mut notices).await;
        Done { result, notices }
    }

    async fn session(
        &mut self,
        prepared: &Prepared,
        notices: &mut Vec<Notice>,
    ) -> Result<Ended, RunError> {
        let launch = &prepared.launch;
        let lock = match self.locks.hold(&launch.at.harp) {
            Ok(lock) => lock,
            Err(error) => {
                let report = Report::NotStarted {
                    deterministic: true,
                    detail: error.report().to_string(),
                };
                self.report(launch, &report, notices).await;
                self.run_lock.take();
                return Err(RunError::Lock(error));
            }
        };
        let running = match self.terminal.start(launch, &self.limits) {
            Ok(running) => running,
            Err(failure) => return Err(self.not_started(launch, failure, lock, notices).await),
        };
        let started = Report::Started { pid: running.pid() };
        if self.report(launch, &started, notices).await {
            // Its start is recorded: the session's lock alone says its
            // owner is alive from here on.
            self.run_lock.take();
        }
        let output = match self.terminal.wait(running).await {
            Ok(output) => output,
            Err(failure) => return Err(self.not_started(launch, failure, lock, notices).await),
        };
        let exited = Report::Exited {
            code: output.exit.code,
            signal: output.exit.signal,
        };
        self.report(launch, &exited, notices).await;
        // Let go only now: the session reads live until its exit is known.
        self.run_lock.take();
        drop(lock);
        Ok(Ended {
            harp: launch.at.harp.clone(),
            session: prepared.session.clone(),
            engine: launch.engine.clone(),
            code: output.exit.code,
        })
    }

    /// The launch that did not start (or whose wait broke): reported, the
    /// locks let go after.
    async fn not_started(
        &mut self,
        launch: &Launch,
        failure: LaunchFailure,
        lock: Held,
        notices: &mut Vec<Notice>,
    ) -> RunError {
        let report = Report::NotStarted {
            deterministic: failure.deterministic,
            detail: failure.detail.clone(),
        };
        self.report(launch, &report, notices).await;
        self.run_lock.take();
        drop(lock);
        RunError::NotStarted {
            detail: failure.detail,
        }
    }

    /// One attempt at `report` (held when no up-to-date daemon takes it):
    /// whether the daemon recorded it (applied, or a duplicate). Anything
    /// else is said.
    async fn report(&self, launch: &Launch, report: &Report, notices: &mut Vec<Notice>) -> bool {
        match self.reports.deliver(launch, report).await {
            Reported::Delivered(Delivery::Applied | Delivery::Duplicate) => true,
            Reported::Delivered(delivery) => {
                notices.push(undelivered(report, &delivery));
                false
            }
            Reported::Held { why } => {
                notices.push(held(report, &why));
                false
            }
            Reported::Lost { why } => {
                notices.push(Notice::Finding {
                    text: format!(
                        "could not tell the daemon {}, nor hold it: {why}",
                        what(report)
                    ),
                    remedy: "the next daemon's start ends the session's run once its lock is free"
                        .to_string(),
                });
                false
            }
        }
    }
}

/// What a report tells, as a notice names it.
fn what(report: &Report) -> &'static str {
    match report {
        Report::Started { .. } => "the session's start",
        Report::Exited { .. } => "the session's exit",
        Report::NotStarted { .. } => "that the session never started",
    }
}

/// What a report kept in the session's hold is said as (a DRAFT, the
/// owner's to word, as `hook-cli-held` is).
fn held(report: &Report, why: &str) -> Notice {
    Notice::Finding {
        text: format!("held {} for the next daemon: {why}", what(report)),
        remedy: "the next ctxloom command delivers it, in order".to_string(),
    }
}

/// What a report that did not reach the daemon is said as.
fn undelivered(report: &Report, delivery: &Delivery) -> Notice {
    let what = what(report);
    let why = match delivery {
        Delivery::Refused { detail } | Delivery::Unavailable { detail } => detail.clone(),
        Delivery::Parked => "the daemon parked it".to_string(),
        Delivery::TimedOut => "no daemon answered in time".to_string(),
        Delivery::NoSuchSession => "the daemon has no record of this session".to_string(),
        Delivery::Applied | Delivery::Duplicate => String::new(),
    };
    Notice::Finding {
        text: format!("could not tell the daemon {what}: {why}"),
        remedy: "the next daemon's start ends the session's run once its lock is free".to_string(),
    }
}

#[cfg(test)]
#[path = "frontend.test.rs"]
mod tests;
