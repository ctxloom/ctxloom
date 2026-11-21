//! A library view as its API message (OD-40; design §6): what every noun's
//! view-to-message mapping shares. Each noun's view is mapped, in its own
//! module (`noun/<noun>.rs`, design §8.4), to its `ctxloom.v1` message
//! (library.proto), named under the parent the daemon's `ResolveProject`
//! gives (OQ-2; `users/me` outside a project, OQ-3), and printed here as the
//! message's generated proto3 JSON (OQ-21): the sourced values, an agent's
//! settings (an engine's agents, and agent and `run --dry-run` once B2
//! converts them) and a free-form document. The views stay as they are; at
//! T13 the same mapping moves into the daemon.
//!
//! Converted so far: the `engine` noun (the spine's worked example,
//! `noun/engine.rs`), `agent`, `companion` and `config`. The other nouns still print their views, until
//! their branch converts them.

use std::io::{self, Write};

/// Write `message`'s proto3 JSON, as every json answer is written.
pub(crate) fn print<M: serde::Serialize>(out: &mut dyn Write, message: &M) -> io::Result<()> {
    angzarr_presentation::write_json(out, message)
}

#[cfg(test)]
#[path = "api.test.rs"]
pub(crate) mod tests;
