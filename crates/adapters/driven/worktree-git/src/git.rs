//! Running git for the workspaces: git-cli's runner (the environment
//! scrubbed of the repository-location variables, non-interactive, bounded in
//! time with its whole process tree), long paths allowed, and a failure said
//! in the workspaces' words.

use std::fmt;
use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::Duration;

use file_system::{Fs, Kind};
use git_cli::{subcommand, Invocation, Runner};
use process_env::EnvSnapshot;
use run_within::RunError;

use crate::error::WorktreeError;

/// How long one git invocation may run: Go's `worktreeTeardownTimeout`,
/// `worktreeReapTimeout` and `upTimeout`, each thirty seconds, which bound
/// the same git calls (`isolation/worktree.go`, `worktree_reap.go`,
/// `operations/launch.go`).
pub const DEFAULT_TIMEOUT: Duration = Duration::from_secs(30);

/// The git a workspace runs: which program, and how long each invocation
/// has. The seam tests narrow to a stub program and a tiny limit.
#[derive(Clone, Debug)]
pub struct Git {
    fs: Arc<dyn Fs>,
    runner: Runner,
}

/// Why a git invocation did not succeed.
#[derive(Clone, PartialEq, Eq, Debug)]
pub enum GitError {
    /// Git's own account of its failure, or why it could not be run.
    Said(String),
    /// It did not finish within its limit; it was killed and reaped.
    TimedOut { command: String, timeout: Duration },
}

impl fmt::Display for GitError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            GitError::Said(said) => f.write_str(said),
            GitError::TimedOut { command, timeout } => write!(
                f,
                "git {command} did not finish within {} and was killed",
                duration_text::format_exact(*timeout)
            ),
        }
    }
}

impl GitError {
    /// The diagnostic of a git that timed out; none for any other failure.
    pub(crate) fn diagnostic(&self) -> Option<WorktreeError> {
        match self {
            GitError::Said(_) => None,
            GitError::TimedOut { command, timeout } => Some(WorktreeError::GitTimedOut {
                command: command.clone(),
                timeout: duration_text::format_exact(*timeout),
            }),
        }
    }
}

fn spawn_failure(e: &RunError, label: &str) -> String {
    match e {
        RunError::NotFound { .. } => "the git binary was not found on PATH".to_string(),
        other => format!("git {label}: {}", other.io()),
    }
}

impl Git {
    /// The git on PATH, each invocation bounded by [`DEFAULT_TIMEOUT`]; `fs`
    /// is the filesystem its working directories are on, `env` the
    /// environment it is run in.
    pub fn on(fs: Arc<dyn Fs>, env: Arc<EnvSnapshot>) -> Git {
        Git::new(fs, "git", DEFAULT_TIMEOUT, env)
    }

    /// `program` (a path, or a name looked up on PATH), each invocation
    /// bounded by `timeout`, run in `env`'s environment.
    pub fn new(
        fs: Arc<dyn Fs>,
        program: impl Into<PathBuf>,
        timeout: Duration,
        env: Arc<EnvSnapshot>,
    ) -> Git {
        Git {
            fs,
            runner: Runner::new(program, timeout, env),
        }
    }

    fn is_dir(&self, dir: &Path) -> bool {
        self.fs
            .metadata(dir)
            .is_ok_and(|meta| meta.kind == Kind::Dir)
    }

    /// `git args` in `dir`: stdout on success, git's own words on failure.
    pub(crate) fn run(&self, dir: &Path, args: &[&str]) -> Result<String, GitError> {
        let label = subcommand(args);
        // Paths past Windows' MAX_PATH (a deep repository's checkout); other
        // platforms ignore it, and no repository's config changes.
        let mut all = vec!["-c", "core.longpaths=true"];
        all.extend_from_slice(args);
        match self.runner.run(&Invocation::new(Some(dir), &all)) {
            Ok(ran) => Ok(String::from_utf8_lossy(&ran.stdout).into_owned()),
            // A spawn that finds nothing is the program missing or the
            // directory missing; only the directory can be checked here.
            Err(git_cli::Failure::Spawn(e)) => Err(if self.is_dir(dir) {
                GitError::Said(spawn_failure(&e, label))
            } else {
                GitError::Said(format!("git {label}: {} is not a directory", dir.display()))
            }),
            Err(git_cli::Failure::TimedOut(timeout)) => Err(GitError::TimedOut {
                command: label.to_string(),
                timeout,
            }),
            Err(git_cli::Failure::Exited { status, stderr, .. }) => {
                let said = String::from_utf8_lossy(&stderr);
                let said = said.trim();
                Err(GitError::Said(if said.is_empty() {
                    format!("git {label}: {status}")
                } else {
                    said.to_string()
                }))
            }
        }
    }
}

/// The identity git uses for commits in a directory: its `user.name` and
/// `user.email` as git resolves them there (the repository's, else the
/// user's global or system configuration, includes followed). Either is
/// empty when git knows none, or cannot be asked.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct User {
    pub name: String,
    pub email: String,
}

impl Git {
    /// The identity git would commit with in `dir` (owner ruling 112: a
    /// session home is seeded with it).
    pub fn user(&self, dir: &Path) -> User {
        let get = |key: &str| {
            self.run(dir, &["config", "--get", key])
                .map(|value| value.trim().to_string())
                .unwrap_or_default()
        };
        User {
            name: get("user.name"),
            email: get("user.email"),
        }
    }
}

/// A repository's identity as a project registers it (owner, 2026-11-12):
/// its git common directory (a worktree's is its primary checkout's) and
/// its root commit. Both are empty outside git.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct VcsIdentity {
    pub git_common_dir: String,
    /// `git rev-list --max-parents=0 HEAD`; of several roots (a merged
    /// history), the lexically least, so every client picks the same one.
    pub root_commit: String,
}

impl Git {
    /// The identity of the repository at `dir` (empty outside git, or in a
    /// repository with no commit yet: no root commit then).
    pub fn vcs_identity(&self, dir: &Path) -> VcsIdentity {
        let Ok(common) = self.run(
            dir,
            &["rev-parse", "--path-format=absolute", "--git-common-dir"],
        ) else {
            return VcsIdentity::default();
        };
        let root_commit = self
            .run(dir, &["rev-list", "--max-parents=0", "HEAD"])
            .ok()
            .and_then(|roots| {
                roots
                    .lines()
                    .map(str::trim)
                    .filter(|line| !line.is_empty())
                    .min()
                    .map(str::to_string)
            })
            .unwrap_or_default();
        VcsIdentity {
            git_common_dir: common.trim().to_string(),
            root_commit,
        }
    }
}

#[cfg(test)]
#[path = "git.test.rs"]
mod tests;
