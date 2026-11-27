//! The terminal wake's poster at work (owner ruling 2026-11-18): while
//! `mcp serve` runs for a primary terminal session (its session named, and
//! its engine's messaging socket exported to it), it watches its own
//! session (`WatchSession`) and does what [`daemon_api_client::Waker`] decides with
//! each signal: a wake due is issued (journaled), then posted on the socket
//! and timed; one not answered in [`daemon_api_client::DISARM_AFTER`] is disarmed;
//! one whose post fails is disarmed at once; one not issued is tried again
//! at the next signal. A watch that ends (the daemon
//! gone) is opened again after [`WATCH_AGAIN`], the poster's state kept.
//! Nothing here is a message: what the wake is for, the session's next
//! prompt's hook delivers.

use std::pin::Pin;
use std::sync::Arc;
use std::time::Duration;

use daemon_api_client::{wake_line, Caller, Daemons, WakeAct, WakePost, Waker};

/// How long after a watch ended it is opened again.
pub const WATCH_AGAIN: Duration = Duration::from_secs(1);

/// What the poster posts on, and how it mints a wake's nonce.
pub struct Wakes {
    pub post: Arc<dyn WakePost>,
    pub mint: Box<dyn Fn() -> String + Send + Sync>,
}

/// The armed wake's timer: its nonce, and when its time runs out.
type Timer = Option<(String, Pin<Box<tokio::time::Sleep>>)>;

/// The timer's end; never, with none armed.
async fn ran_out(timer: &mut Timer) {
    match timer {
        Some((_, sleep)) => sleep.as_mut().await,
        None => std::future::pending().await,
    }
}

/// Keep `caller`'s own session awake, for as long as the server runs.
pub async fn keep_awake<D>(caller: Arc<Caller<D>>, wakes: Wakes)
where
    D: Daemons + Send + Sync + 'static,
    D::Api: Send + Sync,
{
    let mut waker = Waker::new();
    let mut timer: Timer = None;
    loop {
        if let Ok(mut signals) = caller.watch_own().await {
            loop {
                tokio::select! {
                    next = signals.next() => {
                        let Some(Ok(signal)) = next else { break };
                        let act = waker.heard(&signal, || (wakes.mint)());
                        act_on(&caller, &wakes, &mut waker, &mut timer, act).await;
                    }
                    () = ran_out(&mut timer) => expire(&caller, &mut waker, &mut timer).await,
                }
            }
        }
        let again = tokio::time::sleep(WATCH_AGAIN);
        tokio::pin!(again);
        loop {
            tokio::select! {
                () = &mut again => break,
                () = ran_out(&mut timer) => expire(&caller, &mut waker, &mut timer).await,
            }
        }
    }
}

/// The armed wake's time ran out: disarmed, if the waker still holds it.
async fn expire<D>(caller: &Caller<D>, waker: &mut Waker, timer: &mut Timer)
where
    D: Daemons,
{
    let Some((nonce, _)) = timer.take() else {
        return;
    };
    if let Some(WakeAct::Disarm(nonce)) = waker.expired(&nonce) {
        let _ = caller.disarm_wake(&nonce).await;
    }
}

/// Do `act`: a wake issued, then posted and timed (its post failed:
/// disarmed at once; its issue refused: forgotten), or a wake disarmed.
async fn act_on<D>(
    caller: &Caller<D>,
    wakes: &Wakes,
    waker: &mut Waker,
    timer: &mut Timer,
    act: Option<WakeAct>,
) where
    D: Daemons,
{
    match act {
        Some(WakeAct::Wake(nonce)) => {
            if caller.issue_wake(&nonce).await.is_err() {
                waker.not_issued(&nonce);
                return;
            }
            match wakes.post.post(&wake_line(&nonce)) {
                Ok(()) => {
                    let sleep = Box::pin(tokio::time::sleep(daemon_api_client::DISARM_AFTER));
                    *timer = Some((nonce, sleep));
                }
                Err(_) => {
                    waker.failed(&nonce);
                    let _ = caller.disarm_wake(&nonce).await;
                }
            }
        }
        Some(WakeAct::Disarm(nonce)) => {
            let _ = caller.disarm_wake(&nonce).await;
        }
        None => {}
    }
}

#[cfg(test)]
#[path = "wake.test.rs"]
mod tests;
