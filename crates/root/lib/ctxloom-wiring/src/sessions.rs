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

use crate::capture::DaemonReach;
use crate::{Commands, HoldingRecording, HookRecording, HookSink, RunIds, Sessions, Wiring};

/// The nouns these commands own.
pub(crate) const NOUNS: [&str; 2] = ["session", "hook"];

/// The trust noun (step 8 U1), session-cli's second: a client of the
/// daemon, as the child verbs are.
pub(crate) const TRUST_NOUN: &str = "trust";

/// The top noun (DRAFT): a client of the daemon's `WatchTop`.
pub(crate) const TOP_NOUN: &str = "top";

/// The nouns, shaped like the command line's own.
pub(crate) fn nouns() -> Vec<Command> {
    vec![
        shaped(session_cli::noun()),
        shaped(hook_cli::noun()),
        shaped(session_cli::trust_noun()),
        shaped(session_cli::top_noun()),
    ]
}

/// The person at this process's terminal, asked yes or no (`[y/N]`: only
/// `y` or `yes` is a yes); a terminal stood in under test-support
/// (`CTXLOOM_TEST_TERMINAL`), its answers on standard input.
pub(crate) struct TrustTerminal {
    pub(crate) stood_in: bool,
}

impl session_cli::Asking for TrustTerminal {
    // Reads the process's own terminal, which a unit test cannot be given;
    // the acceptance tests stand one in.
    #[cfg_attr(test, mutants::skip)]
    fn is_terminal(&self) -> bool {
        use std::io::IsTerminal;
        self.stood_in || (std::io::stdin().is_terminal() && std::io::stdout().is_terminal())
    }

    // Reads the process's own standard input (see above).
    #[cfg_attr(test, mutants::skip)]
    fn confirm(&self, question: &str) -> bool {
        use std::io::BufRead;
        let mut err = std::io::stderr();
        let _ = write!(err, "{question} [y/N] ");
        let _ = err.flush();
        let mut line = String::new();
        match std::io::stdin().lock().read_line(&mut line) {
            Ok(0) | Err(_) => false,
            Ok(_) => said_yes(&line),
        }
    }
}

/// An ask's `allow-always` rule written into the config (step 8 U4, Q3),
/// through the library over this project as a command finds it.
struct LibraryRules<'a> {
    wiring: &'a Wiring,
    flags: library_application::Flags,
}

impl session_cli::AlwaysRules for LibraryRules<'_> {
    fn allow_always(
        &self,
        agent: &str,
        engine: &str,
        rule: &str,
        apply: bool,
    ) -> Result<session_cli::AlwaysPlan, session_cli::Failure> {
        let library = crate::library::build(self.wiring).map_err(session_cli::Failure::of)?;
        let written = library
            .allow_always(&self.flags, agent, engine, rule, apply)
            .result
            .map_err(session_cli::Failure::of)?;
        Ok(session_cli::AlwaysPlan {
            path: written.path,
            key: written.key,
            rule: written.rule,
            document: written.document,
        })
    }
}

/// The person at `ctxloom top`'s screen answering an ask: what `session
/// approvals answer` does, an `allow-always`'s rule written first and
/// shown before it is.
struct TopAnswering<'a, A> {
    /// The daemon's caller, made when the first answer is given: `top`
    /// starts no daemon and makes no project, so nothing reaches one before.
    answers: std::cell::OnceCell<Result<A, String>>,
    make: &'a dyn Fn() -> Result<A, String>,
    rules: LibraryRules<'a>,
    catalog: &'a Catalog,
    runtime: tokio::runtime::Runtime,
}

