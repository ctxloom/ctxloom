//! The TERMINAL runs the daemon prepares (U5: T9, S3, S17, S20). An
//! interactive session is planned, enqueued, named, minted and set up by the
//! daemon exactly as a session the command ran itself was (the run use
//! case's `Runs::interactive`, over the daemon's one store, in the
//! environment its client sent, S7), with ONE difference: its engine is not
//! started here. Its one `Launch` (T10) is handed to the client that asked,
//! the FRONTEND, which takes the session's lock, starts it on the user's
//! terminal and reports its start and its exit as the run's facts.
//!
//! [`Frontended`] is that difference: the runner launcher the daemon's
//! `Runs::interactive` drives. Its start hands the launch over
//! ([`Told::prepared`]) and waits for the frontend's start report; its wait
//! waits for the exit report. So everything the launcher and the run use
//! case do around a terminal session (the `RecordRunnerStarted` and `RecordRunExited`
//! it records, which the frontend's reports already made and are
//! DUPLICATES, the checkout released WIP-safe, the incarnation ended) is
//! done here, unchanged, once the frontend says the TUI exited.
//!
//! While it waits it watches for what the frontend must be told: the
//! session closed (S20) puts a `stop` on the run's watch, and the frontend
//! asks its TUI to stop, then kills it after the grace. A frontend that died
//! without reporting (its session's lock free and no exit recorded) ends
//! the wait as a kill.
//!
//! [`Terminals`] holds them, for the API: [`Terminals::create`] answers once
//! the launch is ready (or the run ended before that), and
//! [`Terminals::watch`] streams what came after: a stop, what the run
//! noticed, its end.

use std::collections::{BTreeMap, VecDeque};
use std::sync::{Arc, Condvar, Mutex, MutexGuard, PoisonError};
use std::time::Duration;

use daemon_proto::v1;
use run_ports::{
    Exit, Launch, LaunchAt, LaunchFailure, LaunchKind, Limits, LockProbe, ProgramNotFound,
    RunLocks, RunnerLauncher, Running, TurnCommand, TurnOutput,
};

/// How long a prepared session waits for its frontend's start report before
/// its launch is given up as never started.
pub const START_WITHIN: Duration = Duration::from_secs(60);

/// How often a waiting session reads its run for the frontend's reports.
pub const POLL: Duration = Duration::from_millis(100);

/// The signal a frontend that died without reporting is said ended by.
const KILLED: i32 = 9;

/// How many ended sessions are kept for a late watch.
const ENDED_KEPT: usize = 16;

/// A session to prepare: what a hosted one-shot is asked (its project,
/// where and in what its client runs, S7, and what it asks), the run
/// interactive.
pub type TerminalRequest = crate::HostRequest;

/// What prepares a session: the root's, which runs it on a thread of its
/// own over the daemon's store with a [`Frontended`] launcher telling
/// `told`, and tells `told` its end.
pub trait TerminalHost: Send + Sync {
    fn host(&self, request: TerminalRequest, told: Told);
}

/// What the frontend's reports came to, as the run's and the session's
/// streams hold them.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Seen {
    /// The engine was spawned, with this pid.
    pub started: Option<u32>,
    /// It exited.
    pub exited: Option<Exit>,
    /// It never started.
    pub failed: Option<LaunchFailure>,
    /// The session was closed (S20).
    pub closed: bool,
}

/// Where a prepared session's reports are read: its run's stream and its
/// session's, at `at`. The root reads the daemon's store.
pub trait FrontendReports: Send + Sync {
    fn seen(&self, at: &LaunchAt) -> Seen;
}

/// The time a waiting session waits in: a pause, and how long since it
/// began. The root's is the clock; a test's is a counter.
pub trait Waits: Send + Sync {
    fn elapsed(&self) -> Duration;
    fn wait(&self, pause: Duration);
}

/// A session's run as the API names it.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Placed {
    /// The run's id (text).
    pub run: String,
    /// The session's resource name (`projects/{project}/sessions/{harp}`).
    pub name: String,
    pub engine: String,
}

#[derive(Default)]
struct State {
    prepared: Option<(Placed, Launch)>,
    events: Vec<v1::RunEvent>,
    ended: bool,
    /// Its frontend was told to stop.
    stopped: bool,
}

#[derive(Default)]
struct Entry {
    state: Mutex<State>,
    changed: Condvar,
}

impl Entry {
    fn state(&self) -> MutexGuard<'_, State> {
        self.state.lock().unwrap_or_else(PoisonError::into_inner)
    }

    fn change(&self, change: impl FnOnce(&mut State)) {
        change(&mut self.state());
        self.changed.notify_all();
    }
}

/// What a prepared session tells the daemon of itself.
#[derive(Clone)]
pub struct Told(Arc<Entry>);

impl Told {
    /// Its launch is ready for its frontend.
    pub fn prepared(&self, placed: Placed, launch: Launch) {
        self.0
            .change(|state| state.prepared = Some((placed, launch)));
    }

