//! The conversation, told what opencode answered in the order the spike
//! saw it (opencode 1.18.4 over ACP, 2026-11-10): the races are forced by
//! WHERE in that order a message is sent, never by time.

use run_ports::{
    ChildError, ChildMessage, ChildObservation, Exit, FailureClass, Queued, TurnOutcome, Urgency,
};

use crate::conversation::{Act, Conversation, Heard, Opening, Stop};

fn message(id: &str, text: &str) -> ChildMessage {
    ChildMessage::new(id, text)
}

fn opening(model: &str, resume: Option<&str>) -> Opening {
    Opening {
        cwd: "/work".to_string(),
        model: model.to_string(),
        resume: resume.map(String::from),
    }
}

/// A conversation opened on session `S1` with no model, and nothing asked.
fn opened() -> Conversation {
    let (mut conversation, acts) = Conversation::new(opening("", None));
    assert_eq!(acts, vec![Act::Initialize]);
    conversation.hear(Heard::Initialized(Ok(())));
    let (seen, acts) = conversation.hear(Heard::SessionReady(Ok("S1".to_string())));
    assert_eq!(
        seen,
        vec![ChildObservation::Started {
            native_id: "S1".to_string()
        }]
    );
    assert!(acts.is_empty());
    conversation
}

fn prompt(key: u64, session: &str, ids: &[&str], text: &str) -> Act {
    Act::Prompt {
        key,
        session: session.to_string(),
        ids: ids.iter().map(|i| i.to_string()).collect(),
        text: text.to_string(),
    }
}

fn said(conversation: &mut Conversation, message_id: &str, text: &str) {
    let heard = conversation.hear(Heard::Said {
        message_id: Some(message_id.to_string()),
        text: text.to_string(),
    });
    assert_eq!(heard, (Vec::new(), Vec::new()));
}

fn answered(ids: &[&str], answer: &str, native: &str) -> ChildObservation {
    ChildObservation::TurnEnded {
        message_ids: ids.iter().map(|i| i.to_string()).collect(),
        outcome: TurnOutcome::Answered {
            answer: answer.to_string(),
            native_id: Some(native.to_string()),
        },
    }
}

fn cancelled(ids: &[&str], native: &str) -> ChildObservation {
    ChildObservation::TurnEnded {
        message_ids: ids.iter().map(|i| i.to_string()).collect(),
        outcome: TurnOutcome::Failed {
            class: FailureClass::Interrupted,
            detail: "the turn was cancelled".to_string(),
            native_id: Some(native.to_string()),
        },
    }
}

fn end_turn(key: u64) -> Heard {
    Heard::Answered {
        key,
        stop: Ok(Stop::EndTurn),
    }
}

#[test]
fn an_opening_initializes_makes_a_session_and_sets_its_model() {
    let (mut conversation, acts) = Conversation::new(opening("opencode/big-pickle", None));
    assert_eq!(acts, vec![Act::Initialize]);
    // A message before the child is up waits for it.
    assert_eq!(
        conversation
            .send(message("m-1", "early"), Urgency::Wake)
            .unwrap(),
        (Queued::Held, Vec::new())
    );
    assert_eq!(
        conversation.hear(Heard::Initialized(Ok(()))),
        (
            Vec::new(),
            vec![Act::NewSession {
                cwd: "/work".to_string()
            }]
        )
    );
    assert_eq!(
        conversation.hear(Heard::SessionReady(Ok("ses_1".to_string()))),
        (
            Vec::new(),
            vec![Act::SetModel {
                session: "ses_1".to_string(),
                model: "opencode/big-pickle".to_string()
            }]
        )
    );
    assert_eq!(
        conversation.hear(Heard::ModelSet(Ok(()))),
        (
            vec![ChildObservation::Started {
                native_id: "ses_1".to_string()
            }],
            vec![prompt(1, "ses_1", &["m-1"], "early")]
        )
    );
}

