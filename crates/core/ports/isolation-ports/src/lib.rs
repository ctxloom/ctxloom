//! The ports isolation needs from outside. Each is a trait a driven adapter
//! implements; nothing here does I/O.
//!
//! - [`Workspaces`]: prepare, list and reclaim the workspace axis's
//!   checkouts (`worktree-git`). A prepared workspace is a git worktree on
//!   the session's own branch for its agent, under ctxloom's home and
//!   outside the project ([`session_worktrees`]); the scratch checkouts a
//!   crashed run left behind are found, inspected and removed through it.
//! - [`ContainerRuntimes`]: the runtime axis's facts about this machine
//!   (`container-runtime`): whether this process is inside a container, which
//!   runtimes are installed and reachable.
//! - [`Backends`]: the engines `container` may be asked about.
//! - [`Companions`]: the tooling the registered companions declare.

use std::path::{Path, PathBuf};

use angzarr_diagnostic::Diagnostic;

/// Why an adapter could not do what it was asked: its own diagnostic.
pub type Failure = Box<dyn Diagnostic>;

/// Where ctxloom keeps one project's checkouts (owner ruling 2026-11-12,
/// Q1): under its home, `<home>/.ctxloom/worktrees/<project-id>`, outside
/// the project, so an engine in a checkout does not also read the project's
/// own `CLAUDE.md` above it, and nothing in the project moves under it.
/// `home` is the ctxloom home, the directory holding `.ctxloom`.
pub fn worktrees_root(home: &Path, project_id: &str) -> PathBuf {
    scratch_root(home).join(project_id)
}

/// Where ctxloom keeps every project's checkouts, `<home>/.ctxloom/worktrees`:
/// the whole tree is ctxloom's, so a worktree under it is never a person's.
pub fn scratch_root(home: &Path) -> PathBuf {
    home.join(".ctxloom").join("worktrees")
}

/// One session's checkouts: `<worktrees_root>/<harp>`, a [`Request`]'s
/// `work_dir`; its agent's checkout is `<that>/<agent>`.
pub fn session_worktrees(home: &Path, project_id: &str, harp: &str) -> PathBuf {
    worktrees_root(home, project_id).join(harp)
}

/// What a workspace is asked for.
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct Request {
    /// The project's directory (a git repository, or not).
    pub project_dir: String,
    /// The session's checkouts directory, [`session_worktrees`]: under
    /// ctxloom's home, `<home>/.ctxloom/worktrees/<project-id>/<harp>`,
    /// never under the project. The agent's checkout is `<work_dir>/<agent>`.
    pub work_dir: String,
    /// The agent the checkout is for; it names the checkout and its branch.
    pub agent: String,
    /// The paths ctxloom writes into a working tree, hidden from the
    /// project's git status by its shared exclude file.
    pub artifact_patterns: Vec<String>,
}

/// A checkout prepared for an agent.
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct Checkout {
    pub path: String,
    /// The session's own branch for its agent (`ctxloom/<harp>/<agent>`).
    pub branch: String,
    /// The repository it was cut from.
    pub repo_dir: String,
    /// Whether this is an earlier run's own checkout, re-entered in place.
    pub reused: bool,
}

/// What preparing a workspace came to.
#[derive(Clone, PartialEq, Eq, Debug)]
pub enum Prepared {
    Checkout(Checkout),
    /// No worktree could be had (the project is not a git repository, or the
    /// add failed): the run goes ahead in the live directory, and the reason
    /// is a warning. A worktree is a convenience, not a boundary.
    Unavailable(String),
}

/// One scratch checkout under a session's checkouts directory.
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct Scratch {
    pub harp: String,
    pub path: String,
}

/// Whether a checkout holds anything worth keeping.
#[derive(Clone, PartialEq, Eq, Debug)]
pub enum Safety {
    Clean,
    /// Uncommitted changes, gitignored files, or a state git would not
    /// answer for: why. An unreadable state is never read as clean.
    Unsafe(String),
}

/// What a checkout is, asked of git without changing anything.
#[derive(Clone, PartialEq, Eq, Debug)]
pub enum Standing {
    /// The repository it belongs to could not be resolved: why.
    Unresolved(String),
    Resolved {
        repo_dir: String,
        safety: Safety,
    },
}

/// What removing a checkout came to.
#[derive(Clone, PartialEq, Eq, Debug)]
pub enum Removal {
    Removed,
    /// Left in place, and why. Removal is never forced.
    Left(String),
}

/// The workspace axis.
pub trait Workspaces {
    /// A worktree for `request.agent` on its own branch in `request.work_dir`,
    /// or the reason none is available. An earlier run's own checkout at the
    /// agent's path is re-entered; anything else at that path is a refusal
    /// (the `Err`), never a degrade and never adopted.
    fn prepare(&self, request: &Request) -> Result<Prepared, Failure>;

    /// Whether the project holds session `harp` at all: its directory, or
    /// the lock its state is read from.
    fn session_known(&self, harp: &str) -> Result<bool, Failure>;

    /// The scratch checkouts under the project's sessions' checkouts
    /// directories ([`worktrees_root`]): one session's, or every session's.
    fn scratch(&self, harp: Option<&str>) -> Result<Vec<Scratch>, Failure>;

    /// Write `files` (each a path relative to the checkout and its bytes)
    /// into the checkout at `checkout`, its directories made: what a launch
    /// carries that its engine reads only from where it works. A path that
    /// is not plainly relative is refused, and nothing outside it written.
    fn place_files(&self, checkout: &str, files: &[(String, Vec<u8>)]) -> Result<(), Failure>;

    /// Take out of the checkout at `checkout` the files at `rels` that a
    /// launch placed there, best effort, so they do not keep it from being
    /// removed (git counts an excluded file as work).
    fn remove_files(&self, checkout: &str, rels: &[String]);

    /// What `path` is, changing nothing.
    fn inspect(&self, path: &str) -> Standing;

    /// Remove the checkout WIP-safely (nested worktrees first, each checked;
    /// never forced), and say whether it is gone.
    fn remove(&self, repo_dir: &str, path: &str) -> Removal;
}

/// A container runtime installed on this machine.
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct RuntimeFacts {
    /// `docker` or `podman`.
    pub name: String,
    /// Whether its daemon answered.
    pub reachable: bool,
}

/// The names ctxloom knows container runtimes by.
pub const RUNTIMES: [&str; 2] = ["docker", "podman"];

/// The runtime axis's facts about this machine.
pub trait ContainerRuntimes {
    /// Why this process looks to be inside a container (the markers that
    /// matched); empty when it is not.
    fn in_container(&self) -> Vec<String>;

    /// The runtimes installed, in preference order, each probed.
    fn installed(&self) -> Vec<RuntimeFacts>;

    /// Whether `image` exists locally under `runtime`.
    fn image_present(&self, runtime: &str, image: &str) -> bool;
}

/// The engines a `container` command may be asked about.
pub trait Backends {
    /// Every engine's name.
    fn names(&self) -> Vec<String>;

    /// The engine a bare command means: the configured default, else the
    /// first engine. None when there are no engines.
    fn default_backend(&self) -> Option<String>;
}

/// One companion's declaration of the tools its content needs in the agent
/// image.
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct Tooling {
    /// The companion's ref, so every proposed change traces to it.
    pub source: String,
    pub content: String,
}

/// The registered companions' declarations. Only a REGISTERED companion is
/// ever run; one nobody registered declares nothing.
pub trait Companions {
    fn tooling(&self) -> Result<Vec<Tooling>, Failure>;
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
