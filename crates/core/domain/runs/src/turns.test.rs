//! The turn gate keyed by message ids (D28 S-b). NEW-DESIGN, each test naming
//! the decision it holds. A race (a steer arriving while the turn it steers
//! ends) is forced by calling the transitions in the race's order.

use crate::aggregate::tests::Run;
use crate::{
    InterruptRequested, QueueTurn, RunEvent, TurnCompleted, TurnFailed, TurnFailureClass,
    TurnQueued, TurnStarted, TurnUrgency,
};

fn queue(ids: &[&str], urgency: TurnUrgency) -> QueueTurn {
    QueueTurn {
        prompt_ref: String::new(),
        resume: true,
        message_ids: ids.iter().map(|id| id.to_string()).collect(),
        urgency,
    }
}

fn ids(of: &[&str]) -> Vec<String> {
    of.iter().map(|id| id.to_string()).collect()
}

fn completed(turn: u64, of: &[&str]) -> TurnCompleted {
    TurnCompleted {
        turn,
        native_key: String::new(),
        answer_ref: String::new(),
        exit_code: 0,
        message_ids: ids(of),
    }
}

// S-b: a turn carries its messages, and its event says so.
#[test]
fn a_turn_queued_carries_its_messages_and_their_urgency() {
    let mut run = Run::enqueued();
    let events = run
        .command(queue(&["m1", "m2"], TurnUrgency::Wake))
        .unwrap();
    assert_eq!(
        events,
        vec![RunEvent::TurnQueued(TurnQueued {
            prompt_ref: String::new(),
            resume: true,
            message_ids: ids(&["m1", "m2"]),
            urgency: TurnUrgency::Wake,
        })]
    );
    assert_eq!(run.state().turn_in_flight, Some(1));
}

// S-b: a steer while a turn is in flight is queued and asks the turn in
// flight to be interrupted, in one decision.
#[test]
fn a_steer_while_a_turn_is_in_flight_interrupts_it_and_is_queued() {
    let mut run = Run::enqueued();
    run.command(queue(&["m1"], TurnUrgency::Wake)).unwrap();
    let events = run.command(queue(&["m2"], TurnUrgency::Steer)).unwrap();
    assert_eq!(
        events,
        vec![
            RunEvent::InterruptRequested(InterruptRequested {
                turn: 1,
                message_ids: ids(&["m1"]),
            }),
            RunEvent::TurnQueued(TurnQueued {
                prompt_ref: String::new(),
                resume: true,
                message_ids: ids(&["m2"]),
                urgency: TurnUrgency::Steer,
            }),
        ]
    );
    // The interrupted turn is still the one in flight until it ends.
    assert_eq!(run.state().turn_in_flight, Some(1));
}

// S-b: one interrupt per turn in flight; a later steer queues behind the
// first.
#[test]
fn a_second_steer_asks_no_second_interrupt() {
    let mut run = Run::enqueued();
    run.command(queue(&["m1"], TurnUrgency::Wake)).unwrap();
    run.command(queue(&["m2"], TurnUrgency::Steer)).unwrap();
    let events = run.command(queue(&["m3"], TurnUrgency::Steer)).unwrap();
    assert!(
        matches!(events.as_slice(), [RunEvent::TurnQueued(q)] if q.message_ids == ids(&["m3"])),
        "{events:?}"
    );
}

// S-b: a wake or an inform waits while a turn is in flight, or while a steer
// is queued behind it.
#[test]
fn a_wake_or_an_inform_while_a_turn_is_in_flight_is_refused() {
    let mut run = Run::enqueued();
    run.command(queue(&["m1"], TurnUrgency::Wake)).unwrap();
    for urgency in [TurnUrgency::Wake, TurnUrgency::Inform] {
        let (code, args) = run.command(queue(&["m9"], urgency)).unwrap_err();
        assert_eq!(code, "runs-turn-in-flight");
        assert_eq!(args["turn"], "1");
    }
}

// S-b: a steer to an idle run is a turn like any other: nothing to interrupt.
#[test]
fn a_steer_to_an_idle_run_is_queued_with_no_interrupt() {
    let mut run = Run::enqueued();
    let events = run.command(queue(&["m1"], TurnUrgency::Steer)).unwrap();
    assert!(matches!(events.as_slice(), [RunEvent::TurnQueued(_)]));
}

