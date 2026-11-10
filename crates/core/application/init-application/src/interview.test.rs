//! The interview and its restart. Home: `features/journeys/j000200_setup.feature`
//! (scenario 2) and `cli/init.feature` ("on a terminal ... one setup interview").

use std::cell::RefCell;

use super::{interview, Ended, LaunchFailure, Outcome, Sessions, Terminal, RESTART_QUESTION};
use crate::SETUP_INTERVIEW;

struct Person {
    terminal: bool,
    yes: bool,
    asked: RefCell<Vec<String>>,
}

impl Terminal for Person {
    fn is_terminal(&self) -> bool {
        self.terminal
    }

    fn confirm(&self, question: &str) -> bool {
        self.asked.borrow_mut().push(question.to_string());
        self.yes
    }

    fn ask(&self, _: &str, _: &str) -> Option<String> {
        unreachable!("the interview confirms, it asks nothing")
    }
}

/// The sessions, recording what was launched; the interview ends with
/// `interview_code`.
struct Launches {
    interview_code: Option<i32>,
    launched: RefCell<Vec<String>>,
    refuses: bool,
}

impl Sessions for Launches {
    fn interview(&self, opening: &str) -> Result<Ended, LaunchFailure> {
        self.launched
            .borrow_mut()
            .push(format!("interview:{opening}"));
        if self.refuses {
            return Err(LaunchFailure {
                cause: Box::new(deps_application::AppError::DefaultNeedsName),
                exit: 2,
            });
        }
        Ok(Ended {
            code: self.interview_code,
            notices: Vec::new(),
        })
    }

    fn configured(&self) -> Result<Ended, LaunchFailure> {
        self.launched.borrow_mut().push("configured".to_string());
        Ok(Ended {
            code: Some(7),
            notices: Vec::new(),
        })
    }
}

fn person(terminal: bool, yes: bool) -> Person {
    Person {
        terminal,
        yes,
        asked: RefCell::default(),
    }
}

fn launches(interview_code: Option<i32>) -> Launches {
    Launches {
        interview_code,
        launched: RefCell::default(),
        refuses: false,
    }
}

// init.feature: off a terminal init launches nothing and asks nothing (what
// makes the other scenarios hermetic).
#[test]
fn off_a_terminal_nothing_is_launched_or_asked() {
    let (who, sessions) = (person(false, true), launches(Some(0)));
    let done = interview(&who, &sessions).expect("no failure");
    assert_eq!(done.outcome, Outcome::NotATerminal);
    assert!(sessions.launched.borrow().is_empty());
    assert!(who.asked.borrow().is_empty());
}

// j000200 scenario 2: the interview opens with the setup interview's text,
// then the offer is made, then her yes restarts her into the configured one.
#[test]
fn the_interview_opens_with_the_setup_text_and_a_yes_restarts() {
    let (who, sessions) = (person(true, true), launches(Some(0)));
    let done = interview(&who, &sessions).expect("no failure");
    assert_eq!(done.outcome, Outcome::Restarted { code: Some(7) });
    assert_eq!(
        *sessions.launched.borrow(),
        vec![
            format!("interview:{SETUP_INTERVIEW}"),
            "configured".to_string()
        ]
    );
    assert_eq!(*who.asked.borrow(), vec![RESTART_QUESTION.to_string()]);
}

// A no (a bare Enter, a closed input) leaves her where she is: one session.
#[test]
fn a_no_launches_no_second_session() {
    let (who, sessions) = (person(true, false), launches(Some(0)));
    let done = interview(&who, &sessions).expect("no failure");
    assert_eq!(done.outcome, Outcome::Declined);
    assert_eq!(sessions.launched.borrow().len(), 1);
}

// An interview that did not end cleanly (she was killed, the engine failed)
// has configured an unknown amount: no offer is made.
#[test]
fn an_interview_that_did_not_end_cleanly_offers_no_restart() {
    for code in [Some(1), None] {
        let (who, sessions) = (person(true, true), launches(code));
        let done = interview(&who, &sessions).expect("no failure");
        assert_eq!(done.outcome, Outcome::Interrupted { code });
        assert!(who.asked.borrow().is_empty(), "{code:?}");
        assert_eq!(sessions.launched.borrow().len(), 1);
    }
}

// An interview that could not start is the failure, and nothing is asked.
#[test]
fn an_interview_that_cannot_start_fails() {
    let who = person(true, true);
    let sessions = Launches {
        refuses: true,
        ..launches(Some(0))
    };
    let failure = interview(&who, &sessions).expect_err("cannot start");
    assert_eq!(failure.exit, 2, "the launch's own code is kept");
    assert!(who.asked.borrow().is_empty());
}
