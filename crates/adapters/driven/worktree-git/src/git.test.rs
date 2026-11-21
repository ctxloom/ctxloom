//! Running git: what a failure says in the workspaces' words (the runner's
//! own scrub and bounds are git-cli's tests).

use std::path::Path;
use std::time::Duration;

use super::{Git, GitError, User, VcsIdentity, DEFAULT_TIMEOUT};
use crate::fixtures::{env, fs, put, root, runs_scripts};

/// A "git" that is a script, in a directory of its own.
fn stub(body: &str) -> (file_system::Rooted, Git, Duration) {
    let dir = root();
    let path = dir.root().join("git");
    fixture_program::install_program(&*fs(), &path, body).unwrap();
    let tiny = Duration::from_millis(20);
    (dir, Git::new(fs(), path, tiny, env()), tiny)
}

fn run(dir: &Path, args: &[&str]) -> Result<String, String> {
    Git::on(fs(), env())
        .run(dir, args)
        .map_err(|e| e.to_string())
}

#[test]
fn a_silent_failure_is_named_by_the_command_not_its_flags() {
    let dir = root();
    put(dir.root().join("a"), "1");
    put(dir.root().join("b"), "2");
    let args = ["diff", "--no-index", "--quiet", "a", "b"];
    let said = run(dir.root(), &args).unwrap_err();
    // git's status as the host says it (`exit status: 1`; `exit code: 1` on
    // Windows).
    let status = std::process::Command::new("git")
        .args(args)
        .current_dir(dir.root())
        .status()
        .expect("git runs");
    assert_eq!(status.code(), Some(1));
    assert_eq!(said, format!("git diff: {status}"));
}

#[test]
fn a_directory_that_cannot_be_entered_is_told_apart_from_a_missing_git() {
    let dir = root();
    let file = dir.root().join("file");
    put(&file, "x");
    let said = run(&file, &["status"]).unwrap_err();
    assert!(said.starts_with("git status: "), "{said}");
    let gone = run(&dir.root().join("gone"), &["status"]).unwrap_err();
    assert!(gone.starts_with("git status: "), "{gone}");
    assert!(
        !gone.contains("binary"),
        "a missing directory is not a missing git: {gone}"
    );
    assert!(gone.contains("gone"), "{gone}");
}

#[test]
fn a_missing_git_is_named_as_one_whatever_the_directory() {
    let dir = root();
    let git = Git::new(fs(), dir.root().join("no-such-git"), DEFAULT_TIMEOUT, env());
    let said = git.run(dir.root(), &["status"]).unwrap_err().to_string();
    assert_eq!(said, "the git binary was not found on PATH");
}

// S11 hardening: a git that hangs is killed at its limit, not waited on.
#[test]
fn a_git_that_outlives_its_limit_is_killed_reaped_and_named() {
    if !runs_scripts() {
        return;
    }
    let (dir, git, tiny) = stub("#!/bin/sh\nexec sleep 30\n");
    let error = git.run(dir.root(), &["--no-pager", "status"]).unwrap_err();
    let GitError::TimedOut { command, timeout } = &error else {
        panic!("not a timeout: {error:?}");
    };
    assert_eq!(command, "status", "named by the command, not its flags");
    assert_eq!(*timeout, tiny);
    assert_eq!(
        error.to_string(),
        "git status did not finish within 20ms and was killed"
    );
}

#[test]
fn a_timeout_is_a_diagnostic_and_a_failure_of_git_is_not() {
    if !runs_scripts() {
        return;
    }
    let (dir, git, _) = stub("#!/bin/sh\nexec sleep 30\n");
    let timed_out = git.run(dir.root(), &["status"]).unwrap_err();
    let diagnostic = timed_out.diagnostic().expect("a diagnostic");
    assert_eq!(
        crate::WorktreeError::GitTimedOut {
            command: "status".to_string(),
            timeout: "20ms".to_string()
        },
        diagnostic
    );
    assert_eq!(GitError::Said("x".to_string()).diagnostic(), None);
}

