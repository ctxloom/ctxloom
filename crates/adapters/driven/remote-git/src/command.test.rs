//! Running git under a time limit: a git that hangs is killed and reaped, and
//! the failure says so in the remotes' words (the runner's own bounds are
//! git-cli's tests).

use std::time::Duration;

use super::{Failed, Git, DEFAULT_TIMEOUT};
use crate::auth::Credential;
use crate::fixtures::{env, fs, root, runs_scripts};

const TINY: Duration = Duration::from_millis(20);

/// A "git" that is a script, in a directory of its own, run by `sh` and
/// never exec'd as written (`fixture_program`).
pub(crate) fn stub(body: &str) -> (file_system::Rooted, Git) {
    let dir = root();
    let path = dir.root().join("git");
    fixture_program::install_program(&*fs(), &path, body).unwrap();
    let git = Git::new(&path, TINY, env());
    (dir, git)
}

pub(crate) const HANGS: &str = "#!/bin/sh\nexec sleep 30\n";

#[test]
fn a_git_that_outlives_its_limit_is_killed_reaped_and_named() {
    if !runs_scripts() {
        return;
    }
    let (dir, git) = stub(HANGS);
    let why = git
        .run(Some(dir.root()), &["fetch", "--prune"], None)
        .unwrap_err();
    assert_eq!(why, "git fetch: did not finish within 20ms and was killed");
}

#[test]
fn a_timeout_is_told_apart_from_a_failure() {
    if !runs_scripts() {
        return;
    }
    let (dir, git) = stub(HANGS);
    let none = Credential::none();
    assert!(matches!(
        git.attempt(Some(dir.root()), &["status"], None, &none),
        Err(Failed::TimedOut(_))
    ));
    // A failure is judged under a deadline it cannot meet by accident: TINY
    // is the hang's deadline, and a loaded host can take longer than it just
    // to start the script, which then reads as a timeout.
    let (dir, _) = stub("#!/bin/sh\necho no >&2\nexit 1\n");
    let git = Git::new(dir.root().join("git"), Duration::from_secs(30), env());
    assert!(matches!(
        git.attempt(Some(dir.root()), &["status"], None, &none),
        Err(Failed::Said(why)) if why == "git status: no"
    ));
}

#[test]
fn a_git_that_finishes_within_its_limit_is_not_disturbed() {
    if !runs_scripts() {
        return;
    }
    // Output larger than a pipe's buffer, and input fed to it.
    let (dir, _) = stub("#!/bin/sh\ncat\nhead -c 200000 /dev/zero | tr '\\0' x\n");
    let git = Git::new(dir.root().join("git"), Duration::from_secs(60), env());
    let out = git
        .run(Some(dir.root()), &["big"], Some(b"fed:".as_slice()))
        .unwrap();
    assert_eq!(out.len(), 200_004);
    assert!(out.starts_with(b"fed:"));
}

#[test]
fn a_missing_git_is_named_as_one() {
    let dir = root();
    let git = Git::new(dir.root().join("no-such-git"), DEFAULT_TIMEOUT, env());
    let why = git.run(Some(dir.root()), &["status"], None).unwrap_err();
    assert!(why.starts_with("the git binary was not found"), "{why}");
}

#[test]
fn the_default_limit_is_gos_sixty_second_sync() {
    assert_eq!(DEFAULT_TIMEOUT, Duration::from_secs(60));
}

#[test]
fn a_git_that_cannot_be_started_for_another_reason_is_not_called_missing() {
    // A directory is found but cannot be executed.
    let dir = root();
    let git = Git::new(dir.root(), DEFAULT_TIMEOUT, env());
    let why = git.run(Some(dir.root()), &["status"], None).unwrap_err();
    assert!(why.starts_with("git status: "), "{why}");
    assert!(!why.contains("not found"), "{why}");
}

#[test]
fn a_silent_failure_is_named_by_its_status_and_a_spoken_one_has_its_secret_blotted_out() {
    if !runs_scripts() {
        return;
    }
    let (dir, _) = stub(
        "#!/bin/sh\nif [ \"$1\" = speak ]; then echo \"bad $GIT_CONFIG_VALUE_0\" >&2; fi\nexit 2\n",
    );
    let git = Git::new(dir.root().join("git"), Duration::from_secs(30), env());
    let silent = git.run(Some(dir.root()), &["quiet"], None).unwrap_err();
    assert!(silent.starts_with("git quiet: "), "{silent}");
    assert!(silent.contains('2'), "the status: {silent}");
    let credential = crate::auth::Auth::new(
        std::sync::Arc::new(|_| Some("s3cret".to_string())),
        std::sync::Arc::new(|_| vec!["TOKEN".to_string()]),
    )
    .credential("https://example.test/o/r");
    let spoken = git
        .run_with(Some(dir.root()), &["speak"], None, &credential)
        .unwrap_err();
    assert_eq!(spoken, "git speak: bad AUTHORIZATION: basic ***");
}
