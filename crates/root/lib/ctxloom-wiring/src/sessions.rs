//! The `session` and `hook` commands mounted on the command line: the
//! sessions use cases over their default adapters, and the engines'
//! callbacks, run as the extension's nouns.

use cli_support::Exit;
use std::io::Write;
use std::sync::Arc;

use angzarr_diagnostic::Diagnostic;
use angzarr_presentation::{Catalog, Format};
use clap::{ArgMatches, Command, FromArgMatches};
use cli_support::say_warning;
use ctxloom_cli::{shaped, Call, CliError, Failure, PROG};
use hook_cli::Binding;
use sessions_application::{BindRequest, SessionService};

use crate::{Commands, HookRecording, HookSink, RunIds, Sessions, Wiring};

/// The nouns these commands own.
pub(crate) const NOUNS: [&str; 2] = ["session", "hook"];

/// The two nouns, shaped like the command line's own.
pub(crate) fn nouns() -> Vec<Command> {
    vec![shaped(session_cli::noun()), shaped(hook_cli::noun())]
}

/// `noun`'s own matches.
fn matches_of<'a>(call: &Call<'a>, noun: &str) -> &'a ArgMatches {
    call.matches
        .subcommand_matches(noun)
        .unwrap_or_else(|| unreachable!("the line named {noun}"))
}

/// A failure of the command line itself: exit 10.
fn failed(cause: Box<dyn Diagnostic>) -> Failure {
    Failure::boxed(cause, Exit::FAILED)
}

/// A hook the service refused (an engine the registry does not know, a
/// payload its codec cannot read): a decline, exit 20 (ruling 152).
fn declined(cause: Box<dyn Diagnostic>) -> Failure {
    Failure::boxed(cause, Exit::REFUSED)
}

impl Commands {
    /// The sessions use cases, built when a command first needs them: a
    /// command that needs none (the statusline hook) works where there is no
    /// home.
    fn sessions(&mut self) -> Result<&mut Sessions, Box<dyn Diagnostic>> {
        if self.sessions.is_none() {
            let wiring = crate::Wiring::new(Arc::clone(&self.env)).map_err(|e| Box::new(e) as _)?;
            self.sessions = Some(wiring.sessions());
        }
        match self.sessions.as_mut() {
            Some(sessions) => Ok(sessions),
            None => unreachable!("built just above"),
        }
    }

    /// The sessions use cases let go, their stores closed and waited for
    /// (ruling 131) before the process exits: dropped, a store's close is
    /// left to the driver's thread, which the exit need not let run, and the
    /// store's log would stay beside it. None built, nothing to close.
    fn close_sessions(&mut self) -> Result<(), Box<dyn Diagnostic>> {
        let Some(mut sessions) = self.sessions.take() else {
            return Ok(());
        };
        let runtime = tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .build()
            .map_err(|e| -> Box<dyn Diagnostic> {
                Box::new(CliError::Output {
                    detail: e.to_string(),
                })
            })?;
        runtime
            .block_on(sessions.close())
            .map_err(|e| -> Box<dyn Diagnostic> { Box::new(e) })
    }

    /// `answer`, once the sessions' stores are closed: a close that fails is
    /// said, and fails a command that had not (exit 1); beside a failure of
    /// the command's own, which is answered, it is said as a warning.
    fn closing(
        &mut self,
        answer: Result<Exit, Failure>,
        format: Format,
        catalog: &Catalog,
        err: &mut dyn Write,
    ) -> Result<Exit, Failure> {
        match (self.close_sessions(), answer) {
            (Ok(()), answer) => answer,
            (Err(unclosed), Ok(_)) => Err(failed(unclosed)),
            (Err(unclosed), Err(failure)) => {
                say_warning(&*unclosed, PROG, format, catalog, err);
                Err(failure)
            }
        }
    }

    /// `session ...`: the command run on the sessions use cases, its notices
    /// and dead letters said beside its answer.
    pub(crate) fn session(
        &mut self,
        call: &Call<'_>,
        catalog: &Catalog,
        out: &mut dyn Write,
        err: &mut dyn Write,
    ) -> Result<Exit, Failure> {
        let answer = self.session_on_use_cases(call, catalog, out, err);
        self.closing(answer, call.format(), catalog, err)
    }