#[test]
fn a_resumed_child_loads_its_session_and_ignores_the_replay() {
    let (mut conversation, _) = Conversation::new(opening("", Some("ses_old")));
    assert_eq!(
        conversation.hear(Heard::Initialized(Ok(()))).1,
        vec![Act::LoadSession {
            session: "ses_old".to_string(),
            cwd: "/work".to_string()
        }]
    );
    // `session/load` replays the history as updates before it answers.
    said(&mut conversation, "msg_old", "from before");
    assert_eq!(
        conversation
            .hear(Heard::SessionReady(Ok("ses_old".to_string())))
            .0,
        vec![ChildObservation::Started {
            native_id: "ses_old".to_string()
        }]
    );
    conversation
        .send(message("m-1", "again"), Urgency::Wake)
        .unwrap();
    said(&mut conversation, "msg_new", "fresh");
    assert_eq!(
        conversation.hear(end_turn(1)).0,
        vec![answered(&["m-1"], "fresh", "ses_old")]
    );
}

#[test]
fn an_opening_that_fails_ends_the_input_and_the_exit_says_why() {
    let (mut conversation, _) = Conversation::new(opening("nope/model", None));
    conversation.hear(Heard::Initialized(Ok(())));
    conversation.hear(Heard::SessionReady(Ok("ses_1".to_string())));
    assert_eq!(
        conversation.hear(Heard::ModelSet(Err("Model not found".to_string()))),
        (Vec::new(), vec![Act::CloseInput])
    );
    assert_eq!(
        conversation.send(message("m-1", "x"), Urgency::Wake),
        Err(ChildError::Closed)
    );
    let exit = Exit {
        code: Some(0),
        signal: None,
    };
    assert_eq!(
        conversation.exited(exit, String::new()),
        vec![ChildObservation::Exited {
            exit,
            detail: "Model not found".to_string()
        }]
    );
}

#[test]
fn stderr_is_the_exit_detail_over_the_failure() {
    let (mut conversation, _) = Conversation::new(opening("", None));
    conversation.hear(Heard::Initialized(Err("bad version".to_string())));
    let exit = Exit {
        code: Some(1),
        signal: None,
    };
    assert_eq!(
        conversation.exited(exit, "it broke".to_string()),
        vec![ChildObservation::Exited {
            exit,
            detail: "it broke".to_string()
        }]
    );
}

#[test]
fn the_answer_is_the_turns_last_agent_message() {
    let mut conversation = opened();
    assert_eq!(
        conversation
            .send(message("m-1", "use a tool"), Urgency::Wake)
            .unwrap(),
        (
            Queued::Written,
            vec![prompt(1, "S1", &["m-1"], "use a tool")]
        )
    );
    said(&mut conversation, "msg_a", "I will run ");
    said(&mut conversation, "msg_a", "it.");
    said(&mut conversation, "msg_b", "m");
    said(&mut conversation, "msg_b", "ango");
    assert_eq!(
        conversation.hear(end_turn(1)),
        (vec![answered(&["m-1"], "mango", "S1")], Vec::new())
    );
    // The next turn starts with no text of the last.
    conversation
        .send(message("m-2", "quiet"), Urgency::Wake)
        .unwrap();
    assert_eq!(
        conversation.hear(end_turn(2)).0,
        vec![answered(&["m-2"], "", "S1")]
    );
}

#[test]
fn a_wake_mid_turn_is_held_and_asked_when_the_turn_replies() {
    let mut conversation = opened();
    conversation
        .send(message("m-1", "long"), Urgency::Wake)
        .unwrap();
    assert_eq!(
        conversation
            .send(message("m-2", "next"), Urgency::Wake)
            .unwrap(),
        (Queued::Held, Vec::new())
    );
    said(&mut conversation, "msg_a", "done");
    assert_eq!(
        conversation.hear(end_turn(1)),
        (
            vec![answered(&["m-1"], "done", "S1")],
            vec![prompt(2, "S1", &["m-2"], "next")]
        )
    );
}

