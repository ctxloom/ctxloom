//! The daemon's model on the session (D28): close, messages, clear and the
//! owner link. Each test is NEW-DESIGN, naming the decision it holds. Races
//! (two policies, a redelivery) are forced by calling the transitions in the
//! order the race would, never by waiting.

use crate::aggregate::tests::{mint, Session, HARP};
use crate::{
    Child, ChildEnded, ChildForgotten, ChildLaunchFailed, ChildLaunched, ClearChild,
    ClearRequested, CloseSession, EndChild, ForgetChild, IncarnationOwed, LaunchChild, MessageSent,
    MintSession, RecordChildLaunchFailed, RecordIncarnationEnded, SendMessage, SessionClosed,
    SessionEvent, SessionRef, StopOrdered, StopSession, TargetTrust, Urgency,
};

/// The stream the harness addresses: root `[7; 16]`, as text.
const ME: &str = "07070707-0707-0707-0707-070707070707";
const OWNER: &str = "0a0a0a0a-0a0a-0a0a-0a0a-0a0a0a0a0a0a";
const STRANGER: &str = "0b0b0b0b-0b0b-0b0b-0b0b-0b0b0b0b0b0b";
const CHILD: &str = "0c0c0c0c-0c0c-0c0c-0c0c-0c0c0c0c0c0c";

fn at(session: &str) -> SessionRef {
    SessionRef {
        project_id: "calm-otter".to_string(),
        session: session.to_string(),
    }
}

/// A child session: minted for `OWNER`, at depth 1.
fn child() -> Session {
    let mut session = Session::new();
    session
        .run(MintSession {
            owner: Some(at(OWNER)),
            depth: 1,
            ..mint(HARP)
        })
        .unwrap();
    session
}

fn close(requester: Option<&str>) -> CloseSession {
    CloseSession {
        cause: "done".to_string(),
        requester: requester.map(at),
    }
}

fn every_urgency() -> Vec<Urgency> {
    vec![Urgency::Inform, Urgency::Wake, Urgency::Steer]
}

fn send(id: &str, urgency: Urgency, requester: Option<&str>) -> SendMessage {
    SendMessage {
        message_id: id.to_string(),
        urgency,
        text: format!("text of {id}"),
        requester: requester.map(at),
        supported: every_urgency(),
    }
}

fn clear(requester: Option<&str>) -> ClearChild {
    ClearChild {
        clear_id: "c1".to_string(),
        requester: requester.map(at),
    }
}

fn launch(child: &str, requester: Option<&str>) -> LaunchChild {
    LaunchChild {
        child_project_id: "calm-otter".to_string(),
        child_session: child.to_string(),
        child_root: format!("/work/proj/wt-{child}"),
        harp: "child-harp".to_string(),
        engine: "claude-code".to_string(),
        trust: TargetTrust {
            same_project: true,
            trusted: false,
        },
        delegation_limit: 4,
        requester: requester.map(at),
    }
}

fn ended(incarnation: u32) -> RecordIncarnationEnded {
    RecordIncarnationEnded {
        incarnation,
        cause: "runner-exit".to_string(),
    }
}

// ----------------------------------------------------------------- close

// K1: a close is terminal, recorded once with its cause.
#[test]
fn a_close_records_the_session_closed_and_a_second_records_nothing() {
    let mut session = Session::minted();
    let events = session.run(close(None)).unwrap();
    assert_eq!(
        events,
        vec![SessionEvent::SessionClosed(SessionClosed {
            cause: "done".to_string()
        })]
    );
    let state = session.state();
    assert!(state.closed);
    assert_eq!(state.closed_cause, "done");
    assert!(session.run(close(None)).unwrap().is_empty());
}

#[test]
fn a_close_of_a_stream_holding_no_session_is_refused() {
    let mut session = Session::new();
    let (code, _) = session.run(close(None)).unwrap_err();
    assert_eq!(code, "sessions-no-session");
}

// S-f, D25: the child's owner may close it; any other session may not; the
// user (no session requester) may.
#[test]
fn a_child_is_closed_by_its_owner_or_the_user_and_by_no_other_session() {
    let mut refused = child();
    let (code, args) = refused.run(close(Some(STRANGER))).unwrap_err();
    assert_eq!(code, "sessions-not-owner");
    assert_eq!(args["requester"], STRANGER);
    assert!(!refused.state().closed);

    let mut by_owner = child();
    assert_eq!(by_owner.run(close(Some(OWNER))).unwrap().len(), 1);
    let mut by_user = child();
    assert_eq!(by_user.run(close(None)).unwrap().len(), 1);
}

// S-f: a top-level session has no owner, so no session requester may close
// it, itself included (the human closes it).
#[test]
fn a_top_level_session_is_closed_by_the_user_alone() {
    let mut session = Session::minted();
    let (code, _) = session.run(close(Some(ME))).unwrap_err();
    assert_eq!(code, "sessions-not-owner");
}

