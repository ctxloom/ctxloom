//! The session's run lifeline (plan 3.1): which incarnation it owes, whether
//! one is open, the stop flag. No mail, so no relaunch (owner ruling 101).
//! Each test is PORTED-FROM-GO (naming the Go test) or NEW-DESIGN.

use crate::aggregate::tests::{mint, Session};
use crate::MintSession;
use crate::{
    IncarnationEnded, IncarnationOwed, RecordIncarnationEnded, SessionEvent, StopOrdered,
    StopSession,
};

fn ended(incarnation: u32, cause: &str) -> RecordIncarnationEnded {
    RecordIncarnationEnded {
        incarnation,
        cause: cause.to_string(),
    }
}

/// A session (its first incarnation owed by the mint) whose run ended by `cause`.
fn first_run_ended(cause: &str) -> Session {
    let mut session = Session::minted();
    session.fact(ended(1, cause), "end:s:1");
    session
}

// NEW-DESIGN, owner ruling 2026-11-04 (S5 review Q1 (b)): the mint emits
// [SessionMinted, IncarnationOwed{1}] (plan 3.1: "a new session then owes its
// first incarnation"); this supersedes the separate OweIncarnation command.
#[test]
fn a_mint_records_the_session_and_owes_its_first_incarnation() {
    let mut session = Session::new();
    let events = session.run(mint(crate::aggregate::tests::HARP)).unwrap();
    assert!(matches!(events[0], SessionEvent::SessionMinted(_)));
    assert_eq!(
        events[1],
        SessionEvent::IncarnationOwed(IncarnationOwed {
            incarnation: 1,
            reason: "first".to_string()
        })
    );
    assert_eq!(events.len(), 2);
    let state = session.state();
    assert_eq!((state.incarnation, state.open_incarnation), (1, Some(1)));
}

// NEW-DESIGN, owner ruling 2026-11-04 (S5 review Q2 (b)): minimal lineage, the
// session's depth, is recorded at the mint. Parent and may_delegate are S10's.
#[test]
fn a_mint_records_the_sessions_depth() {
    let mut session = Session::new();
    session
        .run(MintSession {
            depth: 2,
            ..mint(crate::aggregate::tests::HARP)
        })
        .unwrap();
    assert_eq!(session.state().depth, 2);
}

// NEW-DESIGN (plan 3.1, owner ruling 101): an end closes the open
// incarnation and notices the parent; nothing owes a relaunch.
#[test]
fn an_end_closes_the_incarnation_and_notices_the_parent() {
    let mut session = Session::minted();
    let events = session.fact(ended(1, "runner-exit"), "end:s:1");
    assert_eq!(
        events,
        vec![SessionEvent::IncarnationEnded(IncarnationEnded {
            incarnation: 1,
            cause: "runner-exit".to_string(),
            notice_parent: true,
        })]
    );
    assert_eq!(session.state().open_incarnation, None);
}

// NEW-DESIGN (hand-test 2026-11-10; the proto's RecordEnd: "the run slice's
// IncarnationEnded supersedes it"): the end of a run is when the session's
// last run ended, its page's instant, so a session that ran says when it
// ended without the importer's RecordEnd.
#[test]
fn an_end_records_when_the_sessions_last_run_ended() {
    let mut session = Session::minted();
    assert_eq!(session.state().ended_at, None, "nothing has ended yet");
    session.fact(ended(1, "runner-exit"), "end:s:1");
    let state = session.state();
    let ended_at = state.ended_at.expect("the end is recorded");
    assert!(
        (ended_at.seconds, ended_at.nanos) >= (state.started_at.seconds, state.started_at.nanos),
        "ended {ended_at:?} before it started {:?}",
        state.started_at
    );
}

// PORTED-FROM-GO: internal/core/coord/launchgate_giveup_test.go
// TestAgentStop_OnAnEndedRunStillStopsTheRelaunch: a stop order sets the flag
// even after the run ended.
#[test]
fn a_stop_ordered_after_the_end_still_sets_the_flag() {
    let mut session = first_run_ended("runner-exit");
    let events = session
        .run(StopSession {
            requester_depth: 0,
            reason: "user".to_string(),
        })
        .unwrap();
    assert_eq!(
        events,
        vec![SessionEvent::StopOrdered(StopOrdered {
            reason: "user".to_string()
        })]
    );
    assert!(session.state().stopped);
}

// Owner ruling 101: no end owes a relaunch, whatever its cause, stopped or
// not; an idle reap, an expected end, notices nobody.
#[test]
fn no_end_owes_a_relaunch_and_an_idle_reap_notices_nobody() {
    for cause in ["runner-exit", "stopped", "orphaned", "launch-failed"] {
        let mut session = Session::minted();
        let events = session.fact(ended(1, cause), "end:s:1");
        assert_eq!(events.len(), 1, "{cause}: {events:?}");
        assert!(
            matches!(&events[0], SessionEvent::IncarnationEnded(e) if e.notice_parent),
            "{cause}"
        );
        assert_eq!(session.state().open_incarnation, None, "{cause}");
    }
    let mut reaped = Session::minted();
    let events = reaped.fact(ended(1, "idle-reaped"), "end:s:1");
    assert_eq!(
        events,
        vec![SessionEvent::IncarnationEnded(IncarnationEnded {
            incarnation: 1,
            cause: "idle-reaped".to_string(),
            notice_parent: false,
        })]
    );
}

