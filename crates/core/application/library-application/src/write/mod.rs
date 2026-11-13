//! The library's write side as an application (S2): create, edit and remove
//! for bundles, items, profiles, agents, MCP servers, skills and
//! companions; `config create` and `config edit`; export and import; the
//! editor round trips. Files are the truth (D4): a command plans the files it
//! leaves, checks that they read, and hands them to the ports.
//!
//! - A preview (a remove without `--yes`) and an edit with nothing to change
//!   call no port that writes.
//! - A refused write has written nothing: every check is made on the plan,
//!   before the first change goes out.
//! - A config write is a section merge with sorted keys and never heals a
//!   corrupt file ([`library::write::section`]).
//! - An import over what is there refuses, and the local edit survives,
//!   unless `--force`.

use std::sync::Arc;

use ctxloom_ports::{Editor, Scaffold, UserFiles};

mod agents;
mod bundles;
pub(crate) mod companions;
mod configfile;
mod configs;
mod editor;
pub(crate) mod engines;
pub(crate) mod error;
mod items;
mod mcp;
mod plan;
mod profiles;
pub mod requests;
mod skills;
pub mod views;

pub use error::WriteError;

/// The one check of a name a write mints (a bundle, fragment, command,
/// skill, MCP server or profile, at create, import or add): an ID
/// (`library::write::item::valid_id`, OQ-6), never nested (OQ-5). `kind` is
/// the word the refusal names.
pub(crate) fn require_id(kind: &str, name: &str) -> Result<(), crate::AppError> {
    if library::write::item::valid_id(name) {
        Ok(())
    } else {
        Err(WriteError::BadId {
            kind: kind.to_string(),
            name: name.to_string(),
        }
        .into())
    }
}

/// The ports the write side needs beyond the library's: the user's own files
/// (where export writes and import reads), the scaffold of a new project, the
/// editor. A companion is checked through the library's probe
/// ([`Library::with_companions`](crate::Library::with_companions)).
#[derive(Clone)]
pub struct WritePorts {
    pub user: Arc<dyn UserFiles>,
    pub scaffold: Arc<dyn Scaffold>,
    pub editor: Arc<dyn Editor>,
}
