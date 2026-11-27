//! The hidden test-only commands (`ctxloom test ...`), mounted on the command
//! line as an [`Extension`](ctxloom_cli::Extension) noun beside the others,
//! so the command line's `Services` asks nothing of a build that has them and
//! a build without them alike. They exist only under `test-support`: the
//! exit ladder, the `--format` matrix, the error envelope, every diagnostic,
//! the id mints, the store open and the seed, reached through the binary
//! because no real command does them.

use cli_support::Exit;
use std::io::Write;

use clap::{Command, FromArgMatches, Subcommand};
use coded_diagnostic::Diagnostic;
use ctxloom_cli::{shaped, Call, CliError, Failure};
use presentation::{render, write_json, Catalog, Render};
use serde::Serialize;

use ctxloom_compose::{LibError, SeedArgs, StoreTest, StoreVerb, TestError, Wiring};

/// The noun these commands own.
pub const NOUN: &str = "test";

/// One value of every diagnostic the binary can raise besides the wiring's
/// and the command line's own: what the composition root hands in.
pub type Samples = fn() -> Vec<Box<dyn Diagnostic>>;

#[derive(Subcommand, Debug)]
enum TestVerb {
    /// Refuse (exit 48)
    Refuse,
    /// Report fatal findings (exit 49)
    Findings,
    /// Print a sample list
    Sample {
        /// Print the empty list
        #[arg(long)]
        empty: bool,
    },
    /// Print every diagnostic the binary can raise, as envelopes
    Diagnostics,
    /// Mint a session id
    SessionId,
    /// Print a run's id
    RunId {
        /// The session's id (a UUID)
        session: String,
        /// The incarnation's number
        incarnation: u64,
    },
    /// Open a project's store under the home
    OpenStore {
        /// The project's id
        project: String,
    },
    /// Plant a session in the cwd's project store, and the files a fixture
    /// writes beside it; print its stable id
    SeedSession(SeedArgs),
    /// Print where the library's resource names are rooted: the cwd's
    /// project as the daemon names it (`projects/<harp>`), or `users/me`
    ProjectName,
    /// Print the resource name of the cwd's project's session, the project
    /// named in process, as a seed names it (no daemon reached)
    SessionName {
        /// The session's harp
        harp: String,
    },
    /// Print the events of the cwd's project's session, as json: each one's
    /// type, and a message's id and text
    SessionEvents {
        /// The session's harp
        harp: String,
        /// The events of its run of this incarnation instead
        #[arg(long)]
        run: Option<u32>,
    },
    /// Print the events of the cwd's checkout's library stream (or, with
    /// `--home`, the home's), as json: each event's type, cause, commits,
    /// motion and changes
    LibraryEvents {
        /// The home's library stream instead
        #[arg(long)]
        home: bool,
    },
    /// Watch the cwd's project's library (or, with --home, the home's)
    /// through the daemon's WatchLibrary, printing each change as json,
    /// until --count changes have been printed
    WatchLibrary {
        /// The home's library instead
        #[arg(long)]
        home: bool,
        /// Stop once this many changes have been printed
        #[arg(long)]
        count: Option<u32>,
    },
    /// Print every stream of the one store, as json: each event's type,
    /// its payload's bytes, and what the record's readers ask of it
    Record,
    /// Plant a child's past re-hosts in the one store, each recorded over
    /// the last minutes, its last host lost again
    SeedRehosts {
        /// The session's harp
        harp: String,
        /// How many re-hosts
        count: u32,
    },
    /// Take the exclusive lock at a path through the platform's file lock
    /// (as the daemon takes a lifeline), print `held`, and keep it until
    /// killed (at most ten minutes)
    HoldLock {
        /// The lock file's absolute path
        path: std::path::PathBuf,
    },
}

/// How long `test hold-lock` waits for its lock, and how long it holds it.
const HOLD_WAIT: std::time::Duration = std::time::Duration::from_secs(10);
const HOLD_FOR: std::time::Duration = std::time::Duration::from_secs(600);

/// The noun, shaped like the command line's own and hidden from its help.
pub fn noun() -> Command {
    shaped(TestVerb::augment_subcommands(
        Command::new(NOUN)
            .about("Commands for the tests only")
            .hide(true)
            .subcommand_required(true),
    ))
}

#[derive(Serialize)]
struct Row {
    name: &'static str,
    count: u64,
}

/// A list of rows: the sample document.
struct Sample(Vec<Row>);

impl Render for Sample {
    fn json(&self) -> impl Serialize + '_ {
        &self.0
    }

    fn text(&self, out: &mut dyn Write) -> std::io::Result<()> {
        if self.0.is_empty() {
            return writeln!(out, "(none)");
        }
        for row in &self.0 {
            writeln!(out, "{}  {}", row.name, row.count)?;
        }
        Ok(())
    }
}

/// A failure of the command line itself: exit 32.
fn failed(cause: impl Diagnostic + 'static) -> Failure {
    Failure::new(cause, Exit::FAILED)
}

fn output(written: std::io::Result<()>) -> Result<(), Failure> {
    written.map_err(|e| {
        failed(CliError::Output {
            detail: e.to_string(),
        })
    })
}

