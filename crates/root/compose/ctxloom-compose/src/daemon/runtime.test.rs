//! The daemon runtime keeps capacity for messages while turns are parked
//! (owner, 2026-11-19 evening), and its threads carry the daemon's stack.

use std::sync::mpsc::{channel, Receiver, Sender};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use run_ports::{
    ChildControl, ChildError, ChildMessage, Ended, Exit, LaunchFailure, Parks, Queued, Running,
    TurnOutput, Urgency,
};

use super::{build, workers_for, BlockingParks, DAEMON_STACK, MIN_WORKERS};

/// Only a hang is bounded by this; nothing waits on it to pass.
const HANG: Duration = Duration::from_secs(60);

#[test]
fn workers_are_the_cpus_and_never_fewer_than_the_minimum() {
    assert_eq!(MIN_WORKERS, 4);
    assert_eq!(workers_for(1), 4);
    assert_eq!(workers_for(3), 4);
    assert_eq!(workers_for(4), 4);
    assert_eq!(workers_for(5), 5);
    assert_eq!(workers_for(64), 64);
}

/// A command that is running until the test lets it go.
struct Held {
    parked: Sender<()>,
    release: Receiver<()>,
}

impl Running for Held {
    fn pid(&self) -> u32 {
        1
    }

    fn finish(self: Box<Self>) -> Result<TurnOutput, LaunchFailure> {
        self.parked.send(()).unwrap();
        self.release.recv().unwrap();
        Ok(TurnOutput {
            exit: Exit {
                code: Some(0),
                signal: None,
            },
            stdout: Vec::new(),
            stderr: Vec::new(),
            timed_out: false,
            truncated: false,
            drain_incomplete: false,
        })
    }
}

/// A hosted child whose steers are recorded.
#[derive(Default)]
struct Steered(Mutex<Vec<(String, Urgency)>>);

impl ChildControl for Steered {
    fn pid(&self) -> u32 {
        2
    }

    fn send(&self, message: ChildMessage, urgency: Urgency) -> Result<Queued, ChildError> {
        self.0.lock().unwrap().push((message.id, urgency));
        Ok(Queued::Written)
    }

    fn interrupt(&self) -> Result<bool, ChildError> {
        Ok(true)
    }

    fn clear(&self) -> Result<Queued, ChildError> {
        Ok(Queued::Written)
    }

    fn end(&self, _: Duration) -> Result<Ended, ChildError> {
        Err(ChildError::Closed)
    }
}

// A request as the API serves one: on the blocking pool, its use case driven
// by `block_on` on that thread (daemon-api-grpc's `answer`).
async fn served<T: Send + 'static>(work: impl FnOnce() -> T + Send + 'static) -> T {
    let runtime = tokio::runtime::Handle::current();
    tokio::task::spawn_blocking(move || {
        let _ = &runtime;
        work()
    })
    .await
    .unwrap()
}

// N turns parked on a runtime of FEW workers (more turns than workers): an
// unrelated request AND a steer to one of the parked children still
// complete. A parked turn that took a worker would leave none. Forced with
// hold points; the bound only turns a hang into a failure.
#[test]
fn an_unrelated_request_and_a_steer_complete_while_more_turns_than_workers_are_parked() {
    let few = 2;
    let turns = few + 3;
    let runtime = build(few).unwrap();
    let (parked, hear) = channel::<()>();
    let mut releases = Vec::new();
    let mut waits = Vec::new();
    for _ in 0..turns {
        let (release, held) = channel::<()>();
        releases.push(release);
        let running: Box<dyn Running> = Box::new(Held {
            parked: parked.clone(),
            release: held,
        });
        waits.push(runtime.spawn(BlockingParks.park(running)));
    }
    for _ in 0..turns {
        hear.recv_timeout(HANG).expect("every turn parked");
    }
    let child = Arc::new(Steered::default());
    let (to_child, hears) = (Arc::clone(&child), Arc::clone(&child));
    let (done, hear_done) = channel::<&'static str>();
    let (a, b) = (done.clone(), done);
    runtime.spawn(async move {
        served(move || {
            to_child
                .send(ChildMessage::new("m-1", "stop"), Urgency::Steer)
                .unwrap()
        })
        .await;
        let _ = a.send("steer");
    });
    runtime.spawn(async move {
        served(|| 7).await;
        let _ = b.send("request");
    });
    let mut heard = vec![
        hear_done.recv_timeout(HANG).expect("a request completed"),
        hear_done.recv_timeout(HANG).expect("a request completed"),
    ];
    for release in &releases {
        release.send(()).unwrap();
    }
    heard.sort_unstable();
    assert_eq!(heard, ["request", "steer"]);
    assert_eq!(
        *hears.0.lock().unwrap(),
        vec![("m-1".to_string(), Urgency::Steer)]
    );
    for wait in waits {
        assert!(runtime.block_on(wait).unwrap().is_ok());
    }
}

/// Recurse `depth` frames of about a kilobyte each, answering `depth`.
#[inline(never)]
fn deep(depth: usize) -> usize {
    // Kept in the frame (`black_box`), and never more than one byte's quotient.
    let frame = std::hint::black_box([depth as u8; 1024]);
    if depth == 0 {
        0
    } else {
        deep(depth - 1) + 1 + usize::from(frame[1] / 255 > 1)
    }
}

/// A depth needing more than tokio's default 2 MiB (about 6 MiB in all), and
/// under the daemon's 16 MiB with room for an unoptimized build's frames.
const DEPTH: usize = 6 * 1024;
const _: () = assert!(DEPTH * 1024 > 2 * 1024 * 1024 && DEPTH * 1536 < DAEMON_STACK);

// A deep call on the pool the requests run on, and on a worker, needs more
// than a default thread's stack: it passes only with the daemon's set on
// every thread the builder makes.
#[test]
fn a_deep_call_on_the_blocking_pool_and_on_a_worker_has_the_daemons_stack() {
    let runtime = build(2).unwrap();
    let pooled = runtime.block_on(async { served(|| deep(DEPTH)).await });
    assert_eq!(pooled, DEPTH);
    let worker = runtime.block_on(async { tokio::spawn(async { deep(DEPTH) }).await.unwrap() });
    assert_eq!(worker, DEPTH);
}
