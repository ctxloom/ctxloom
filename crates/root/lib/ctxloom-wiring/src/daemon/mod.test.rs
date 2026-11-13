//! What a client gives the daemon it spawns, and how long it waits for it.

use std::ffi::OsString;
use std::path::Path;

use super::{
    account_home, daemon_env, daemon_home, instance_lock, ACCOUNT_HOME_VARIABLE, DELIVERY_MARGIN,
    HOOK_BOUND, SPAWN_WAIT,
};

fn vars(pairs: &[(&str, &str)]) -> Vec<(OsString, OsString)> {
    pairs
        .iter()
        .map(|(name, value)| (OsString::from(name), OsString::from(value)))
        .collect()
}

fn names(env: &[(OsString, OsString)]) -> Vec<String> {
    let mut names: Vec<String> = env
        .iter()
        .map(|(name, _)| name.to_string_lossy().into_owned())
        .collect();
    names.sort();
    names
}

fn value<'a>(env: &'a [(OsString, OsString)], name: &str) -> Option<&'a OsString> {
    env.iter().find(|(n, _)| n == name).map(|(_, v)| v)
}

// S16, M11: a spawn and a delivery fit in a hook's bound together.
#[test]
fn a_spawn_wait_leaves_a_delivery_room_within_a_hooks_bound() {
    assert!(
        SPAWN_WAIT + DELIVERY_MARGIN <= HOOK_BOUND,
        "{SPAWN_WAIT:?} + {DELIVERY_MARGIN:?} > {HOOK_BOUND:?}"
    );
}

// M8: on Windows a variable's name is matched as Windows compares names, so
// `Path` and `SystemRoot` pass, and what a Windows program needs to start
// passes too; the spawner's profile and its folders do not (the daemon's
// home is the account's, never its spawner's).
#[test]
fn a_windows_daemon_is_given_its_path_and_startup_variables_whatever_their_case() {
    let given = daemon_env(
        os::Flavor::Windows,
        vars(&[
            ("Path", "C:\\bin"),
            ("SystemRoot", "C:\\Windows"),
            ("SystemDrive", "C:"),
            ("windir", "C:\\Windows"),
            ("TEMP", "C:\\t"),
            ("TMP", "C:\\t"),
            ("PATHEXT", ".EXE"),
            ("ComSpec", "C:\\cmd.exe"),
            ("APPDATA", "C:\\a"),
            ("LOCALAPPDATA", "C:\\l"),
            ("USERPROFILE", "C:\\u"),
            ("ANTHROPIC_API_KEY", "a key"), // gitleaks:allow
            ("Secret", "s"),
        ]),
        None,
    );
    let mut expected = vec![
        "Path",
        "SystemRoot",
        "SystemDrive",
        "windir",
        "TEMP",
        "TMP",
        "PATHEXT",
        "ComSpec",
    ];
    expected.sort_unstable();
    assert_eq!(names(&given), expected);
    assert_eq!(value(&given, "Path"), Some(&OsString::from("C:\\bin")));
}

// On unix names are case-sensitive: `Path` is not `PATH`, and no credential
// of the spawner's crosses, nor its HOME or its confinement (the daemon's
// home is the account's).
#[test]
fn a_unix_daemon_is_given_what_it_needs_and_no_credential() {
    let given = daemon_env(
        os::Flavor::Unix,
        vars(&[
            ("PATH", "/bin"),
            ("Path", "/elsewhere"),
            ("HOME", "/home/u"),
            ("TMPDIR", "/tmp"),
            ("LANG", "C"),
            ("LC_ALL", "C"),
            ("ANTHROPIC_API_KEY", "a key"), // gitleaks:allow
            ("CLAUDE_CONFIG_DIR", "/confined"),
            ("XDG_CONFIG_HOME", "/confined/xdg"),
        ]),
        None,
    );
    let mut expected = vec!["PATH", "TMPDIR", "LANG", "LC_ALL"];
    expected.sort_unstable();
    assert_eq!(names(&given), expected);
}

// The daemon's ctxloom home is its spawner's CTXLOOM_HOME when it set one
// (once, whatever the spawner's own variable said), and else none: the
// daemon then serves the account's home.
#[test]
fn a_daemons_ctxloom_home_is_its_spawners_ctxloom_home_alone() {
    let given = daemon_env(
        os::Flavor::Unix,
        vars(&[(run_ports::HOME_VARIABLE, "/stale"), ("HOME", "/home/u")]),
        Some(Path::new("/home/u/.ctxloom-rs")),
    );
    assert_eq!(
        value(&given, run_ports::HOME_VARIABLE),
        Some(&OsString::from("/home/u/.ctxloom-rs"))
    );
    assert_eq!(
        given
            .iter()
            .filter(|(n, _)| n == run_ports::HOME_VARIABLE)
            .count(),
        1
    );
    let given = daemon_env(
        os::Flavor::Unix,
        vars(&[(run_ports::HOME_VARIABLE, "/stale"), ("HOME", "/home/u")]),
        None,
    );
    assert!(names(&given).is_empty(), "{given:?}");
}

/// An environment of `pairs`, unix, its working directory `/`.
fn snapshot(pairs: &[(&str, &str)]) -> process_env::EnvSnapshot {
    process_env::EnvSnapshot::from_parts(
        os::Flavor::Unix,
        Ok(std::path::PathBuf::from("/")),
        std::path::PathBuf::from("/tmp"),
        vars(pairs),
    )
}

// One daemon per OS USER: the account's home is the system's (a test's
// override first, and under test-support HOME when none is named), never
// a moved HOME.
#[test]
fn the_accounts_home_is_the_tests_override_first() {
    let env = snapshot(&[(ACCOUNT_HOME_VARIABLE, "/account"), ("HOME", "/moved")]);
    assert_eq!(
        account_home(&env),
        Some(std::path::PathBuf::from("/account"))
    );
    let env = snapshot(&[("HOME", "/moved")]);
    assert_eq!(account_home(&env), Some(std::path::PathBuf::from("/moved")));
}

// The daemon's home (M4) is its CTXLOOM_HOME when it was given one, else the
// account's, never its HOME.
#[test]
fn the_daemons_home_is_its_ctxloom_home_else_the_accounts_never_its_home() {
    let env = snapshot(&[(ACCOUNT_HOME_VARIABLE, "/account"), ("HOME", "/moved")]);
    assert_eq!(
        daemon_home(&env),
        Some(std::path::PathBuf::from("/account"))
    );
    let env = snapshot(&[
        (ACCOUNT_HOME_VARIABLE, "/account"),
        ("HOME", "/moved"),
        (run_ports::HOME_VARIABLE, "/ctxloom-rs"),
    ]);
    assert_eq!(
        daemon_home(&env),
        Some(std::path::PathBuf::from("/ctxloom-rs"))
    );
}

// The single-instance lock is the ACCOUNT's (its home's
// `.ctxloom/daemon/daemon.lock`), whatever ctxloom home the process has.
#[test]
fn the_instance_lock_is_under_the_accounts_home_whatever_the_ctxloom_home() {
    let env = snapshot(&[
        (ACCOUNT_HOME_VARIABLE, "/account"),
        ("HOME", "/moved"),
        (run_ports::HOME_VARIABLE, "/ctxloom-rs"),
    ]);
    let wiring = crate::Wiring::new(std::sync::Arc::new(env)).unwrap();
    assert_eq!(
        instance_lock(&wiring),
        std::path::PathBuf::from("/account/.ctxloom/daemon/daemon.lock")
    );
}
