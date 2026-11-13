//! The `session` commands (the driving adapter): arguments parsed into use
//! cases, answers rendered as json or text. It sees only the application
//! layer ([`SessionService`]).
//!
//! ```text
//! ctxloom session                          (bare: lists)
//! ctxloom session list [--all] [--state live|ended|unknown]
//! ctxloom session search <word>... [--all] [--state ...]
//! ctxloom session show <harp>
//! ctxloom session rename <harp> <title>
//! ctxloom session keep <harp> [--undo]
//! ctxloom session remove <harp> [--yes] [--even-if-live]  (session-destroy)
//! ctxloom session sweep [--purge-older-than <age>] [--yes] (session-destroy)
//! ctxloom session transcript               (bare: lists)
//! ctxloom session transcript list [<harp>] [--all]
//! ctxloom session transcript show <harp>
//! ctxloom session worktrees                (bare: lists)
//! ctxloom session worktrees list [<harp>]
//! ctxloom session worktrees purge <harp> [--yes]
//! ```
//!
//! `session remove` DESTROYS a session (owner ruling 98): a plain build has
//! no deleting verb, and `session purge` is gone. Go's `session adopt` and
//! `approvals` arrive with S10 and S12; `session watch` waits on the
//! owner's ruling on the watch lines. `rename` sets the title (S55): a
//! harp is permanent, and the harp-changing `edit --name` is gone.
//!
//! `list`, `search` and `show` are clients of the user's daemon (slice 2
//! step 6, through `daemon-api`'s [`daemon_api::SessionReads`]): their
//! `--format json` is the daemon's answer as its proto3 JSON (ruling 146,
//! [`proto_json`]: a `ListSessionsResponse`, a `Session`); their text is
//! the table and the facts it always was. `remove` and `sweep` are the
//! daemon's too (owner, 2026-11-12, through [`daemon_api::SessionRemovals`]):
//! the daemon destroys, and their json is its `DeleteSessionResponse` and
//! `SweepSessionsResponse`; a refused removal prints the plan its refusal
//! carries.

mod error;
pub mod proto_json;
mod render;
mod run;

use clap::{Args, Command, Subcommand};

#[cfg(any(test, feature = "test-support"))]
pub use error::diagnostic_samples;
pub use error::{CliFault, EN_US};
#[cfg(feature = "session-destroy")]
pub use run::destroy;
pub use run::{is_destroy, is_read, read, run, Done, Failure};

/// The `session` noun, shaped for mounting on the command line.
pub fn noun() -> Command {
    Session::augment_args(Command::new("session"))
        .about("Browse and manage your recorded sessions")
        .after_help("Examples:\n  ctxloom session list\n  ctxloom session search lockfile")
}

/// The `session` group.
#[derive(Args, Debug)]
pub struct Session {
    #[command(subcommand)]
    pub verb: Option<SessionVerb>,
}

#[derive(Subcommand, Debug)]
pub enum SessionVerb {
    /// List recorded sessions (default: current project; --all for everything)
    #[command(
        after_help = "Examples:\n  ctxloom session list\n  ctxloom session list --all\n  ctxloom session list --state live"
    )]
    List(ListArgs),
    /// Search sessions by name, title and times (default: current project; --all for everything)
    #[command(
        after_help = "Examples:\n  ctxloom session search lockfile migration\n  ctxloom session search --state ended lockfile"
    )]
    Search(SearchArgs),
    /// Show a named session: its title, state, engine and times
    #[command(after_help = "Examples:\n  ctxloom session show amber-swift-owl")]
    Show(HarpArgs),
    /// Set a recorded session's title; its name never changes
    #[command(
        after_help = "Examples:\n  ctxloom session rename amber-swift-owl \"the auth refactor\""
    )]
    Rename(RenameArgs),
    /// Keep a session's transcript at any age (--undo releases it)
    #[command(
        after_help = "Examples:\n  ctxloom session keep amber-swift-owl\n  ctxloom session keep amber-swift-owl --undo"
    )]
    Keep(KeepArgs),
    /// Destroy a session: every event of it and of its runs, its directory and its lock; its name stays taken
    #[cfg(feature = "session-destroy")]
    #[command(
        after_help = "Examples:\n  ctxloom session remove amber-swift-owl\n  ctxloom session remove amber-swift-owl --yes"
    )]
    Remove(RemoveArgs),
    /// Tidy every ended session of this project by rule: destroy what it can prove is safe, report the rest
    #[cfg(feature = "session-destroy")]
    #[command(
        after_help = "Examples:\n  ctxloom session sweep\n  ctxloom session sweep --purge-older-than 90d --yes"
    )]
    Sweep(SweepArgs),
    /// The recorded conversation behind a session: list it, show it
    Transcript(Transcript),
    /// The scratch git checkouts a session left behind: list them, remove the safe ones
    Worktrees(Worktrees),
    /// Import Go ctxloom's sessions (~/.ctxloom/sessions) into each project's store, once
    #[command(after_help = "Examples:\n  ctxloom session import")]
    Import,
}

