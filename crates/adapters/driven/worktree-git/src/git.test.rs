//! Running git: which variables are scrubbed, and what a failure says.

use std::path::Path;
use std::time::{Duration, Instant};

use super::{environment, is_local, local_variables, Git, GitError, User, DEFAULT_TIMEOUT};
use crate::fixtures::{env, exists, fs, put, root, runs_scripts, slurp};

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

fn names(list: &[&str]) -> Option<Vec<String>> {
    Some(list.iter().map(ToString::to_string).collect())
}

#[test]
fn git_names_its_repository_local_variables() {
    let local = local_variables().as_ref().expect("git answers");
    assert!(local.iter().any(|n| n == "GIT_DIR"), "{local:?}");
}

#[test]
fn a_variable_git_calls_local_is_scrubbed_and_others_are_not() {
    let local = names(&["GIT_DIR"]);
    assert!(is_local("GIT_DIR", &local));
    assert!(!is_local("HOME", &local));
    assert!(!is_local("GIT_SSH", &local), "only what git listed");
}

#[test]
fn when_git_cannot_be_asked_every_git_variable_is_scrubbed() {
    assert!(is_local("GIT_INDEX_FILE", &None));
    assert!(!is_local("HOME", &None));
}

#[test]
fn indexed_config_variables_are_scrubbed_only_with_their_count() {
    let counted = names(&["GIT_CONFIG_COUNT"]);
    assert!(is_local("GIT_CONFIG_KEY_0", &counted));
    assert!(is_local("GIT_CONFIG_VALUE_3", &counted));
    assert!(!is_local("GIT_CONFIG_OTHER", &counted));
    assert!(!is_local("FOO", &counted));
    assert!(!is_local("GIT_CONFIG_KEY_0", &names(&["GIT_DIR"])));
}

#[test]
fn the_environment_keeps_the_callers_and_never_prompts() {
    let given =
        process_env::EnvSnapshot::of([("PATH", "/bin"), ("FOO", "bar"), ("GIT_DIR", "/elsewhere")]);
    let env = environment(&given);
    let has = |k: &str, v: &str| env.iter().any(|(key, val)| key == k && val == v);
    assert!(has("PATH", "/bin"));
    assert!(has("FOO", "bar"));
    assert!(!env.iter().any(|(k, _)| k == "GIT_DIR"));
    assert!(has("GIT_TERMINAL_PROMPT", "0"));
    assert!(has("GIT_ASKPASS", ""));
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
fn a_hook_the_git_started_is_killed_with_it() {
    if !runs_scripts() || !exists("/proc/self") {
        return;
    }
    // The hook holds git's pipes, as a real one would.
    let (dir, _, _) = stub("#!/bin/sh\nsleep 30 &\necho $! > \"$(dirname \"$0\")/hook\"\nwait\n");
    // A loaded machine may not have started the hook by the first limit:
    // ask again with a longer one until it has.
    let started = Instant::now();
    for secs in [1, 5, 25] {
        let git = Git::new(
            fs(),
            dir.root().join("git"),
            Duration::from_secs(secs),
            env(),
        );
        let error = git.run(dir.root(), &["checkout"]).unwrap_err();
        assert!(matches!(error, GitError::TimedOut { .. }), "{error:?}");
        if exists(dir.root().join("hook")) {
            break;
        }
    }
    assert!(started.elapsed() < Duration::from_secs(40));
    let pid = slurp(dir.root().join("hook"));
    assert!(gone_soon(pid.trim()), "the hook {pid} outlived the git");
}

/// Whether the killed process `pid` is gone within ten seconds. A run past
/// its limit sends SIGKILL to the program's whole process group and reaps the
/// program, not its descendants: a helper dies when the kernel next runs it,
/// which can be just after the run returned (it was caught in state `R`), so
/// one look at that instant can see a helper that is already doomed. The
/// bounded look is forced in taskloom-cli-client's
/// `a_helper_seen_alive_before_its_kill_lands_is_gone_within_the_bound`.
fn gone_soon(pid: &str) -> bool {
    let until = std::time::Instant::now() + std::time::Duration::from_secs(10);
    while !run_within::process_gone(pid) {
        if std::time::Instant::now() >= until {
            return false;
        }
        std::thread::sleep(std::time::Duration::from_millis(1));
    }
    true
}

/// A hook that holds git's pipes after git exits, until the file `release`
/// appears beside the script (or 30 seconds, its own bound), its pid in
/// `hook`. A git that exits normally is not killed with what it started
/// (run-within: the descendant may be meant to stay), so the test lets it go
/// itself.
const HELD: &str = "d=$(dirname \"$0\")\n\
    ( i=0; while [ ! -e \"$d/release\" ] && [ \"$i\" -lt 3000 ]; do sleep 0.01; i=$((i+1)); done ) &\n\
    echo $! > \"$d/hook\"\n";

/// Lets a [`HELD`] hook in this directory go when dropped, whether the test
/// passed or not.
struct Release(std::path::PathBuf);

impl Drop for Release {
    fn drop(&mut self) {
        let _ = fs().write_atomic(
            &self.0.join("release"),
            b"",
            file_system::Perms::Exact(os::Mode::FILE),
        );
    }
}

#[test]
fn a_hook_that_outlives_a_finished_git_does_not_hold_the_caller() {
    if !runs_scripts() {
        return;
    }
    let (dir, _, _) = stub(&format!("#!/bin/sh\n{HELD}echo done\n"));
    let release = Release(dir.root().to_path_buf());
    let git = Git::new(fs(), dir.root().join("git"), Duration::from_secs(30), env())
        .drain_grace(Duration::from_millis(50));
    let started = Instant::now();
    assert_eq!(git.run(dir.root(), &["gc"]).unwrap(), "done\n");
    assert!(started.elapsed() < Duration::from_secs(4));
    let pid = slurp(dir.root().join("hook"));
    assert!(
        !run_within::process_gone(pid.trim()),
        "the hook held the pipes"
    );
    drop(release);
    assert!(gone_soon(pid.trim()), "the hook {pid} outlived the test");
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
    let (dir, _, _) = stub("#!/bin/sh\nif [ \"$1\" = fail ]; then echo oops >&2; exit 3; fi\nhead -c 200000 /dev/zero | tr '\\0' x\n");
    let git = Git::new(fs(), dir.root().join("git"), Duration::from_secs(60), env());
    let out = git.run(dir.root(), &["big"]).unwrap();
    assert_eq!(out.len(), 200_000);
    let said = git.run(dir.root(), &["fail"]).unwrap_err();
    assert_eq!(said, GitError::Said("oops".to_string()));
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
  "config --get user.name") echo "Alice Example" ;;
  "config --get user.email") echo "alice@example.com" ;;
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
  "config --get user.email") echo "a@x" ;;
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
