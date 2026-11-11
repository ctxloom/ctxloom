//! The runs use cases (S5, in-process): what an agent may do to the core, and
//! only that.
//!
//! - [`Facts`]: the inbound fact port, narrow by construction (plan 4.2). The
//!   closed [`RunnerFact`] and [`ModelFact`] have no command in them; every
//!   variant addresses the caller's own run or session, and its `external_id` is derived from its
//!   cause (plan 4.3).
//! - [`AgentRequests`]: the agent's refusable asks, equally closed and
//!   identity-stamped (D7). The MODEL principal's only.
//!
//! A run has TWO principals (owner ruling 2026-11-04, item 16): the runner,
//! which records authoritative observations, and the model, which records only
//! advisory CLAIM facts and sends the closed requests. Each has its own token
//! and allowlist; a decider role (S10, S14) turns a claim into a transition.
//! - [`Feeds`]: per-stream catch-up from a resume point, no cross-stream
//!   position anywhere (plan 6).
//! - [`Admission`]: credential to [`Identity`], the allowlist, `owns`,
//!   then the edge. The LOCAL shape of the gate the upstream listener will
//!   provide (embedded-listener.plan.md section 4); a stopgap SERVER is not
//!   here and is not authorized.
//! - [`Enqueuer`]: the run's enqueue, which mints the credential (D29).
//!
//! RED (S5, design-by-test): every body is a stub; the tests propose the
//! signatures and the behaviour.

mod admission;
mod capture;
mod enqueue;
mod error;
mod facts;
mod feeds;
mod gate;
mod hook_commands;
mod hook_gate;
mod identity;
mod orchestrate_gate;
mod requests;
mod stamp;

pub use admission::Admission;
pub use capture::{delivery_budget, observe, Attempt, Capture, Observation, Report};
pub use enqueue::{
    CredentialEvidence, EnqueueError, EnqueueRequest, Enqueued, Enqueuer, Evidence, PostureEvidence,
};
#[cfg(any(test, feature = "test-support"))]
pub use error::diagnostic_samples;
pub use error::{AppError, Why, EN_US};
pub use facts::{Fact, Facts, ModelFact, Recorded, RunnerFact};
pub use feeds::{Cursor, Feeds, Follow};
pub use gate::{owns, Allowlist, Credential, Gate, RunsGate};
pub use hook_commands::{EdgeCommands, EdgeFailure};
pub use hook_gate::{hook_allowlist, HookGate, HookIdentity, Scope};
pub use identity::{Identity, Principal, Role};
pub use orchestrate_gate::{
    orchestration_scope, stamp_book, OrchestratorGate, OrchestratorIdentity,
};
pub use requests::{AgentRequest, AgentRequests, Requested};
/// The edge port the listener serves the kernel through, for the driving adapter
/// that holds one without depending on the ports.
pub use run_ports::Edge;
pub use stamp::stamped;

#[cfg(test)]
#[path = "fakes.test.rs"]
mod fakes;
