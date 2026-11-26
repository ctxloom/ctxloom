//! The scheduler, run by the daemon (step 8 U5a): angzarr's clock loop
//! (`scheduler-loop`) over the daemon's core, for the daemon's life.
//!
//! - The due model ([`SchedulerDues`]) is registered in the core's kernel
//!   (`Projectors::due`), so an alarm armed in this process is folded as it
//!   commits and wakes the loop; the loop catches the same fold up from the
//!   store for what other processes commit (one fold, each page once:
//!   `held-projection`).
//! - The clock is the system's ([`crate::core_link::SystemClock`]); the loop
//!   waits on the runtime's timer, cut short by an arming folded inline or
//!   by the stop ([`Woken`]).
//! - [`Scheduler::stop`] ends it: the loop's current tick finishes, it
//!   returns, and its copy of the core is let go before the store closes
//!   (ruling 131).
//!
//! A daemon started after another (killed or stopped) rebuilds its model
//! from every scheduler stream's start: an alarm armed before is due as
//! ever, and one that came due while no daemon ran fires once, late.

use std::future::Future;
use std::pin::Pin;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex, PoisonError};
use std::time::Duration;

use angzarr_embedded::angzarr_router::error::CodedError;
use angzarr_embedded::angzarr_router::hpb::{ProjectRequest, ProjectResponse};
use angzarr_embedded::v1::{Cover, EventBook};
use angzarr_embedded::{
    Any, ListStreams, PayloadError, Processed, ProjectorHandler, ReadStore, Store, Timestamp, Watch,
};
use held_projection::{Fill, Held, Model};
use scheduler_due::{Due, ReadsDue};
use scheduler_loop::{SchedulerLoop, Tick};
use scheduler_ports::{Clock, Wait};
use tokio::sync::{watch, Notify};

use crate::SharedCore;

/// The due model, as a [`Held`] folds it.
#[derive(Default)]
pub struct DueFold(Due);

impl Model for DueFold {
    const NAME: &'static str = "scheduler-due";

    fn project(&mut self, book: &EventBook) -> Result<(), PayloadError> {
        self.0.project(book)
    }

    /// No resource message: nothing asks a `HandleSync` for it.
    fn view(&self, _cover: &Cover) -> Option<Any> {
        None
    }
}

/// The due model on the projector port, registered in the kernel and
/// shared with the loop: every clone is the same fold. Each book the kernel
/// hands it wakes the loop's wait, as it may arm an alarm sooner than the
/// one the loop waits for.
#[derive(Clone, Default)]
pub struct SchedulerDues {
    held: Held<DueFold>,
    woken: Arc<Notify>,
}

impl SchedulerDues {
    /// An empty model whose gaps are read through `fill`.
    pub fn filled(fill: Arc<dyn Fill>) -> SchedulerDues {
        SchedulerDues {
            held: Held::new(Some(fill)),
            woken: Arc::default(),
        }
    }
}

impl ProjectorHandler for SchedulerDues {
    fn name(&self) -> &str {
        self.held.name()
    }

    async fn dispatch(&self, request: &ProjectRequest) -> Result<ProjectResponse, CodedError> {
        let folded = self.held.dispatch(request).await;
        self.woken.notify_one();
        folded
    }
}

impl ReadsDue for SchedulerDues {
    fn read_due<R>(&self, read: impl FnOnce(&Due) -> R) -> R {
        read(&self.held.lock().0)
    }
}

/// The loop's wait: the pause on the runtime's timer, cut short by `woken`
/// (an arming folded inline, or the stop). A wake while no wait is waiting
/// is kept for the next, so none is lost.
struct Woken(Arc<Notify>);

impl Wait for Woken {
    fn wait(&self, backoff: Duration) -> Pin<Box<dyn Future<Output = ()> + Send + '_>> {
        Box::pin(async move {
            let _ = tokio::time::timeout(backoff, self.0.notified()).await;
        })
    }

    // The loop draws no jitter.
    #[cfg_attr(test, mutants::skip)]
    fn draw(&self) -> f64 {
        0.0
    }
}

/// The clock, its last reading kept: what the tick after it ran at.
struct Reading {
    clock: Arc<dyn Clock>,
    last: Mutex<Timestamp>,
}

impl Clock for Reading {
    fn now(&self) -> Timestamp {
        let now = self.clock.now();
        *self.last.lock().unwrap_or_else(PoisonError::into_inner) = now;
        now
    }
}

impl Reading {
    fn last(&self) -> Timestamp {
        *self.last.lock().unwrap_or_else(PoisonError::into_inner)
    }
}

/// Where the scheduler's lines go: the daemon's log.
pub(crate) type Say = Arc<dyn Fn(&str) + Send + Sync>;

/// The scheduler running: its stop, and the instant of its last tick.
pub(crate) struct Scheduler {
    stopping: Arc<AtomicBool>,
    woken: Arc<Notify>,
    task: tokio::task::JoinHandle<()>,
    /// What a test waits on; nothing else reads it.
    #[cfg(test)]
    ticked: watch::Receiver<Ticked>,
}

/// What the loop has done: the instant its last tick ran at, and how many
/// alarms it has fired.
#[derive(Clone, Copy, Debug, Default)]
struct Ticked {
    at: Option<Timestamp>,
    fired: usize,
}

