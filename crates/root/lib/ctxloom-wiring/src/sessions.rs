//! The `session` and `hook` commands mounted on the command line: the
//! sessions use cases over their default adapters, and the engines'
//! callbacks, run as the extension's nouns.

use std::io::Write;
use std::sync::Arc;

use angzarr_diagnostic::Diagnostic;
use angzarr_presentation::{Catalog, Format};
use clap::{ArgMatches, Command, FromArgMatches};
use cli_support::say_warning;
use ctxloom_cli::{shaped, Call, CliError, Failure, PROG};
use hook_cli::Binding;
use sessions_application::{BindRequest, SessionService};

use crate::{Commands, DirIds, HookRecording, HookSink, Sessions};

/// The nouns these commands own.
pub(crate) const NOUNS: [&str; 2] = ["session", "hook"];

/// The environment variable naming the session the hooks run in.
const SESSION_HARP: &str = run_ports::HARP_VARIABLE;

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

/// A failure of the command line itself: exit 1.
fn failed(cause: Box<dyn Diagnostic>) -> Failure {
    Failure { cause, exit: 1 }
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

    /// `session ...`: the command run on the sessions use cases, its notices
    /// and dead letters said beside its answer.
    pub(crate) fn session(
        &mut self,
        call: &Call<'_>,
        catalog: &Catalog,
        out: &mut dyn Write,
        err: &mut dyn Write,
    ) -> Result<u8, Failure> {
        let command = session_cli::Session::from_arg_matches(matches_of(call, "session"))
            .unwrap_or_else(|e| unreachable!("clap parsed it: {e}"));
        let format = call.format();
        let service = self.sessions().map_err(failed)?;
        match session_cli::run(&command, service, format, out) {
            Ok(done) => {
                for line in &done.lines {
                    let _ = writeln!(err, "{PROG}: {line}");
                }
                for notice in &done.notices {
                    say_warning(&**notice, PROG, format, catalog, err);
                }
                Ok(0)
            }
            Err(failure) => {
                for line in &failure.lines {
                    let _ = writeln!(err, "{PROG}: {line}");
                }
                Err(Failure {
                    cause: failure.failure.cause,
                    exit: failure.failure.exit,
                })
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
    ) -> Result<u8, Failure> {
        let hook = hook_cli::Hook::from_arg_matches(matches_of(call, "hook"))
            .unwrap_or_else(|e| unreachable!("clap parsed it: {e}"));
        let format: Format = call.format();
        let harp = self.env.utf8(SESSION_HARP).unwrap_or_default().to_string();
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
            let wiring = crate::Wiring::new(Arc::clone(&self.env)).ok();
            let dirs = wiring
                .as_ref()
                .map(|w| session_files::SessionDirs::new(w.fs(), w.project_dir()));
            // A hook is a process of its own: it delivers to the run's
            // listener, named by its environment. With none to reach it fails
            // open (stderr says each observation was not recorded).
            let sink = HookSink::from_environment(
                self.env.utf8(run_ports::ENDPOINT_VARIABLE),
                self.env.utf8(run_ports::BEARER_VARIABLE),
            );
            let recording = HookRecording::new(sink, DirIds::new(dirs));
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
        .map_err(failed)?;
        if let Some((harp, native_id, transcript)) = asked.0 {
            Bind(self)
                .bind(&harp, &native_id, &transcript)
                .map_err(failed)?;
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
                1,
            )
        })?;
        Ok(0)
    }
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