#[test]
fn an_inform_rides_on_the_next_prompt_and_starts_none() {
    let mut conversation = opened();
    assert_eq!(
        conversation
            .send(message("i-1", "for context"), Urgency::Inform)
            .unwrap(),
        (Queued::Held, Vec::new())
    );
    assert_eq!(
        conversation
            .send(message("m-1", "now"), Urgency::Wake)
            .unwrap(),
        (
            Queued::Written,
            vec![prompt(1, "S1", &["i-1", "m-1"], "for context\n\nnow")]
        )
    );
}

// The spike: a prompt sent mid-turn JOINS the turn in flight, and both
// prompts reply `end_turn` with the joined turn's answer.
#[test]
fn a_steer_mid_turn_joins_it_and_each_prompt_ends_its_own_messages() {
    let mut conversation = opened();
    conversation
        .send(message("m-1", "sleep 10"), Urgency::Wake)
        .unwrap();
    conversation
        .send(message("m-2", "held"), Urgency::Wake)
        .unwrap();
    conversation
        .send(message("i-1", "aside"), Urgency::Inform)
        .unwrap();
    assert_eq!(
        conversation
            .send(message("m-3", "mango"), Urgency::Steer)
            .unwrap(),
        (
            Queued::Written,
            vec![prompt(2, "S1", &["i-1", "m-3"], "aside\n\nmango")]
        )
    );
    said(&mut conversation, "msg_b", "mango");
    assert_eq!(
        conversation.hear(end_turn(1)),
        (vec![answered(&["m-1"], "mango", "S1")], Vec::new())
    );
    assert_eq!(
        conversation.hear(end_turn(2)),
        (
            vec![answered(&["i-1", "m-3"], "mango", "S1")],
            vec![prompt(3, "S1", &["m-2"], "held")]
        )
    );
}

#[test]
fn a_steer_after_an_interrupt_is_held_first_and_asked_when_the_cancel_lands() {
    let mut conversation = opened();
    conversation
        .send(message("m-1", "sleep 25"), Urgency::Wake)
        .unwrap();
    conversation
        .send(message("m-2", "held"), Urgency::Wake)
        .unwrap();
    assert_eq!(
        conversation.interrupt().unwrap(),
        (
            true,
            vec![Act::Cancel {
                session: "S1".to_string()
            }]
        )
    );
    // Asked once per turn.
    assert_eq!(conversation.interrupt().unwrap(), (false, Vec::new()));
    assert_eq!(
        conversation
            .send(message("m-3", "first"), Urgency::Steer)
            .unwrap(),
        (Queued::Interrupting, Vec::new())
    );
    assert_eq!(
        conversation
            .send(message("m-4", "second"), Urgency::Steer)
            .unwrap(),
        (Queued::Interrupting, Vec::new())
    );
    assert_eq!(
        conversation.hear(Heard::Answered {
            key: 1,
            stop: Ok(Stop::Cancelled)
        }),
        (
            vec![
                ChildObservation::InterruptAnswered { accepted: true },
                cancelled(&["m-1"], "S1"),
            ],
            vec![prompt(2, "S1", &["m-3"], "first")]
        )
    );
    // The cancel is over: a new turn may be interrupted again.
    assert_eq!(
        conversation.hear(end_turn(2)).1,
        vec![prompt(3, "S1", &["m-4"], "second")]
    );
    assert_eq!(
        conversation.hear(end_turn(3)).1,
        vec![prompt(4, "S1", &["m-2"], "held")]
    );
    assert!(conversation.interrupt().unwrap().0);
}

#[test]
fn a_cancel_the_turn_outran_is_said_refused() {
    let mut conversation = opened();
    conversation
        .send(message("m-1", "quick"), Urgency::Wake)
        .unwrap();
    conversation.interrupt().unwrap();
    assert_eq!(
        conversation.hear(end_turn(1)).0,
        vec![
            ChildObservation::InterruptAnswered { accepted: false },
            answered(&["m-1"], "", "S1"),
        ]
    );
}

