//! What the daemon's launcher needs from outside, beyond `run-ports`' edge,
//! locks and tokens: each is the root's to implement.

use std::collections::BTreeMap;
use std::sync::Arc;

use library_application::Previewed;
use run_ports::{ChildEngine, ChildObservation, ChildSpec, Engine};

/// Where an engine keeps a child's transcripts, from the child's spec.
pub type Transcripts = Arc<dyn Fn(&ChildSpec) -> Option<String> + Send + Sync>;

/// One engine the daemon can host a child on, by its name: its `Engine` (the
/// session home it prepares, its credentials) beside its `ChildEngine` (the
/// process it starts), as the root's engine registry composes them.
#[derive(Clone)]
pub struct HostedEngine {
    pub engine: Arc<dyn Engine>,
    pub child: Arc<dyn ChildEngine>,
    /// Where the engine keeps a child's transcripts, if it says (a bind
    /// names the transcript of the native id there).
    pub transcripts: Transcripts,
}

impl HostedEngine {
    /// The engine's name.
    pub fn name(&self) -> &str {
        self.engine.name()
    }
}

/// A child's plan: its project's dry run, as a ctxloom process in that
/// project would make it, on `engine`, with `env` (its launcher's
/// environment). The error is the plan's code.
pub trait ChildPlans: Send + Sync {
    fn preview(
        &self,
        engine: &str,
        project_dir: &str,
        env: &BTreeMap<String, String>,
    ) -> Result<Previewed, String>;
}

/// The hand-off files a launcher leaves a child's environment in (the 0600
/// files of `~/.ctxloom/daemon/launch/`): taken at the child's first start.
pub trait HandOffs: Send + Sync {
    fn take(&self, session: &str) -> Option<BTreeMap<String, String>>;
}

/// A child's session directory, in its project: where its session home is,
/// and a file written in it.
pub trait ChildHomes: Send + Sync {
    /// The session home of `harp` in `project_dir`.
    fn home(&self, project_dir: &str, harp: &str) -> String;

    /// Write `bytes` at `rel` under `harp`'s session directory.
    fn write(&self, project_dir: &str, harp: &str, rel: &str, bytes: &[u8]) -> Result<(), String>;
}

/// What hosted children say, heard off the launcher's thread: the root
/// pumps each child's observations (a blocking iterator) and hands them back
/// as they come, by run.
pub trait Ears: Send + Sync {
    /// Listen to `observations`, the child of `run`'s.
    fn listen(&self, run: String, observations: Box<dyn Iterator<Item = ChildObservation> + Send>);

    /// What was heard since last asked, in the order heard.
    fn heard(&self) -> Vec<(String, ChildObservation)>;
}
