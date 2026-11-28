//! What the daemon's launcher needs from outside, beyond `run-ports`' edge,
//! locks and tokens: each is the root's to implement.

use std::collections::BTreeMap;
use std::sync::Arc;

use library_application::Previewed;
use run_ports::{
    ChildEngine, ChildObservation, ChildSpec, Engine, GitUser, HeldLock, SessionRecords,
};

/// Where an engine keeps a child's transcripts, from the child's spec.
pub type Transcripts = Arc<dyn Fn(&ChildSpec) -> Option<String> + Send + Sync>;

/// The engine a launch sets its session home up and starts its child with,
/// composed for that launch from what the launcher said of its user's
/// settings (T21: her settings are read at every launch, where HER launcher
/// finds them: its `CLAUDE_CONFIG_DIR`, its XDG bases, S7), its callbacks
/// running the launcher's program (owner, 2026-11-18; none: the daemon's).
pub type ForLaunch = Arc<
    dyn Fn(&BTreeMap<String, String>, Option<&Program>) -> (Arc<dyn Engine>, Arc<dyn ChildEngine>)
        + Send
        + Sync,
>;

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
    /// The engine each launch is composed with, from its launcher's places
    /// ([`ChildInputs::launcher`]); none: `engine` and `child` as they are.
    pub for_launch: Option<ForLaunch>,
}

impl HostedEngine {
    /// The engine's name.
    pub fn name(&self) -> &str {
        self.engine.name()
    }

    /// The engine and the child engine a launch whose launcher said
    /// `launcher` of its user's settings, and ran `program`, is set up and
    /// started with.
    pub fn launched(
        &self,
        launcher: &BTreeMap<String, String>,
        program: Option<&Program>,
    ) -> (Arc<dyn Engine>, Arc<dyn ChildEngine>) {
        match &self.for_launch {
            Some(compose) => compose(launcher, program),
            None => (Arc::clone(&self.engine), Arc::clone(&self.child)),
        }
    }
}

/// What a child was launched with, as its `ChildLaunched` recorded it (T6,
/// S10; owner ruling 2026-11-25: the inputs of a launch ride the event the
/// launch records, never a file or the daemon's memory): the environment the
/// child may be given (its secrets sealed in the record, opened here), where
/// the launcher finds its user's own settings (the variables that place
/// them: `CLAUDE_CONFIG_DIR`, the XDG bases), which the launch reads her
/// settings from and never hands the child, the agent it is planned for
/// (empty: the project's default) and the launcher's program.
#[derive(Clone, Default, PartialEq, Eq)]
pub struct ChildInputs {
    pub env: BTreeMap<String, String>,
    pub launcher: BTreeMap<String, String>,
    pub agent: String,
    /// The child's project (its id): whose seal key opened its environment.
    pub project: String,
    /// The launcher's program (owner, 2026-11-18): what the child's hooks
    /// run and its engine's `PATH` leads with; none, the daemon's own.
    pub program: Option<Program>,
}

impl std::fmt::Debug for ChildInputs {
    /// Both maps by name alone: a value may be a credential.
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("ChildInputs")
            .field("env", &run_ports::RedactedEnv(&self.env))
            .field("launcher", &run_ports::RedactedEnv(&self.launcher))
            .field("agent", &self.agent)
            .field("program", &self.program)
            .finish()
    }
}

/// The field a child's environment value is sealed for: bound to its
/// variable, so it cannot be moved to another.
pub fn child_env_field(name: &str) -> String {
    format!("child-env:{name}")
}

impl ChildInputs {
    /// The inputs a `ChildLaunched` recorded, its sealed values opened with
    /// `sealing`.
    pub fn of_launched(
        launched: &sessions::ChildLaunched,
        sealing: &dyn run_ports::Sealing,
    ) -> Result<ChildInputs, run_ports::SealingError> {
        let project = launched
            .child
            .as_ref()
            .map(|child| child.project_id.clone())
            .unwrap_or_default();
        let value = |entry: &sessions::ChildEnvEntry| -> Result<String, run_ports::SealingError> {
            if entry.sealed.is_empty() {
                return Ok(entry.plain.clone());
            }
            let plain = sealing.open(&project, &child_env_field(&entry.name), &entry.sealed)?;
            Ok(String::from_utf8_lossy(&plain).into_owned())
        };
        let map = |entries: &[sessions::ChildEnvEntry]| -> Result<_, run_ports::SealingError> {
            entries
                .iter()
                .map(|entry| Ok((entry.name.clone(), value(entry)?)))
                .collect::<Result<BTreeMap<_, _>, _>>()
        };
        let program = &launched.client_program;
        Ok(ChildInputs {
            env: map(&launched.env)?,
            launcher: map(&launched.settings_places)?,
            agent: launched.agent.clone(),
            project,
            program: (!program.name.is_empty() || !program.directory.is_empty()).then(|| Program {
                name: program.name.clone(),
                dir: program.directory.clone(),
            }),
        })
    }
}

/// The program a launch's hook commands and statusline run, and the
/// directory its engine's `PATH` leads with (owner, 2026-11-18): the
/// CLIENT's, as its `Caller` named them, not the daemon's own executable.
/// Taken as sent: the client is the same user as the daemon (ruling 37's
/// trust boundary), so it can name nothing she could not run herself.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Program {
    /// Its file name, its executable suffix stripped (`ctxloom`).
    pub name: String,
    /// The directory it is in.
    pub dir: String,
}