impl<A: daemon_api::ApprovalAnswers> session_cli::TopAnswers for TopAnswering<'_, A> {
    fn preview(
        &self,
        ask: &daemon_proto::v1::Approval,
        option: &str,
    ) -> Result<Option<session_cli::AlwaysPlan>, String> {
        let Some(rule) = ask
            .options
            .iter()
            .find(|o| o.id == option && o.kind == "allow_always" && !o.rule.is_empty())
        else {
            return Ok(None);
        };
        session_cli::AlwaysRules::allow_always(
            &self.rules,
            &ask.agent,
            &ask.engine,
            &rule.rule,
            false,
        )
        .map(Some)
        .map_err(|failure| self.catalog.message(&*failure.failure.cause))
    }

    fn answer(
        &self,
        ask: &daemon_proto::v1::Approval,
        option: &str,
        write_rule: bool,
    ) -> Result<String, String> {
        let ask_id = daemon_api::ask_of(ask).to_string();
        let answers = match self.answers.get_or_init(|| (self.make)()) {
            Ok(answers) => answers,
            Err(why) => return Err(why.clone()),
        };
        // The order of `session approvals answer` (Q3): the ask is checked
        // waiting, the rule written, then the ask answered.
        let mut kept = None;
        if write_rule {
            let current = self
                .runtime
                .block_on(answers.approval(&ask_id))
                .map_err(|error| self.catalog.message(&error))?;
            if let Some(settled) = daemon_api::settled_refusal(&current) {
                return Err(self.catalog.message(&settled));
            }
            let rule = ask
                .options
                .iter()
                .find(|o| o.id == option)
                .map(|o| o.rule.clone())
                .unwrap_or_default();
            kept = Some(
                session_cli::AlwaysRules::allow_always(
                    &self.rules,
                    &ask.agent,
                    &ask.engine,
                    &rule,
                    true,
                )
                .map_err(|failure| self.catalog.message(&*failure.failure.cause))?,
            );
        }
        match self.runtime.block_on(answers.answer(ask, option, "")) {
            Ok(_) => Ok(format!("{ask_id}: answered {option}")),
            Err(error) => Err(match (daemon_api::settled_of(&error), kept) {
                // The rule written for an ask settled meanwhile stays, and is said.
                (Some((ask_id, how)), Some(plan)) => {
                    self.catalog.message(&daemon_api::CallError::Refused(
                        daemon_api::Rejection::Api(daemon_api::ApiRejection::ApprovalRuleKept {
                            ask_id,
                            how,
                            rule: plan.rule,
                            path: plan.path,
                        }),
                    ))
                }
                (Some((ask_id, how)), None) => format!("{ask_id} was already settled, {how}"),
                _ => self.catalog.message(&error),
            }),
        }
    }
}

/// Whether `line`, what she typed at a `[y/N]` question, is a yes: `y` or
/// `yes`, in any case; anything else, a bare Enter too, is a no.
fn said_yes(line: &str) -> bool {
    matches!(line.trim().to_lowercase().as_str(), "y" | "yes")
}

/// The user's login as this process's environment says it (`USER`, else
/// `LOGNAME`, else Windows' `USERNAME`): the actor a trust is recorded with
/// (one principal; nothing stamps yet, D13). None said, empty.
pub(crate) fn user_login(env: &process_env::EnvSnapshot) -> String {
    ["USER", "LOGNAME", "USERNAME"]
        .iter()
        .find_map(|name| {
            env.non_empty(name)
                .map(|v| v.to_string_lossy().into_owned())
        })
        .unwrap_or_default()
}

/// `noun`'s own matches.
fn matches_of<'a>(call: &Call<'a>, noun: &str) -> &'a ArgMatches {
    call.matches
        .subcommand_matches(noun)
        .unwrap_or_else(|| unreachable!("the line named {noun}"))
}

/// A failure of the command line itself: exit 32.
fn failed(cause: Box<dyn Diagnostic>) -> Failure {
    Failure::boxed(cause, Exit::FAILED)
}

