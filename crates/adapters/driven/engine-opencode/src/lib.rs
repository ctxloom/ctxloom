//! opencode's two halves.
//!
//! opencode has no hook commands of its own: its hooks are plugins running in
//! its process. ctxloom writes one into each session home (`plugins/ctxloom.js`,
//! found through `OPENCODE_CONFIG_DIR`, [`home`]), and it forwards what
//! opencode's bus says of the session, one event at a time and in order, by
//! running `ctxloom hook opencode execute <Event>` with the event on stdin.
//! [`Codec`] reads those payloads into the unified hook event.
//!
//! The drive half (`drive`) runs a child's turn as `opencode run --format
//! json` and the human's session as opencode's TUI, both with the same plugin.
//!
//! At a directory's root (the surface half) ctxloom gives opencode its context
//! alone, as a section of `AGENTS.md`; MCP servers, settings, hooks, commands
//! and skills at rest are not built for opencode.

mod codec;
mod drive;
mod home;

use surface_ports::{
    Claim, EngineSurfaces, HookCodec, Inputs, Kind, Planned, SurfaceError, SurfaceStore, Wiring,
};

pub use codec::Codec;
pub use drive::{ENV_ALLOWLIST, INTERACTIVE_ENV_ALLOWLIST, OPTIONAL_KEY};
pub use home::{CONTEXT_FILE, PLUGIN, PLUGIN_FILE, PRESEED_LOCK};

/// The engine's name.
pub const NAME: &str = "opencode";

/// opencode.
#[derive(Default)]
pub struct Opencode {
    codec: Codec,
}

impl Opencode {
    pub fn new() -> Opencode {
        Opencode::default()
    }
}

impl EngineSurfaces for Opencode {
    fn name(&self) -> &str {
        NAME
    }

    fn carries(&self, _kind: Kind) -> bool {
        false
    }

    fn context_rides_launch(&self) -> bool {
        false
    }

    fn plan(&self, _kind: Kind, _inputs: &Inputs) -> Result<Planned, SurfaceError> {
        Ok(Planned::default())
    }

    fn wiring(&self, _store: &dyn SurfaceStore, _target: &str) -> Result<Wiring, SurfaceError> {
        Ok(Wiring::default())
    }

    fn codec(&self) -> &dyn HookCodec {
        &self.codec
    }
}

#[allow(dead_code)]
fn section(path: &str, text: &str) -> Claim {
    Claim::Section {
        path: path.to_string(),
        text: text.to_string(),
    }
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