// S-b, the race: the interrupted turn's end hands the gate to the steer
// queued behind it, and a wake still waits; the steer's own end frees it.
#[test]
fn the_interrupted_turns_end_hands_the_gate_to_the_steer() {
    let mut run = Run::enqueued();
    run.command(queue(&["m1"], TurnUrgency::Wake)).unwrap(); // seq 1
    run.command(queue(&["m2"], TurnUrgency::Steer)).unwrap(); // seq 2, 3
    run.fact(
        TurnFailed {
            turn: 1,
            class: TurnFailureClass::Interrupted,
            detail: String::new(),
            message_ids: ids(&["m1"]),
        },
        "turn:r:m1:failed",
    );
    assert_eq!(run.state().turn_in_flight, Some(3));
    let (code, args) = run.command(queue(&["m4"], TurnUrgency::Wake)).unwrap_err();
    assert_eq!(
        (code.as_str(), args["turn"].as_str()),
        ("runs-turn-in-flight", "3")
    );
    run.fact(completed(3, &["m2"]), "turn:r:m2:completed");
    assert_eq!(run.state().turn_in_flight, None);
    assert!(run.command(queue(&["m4"], TurnUrgency::Wake)).is_ok());
}

// S-b: a turn's end is matched by its messages, whatever number it carries
// (the driver knows the message ids, not the stream's sequence).
#[test]
fn a_turn_ends_by_its_message_ids() {
    let mut run = Run::enqueued();
    run.command(queue(&["m1", "m2"], TurnUrgency::Wake))
        .unwrap();
    run.fact(completed(0, &["m1", "m2"]), "turn:r:m1:completed");
    assert_eq!(run.state().turn_in_flight, None);
}

// S-b: an end naming messages of no open turn frees nothing.
#[test]
fn an_end_naming_no_open_turn_frees_nothing() {
    let mut run = Run::enqueued();
    run.command(queue(&["m1"], TurnUrgency::Wake)).unwrap();
    run.fact(completed(1, &["other"]), "turn:r:other:completed");
    assert_eq!(run.state().turn_in_flight, Some(1));
}

// M1's one-shot path is kept: a turn the run never queued (the launcher's
// own turn 1) records its facts and leaves the gate as it was.
#[test]
fn a_turn_the_run_never_queued_records_its_facts_and_frees_nothing() {
    let mut run = Run::enqueued();
    let started = run.fact(
        TurnStarted {
            turn: 1,
            message_ids: Vec::new(),
        },
        "turn:r:1:started",
    );
    assert_eq!(started.len(), 1);
    let ended = run.fact(completed(1, &[]), "turn:r:1:completed");
    assert_eq!(ended.len(), 1);
    assert_eq!(run.state().turn_in_flight, None);
    assert!(run.command(queue(&["m1"], TurnUrgency::Wake)).is_ok());
}

// S-a: a turn's start is confirmed by its messages (claude's isReplay echo),
// recorded with them.
#[test]
fn a_turn_started_records_its_messages() {
    let mut run = Run::enqueued();
    run.command(queue(&["m1"], TurnUrgency::Wake)).unwrap();
    let events = run.fact(
        TurnStarted {
            turn: 1,
            message_ids: ids(&["m1"]),
        },
        "turn:r:m1:started",
    );
    assert!(
        matches!(events.as_slice(), [RunEvent::TurnStartedRecorded(s)] if s.message_ids == ids(&["m1"]))
    );
}

// The open turns survive a restart: the gate is folded from the stream.
#[test]
fn the_gate_is_rebuilt_from_the_stream() {
    let mut run = Run::enqueued();
    run.command(queue(&["m1"], TurnUrgency::Wake)).unwrap();
    run.command(queue(&["m2"], TurnUrgency::Steer)).unwrap();
    let state = run.state();
    assert_eq!(state.turns.len(), 2);
    assert!(state.turns[0].interrupt_requested);
    assert_eq!(state.turns[1].message_ids, ids(&["m2"]));
}
