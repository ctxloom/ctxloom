//! `ctxloom shutdown [<when>]` (owner, 2026-11-15: one ctxloom version runs
//! at a time, and an installer asks every running ctxloom of this user to
//! stop before it replaces the binary): the daemon and the live
//! interactive frontends, which outlive their daemon (U5's adoption).
//!
//! `<when>` is a span of the one grammar (rust-support's `duration-text`: `15m`,
//! `1h30m`, `now`); none given, the home config's `daemon.drain_timeout`,
//! else [`daemon_application::HAND_OVER_PERIOD`]. A span that is not `now`
//! DRAINS the daemon within it (`daemon stop --drain`'s request, its
//! timeout this one), TELLS each live frontend at once that it will be
//! stopped then (its notice, which it shows its human: owner, 2026-11-15,
//! OD-63), and waits for the daemon to exit and the frontends to
//! end, at most that long (the daemon then a grace longer, as its period's
//! end stops what it still hosts, unless a live frontend keeps it: a
//! draining daemon stays while one remains, so its last reports reach it).
//! Then, `now` at once: each live frontend
//! is asked to stop (its stop file, [`liveness::FrontendLocks`]; it stops
//! its engine as a SIGTERM would, ruling 114, and reports its end), waited
//! for, and the daemon serving then is asked the plain stop
//! (`--even-if-live`: its engines stopped, ruling 114), waited for. Nothing
//! is killed. What still runs is said, and the command fails (10).
//!
//! Flush before install (owner ruling, 2026-11-16): a hold an older binary
//! wrote carries type URLs a newer daemon may not know, so the home's holds
//! are delivered through the daemon serving (none is ever started for it)
//! at the start, once the frontends have ended, and before the plain stop;
//! then the holds are judged. They are EMPTY when no hold has a live record
//! (a fact or a gap a flush would deliver, or a hold in a format this
//! ctxloom cannot read); a hold's set-aside part is never delivered again,
//! so it holds nothing a daemon will decode, and does not count. A hold
//! left undelivered is named, by session and count, and declines the
//! shutdown (20, `shutdown-held`: the state forbids reporting done), which
//! outranks what still runs (10); the installers refuse to replace the
//! binary on a decline.

use std::io::Write;
use std::path::Path;
use std::sync::Arc;
use std::time::{Duration, Instant};

use clap::{Arg as ClapArg, Command};
use cli_support::Exit;
use coded_diagnostic::{code, Arg, Diagnostic};
use ctxloom_cli::{Call, Failure};
use file_system::Fs;
use liveness::FrontendLocks;
use presentation::{render, Format, Render};
use serde::Serialize;

use run_ports::{HeldFacts, HoldError};
use runs_application::Undelivered;

use super::{
    block_on, called, client_reaching, files_of, flush, output, own_stamp, serving, serving_here,
    Reach,
};
use crate::Wiring;

/// The noun.
pub(crate) const NOUN: &str = "shutdown";

/// How much longer than its drain's timeout the daemon is waited for: its
/// period's end stops each one-shot (a five-second grace, then its end
/// recorded) and cuts each busy child before it exits.
const DRAIN_GRACE: Duration = Duration::from_secs(30);

/// How long the frontends asked to stop are waited for: the TUI's stop
/// grace (`Terminal::STOP_GRACE`), then its exit reported.
const FRONTENDS_WAIT: Duration = Duration::from_secs(20);

/// How long the daemon asked the plain stop is waited for.
const STOP_WAIT: Duration = Duration::from_secs(30);

/// How long one flush of the home's holds may take, in all: longer than a
/// command's on its way in ([`flush::FLUSH_BOUND`]), as a shutdown is no
/// command anyone waits on to type the next.
const FLUSH_WAIT: Duration = Duration::from_secs(30);

/// How often what still runs is looked at.
const LOOK: Duration = Duration::from_millis(100);

/// What a live frontend's human is told when a drain starts (owner,
/// 2026-11-15, OD-63; a DRAFT wording): it will be stopped once `left` has
/// passed.
pub(crate) fn notice_text(left: Duration) -> String {
    format!(
        "ctxloom is shutting down: this session will be stopped in {}; finish and exit",
        duration_text::format_exact(left)
    )
}

/// Seconds since the epoch, on this host's clock.
pub(crate) fn epoch_seconds() -> u64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map_or(0, |since| since.as_secs())
}

