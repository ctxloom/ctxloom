//! The one-shots the daemon hosts (U4, T7; owner s4-Q8: the engine's TURN
//! command, through the one launcher and the one `Launch`). A one-shot is
//! `run_application::Runs::one_shot` run in the daemon, over its one store,
//! in the environment its client sent (S7): planned, enqueued (its checks
//! the run's, ruling 73), its name claimed and its session minted, its
//! session's lock and its run's held by the daemon, its engine started and
//! its one turn taken, its end recorded, its checkout let go. The root runs
//! each on a thread of its own ([`OneShotHost`]); this is what the API and
//! the launcher share of them:
//!
//! - [`OneShots::create`]: the run hosted, answered once it is placed (its
//!   client then watches it) or ended before that;
//! - [`OneShots::watch`]: its events, in order, as they come: what it
//!   noticed, its reply, a stop, its end. A client that watched none, or
//!   stopped watching, changes nothing: the run goes on to its end (owner
//!   s4-Q7);
//! - [`OneShots::stop`]: its engine stopped (the daemon's `Step::Stop`, once
//!   its run's stream holds a `StopRequested`): its tree asked to stop, then
//!   killed after the grace, and its end the stop's
//!   (`run-application-stopped`, 128 plus the signal's number).

use std::collections::{BTreeMap, VecDeque};
use std::sync::{Arc, Condvar, Mutex, MutexGuard, PoisonError};
use std::time::Duration;

use daemon_proto::v1;
use library_application::Flags;
use run_application::client::{answered, end_of, notice_of, status_of};
use run_application::{Answer, Done, OneShot, Placement, RunError};

/// How long a stopped one-shot's tree has before it is killed (the
/// launcher's `stop_grace`), as its client is told.
pub const STOP_GRACE_MS: u32 = 5_000;

/// The signal a one-shot the daemon stops of its own is stopped as (its
/// end, `--even-if-live`, or a drain's period passed): its client exits
/// 128 plus its number.
pub const STOP_SIGNAL: &str = "SIGTERM";

/// How many ended one-shots are kept for a late watch.
const ENDED_KEPT: usize = 16;

/// The project a hosted one-shot is in, as the daemon's index knows it.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct HostProject {
    pub id: String,
    pub harp: String,
    pub root: String,
}

/// A one-shot to host: its project (none: its client named none), where
/// and in what its client runs (S7), and what it asks.
#[derive(Clone, Debug, Default)]
pub struct HostRequest {
    pub project: Option<HostProject>,
    pub cwd: String,
    pub env: BTreeMap<String, String>,
    pub flags: Flags,
    pub request: OneShot,
    /// The client's program (owner, 2026-11-18): what the run's hooks and
    /// statusline run and its engine's `PATH` leads with; none, the
    /// daemon's own.
    pub program: Option<crate::Program>,
    /// A terminal run's own lock, held by its frontend since before it
    /// asked (U5's start-report race): the run journals it as its launcher
    /// lock while it reads held. None: a one-shot, or an older frontend.
    pub launcher_lock: Option<String>,
}

/// What hosts a one-shot: the root's, which runs it on a thread of its own
/// over the daemon's store, telling `told` where it was placed and how it
/// ended, and answers how to stop its engine.
pub trait OneShotHost: Send + Sync {
    fn host(&self, request: HostRequest, told: Told) -> Box<dyn OneShotControl>;
}

/// What follows from a hosted one-shot's outcome (step 10b, owner ruling
/// 2026-11-18: distill's write follows from its run): handed the outcome
/// once, after its engine ended (a stop's end when its engine was stopped),
/// with where it was placed (none: it ended before), and answering what the
/// run ends as. Its watchers see nothing of the end before it returns, and
/// the run is hosted until then. It is in memory, as the one-shot is: a
/// daemon that dies before it is handed the outcome never hands it (its run
/// is then the sweep's, lost, and nothing follows from it).
pub trait Reaction: Send + Sync {
    fn react<'a>(&'a self, placed: Option<&'a Placement>, done: Done<Answer>) -> Reacted<'a>;
}

/// What a [`Reaction`] answers, once it has done what follows: the run's
/// end. A future, so what follows is driven where the one-shot is (the
/// root's runtime; the core spawns nothing).
pub type Reacted<'a> = std::pin::Pin<Box<dyn std::future::Future<Output = Done<Answer>> + 'a>>;

/// How a hosted one-shot's engine is stopped.
pub trait OneShotControl: Send + Sync {
    /// Ask its engine to stop (its tree asked, then killed after the
    /// grace); the run then ends as any whose engine exited.
    fn stop(&self);
}