/// A hook the service refused (an engine the registry does not know, a
/// payload its codec cannot read): a decline, exit 48 (ruling 152).
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
    /// said, and fails a command that had not (exit 17); beside a failure of
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

    /// `trust ...` (step 8 U1): the user's verdict on this project's own
    /// settings, through her daemon, asked at her terminal.
    pub(crate) fn trust(
        &mut self,
        call: &Call<'_>,
        catalog: &Catalog,
        out: &mut dyn Write,
        err: &mut dyn Write,
    ) -> Result<Exit, Failure> {
        let command = session_cli::Trust::from_arg_matches(matches_of(call, TRUST_NOUN))
            .unwrap_or_else(|e| unreachable!("clap parsed it: {e}"));
        let format = call.format();
        let wiring = Wiring::new(Arc::clone(&self.env)).map_err(|e| failed(Box::new(e)))?;
        let caller = crate::daemon::child_caller(&wiring, &self.version)?;
        let asking = TrustTerminal {
            stood_in: crate::interview::stood_in(&self.env),
        };
        let user = user_login(&self.env);
        let ran = session_cli::trust(&command, &caller, &user, &asking, format, out);
        said(ran, format, catalog, err)
    }

    /// `top ...` (DRAFT): what the daemon hosts, live, through her daemon's
    /// `WatchTop`; it starts no daemon and waits for one.
    pub(crate) fn top(
        &mut self,
        call: &Call<'_>,
        catalog: &Catalog,
        out: &mut dyn Write,
        err: &mut dyn Write,
    ) -> Result<Exit, Failure> {
        let command = session_cli::Top::from_arg_matches(matches_of(call, TOP_NOUN))
            .unwrap_or_else(|e| unreachable!("clap parsed it: {e}"));
        let format = call.format();
        let wiring = Wiring::new(Arc::clone(&self.env)).map_err(|e| failed(Box::new(e)))?;
        let terminal = TrustTerminal {
            stood_in: crate::interview::stood_in(&self.env),
        };
        let source = crate::top::DaemonTop::new(&wiring, &self.version).map_err(failed)?;
        let runtime = tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .build()
            .map_err(|e| {
                failed(Box::new(CliError::Output {
                    detail: e.to_string(),
                }))
            })?;
        let make = || {
            crate::daemon::child_caller(&wiring, &self.version)
                .map_err(|failure| catalog.message(&*failure.cause))
        };
        let answering = TopAnswering {
            answers: std::cell::OnceCell::new(),
            make: &make,
            rules: LibraryRules {
                wiring: &wiring,
                flags: call.globals.clone(),
            },
            catalog,
            runtime,
        };
        // A screen asked for where none can be drawn is refused in the
        // envelope a program reads, whatever format was asked: the format
        // that was asked for is what could not be served.
        let on_terminal = session_cli::Asking::is_terminal(&terminal);
        if let Err(refusal) = session_cli::needs_terminal(format, on_terminal) {
            cli_support::say_failure(PROG, Format::Json, catalog, &refusal, &[], err).map_err(
                |e| {
                    failed(Box::new(CliError::Output {
                        detail: e.to_string(),
                    }))
                },
            )?;
            return Ok(Exit::REFUSED);
        }
        let ran = session_cli::top(
            &command,
            &source,
            &answering,
            format,
            (
                session_cli::Asking::is_terminal(&terminal),
                terminal.stood_in,
                session_cli::Look::of(|name| {
                    self.env
                        .non_empty(name)
                        .map(|value| value.to_string_lossy().into_owned())
                }),
            ),
            out,
        );
        said(ran, format, catalog, err)
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
        } else if session_cli::is_approvals(&command) {
            // The asks are the daemon's (step 8 U4); an allow-always's rule
            // is the config's, written through the library here.
            let wiring = Wiring::new(Arc::clone(&self.env)).map_err(|e| failed(Box::new(e)))?;
            let caller = crate::daemon::child_caller(&wiring, &self.version)?;
            let rules = LibraryRules {
                wiring: &wiring,
                flags: call.globals.clone(),
            };
            let asking = TrustTerminal {
                stood_in: crate::interview::stood_in(&self.env),
            };
            session_cli::approvals(&command, &caller, &rules, &asking, format, out)
        } else if session_cli::is_child(&command) {
            // The child verbs are the user's writes through the daemon's
            // coordinator (slice 4 U3): this process opens no store for them.
            let wiring = Wiring::new(Arc::clone(&self.env)).map_err(|e| failed(Box::new(e)))?;
            let caller = crate::daemon::child_caller(&wiring, &self.version)?;
            session_cli::child(&command, &caller, format, out)
        } else if session_cli::is_destroy(&command) {
            // The destroy is the daemon's (owner, 2026-11-12): this process
            // opens no store for it.
            let wiring = Wiring::new(Arc::clone(&self.env)).map_err(|e| failed(Box::new(e)))?;
            let client = crate::daemon::client(&wiring, &self.version);
            // The hold is this process's (s4-Q14, H4; H-Q5 revised): the
            // destroy's warning and discard read this home's.
            let holds =
                reclaim_application::Holds(fact_spool::Spool::at_home(wiring.fs(), wiring.home()));
            // At her terminal the removal's confirmation is asked there
            // (step 10c U11).
            let asking = TrustTerminal {
                stood_in: crate::interview::stood_in(&self.env),
            };
            destroy(&command, &client, (&holds, &asking), format, out)
        } else {
            let service = self.sessions().map_err(failed)?;
            session_cli::run(&command, service, format, out)
        };
        said(ran, format, catalog, err)
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
        // The hook's bound (S16) runs from its start.
        let started = std::time::Instant::now();
        let hook = hook_cli::Hook::from_arg_matches(matches_of(call, "hook"))
            .unwrap_or_else(|e| unreachable!("clap parsed it: {e}"));
        let format: Format = call.format();
        // The session the hooks run in, by its resource name (s3-Q3).
        let harp = crate::capture::harp_of(self.env.utf8(run_ports::SESSION_VARIABLE));
        let hook_cli::HookVerb::Execute { hook_name } = &hook.verb;
        // The service the answer's ack goes through, once it is printed.
        let mut acknowledging: Option<crate::commands::Hooks<'_>> = None;
        let outcome = if hook_name == hook_cli::STATUSLINE {
            // Rendering only: it needs neither the application nor a home.
            hook_cli::execute(&hook.engine, hook_name, input, &harp, &NoService)
        } else {
            // A hook is a process of its own. One whose environment names an
            // endpoint (M10b's container arrival, OD-13) delivers there alone,
            // never spawning a daemon and never holding. Any other delivers to
            // the user's daemon, rediscovered at each hook (B1, D27) and
            // started if none serves, within its bound (S16), or holds its
            // facts in its session's hold (s4-Q14, H2).
            let endpoint = self
                .env
                .utf8(run_ports::ENDPOINT_VARIABLE)
                .filter(|e| !e.is_empty());
            // The run names its session, by id, in the engine's environment.
            let ids = RunIds::from_environment(self.env.utf8(run_ports::SESSION_VARIABLE));
            let recording: Box<dyn hook_cli::Recording> = match endpoint {
                Some(endpoint) => Box::new(HookRecording::new(
                    HookSink::from_environment(
                        Some(endpoint),
                        self.env.utf8(run_ports::BEARER_VARIABLE),
                        self.env.utf8(run_ports::TLS_CERT_VARIABLE),
                    ),
                    ids,
                )),
                None => {
                    let wiring =
                        Wiring::new(Arc::clone(&self.env)).map_err(|e| failed(Box::new(e)))?;
                    Box::new(HoldingRecording::new(
                        DaemonReach(Arc::clone(&self.env)),
                        fact_spool::Spool::at_home(wiring.fs(), wiring.home()),
                        ids,
                        crate::daemon::own_stamp(&self.env, &self.version),
                        self.env
                            .utf8(run_ports::SESSION_VARIABLE)
                            .unwrap_or_default(),
                        started,
                    ))
                }
            };
            let setup = acknowledging.insert(self.hooks().map_err(|e| failed(Box::new(e)))?);
            hook_cli::execute_recording(&hook.engine, hook_name, input, &harp, &*setup, &*recording)
        }
        .map_err(declined)?;
        for warning in &outcome.warnings {
            say_warning(warning, PROG, format, catalog, err);
        }
        // Print, FLUSH, and only then acknowledge what was offered
        // (`hook_cli::deliver`): a hook that dies before the ack leaves its
        // messages pending, to be offered again (at-least-once, owner
        // 2026-11-19). An ack that was not recorded is a repeat later.
        let written = match &acknowledging {
            Some(service) => hook_cli::deliver(&outcome, out, service).map(|_| ()),
            None => out.write_all(outcome.stdout.as_bytes()),
        };
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