/// The noun as the command line has it.
pub(crate) fn noun() -> Command {
    Command::new(NOUN)
        .about("Stop every running ctxloom of this user, gracefully within a time, then what remains")
        .arg(
            ClapArg::new("when")
                .value_name("DURATION")
                .help("How long the drain waits (15m, 1h30m, now); default the config's daemon.drain_timeout, else 5m"),
        )
}

/// Why the shutdown is refused, or did not stop everything.
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) enum ShutdownError {
    /// `<when>` is no span.
    BadWhen { value: String, cause: String },
    /// What still runs once everything was asked to stop and waited for.
    Incomplete { daemon: String, frontends: String },
    /// What the home's holds still hold that no daemon took (owner,
    /// 2026-11-16, "flush before install"): `holds`, each session's said
    /// with its count.
    Held { holds: String },
}

impl std::fmt::Display for ShutdownError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        coded_diagnostic::display(self, f)
    }
}

impl std::error::Error for ShutdownError {}

impl Diagnostic for ShutdownError {
    fn code(&self) -> &str {
        match self {
            ShutdownError::BadWhen { .. } => code!("shutdown-bad-time"),
            ShutdownError::Incomplete { .. } => code!("shutdown-incomplete"),
            ShutdownError::Held { .. } => code!("shutdown-held"),
        }
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        match self {
            ShutdownError::BadWhen { value, cause } => vec![
                ("value", value.as_str().into()),
                ("ext_err_txt", cause.as_str().into()),
            ],
            ShutdownError::Incomplete { daemon, frontends } => vec![
                ("daemon", daemon.as_str().into()),
                ("frontends", frontends.as_str().into()),
            ],
            ShutdownError::Held { holds } => vec![("holds", holds.as_str().into())],
        }
    }
}

/// One value of each, for the catalog's test.
#[cfg(any(test, feature = "test-support"))]
pub(crate) fn samples() -> Vec<Box<dyn Diagnostic>> {
    vec![
        Box::new(ShutdownError::BadWhen {
            value: "soon".to_string(),
            cause: "not a span".to_string(),
        }),
        Box::new(ShutdownError::Incomplete {
            daemon: "pid 4242".to_string(),
            frontends: "pid 77".to_string(),
        }),
        Box::new(ShutdownError::Held {
            holds: "amber-swift-owl (3 facts)".to_string(),
        }),
    ]
}

/// The home config's `daemon.drain_timeout` (owner, 2026-11-15), read
/// afresh, as a command reads the home layer: none when it is not said, or
/// the config does not read.
pub(crate) fn configured_drain_timeout(fs: &Arc<dyn Fs>, home: &Path) -> Option<Duration> {
    let app = config_files::AppDir {
        path: home.join(".ctxloom"),
        source: ctxloom_ports::Source::Home,
        ignored_root: None,
    };
    let files = config_files::ConfigFilesFs::new(Arc::clone(fs), app, home);
    library_application::load_config(
        &files,
        &[],
        &library_application::Flags::default(),
        &mut Vec::new(),
    )
    .ok()?
    .drain_timeout()
}

/// The holds of `held` that still hold a live record (owner, 2026-11-16:
/// shutdown flushes the holds, or confirms they are empty): a fact or a
/// gap no daemon took, or a hold this ctxloom cannot read. A set-aside part
/// is never delivered again, so it is no record a daemon will decode: it
/// is not counted ([`runs_application::undelivered`]).
pub(crate) fn undelivered_in<H: HeldFacts + ?Sized>(
    held: &H,
) -> Result<Vec<Undelivered>, HoldError> {
    Ok(runs_application::undelivered(&held.survey()?))
}

/// The holds `left` names, as shutdown says them: each session's harp
/// (else its session root) and what it holds, in the survey's order.
pub(crate) fn holds_said(left: &[Undelivered]) -> String {
    left.iter()
        .map(|hold| {
            let who = flush::harp_or_root(hold.session.as_deref(), &hold.root);
            if hold.unreadable {
                return format!("{who} (in a format this ctxloom cannot read)");
            }
            let mut what = Vec::new();
            if hold.facts > 0 {
                what.push(counted(hold.facts, "fact"));
            }
            if hold.gaps > 0 {
                what.push(counted(hold.gaps, "gap"));
            }
            format!("{who} ({})", what.join(", "))
        })
        .collect::<Vec<_>>()
        .join("; ")
}

