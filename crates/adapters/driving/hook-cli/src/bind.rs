//! What `hook <engine> execute SessionStart` asks the edge to do beyond
//! printing: record the harp-to-engine-session mapping. Claude Code (and other
//! engines with SessionStart hooks) fire it once per TRANSCRIPT, not once per
//! session: /clear rotates the transcript to a new UUID under the same live
//! process and the hook fires again. Go: `session_bind.go` (release/0.7).

use angzarr_diagnostic::Diagnostic;

use crate::HookWarning;

/// What binds a session: the application's, behind the hook.
pub trait Binding {
    /// Record that `harp`'s engine session is `native_id`, its transcript
    /// `transcript_path` (either may be empty).
    fn bind(
        &mut self,
        harp: &str,
        native_id: &str,
        transcript_path: &str,
    ) -> Result<(), Box<dyn Diagnostic>>;
}

/// What the hook answered: the engine's input, and what it warned of.
#[derive(Clone, PartialEq, Eq, Debug, Default)]
pub struct Outcome {
    pub stdout: String,
    pub warnings: Vec<HookWarning>,
}
