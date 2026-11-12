//! What a client gives the daemon it spawns, and how long it waits for it.

use std::ffi::OsString;
use std::path::Path;

use super::{daemon_env, DELIVERY_MARGIN, HOOK_BOUND, SPAWN_WAIT};

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
// passes too.
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
        Path::new("C:\\u"),
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
        "APPDATA",
        "LOCALAPPDATA",
        "USERPROFILE",
        run_ports::HOME_VARIABLE,
    ];
    expected.sort_unstable();
    assert_eq!(names(&given), expected);
    assert_eq!(value(&given, "Path"), Some(&OsString::from("C:\\bin")));
}

// On unix names are case-sensitive: `Path` is not `PATH`, and no credential
// of the spawner's crosses.
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
        ]),
        Path::new("/home/u"),
    );
    let mut expected = vec![
        "PATH",
        "HOME",
        "TMPDIR",
        "LANG",
        "LC_ALL",
        run_ports::HOME_VARIABLE,
    ];
    expected.sort_unstable();
    assert_eq!(names(&given), expected);
}

// The daemon's ctxloom home is the one its spawner resolved, whatever its
// spawner's own variable said.
#[test]
fn a_daemons_ctxloom_home_is_the_one_its_spawner_resolved() {
    let given = daemon_env(
        os::Flavor::Unix,
        vars(&[(run_ports::HOME_VARIABLE, "/stale"), ("HOME", "/home/u")]),
        Path::new("/home/u/.ctxloom-rs"),
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
}
