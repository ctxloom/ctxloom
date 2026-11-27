//! The `daemon` noun's verbs: `serve`, `status`, `stop` and `resolve`, the CLI's
//! words for what the rest of this module does (finding, reaching and asking
//! the daemon). The only part of `daemon/` that names a frontend crate.

use std::io::Write;
use std::sync::Arc;

use clap::{Command, FromArgMatches, Subcommand};
use cli_support::{Exit, ExitClass};
use ctxloom_cli::{shaped, Call, CliError, Failure};
use presentation::{render, Catalog, Render};
use process_env::EnvSnapshot;
use serde::Serialize;

use std::time::Duration;

use ctxloom_compose::{LibError, Wiring};

use super::*;

#[derive(Subcommand, Debug)]
enum DaemonVerb {
    /// Be the user's daemon
    Serve {
        /// Exit after this many seconds with nothing hosted and nothing to do
        #[arg(long, default_value_t = 600)]
        idle_seconds: u64,
        /// How often to look at the stores, in milliseconds
        #[arg(long, default_value_t = 100)]
        poll_ms: u64,
        /// How long a drain waits for a busy child, in seconds, before it
        /// hands it over anyway, its turn cut
        #[arg(long, default_value_t = daemon_application::HAND_OVER_PERIOD.as_secs())]
        hand_over_seconds: u64,
    },
    /// Say whether a daemon serves, and where
    Status,
    /// Ask the daemon to drain and exit
    Stop {
        /// Stop it even while it hosts live runs, ending them first
        #[arg(long, conflicts_with = "drain")]
        even_if_live: bool,
        /// Hand what it hosts over to the next daemon, which resumes each
        /// child in its own conversation, then exit
        #[arg(long)]
        drain: bool,
    },
    /// Register this project with the daemon, or recheck it
    Resolve,
}

/// The noun, hidden from the help.
pub(crate) fn noun() -> Command {
    shaped(DaemonVerb::augment_subcommands(
        Command::new(NOUN)
            .about("The user's daemon")
            .hide(true)
            .subcommand_required(true),
    ))
}

/// The one json schema of `daemon status` and `daemon stop` (owner, dogfood
/// 2026-11-21): `{"daemons": [...]}`, each the `Daemon`'s proto3 JSON
/// (ruling 146), the list empty when none served; the text carries the
/// message. A count is the list's length: there is no second shape.
#[derive(Serialize)]
struct DaemonList<'a> {
    daemons: Vec<&'a daemon_proto::v1::Daemon>,
}

impl<'a> DaemonList<'a> {
    fn of(daemon: Option<&'a daemon_proto::v1::Daemon>) -> Self {
        Self {
            daemons: daemon.into_iter().collect(),
        }
    }
}

/// What `status` answers: the daemon as it says itself (`GetDaemon`), or
/// none serving. Its json is [`DaemonList`].
struct Status(Option<daemon_proto::v1::Daemon>, Vec<String>);

impl Render for Status {
    fn json(&self) -> impl Serialize + '_ {
        DaemonList::of(self.0.as_ref())
    }

    fn text(&self, out: &mut dyn Write) -> std::io::Result<()> {
        let Some(daemon) = &self.0 else {
            return writeln!(out, "no ctxloom daemon is serving");
        };
        writeln!(
            out,
            "serving at {} (pid {}), ctxloom {}",
            daemon.endpoint, daemon.pid, daemon.version
        )?;
        writeln!(
            out,
            "idle {} of {}",
            duration_text::format_exact(Duration::from_secs(daemon.idle_seconds)),
            duration_text::format_exact(Duration::from_secs(daemon.idle_limit_seconds))
        )?;
        if daemon.draining {
            writeln!(
                out,
                "draining: handing what it hosts over to the next daemon"
            )?;
        }
        // What it warns of (OD-52), in this command's catalog's words.
        for warning in &self.1 {
            writeln!(out, "{warning}")?;
        }
        for run in &daemon.runs {
            writeln!(out, "hosting {} ({})", run.harp, run.session)?;
        }
        Ok(())
    }
}

/// What `stop` answers (smoke 2026-11-19): the daemon it stopped, as the
/// stop's answer says it, or none serving; whether it drained first. Its
/// json is [`DaemonList`].
struct Stopped(Option<daemon_proto::v1::Daemon>, bool);

impl Render for Stopped {
    fn json(&self) -> impl Serialize + '_ {
        DaemonList::of(self.0.as_ref())
    }

    // DRAFT text (smoke 2026-11-19): `daemon stop` said nothing.
    fn text(&self, out: &mut dyn Write) -> std::io::Result<()> {
        let Some(daemon) = &self.0 else {
            return writeln!(out, "no ctxloom daemon was serving: nothing to stop");
        };
        let done = if self.1 {
            "drained and stopped"
        } else {
            "stopped"
        };
        writeln!(
            out,
            "{done} the ctxloom daemon (pid {}), ctxloom {}",
            daemon.pid, daemon.version
        )
    }
}

/// `warnings`, as `daemon status` says them in text: each its message in
/// `catalog`'s words, then its fix where the catalog has one.
fn warned(warnings: &[daemon_proto::v1::Notice], catalog: &Catalog) -> Vec<String> {
    warnings
        .iter()
        .map(|notice| {
            let warning = run_application::client::report_of(&notice.code, &notice.args);
            let fix =
                run_application::client::report_of(&format!("{}-fix", notice.code), &notice.args);
            match catalog.render(&fix) {
                Ok(fix) => format!("warning: {}\n  fix: {fix}", catalog.message(&warning)),
                Err(_) => format!("warning: {}", catalog.message(&warning)),
            }
        })
        .collect()
}