    /// Its frontend is to stop its TUI, for `reason`, once.
    pub fn stop(&self, reason: &str) {
        self.0.change(|state| {
            if state.stopped || state.ended {
                return;
            }
            state.stopped = true;
            state.events.push(v1::RunEvent {
                event: Some(v1::run_event::Event::Stop(v1::RunStop {
                    reason: reason.to_string(),
                    grace_ms: u32::try_from(run_ports::STOP_GRACE.as_millis()).unwrap_or(u32::MAX),
                })),
            });
        });
    }

    /// It is over: what it noticed, then its end.
    pub fn done(&self, events: Vec<v1::RunEvent>) {
        self.0.change(|state| {
            state.events.extend(events);
            state.ended = true;
        });
    }
}

/// What a `create` came to.
#[derive(Clone, Debug, PartialEq)]
pub enum Created {
    /// Ready: the launch for its frontend, watched by its run.
    Prepared(Placed, Box<Launch>),
    /// Over before its launch was ready: what it noticed, then its end.
    Ended(Vec<v1::RunEvent>),
}

/// A session's events after its launch, from the first, as they come.
pub struct Watch {
    entry: Arc<Entry>,
    next: usize,
}

impl Iterator for Watch {
    type Item = v1::RunEvent;

    /// The next event, waited for; none once its end was given. Blocks (the
    /// API's edge runs it on a blocking thread).
    fn next(&mut self) -> Option<v1::RunEvent> {
        let mut state = self.entry.state();
        loop {
            if let Some(event) = state.events.get(self.next) {
                self.next += 1;
                return Some(event.clone());
            }
            if state.ended {
                return None;
            }
            state = self
                .entry
                .changed
                .wait(state)
                .unwrap_or_else(PoisonError::into_inner);
        }
    }
}

/// The sessions the daemon prepares, by their run's id (text).
pub struct Terminals {
    host: Box<dyn TerminalHost>,
    table: Mutex<BTreeMap<String, Arc<Entry>>>,
    ended: Mutex<VecDeque<String>>,
    creating: Mutex<usize>,
}

impl Terminals {
    pub fn new(host: Box<dyn TerminalHost>) -> Terminals {
        Terminals {
            host,
            table: Mutex::new(BTreeMap::new()),
            ended: Mutex::new(VecDeque::new()),
            creating: Mutex::new(0),
        }
    }

    fn table(&self) -> MutexGuard<'_, BTreeMap<String, Arc<Entry>>> {
        self.table.lock().unwrap_or_else(PoisonError::into_inner)
    }

    fn creating(&self) -> MutexGuard<'_, usize> {
        self.creating.lock().unwrap_or_else(PoisonError::into_inner)
    }

    /// Prepare `request`, and answer once its launch is ready or it ended.
    /// Blocks until then (the API's edge runs it on a blocking thread).
    pub fn create(&self, request: TerminalRequest) -> Created {
        let entry = Arc::new(Entry::default());
        *self.creating() += 1;
        self.host.host(request, Told(Arc::clone(&entry)));
        let prepared = {
            let mut state = entry.state();
            while state.prepared.is_none() && !state.ended {
                state = entry
                    .changed
                    .wait(state)
                    .unwrap_or_else(PoisonError::into_inner);
            }
            state.prepared.clone()
        };
        let created = match prepared {
            Some((placed, launch)) => {
                self.table().insert(placed.run.clone(), Arc::clone(&entry));
                Created::Prepared(placed, Box::new(launch))
            }
            None => Created::Ended(entry.state().events.clone()),
        };
        *self.creating() -= 1;
        self.forget_ended();
        created
    }

    /// Forget the oldest ended sessions past [`ENDED_KEPT`].
    fn forget_ended(&self) {
        let mut table = self.table();
        let mut ended = self.ended.lock().unwrap_or_else(PoisonError::into_inner);
        for (run, entry) in table.iter() {
            if entry.state().ended && !ended.contains(run) {
                ended.push_back(run.clone());
            }
        }
        while ended.len() > ENDED_KEPT {
            if let Some(run) = ended.pop_front() {
                table.remove(&run);
            }
        }
    }

    /// The prepared session of `run`, if the daemon prepared (or kept) it.
    pub fn get(&self, run: &str) -> Option<Placed> {
        let entry = self.table().get(run).cloned()?;
        let state = entry.state();
        state.prepared.as_ref().map(|(placed, _)| placed.clone())
    }

    /// The events of `run`'s session, from its first.
    pub fn watch(&self, run: &str) -> Option<Watch> {
        let entry = self.table().get(run).cloned()?;
        Some(Watch { entry, next: 0 })
    }

    /// How many it prepares or holds open now: asked for, or prepared and
    /// not ended (each is a client the daemon serves, for its idleness).
    pub fn hosting(&self) -> usize {
        let open = self
            .table()
            .values()
            .filter(|entry| !entry.state().ended)
            .count();
        *self.creating() + open
    }

    /// Tell `run`'s frontend to stop its TUI, for `reason`. Whether the
    /// daemon holds it open.
    pub fn stop(&self, run: &str, reason: &str) -> bool {
        let Some(entry) = self.table().get(run).cloned() else {
            return false;
        };
        let open = !entry.state().ended;
        Told(entry).stop(reason);
        open
    }
}

