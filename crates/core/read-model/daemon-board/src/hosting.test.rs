//! The hosting projector's state: Active or Idle from the run stream, a
//! restart owed from the session stream, and what the board then says is due.

use angzarr_embedded::{Any, Payload};
use runs::{
    run_root, EndCause, RunEnded, RunEnqueued, RunEvent, RunMode, RunnerStartedRecorded,
    TurnCompletedRecorded, TurnQueued, TurnUrgency, RUN_DOMAIN,
};
use sessions::{
    IncarnationEnded, IncarnationOwed, RestartOwed, RestartReason, SessionClosed, SessionEvent,
    SESSION_DOMAIN,
};

use crate::tests::{book, minted, root, ID};
use crate::{Agent, Board, RestartDue};

fn session_event(event: SessionEvent) -> Any {
    event.to_any()
}

fn run_event(event: RunEvent) -> Any {
    event.to_any()
}

/// The run's first events: enqueued, its runner started.
pub(super) fn started() -> Vec<Any> {
    vec![
        run_event(RunEvent::RunEnqueued(RunEnqueued {
            session: ID.to_string(),
            incarnation: 1,
            mode: RunMode::Structured,
            launcher_lock: String::new(),
            grant: None,
            spec: None,
        })),
        run_event(RunEvent::RunnerStartedRecorded(RunnerStartedRecorded {
            runtime: "host".to_string(),
            workspace: "/work".to_string(),
            pid: 1,
        })),
    ]
}

pub(super) fn turn_queued() -> Any {
    run_event(RunEvent::TurnQueued(TurnQueued {
        prompt_ref: String::new(),
        resume: true,
        message_ids: vec!["m1".to_string()],
        urgency: TurnUrgency::Wake,
    }))
}

pub(super) fn turn_completed(turn: u64) -> Any {
    run_event(RunEvent::TurnCompletedRecorded(TurnCompletedRecorded {
        turn,
        native_key: String::new(),
        answer_ref: String::new(),
        exit_code: 0,
        message_ids: vec!["m1".to_string()],
        ..Default::default()
    }))
}

pub(super) fn engine_ended() -> Any {
    run_event(RunEvent::RunEnded(RunEnded {
        cause: EndCause::RunnerExit,
        exit_code: Some(0),
        detail: String::new(),
    }))
}

pub(super) fn restart_owed(reason: RestartReason) -> Any {
    session_event(SessionEvent::RestartOwed(RestartOwed {
        reason,
        incarnation: 1,
        note: String::new(),
    }))
}

/// The board after the session's `session` events (after its mint) and the
/// run's `run` events (after its start).
fn board(session: Vec<Any>, run: Vec<Any>) -> Board {
    let mut board = Board::new();
    let mut all = minted(Some("owner"));
    all.extend(session);
    board
        .project(&book(SESSION_DOMAIN, root(), 0, all))
        .unwrap();
    let mut events = started();
    events.extend(run);
    board
        .project(&book(RUN_DOMAIN, run_root(ID, 1), 0, events))
        .unwrap();
    board
}

fn due(reason: RestartReason) -> Vec<RestartDue> {
    vec![RestartDue {
        session: ID.to_string(),
        incarnation: 1,
        reason,
    }]
}

#[test]
fn an_agent_is_idle_until_a_turn_is_in_flight_and_idle_again_when_it_ends() {
    assert_eq!(board(vec![], vec![]).agent(ID), Agent::Idle);
    let busy = board(vec![], vec![turn_queued()]);
    assert_eq!(busy.agent(ID), Agent::Active { turn: 2 });
    let done = board(vec![], vec![turn_queued(), turn_completed(2)]);
    assert_eq!(done.agent(ID), Agent::Idle);
    assert!(busy.restarts_due().is_empty() && done.restarts_due().is_empty());
}

