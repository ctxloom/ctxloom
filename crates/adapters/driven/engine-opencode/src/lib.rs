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
//! A long-lived child (`child`, the [`run_ports::ChildEngine`]) is one
//! `opencode acp` process driven as an ACP client, many turns on it.
//!
//! At a directory's root (the surface half) ctxloom gives opencode its context
//! alone, as a section of `AGENTS.md`; MCP servers, settings, hooks, commands
//! and skills at rest are not built for opencode. A session's home carries
//! what the launch does: the authored hooks ([`authored`]), the commands and
//! the MCP servers ([`carried`]).

mod advice;
mod authored;
mod carried;
mod child;
mod codec;
mod conversation;
mod drive;
mod home;
mod stream;

use surface_ports::{
    Claim, EngineSurfaces, HookCodec, Inputs, Kind, Planned, SurfaceError, SurfaceStore, Wiring,
};

#[cfg(any(test, feature = "test-support"))]
pub use advice::diagnostic_samples;
pub use advice::{OpencodeNotice, EN_US};
pub use authored::AUTHORED_FILE;
pub use codec::Codec;
pub use drive::{ENV_ALLOWLIST, INTERACTIVE_ENV_ALLOWLIST, LOGIN_FILE, PROVIDER_KEYS, SIGN_IN};
pub use home::{
    plugin, user_config_file, user_model_file, CONFIG_FILE, CONTEXT_FILE, MODEL_SEED, PLUGIN,
    PLUGIN_FILE, PRESEED_LOCK,
};

/// The engine's name.
pub const NAME: &str = "opencode";

/// Every unified hook event, in event order: those the plugin has no
/// trigger for ([`authored`]'s routes) are opencode's losses.
const UNIFIED_HOOK_EVENTS: &[&str] = &[
    "pre_tool",
    "post_tool",
    "session_start",
    "session_end",
    "turn_end",
    "turn_start",
    "pre_shell",
    "post_file_edit",
    "permission_ask",
    "post_tool_batch",
    "message_display",
    "compaction_before",
    "compaction_after",
];

/// opencode.
pub struct Opencode {
    codec: Codec,
    /// The program the plugin runs for each event: the running binary's file
    /// name (owner ruling 163), told by the composition root.
    program: String,
    /// The user's own `model.json`, as read, which a session home is seeded
    /// with (owner ruling 167), told by the composition root.
    model: Option<Vec<u8>>,
    /// The user's own global `opencode.json`, as the composition root found
    /// it: a session home's config starts from it (owner ruling 169).
    user_config: Option<run_ports::UserSettings>,
}

impl Default for Opencode {
    fn default() -> Opencode {
        Opencode {
            codec: Codec,
            program: surface_ports::CALLBACK_PROGRAM.to_string(),
            model: None,
            user_config: None,
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

    /// opencode, told the user's own `model.json` ([`user_model_file`]), as
    /// read: a session home is seeded with it, once ([`MODEL_SEED`], owner
    /// ruling 167); none, nothing is.
    pub fn seeding_model(self, model: Option<Vec<u8>>) -> Opencode {
        Opencode { model, ..self }
    }

    /// opencode, told the user's own global config ([`user_config_file`]):
    /// a session home's `opencode.json` is hers with the agent's permissions
    /// document as its `permission` (owner ruling 169); none, `{}` is.
    pub fn over_user_config(self, user: Option<run_ports::UserSettings>) -> Opencode {
        Opencode {
            user_config: user,
            ..self
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

    fn hook_losses(&self) -> Vec<(String, String)> {
        UNIFIED_HOOK_EVENTS
            .iter()
            .filter(|event| !authored::carries(event))
            .map(|event| {
                let reason = if *event == "session_end" {
                    format!("{NAME} has no native {event} event")
                } else {
                    format!("ctxloom's {NAME} plugin runs no {event} hook")
                };
                ((*event).to_string(), reason)
            })
            .collect()
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
