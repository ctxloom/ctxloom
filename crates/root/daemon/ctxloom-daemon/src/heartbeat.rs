//! The daemon's heartbeat: the proof, behind `grpc.health.v1`, that its loop
//! is really running and not merely that its listener accepts.
//!
//! The loop ([`crate::serve`]'s rounds) beats at the start and the end of
//! each round. A watch task on the runtime's own workers (never the
//! blocking pool, which the API's use cases fill) compares the beat's age
//! with [`stale_after`] and says SERVING or NOT_SERVING to the health
//! service. A deadlocked loop stops beating and turns it NOT_SERVING; a
//! runtime too starved to run the watch cannot answer the Check at all,
//! which a client reads as not serving too.

use std::future::Future;
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::{Duration, Instant};

use hook_listener::HealthStatus;

/// How many of the daemon's polls a round may be late before the loop is
/// called stale: a round is woken at least every poll, so this many missed
/// ones is not a busy round but a stopped one.
pub const STALE_POLLS: u32 = 4;

/// What a round may take beyond its polls (a tick that reads a slow store,
/// a drain's hand-over) before the loop is called stale.
pub const ROUND_ALLOWANCE: Duration = Duration::from_secs(30);

/// How often the watch looks at the beat.
pub const LOOK_EVERY: Duration = Duration::from_millis(500);

/// How old the beat may grow before the daemon is NOT_SERVING: `STALE_POLLS`
/// polls plus `ROUND_ALLOWANCE`, so it follows the daemon's round period.
pub fn stale_after(poll: Duration) -> Duration {
    poll.saturating_mul(STALE_POLLS)
        .saturating_add(ROUND_ALLOWANCE)
}

/// When the loop last made progress, on this process's clock.
pub struct Heartbeat {
    origin: Instant,
    /// Milliseconds after `origin` of the last beat.
    last: AtomicU64,
}

impl Heartbeat {
    /// A heartbeat that has just beaten.
    pub fn new() -> Heartbeat {
        Heartbeat {
            origin: Instant::now(),
            last: AtomicU64::new(0),
        }
    }

    /// The loop made progress.
    pub fn beat(&self) {
        let now = u64::try_from(self.origin.elapsed().as_millis()).unwrap_or(u64::MAX);
        self.last.store(now, Ordering::Release);
    }

    /// How long since the last beat.
    pub fn age(&self) -> Duration {
        let last = Duration::from_millis(self.last.load(Ordering::Acquire));
        self.origin.elapsed().saturating_sub(last)
    }
}

impl Default for Heartbeat {
    fn default() -> Heartbeat {
        Heartbeat::new()
    }
}

/// What the health service says of a beat of `age`: stale beyond `bound` is
/// NOT_SERVING.
pub fn status(age: Duration, bound: Duration) -> HealthStatus {
    if age > bound {
        HealthStatus::NotServing
    } else {
        HealthStatus::Serving
    }
}

/// Tell `report` the status of `heartbeat` every `every`, for ever (the
/// caller drops the task to stop it). Only a change is reported.
pub async fn watch<F, Fut>(heartbeat: &Heartbeat, bound: Duration, every: Duration, report: F)
where
    F: Fn(HealthStatus) -> Fut,
    Fut: Future<Output = ()>,
{
    let mut said = None;
    loop {
        let now = status(heartbeat.age(), bound);
        if said != Some(now) {
            report(now).await;
            said = Some(now);
        }
        tokio::time::sleep(every).await;
    }
}

/// Stop `task` (the watch) and wait until it has dropped everything it held:
/// the daemon does this before it lets its listener and store go, so the
/// store's close finds no holder of the watch's.
pub async fn stopped(task: tokio::task::JoinHandle<()>) {
    task.abort();
    let _ = task.await;
}

#[cfg(test)]
#[path = "heartbeat.test.rs"]
mod tests;