    /// `session ...` on the sessions use cases, which [`Commands::session`]
    /// closes after.
    fn session_on_use_cases(
        &mut self,
        call: &Call<'_>,
        catalog: &Catalog,
        out: &mut dyn Write,
        err: &mut dyn Write,
    ) -> Result<Exit, Failure> {
        let command = session_cli::Session::from_arg_matches(matches_of(call, "session"))
            .unwrap_or_else(|e| unreachable!("clap parsed it: {e}"));
        let format = call.format();
        // The reads and the destroy are the daemon's (slice 2 step 6): this
        // process opens no store for them.
        let ran = if session_cli::is_read(&command) {
            let wiring = Wiring::new(Arc::clone(&self.env)).map_err(|e| failed(Box::new(e)))?;
            let client = crate::daemon::client(&wiring, &self.version);
            session_cli::read(&command, &client, format, out)
        } else if session_cli::is_destroy(&command) {
            // The destroy is the daemon's (owner, 2026-11-12): this process
            // opens no store for it.
            let wiring = Wiring::new(Arc::clone(&self.env)).map_err(|e| failed(Box::new(e)))?;
            let client = crate::daemon::client(&wiring, &self.version);
            destroy(&command, &client, format, out)
        } else {
            let service = self.sessions().map_err(failed)?;
            session_cli::run(&command, service, format, out)
        };
        match ran {
            Ok(done) => {
                for line in &done.lines {
                    let _ = writeln!(err, "{PROG}: {line}");
                }
                for notice in &done.notices {
                    say_warning(&**notice, PROG, format, catalog, err);
                }
                Ok(if done.would_change {
                    Exit::WOULD_CHANGE
                } else {
                    Exit::OK
                })
            }
            Err(failure) => {
                for line in &failure.lines {
                    let _ = writeln!(err, "{PROG}: {line}");
                }
                Err(Failure::boxed(failure.failure.cause, failure.failure.exit))
            }
        }
    }

    /// `hook ...`: an engine's callback. Stdout is the engine's input and
    /// only that; what a hook can foresee going wrong it warns of on the
    /// diagnostic channel and still succeeds. `input` is its payload.
    pub(crate) fn hook(
        &mut self,
        input: &str,
        call: &Call<'_>,
        catalog: &Catalog,
        out: &mut dyn Write,
        err: &mut dyn Write,
    ) -> Result<Exit, Failure> {
        let hook = hook_cli::Hook::from_arg_matches(matches_of(call, "hook"))
            .unwrap_or_else(|e| unreachable!("clap parsed it: {e}"));
        let format: Format = call.format();
        // The session the hooks run in, by its resource name (s3-Q3).
        let harp = crate::capture::harp_of(self.env.utf8(run_ports::SESSION_VARIABLE));
        let hook_cli::HookVerb::Execute { hook_name } = &hook.verb;
        // The bind is asked for while the setup use cases are borrowed, and
        // made once they are let go (both live on this command's state).
        let mut asked = Asked::default();
        let outcome = if hook_name == hook_cli::STATUSLINE {
            // Rendering only: it needs neither the application nor a home.
            hook_cli::execute(
                &hook.engine,
                hook_name,
                input,
                &harp,
                &NoService,
                &mut asked,
            )
        } else {
            // A hook is a process of its own: it delivers to the run's
            // listener, named by its environment. With none to reach it fails
            // open (stderr says each observation was not recorded).
            let spawned = HookSink::from_environment(
                self.env.utf8(run_ports::ENDPOINT_VARIABLE),
                self.env.utf8(run_ports::BEARER_VARIABLE),
                self.env.utf8(run_ports::TLS_CERT_VARIABLE),
            );
            // The user daemon, rediscovered at each hook (B1, D27), before
            // the endpoint the run was spawned with: its discovery file
            // names the user bearer the hook presents there.
            let sink = HookSink::rediscovered(crate::daemon::discovery(&self.env), spawned);
            // The run names its session, by id, in the engine's environment.
            let ids = RunIds::from_environment(self.env.utf8(run_ports::SESSION_VARIABLE));
            let recording = HookRecording::new(sink, ids);
            let setup = self.hooks().map_err(|e| failed(Box::new(e)))?;
            hook_cli::execute_recording(
                &hook.engine,
                hook_name,
                input,
                &harp,
                &setup,
                &mut asked,
                &recording,
            )
        }
        .map_err(declined)?;
        if let Some((harp, native_id, transcript)) = asked.0 {
            let bound = Bind(self)
                .bind(&harp, &native_id, &transcript)
                .map_err(failed)
                .map(|()| Exit::OK);
            // The bind's store closed before the hook answers (ruling 131).
            self.closing(bound, format, catalog, err)?;
        }
        for warning in &outcome.warnings {
            say_warning(warning, PROG, format, catalog, err);
        }
        let written = out.write_all(outcome.stdout.as_bytes());
        written.map_err(|e| {
            Failure::new(
                CliError::Output {
                    detail: e.to_string(),
                },
                Exit::FAILED,
            )
        })?;
        Ok(Exit::OK)
    }
}

