//! What `session show` says of a session's latest run (step 9).

use daemon_board::{RunFold, TurnFailure};
use runs::{EndCause, Grant, TurnFailureClass};

use super::{failure_of, grant_of};

fn run() -> RunFold {
    RunFold::default()
}

// The grant journaled at the run's enqueue is the session's, its names and
// its document; a run that journaled none has none.
#[test]
fn the_grant_is_the_runs_journaled_one() {
    let mut fold = run();
    assert_eq!(grant_of(&fold), None);
    fold.state.grant = Some(Grant {
        mcp_servers: vec!["ctxloom".to_string(), "fix-tools".to_string()],
        permissions: r#"{"defaultMode":"plan"}"#.to_string(),
    });
    assert_eq!(
        grant_of(&fold),
        Some(daemon_proto::v1::SessionGrant {
            mcp_servers: vec!["ctxloom".to_string(), "fix-tools".to_string()],
            permissions: r#"{"defaultMode":"plan"}"#.to_string(),
        })
    );
}

// A turn that ran into its rate limit is said by its class, its engine's
// words and the notice that says nothing tries it again and how to resume.
#[test]
fn a_rate_limited_turn_is_said_with_how_to_resume_it() {
    let mut fold = run();
    assert_eq!(failure_of("brisk-copper-moth", true, &fold), None);
    fold.last_failure = Some(TurnFailure {
        class: TurnFailureClass::RateLimited,
        detail: "429 rate_limit_error".to_string(),
    });
    let failure = failure_of("brisk-copper-moth", true, &fold).expect("a failure");
    assert_eq!(failure.kind, "rate-limited");
    assert_eq!(failure.detail, "429 rate_limit_error");
    let notice = failure.notice.expect("its notice");
    assert_eq!(notice.code, "daemon-api-turn-rate-limited");
    assert!(notice.text.contains("rate limit"), "{}", notice.text);
    assert!(
        notice
            .text
            .contains("ctxloom session send brisk-copper-moth wake"),
        "{}",
        notice.text
    );
}

// Each turn class has a word of its own.
#[test]
fn each_turn_class_is_its_word() {
    for (class, kind) in [
        (TurnFailureClass::CredentialRefused, "credential-refused"),
        (TurnFailureClass::EngineError, "engine-error"),
        (TurnFailureClass::Interrupted, "interrupted"),
        (TurnFailureClass::RunnerLost, "runner-lost"),
    ] {
        let mut fold = run();
        fold.last_failure = Some(TurnFailure {
            class,
            detail: String::new(),
        });
        let failure = failure_of("kid", true, &fold).expect("a failure");
        assert_eq!(failure.kind, kind);
        assert_eq!(
            failure.notice.expect("a notice").code,
            format!("daemon-api-turn-{kind}")
        );
    }
}

// A run that never started is launch-failed, said in its launcher's words,
// a child's as the child's engine; it outranks a turn's failure.
#[test]
fn a_run_that_never_started_is_launch_failed_in_its_launchers_words() {
    let mut fold = run();
    fold.state.ended = Some(EndCause::LaunchFailed);
    fold.end_detail = "claude: program not found".to_string();
    fold.last_failure = Some(TurnFailure {
        class: TurnFailureClass::EngineError,
        detail: String::new(),
    });
    let failure = failure_of("kid", true, &fold).expect("a failure");
    assert_eq!(failure.kind, "launch-failed");
    assert_eq!(failure.detail, "claude: program not found");
    let notice = failure.notice.expect("its notice");
    assert_eq!(notice.code, "daemon-api-run-launch-failed-child");
    assert!(
        notice
            .text
            .contains("could not start the child's engine: claude: program not found"),
        "{}",
        notice.text
    );
    let primary = failure_of("amber-quiet-heron", false, &fold).expect("a failure");
    assert_eq!(
        primary.notice.expect("its notice").code,
        "daemon-api-run-launch-failed"
    );
}

// A run that ended otherwise, with no failed turn, says no failure.
#[test]
fn a_run_that_ended_well_says_none() {
    let mut fold = run();
    fold.state.ended = Some(EndCause::RunnerExit);
    assert_eq!(failure_of("kid", true, &fold), None);
}