#[test]
fn an_interrupt_with_no_turn_asks_nothing() {
    let mut conversation = opened();
    assert_eq!(conversation.interrupt().unwrap(), (false, Vec::new()));
    let (mut opening, _) = Conversation::new(opening("", None));
    assert_eq!(opening.interrupt().unwrap(), (false, Vec::new()));
}

#[test]
fn a_clear_makes_a_new_session_and_is_the_rotation() {
    let mut conversation = opened();
    assert_eq!(
        conversation.clear().unwrap(),
        (
            Queued::Written,
            vec![Act::NewSession {
                cwd: "/work".to_string()
            }]
        )
    );
    // What follows a clear waits for it, a steer too.
    assert_eq!(
        conversation
            .send(message("m-1", "recall"), Urgency::Wake)
            .unwrap(),
        (Queued::Held, Vec::new())
    );
    assert_eq!(
        conversation
            .send(message("m-2", "steer"), Urgency::Steer)
            .unwrap(),
        (Queued::Held, Vec::new())
    );
    assert_eq!(
        conversation.hear(Heard::SessionReady(Ok("S2".to_string()))),
        (
            vec![ChildObservation::Cleared {
                native_id: Some("S2".to_string())
            }],
            vec![prompt(1, "S2", &["m-2"], "steer")]
        )
    );
    assert_eq!(
        conversation.hear(end_turn(1)),
        (
            vec![answered(&["m-2"], "", "S2")],
            vec![prompt(2, "S2", &["m-1"], "recall")]
        )
    );
}

#[test]
fn a_clear_mid_turn_waits_and_resets_the_model() {
    let (mut conversation, _) = Conversation::new(opening("m/x", None));
    conversation.hear(Heard::Initialized(Ok(())));
    conversation.hear(Heard::SessionReady(Ok("S1".to_string())));
    conversation.hear(Heard::ModelSet(Ok(())));
    conversation
        .send(message("m-1", "work"), Urgency::Wake)
        .unwrap();
    assert_eq!(conversation.clear().unwrap(), (Queued::Held, Vec::new()));
    assert_eq!(
        conversation.hear(end_turn(1)).1,
        vec![Act::NewSession {
            cwd: "/work".to_string()
        }]
    );
    assert_eq!(
        conversation.hear(Heard::SessionReady(Ok("S2".to_string()))),
        (
            Vec::new(),
            vec![Act::SetModel {
                session: "S2".to_string(),
                model: "m/x".to_string()
            }]
        )
    );
    assert_eq!(
        conversation.hear(Heard::ModelSet(Ok(()))).0,
        vec![ChildObservation::Cleared {
            native_id: Some("S2".to_string())
        }]
    );
}

#[test]
fn a_turn_that_stops_short_or_errs_fails() {
    let mut conversation = opened();
    conversation
        .send(message("m-1", "a"), Urgency::Wake)
        .unwrap();
    assert_eq!(
        conversation
            .hear(Heard::Answered {
                key: 1,
                stop: Ok(Stop::MaxTokens)
            })
            .0,
        vec![ChildObservation::TurnEnded {
            message_ids: vec!["m-1".to_string()],
            outcome: TurnOutcome::Failed {
                class: FailureClass::EngineError,
                detail: "the turn stopped: max_tokens".to_string(),
                native_id: Some("S1".to_string()),
            },
        }]
    );
    for (key, stop, name) in [
        (2, Stop::MaxTurnRequests, "max_turn_requests"),
        (3, Stop::Refusal, "refusal"),
    ] {
        conversation.send(message("m", "b"), Urgency::Wake).unwrap();
        let (seen, _) = conversation.hear(Heard::Answered {
            key,
            stop: Ok(stop),
        });
        assert!(
            matches!(&seen[..], [ChildObservation::TurnEnded {
                outcome: TurnOutcome::Failed { detail, .. }, ..
            }] if detail == &format!("the turn stopped: {name}")),
            "{seen:?}"
        );
    }
    conversation
        .send(message("m-9", "c"), Urgency::Wake)
        .unwrap();
    assert_eq!(
        conversation
            .hear(Heard::Answered {
                key: 4,
                stop: Err("Internal error".to_string())
            })
            .0,
        vec![ChildObservation::TurnEnded {
            message_ids: vec!["m-9".to_string()],
            outcome: TurnOutcome::Failed {
                class: FailureClass::EngineError,
                detail: "Internal error".to_string(),
                native_id: Some("S1".to_string()),
            },
        }]
    );
}

