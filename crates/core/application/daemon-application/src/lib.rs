//! The daemon's ONE launcher (owner T18), in the application ring: what the
//! user's daemon does with the one store (T14), over ports, so the
//! composition root only composes it.
//!
//! - Its START is the orphan sweep ([`Daemon::start_up`]): `run-launcher`'s
//!   [`Sweeper`], the one every launcher's step runs (rulings 86, 92, 93,
//!   95, 100), over every run the store holds, before anything is hosted. A
//!   killed daemon's children (their run locks left free) end `RecordRunnerLost`.
//! - Each [`Daemon::tick`]: the read models catch up (the same folds the
//!   kernel runs inline, S32), the policy plan is made for each project on
//!   the board (`session-policies`), and its steps are taken: a launch
//!   claimed and minted, a child started (planned by its project's dry
//!   run, enqueued judged on its launcher's environment, its session home
//!   prepared and its engine started), a turn fed, a clear handed, an end
//!   asked; what a child said is recorded as its run's facts. A child's
//!   hand-off is read at each of its starts, deleted at its end, and a
//!   hand-off no open session holds is swept (T6, slice 4 U3).
//! - The one-shots it hosts ([`OneShots`], U4, T7): each is the run use
//!   case's one-shot run in the daemon over its one store, in its client's
//!   environment (the engine's TURN command, owner s4-Q8), its engine
//!   stopped by the tick's stop step once its run's stream asks.
//! - The interactive sessions it prepares ([`Terminals`], U5, T9): each is
//!   the run use case's interactive session run in the daemon, its one
//!   launch handed to the frontend that asked ([`Frontended`]), which starts
//!   it on the user's terminal and reports its start and exit.
//! - Engines are resolved by NAME through the registry the root composes
//!   ([`HostedEngine`]: an `Engine` beside its `ChildEngine`), never one
//!   adapter named in code.
//!
//! The clock, the runtime, the push and the poll stay the root's: it calls
//! [`Daemon::tick`] when a commit lands or the poll passes, and decides the
//! idle exit from [`Daemon::hosting`].

mod adopt;
mod daemon;
mod one_shot;
pub mod owed;
mod ports;
pub mod positions;
pub mod reactions;
mod rehost;
mod terminal;
mod watches;

pub use daemon::{Daemon, Folds, Place, Ports, Taken, HAND_OVER_PERIOD};
pub use one_shot::{
    Created, HostProject, HostRequest, Hosted, OneShotControl, OneShotHost, OneShots, Told, Watch,
    STOP_GRACE_MS,
};
pub use ports::{
    child_env_field, ChildHomes, ChildInputs, ChildPlans, ChildWorkspaces, Distilling, Ears,
    ForLaunch, HoldFlushed, HostedEngine, Monotonic, Program, StartHolds, Transcripts,
};
pub use run_launcher::{Sweeper, Swept};
pub use terminal::{
    Created as TerminalCreated, FrontendReports, Frontended, Placed as TerminalPlaced,
    PreparedLaunches, Seen, TerminalHost, TerminalRequest, Terminals, Told as TerminalTold, Waits,
    Watch as TerminalWatch, POLL, START_WITHIN,
};
pub use watches::{signal_of, SessionWatch, SessionWatches, KEEPALIVE};

#[cfg(test)]
#[path = "daemon.test.rs"]
mod tests;

#[cfg(test)]
#[path = "hosted.test.rs"]
mod hosted;
