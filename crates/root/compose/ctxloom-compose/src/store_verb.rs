//! The test commands that read or write the store, by name and arguments:
//! what the binary hands the daemon's entry ([`crate::daemon::DaemonEntry`]),
//! below both halves so the CLI's `ctxloom test` noun and the daemon's runs
//! of them name the same types.

use std::io::Write;

use clap::Args;
use cli_support::Failure;

use crate::Wiring;

/// The test commands that read or write the store: the daemon's
/// composition runs them (the CLI's wiring opens no store), handed in by the
/// binary as [`DaemonEntry::store_tests`](crate::daemon::DaemonEntry).
#[derive(Debug)]
pub enum StoreVerb {
    OpenStore {
        project: String,
    },
    SeedSession {
        args: Box<SeedArgs>,
        loadout: Option<sessions::SessionLoadout>,
    },
    SessionName {
        harp: String,
    },
    SessionEvents {
        harp: String,
        run: Option<u32>,
    },
    LibraryEvents {
        home: bool,
    },
    Record,
    SeedRehosts {
        harp: String,
        count: u32,
    },
}

/// Runs a [`StoreVerb`] where `wiring` is, for the binary of stamp `version`,
/// writing its answer to `out`.
pub type StoreTest = fn(&Wiring, &str, StoreVerb, &mut dyn Write) -> Result<(), Failure>;

/// What `ctxloom test seed-session` plants (see the acceptance tests).
#[derive(Args, Debug, Clone, Default)]
pub struct SeedArgs {
    /// The session's name
    pub harp: String,
    /// Another project than the cwd's
    #[arg(long)]
    pub project: Option<String>,
    /// Seconds ago it was minted
    #[arg(long, default_value_t = 86_400)]
    pub started_ago: u64,
    /// Seconds ago its last run ended; left out, it never ended
    #[arg(long)]
    pub ended_ago: Option<u64>,
    /// `free` makes its lock file with nothing holding it; `none` makes none
    #[arg(long, default_value = "none")]
    pub lock: String,
    /// Record the first prompt Go's fixture's transcript holds, as a hook would
    #[arg(long)]
    pub transcript: bool,
    /// Mark its transcript kept
    #[arg(long)]
    pub keep: bool,
    /// An internal one-shot's session
    #[arg(long)]
    pub oneshot: bool,
    /// The engine's session it is bound to
    #[arg(long)]
    pub native_id: Option<String>,
    /// The engine's transcript it is bound to
    #[arg(long)]
    pub native_transcript: Option<String>,
    /// An authored file in the session directory
    #[arg(long)]
    pub other_file: Vec<String>,
    /// Its first incarnation ended, so it can be resumed
    #[arg(long)]
    pub resumable: bool,
    /// The profile its run was planned on, so its loadout names that
    /// profile's bundles
    #[arg(long)]
    pub profile: Option<String>,
}
