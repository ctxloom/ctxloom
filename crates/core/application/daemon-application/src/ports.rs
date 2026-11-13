//! What the daemon's launcher needs from outside, beyond `run-ports`' edge,
//! locks and tokens: each is the root's to implement.

use std::collections::BTreeMap;
use std::sync::Arc;

use library_application::Previewed;
use run_ports::{ChildEngine, ChildObservation, ChildSpec, Engine, GitUser, SessionRecords};

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

    /// The user's git identity in the project at `project_dir`, as git
    /// resolves it there in her own environment (`env`, her launcher's, and
    /// her home): what the child's session home is seeded with (owner
    /// ruling 112), as a CLI run's is. None when git knows neither part.
    fn git_user(&self, project_dir: &str, env: &BTreeMap<String, String>) -> Option<GitUser>;
}

/// The hand-off files a launcher leaves a child's environment in (the 0600
/// files of `~/.ctxloom/daemon/launch/`): taken at the child's first start.
pub trait HandOffs: Send + Sync {
    fn take(&self, session: &str) -> Option<BTreeMap<String, String>>;
}

/// The session directories of a child's project: the same port the one
/// launcher prepares every other session's home through
/// (`run_ports::SessionRecords`), so a hosted child's home is set up by the
/// same code.
pub trait ChildHomes: Send + Sync {
    /// The session directories of the project at `project_dir`.
    fn records(&self, project_dir: &str) -> Box<dyn SessionRecords>;
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