// K1: nothing is sent, cleared or launched on a closed session.
#[test]
fn a_closed_session_takes_no_message_clear_or_child() {
    let mut session = Session::minted();
    session.run(close(None)).unwrap();
    for (code, refused) in [
        (
            "sessions-session-closed",
            session.run(send("m1", Urgency::Wake, None)).unwrap_err(),
        ),
        (
            "sessions-session-closed",
            session.run(clear(None)).unwrap_err(),
        ),
        (
            "sessions-owner-closed",
            session.run(launch(CHILD, None)).unwrap_err(),
        ),
    ] {
        assert_eq!(refused.0, code);
        assert_eq!(refused.1["harp"], HARP);
    }
}

// --------------------------------------------------------- stop (S-f)

// D25/S-f reconciles StopSession: an owner stops its own child whatever the
// child's depth; another session may not; the depth rule still guards a
// requester that names no session.
#[test]
fn a_child_is_stopped_by_its_owner_and_by_no_other_session() {
    let stop = |requester: Option<&str>, depth: u32| StopSession {
        requester_depth: depth,
        reason: "r".to_string(),
        requester: requester.map(at),
    };
    let mut by_owner = child();
    assert_eq!(
        by_owner.run(stop(Some(OWNER), 1)).unwrap(),
        vec![SessionEvent::StopOrdered(StopOrdered {
            reason: "r".to_string()
        })]
    );
    let mut by_stranger = child();
    let (code, _) = by_stranger.run(stop(Some(STRANGER), 0)).unwrap_err();
    assert_eq!(code, "sessions-not-owner");
    let mut by_depth = child();
    let (code, _) = by_depth.run(stop(None, 1)).unwrap_err();
    assert_eq!(code, "sessions-child-cannot-stop");
}

// ------------------------------------------------------------- messages

// D12, D28: one message type; it records its id, urgency, text and sender.
#[test]
fn a_message_records_its_id_urgency_text_and_sender() {
    let mut session = child();
    let events = session
        .run(send("m1", Urgency::Inform, Some(OWNER)))
        .unwrap();
    assert_eq!(
        events,
        vec![SessionEvent::MessageSent(MessageSent {
            message_id: "m1".to_string(),
            urgency: Urgency::Inform,
            text: "text of m1".to_string(),
            sender: Some(at(OWNER)),
        })]
    );
}

// D11, S-f: only the owner and the user send a session mail.
#[test]
fn a_message_from_a_session_that_is_not_the_owner_is_refused() {
    let mut session = child();
    let (code, _) = session
        .run(send("m1", Urgency::Wake, Some(STRANGER)))
        .unwrap_err();
    assert_eq!(code, "sessions-not-owner");
    let mut top = Session::minted();
    let (code, _) = top.run(send("m1", Urgency::Wake, Some(OWNER))).unwrap_err();
    assert_eq!(code, "sessions-not-owner");
}

#[test]
fn a_message_to_a_stream_holding_no_session_is_refused() {
    let mut session = Session::new();
    let (code, _) = session.run(send("m1", Urgency::Wake, None)).unwrap_err();
    assert_eq!(code, "sessions-no-session");
}

// S-c: an urgency the engine cannot honour is refused on the evidence; no
// evidence proves nothing, so every urgency is refused.
#[test]
fn an_urgency_the_engine_cannot_honour_is_refused() {
    let mut session = Session::minted();
    let (code, args) = session
        .run(SendMessage {
            supported: vec![Urgency::Inform, Urgency::Wake],
            ..send("m1", Urgency::Steer, None)
        })
        .unwrap_err();
    assert_eq!(code, "sessions-urgency-unsupported");
    assert_eq!(args["urgency"], "steer");
    let (code, args) = session
        .run(SendMessage {
            supported: Vec::new(),
            ..send("m2", Urgency::Inform, None)
        })
        .unwrap_err();
    assert_eq!(code, "sessions-urgency-unsupported");
    assert_eq!(args["urgency"], "inform");
}

// M2: a wake to an open session with no open incarnation owes the next one
// in the same decision.
#[test]
fn a_wake_to_a_session_with_no_run_open_owes_the_next_incarnation() {
    let mut session = Session::minted();
    session.fact(ended(1), "end:s:1");
    let events = session.run(send("m1", Urgency::Wake, None)).unwrap();
    assert_eq!(events.len(), 2, "{events:?}");
    assert!(matches!(&events[0], SessionEvent::MessageSent(m) if m.message_id == "m1"));
    assert_eq!(
        events[1],
        SessionEvent::IncarnationOwed(IncarnationOwed {
            incarnation: 2,
            reason: "wake".to_string(),
        })
    );
    let state = session.state();
    assert_eq!((state.incarnation, state.open_incarnation), (2, Some(2)));
    // A second wake finds the incarnation owed: nothing more is owed.
    let events = session.run(send("m2", Urgency::Wake, None)).unwrap();
    assert_eq!(events.len(), 1, "{events:?}");
}

