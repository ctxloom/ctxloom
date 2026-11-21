//! The session's run lifeline (plan 3.1): which incarnation it owes, whether
//! one is open, the stop flag. No mail, so no relaunch (owner ruling 101).
//! Each test is PORTED-FROM-GO (naming the Go test) or NEW-DESIGN.

use crate::aggregate::tests::{mint, Session};
use crate::MintSession;
use crate::{IncarnationEnded, IncarnationOwed, RecordIncarnationEnded, SessionEvent, StopOrdered};

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
            owner: None,
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

// Owner ruling 101: no end owes a relaunch, whatever its cause; an idle reap, an expected end, notices nobody.
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
    bound_and_ended_in(None)
}

/// The same, minted on the workspace axis `workspace` (none recorded).
fn bound_and_ended_in(workspace: Option<&str>) -> Session {
    let mut session = Session::new();
    session
        .run(MintSession {
            workspace: workspace.map(str::to_string),
            ..mint(crate::aggregate::tests::HARP)
        })
        .unwrap();
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
        workspace: String::new(),
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

// Owner ruling 2026-11-12: the workspace axis is a session trait, recorded at
// its mint, so a resume works where the session worked.
#[test]
fn a_mint_records_the_sessions_workspace() {
    let mut session = Session::new();
    let events = session
        .run(MintSession {
            workspace: Some("worktree".to_string()),
            ..mint(crate::aggregate::tests::HARP)
        })
        .unwrap();
    assert!(matches!(&events[0],
        SessionEvent::SessionMinted(minted) if minted.workspace.as_deref() == Some("worktree")));
    assert_eq!(session.state().workspace.as_deref(), Some("worktree"));
}

fn resume_in(workspace: &str) -> crate::ResumeSession {
    crate::ResumeSession {
        workspace: workspace.to_string(),
        ..resume("")
    }
}

// A resume asking for another workspace than the session's is refused,
// naming both (claude keys its conversation by where it works).
#[test]
fn a_resume_in_another_workspace_is_refused_naming_both() {
    let mut session = bound_and_ended_in(Some("worktree"));
    let (code, args) = session.run(resume_in("none")).unwrap_err();
    assert_eq!(code, "sessions-workspace-mismatch");
    assert_eq!(args["workspace"], "worktree");
    assert_eq!(args["asked"], "none");
    assert_eq!(session.state().incarnation, 1, "nothing owed");
}

// The session's own workspace, asked or not, resumes it.
#[test]
fn a_resume_in_the_sessions_own_workspace_or_none_asked_resumes_it() {
    for asked in ["", "worktree"] {
        let mut session = bound_and_ended_in(Some("worktree"));
        assert!(session.run(resume_in(asked)).is_ok(), "{asked:?}");
    }
}

// A session from before the axis was recorded worked in the project.
#[test]
fn a_session_with_no_workspace_recorded_resumes_as_one_in_the_project() {
    let mut session = bound_and_ended_in(None);
    assert!(session.run(resume_in("none")).is_ok());
    let mut session = bound_and_ended_in(None);
    let (code, args) = session.run(resume_in("worktree")).unwrap_err();
    assert_eq!(code, "sessions-workspace-mismatch");
    assert_eq!(args["workspace"], "none");
}

fn rehost(recent: u32, cause: &str) -> crate::RehostSession {
    crate::RehostSession {
        recent_rehosts: recent,
        cause: cause.to_string(),
    }
}

/// A child whose first run bound its native session and was lost: its
/// incarnation ended `orphaned` by a sweep.
fn lost_child() -> Session {
    let mut session = Session::new();
    session.run(mint(crate::aggregate::tests::HARP)).unwrap();
    session
        .bound(crate::BindEngineSession {
            native_id: "native-1".to_string(),
            transcript_path: String::new(),
            engine_version: None,
        })
        .unwrap();
    session.fact(ended(1, "orphaned"), "end:s:1");
    session
}

// T19, s4-Q9: a re-host owes the lost child its next incarnation, reason
// `rehost`, on the same stream.
#[test]
fn a_rehost_owes_the_lost_childs_next_incarnation() {
    let mut session = lost_child();
    let events = session.run(rehost(0, "")).unwrap();
    assert_eq!(
        events,
        vec![SessionEvent::IncarnationOwed(IncarnationOwed {
            incarnation: 2,
            reason: "rehost".to_string(),
        })]
    );
    assert_eq!(session.state().open_incarnation, Some(2));
}

// S47: at 60 recent re-hosts it is refused, naming the budget, and nothing
// is owed; at 59 it is not.
#[test]
fn a_rehost_past_its_budget_is_refused_and_owes_nothing() {
    let mut session = lost_child();
    let (code, args) = session.run(rehost(60, "rehost")).unwrap_err();
    assert_eq!(code, "sessions-rehost-budget-spent");
    assert_eq!(args["limit"], "60");
    assert_eq!(args["recent"], "60");
    assert_eq!(args["harp"], crate::aggregate::tests::HARP);
    assert_eq!(session.state().open_incarnation, None);
    assert!(session.run(rehost(59, "")).is_ok());
}

// s4-Q5: a drain's hand-over re-hosts with cause `drain`, never refused for
// the budget.
#[test]
fn a_drains_rehost_is_never_refused_for_the_budget() {
    let mut session = lost_child();
    let events = session.run(rehost(600, "drain")).unwrap();
    assert!(matches!(&events[..],
        [SessionEvent::IncarnationOwed(o)] if o.reason == "drain" && o.incarnation == 2));
}

#[test]
fn a_rehost_while_an_incarnation_is_open_is_refused_as_running() {
    let mut session = lost_child();
    session.run(rehost(0, "")).unwrap();
    let (code, _) = session.run(rehost(0, "")).unwrap_err();
    assert_eq!(code, "sessions-session-running");
}

#[test]
fn a_rehost_of_a_child_with_no_native_session_is_refused() {
    let mut session = first_run_ended("orphaned");
    let (code, _) = session.run(rehost(0, "")).unwrap_err();
    assert_eq!(code, "sessions-no-native-session");
}

#[test]
fn a_rehost_of_no_session_is_refused() {
    let mut session = Session::new();
    let (code, _) = session.run(rehost(0, "")).unwrap_err();
    assert_eq!(code, "sessions-no-session");
}

#[test]
fn a_rehost_of_a_closed_child_is_refused() {
    let mut session = lost_child();
    session
        .run(crate::CloseSession {
            requester: None,
            cause: "closed".to_string(),
        })
        .unwrap();
    let (code, _) = session.run(rehost(0, "")).unwrap_err();
    assert_eq!(code, "sessions-session-closed");
}

// ------------------------------------- a resume reopens a closed session
//
// Owner 2026-11-15: an explicit resume REOPENS a closed session. A close is
// ctxloom's control lifecycle of its sessions, apart from the engine's
// conversation: `run --session` on a closed session records that it was
// reopened, then owes its next incarnation, and the session takes work again
// with its close's cause cleared.

/// The bound session, its run ended, then closed by a human.
fn bound_ended_and_closed() -> Session {
    let mut session = bound_and_ended();
    session
        .run(crate::CloseSession {
            requester: None,
            cause: "closed".to_string(),
        })
        .unwrap();
    assert!(session.state().closed);
    session
}

#[test]
fn a_resume_of_a_closed_session_reopens_it_then_owes_the_next_incarnation() {
    let mut session = bound_ended_and_closed();
    let events = session.run(resume("")).unwrap();
    assert_eq!(
        events,
        vec![
            SessionEvent::SessionReopened(crate::SessionReopened {
                cause: "resume".to_string(),
            }),
            SessionEvent::IncarnationOwed(IncarnationOwed {
                incarnation: 2,
                reason: "resume".to_string(),
            }),
        ]
    );
    let state = session.state();
    assert!(!state.closed, "reopened: {state:?}");
    assert_eq!(state.closed_cause, "", "its close's cause cleared");
    assert_eq!(state.open_incarnation, Some(2));
}

#[test]
fn a_reopened_session_takes_messages_again() {
    let mut session = bound_ended_and_closed();
    session.run(resume("")).unwrap();
    let sent = session.run(crate::SendMessage {
        message_id: "m1".to_string(),
        urgency: crate::Urgency::Inform,
        text: "after the reopen".to_string(),
        requester: None,
        supported: vec![crate::Urgency::Inform],
    });
    assert!(sent.is_ok(), "{sent:?}");
}

#[test]
fn a_resume_of_an_open_session_records_no_reopen() {
    let mut session = bound_and_ended();
    let events = session.run(resume("")).unwrap();
    assert!(
        !events
            .iter()
            .any(|e| matches!(e, SessionEvent::SessionReopened(_))),
        "{events:?}"
    );
}

// Only an explicit resume reopens: a re-host of a closed child stays refused
// (`a_rehost_of_a_closed_child_is_refused`, above).

// Owner 2026-11-15: the stop latch is removed. `StopOrdered`, in streams
// recorded before, is retired in place: it still reads, and folds to nothing.
#[test]
fn a_stream_holding_a_retired_stop_order_folds_as_before() {
    let mut session = first_run_ended("runner-exit");
    let before = session.state();
    session.given([SessionEvent::StopOrdered(StopOrdered {
        reason: "user".to_string(),
    })]);
    assert_eq!(session.state(), before);
}

// -------------------------------------------- a restart (step 10e Stage 2a)
//
// Owner rulings 2026-11-20: the SESSION decides when a restart is owed. A
// request records `RestartOwed` for the open incarnation, once; the next
// incarnation owed closes it; it is checkable from the stream alone.

fn restart(reason: crate::RestartReason) -> crate::RequestRestart {
    crate::RequestRestart {
        reason,
        note: "the operator's note".to_string(),
    }
}

/// A child whose first run is open and bound to its native session.
fn running_child() -> Session {
    let mut session = Session::minted();
    session
        .bound(crate::BindEngineSession {
            native_id: "native-1".to_string(),
            transcript_path: String::new(),
            engine_version: None,
        })
        .unwrap();
    session
}

#[test]
fn a_restart_request_owes_a_restart_of_the_open_incarnation() {
    let mut session = running_child();
    let events = session
        .run(restart(crate::RestartReason::Operator))
        .unwrap();
    assert_eq!(
        events,
        vec![SessionEvent::RestartOwed(crate::RestartOwed {
            reason: crate::RestartReason::Operator,
            incarnation: 1,
            note: "the operator's note".to_string(),
        })]
    );
    let owed = session.state().restart_owed.expect("the fold holds it");
    assert_eq!(
        (owed.incarnation, owed.reason),
        (1, crate::RestartReason::Operator)
    );
}

// A request while one is owed records nothing, whatever its reason: an
// operator's too.
#[test]
fn a_request_while_a_restart_is_owed_records_nothing() {
    let mut session = running_child();
    session
        .run(restart(crate::RestartReason::Operator))
        .unwrap();
    let pages = session.pages().len();
    for reason in [
        crate::RestartReason::Operator,
        crate::RestartReason::Context,
        crate::RestartReason::Config,
    ] {
        assert_eq!(session.run(restart(reason)).unwrap(), vec![], "{reason:?}");
    }
    assert_eq!(session.pages().len(), pages);
    // The first reason stands.
    let owed = session.state().restart_owed.unwrap();
    assert_eq!(owed.reason, crate::RestartReason::Operator);
}

// The same request replayed under its key is a DUPLICATE, not a second
// event.
#[test]
fn a_replayed_request_is_one_restart() {
    let mut session = running_child();
    let request = restart(crate::RestartReason::Operator);
    let first = session.keyed(&request, "restart:s:1:operator").unwrap();
    assert!(matches!(
        first.as_deref(),
        Some([SessionEvent::RestartOwed(_)])
    ));
    assert_eq!(
        session.keyed(&request, "restart:s:1:operator").unwrap(),
        None
    );
}

// The owed restart is in the stream, not in a process: it outlives the
// incarnation's end, and the next incarnation owed closes it.
#[test]
fn an_owed_restart_survives_the_end_and_the_next_incarnation_closes_it() {
    let mut session = running_child();
    session
        .run(restart(crate::RestartReason::Operator))
        .unwrap();
    session.fact(ended(1, "restarted"), "end:s:1");
    assert!(
        session.state().restart_owed.is_some(),
        "owed until it is hosted again"
    );
    let events = session.run(rehost(600, "restart")).unwrap();
    assert!(
        matches!(&events[..],
        [SessionEvent::IncarnationOwed(o)] if o.reason == "restart" && o.incarnation == 2),
        "a restart is never refused for the re-host budget: {events:?}"
    );
    let state = session.state();
    assert_eq!(state.restart_owed, None, "the next incarnation closed it");
    // A new request in the new incarnation is a new restart.
    let again = session
        .run(restart(crate::RestartReason::Operator))
        .unwrap();
    assert!(matches!(&again[..], [SessionEvent::RestartOwed(o)] if o.incarnation == 2));
}

#[test]
fn a_restart_of_no_session_is_refused() {
    let mut session = Session::new();
    let (code, _) = session
        .run(restart(crate::RestartReason::Operator))
        .unwrap_err();
    assert_eq!(code, "sessions-no-session");
}

#[test]
fn a_restart_of_a_closed_session_is_refused() {
    let mut session = running_child();
    session
        .run(crate::CloseSession {
            requester: None,
            cause: "closed".to_string(),
        })
        .unwrap();
    let (code, args) = session
        .run(restart(crate::RestartReason::Operator))
        .unwrap_err();
    assert_eq!(code, "sessions-session-closed");
    assert_eq!(args["harp"], crate::aggregate::tests::HARP);
}

#[test]
fn a_restart_of_a_session_with_no_run_open_is_refused() {
    let mut session = bound_and_ended();
    let (code, args) = session
        .run(restart(crate::RestartReason::Operator))
        .unwrap_err();
    assert_eq!(code, "sessions-no-run-to-restart");
    assert_eq!(args["harp"], crate::aggregate::tests::HARP);
    assert_eq!(session.state().restart_owed, None);
}

#[test]
fn a_restart_before_the_engine_bound_a_session_is_refused() {
    let mut session = Session::minted();
    let (code, _) = session
        .run(restart(crate::RestartReason::Operator))
        .unwrap_err();
    assert_eq!(code, "sessions-no-native-session");
}
