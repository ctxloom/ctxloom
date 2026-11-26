//! The setup interview's ports (S12), wired: the person's terminal, the
//! interactive sessions `init` launches, the interview's text and the
//! companions' guidance (the daemon's `GetInitPrompt`, read through the
//! project's catalog, owner ruling 128) and a first init's companions
//! registered (the daemon's `CreateCompanion`). Nothing here reads or writes
//! a library in process (step 10c U6, D9).
//!
//! The terminal is the process's own (standard input and output both a
//! terminal). Under `test-support` the variable `CTXLOOM_TEST_TERMINAL`
//! stands one in, so the acceptance tests need no pty; it is compiled out of
//! the shipped binary. Each session is built when it is launched, from the
//! project as it is then, which is what makes the restart see what the
//! interview configured.

use cli_support::ExitClass;
use std::io::{BufRead, IsTerminal, Write};
use std::sync::Arc;

use init_application::{
    Ended, LaunchFailure, PromptFailure, PromptRead, Prompts, Sessions, Terminal,
};
use library_application::{Flags, RunRequest};
use process_env::EnvSnapshot;
use run_application::{OneShot, RunService};
use setup_cli::SetupReading as _;

use crate::oneshot::OneShots;
use crate::Wiring;

/// The person's terminal.
pub struct StdTerminal {
    stood_in: bool,
}

impl StdTerminal {
    pub fn new(env: &EnvSnapshot) -> StdTerminal {
        StdTerminal {
            stood_in: stood_in(env),
        }
    }
}

/// A terminal stood in by the tests' variable: only in a binary built for
/// them.
#[cfg(feature = "test-support")]
pub fn stood_in(env: &EnvSnapshot) -> bool {
    env.non_empty("CTXLOOM_TEST_TERMINAL").is_some()
}

/// The shipped binary has no stand-in.
#[cfg(not(feature = "test-support"))]
pub fn stood_in(_: &EnvSnapshot) -> bool {
    false
}

impl Terminal for StdTerminal {
    // Reads the process's own terminal, which a unit test cannot be given;
    // the acceptance tests stand one in and answer on standard input.
    #[cfg_attr(test, mutants::skip)]
    fn is_terminal(&self) -> bool {
        self.stood_in || (std::io::stdin().is_terminal() && std::io::stdout().is_terminal())
    }

    // Reads the process's own standard input (see above).
    #[cfg_attr(test, mutants::skip)]
    fn confirm(&self, question: &str) -> bool {
        let mut err = std::io::stderr();
        let _ = write!(err, "{question} [Y/n] ");
        let _ = err.flush();
        let mut line = String::new();
        match std::io::stdin().lock().read_line(&mut line) {
            Ok(0) | Err(_) => answered(None),
            Ok(_) => answered(Some(&line)),
        }
    }

    // Reads the process's own standard input (see above).
    #[cfg_attr(test, mutants::skip)]
    fn ask(&self, question: &str, suggested: &str) -> Option<String> {
        let mut err = std::io::stderr();
        let _ = write!(err, "{question} [{suggested}] ");
        let _ = err.flush();
        let mut line = String::new();
        match std::io::stdin().lock().read_line(&mut line) {
            Ok(0) | Err(_) => None,
            Ok(_) => Some(line),
        }
    }
}

/// Whether the answer to a `[Y/n]` question is a yes. `line` is what she
/// typed, none where the input was closed (end of input, or unreadable):
/// that is a no, though a bare Enter is a yes. `y` and `yes`, in any case,
/// are a yes too; `n`, `no` and anything else are a no.
fn answered(line: Option<&str>) -> bool {
    match line.map(|l| l.trim().to_lowercase()) {
        Some(answer) => matches!(answer.as_str(), "" | "y" | "yes"),
        None => false,
    }
}

