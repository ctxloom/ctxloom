//! The engine callbacks ctxloom installs into engines (the hidden `hook`
//! command). Each is a machine callback, not for people, and answers to a
//! different contract: stdout carries the hook's output only, and a
//! diagnostic goes on the warning channel (`HookWarning`), never to stdout.
//!
//! There is one callback form, `hook <engine> execute <hook-name>` (the
//! owner's ruling, 2026-11-08): `<engine>` is any name (the registry judges
//! it and refuses an unknown one, listing the known), `<hook-name>` the
//! engine's native event, or `statusline` for its statusline command, which is
//! rendered here and records nothing. Every other name is answered by the
//! application ([`HookService`]) through the engine's own codec, and a
//! session start it asks to bind is bound through [`Binding`]. This crate
//! holds no engine knowledge.
//!
//! `mail-drain`, `mail-peek` and `permission` arrive with the slices that need
//! them (S10). The retired `turn-changed` is not a command; `stamp-plan` is
//! taskloom's (D17).

mod bind;
mod capture;
mod error;
mod execute;
mod hud;

use angzarr_diagnostic::Diagnostic;
use clap::{Args, Command, Subcommand};

pub use bind::{Binding, Outcome};
pub use capture::{execute_recording, NotRecorded, Recording};
#[cfg(any(test, feature = "test-support"))]
pub use error::diagnostic_samples;
pub use error::{HookError, HookWarning, EN_US};
pub use execute::{execute, STATUSLINE};
pub use hud::{
    bar_color, context_bar, hud, render, StatusPayload, CYAN, DIM, GREEN, RED, RESET, YELLOW,
};

/// The `hook` noun, shaped for mounting on the command line: hidden.
pub fn noun() -> Command {
    Hook::augment_args(Command::new("hook"))
        .about("Callbacks ctxloom installs into engines (not for people)")
        .hide(true)
}

/// The `hook` group: hidden, for engines.
#[derive(Args, Debug)]
#[command(subcommand_required = true, arg_required_else_help = true)]
pub struct Hook {
    /// The engine firing the hook, by its registry name (`claude-code`)
    #[arg(value_name = "ENGINE")]
    pub engine: String,
    #[command(subcommand)]
    pub verb: HookVerb,
}

#[derive(Subcommand, Debug)]
pub enum HookVerb {
    /// Run the engine's hook (internal: the command engines are configured to call)
    Execute {
        /// The engine's native hook event (`SessionStart`, `Stop`, ...), or
        /// `statusline` for its statusline command
        #[arg(value_name = "HOOK_NAME")]
        hook_name: String,
    },
}

/// What the callback calls: the application's answer to one payload.
pub trait HookService {
    /// The answer to `hook <engine> execute <native>`: the application's
    /// (`Setup::execute_hook`), for any engine the registry names. `harp` is
    /// the session the hook runs in (may be empty).
    fn execute(
        &self,
        engine: &str,
        native: &str,
        payload: &str,
        harp: &str,
    ) -> Result<setup_application::HookAnswer, Box<dyn Diagnostic>>;
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
