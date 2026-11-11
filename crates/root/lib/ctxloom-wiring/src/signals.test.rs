//! A run's signals, forced with the platform's fake (`os::Signals::fake`):
//! no signal is sent to the test process.

use std::time::Duration;

use host_launcher::Stop;
use os::{Signal, Signals};
use run_application::RunError;

use super::{forward, Heard, HEARD, STOP_ONE_SHOT, STOP_SESSION};

fn runtime() -> tokio::runtime::Runtime {
    tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
        .unwrap()
}

/// Whether `forward` is still hearing after the signals raised so far: it
/// has had every chance to act on them.
fn still_hearing(runtime: &tokio::runtime::Runtime, task: &tokio::task::JoinHandle<()>) -> bool {
    runtime.block_on(async {
        for _ in 0..10 {
            tokio::task::yield_now().await;
        }
    });
    !task.is_finished()
}

#[test]
fn test_a_run_hears_all_three_signals_and_a_one_shot_stops_on_each() {
    assert_eq!(
        HEARD,
        [Signal::Hangup, Signal::Interrupt, Signal::Terminate]
    );
    for signal in Signal::ALL {
        let runtime = runtime();
        let (raise, signals) = Signals::fake(&HEARD);
        let (stop, heard) = (Stop::new(), Heard::default());
        let task = runtime.spawn(forward(
            signals,
            &STOP_ONE_SHOT,
            stop.clone(),
            heard.clone(),
        ));
        assert!(still_hearing(&runtime, &task));
        assert!(!stop.is_requested() && heard.signal().is_none());
        raise.raise(signal);
        runtime
            .block_on(async { tokio::time::timeout(Duration::from_secs(10), task).await })
            .expect("the one-shot was not stopped")
            .unwrap();
        assert!(stop.is_requested(), "{signal} did not stop the engine");
        assert_eq!(heard.signal(), Some(signal));
    }
}

#[test]
fn test_the_humans_session_lets_an_interrupt_pass_and_stops_on_a_hangup_or_a_terminate() {
    for signal in [Signal::Hangup, Signal::Terminate] {
        let runtime = runtime();
        let (raise, signals) = Signals::fake(&HEARD);
        let (stop, heard) = (Stop::new(), Heard::default());
        let task = runtime.spawn(forward(signals, &STOP_SESSION, stop.clone(), heard.clone()));
        raise.raise(Signal::Interrupt);
        assert!(still_hearing(&runtime, &task), "a SIGINT ended the hearing");
        assert!(!stop.is_requested(), "a SIGINT stopped the session");
        assert_eq!(heard.signal(), None);
        raise.raise(signal);
        runtime
            .block_on(async { tokio::time::timeout(Duration::from_secs(10), task).await })
            .expect("the session was not stopped")
            .unwrap();
        assert!(stop.is_requested());
        assert_eq!(heard.signal(), Some(signal));
    }
}

#[test]
fn test_a_stopped_run_ends_as_the_signal_says_and_one_not_stopped_as_it_did() {
    let mut kept = Ok::<u8, RunError>(4);
    Heard::default().overrule(&mut kept);
    assert_eq!(kept.unwrap(), 4);
    let runtime = runtime();
    let (raise, signals) = Signals::fake(&HEARD);
    let heard = Heard::default();
    let task = runtime.spawn(forward(signals, &STOP_ONE_SHOT, Stop::new(), heard.clone()));
    raise.raise(Signal::Terminate);
    runtime.block_on(task).unwrap();
    let mut ended = Ok::<u8, RunError>(0);
    heard.overrule(&mut ended);
    let ended = ended.unwrap_err();
    assert!(
        matches!(&ended, RunError::Stopped { signal, status: 143 } if signal == "SIGTERM"),
        "{ended:?}"
    );
    assert_eq!(ended.exit_code(), 143);
    // The signal is the cause even of a run that failed after it.
    let mut failed = Err::<u8, RunError>(RunError::EmptyAnswer);
    heard.overrule(&mut failed);
    assert_eq!(failed.unwrap_err().exit_code(), 143);
}
