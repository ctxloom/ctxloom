//! The run launcher (Plan step 1b-ii): the runner side of a run in one
//! process.
//!
//! - [`Launcher`]: a `CatchUp` over the pending launches that, for each run
//!   owed a runner whose [`Order`] it holds, drives the engine for one turn
//!   through the `RunnerLauncher` and records the outcome as the run's facts
//!   (attached, turn started, turn completed or failed, exited; or a launch
//!   that never started). It also sweeps what other processes left: a run still owed a runner
//!   whose own lock is free, and a started run whose session's lock is free,
//!   have lost their owner, and are ended `orphaned` (rulings 86, 92, 93).
//!
//! The session home is the launcher's, and nothing else of the session's
//! directory is written (owner ruling 87a): the session's events, in the
//! store, are its record, and its transcript is read from them
//! (`session_index`).

mod error;
mod launcher;
mod orders;
mod workspace;

#[cfg(any(test, feature = "test-support"))]
pub use error::diagnostic_samples;
pub use error::{LaunchError, EN_US};
pub use launcher::{Launched, Launcher, Outcome};
pub use orders::{Order, Orders};
pub use workspace::{place_workspace, Axis, Placement, Workspace};

/// In-memory ports and a world of the real aggregates, for this crate's tests
/// and the use cases' above it.
#[cfg(any(test, feature = "test-support"))]
pub mod fakes;
