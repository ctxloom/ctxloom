//! The capacity the daemon's runtime keeps (owner, 2026-11-19 evening): a
//! turn is waited on by PARKING a thread of the blocking pool, never an async
//! worker, so the workers stay free for messages, steers, hooks and requests.

use run_ports::{BoxFuture, LaunchFailure, Parks, Running, TurnOutput};

use super::DAEMON_STACK;

/// The fewest workers a daemon runtime has, however few CPUs the host
/// reports: a parked turn takes none of them, and these are for the
/// requests, hooks and steers that arrive meanwhile.
pub(crate) const MIN_WORKERS: usize = 4;

/// The workers a daemon runtime has on a host of `cpus` CPUs: one per CPU,
/// never fewer than [`MIN_WORKERS`].
pub(crate) fn workers_for(cpus: usize) -> usize {
    cpus.max(MIN_WORKERS)
}

/// The workers of the daemon's runtime on this host.
pub(crate) fn workers() -> usize {
    workers_for(
        std::thread::available_parallelism()
            .map(std::num::NonZeroUsize::get)
            .unwrap_or(1),
    )
}

/// The one builder of the daemon's multi-thread runtimes: `workers` workers,
/// and the daemon's stack on every thread of it, the blocking pool's too
/// (the requests run there, and a deep plan overflowed tokio's default 2 MiB
/// on Windows).
pub(crate) fn build(workers: usize) -> std::io::Result<tokio::runtime::Runtime> {
    tokio::runtime::Builder::new_multi_thread()
        .worker_threads(workers)
        .thread_stack_size(DAEMON_STACK)
        .enable_all()
        .build()
}

/// A turn's end waited for on a thread of the runtime's blocking pool
/// (`finish` stays blocking). The parked threads are bounded by
/// construction: a runner runs one agent, and an agent has one turn at a
/// time; the pool's threads carry the daemon's stack.
pub(crate) struct BlockingParks;

impl Parks for BlockingParks {
    fn park(
        &self,
        running: Box<dyn Running>,
    ) -> BoxFuture<'static, Result<TurnOutput, LaunchFailure>> {
        Box::pin(async move {
            tokio::task::spawn_blocking(move || running.finish())
                .await
                .unwrap_or_else(|joined| {
                    Err(LaunchFailure {
                        deterministic: false,
                        detail: joined.to_string(),
                    })
                })
        })
    }
}

#[cfg(test)]
#[path = "runtime.test.rs"]
mod tests;