#[test]
fn an_answer_when_idle_is_nothing() {
    let mut conversation = opened();
    assert_eq!(conversation.hear(end_turn(7)), (Vec::new(), Vec::new()));
    // Words with no turn in flight are not kept for the next.
    said(&mut conversation, "msg_x", "stray");
    conversation
        .send(message("m-1", "a"), Urgency::Wake)
        .unwrap();
    assert_eq!(
        conversation.hear(end_turn(1)).0,
        vec![answered(&["m-1"], "", "S1")]
    );
}

#[test]
fn an_end_cancels_the_turn_closes_the_input_and_names_what_was_held() {
    let mut conversation = opened();
    conversation
        .send(message("m-1", "work"), Urgency::Wake)
        .unwrap();
    conversation
        .send(message("m-2", "later"), Urgency::Wake)
        .unwrap();
    conversation
        .send(message("i-1", "aside"), Urgency::Inform)
        .unwrap();
    conversation.clear().unwrap();
    assert_eq!(
        conversation.end().unwrap(),
        (
            vec!["i-1".to_string(), "m-2".to_string()],
            vec![
                Act::Cancel {
                    session: "S1".to_string()
                },
                Act::CloseInput
            ]
        )
    );
    // The cancelled turn still ends; nothing held follows it.
    assert_eq!(
        conversation.hear(Heard::Answered {
            key: 1,
            stop: Ok(Stop::Cancelled)
        }),
        (
            vec![
                ChildObservation::InterruptAnswered { accepted: true },
                cancelled(&["m-1"], "S1")
            ],
            Vec::new()
        )
    );
    assert_eq!(conversation.end(), Err(ChildError::Closed));
    assert_eq!(conversation.interrupt(), Err(ChildError::Closed));
    assert_eq!(conversation.clear(), Err(ChildError::Closed));
    assert!(conversation.release().is_empty());
}

#[test]
fn an_idle_end_only_closes_the_input() {
    let mut conversation = opened();
    assert_eq!(
        conversation.end().unwrap(),
        (Vec::new(), vec![Act::CloseInput])
    );
}

#[test]
fn a_release_closes_the_input_once() {
    let mut conversation = opened();
    assert_eq!(conversation.release(), vec![Act::CloseInput]);
    assert!(conversation.release().is_empty());
    assert_eq!(
        conversation.send(message("m-1", "x"), Urgency::Inform),
        Err(ChildError::Closed)
    );
}

#[test]
fn an_exit_mid_turn_fails_every_prompt_in_flight() {
    let mut conversation = opened();
    conversation
        .send(message("m-1", "a"), Urgency::Wake)
        .unwrap();
    conversation
        .send(message("m-2", "b"), Urgency::Steer)
        .unwrap();
    let exit = Exit {
        code: Some(7),
        signal: None,
    };
    let failed = |id: &str| ChildObservation::TurnEnded {
        message_ids: vec![id.to_string()],
        outcome: TurnOutcome::Failed {
            class: FailureClass::EngineError,
            detail: "the engine exited mid-turn: fell over".to_string(),
            native_id: Some("S1".to_string()),
        },
    };
    assert_eq!(
        conversation.exited(exit, "fell over".to_string()),
        vec![
            failed("m-1"),
            failed("m-2"),
            ChildObservation::Exited {
                exit,
                detail: "fell over".to_string()
            }
        ]
    );
    assert_eq!(
        conversation.send(message("m-3", "c"), Urgency::Wake),
        Err(ChildError::Closed)
    );
}