// D30, M2: an inform owes nothing; a wake to a session with its run open
// owes nothing; nor does a wake to a stopped session.
#[test]
fn only_a_wake_to_an_idle_unstopped_session_owes_an_incarnation() {
    let mut open = Session::minted();
    assert_eq!(open.run(send("m1", Urgency::Wake, None)).unwrap().len(), 1);

    let mut informed = Session::minted();
    informed.fact(ended(1), "end:s:1");
    assert_eq!(
        informed
            .run(send("m1", Urgency::Inform, None))
            .unwrap()
            .len(),
        1
    );
    assert_eq!(informed.state().open_incarnation, None);

    let mut stopped = Session::minted();
    stopped
        .run(StopSession {
            requester_depth: 0,
            reason: "u".to_string(),
            requester: None,
        })
        .unwrap();
    stopped.fact(ended(1), "end:s:1");
    assert_eq!(
        stopped.run(send("m1", Urgency::Wake, None)).unwrap().len(),
        1
    );
}

// D28: the command id is derived from the message id, so a redelivery of the
// same message is DUPLICATE, and another message is not.
#[test]
fn a_message_redelivered_under_its_id_is_a_duplicate() {
    let mut session = Session::minted();
    let message = send("m1", Urgency::Inform, None);
    let first = session.keyed(&message, "m1").unwrap();
    assert_eq!(first.map(|events| events.len()), Some(1));
    assert_eq!(session.keyed(&message, "m1").unwrap(), None, "a duplicate");
    let other = send("m2", Urgency::Inform, None);
    assert_eq!(
        session.keyed(&other, "m2").unwrap().map(|e| e.len()),
        Some(1)
    );
}

#[test]
fn a_command_id_is_a_pure_function_of_its_key() {
    assert_eq!(crate::command_id("send:m1"), crate::command_id("send:m1"));
    assert_ne!(crate::command_id("send:m1"), crate::command_id("send:m2"));
}

// ---------------------------------------------------------------- clear

// M1: a clear is requested of a child by its owner (or the user).
#[test]
fn a_clear_is_requested_by_the_owner_and_refused_from_another_session() {
    let mut session = child();
    assert_eq!(
        session.run(clear(Some(OWNER))).unwrap(),
        vec![SessionEvent::ClearRequested(ClearRequested {
            clear_id: "c1".to_string()
        })]
    );
    let (code, _) = session.run(clear(Some(STRANGER))).unwrap_err();
    assert_eq!(code, "sessions-not-owner");
    let mut none = Session::new();
    let (code, _) = none.run(clear(None)).unwrap_err();
    assert_eq!(code, "sessions-no-session");
}

// ------------------------------------------------------------ the owner

// D26: a mint records its owner.
#[test]
fn a_mint_records_its_owner() {
    assert_eq!(child().state().owner, Some(at(OWNER)));
    assert_eq!(Session::minted().state().owner, None);
}

// K2: a launch is recorded on the owner, which then holds the child.
#[test]
fn a_launch_records_the_child_on_the_owner() {
    let mut owner = Session::minted();
    let events = owner.run(launch(CHILD, Some(ME))).unwrap();
    assert_eq!(
        events,
        vec![SessionEvent::ChildLaunched(ChildLaunched {
            child: Some(at(CHILD)),
            child_root: format!("/work/proj/wt-{CHILD}"),
            harp: "child-harp".to_string(),
            engine: "claude-code".to_string(),
            depth: 1,
        })]
    );
    assert_eq!(
        owner.state().children,
        vec![Child {
            session: Some(at(CHILD)),
            root: format!("/work/proj/wt-{CHILD}"),
        }]
    );
}

// D25: only the owner session itself (or the user) launches its children.
#[test]
fn a_launch_from_another_session_is_refused() {
    let mut owner = Session::minted();
    let (code, args) = owner.run(launch(CHILD, Some(STRANGER))).unwrap_err();
    assert_eq!(code, "sessions-not-owner");
    assert_eq!(args["requester"], STRANGER);
    assert!(owner.run(launch(CHILD, None)).is_ok());
}

// K2: nothing orchestrates a child of an owner with no run open.
#[test]
fn a_launch_by_an_owner_with_no_run_open_is_refused() {
    let mut owner = Session::minted();
    owner.fact(ended(1), "end:s:1");
    let (code, args) = owner.run(launch(CHILD, None)).unwrap_err();
    assert_eq!(code, "sessions-no-open-incarnation");
    assert_eq!(args["harp"], HARP);
}

