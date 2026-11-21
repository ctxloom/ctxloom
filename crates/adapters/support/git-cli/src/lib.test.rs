//! Running git: which variables are scrubbed, what it is given, and how a
//! git that hangs, or leaves a helper behind, is bounded.

use std::ffi::OsString;
use std::time::{Duration, Instant};

use super::{environment, is_local, local_variables, subcommand, Failure, Invocation, Runner};
use crate::fixtures::{env, exists, fs, put, root, runs_scripts, slurp};

const TINY: Duration = Duration::from_millis(20);

/// A "git" that is a script, in a directory of its own, run by `sh` and
/// never exec'd as written (`fixture_program`).
fn stub(body: &str, timeout: Duration) -> (file_system::Rooted, Runner) {
    let dir = root();
    let path = dir.root().join("git");
    fixture_program::install_program(&*fs(), &path, body).unwrap();
    (dir, Runner::new(path, timeout, env()))
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
    assert!(has("SSH_ASKPASS", ""));
}

#[test]
fn an_invocation_is_named_by_its_first_argument_that_is_not_a_flag() {
    assert_eq!(subcommand(&["--no-pager", "-c", "status"]), "status");
    assert_eq!(subcommand(&["fetch", "--prune"]), "fetch");
    assert_eq!(subcommand(&["--version"]), "");
    assert_eq!(subcommand(&[]), "");
}

#[test]
fn a_git_that_runs_is_given_its_arguments_its_input_its_directory_and_the_extra_variables() {
    if !runs_scripts() {
        return;
    }
    let (dir, git) = stub(
        "#!/bin/sh\nprintf '%s|' \"$@\"\nprintf 'pwd=%s|' \"$(pwd -P)\"\nprintf 'cred=%s|' \"$CRED\"\nprintf 'dir=%s|' \"$GIT_DIR\"\ncat\n",
        Duration::from_secs(60),
    );
    let extra = [(OsString::from("CRED"), OsString::from("secret"))];
    let here = fs().canonicalize(dir.root()).unwrap();
    let ran = git
        .run(&Invocation {
            dir: Some(&here),
            args: &["status", "-z"],
            stdin: Some(b"fed"),
            extra_env: &extra,
        })
        .unwrap();
    assert_eq!(
        String::from_utf8(ran.stdout).unwrap(),
        format!("status|-z|pwd={}|cred=secret|dir=|fed", here.display())
    );
}

#[test]
fn a_git_that_fails_says_its_status_and_its_words() {
    if !runs_scripts() {
        return;
    }
    let (dir, git) = stub(
        "#!/bin/sh\necho partial\necho '  oops  ' >&2\nexit 3\n",
        Duration::from_secs(60),
    );
    let failure = git
        .run(&Invocation::new(Some(dir.root()), &["status"]))
        .unwrap_err();
    let Failure::Exited {
        status,
        stdout,
        stderr,
    } = &failure
    else {
        panic!("not an exit: {failure:?}");
    };
    assert_eq!(status.code(), Some(3));
    assert_eq!(stdout, b"partial\n");
    assert_eq!(stderr, b"  oops  \n");
    assert_eq!(failure.said(), "oops");
    assert!(!failure.is_not_found());
}

#[test]
fn a_missing_git_is_named_as_one() {
    let dir = root();
    let git = Runner::new(dir.root().join("no-such-git"), TINY, env());
    let failure = git
        .run(&Invocation::new(Some(dir.root()), &["status"]))
        .unwrap_err();
    assert!(failure.is_not_found(), "{failure:?}");
    assert_eq!(failure.said(), "");
}

#[test]
fn a_git_that_cannot_be_started_for_another_reason_is_not_called_missing() {
    // A directory is found but cannot be executed.
    let dir = root();
    let git = Runner::new(dir.root(), Duration::from_secs(60), env());
    let failure = git
        .run(&Invocation::new(Some(dir.root()), &["status"]))
        .unwrap_err();
    assert!(matches!(failure, Failure::Spawn(_)), "{failure:?}");
    assert!(!failure.is_not_found(), "{failure:?}");
}

#[test]
fn a_git_that_outlives_its_limit_is_killed_and_says_its_limit() {
    if !runs_scripts() {
        return;
    }
    let (dir, git) = stub("#!/bin/sh\nexec sleep 30\n", TINY);
    let failure = git
        .run(&Invocation::new(Some(dir.root()), &["status"]))
        .unwrap_err();
    assert!(
        matches!(failure, Failure::TimedOut(t) if t == TINY),
        "{failure:?}"
    );
    assert_eq!(failure.said(), "");
}

