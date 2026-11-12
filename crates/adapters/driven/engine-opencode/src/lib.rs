//! opencode's two halves.
//!
//! opencode has no hook commands of its own: its hooks are plugins running in
//! its process. ctxloom writes one into each session home (`plugins/ctxloom.js`,
//! found through `OPENCODE_CONFIG_DIR`, [`home`]), and it forwards what
//! opencode's bus says of the session, one event at a time and in order, by
//! running `ctxloom hook opencode execute <Event>` with the event on stdin
//! (`ctxloom` being the running binary's file name, owner ruling 163:
//! [`Opencode::calling`]).
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
mod stream;

use surface_ports::{
    Claim, EngineSurfaces, HookCodec, Inputs, Kind, Planned, SurfaceError, SurfaceStore, Wiring,
};

pub use codec::Codec;
pub use drive::{ENV_ALLOWLIST, INTERACTIVE_ENV_ALLOWLIST, LOGIN_FILE, PROVIDER_KEYS, SIGN_IN};
pub use home::{plugin, CONFIG_FILE, CONTEXT_FILE, PLUGIN, PLUGIN_FILE, PRESEED_LOCK};

/// The engine's name.
pub const NAME: &str = "opencode";

/// opencode.
pub struct Opencode {
    codec: Codec,
    /// The program the plugin runs for each event: the running binary's file
    /// name (owner ruling 163), told by the composition root.
    program: String,
}

impl Default for Opencode {
    fn default() -> Opencode {
        Opencode {
            codec: Codec,
            program: surface_ports::CALLBACK_PROGRAM.to_string(),
        }
    }
}

impl Opencode {
    /// opencode, its plugin calling [`surface_ports::CALLBACK_PROGRAM`].
    pub fn new() -> Opencode {
        Opencode::default()
    }

    /// opencode, its plugin calling `program`: the running binary's file
    /// name, as the composition root resolved it.
    pub fn calling(program: &str) -> Opencode {
        Opencode {
            program: program.to_string(),
            ..Opencode::default()
        }
    }
}

impl EngineSurfaces for Opencode {
    fn name(&self) -> &str {
        NAME
    }

    fn carries(&self, kind: Kind) -> bool {
        kind == Kind::Context
    }

    /// A session's home holds the context (`AGENTS.md` in
    /// `OPENCODE_CONFIG_DIR`), so a copy at rest would double it.
    fn context_rides_launch(&self) -> bool {
        true
    }

    fn plan(&self, kind: Kind, inputs: &Inputs) -> Result<Planned, SurfaceError> {
        let mut planned = Planned::default();
        if kind == Kind::Context {
            planned.claims.push(Claim::Section {
                path: inputs
                    .context_file
                    .clone()
                    .unwrap_or_else(|| CONTEXT_FILE.to_string()),
                text: inputs.context.clone(),
            });
        }
        Ok(planned)
    }

    // `Ok(Default::default())` is `Ok(Wiring::default())`: the mutant is the
    // body itself (nothing of ctxloom's is wired at rest for opencode).
    #[cfg_attr(test, mutants::skip)]
    fn wiring(&self, _store: &dyn SurfaceStore, _target: &str) -> Result<Wiring, SurfaceError> {
        Ok(Wiring::default())
    }

    fn codec(&self) -> &dyn HookCodec {
        &self.codec
    }
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