// K2: the owner's delegation limit, fail closed.
#[test]
fn a_launch_past_the_delegation_limit_is_refused() {
    let mut owner = Session::minted();
    let limited = |child: &str, limit: u32| LaunchChild {
        delegation_limit: limit,
        ..launch(child, None)
    };
    owner.run(limited(CHILD, 1)).unwrap();
    let (code, args) = owner.run(limited(STRANGER, 1)).unwrap_err();
    assert_eq!(code, "sessions-delegation-limit");
    assert_eq!((args["limit"].as_str(), args["open"].as_str()), ("1", "1"));
    let mut none = Session::minted();
    let (code, _) = none.run(limited(CHILD, 0)).unwrap_err();
    assert_eq!(code, "sessions-delegation-limit");
}

// D26, S8: a child in another repository needs that repository trusted.
#[test]
fn a_launch_into_an_untrusted_repository_is_refused() {
    let mut owner = Session::minted();
    let elsewhere = |trusted: bool| LaunchChild {
        child_project_id: "far-heron".to_string(),
        trust: TargetTrust {
            same_project: false,
            trusted,
        },
        ..launch(CHILD, None)
    };
    let (code, args) = owner.run(elsewhere(false)).unwrap_err();
    assert_eq!(code, "sessions-untrusted-target");
    assert_eq!(args["root"], format!("/work/proj/wt-{CHILD}"));
    let events = owner.run(elsewhere(true)).unwrap();
    assert!(matches!(&events[0], SessionEvent::ChildLaunched(c)
        if c.child.as_ref().map(|r| r.project_id.as_str()) == Some("far-heron")));
}

// K2, M3: the owner stops holding a child that ended, that failed to launch
// or that is forgotten (its destroy); a child it does not hold records
// nothing.
#[test]
fn the_owner_stops_holding_a_child_that_ended_failed_or_was_forgotten() {
    let mut owner = Session::minted();
    owner.run(launch(CHILD, None)).unwrap();
    owner.run(launch(STRANGER, None)).unwrap();
    owner.run(launch(OWNER, None)).unwrap();
    assert_eq!(owner.state().children.len(), 3);

    let events = owner
        .run(EndChild {
            child: Some(at(CHILD)),
            cause: "closed".to_string(),
        })
        .unwrap();
    assert_eq!(
        events,
        vec![SessionEvent::ChildEnded(ChildEnded {
            child: Some(at(CHILD)),
            cause: "closed".to_string(),
        })]
    );
    let failed = owner.fact(
        RecordChildLaunchFailed {
            child: Some(at(STRANGER)),
            reason: "sessions-names-taken".to_string(),
        },
        "launch-failed:o:s",
    );
    assert_eq!(
        failed,
        vec![SessionEvent::ChildLaunchFailed(ChildLaunchFailed {
            child: Some(at(STRANGER)),
            reason: "sessions-names-taken".to_string(),
        })]
    );
    let forgotten = owner
        .run(ForgetChild {
            child: Some(at(OWNER)),
        })
        .unwrap();
    assert_eq!(
        forgotten,
        vec![SessionEvent::ChildForgotten(ChildForgotten {
            child: Some(at(OWNER)),
        })]
    );
    assert!(owner.state().children.is_empty());

    // Not held any more: nothing recorded, whichever comes second (a race of
    // the child-ended and destroy policies).
    assert!(owner
        .run(ForgetChild {
            child: Some(at(CHILD)),
        })
        .unwrap()
        .is_empty());
    assert!(owner
        .run(EndChild {
            child: Some(at(OWNER)),
            cause: "closed".to_string(),
        })
        .unwrap()
        .is_empty());
    assert!(owner
        .fact(
            RecordChildLaunchFailed {
                child: Some(at(CHILD)),
                reason: "x".to_string(),
            },
            "launch-failed:o:c",
        )
        .is_empty());
}

// K2: a launch frees a slot when a child ends, so the limit counts the open.
#[test]
fn an_ended_child_frees_its_place_under_the_limit() {
    let mut owner = Session::minted();
    let limited = |child: &str| LaunchChild {
        delegation_limit: 1,
        ..launch(child, None)
    };
    owner.run(limited(CHILD)).unwrap();
    owner
        .run(EndChild {
            child: Some(at(CHILD)),
            cause: "closed".to_string(),
        })
        .unwrap();
    assert!(owner.run(limited(STRANGER)).is_ok());
}

// The new state survives its wire form.
#[test]
fn the_owner_link_and_the_close_round_trip_through_the_wire_form() {
    let mut owner = child();
    owner.run(launch(CHILD, None)).unwrap();
    owner.run(close(None)).unwrap();
    let state = owner.state();
    let wire = sessions_proto::v1::SessionState::from(state.clone());
    assert_eq!(crate::SessionState::try_from(wire).unwrap(), state);
}