/// `verb` run where the home is, by the daemon's composition.
fn stored(
    wiring: impl FnOnce() -> Result<Wiring, LibError>,
    store_tests: StoreTest,
    version: &str,
    verb: StoreVerb,
    out: &mut dyn Write,
) -> Result<(), Failure> {
    let wiring = wiring().map_err(failed)?;
    store_tests(&wiring, version, verb, out)
}

/// Run the `test` noun's verb.
pub fn run(
    wiring: impl FnOnce() -> Result<Wiring, LibError>,
    version: &str,
    (samples, store_tests): (Samples, StoreTest),
    call: &Call<'_>,
    catalog: &Catalog,
    out: &mut dyn Write,
) -> Result<Exit, Failure> {
    let matches = call
        .matches
        .subcommand_matches(NOUN)
        .unwrap_or_else(|| unreachable!("the line named {NOUN}"));
    let verb = TestVerb::from_arg_matches(matches)
        .unwrap_or_else(|_| unreachable!("the command line parsed {NOUN}"));
    let format = call.format();
    match verb {
        TestVerb::Refuse => Err(Failure::new(
            TestError::Refused {
                verb: "refuse".to_string(),
            },
            Exit::REFUSED,
        )),
        TestVerb::Findings => Err(Failure::new(
            TestError::Findings { count: 1 },
            Exit::FATAL_FINDINGS,
        )),
        TestVerb::Sample { empty } => output(render(out, format, &Sample(sample(empty)))),
        TestVerb::Diagnostics => output(diagnostics(samples, catalog, out)),
        TestVerb::SessionId => output(writeln!(out, "{}", crate::session_name())),
        TestVerb::RunId {
            session,
            incarnation,
        } => {
            let id = crate::run_id(&session, incarnation).map_err(failed)?;
            output(writeln!(out, "{id}"))
        }
        TestVerb::SeedSession(seed) => {
            let wiring = wiring().map_err(failed)?;
            // On a profile (step 10c U13): its loadout, as the daemon
            // assembles the profile, as a launch on it would be planned.
            let loadout = match &seed.profile {
                Some(profile) => Some(crate::daemon::profile_loadout(&wiring, version, profile)?),
                None => None,
            };
            store_tests(
                &wiring,
                version,
                StoreVerb::SeedSession {
                    args: Box::new(seed),
                    loadout,
                },
                out,
            )?;
            Ok(())
        }
        TestVerb::ProjectName => {
            let wiring = wiring().map_err(failed)?;
            let parent = crate::daemon::library_parent(&wiring, version, crate::Minting::AtRoot)?;
            output(writeln!(out, "{parent}"))
        }
        TestVerb::SessionName { harp } => stored(
            wiring,
            store_tests,
            version,
            StoreVerb::SessionName { harp },
            out,
        ),
        TestVerb::SessionEvents { harp, run } => stored(
            wiring,
            store_tests,
            version,
            StoreVerb::SessionEvents { harp, run },
            out,
        ),
        TestVerb::LibraryEvents { home } => stored(
            wiring,
            store_tests,
            version,
            StoreVerb::LibraryEvents { home },
            out,
        ),
        TestVerb::WatchLibrary { home, count } => {
            let wiring = wiring().map_err(failed)?;
            crate::daemon::watch::watch_library(&wiring, version, home, count, out)
        }
        TestVerb::Record => stored(wiring, store_tests, version, StoreVerb::Record, out),
        TestVerb::SeedRehosts { harp, count } => stored(
            wiring,
            store_tests,
            version,
            StoreVerb::SeedRehosts { harp, count },
            out,
        ),
        TestVerb::HoldLock { path } => {
            // The platform's lock, not `flock(1)`, which macOS has not
            // (CI, 2026-11-19).
            let fs = wiring().map_err(failed)?.fs();
            let held = file_lock::lock_within(&*fs, &path, HOLD_WAIT).map_err(failed)?;
            output(writeln!(out, "held").and_then(|()| out.flush()))?;
            std::thread::sleep(HOLD_FOR);
            drop(held);
            Ok(())
        }
        TestVerb::OpenStore { project } => stored(
            wiring,
            store_tests,
            version,
            StoreVerb::OpenStore { project },
            out,
        ),
    }
    .map(|()| Exit::OK)
}

fn sample(empty: bool) -> Vec<Row> {
    if empty {
        return Vec::new();
    }
    vec![
        Row {
            name: "alpha",
            count: 1,
        },
        Row {
            name: "beta",
            count: 2,
        },
    ]
}

/// Every diagnostic's envelope, as one json array.
fn diagnostics(samples: Samples, catalog: &Catalog, out: &mut dyn Write) -> std::io::Result<()> {
    let mut all = ctxloom_cli::diagnostic_samples();
    all.extend(crate::diagnostic_samples());
    all.extend(engine_claude::diagnostic_samples());
    all.extend(engine_opencode::diagnostic_samples());
    all.extend(crate::test_error::diagnostic_samples());
    all.extend(samples());
    let envelopes: Vec<_> = all.iter().map(|d| catalog.envelope(&**d)).collect();
    write_json(out, &envelopes)
}

#[cfg(test)]
#[path = "test_commands.test.rs"]
mod tests;
