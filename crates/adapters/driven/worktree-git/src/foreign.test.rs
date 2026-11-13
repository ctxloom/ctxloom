//! The project's foreign worktrees, over real git in temporary directories.
//! Go: `doctor_cmd_test.go`'s foreign-worktree rows (release/0.7).

use std::path::{Path, PathBuf};
use std::process::Command;

use ctxloom_ports::{ForeignListing, ForeignWorktree, ForeignWorktrees};

use super::GitForeignWorktrees;
use crate::fixtures::{canonical, fs, mkdirs, put, root};

/// git with a clean identity and none of the caller's environment.
fn git(dir: &Path, args: &[&str]) -> String {
    let output = Command::new("git")
        .args(["-c", "user.name=Test", "-c", "user.email=test@example.com"])
        .args([
            "-c",
            "commit.gpgsign=false",
            "-c",
            "init.defaultBranch=main",
        ])
        .args(args)
        .current_dir(dir)
        .env_clear()
        .env(
            "PATH",
            crate::fixtures::env().var("PATH").unwrap_or_default(),
        )
        .env("HOME", dir)
        .output()
        .expect("git runs");
    assert!(
        output.status.success(),
        "git {args:?}: {}",
        String::from_utf8_lossy(&output.stderr)
    );
    String::from_utf8_lossy(&output.stdout).into_owned()
}

/// A ctxloom home and a git-backed project beside it.
struct Fixture {
    _root: file_system::Rooted,
    top: PathBuf,
    home: PathBuf,
    project: PathBuf,
}

impl Fixture {
    fn new() -> Fixture {
        let root = root();
        let top = canonical(root.root());
        let home = top.join("home");
        let project = top.join("project");
        mkdirs(&home);
        mkdirs(&project);
        git(&project, &["init", "-q"]);
        put(project.join("README.md"), "hello\n");
        git(&project, &["add", "-A"]);
        git(&project, &["commit", "-q", "-m", "initial"]);
        Fixture {
            _root: root,
            top,
            home,
            project,
        }
    }

    fn adapter(&self) -> GitForeignWorktrees {
        GitForeignWorktrees::new(
            fs(),
            isolation_ports::scratch_root(&self.home),
            crate::fixtures::env(),
        )
    }

    /// A worktree on a new branch `branch` at `path`.
    fn add(&self, branch: &str, path: &Path) {
        mkdirs(path.parent().expect("a parent"));
        git(
            &self.project,
            &[
                "worktree",
                "add",
                "-q",
                "-b",
                branch,
                &path.display().to_string(),
            ],
        );
    }

    fn listed(&self) -> Vec<ForeignWorktree> {
        match self.adapter().foreign(&self.project.display().to_string()) {
            ForeignListing::Listed(listed) => listed,
            other => panic!("not listed: {other:?}"),
        }
    }
}

// j001300: her long-lived worktree with unmerged, uncommitted work is foreign,
// unmerged and dirty; the project's own checkout and ctxloom's scratch
// checkouts (any project's) are not listed at all.
#[test]
fn her_own_worktree_is_listed_unmerged_and_dirty_and_ctxlooms_are_not() {
    let f = Fixture::new();
    let stale = f.top.join("workspace/worktrees/proj--stale-feature");
    f.add("stale-feature", &stale);
    put(stale.join("feature.txt"), "unmerged work\n");
    git(&stale, &["add", "-A"]);
    git(&stale, &["commit", "-q", "-m", "unmerged"]);
    put(stale.join("uncommitted.txt"), "wip\n");
    let scratch = isolation_ports::session_worktrees(&f.home, "calm-otter", "amber-quiet-heron")
        .join("reviewer");
    f.add("ctxloom/amber-quiet-heron/reviewer", &scratch);
    let listed = f.listed();
    assert_eq!(
        listed,
        [ForeignWorktree {
            path: stale.display().to_string(),
            branch: "stale-feature".to_string(),
            merged: Some(false),
            dirty: Some(true),
        }]
    );
}

// A clean worktree whose branch HEAD already holds is merged and clean; a
// detached one has no branch and so no merge state.
#[test]
fn a_merged_clean_worktree_and_a_detached_one_are_said_as_they_are() {
    let f = Fixture::new();
    let done = f.top.join("done");
    f.add("done", &done);
    let detached = f.top.join("detached");
    git(
        &f.project,
        &[
            "worktree",
            "add",
            "-q",
            "--detach",
            &detached.display().to_string(),
        ],
    );
    let listed = f.listed();
    assert_eq!(listed.len(), 2, "{listed:?}");
    let by = |path: &Path| {
        listed
            .iter()
            .find(|w| Path::new(&w.path) == path)
            .unwrap_or_else(|| panic!("{} not listed: {listed:?}", path.display()))
    };
    assert_eq!(by(&done).branch, "done");
    assert_eq!(by(&done).merged, Some(true));
    assert_eq!(by(&done).dirty, Some(false));
    assert_eq!(by(&detached).branch, "");
    assert_eq!(by(&detached).merged, None);
}

#[test]
fn a_project_with_no_other_worktree_lists_none() {
    let f = Fixture::new();
    assert!(f.listed().is_empty());
}

#[test]
fn a_directory_that_is_no_repository_is_said_so() {
    let f = Fixture::new();
    let plain = f.top.join("plain");
    mkdirs(&plain);
    assert_eq!(
        f.adapter().foreign(&plain.display().to_string()),
        ForeignListing::NotARepository
    );
}
