//! The trees domain: the shrunk tree of plan 3.2 (D5 Q4). It keeps founding
//! and the delegation limits, ephemeral, and the current occupant; routing,
//! grants, credentials and spawn refusals have left it.

#[allow(dead_code)]
mod generated {
    include!(concat!(env!("OUT_DIR"), "/ctxloom.runs.v1.domain.rs"));
}
mod aggregate;
mod refusal;
mod rejection;
mod state;

/// The tree aggregate's commands, one variant per command type.
pub type TreeCommand = TreeStateCommand;
/// The tree aggregate's events, one variant per event type.
pub type TreeEvent = TreeStateEvent;

pub use aggregate::TreeAggregate;
pub use generated::*;
pub use refusal::NotOurs;
#[cfg(any(test, feature = "test-support"))]
pub use rejection::diagnostic_samples;
pub use rejection::{TreeRejection, EN_US};
pub use state::{TreeState, TREE_DOMAIN};

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