#[test]
fn a_git_that_finishes_within_its_limit_is_not_disturbed() {
    if !runs_scripts() {
        return;
    }
    // Output larger than a pipe's buffer.
    let (dir, git) = stub(
        "#!/bin/sh\nhead -c 200000 /dev/zero | tr '\\0' x\n",
        Duration::from_secs(60),
    );
    let ran = git
        .run(&Invocation::new(Some(dir.root()), &["big"]))
        .unwrap();
    assert_eq!(ran.stdout.len(), 200_000);
}

#[test]
fn a_helper_the_git_started_is_killed_with_it() {
    if !runs_scripts() || !exists("/proc/self") {
        return;
    }
    // The helper holds git's pipes, as a hook or a credential helper would.
    let (dir, _) = stub(
        "#!/bin/sh\nsleep 30 &\necho $! > \"$(dirname \"$0\")/helper\"\nwait\n",
        TINY,
    );
    // A loaded machine may not have started the helper by the first limit:
    // ask again with a longer one until it has.
    let started = Instant::now();
    for secs in [1, 5, 25] {
        let git = Runner::new(dir.root().join("git"), Duration::from_secs(secs), env());
        let failure = git
            .run(&Invocation::new(Some(dir.root()), &["fetch"]))
            .unwrap_err();
        assert!(matches!(failure, Failure::TimedOut(_)), "{failure:?}");
        if exists(dir.root().join("helper")) {
            break;
        }
    }
    assert!(started.elapsed() < Duration::from_secs(40));
    let pid = slurp(dir.root().join("helper"));
    assert!(gone_soon(pid.trim()), "the helper {pid} outlived the git");
}

/// Whether the killed process `pid` is gone within ten seconds. A run past
/// its limit sends SIGKILL to the program's whole process group and reaps the
/// program, not its descendants: a helper dies when the kernel next runs it,
/// which can be just after the run returned, so one look at that instant can
/// see a helper that is already doomed. The bounded look is forced in
/// taskloom-cli-client's
/// `a_helper_seen_alive_before_its_kill_lands_is_gone_within_the_bound`.
fn gone_soon(pid: &str) -> bool {
    let until = Instant::now() + Duration::from_secs(10);
    while !run_within::process_gone(pid) {
        if Instant::now() >= until {
            return false;
        }
        std::thread::sleep(Duration::from_millis(1));
    }
    true
}

/// A helper that holds git's pipes after git exits, until the file `release`
/// appears beside the script (or 30 seconds, its own bound), its pid in
/// `helper`. A git that exits normally is not killed with what it started
/// (run-within: the descendant may be meant to stay), so the test lets it go
/// itself.
const HELD: &str = "d=$(dirname \"$0\")\n\
    ( i=0; while [ ! -e \"$d/release\" ] && [ \"$i\" -lt 3000 ]; do sleep 0.01; i=$((i+1)); done ) &\n\
    echo $! > \"$d/helper\"\n";

/// Lets a [`HELD`] helper in this directory go when dropped, whether the
/// test passed or not.
struct Release(std::path::PathBuf);

impl Drop for Release {
    fn drop(&mut self) {
        put(self.0.join("release"), "");
    }
}

#[test]
fn a_helper_that_outlives_a_finished_git_does_not_hold_the_caller() {
    if !runs_scripts() {
        return;
    }
    let (dir, _) = stub(&format!("#!/bin/sh\n{HELD}echo done\n"), TINY);
    let release = Release(dir.root().to_path_buf());
    let git = Runner::new(dir.root().join("git"), Duration::from_secs(30), env())
        .drain_grace(Duration::from_millis(50));
    let started = Instant::now();
    let ran = git
        .run(&Invocation::new(Some(dir.root()), &["gc"]))
        .unwrap();
    assert_eq!(ran.stdout, b"done\n");
    assert!(started.elapsed() < Duration::from_secs(4));
    let pid = slurp(dir.root().join("helper"));
    assert!(
        !run_within::process_gone(pid.trim()),
        "the helper held the pipes"
    );
    drop(release);
    assert!(gone_soon(pid.trim()), "the helper {pid} outlived the test");
}

// git's list is taken only from a git that succeeded and named something;
// otherwise there is none, and every `GIT_` variable counts.
#[test]
fn gits_list_is_taken_only_when_it_succeeded_and_named_some() {
    assert_eq!(
        super::listed(true, b"GIT_DIR\nGIT_WORK_TREE\n"),
        Some(vec!["GIT_DIR".to_string(), "GIT_WORK_TREE".to_string()])
    );
    assert_eq!(super::listed(false, b"GIT_DIR\n"), None);
    assert_eq!(super::listed(true, b"  \n"), None);
}