fn output(written: std::io::Result<()>) -> Result<(), Failure> {
    written.map_err(|e| {
        failed(CliError::Output {
            detail: e.to_string(),
        })
    })
}

/// Run the `daemon` noun's verb.
pub(crate) fn run(
    serve: Serve,
    env: &Arc<EnvSnapshot>,
    stamp: &str,
    call: &Call<'_>,
    catalog: &Catalog,
    out: &mut dyn Write,
) -> Result<Exit, Failure> {
    let matches = call
        .matches
        .subcommand_matches(NOUN)
        .unwrap_or_else(|| unreachable!("the line named {NOUN}"));
    let verb = DaemonVerb::from_arg_matches(matches)
        .unwrap_or_else(|_| unreachable!("the command line parsed {NOUN}"));
    let format = call.format();
    // `daemon serve` is the daemon composition's, handed in by the binary
    // (`Commands::with_serve`); every other verb is a client.
    if let DaemonVerb::Serve {
        idle_seconds,
        poll_ms,
        hand_over_seconds,
    } = verb
    {
        let pace = Pace {
            idle: Duration::from_secs(idle_seconds),
            poll: Duration::from_millis(poll_ms.max(1)),
            hand_over: Duration::from_secs(hand_over_seconds),
        };
        return serve(env, stamp, pace);
    }
    let wiring = Wiring::new(Arc::clone(env)).map_err(failed)?;
    match verb {
        DaemonVerb::Serve { .. } => unreachable!("served above"),
        // Clients of the daemon (slice 2 step 6), answered here when none
        // serves (S14): neither starts one.
        // They look at the daemon and change nothing: no hold is delivered
        // on their way in (H3).
        DaemonVerb::Status => {
            // It shows the daemon's warnings itself (OD-52): it hears none.
            let reach = Reach::new(&wiring, stamp).observing().showing_warnings();
            let found = block_on(client_reaching(&wiring, reach).status())?.map_err(called)?;
            let warnings = found
                .as_ref()
                .map(|daemon| warned(&daemon.warnings, catalog))
                .unwrap_or_default();
            output(render(out, format, &Status(found, warnings)))?;
            Ok(Exit::OK)
        }
        DaemonVerb::Stop {
            even_if_live,
            drain,
        } => {
            let reach = Reach::new(&wiring, stamp).observing();
            let client = client_reaching(&wiring, reach);
            // Its warnings (a drain's hand-over among them) are heard from
            // the stop's answer, as every call's (OD-55), and said at once.
            let stopped = match drain {
                // OD-54 (3): handed over within the configured timeout.
                true => block_on(client.drain(None))?,
                false => block_on(client.stop(even_if_live))?,
            }
            .map_err(called)?;
            output(render(out, format, &Stopped(stopped, drain)))?;
            Ok(Exit::OK)
        }
        DaemonVerb::Resolve => {
            let project = resolve(&wiring, env, stamp)?;
            output(render(out, format, &Resolved(project)))?;
            Ok(Exit::OK)
        }
    }
}

/// `resolve`: this project registered with the daemon, or rechecked
/// (owner, 2026-11-12), the daemon made sure of first (M3 included).
fn resolve(
    wiring: &Wiring,
    env: &Arc<EnvSnapshot>,
    stamp: &str,
) -> Result<daemon_proto::v1::Project, Failure> {
    let files = files_of(wiring);
    reconciled(
        &files,
        serving(wiring, &files)?.as_ref(),
        own_stamp(env, stamp),
    )?;
    let died = ensure(wiring, env);
    let Some(found) = serving(wiring, &files)? else {
        let detail = match died {
            Some(Died { status, tail }) => format!(
                "no ctxloom daemon could be started: the one started exited ({status}); the end of its log:\n{tail}"
            ),
            None => "no ctxloom daemon could be started".to_string(),
        };
        return Err(failed(LibError::NoRuntime { detail }));
    };
    // It answers the project's name, so it names it (owner ruling
    // 2026-11-14): one nothing has named gets its id at its root.
    let request = wiring
        .naming_registration()
        .map_err(|e| {
            let exit = e.exit();
            Failure::new(e, exit)
        })?
        .unwrap_or_default();
    let caller = caller_of(env, daemon_proto::v1::client::Kind::Cli, stamp);
    block_on(daemon_api_client::resolve(
        &hearing_client_of(&found, caller),
        request,
    ))?
    .map_err(called)
}

/// What `resolve` answers: the project the daemon named.
struct Resolved(daemon_proto::v1::Project);

#[derive(Serialize)]
struct ResolvedJson<'a> {
    name: &'a str,
    root: &'a str,
    uid: &'a str,
    git_common_dir: &'a str,
    root_commit: &'a str,
}

impl Render for Resolved {
    fn json(&self) -> impl Serialize + '_ {
        let p = &self.0;
        ResolvedJson {
            name: &p.name,
            root: &p.root,
            uid: &p.uid,
            git_common_dir: &p.git_common_dir,
            root_commit: &p.root_commit,
        }
    }

    fn text(&self, out: &mut dyn Write) -> std::io::Result<()> {
        writeln!(out, "{} ({})", self.0.name, self.0.root)
    }
}

#[cfg(test)]
#[path = "cli.test.rs"]
mod tests;
