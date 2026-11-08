//! The run use case (Plan step 1b-ii): `run --one-shot`.
//!
//! [`Runs::one_shot`] plans the run with the library (the default agent's
//! context, or the profile's), mints the session, enqueues the run, has the
//! launcher drive the engine for one turn and the recorder write the session's
//! directory, and answers with the engine's reply. The interactive run, a
//! saved session's resume and the dry run are not here: the library answers
//! the last, and the others arrive with their steps.

mod error;
mod one_shot;

#[cfg(any(test, feature = "test-support"))]
pub use error::diagnostic_samples;
pub use error::{RunError, EN_US};
pub use one_shot::{Answer, Core, Done, OneShot, Parts, Place, Planner, Runs};