/// The interactive sessions of `init`: each prepared by the user's daemon
/// and started on this terminal by its frontend (U5, owner s4-Q6), as a
/// `ctxloom run` is.
pub struct SetupSessions<'a> {
    pub env: &'a Arc<EnvSnapshot>,
    pub flags: &'a Flags,
    /// This binary's stamp, which the daemon that prepares the session must
    /// share (M3).
    pub stamp: &'a str,
}

impl SetupSessions<'_> {
    /// One interactive session of the default agent, opened with `prompt`
    /// (none: nothing), over the project as it is now: the daemon plans it
    /// in this process's directory and environment (S7), so it sees what the
    /// interview configured, and pulls its dependencies when `sync.on_run`
    /// asks.
    fn launch(&self, prompt: &str) -> Result<Ended, LaunchFailure> {
        let wiring =
            Wiring::new(Arc::clone(self.env)).map_err(|e| LaunchFailure::failed(Box::new(e)))?;
        let request = OneShot {
            run: RunRequest {
                prompt: prompt.to_string(),
                ..RunRequest::default()
            },
            engine: String::new(),
            session: String::new(),
        };
        let done = OneShots::new(&wiring)
            .stamped(self.stamp)
            .interactive(self.flags, &request);
        ended_of(done)
    }
}

/// What init is told of a session's run: its exit code and notices, or, for
/// a run that refused or failed, a launch failure with the run's own exit
/// (as `ctxloom run` ends with it).
fn ended_of(done: run_application::Done<run_application::Ended>) -> Result<Ended, LaunchFailure> {
    let notices = done
        .notices
        .into_iter()
        .map(|n| Box::new(n) as init_application::Failure)
        .collect();
    let ended = done.result.map_err(|e| {
        let exit = e.exit();
        LaunchFailure {
            cause: Box::new(e),
            exit,
        }
    })?;
    Ok(Ended {
        code: ended.code,
        notices,
    })
}

impl Sessions for SetupSessions<'_> {
    fn interview(&self, opening: &str) -> Result<Ended, LaunchFailure> {
        self.launch(opening)
    }

    fn configured(&self) -> Result<Ended, LaunchFailure> {
        self.launch("")
    }
}

impl SetupSessions<'_> {
    /// `GetInitPrompt` where this process stands: the interview as the
    /// project's catalog holds it now (a project bundle `ctxloom` overrides
    /// the built-in one whole, once the project is trusted), and each
    /// enabled companion's guidance.
    fn read_prompt(&self) -> Result<daemon_proto::v1::GetInitPromptResponse, PromptFailure> {
        crate::library::DaemonServed::new(Arc::clone(self.env), self.stamp.to_string())
            .init_prompt(daemon_proto::v1::GetInitPromptRequest {
                caller: daemon_proto::v1::Caller::of(
                    Some(library_api::reads::globals_of(self.flags)),
                    None,
                ),
                ..daemon_proto::v1::GetInitPromptRequest::default()
            })
            .map_err(|failure| PromptFailure {
                cause: failure.cause,
                exit: failure.exit,
            })
    }
}

impl Prompts for SetupSessions<'_> {
    /// The setup interview, the daemon's read (step 10c U6), without the
    /// line `init prompt` prints after it; what reading it noticed said.
    fn setup_interview(&self) -> Result<PromptRead, PromptFailure> {
        let read = self.read_prompt()?;
        let text = read
            .text
            .strip_suffix('\n')
            .unwrap_or(&read.text)
            .to_string();
        Ok(PromptRead {
            text,
            notices: read
                .notices
                .iter()
                .map(|notice| {
                    Box::new(library_api::reads::reported(notice)) as init_application::Failure
                })
                .collect(),
        })
    }

    /// Each enabled registered companion's setup guidance, as the daemon
    /// reads it (so a first init's sees the companions it just registered).
    fn companion_guidance(&self) -> Vec<String> {
        self.read_prompt()
            .map(|read| read.companion_guidance)
            .unwrap_or_default()
    }
}

#[cfg(test)]
#[path = "interview.test.rs"]
mod tests;
