//! The setup interview's ports (S12), wired: the person's terminal and the
//! interactive sessions `init` launches.
//!
//! The terminal is the process's own (standard input and output both a
//! terminal). Under `test-support` the variable `CTXLOOM_TEST_TERMINAL`
//! stands one in, so the acceptance tests need no pty; it is compiled out of
//! the shipped binary. Each session is built when it is launched, from the
//! project as it is then, which is what makes the restart see what the
//! interview configured.

use std::io::{BufRead, IsTerminal, Write};
use std::rc::Rc;
use std::sync::Arc;

use deps_application::Deps;
use init_application::{Ended, LaunchFailure, Sessions, Terminal};
use library_application::{Flags, RunRequest};
use process_env::EnvSnapshot;
use run_application::{OneShot, RunService};

use crate::oneshot::OneShots;
use crate::Wiring;

/// The person's terminal.
pub(crate) struct StdTerminal {
    stood_in: bool,
}

impl StdTerminal {
    pub(crate) fn new(env: &EnvSnapshot) -> StdTerminal {
        StdTerminal {
            stood_in: stood_in(env),
        }
    }
}

/// A terminal stood in by the tests' variable: only in a binary built for
/// them.
#[cfg(feature = "test-support")]
fn stood_in(env: &EnvSnapshot) -> bool {
    env.non_empty("CTXLOOM_TEST_TERMINAL").is_some()
}

/// The shipped binary has no stand-in.
#[cfg(not(feature = "test-support"))]
fn stood_in(_: &EnvSnapshot) -> bool {
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

/// The interactive sessions of `init`.
pub(crate) struct SetupSessions<'a> {
    pub(crate) env: &'a Arc<EnvSnapshot>,
    pub(crate) deps: &'a Rc<Deps>,
    pub(crate) flags: &'a Flags,
}

impl SetupSessions<'_> {
    /// One interactive session of the default agent, opened with `prompt`
    /// (none: nothing), over the project as it is now.
    fn launch(&self, prompt: &str) -> Result<Ended, LaunchFailure> {
        let wiring =
            Wiring::new(Arc::clone(self.env)).map_err(|e| LaunchFailure::failed(Box::new(e)))?;
        let remotes: Rc<dyn deps_ports::RemoteLibrary> = self.deps.clone();
        let library = crate::library::build(&wiring, Some(remotes))
            .map_err(|e| LaunchFailure::failed(Box::new(e)))?;
        let mut sessions = OneShots::new(&wiring, library)
            .with_puller(Rc::new(crate::commands::DepsPuller(Rc::clone(self.deps))));
        let request = OneShot {
            run: RunRequest {
                prompt: prompt.to_string(),
                ..RunRequest::default()
            },
            engine: String::new(),
        };
        let done = sessions.interactive(self.flags, &request);
        let notices = done
            .notices
            .into_iter()
            .map(|n| Box::new(n) as init_application::Failure)
            .collect();
        // The launch's own exit code, as `ctxloom run` ends with it.
        let ended = done.result.map_err(|e| {
            let exit = e.exit_code();
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
}

impl Sessions for SetupSessions<'_> {
    fn interview(&self, opening: &str) -> Result<Ended, LaunchFailure> {
        self.launch(opening)
    }

    fn configured(&self) -> Result<Ended, LaunchFailure> {
        self.launch("")
    }
}

#[cfg(test)]
#[path = "interview.test.rs"]
mod tests;