/// What a `session` or `trust` command came to, said: its dead letters and
/// notices on `err`, and its exit (a preview that would change something,
/// 17 under `--detailed-exitcode`).
fn said(
    ran: Result<session_cli::Done, session_cli::Failure>,
    format: Format,
    catalog: &Catalog,
    err: &mut dyn Write,
) -> Result<Exit, Failure> {
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

/// `session remove|sweep` on the daemon's client: a `session-destroy`
/// build's alone.
#[cfg(feature = "session-destroy")]
fn destroy<R: daemon_api::SessionRemovals>(
    command: &session_cli::Session,
    client: &R,
    (holds, asking): (
        &dyn reclaim_application::SessionHolds,
        &dyn session_cli::Asking,
    ),
    format: Format,
    out: &mut dyn Write,
) -> Result<session_cli::Done, session_cli::Failure> {
    session_cli::destroy(command, client, holds, asking, format, out)
}

/// A plain build has no destroying verb, so nothing is a destroy.
#[cfg(not(feature = "session-destroy"))]
fn destroy<R>(
    _: &session_cli::Session,
    _: &R,
    _: (
        &dyn reclaim_application::SessionHolds,
        &dyn session_cli::Asking,
    ),
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

#[cfg(test)]
#[path = "sessions.test.rs"]
mod tests;