/// `session remove|sweep` on the daemon's client: a `session-destroy`
/// build's alone.
#[cfg(feature = "session-destroy")]
fn destroy<R: daemon_api::SessionRemovals>(
    command: &session_cli::Session,
    client: &R,
    format: Format,
    out: &mut dyn Write,
) -> Result<session_cli::Done, session_cli::Failure> {
    session_cli::destroy(command, client, format, out)
}

/// A plain build has no destroying verb, so nothing is a destroy.
#[cfg(not(feature = "session-destroy"))]
fn destroy<R>(
    _: &session_cli::Session,
    _: &R,
    _: Format,
    _: &mut dyn Write,
) -> Result<session_cli::Done, session_cli::Failure> {
    unreachable!("a plain build has no destroying verb")
}

/// The hook service of a callback that never asks the application.
struct NoService;

impl hook_cli::HookService for NoService {
    // Unreachable by design (the body is `unreachable!`): no test can call it.
    #[cfg_attr(test, mutants::skip)]
    fn execute(
        &self,
        _: &str,
        _: &str,
        _: &str,
        _: &str,
    ) -> Result<setup_application::HookAnswer, Box<dyn Diagnostic>> {
        unreachable!("the statusline renders in hook-cli and asks nothing")
    }
}

/// The bind a callback asked for: harp, the engine's session, its transcript.
#[derive(Default)]
struct Asked(Option<(String, String, String)>);

impl hook_cli::Binding for Asked {
    fn bind(
        &mut self,
        harp: &str,
        native_id: &str,
        transcript: &str,
    ) -> Result<(), Box<dyn Diagnostic>> {
        self.0 = Some((harp.into(), native_id.into(), transcript.into()));
        Ok(())
    }
}

/// What binds a session for the hook: the sessions use case, blocked on.
struct Bind<'a>(&'a mut Commands);

impl hook_cli::Binding for Bind<'_> {
    fn bind(
        &mut self,
        harp: &str,
        native_id: &str,
        transcript_path: &str,
    ) -> Result<(), Box<dyn Diagnostic>> {
        let service = self.0.sessions()?;
        let runtime = tokio::runtime::Builder::new_current_thread()
            .enable_time()
            .build()
            .map_err(|e| -> Box<dyn Diagnostic> {
                Box::new(CliError::Output {
                    detail: e.to_string(),
                })
            })?;
        runtime
            .block_on(service.bind(BindRequest {
                project: None,
                harp: harp.to_string(),
                native_id: native_id.to_string(),
                transcript_path: transcript_path.to_string(),
            }))
            .map(drop)
            .map_err(|e| -> Box<dyn Diagnostic> { Box::new(e) })
    }
}

#[cfg(test)]
#[path = "sessions.test.rs"]
mod tests;
