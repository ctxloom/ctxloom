//! The run launcher (Plan step 1b-ii): the runner side of a run in one
//! process.
//!
//! - [`Launcher`]: a `CatchUp` over the pending launches that, for each run
//!   owed a runner whose [`Order`] it holds, drives the engine for one turn
//!   through the `RunnerLauncher` and records the outcome as the run's facts
//!   (attached, turn started, turn completed or failed, exited; or a launch
//!   that never started);
//! - [`Recorder`]: a `CatchUp` over the session and run streams that writes
//!   the session's directory, `.ctxloom/sessions/<harp>/`, from the events.
//!
//! The session home is the launcher's. The transcript (D27) is the recorder's
//! alone, projected from the session stream's capture events: the launcher
//! records its prompt and answer as the same facts an engine's hooks record.
//! The metadata and the records are the recorder's too.

mod error;
mod launcher;
mod orders;
mod projection;
mod recorder;
mod transcript;

#[cfg(any(test, feature = "test-support"))]
pub use error::diagnostic_samples;
pub use error::{LaunchError, EN_US};
pub use launcher::{Launched, Launcher, Outcome};
pub use orders::{Order, Orders};
pub use projection::Projection;
pub use recorder::{Recorder, METADATA, RUNS_RECORD, SESSION_RECORD, TRANSCRIPT_POSITION};
pub use transcript::{entry_line, next_seq, rfc3339, Speaker, TRANSCRIPT};

/// In-memory ports and a world of the real aggregates, for this crate's tests
/// and the use cases' above it.
#[cfg(any(test, feature = "test-support"))]
pub mod fakes;