/// A one-shot as its entry holds it.
#[derive(Default)]
struct State {
    placement: Option<Placement>,
    events: Vec<v1::RunEvent>,
    ended: bool,
    /// Why its engine is being stopped, once it is.
    stopping: Option<String>,
    control: Option<Box<dyn OneShotControl>>,
    /// What follows from its outcome, until it is handed it.
    reaction: Option<Arc<dyn Reaction>>,
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

    /// Change the state, and wake whoever waits on it.
    fn change(&self, change: impl FnOnce(&mut State)) {
        change(&mut self.state());
        self.changed.notify_all();
    }
}

/// What a hosted one-shot tells the daemon of itself: where it was placed,
/// and how it ended.
#[derive(Clone)]
pub struct Told(Arc<Entry>);

impl Told {
    /// It was placed: its run and session made, its engine about to start.
    pub fn placed(&self, placement: &Placement) {
        let placement = placement.clone();
        self.0.change(|state| state.placement = Some(placement));
    }

    /// It is over: what it noticed, then its reply and its end. A one-shot
    /// whose engine the daemon stopped ends as the stop says, whatever its
    /// turn came to (ruling 114).
    /// It is awaited by whatever drives the one-shot: a reaction's work
    /// (a write) is done in it.
    pub async fn done(&self, done: Done<Answer>) {
        let Done {
            mut result,
            notices,
        } = done;
        let (reaction, placement) = {
            let mut state = self.0.state();
            if let Some(signal) = &state.stopping {
                result = Err(RunError::Stopped {
                    signal: signal.clone(),
                    status: status_of(signal),
                });
            }
            (state.reaction.take(), state.placement.clone())
        };
        // Outside the lock: a reaction may take its time (a write), and the
        // run is not ended, so not watched to its end, until it returns.
        let done = Done { result, notices };
        let Done { result, notices } = match reaction {
            Some(reaction) => reaction.react(placement.as_ref(), done).await,
            None => done,
        };
        self.0.change(|state| {
            let event = |event| v1::RunEvent { event: Some(event) };
            state.events.extend(
                notices
                    .iter()
                    .map(|notice| event(v1::run_event::Event::Notice(notice_of(notice)))),
            );
            match &result {
                Ok(answer) => {
                    state
                        .events
                        .push(event(v1::run_event::Event::Reply(answer.answer.clone())));
                    state
                        .events
                        .push(event(v1::run_event::Event::End(answered())));
                }
                Err(error) => state
                    .events
                    .push(event(v1::run_event::Event::End(end_of(error)))),
            }
            state.ended = true;
        });
    }
}

/// What a `create` came to.
#[derive(Clone, Debug, PartialEq)]
pub enum Created {
    /// Placed: watch it by its run.
    Placed(Placement),
    /// Over before it was placed: what it noticed, then its end.
    Ended(Vec<v1::RunEvent>),
}

/// A hosted one-shot as `GetRun` reads it.
#[derive(Clone, Debug, PartialEq)]
pub struct Hosted {
    pub placement: Placement,
    pub ended: bool,
}

/// A hosted one-shot's events, from its first, as they come.
pub struct Watch {
    entry: Arc<Entry>,
    next: usize,
}

