//! The reclaim use cases (S12): `ctxloom clean` (the project's regenerable
//! cache) and `ctxloom session sweep` (every ended session of a project,
//! decided by a fixed table; nothing is decided by a model). Both report
//! first and act only on `yes`.
//!
//! `session remove` is [`remove`]: a session's worktrees classified first,
//! the whole refused if one is unsafe, else the worktrees removed and then
//! the session destroyed (the daemon runs it, slice 2). The sweep acts
//! through it and the worktree leaf (`session worktrees purge`), so it
//! inherits every refusal they make: a session whose lock does not prove it dead, one marked keep,
//! one whose transcript is within its retention, and a scratch worktree with
//! uncommitted work, spared in place. The reclaim of the disposable members
//! of old sessions is S6's: it is not decided here.

mod age;
mod clean;
mod error;
mod purge;
mod remove;
mod sweep;

pub use age::parse_age;
pub use clean::{CleanReport, CleanTarget, Cleaner, TARGETS};
/// The ports' types the sweep's and cleaner's services speak, so a driving
/// adapter's tests fake them without linking the ports.
pub use ctxloom_ports::{Failure, ProjectCache};
#[cfg(any(test, feature = "test-support"))]
pub use error::diagnostic_samples;
pub use error::{ReclaimError, EN_US};
pub use purge::{purge_cutoff, SweepConfig};
pub use remove::{remove, Removals, SessionRemoval};
pub use sweep::{sweep, SweepReport, SweepRequest, SweepRow, Verdict};

#[cfg(test)]
#[path = "clean.test.rs"]
mod clean_tests;

#[cfg(test)]
#[path = "sweep.test.rs"]
mod sweep_tests;

#[cfg(test)]
#[path = "age.test.rs"]
mod age_tests;
