//! The run use case (Plan step 1b-ii): `run --one-shot`.
//!
//! [`Runs::one_shot`] plans the run with the library (the default agent's
//! context, or the profile's), mints the session, enqueues the run, has the
//! launcher drive the engine for one turn and the recorder write the session's
//! directory, and answers with the engine's reply. [`Runs::interactive`] is the
//! human's `run` (Plan step 3): the same plan and launch with her terminal
//! given to the engine. A saved session's resume and the dry run are not here:
//! the library answers the last, and the other arrives with its step.
//! [`hosted_child`] is how the user daemon plans and judges a child it hosts,
//! the same way a one-shot child is (D18, rulings 53, 73e). [`client`] is
//! `run --one-shot` as the daemon's client (U4, T7): the daemon hosts the
//! run (`Runs::one_shot` in its process), the command prints what it said.
//! [`frontend`] is
//! the interactive session's frontend (U5, T9): the daemon's launch started
//! on the user's terminal, its start and exit reported to the daemon.

pub mod client;
mod credential;
mod error;
pub mod frontend;
mod hosted;
mod loadout;
mod one_shot;
mod settings_log;

pub use credential::{credential_evidence, links_on, share_login};
#[cfg(any(test, feature = "test-support"))]
pub use error::diagnostic_samples;
pub use error::{HostedEnd, RunError, EN_US};
pub use hosted::{hosted_child, HostedChild};
pub use loadout::loadout_of_plan;
pub use one_shot::{
    Answer, Core, Done, Ended, OneShot, Parts, Place, Placement, Planner, RunService, Runs, Telling,
};
