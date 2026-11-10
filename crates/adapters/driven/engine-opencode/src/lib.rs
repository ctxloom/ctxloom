//! opencode's two halves.
//!
//! opencode has no hook commands of its own: its hooks are plugins running in
//! its process. ctxloom writes one into each session home (`plugins/ctxloom.js`,
//! found through `OPENCODE_CONFIG_DIR`), and it forwards what opencode's bus
//! says of the session, one event at a time and in order, by running
//! `ctxloom hook opencode execute <Event>` with the event on stdin. [`Codec`]
//! reads those payloads into the unified hook event.

mod codec;

pub use codec::Codec;

/// The engine's name.
pub const NAME: &str = "opencode";