#[test]
fn a_git_that_finishes_within_its_limit_is_not_disturbed() {
    if !runs_scripts() {
        return;
    }
    // Output larger than a pipe's buffer, then a failure's own words.
    let (dir, _, _) = stub("#!/bin/sh\nif [ \"$3\" = fail ]; then echo oops >&2; exit 3; fi\nhead -c 200000 /dev/zero | tr '\\0' x\n");
    let git = Git::new(fs(), dir.root().join("git"), Duration::from_secs(60), env());
    let out = git.run(dir.root(), &["big"]).unwrap();
    assert_eq!(out.len(), 200_000);
    let said = git.run(dir.root(), &["fail"]).unwrap_err();
    assert_eq!(said, GitError::Said("oops".to_string()));
}

// Every invocation lets git use paths past Windows' MAX_PATH: a deep
// repository's `worktree add` would otherwise fail, and the run quietly use
// the live project. Other platforms ignore it; no repository config changes.
#[test]
fn every_git_invocation_allows_long_paths() {
    if !runs_scripts() {
        return;
    }
    let (dir, _, _) = stub("#!/bin/sh\nprintf '%s\\n' \"$@\"\n");
    let git = Git::new(fs(), dir.root().join("git"), Duration::from_secs(60), env());
    let out = git.run(dir.root(), &["worktree", "list"]).unwrap();
    assert_eq!(out, "-c\ncore.longpaths=true\nworktree\nlist\n");
}

#[test]
fn the_default_limit_is_gos_thirty_seconds() {
    assert_eq!(DEFAULT_TIMEOUT, Duration::from_secs(30));
}

// Owner ruling 112: the identity a session home is seeded with is git's own
// answer for the directory, asked one key at a time.
#[test]
fn the_user_is_gits_name_and_email_for_the_directory() {
    if !runs_scripts() {
        return;
    }
    let (dir, git, _) = stub(
        r#"case "$*" in
  "-c core.longpaths=true config --get user.name") echo "Alice Example" ;;
  "-c core.longpaths=true config --get user.email") echo "alice@example.com" ;;
  *) exit 1 ;;
esac"#,
    );
    // The stub's tiny limit is for the timeout tests: this one answers.
    let _ = git;
    let git = Git::new(fs(), dir.root().join("git"), DEFAULT_TIMEOUT, env());
    assert_eq!(
        git.user(dir.root()),
        User {
            name: "Alice Example".to_string(),
            email: "alice@example.com".to_string(),
        }
    );
}

#[test]
fn a_key_git_does_not_know_is_empty() {
    if !runs_scripts() {
        return;
    }
    let (dir, _, _) = stub(
        r#"case "$*" in
  "-c core.longpaths=true config --get user.email") echo "a@x" ;;
  *) exit 1 ;;
esac"#,
    );
    let git = Git::new(fs(), dir.root().join("git"), DEFAULT_TIMEOUT, env());
    assert_eq!(
        git.user(dir.root()),
        User {
            name: String::new(),
            email: "a@x".to_string(),
        }
    );
}

// Project registration (owner, 2026-11-12): a repository's common directory
// and its root commit; of several roots, the lexically least.
#[test]
fn a_repositorys_identity_is_its_common_dir_and_its_least_root_commit() {
    if !runs_scripts() {
        return;
    }
    let (dir, _, _) = stub(
        r#"case "$*" in
  "-c core.longpaths=true rev-parse --path-format=absolute --git-common-dir") echo "/work/calm/.git" ;;
  "-c core.longpaths=true rev-list --max-parents=0 HEAD") printf 'bbbb\naaaa\n' ;;
  *) exit 1 ;;
esac"#,
    );
    let git = Git::new(fs(), dir.root().join("git"), DEFAULT_TIMEOUT, env());
    assert_eq!(
        git.vcs_identity(dir.root()),
        VcsIdentity {
            git_common_dir: "/work/calm/.git".to_string(),
            root_commit: "aaaa".to_string(),
        }
    );
}

// Outside git: an empty identity; a repository with no commit, no root.
#[test]
fn outside_git_the_identity_is_empty() {
    if !runs_scripts() {
        return;
    }
    let (dir, _, _) = stub("exit 128");
    let git = Git::new(fs(), dir.root().join("git"), DEFAULT_TIMEOUT, env());
    assert_eq!(git.vcs_identity(dir.root()), VcsIdentity::default());
    let (dir, _, _) = stub(
        r#"case "$*" in
  rev-parse*) echo "/work/new/.git" ;;
  *) exit 128 ;;
esac"#,
    );
    let git = Git::new(fs(), dir.root().join("git"), DEFAULT_TIMEOUT, env());
    assert_eq!(git.vcs_identity(dir.root()).root_commit, "");
}