#[derive(Args, Debug, Default)]
pub struct ListArgs {
    /// Include sessions from every project (default: filter to the current one)
    #[arg(long)]
    pub all: bool,
    /// Only sessions in this state, from the session's liveness lock: live, ended, unknown
    #[arg(long, value_name = "STATE")]
    pub state: Option<String>,
}

#[derive(Args, Debug)]
pub struct SearchArgs {
    /// Words that must all match, case-insensitively, the session's name, title or times
    #[arg(required = true, value_name = "WORD")]
    pub words: Vec<String>,
    /// Search sessions from every project (default: the current one)
    #[arg(long)]
    pub all: bool,
    /// Only sessions in this state, from the session's liveness lock: live, ended, unknown
    #[arg(long, value_name = "STATE")]
    pub state: Option<String>,
}

#[derive(Args, Debug)]
pub struct HarpArgs {
    /// The session's name
    #[arg(value_name = "SESSION-NAME")]
    pub harp: String,
}

#[derive(Args, Debug)]
pub struct RenameArgs {
    /// The session's name
    #[arg(value_name = "SESSION-NAME")]
    pub harp: String,
    /// The title, free text (empty clears it)
    #[arg(value_name = "TITLE")]
    pub title: String,
}

#[derive(Args, Debug)]
pub struct KeepArgs {
    /// The session's name
    #[arg(value_name = "SESSION-NAME")]
    pub harp: String,
    /// Release the mark: the transcript becomes reclaimable once its retention has passed
    #[arg(long)]
    pub undo: bool,
}

#[cfg(feature = "session-destroy")]
#[derive(Args, Debug)]
pub struct RemoveArgs {
    /// The session's name
    #[arg(value_name = "SESSION-NAME")]
    pub harp: String,
    /// Apply the plan this invocation printed (default: report only)
    #[arg(short = 'y', long)]
    pub yes: bool,
    /// Permit destroying a session whose owner may still be running: its lock is held, or it has none
    #[arg(long)]
    pub even_if_live: bool,
}

/// The `session transcript` group.
#[derive(Args, Debug)]
pub struct Transcript {
    #[command(subcommand)]
    pub verb: Option<TranscriptVerb>,
}

#[derive(Subcommand, Debug)]
pub enum TranscriptVerb {
    /// List which sessions have a captured transcript, and how large each one is
    List(TranscriptListArgs),
    /// Print a session's whole transcript, then exit
    Show(HarpArgs),
}

#[derive(Args, Debug)]
pub struct SweepArgs {
    /// Purge ended sessions last active before this age (30d, 12w, 720h) or date (2026-01-01); overriding session_purge_age; without either nothing is purged
    #[arg(long, value_name = "AGE")]
    pub purge_older_than: Option<String>,
    /// Sweep every session on this machine, not only this project's
    #[arg(long)]
    pub all_projects: bool,
    /// Act on exactly the plan this reports
    #[arg(short = 'y', long)]
    pub yes: bool,
}

#[derive(Args, Debug, Default)]
pub struct TranscriptListArgs {
    /// Restrict the listing to this session
    #[arg(value_name = "SESSION-NAME")]
    pub harp: Option<String>,
    /// Include sessions from every project (default: the current one)
    #[arg(long)]
    pub all: bool,
}

/// The `session worktrees` group.
#[derive(Args, Debug)]
pub struct Worktrees {
    #[command(subcommand)]
    pub verb: Option<WorktreesVerb>,
}

#[derive(Subcommand, Debug)]
pub enum WorktreesVerb {
    /// List ctxloom-owned scratch worktrees and the verdict each one would get
    #[command(after_help = "Examples:\n  ctxloom session worktrees list")]
    List(WorktreesListArgs),
    /// Remove a session's scratch worktrees that this run can prove are safe
    #[command(after_help = "Examples:\n  ctxloom session worktrees purge amber-swift-owl --yes")]
    Purge(WorktreesPurgeArgs),
}

#[derive(Args, Debug, Default)]
pub struct WorktreesListArgs {
    /// Restrict the listing to this session
    #[arg(value_name = "SESSION-NAME")]
    pub harp: Option<String>,
}

#[derive(Args, Debug)]
pub struct WorktreesPurgeArgs {
    /// The session whose scratch worktrees to remove
    #[arg(value_name = "SESSION-NAME")]
    pub harp: String,
    /// Act on exactly the plan this invocation printed. No config key may pre-answer it.
    #[arg(short = 'y', long)]
    pub yes: bool,
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