impl Watch {
    /// The next event, waited for; none once its end was given. Blocks
    /// until there is one (the API's edge runs it on a blocking thread).
    pub fn next_event(&mut self) -> Option<v1::RunEvent> {
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

impl Iterator for Watch {
    type Item = v1::RunEvent;

    fn next(&mut self) -> Option<v1::RunEvent> {
        self.next_event()
    }
}

/// The one-shots the daemon hosts, by their run's root (text).
pub struct OneShots {
    host: Box<dyn OneShotHost>,
    table: Mutex<BTreeMap<String, Arc<Entry>>>,
    /// The ended ones, oldest first, kept for a late watch.
    ended: Mutex<VecDeque<String>>,
    /// The ones asked for and not yet placed or ended.
    creating: Mutex<usize>,
}

impl OneShots {
    pub fn new(host: Box<dyn OneShotHost>) -> OneShots {
        OneShots {
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

    /// Host `request`, and answer once it is placed or ended. Blocks until
    /// then (the API's edge runs it on a blocking thread).
    pub fn create(&self, request: HostRequest) -> Created {
        self.hosted(request, None)
    }

    /// Host `request` as [`OneShots::create`] does, its end what `reaction`
    /// answers of its outcome.
    pub fn create_reacting(&self, request: HostRequest, reaction: Arc<dyn Reaction>) -> Created {
        self.hosted(request, Some(reaction))
    }

    fn hosted(&self, request: HostRequest, reaction: Option<Arc<dyn Reaction>>) -> Created {
        let entry = Arc::new(Entry::default());
        entry.change(|state| state.reaction = reaction);
        *self.creating() += 1;
        let control = self.host.host(request, Told(Arc::clone(&entry)));
        entry.change(|state| state.control = Some(control));
        let placed = {
            let mut state = entry.state();
            while state.placement.is_none() && !state.ended {
                state = entry
                    .changed
                    .wait(state)
                    .unwrap_or_else(PoisonError::into_inner);
            }
            state.placement.clone()
        };
        let created = match placed {
            Some(placement) => {
                self.table()
                    .insert(placement.run.clone(), Arc::clone(&entry));
                Created::Placed(placement)
            }
            None => Created::Ended(entry.state().events.clone()),
        };
        *self.creating() -= 1;
        self.forget_ended();
        created
    }

    /// Forget the oldest ended one-shots past [`ENDED_KEPT`].
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

    /// The hosted one-shot of `run`, if the daemon hosts (or kept) it.
    pub fn get(&self, run: &str) -> Option<Hosted> {
        let entry = self.table().get(run).cloned()?;
        let state = entry.state();
        Some(Hosted {
            placement: state.placement.clone()?,
            ended: state.ended,
        })
    }

    /// The events of `run`'s one-shot, from its first.
    pub fn watch(&self, run: &str) -> Option<Watch> {
        let entry = self.table().get(run).cloned()?;
        Some(Watch { entry, next: 0 })
    }

    /// How many it hosts now: asked for, placed or running, and not ended.
    pub fn hosting(&self) -> usize {
        *self.creating() + self.live().len()
    }

    /// The placed ones not ended, in run order.
    pub fn live(&self) -> Vec<Placement> {
        self.table()
            .values()
            .filter_map(|entry| {
                let state = entry.state();
                (!state.ended).then(|| state.placement.clone()).flatten()
            })
            .collect()
    }

    /// What an ask of the engine of `session`'s live one-shot is recorded
    /// with (step 8, D7: one-shots ask too): its run, its engine, the agent
    /// it was planned for and its engine's `approval_timeout`, as it was
    /// placed. None for a session none of them is, and for the human's
    /// terminal session, whose asks are hers to answer there (S28).
    pub fn asker(&self, session: &str) -> Option<crate::Asker> {
        self.live()
            .into_iter()
            .find(|placed| placed.session == session && !placed.interactive)
            .map(|placed| crate::Asker {
                session: placed.session,
                run: placed.run,
                engine: placed.engine,
                agent: placed.agent,
                approval_timeout: placed.approval_timeout,
            })
    }

    /// The runs of the live ones whose engine is not being stopped yet.
    pub fn unstopped(&self) -> Vec<String> {
        self.table()
            .iter()
            .filter(|(_, entry)| {
                let state = entry.state();
                !state.ended && state.stopping.is_none()
            })
            .map(|(run, _)| run.clone())
            .collect()
    }

    /// Stop `run`'s engine for `reason` (the signal its client heard), once:
    /// its watchers told (a `stop` event), its control asked. Whether this
    /// stopped it (false: not hosted, ended, or stopping already).
    pub fn stop(&self, run: &str, reason: &str) -> bool {
        let Some(entry) = self.table().get(run).cloned() else {
            return false;
        };
        let mut stopped = false;
        entry.change(|state| {
            if state.ended || state.stopping.is_some() {
                return;
            }
            state.stopping = Some(reason.to_string());
            state.events.push(v1::RunEvent {
                event: Some(v1::run_event::Event::Stop(v1::RunStop {
                    reason: reason.to_string(),
                    grace_ms: STOP_GRACE_MS,
                })),
            });
            if let Some(control) = &state.control {
                control.stop();
            }
            stopped = true;
        });
        stopped
    }

    /// Stop every live one (the daemon is asked to stop even if live), as a
    /// SIGTERM would.
    pub fn stop_all(&self) {
        for run in self.unstopped() {
            self.stop(&run, STOP_SIGNAL);
        }
    }

    /// Wait, at most `within` for each, until every live one has ended.
    /// Whether all did.
    pub fn wait_ended(&self, within: Duration) -> bool {
        let entries: Vec<Arc<Entry>> = self.table().values().cloned().collect();
        entries.iter().all(|entry| {
            let state = entry.state();
            let (state, _) = entry
                .changed
                .wait_timeout_while(state, within, |state| !state.ended)
                .unwrap_or_else(PoisonError::into_inner);
            state.ended
        })
    }
}

#[cfg(test)]
#[path = "one_shot.test.rs"]
mod tests;