#[test]
fn a_restart_owed_while_a_turn_is_in_flight_is_parked_and_due_at_its_end() {
    let owed = || restart_owed(RestartReason::Operator);
    let parked = board(
        vec![restart_owed(RestartReason::Context)],
        vec![turn_queued()],
    );
    assert_eq!(
        parked.agent(ID),
        Agent::Parked {
            turn: 2,
            reason: RestartReason::Context
        }
    );
    assert!(parked.restarts_due().is_empty(), "parked, not due");
    let edge = board(vec![owed()], vec![turn_queued(), turn_completed(2)]);
    assert_eq!(
        edge.agent(ID),
        Agent::Restarting {
            reason: RestartReason::Operator
        }
    );
    assert_eq!(edge.restarts_due(), due(RestartReason::Operator));
}

#[test]
fn a_restart_owed_to_an_idle_agent_is_due_at_once() {
    let idle = board(vec![restart_owed(RestartReason::Context)], vec![]);
    assert_eq!(idle.restarts_due(), due(RestartReason::Context));
}

// The engine's end forces Idle: a turn whose end was never delivered cannot
// hold a restart for ever, and an engine that has ended is not ended again.
#[test]
fn the_engines_exit_forces_idle_and_it_is_not_due_to_be_ended_again() {
    let exited = board(
        vec![restart_owed(RestartReason::Operator)],
        vec![turn_queued(), engine_ended()],
    );
    assert_eq!(
        exited.agent(ID),
        Agent::Restarting {
            reason: RestartReason::Operator
        }
    );
    assert!(exited.restarts_due().is_empty());
    let unowed = board(vec![], vec![turn_queued(), engine_ended()]);
    assert_eq!(unowed.agent(ID), Agent::Idle);
}

// Owed in the stream, closed by the next incarnation owed: the state after a
// crash is the stream's, and a restart already carried out is not due again.
#[test]
fn the_next_incarnation_closes_the_restart_and_a_fold_from_zero_agrees() {
    let carried_out = vec![
        restart_owed(RestartReason::Operator),
        session_event(SessionEvent::IncarnationEnded(IncarnationEnded {
            incarnation: 1,
            cause: "restarted".to_string(),
            notice_parent: true,
        })),
        session_event(SessionEvent::IncarnationOwed(IncarnationOwed {
            incarnation: 2,
            reason: "restart".to_string(),
        })),
    ];
    let board = board(carried_out, vec![engine_ended()]);
    assert_eq!(board.agent(ID), Agent::Idle);
    assert!(board.restarts_due().is_empty());
}

#[test]
fn a_fold_from_zero_of_the_same_streams_agrees_with_the_one_it_replays() {
    let streams = || {
        (
            vec![restart_owed(RestartReason::Operator)],
            vec![turn_queued()],
        )
    };
    let (session, run) = streams();
    let mut live = board(session, run);
    let before = live.agent(ID);
    // The turn ends: the live fold continues the stream, the replay starts
    // it afresh from 0; they agree.
    live.project(&book(
        RUN_DOMAIN,
        run_root(ID, 1),
        4,
        vec![turn_completed(2)],
    ))
    .unwrap();
    let (session, mut run) = streams();
    run.push(turn_completed(2));
    let replay = board(session, run);
    assert_ne!(before, live.agent(ID));
    assert_eq!(live.agent(ID), replay.agent(ID));
    assert_eq!(live.restarts_due(), replay.restarts_due());
}

// Arrival order across streams is not session-then-run.
#[test]
fn the_run_may_be_folded_before_its_session() {
    let mut board = Board::new();
    let mut events = started();
    events.push(turn_queued());
    events.push(turn_completed(2));
    board
        .project(&book(RUN_DOMAIN, run_root(ID, 1), 0, events))
        .unwrap();
    assert_eq!(board.agent(ID), Agent::Idle, "no session yet");
    let mut all = minted(Some("owner"));
    all.push(restart_owed(RestartReason::Config));
    board
        .project(&book(SESSION_DOMAIN, root(), 0, all))
        .unwrap();
    assert_eq!(board.restarts_due(), due(RestartReason::Config));
}

#[test]
fn a_closed_agent_is_idle_whatever_it_was_owed() {
    let board = board(
        vec![
            restart_owed(RestartReason::Operator),
            session_event(SessionEvent::SessionClosed(SessionClosed {
                cause: "closed".to_string(),
            })),
        ],
        vec![],
    );
    assert_eq!(board.agent(ID), Agent::Idle);
    assert!(board.restarts_due().is_empty());
}
