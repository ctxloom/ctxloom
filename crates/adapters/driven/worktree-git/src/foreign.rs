//! The project's worktrees ctxloom did not create (j001300's doctor row):
//! every worktree git lists for the project but its own checkout and those
//! under ctxloom's scratch tree (`isolation_ports::scratch_root`), each with
//! its branch's merge state and its dirty state as git answers them now.
//! Read-only: nothing here removes a worktree. Go:
//! `doctorCheckForeignWorktrees`.

use std::collections::BTreeSet;
use std::path::{Path, PathBuf};
use std::sync::Arc;

use ctxloom_ports::{ForeignListing, ForeignWorktree, ForeignWorktrees};
use file_system::Fs;
use process_env::EnvSnapshot;

use crate::git::Git;
use crate::workspace::{is_repo, same_path, worktree_list};

/// The foreign worktrees of a project, over the git command line.
pub struct GitForeignWorktrees {
    fs: Arc<dyn Fs>,
    git: Git,
    /// ctxloom's scratch tree: a worktree under it is ctxloom's.
    scratch: PathBuf,
}

impl GitForeignWorktrees {
    /// Over the git run in `env`'s environment, ctxloom's checkouts being
    /// under `scratch` (`isolation_ports::scratch_root`).
    pub fn new(fs: Arc<dyn Fs>, scratch: impl Into<PathBuf>, env: Arc<EnvSnapshot>) -> Self {
        GitForeignWorktrees {
            git: Git::on(Arc::clone(&fs), env),
            fs,
            scratch: scratch.into(),
        }
    }

    /// The same over another git.
    pub fn with_git(self, git: Git) -> Self {
        GitForeignWorktrees { git, ..self }
    }

    /// Whether `path` is under ctxloom's scratch tree (as resolved, so a
    /// symlinked home is the same tree).
    fn is_scratch(&self, path: &Path) -> bool {
        let resolve = |p: &Path| self.fs.canonicalize(p).unwrap_or_else(|_| p.to_path_buf());
        path.starts_with(&self.scratch) || resolve(path).starts_with(resolve(&self.scratch))
    }

    /// The local branches merged into the project's HEAD; none when git
    /// would not say.
    fn merged(&self, project: &Path) -> Option<BTreeSet<String>> {
        let out = self
            .git
            .run(
                project,
                &["branch", "--merged", "HEAD", "--format=%(refname:short)"],
            )
            .ok()?;
        Some(out.lines().map(str::trim).map(str::to_string).collect())
    }

    /// Whether `dir` holds uncommitted changes; none when git would not say.
    fn dirty(&self, dir: &Path) -> Option<bool> {
        self.git
            .run(dir, &["--no-optional-locks", "status", "--porcelain"])
            .ok()
            .map(|out| !out.trim().is_empty())
    }
}

impl ForeignWorktrees for GitForeignWorktrees {
    fn foreign(&self, project_dir: &str) -> ForeignListing {
        let project = Path::new(project_dir);
        match is_repo(&self.git, project) {
            Ok(true) => {}
            Ok(false) => return ForeignListing::NotARepository,
            Err(error) => return ForeignListing::Unlisted(error.to_string()),
        }
        let listed = match worktree_list(&self.git, project) {
            Ok(listed) => listed,
            Err(error) => return ForeignListing::Unlisted(error.to_string()),
        };
        // git lists the main checkout first; the project may be a linked
        // worktree itself, so it is skipped by path too.
        let main = listed.first().map(|w| w.path.clone());
        let merged = self.merged(project);
        let mut foreign: Vec<ForeignWorktree> = listed
            .iter()
            .filter(|w| Some(&w.path) != main.as_ref())
            .filter(|w| !same_path(self.fs.as_ref(), &w.path, project))
            .filter(|w| !self.is_scratch(&w.path))
            .map(|w| {
                let branch = w.branch.clone().unwrap_or_default();
                ForeignWorktree {
                    path: w.path.display().to_string(),
                    merged: (!branch.is_empty())
                        .then(|| merged.as_ref().map(|set| set.contains(&branch)))
                        .flatten(),
                    dirty: self.dirty(&w.path),
                    branch,
                }
            })
            .collect();
        foreign.sort_by(|a, b| a.path.cmp(&b.path));
        ForeignListing::Listed(foreign)
    }
}

#[cfg(test)]
#[path = "foreign.test.rs"]
mod tests;
