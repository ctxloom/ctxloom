//! The runs use cases (S5, in-process): what an agent may do to the core, and
//! only that.
//!
//! - [`Facts`]: the inbound fact port, narrow by construction (plan 4.2). The
//!   closed [`RunnerFact`] and [`ModelFact`] have no command in them; every
//!   variant addresses the caller's own run or session, and its `external_id` is derived from its
//!   cause (plan 4.3).
//! - [`Feeds`]: per-stream catch-up from a resume point, no cross-stream
//!   position anywhere (plan 6).
//! - [`Enqueuer`]: the run's enqueue, which mints no credential (owner
//!   ruling s4-Q3).
//!
//! On the host there is ONE principal, the user (slice 2, the separation
//! model): the runner's observations and the model's claims are provenances,
//! not permissions, and no token tells them apart. A decider role (S10, S14)
//! turns a claim into a transition. The gate is angzarr's `Gate`:
//! [`UserGate`], the user daemon's (the user bearer). The CLI run's own
//! listener and its `HookGate` are gone (slice 4 U7, owner ruling s4-Q3): a
//! hook delivers to the daemon.
//!
//! RED (S5, design-by-test): every body is a stub; the tests propose the
//! signatures and the behaviour.

mod capture;
mod enqueue;
mod error;
mod facts;
mod feeds;
mod gate;
mod hold;
mod hook_commands;
mod identity;
mod stamp;
mod user_gate;

pub use capture::{
    delivery_budget, no_such_session, observe, Attempt, Capture, Observation, Report,
};
pub use enqueue::{CredentialEvidence, EnqueueError, EnqueueRequest, Enqueued, Enqueuer, Evidence};
#[cfg(any(test, feature = "test-support"))]
pub use error::diagnostic_samples;
pub use error::{AppError, EN_US};
pub use facts::{launch_failed_book, Fact, Facts, ModelFact, Recorded, RunnerFact};
pub use feeds::{Cursor, Feeds, Follow};
pub use gate::owns;
pub use hold::{
    command_flushes, undelivered, Flush, HoldReport, Holding, HookClock, Kept, Left, NotHeld,
    Outcome, RunReport, Serving, SessionFlush, Undelivered, Why, TAKE_ROUNDS,
};
pub use hook_commands::{EdgeCommands, EdgeFailure};
pub use identity::{Identity, Role};
/// The edge port the listener serves the kernel through, for the driving adapter
/// that holds one without depending on the ports.
pub use run_ports::Edge;
pub use stamp::stamped;
pub use user_gate::{user_allowlist, User, UserGate};

#[cfg(test)]
#[path = "fakes.test.rs"]
mod fakes;