/// The runner launcher of a session the daemon prepares: its terminal launch
/// handed to the frontend, its start and exit the frontend's reports. Every
/// other launch is not this launcher's (refused, nothing started); a
/// program is found as `finder` finds it.
pub struct Frontended {
    pub told: Told,
    /// The harp of the project the session is in, which its resource name
    /// is rooted on.
    pub project_harp: String,
    pub reports: Arc<dyn FrontendReports>,
    pub waits: Arc<dyn Waits>,
    pub locks: Arc<dyn RunLocks>,
    /// The project the session's lock is in.
    pub project_dir: String,
    /// The path of the lock of the session of a harp, which its frontend
    /// takes.
    pub lock_path: Arc<dyn Fn(&str) -> String + Send + Sync>,
    pub finder: Arc<dyn RunnerLauncher>,
}

fn not_this(what: &str) -> LaunchFailure {
    LaunchFailure {
        deterministic: true,
        detail: format!("a session the daemon prepares starts no {what}"),
    }
}

impl RunnerLauncher for Frontended {
    fn launch(&self, _: &TurnCommand, _: &Limits) -> Result<Box<dyn Running>, LaunchFailure> {
        Err(not_this("turn"))
    }

    fn launch_terminal(&self, _: &TurnCommand) -> Result<Box<dyn Running>, LaunchFailure> {
        Err(not_this("terminal of its own"))
    }

    fn find_program(&self, command: &TurnCommand) -> Result<String, ProgramNotFound> {
        self.finder.find_program(command)
    }

    /// Hand the terminal launch to its frontend, and wait for its start
    /// report (at most [`START_WITHIN`]): its pid, or why it never started.
    fn start(&self, launch: &Launch, _: &Limits) -> Result<Box<dyn Running>, LaunchFailure> {
        if launch.kind != LaunchKind::Terminal {
            return Err(not_this("child or turn"));
        }
        let mut launch = launch.clone();
        launch.session_lock = (self.lock_path)(&launch.at.harp);
        let name = sessions::SessionName::new(&self.project_harp, &launch.at.harp)
            .map(|name| name.to_string())
            .unwrap_or_default();
        let placed = Placed {
            run: launch.at.run.clone(),
            name,
            engine: launch.engine.clone(),
        };
        let at = launch.at.clone();
        self.told.prepared(placed, launch);
        let since = self.waits.elapsed();
        loop {
            let seen = self.reports.seen(&at);
            if let Some(failed) = seen.failed {
                return Err(failed);
            }
            if let Some(pid) = seen.started {
                return Ok(Box::new(Frontend {
                    at,
                    pid,
                    told: self.told.clone(),
                    reports: Arc::clone(&self.reports),
                    waits: Arc::clone(&self.waits),
                    locks: Arc::clone(&self.locks),
                    project_dir: self.project_dir.clone(),
                }));
            }
            if self.waits.elapsed().saturating_sub(since) >= START_WITHIN {
                return Err(LaunchFailure {
                    deterministic: false,
                    detail: format!(
                        "no frontend started the session within {}",
                        duration_text::format_exact(START_WITHIN)
                    ),
                });
            }
            self.waits.wait(POLL);
        }
    }
}

/// A session its frontend started: waited for until its exit is reported.
struct Frontend {
    at: LaunchAt,
    pid: u32,
    told: Told,
    reports: Arc<dyn FrontendReports>,
    waits: Arc<dyn Waits>,
    locks: Arc<dyn RunLocks>,
    project_dir: String,
}

fn output(exit: Exit) -> TurnOutput {
    TurnOutput {
        exit,
        stdout: Vec::new(),
        stderr: Vec::new(),
        timed_out: false,
        truncated: false,
        drain_incomplete: false,
    }
}

impl Running for Frontend {
    fn pid(&self) -> u32 {
        self.pid
    }

    /// The exit its frontend reports; the session's close told to the
    /// frontend as a stop on the way. A frontend gone without reporting (its
    /// session's lock free, and no exit recorded once that was seen: a
    /// frontend records its exit before it lets the lock go) is a kill.
    fn finish(self: Box<Self>) -> Result<TurnOutput, LaunchFailure> {
        loop {
            let seen = self.reports.seen(&self.at);
            if let Some(exit) = seen.exited {
                return Ok(output(exit));
            }
            if seen.closed {
                self.told.stop("closed");
            }
            if self.locks.session(&self.project_dir, &self.at.harp) == LockProbe::Free {
                let exit = self.reports.seen(&self.at).exited.unwrap_or(Exit {
                    code: None,
                    signal: Some(KILLED),
                });
                return Ok(output(exit));
            }
            self.waits.wait(POLL);
        }
    }
}

#[cfg(test)]
#[path = "terminal.test.rs"]
mod tests;
