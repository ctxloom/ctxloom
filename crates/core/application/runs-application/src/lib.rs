//! The runs use cases (S5, in-process): what an agent may do to the core, and
//! only that.
//!
//! - [`Facts`]: the inbound fact port, narrow by construction (plan 4.2). The
//!   closed [`RunnerFact`] and [`ModelFact`] have no command in them; every
//!   variant addresses the caller's own run or session, and its `external_id` is derived from its
//!   cause (plan 4.3).
//! - [`AgentRequests`]: the agent's refusable asks, equally closed and
//!   identity-stamped (D7).
//! - [`Feeds`]: per-stream catch-up from a resume point, no cross-stream
//!   position anywhere (plan 6).
//! - [`Enqueuer`]: the run's enqueue, which mints the hook bearer's token
//!   (D29).
//!
//! On the host there is ONE principal, the user (slice 2, the separation
//! model): the runner's observations and the model's claims are provenances,
//! not permissions, and no token tells them apart. A decider role (S10, S14)
//! turns a claim into a transition. The gates are angzarr's `Gate`:
//! [`UserGate`], the user daemon's (the user bearer), and [`HookGate`], a CLI
//! run's own listener's (its hook bearer, kept while the CLI serves its own
//! runs).
//!
//! RED (S5, design-by-test): every body is a stub; the tests propose the
//! signatures and the behaviour.

mod capture;
mod enqueue;
mod error;
mod facts;
mod feeds;
mod gate;
mod hook_commands;
mod hook_gate;
mod identity;
mod requests;
mod stamp;
mod user_gate;

pub use capture::{delivery_budget, observe, Attempt, Capture, Observation, Report};
pub use enqueue::{CredentialEvidence, EnqueueError, EnqueueRequest, Enqueued, Enqueuer, Evidence};
#[cfg(any(test, feature = "test-support"))]
pub use error::diagnostic_samples;
pub use error::{AppError, EN_US};
pub use facts::{Fact, Facts, ModelFact, Recorded, RunnerFact};
pub use feeds::{Cursor, Feeds, Follow};
pub use gate::owns;
pub use hook_commands::{EdgeCommands, EdgeFailure};
pub use hook_gate::{HookGate, HookIdentity};
pub use identity::{Identity, Role};
pub use requests::{AgentRequest, AgentRequests, Requested};
/// The edge port the listener serves the kernel through, for the driving adapter
/// that holds one without depending on the ports.
pub use run_ports::Edge;
pub use stamp::stamped;
pub use user_gate::{user_allowlist, User, UserGate};

#[cfg(test)]
#[path = "fakes.test.rs"]
mod fakes;