/// The scheduler, started on the current runtime: the loop over `core`'s
/// kernel and store, reading `dues` (the model `core`'s kernel folds), on
/// `clock`, waking at least `every` to see what other processes armed.
pub(crate) fn start<S>(
    core: SharedCore<S>,
    dues: SchedulerDues,
    clock: Arc<dyn Clock>,
    every: Duration,
    say: Say,
) -> Scheduler
where
    S: Store + ReadStore + Processed + ListStreams + Watch + Send + Sync + 'static,
{
    let stopping = Arc::new(AtomicBool::new(false));
    let woken = Arc::clone(&dues.woken);
    let (told, ticked) = watch::channel(Ticked::default());
    let task = tokio::spawn(looped(
        core,
        dues,
        Reading {
            clock,
            last: Mutex::default(),
        },
        every,
        (Arc::clone(&stopping), told, say),
    ));
    #[cfg(not(test))]
    drop(ticked);
    Scheduler {
        stopping,
        woken,
        task,
        #[cfg(test)]
        ticked,
    }
}

/// The loop, until it is stopped: a tick's error (its catch-up failed) is
/// said and the loop goes on after a pause, its model kept.
async fn looped<S>(
    core: SharedCore<S>,
    dues: SchedulerDues,
    clock: Reading,
    every: Duration,
    (stopping, told, say): (Arc<AtomicBool>, watch::Sender<Ticked>, Say),
) where
    S: Store + ReadStore + Processed + ListStreams + Watch + Send + Sync + 'static,
{
    let wait = Woken(Arc::clone(&dues.woken));
    let kernel = core.kernel();
    let mut scheduler = SchedulerLoop::sharing(dues);
    loop {
        let ran = scheduler
            .run(&kernel, core.store(), &clock, &wait, every, |tick| {
                said_of(tick, say.as_ref());
                told.send_modify(|ticked| {
                    ticked.at = Some(clock.last());
                    ticked.fired += tick.fired().len();
                });
                !stopping.load(Ordering::SeqCst)
            })
            .await;
        match ran {
            // It returns once it is stopped.
            Ok(()) => return,
            Err(error) => {
                say(&format!("the scheduler's tick failed: {error}"));
                wait.wait(every).await;
            }
        }
    }
}

/// What a tick did that the log says: each alarm fired, each whose fact
/// failed, each stream the model could not read.
fn said_of(tick: &Tick, say: &(dyn Fn(&str) + Send + Sync)) {
    for alarm in tick.fired() {
        say(&format!(
            "alarm fired: {} {}",
            scheduler::stream_text(&alarm.target),
            alarm.alarm_id
        ));
    }
    for (alarm, code) in &tick.failed {
        say(&format!(
            "alarm not fired, tried again next tick: {} {} ({code})",
            scheduler::stream_text(&alarm.target),
            alarm.alarm_id
        ));
    }
    for cover in &tick.unread {
        say(&format!(
            "a scheduler stream cannot be read, held for the next tick: {}",
            scheduler::cover_text(cover)
        ));
    }
}

impl Scheduler {
    /// Stop it, and wait for its loop to end, at most `within`: the tick it
    /// is in finishes first. One that does not end in time is cut at its
    /// next await. Answers whether it ended by itself.
    pub(crate) async fn stop(self, within: Duration) -> bool {
        let Scheduler {
            stopping,
            woken,
            mut task,
            ..
        } = self;
        stopping.store(true, Ordering::SeqCst);
        woken.notify_one();
        match tokio::time::timeout(within, &mut task).await {
            Ok(_) => true,
            Err(_) => {
                task.abort();
                let _ = task.await;
                false
            }
        }
    }

    /// Wake it now: its next tick reads the clock again.
    #[cfg(test)]
    pub(crate) fn wake(&self) {
        self.woken.notify_one();
    }

    /// Wait, at most `TEST_BOUND`, until it has ticked at `at` or later.
    #[cfg(test)]
    pub(crate) async fn ticked_at(&mut self, at: Timestamp) {
        let reached = |ticked: &Ticked| {
            ticked
                .at
                .is_some_and(|last| (last.seconds, last.nanos) >= (at.seconds, at.nanos))
        };
        let ticked = tokio::time::timeout(TEST_BOUND, self.ticked.wait_for(reached)).await;
        assert!(
            ticked.is_ok_and(|ticked| ticked.is_ok()),
            "no tick at {at:?} within {TEST_BOUND:?}"
        );
    }

    /// Wait, at most `TEST_BOUND`, until it has fired `n` alarms in all.
    #[cfg(test)]
    pub(crate) async fn fired(&mut self, n: usize) {
        let ticked =
            tokio::time::timeout(TEST_BOUND, self.ticked.wait_for(|ticked| ticked.fired >= n))
                .await;
        assert!(
            ticked.is_ok_and(|ticked| ticked.is_ok()),
            "{n} alarms not fired within {TEST_BOUND:?}"
        );
    }

    /// Cut it where it is, as a kill would: nothing is waited for.
    #[cfg(test)]
    pub(crate) fn kill(self) {
        self.task.abort();
    }
}

/// How long a test waits for the loop before it fails: a bound on a wait
/// the test forces, never a sleep.
#[cfg(test)]
const TEST_BOUND: Duration = Duration::from_secs(30);

#[cfg(test)]
#[path = "scheduling.test.rs"]
mod tests;
