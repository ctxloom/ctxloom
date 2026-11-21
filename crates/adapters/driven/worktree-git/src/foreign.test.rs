//! The project's foreign worktrees, over real git in temporary directories.
//! Go: `doctor_cmd_test.go`'s foreign-worktree rows (release/0.7).

use std::path::{Path, PathBuf};
use std::process::Command;

use ctxloom_ports::{ForeignListing, ForeignWorktree, ForeignWorktrees};

use super::{spelled, GitForeignWorktrees};
use crate::fixtures::{canonical, fs, link, mkdirs, put, root, runs_scripts};

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
    assert_eq!(listed.len(), 1, "{listed:?}");
    // The path is git's spelling (`C:/a/b` on Windows, where `stale` is
    // `C:\a\b`), so it is read as a path, which takes `/` and `\` alike there.
    assert_eq!(Path::new(&listed[0].path), stale, "{listed:?}");
    assert_eq!(
        listed,
        [ForeignWorktree {
            path: listed[0].path.clone(),
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

// The git it is given is the git it runs: one that fails reads no repository,
// where the PATH's git would have listed them.
#[test]
fn the_git_it_is_given_is_the_one_it_runs() {
    if !runs_scripts() {
        return;
    }
    let f = Fixture::new();
    let path = f.top.join("bin/git");
    mkdirs(f.top.join("bin"));
    fixture_program::install_program(&*fs(), &path, "#!/bin/sh\necho broken >&2\nexit 7\n")
        .unwrap();
    let git = crate::git::Git::new(
        fs(),
        path,
        std::time::Duration::from_secs(10),
        crate::fixtures::env(),
    );
    let listing = f
        .adapter()
        .with_git(git)
        .foreign(&f.project.display().to_string());
    assert_eq!(listing, ForeignListing::NotARepository);
}

// ctxloom's home named through a symlink is the same tree: a scratch checkout
// git lists by its resolved path is still ctxloom's, not foreign.
#[test]
fn a_scratch_checkout_under_a_symlinked_home_is_not_listed() {
    let f = Fixture::new();
    let linked = f.top.join("linked-home");
    link(&f.home, &linked);
    let scratch = isolation_ports::session_worktrees(&f.home, "calm-otter", "amber-quiet-heron")
        .join("reviewer");
    f.add("ctxloom/amber-quiet-heron/reviewer", &scratch);
    let adapter = GitForeignWorktrees::new(
        fs(),
        isolation_ports::scratch_root(&linked),
        crate::fixtures::env(),
    );
    match adapter.foreign(&f.project.display().to_string()) {
        ForeignListing::Listed(listed) => assert!(listed.is_empty(), "{listed:?}"),
        other => panic!("not listed: {other:?}"),
    }
}

// A worktree's path is shown as the platform spells a path: git lists one
// on Windows as `C:/a/b`, and the doctor row beside the project's `C:\a\b`
// would mix the two. Unix keeps git's spelling.
#[test]
fn a_listed_path_is_shown_with_the_platforms_separator() {
    let listed = Path::new("C:/Users/her/workspace/proj--stale-feature");
    assert_eq!(
        spelled(os::Flavor::Windows, listed),
        r"C:\Users\her\workspace\proj--stale-feature"
    );
    let unix = Path::new("/home/her/workspace/proj--stale-feature");
    assert_eq!(
        spelled(os::Flavor::Unix, unix),
        "/home/her/workspace/proj--stale-feature"
    );
}