/// `count` of `what`, plural when it is not one.
fn counted(count: u64, what: &str) -> String {
    match count {
        1 => format!("1 {what}"),
        n => format!("{n} {what}s"),
    }
}

/// A hold left undelivered, in the json report's `held`.
#[derive(Debug, Serialize)]
struct HeldSaid {
    session: String,
    facts: u64,
    gaps: u64,
    unreadable: bool,
}

impl HeldSaid {
    fn of(hold: &Undelivered) -> HeldSaid {
        HeldSaid {
            session: flush::harp_or_root(hold.session.as_deref(), &hold.root),
            facts: hold.facts,
            gaps: hold.gaps,
            unreadable: hold.unreadable,
        }
    }
}

/// The command's verdict on what it `left` running and what the home's
/// holds still hold (`held`): done (0) when nothing runs and nothing is
/// held; a hold left undelivered is a DECLINE (20, the state forbids
/// reporting done, so an installer does not replace the binary: owner,
/// 2026-11-16), and it outranks what still runs (10), as the more severe
/// of two outcomes wins.
fn verdict(left: &Running, held: &[Undelivered]) -> Result<Exit, Failure> {
    if !held.is_empty() {
        return Err(Failure::new(
            ShutdownError::Held {
                holds: holds_said(held),
            },
            Exit::REFUSED,
        ));
    }
    if left.nothing() {
        return Ok(Exit::OK);
    }
    Err(Failure::new(
        ShutdownError::Incomplete {
            daemon: left
                .daemon
                .map_or("none".to_string(), |pid| format!("pid {pid}")),
            frontends: match left.frontends.as_slice() {
                [] => "none".to_string(),
                pids => pids_of(pids),
            },
        },
        Exit::FAILED,
    ))
}

/// What the shutdown did, step by step, and what it left running.
#[derive(Debug, Default, Serialize)]
struct Report {
    /// The drain's timeout in seconds; 0 for `now`.
    drain_seconds: u64,
    /// Each step, as it was said.
    steps: Vec<String>,
    /// The holds still holding a live record once everything stopped.
    held: Vec<HeldSaid>,
}

impl Render for Report {
    fn json(&self) -> impl Serialize + '_ {
        self
    }

    fn text(&self, out: &mut dyn Write) -> std::io::Result<()> {
        self.steps
            .iter()
            .try_for_each(|step| writeln!(out, "{step}"))
    }
}

/// What runs now: the daemon serving (its pid, what it hosts) and the
/// live frontends' process ids.
struct Running {
    daemon: Option<u32>,
    frontends: Vec<u32>,
}

impl Running {
    fn nothing(&self) -> bool {
        self.daemon.is_none() && self.frontends.is_empty()
    }

    fn said(&self) -> String {
        let daemon = self.daemon.map_or("no daemon".to_string(), |pid| {
            format!("the daemon (pid {pid})")
        });
        match self.frontends.as_slice() {
            [] => format!("{daemon}, no frontend"),
            pids => format!("{daemon}, frontends {}", pids_of(pids)),
        }
    }
}

fn pids_of(pids: &[u32]) -> String {
    pids.iter()
        .map(|pid| format!("pid {pid}"))
        .collect::<Vec<_>>()
        .join(", ")
}

/// Run `ctxloom shutdown`.
pub(crate) fn run(
    wiring: &Wiring,
    stamp: &str,
    call: &Call<'_>,
    out: &mut dyn Write,
) -> Result<Exit, Failure> {
    let when = call
        .matches
        .subcommand_matches(NOUN)
        .and_then(|matches| matches.get_one::<String>("when").cloned());
    let period = match &when {
        Some(text) => duration_text::parse(text).map_err(|cause| {
            Failure::new(
                ShutdownError::BadWhen {
                    value: text.clone(),
                    cause: cause.to_string(),
                },
                Exit::REFUSED,
            )
        })?,
        None => configured_drain_timeout(&wiring.fs(), wiring.home())
            .unwrap_or(daemon_application::HAND_OVER_PERIOD),
    };
    let format = call.format();
    let mut report = Report {
        drain_seconds: period.as_secs(),
        steps: Vec::new(),
        held: Vec::new(),
    };
    let mut say = |line: String| {
        if format == Format::Text {
            let _ = writeln!(out, "{line}");
        }
        report.steps.push(line);
    };
    let shut = Shutdown { wiring, stamp };
    let left = shut.down(period, &mut say)?;
    // Owner, 2026-11-16 ("flush before install"): done only once the
    // holds are empty, what a flush delivered gone and nothing live left.
    let spool = fact_spool::Spool::at_home(wiring.fs(), wiring.home());
    let held = undelivered_in(&spool).map_err(|e| Failure::new(e, Exit::FAILED))?;
    if held.is_empty() {
        say("held: nothing undelivered".to_string());
    } else {
        say(format!("held, undelivered: {}", holds_said(&held)));
    }
    report.held = held.iter().map(HeldSaid::of).collect();
    if format == Format::Json {
        output(render(out, format, &report))?;
    }
    verdict(&left, &held)
}