/// A child's plan: its project's dry run, as a ctxloom process in that
/// project would make it, on `engine`, with `env` (its launcher's
/// environment). The error is the plan's code.
pub trait ChildPlans: Send + Sync {
    /// The plan for `agent` (empty: the project's default), the project's
    /// own project-only settings applied only when the user `trusted` it
    /// (step 8 U2; otherwise held back, and named in the plan).
    fn preview(
        &self,
        engine: &str,
        agent: &str,
        project_dir: &str,
        env: &BTreeMap<String, String>,
        trusted: bool,
    ) -> Result<Previewed, String>;

    /// The user's git identity in the project at `project_dir`, as git
    /// resolves it there in her own environment (`env`, her launcher's, and
    /// her home): what the child's session home is seeded with (owner
    /// ruling 112), as a CLI run's is. None when git knows neither part.
    fn git_user(&self, project_dir: &str, env: &BTreeMap<String, String>) -> Option<GitUser>;

    /// Where the project at `project_dir` keeps its sessions' outputs, as
    /// its config says (`output.dir`, against its root unless absolute;
    /// unsaid, `ctxloom/`), read as a ctxloom process there would read it
    /// with `env`: a child launched into it from another project keeps its
    /// outputs in `<this>/<harp>` (step 8 U2b). The error is the code of
    /// what could not be read.
    fn output_root(
        &self,
        project_dir: &str,
        env: &BTreeMap<String, String>,
    ) -> Result<String, String>;
}

/// The session directories of a child's project: the same port the one
/// launcher prepares every other session's home through
/// (`run_ports::SessionRecords`), so a hosted child's home is set up by the
/// same code.
pub trait ChildHomes: Send + Sync {
    /// The session directories of the project at `project_dir`.
    fn records(&self, project_dir: &str) -> Box<dyn SessionRecords>;

    /// Take the lock of session `harp` in the project at `project_dir`
    /// (`<project>/.ctxloom/sessions/<harp>/session.lock`, the platform's file lock,
    /// at once or not at all), held for the hosted child's life, so its
    /// session reads live and no sweep (a one-shot's launcher's included)
    /// takes it for lost while its host holds it. Why not, when it cannot
    /// be had (another holds it: the session runs elsewhere).
    fn hold_session(&self, project_dir: &str, harp: &str) -> Result<HeldLock, String>;
}

/// Where a hosted child works (owner s4-Q11): its agent's `workspace`, the
/// live project or a checkout of its session's own, placed by the one
/// launch through the project's workspaces (worktree-git's, under
/// ctxloom's home) and let go WIP-safe at the child's close and, for a
/// lost one, at the next start's sweep (OD-19).
pub trait ChildWorkspaces: Send + Sync {
    /// The workspaces of the project `project_id` at `project_dir`, their
    /// git run in `env`, the environment the child's launcher sent (its
    /// `GIT_*`, its `SSH_AUTH_SOCK`); none (a sweep, which no launcher
    /// asked), the daemon's own.
    fn of(
        &self,
        project_id: &str,
        project_dir: &str,
        env: Option<&std::collections::BTreeMap<String, String>>,
    ) -> Arc<dyn isolation_ports::Workspaces + Send + Sync>;
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

/// The daemon's monotonic clock (OD-54): how long since some fixed instant
/// of its own. The core reads no clock: the root's is its process's
/// `Instant`, a test's a fake it moves. What times a hand-over's period.
pub trait Monotonic: Send + Sync {
    fn now(&self) -> std::time::Duration;
}

/// THE ONE NAMED EXCEPTION to "the daemon never reads a hold" (OD-59 (2),
/// owner 2026-11-16, "ALLOW the check"; H-Q5 revised made the hold the
/// client side's to deliver): the start-up sweep delivers the hold of each
/// session whose run it is about to judge BEFORE it judges it, so a TUI
/// frontend that exited while no daemon served (its exit report held) has
/// its run ended as its report says, not `runner-lost`. It is a FLUSH, the
/// client side's own ([`runs_application::Flush::session`]): every record
/// of the session's hold this daemon is up to date for, in held order,
/// through the daemon's own edge, its delivered prefix cut; no second
/// reader of a hold's record exists. Nothing else of the daemon reads a
/// hold: not its rounds, not its API.
pub trait StartHolds: Send + Sync {
    /// Deliver what the hold of `session` (its stable id, hyphenated)
    /// holds, as an up-to-date client's flush of it delivers it.
    fn flush<'a>(
        &'a self,
        session: &'a str,
    ) -> std::pin::Pin<Box<dyn std::future::Future<Output = HoldFlushed> + Send + 'a>>;
}

/// What a start-up flush of one session's hold did.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct HoldFlushed {
    /// How many held records it delivered (applied, or a duplicate).
    pub delivered: usize,
    /// How many it set aside (finally refused or parked, or no fact of the
    /// hold's session): never delivered again.
    pub set_aside: usize,
    /// Why something stayed in the hold (a record of a newer ctxloom, a
    /// hold another process is taking, one that does not read, a delivery
    /// not settled); none when nothing is left.
    pub left: Option<String>,
}

impl HoldFlushed {
    /// Whether all the hold held reached the stream: nothing set aside and
    /// nothing left.
    pub fn whole(&self) -> bool {
        self.set_aside == 0 && self.left.is_none()
    }
}

#[cfg(test)]
#[path = "ports.test.rs"]
mod tests;

/// Where a distill run's recorded answer is written as the item's distilled
/// form, and what it came to recorded on the run (the root's, through the
/// daemon API). An error is why it was not recorded: the run is due again.
pub trait Distilling: Send + Sync {
    fn write(&self, due: daemon_board::DistillDue) -> run_ports::BoxFuture<'_, Result<(), String>>;
}
