//! The terminal answer to the restart offer, `[Y/n]`: a bare Enter is a yes,
//! `n` and `no` (any case) are a no, a closed input is a no, anything else is
//! a no.

use super::answered;
use cli_support::Exit;

#[test]
fn a_bare_enter_is_a_yes() {
    for enter in ["\n", "\r\n", "  \n"] {
        assert!(answered(Some(enter)), "{enter:?}");
    }
}

#[test]
fn y_and_yes_are_a_yes_however_written() {
    for yes in ["y\n", "Y", "yes", " YES \n", "Yes\r\n"] {
        assert!(answered(Some(yes)), "{yes:?}");
    }
}

#[test]
fn n_and_no_and_anything_else_are_a_no() {
    for no in [
        "n\n", "N", "no", " NO \n", "No\r\n", "yep", "ye", "sure", "y y", "nope",
    ] {
        assert!(!answered(Some(no)), "{no:?}");
    }
}

#[test]
fn a_closed_input_is_a_no_though_a_bare_enter_is_a_yes() {
    assert!(!answered(None));
    assert!(answered(Some("\n")));
}

// A session init launches is the run's own: one the run refuses (a config of a
// newer generation, as the daemon's plan refuses it) is a launch failure with
// the run's exit, 48, never a session that ended; one that ended is its code
// and what it noticed.
#[test]
fn a_session_the_run_refuses_is_a_launch_failure_with_its_exit() {
    let refused = run_application::Done::<run_application::Ended> {
        result: Err(run_application::RunError::Resume(
            sessions::SessionRejection::SessionRunning {
                harp: "amber-quiet-heron".to_string(),
            },
        )),
        notices: Vec::new(),
    };
    let failure = super::ended_of(refused).expect_err("refused");
    assert_eq!(failure.exit, Exit::REFUSED);
    assert_eq!(failure.cause.code(), "sessions-session-running");
    let ended = super::ended_of(run_application::Done {
        result: Ok(run_application::Ended {
            harp: "h".to_string(),
            session: "s".to_string(),
            engine: "claude-code".to_string(),
            code: Some(3),
        }),
        notices: vec![library_application::Notice::text("noticed")],
    })
    .unwrap_or_else(|_| panic!("ended"));
    assert_eq!(ended.code, Some(3));
    assert_eq!(ended.notices.len(), 1);
}