/// The shutdown of `wiring`'s home, as the client of stamp `stamp`.
struct Shutdown<'a> {
    wiring: &'a Wiring,
    stamp: &'a str,
}

impl Shutdown<'_> {
    fn frontends(&self) -> FrontendLocks {
        FrontendLocks::new(self.wiring.fs(), self.wiring.home())
    }

    /// What runs now.
    fn running(&self) -> Result<Running, Failure> {
        let files = files_of(self.wiring);
        Ok(Running {
            daemon: serving(self.wiring, &files)?.map(|found| found.pid),
            frontends: self.frontends().live(),
        })
    }

    /// Look at what runs until `done` says it is enough, or `within` has
    /// passed: what runs then.
    fn wait(&self, within: Duration, done: impl Fn(&Running) -> bool) -> Result<Running, Failure> {
        let deadline = Instant::now() + within;
        loop {
            let running = self.running()?;
            if done(&running) || Instant::now() >= deadline {
                return Ok(running);
            }
            std::thread::sleep(LOOK);
        }
    }

    /// How long the daemon asked the plain stop is waited for:
    /// [`STOP_WAIT`], or, under test-support, the daemon directory's
    /// `test-shutdown-stop-wait` (seconds), so a test of a daemon that will
    /// not stop waits less.
    fn stop_wait(&self) -> Duration {
        #[cfg(feature = "test-support")]
        if let Some(wait) = files_of(self.wiring).shutdown_stop_wait() {
            return wait;
        }
        STOP_WAIT
    }

    /// Deliver the home's holds to the daemon serving now, if one does
    /// (none is started: owner, 2026-11-16), within [`FLUSH_WAIT`]; what
    /// it delivered is said, and what it set aside is said as the command
    /// ends (H3's notice).
    fn flush(&self, say: &mut dyn FnMut(String)) {
        let Ok(Some(found)) = serving_here(self.wiring, &files_of(self.wiring)) else {
            return;
        };
        let own = own_stamp(self.wiring.env(), self.stamp);
        let flushed = flush::flush_within(self.wiring, own, &found, FLUSH_WAIT);
        match &flushed {
            flush::Flushed::Done(done) => {
                let (mut delivered, mut sessions) = (0, 0);
                for session in done {
                    let went = session.flushed.len() - session.set_aside().len();
                    if went > 0 {
                        delivered += went;
                        sessions += 1;
                    }
                }
                if delivered > 0 {
                    say(format!(
                        "delivered {} of {} to the daemon (pid {})",
                        counted(delivered as u64, "held fact"),
                        counted(sessions, "session"),
                        found.pid
                    ));
                }
            }
            flush::Flushed::Older { daemon } => say(format!(
                "the daemon is ctxloom {daemon}, newer than this one: what is held is left for it"
            )),
            flush::Flushed::Failed(e) => say(format!("the holds could not be read: {e}")),
            flush::Flushed::NotServing => {}
        }
        flush::to_say(flush::notices(&flushed));
    }

    /// A client of the daemon serving, starting none.
    fn client(&self) -> daemon_api::Client<Reach<'_>> {
        client_reaching(self.wiring, Reach::new(self.wiring, self.stamp).observing())
    }

    /// Drain within `period` (unless it is none), then stop what remains;
    /// what still runs after.
    fn down(&self, period: Duration, say: &mut dyn FnMut(String)) -> Result<Running, Failure> {
        let at_first = self.running()?;
        if at_first.nothing() {
            say("nothing of ctxloom is running".to_string());
            return Ok(at_first);
        }
        say(format!("running: {}", at_first.said()));
        self.flush(say);
        if !period.is_zero() {
            self.tell_frontends(&at_first.frontends, period, say);
            if at_first.daemon.is_some() {
                let drained = block_on(self.client().drain(Some(period)))?.map_err(called)?;
                let hosts: Vec<String> = drained
                    .iter()
                    .flat_map(|daemon| daemon.runs.iter().map(|run| run.harp.clone()))
                    .collect();
                say(format!(
                    "asked the daemon to drain within {}; it hosts: {}",
                    duration_text::format_exact(period),
                    if hosts.is_empty() {
                        "nothing".to_string()
                    } else {
                        hosts.join(", ")
                    }
                ));
            }
            // The frontends' last reports reach the daemon, or its holds,
            // as they end; those are delivered while it still serves.
            let deadline = Instant::now() + period;
            let ended = self.wait(period, |running| running.frontends.is_empty())?;
            if ended.frontends.is_empty() && !at_first.frontends.is_empty() {
                self.flush(say);
            }
            let after = self.wait(
                deadline.saturating_duration_since(Instant::now()),
                Running::nothing,
            )?;
            if after.nothing() {
                say("drained: everything ended".to_string());
                return Ok(after);
            }
            // The daemon's own period ends with it: it stops what it still
            // hosts, then exits, a grace later. A live frontend keeps it
            // (owner ruling, 2026-11-16: its last reports are the daemon's),
            // so the frontends are asked to stop now, while it serves.
            let after = match (after.daemon, after.frontends.is_empty()) {
                (Some(_), true) => self.wait(DRAIN_GRACE, |running| running.daemon.is_none())?,
                _ => after,
            };
            say(format!(
                "after the drain's {}, still running: {}",
                duration_text::format_exact(period),
                after.said()
            ));
        }
        self.stop_what_remains(say)
    }

    /// Tell each of `frontends` it will be stopped once `period` has passed
    /// (its notice: no `stop_at`, as this shutdown asks it then).
    fn tell_frontends(&self, frontends: &[u32], period: Duration, say: &mut dyn FnMut(String)) {
        let locks = self.frontends();
        let notice = liveness::FrontendNotice {
            text: notice_text(period),
            stop_at: None,
        };
        for pid in frontends {
            match locks.tell(*pid, &notice) {
                Ok(()) => say(format!("told the frontend pid {pid}: {}", notice.text)),
                Err(e) => say(format!("cannot tell the frontend pid {pid}: {e}")),
            }
        }
    }

    /// Ask each live frontend to stop, then the daemon serving the plain
    /// stop, waiting for each; what still runs after.
    fn stop_what_remains(&self, say: &mut dyn FnMut(String)) -> Result<Running, Failure> {
        let frontends = self.frontends();
        let live = frontends.live();
        for pid in &live {
            match frontends.ask_to_stop(*pid) {
                Ok(()) => say(format!("asked the frontend pid {pid} to stop")),
                Err(e) => say(format!("cannot ask the frontend pid {pid} to stop: {e}")),
            }
        }
        if !live.is_empty() {
            let after = self.wait(FRONTENDS_WAIT, |running| running.frontends.is_empty())?;
            if !after.frontends.is_empty() {
                say(format!(
                    "after {}, frontends still running: {}",
                    duration_text::format_exact(FRONTENDS_WAIT),
                    pids_of(&after.frontends)
                ));
            }
        }
        // What the frontends held as they ended, delivered before the stop.
        self.flush(say);
        if let Some(stopped) = block_on(self.client().stop(true))?.map_err(called)? {
            say(format!(
                "asked the daemon (pid {}) to stop even if live",
                stopped.pid
            ));
            let within = self.stop_wait();
            let after = self.wait(within, |running| running.daemon.is_none())?;
            if after.daemon.is_some() {
                say(format!(
                    "after {}, the daemon still serves",
                    duration_text::format_exact(within)
                ));
            }
        }
        let left = self.running()?;
        if left.nothing() {
            say("stopped: nothing of ctxloom is running".to_string());
        } else {
            say(format!("still running: {}", left.said()));
        }
        Ok(left)
    }
}

#[cfg(test)]
#[path = "shutdown.test.rs"]
mod tests;
