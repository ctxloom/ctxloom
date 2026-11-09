//! The setup interview on a terminal: the mechanism only (the interview's
//! words are [`crate::SETUP_INTERVIEW`], the owner's, used as they are).
//!
//! On a terminal, `init` hands the project to the engine once, as an
//! interactive session whose opening prompt is the interview. That session
//! was made before the interview configured anything, so it cannot see what
//! it has just set up: when it ends cleanly, `init` offers to restart the
//! human into a session on the configured project; the offer is `[Y/n]`, so
//! a bare Enter accepts it and a closed input declines it. Whether there is a
//! terminal, the answer she types, and the sessions themselves are ports: nothing here reads the process.

use ctxloom_ports::Failure;

use crate::SETUP_INTERVIEW;

/// The command that starts the configured session later.
pub const RESTART_COMMAND: &str = "ctxloom run";

/// What is asked before the restart.
pub const RESTART_QUESTION: &str =
    "The setup session could not see what it just configured. Restart into your configured session?";

/// What `init` needs of the person's terminal.
pub trait Terminal {
    /// Whether a person is there to answer: standard input and output are a
    /// terminal. Off one, `init` interviews nobody and asks nothing.
    fn is_terminal(&self) -> bool;

    /// Ask a yes/no `question`, offered as `[Y/n]`: a bare Enter is a yes, and
    /// so are `y` and `yes`; `n`, `no` and anything else are a no. A closed
    /// input (end of input, no terminal to read) is a no: nobody answered.
    fn confirm(&self, question: &str) -> bool;
}

/// How a session the human was given ended.
#[derive(Debug, Default)]
pub struct Ended {
    /// The engine's exit code; none where a signal ended it.
    pub code: Option<i32>,
    /// What the run noticed, for the error stream.
    pub notices: Vec<Failure>,
}

impl Ended {
    /// A clean end: the engine exited 0.
    fn clean(&self) -> bool {
        self.code == Some(0)
    }
}

/// The interactive sessions `init` launches, each on the project as it is
/// when the session starts.
pub trait Sessions {
    /// The interview: a session of the default agent opened with `opening`.
    fn interview(&self, opening: &str) -> Result<Ended, Failure>;

    /// The configured session: the same as `ctxloom run`, with no prompt.
    fn configured(&self) -> Result<Ended, Failure>;
}

/// What the interview came to.
#[derive(Debug, PartialEq, Eq)]
pub enum Outcome {
    /// There was no terminal: nothing was launched, nothing asked.
    NotATerminal,
    /// The interview did not end cleanly (the code is the engine's): no
    /// restart is offered, because what it configured is unknown.
    Interrupted { code: Option<i32> },
    /// She did not ask for the restart.
    Declined,
    /// She restarted into the configured session, which ended with `code`.
    Restarted { code: Option<i32> },
}

/// The interview and its restart.
#[derive(Debug)]
pub struct Interviewed {
    pub outcome: Outcome,
    /// What the sessions noticed, in order.
    pub notices: Vec<Failure>,
}

/// Interview her if there is a terminal, then offer the restart.
pub fn interview(terminal: &dyn Terminal, sessions: &dyn Sessions) -> Result<Interviewed, Failure> {
    if !terminal.is_terminal() {
        return Ok(Interviewed {
            outcome: Outcome::NotATerminal,
            notices: Vec::new(),
        });
    }
    let mut ended = sessions.interview(SETUP_INTERVIEW)?;
    let mut notices = std::mem::take(&mut ended.notices);
    let outcome = if !ended.clean() {
        Outcome::Interrupted { code: ended.code }
    } else if !terminal.confirm(RESTART_QUESTION) {
        Outcome::Declined
    } else {
        let mut restarted = sessions.configured()?;
        notices.append(&mut restarted.notices);
        Outcome::Restarted {
            code: restarted.code,
        }
    };
    Ok(Interviewed { outcome, notices })
}

#[cfg(test)]
#[path = "interview.test.rs"]
mod tests;
