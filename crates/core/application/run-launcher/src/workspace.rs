//! The workspace a launch places its engine in (the workspace axis): the
//! live project, or a checkout of the session's own for its agent.
//!
//! The placement is one of the launch's setup steps, so it is made where
//! every other one is, the launcher's `prepare`, for every launch path
//! alike: a one-shot child's turn, the terminal session, a hosted child. Its
//! outcomes:
//!
//! | asked | the workspaces port says | the engine's directory |
//! |---|---|---|
//! | `none` | (not asked) | the project |
//! | `worktree` | a checkout | the checkout |
//! | `worktree` | unavailable (not a git repository, the add failed) | the project, and a warning |
//! | `worktree` | an error (an occupant it will not adopt, a git that hung) | none: the launch fails |
//!
//! A worktree is a convenience, not a boundary: one that cannot be had runs
//! the engine in the project, and says so.

use isolation_ports::{Checkout, Failure, Prepared, Request, Workspaces};

/// The workspace axis, as a run settles on it (`DryRun.axes.0`: the flag,
/// else the config's default, else `none`).
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum Axis {
    /// The live project directory.
    #[default]
    None,
    /// A git worktree of the session's own for its agent.
    Worktree,
}

impl Axis {
    /// The axis a word names: `worktree`, else the live project.
    pub fn of(word: &str) -> Axis {
        if word == "worktree" {
            Axis::Worktree
        } else {
            Axis::None
        }
    }

    /// The word for the axis, as the config and the session record say it.
    pub fn word(self) -> &'static str {
        match self {
            Axis::None => "none",
            Axis::Worktree => "worktree",
        }
    }
}

/// What a launch's order asks of the workspace axis.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Workspace {
    pub axis: Axis,
    /// The session's checkouts directory
    /// (`isolation_ports::session_worktrees`); empty where there is none.
    pub work_dir: String,
    /// The agent the checkout is for: it names the checkout and its branch.
    pub agent: String,
}

/// Where a launch placed its engine.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Placement {
    /// The engine's working directory.
    pub cwd: String,
    /// The checkout it works in; none in the live project.
    pub checkout: Option<Checkout>,
    /// Why a worktree asked for could not be had, and the engine works in
    /// the project instead.
    pub warning: Option<String>,
}

/// Place a launch: see the module's table. `project_dir` is the live
/// project, `work_dir` the session's checkouts directory.
pub fn place_workspace(
    axis: Axis,
    project_dir: &str,
    work_dir: &str,
    agent: &str,
    workspaces: &dyn Workspaces,
) -> Result<Placement, Failure> {
    let live = |warning: Option<String>| Placement {
        cwd: project_dir.to_string(),
        checkout: None,
        warning,
    };
    if axis == Axis::None {
        return Ok(live(None));
    }
    if work_dir.is_empty() {
        return Ok(live(Some(NO_WORK_DIR.to_string())));
    }
    let request = Request {
        project_dir: project_dir.to_string(),
        work_dir: work_dir.to_string(),
        agent: agent.to_string(),
        // Nothing is written into a checkout at a launch (the surfaces ride
        // the launch), so nothing needs hiding from git.
        artifact_patterns: Vec::new(),
    };
    Ok(match workspaces.prepare(&request)? {
        Prepared::Checkout(checkout) => Placement {
            cwd: checkout.path.clone(),
            checkout: Some(checkout),
            warning: None,
        },
        Prepared::Unavailable(why) => live(Some(why)),
    })
}

/// Why a worktree asked for of a session with no checkouts directory (a run
/// outside any project) cannot be had.
pub(crate) const NO_WORK_DIR: &str =
    "worktree isolation: the session has no checkouts directory (no project)";

/// Why a worktree asked for of a launcher that makes none cannot be had.
pub(crate) const NO_WORKSPACES: &str = "worktree isolation: this launcher makes no worktrees";

#[cfg(test)]
#[path = "workspace.test.rs"]
mod tests;