// NEW-DESIGN (plan 3.1, Go's I2: "refused from a child"). The depth is
// STAMPED from the caller's identity by admission, never read from the
// payload; the aggregate refuses depth > 0.
#[test]
fn a_stop_ordered_by_a_child_is_refused() {
    let mut session = Session::minted();
    let (code, args) = session
        .run(StopSession {
            requester_depth: 2,
            reason: "x".to_string(),
        })
        .unwrap_err();
    assert_eq!(code, "sessions-child-cannot-stop");
    assert_eq!(args["depth"], "2");
    assert!(!session.state().stopped);
}

// NEW-DESIGN: a second stop order records nothing.
#[test]
fn a_second_stop_order_records_nothing() {
    let mut session = Session::minted();
    let stop = StopSession {
        requester_depth: 0,
        reason: "u".to_string(),
    };
    assert_eq!(session.run(stop.clone()).unwrap().len(), 1);
    assert!(session.run(stop).unwrap().is_empty());
}

// NEW-DESIGN: an end told twice (a retried policy step) records nothing the
// second time; the kernel answers it DUPLICATE by its external_id, and a
// different external_id for an already-ended incarnation is a no-op by level.
#[test]
fn an_end_for_an_incarnation_already_ended_records_nothing() {
    let mut session = first_run_ended("runner-exit");
    let events = session.fact(ended(1, "runner-exit"), "end:s:1:again");
    assert!(events.is_empty());
}

/// A session whose first run bound the engine's session `native-1` and ended.
fn bound_and_ended() -> Session {
    let mut session = Session::minted();
    session
        .bound(crate::BindEngineSession {
            native_id: "native-1".to_string(),
            transcript_path: String::new(),
            engine_version: None,
        })
        .unwrap();
    session.fact(ended(1, "runner-exit"), "end:s:1");
    session
}

fn resume(engine: &str) -> crate::ResumeSession {
    crate::ResumeSession {
        harp: crate::aggregate::tests::HARP.to_string(),
        engine: engine.to_string(),
    }
}

// Owner ruling 115: `run --session <harp>` is a new incarnation on the same
// session stream, the next after the highest owed, opened by the resume.
#[test]
fn a_resume_owes_the_next_incarnation_on_the_same_stream() {
    let mut session = bound_and_ended();
    let events = session.run(resume("")).unwrap();
    assert_eq!(
        events,
        vec![SessionEvent::IncarnationOwed(IncarnationOwed {
            incarnation: 2,
            reason: "resume".to_string(),
        })]
    );
    assert_eq!(
        (
            session.state().incarnation,
            session.state().open_incarnation
        ),
        (2, Some(2))
    );
    // Its end closes it, and the next resume owes the third.
    session.fact(ended(2, "runner-exit"), "end:s:2");
    let events = session.run(resume("claude-code")).unwrap();
    assert!(matches!(&events[..], [SessionEvent::IncarnationOwed(o)] if o.incarnation == 3));
}

#[test]
fn a_resume_while_an_incarnation_is_open_is_refused_as_running() {
    let mut session = Session::minted();
    session
        .bound(crate::BindEngineSession {
            native_id: "native-1".to_string(),
            transcript_path: String::new(),
            engine_version: None,
        })
        .unwrap();
    let (code, args) = session.run(resume("")).unwrap_err();
    assert_eq!(code, "sessions-session-running");
    assert_eq!(args["harp"], crate::aggregate::tests::HARP);
    assert_eq!(session.state().incarnation, 1);
}

#[test]
fn a_resume_before_the_engine_bound_a_session_is_refused() {
    let mut session = first_run_ended("runner-exit");
    let (code, _) = session.run(resume("")).unwrap_err();
    assert_eq!(code, "sessions-no-native-session");
}

#[test]
fn a_resume_on_another_engine_is_refused_naming_both() {
    let mut session = bound_and_ended();
    let (code, args) = session.run(resume("opencode")).unwrap_err();
    assert_eq!(code, "sessions-engine-mismatch");
    assert_eq!(args["engine"], "claude-code");
    assert_eq!(args["asked"], "opencode");
}

// The run application asks only for a claimed name, so a stream holding no
// session is one `session remove` destroyed (its claim is kept, ruling 94c).
#[test]
fn a_resume_of_a_stream_holding_no_session_is_refused_as_destroyed() {
    let mut session = Session::new();
    let (code, args) = session.run(resume("")).unwrap_err();
    assert_eq!(code, "sessions-session-destroyed");
    assert_eq!(args["harp"], crate::aggregate::tests::HARP);
}
