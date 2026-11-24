//! What a client gives the daemon it spawns, and how long it waits for it.

use std::collections::BTreeMap;
use std::ffi::OsString;
use std::path::Path;

use super::{
    account_home, caller_env, caller_temp, caller_wiring, clock_offset, daemon_env, daemon_home,
    instance_lock, logger, request_id, served_exit, spawn_waits_on, tail_of, under_home,
    ACCOUNT_HOME_VARIABLE, CALL_WAIT, CLOCK_OFFSET_VARIABLE, DELIVERY_MARGIN, DESTROY_WAIT,
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

// The daemon cluster's cap (owner ruling, 2026-11-18): `daemon serve` that
// lost the single-instance lock to another daemon is a decline (20, a lock
// another process holds); one that could not serve failed (32).
#[test]
fn a_daemon_that_lost_the_lock_declines_and_one_that_could_not_serve_failed() {
    use super::serve::Unserved;
    use cli_support::Exit;
    assert_eq!(served_exit(Ok(())).ok(), Some(Exit::OK));
    assert_eq!(
        served_exit(Err(Unserved::Held { lock: "l".into() }))
            .err()
            .map(|f| f.exit),
        Some(Exit::REFUSED)
    );
    assert_eq!(
        served_exit(Err(Unserved::Failed("no store".into())))
            .err()
            .map(|f| f.exit),
        Some(Exit::FAILED)
    );
}

// The same ruling: a client whose spawned daemon has exited waits on only
// when it lost the lock to another daemon (that one advertises); any other
// exit, or a signal's, means no daemon is coming of it, and the wait ends.
#[test]
fn a_spawn_wait_goes_on_only_for_a_daemon_that_lost_the_lock() {
    use super::serve::Unserved;
    let lost = served_exit(Err(Unserved::Held { lock: "l".into() })).expect_err("a decline");
    let lost = i32::from(lost.exit.code(false));
    assert!(spawn_waits_on(Some(lost)), "another daemon holds the lock");
    let failed = served_exit(Err(Unserved::Failed("x".into()))).expect_err("a failure");
    assert!(!spawn_waits_on(Some(i32::from(failed.exit.code(false)))));
    assert!(!spawn_waits_on(Some(0)), "it served and exited");
    assert!(!spawn_waits_on(None), "a signal ended it");
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

// The daemon's ctxloom home is its spawner's CTXLOOM_DAEMON_ROOT when it set one
// (once, whatever the spawner's own variable said), and else none: the
// daemon then serves the account's home.
#[test]
fn a_daemons_ctxloom_home_is_its_spawners_ctxloom_home_alone() {
    let given = daemon_env(
        os::Flavor::Unix,
        vars(&[
            (run_ports::DAEMON_ROOT_VARIABLE, "/stale"),
            ("HOME", "/home/u"),
        ]),
        Some(Path::new("/home/u/.ctxloom-rs")),
    );
    assert_eq!(
        value(&given, run_ports::DAEMON_ROOT_VARIABLE),
        Some(&OsString::from("/home/u/.ctxloom-rs"))
    );
    assert_eq!(
        given
            .iter()
            .filter(|(n, _)| n == run_ports::DAEMON_ROOT_VARIABLE)
            .count(),
        1
    );
    let given = daemon_env(
        os::Flavor::Unix,
        vars(&[
            (run_ports::DAEMON_ROOT_VARIABLE, "/stale"),
            ("HOME", "/home/u"),
        ]),
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
    // An engine's hook process: its HOME the session home, ctxloom's home
    // the test's (slice 4 U7).
    let env = snapshot(&[
        ("HOME", "/session-home"),
        (run_ports::DAEMON_ROOT_VARIABLE, "/ctxloom-rs"),
    ]);
    assert_eq!(
        account_home(&env),
        Some(std::path::PathBuf::from("/ctxloom-rs"))
    );
    let env = snapshot(&[
        (ACCOUNT_HOME_VARIABLE, "/account"),
        (run_ports::DAEMON_ROOT_VARIABLE, "/ctxloom-rs"),
    ]);
    assert_eq!(
        account_home(&env),
        Some(std::path::PathBuf::from("/account"))
    );
}

// The daemon's home (M4) is its CTXLOOM_DAEMON_ROOT when it was given one, else the
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
        (run_ports::DAEMON_ROOT_VARIABLE, "/ctxloom-rs"),
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
        (run_ports::DAEMON_ROOT_VARIABLE, "/ctxloom-rs"),
    ]);
    let wiring = crate::Wiring::new(std::sync::Arc::new(env)).unwrap();
    assert_eq!(
        instance_lock(&wiring),
        std::path::PathBuf::from("/account/.ctxloom/daemon/daemon.lock")
    );
}

// T21, S7: a launch hands the daemon where its launcher finds its user's
// settings (her claude config dir, her XDG bases), and nothing else of the
// launcher's in that place.
#[test]
fn a_launch_names_where_its_launcher_finds_its_users_settings() {
    let caller: BTreeMap<String, String> = [
        ("CLAUDE_CONFIG_DIR", "/home/alice/.claude-work"),
        ("XDG_CONFIG_HOME", "/home/alice/.cfg"),
        ("XDG_STATE_HOME", ""),
        ("ANTHROPIC_API_KEY", "k"), // gitleaks:allow
    ]
    .into_iter()
    .map(|(k, v)| (k.to_string(), v.to_string()))
    .collect();
    let places = super::settings_places(&caller);
    assert_eq!(
        places.keys().map(String::as_str).collect::<Vec<_>>(),
        ["CLAUDE_CONFIG_DIR", "XDG_CONFIG_HOME"]
    );
}

// A destroy's deadline is sized to its worktrees' git, each invocation
// bounded by worktree-git's own timeout, with a read's wait to spare.
#[test]
fn a_destroys_deadline_outlasts_its_worktrees_git() {
    assert_eq!(
        DESTROY_WAIT,
        worktree_git::DEFAULT_TIMEOUT * 60 + CALL_WAIT,
        "sixty git invocations at their whole timeout, plus a read's wait"
    );
}

// Each write's request id is fresh.
#[test]
fn each_request_id_is_fresh() {
    let (one, two) = (request_id(), request_id());
    assert_ne!(one, two);
    assert!(uuid::Uuid::parse_str(&one).is_ok(), "{one}");
}

// j000100's exit: the daemon's log is opened where it starts and written
// through that handle, never made again by its path (owner, 2026-11-17): a
// line said once the log, or its home, is removed makes nothing there.
#[test]
fn the_log_is_opened_at_the_start_and_never_made_again() {
    let root = crate::fixtures::root();
    let home = root.root().join("home");
    let log = home.join(".ctxloom/daemon/daemon.log");
    let mut say = logger(crate::fixtures::fs(), log.clone());
    assert!(
        crate::fixtures::exists(log.parent().unwrap()),
        "made at the start"
    );
    say("serving");
    let text = crate::fixtures::fs().read_to_string(&log).unwrap();
    assert!(text.ends_with(" serving\n"), "{text}");
    crate::fixtures::fs().remove_file(&log).unwrap();
    say("the discovery file was removed: exiting");
    assert!(!crate::fixtures::exists(&log), "the log made again");
    crate::fixtures::remove_dir(home.join(".ctxloom"));
    say("the single-instance lock file was replaced or removed: exiting");
    say("exited");
    assert!(
        !crate::fixtures::exists(home.join(".ctxloom")),
        "made again"
    );
}

// Step 8 Q1: a launch's `--project ../api` is the sibling directory, by its
// words alone.
#[test]
fn a_projects_directory_is_read_lexically() {
    assert_eq!(
        super::lexical(Path::new("/w/project/../api")),
        Path::new("/w/api")
    );
    assert_eq!(super::lexical(Path::new("/w/./a/b/..")), Path::new("/w/a"));
    assert_eq!(super::lexical(Path::new("../x")), Path::new("../x"));
}

// Step 10a U2 (the one per-call builder): a call the daemon serves (a
// library read, a child's plan, a hosted run) is read as a process at the
// caller's directory in the caller's environment, with ctxloom's own home
// as its home and the caller's home its own; a project named for it is its
// `CTXLOOM_ROOT` over the caller's, as the platform compares names.
#[test]
fn a_callers_process_has_ctxlooms_home_and_keeps_the_callers_own() {
    let own = Path::new("C:\\own");
    for (flavor, caller, root) in [
        (os::Flavor::Windows, "USERPROFILE", "Ctxloom_Root"),
        (os::Flavor::Unix, "HOME", "CTXLOOM_ROOT"),
    ] {
        let env = [(caller, "C:\\caller"), (root, "C:\\elsewhere")];
        let rooted = caller_env(
            flavor,
            Path::new("C:\\tmp"),
            ("C:\\work", Some("C:\\work")),
            env,
        );
        assert_eq!(rooted.utf8("CTXLOOM_ROOT"), Some("C:\\work"), "{flavor:?}");
        assert_eq!(
            rooted.home(),
            Some(std::ffi::OsStr::new("C:\\caller")),
            "{flavor:?}"
        );
        assert_eq!(rooted.cwd().unwrap(), Path::new("C:\\work"));
        let kept = caller_env(flavor, Path::new("C:\\tmp"), ("C:\\work", None), env);
        assert_eq!(
            kept.utf8("CTXLOOM_ROOT"),
            Some("C:\\elsewhere"),
            "{flavor:?}"
        );
    }
    let wiring = caller_wiring(
        own,
        Path::new("/tmp"),
        ("/work", None),
        [("HOME", "/caller")],
    );
    assert_eq!(wiring.home(), own);
    if os::host().flavor() == os::Flavor::Unix {
        assert_eq!(wiring.user_home().as_deref(), Some(Path::new("/caller")));
    }
}

// The daemon log's path as a person reads it is `~` and the rest joined by
// the platform's separator: never a mix of `/` and `\` on Windows.
#[test]
fn a_path_under_the_home_is_said_with_the_platforms_separator() {
    let below = Path::new(".ctxloom").join("daemon").join("daemon.log");
    assert_eq!(
        under_home(os::Flavor::Unix, &below),
        "~/.ctxloom/daemon/daemon.log"
    );
    assert_eq!(
        under_home(os::Flavor::Windows, &below),
        r"~\.ctxloom\daemon\daemon.log"
    );
}

/// A discovery of `home`'s daemon at an endpoint nothing listens on.
fn dead_discovery(home: &str) -> run_ports::Discovery {
    run_ports::Discovery {
        stamp: "v0.1.0-abc1234".to_string(),
        pid: 7,
        endpoint: "https://127.0.0.1:1".to_string(),
        cert: run_ports::PublicCert::from_der(vec![0x30, 0x00]).unwrap(),
        bearer: run_ports::Token::from_bytes([7; 32]),
        home: home.to_string(),
    }
}

// The lean hook RPC (owner budget 2026-11-18): a hook's one call asks the
// daemon its discovery file names with NO health check (a TLS connection of
// its own), so even a daemon that would not answer one is named; never a
// daemon of another home.
#[test]
fn test_a_hooks_unprobed_daemon_is_its_homes_discovery_without_a_health_check() {
    let world = crate::commands::tests::World::new("hook-unprobed");
    let wiring = world.wiring();
    let env = std::sync::Arc::clone(wiring.env());
    assert!(
        super::hook_daemon_unprobed(&env).is_none(),
        "none published"
    );
    let home = wiring.home().display().to_string();
    super::publish_for_test(&wiring, &dead_discovery(&home));
    let found = super::hook_daemon_unprobed(&env).expect("named, unprobed");
    assert_eq!(found.endpoint, "https://127.0.0.1:1");
    assert!(
        super::hook_daemon_serving(&env).is_none(),
        "probed, it is not live"
    );
    super::publish_for_test(&wiring, &dead_discovery("/another/home"));
    assert!(
        super::hook_daemon_unprobed(&env).is_none(),
        "another home's"
    );
}

/// What `render` says of `value` in text.
fn said_text(value: &impl angzarr_presentation::Render) -> String {
    let mut out = Vec::new();
    angzarr_presentation::render(&mut out, angzarr_presentation::Format::Text, value)
        .expect("rendered");
    String::from_utf8(out).expect("utf-8")
}

// Smoke 2026-11-19: `daemon stop` says what it stopped (DRAFT text), or
// that nothing was serving, and its json is the stopped daemon's message.
#[test]
fn daemon_stop_says_what_it_stopped_and_when_nothing_served() {
    use super::Stopped;
    let daemon = daemon_proto::v1::Daemon {
        pid: 4242,
        version: "v0.1.0-abc1234".to_string(),
        ..daemon_proto::v1::Daemon::default()
    };
    assert_eq!(
        said_text(&Stopped(Some(daemon.clone()), false)),
        "stopped the ctxloom daemon (pid 4242), ctxloom v0.1.0-abc1234\n"
    );
    assert_eq!(
        said_text(&Stopped(Some(daemon), true)),
        "drained and stopped the ctxloom daemon (pid 4242), ctxloom v0.1.0-abc1234\n"
    );
    assert_eq!(
        said_text(&Stopped(None, false)),
        "no ctxloom daemon was serving: nothing to stop\n"
    );
}

/// `pairs` as a snapshot's variables.
fn vars_of(pairs: &[(&str, &str)]) -> Vec<(std::ffi::OsString, std::ffi::OsString)> {
    pairs
        .iter()
        .map(|(name, value)| ((*name).into(), (*value).into()))
        .collect()
}

// Owner, 2026-11-18: a call's temporary directory is the caller's, read
// from her environment as the platform's standard library reads it, never
// the daemon's own.
#[test]
fn a_calls_temporary_directory_is_the_callers() {
    use os::Flavor::{Unix, Windows};
    let own = Path::new("/daemon-tmp");
    let temp = |flavor, pairs: &[(&str, &str)]| caller_temp(flavor, &vars_of(pairs), own);
    assert_eq!(
        temp(Unix, &[("TMPDIR", "/her/tmp"), ("HOME", "/h")]),
        Path::new("/her/tmp")
    );
    assert_eq!(temp(Unix, &[("HOME", "/h")]), Path::new("/tmp"));
    assert_eq!(
        temp(Unix, &[("TMPDIR", ""), ("HOME", "/h")]),
        Path::new("/tmp")
    );
    assert_eq!(
        temp(Windows, &[("tmp", "C:\\a"), ("TEMP", "C:\\t")]),
        Path::new("C:\\a")
    );
    assert_eq!(
        temp(Windows, &[("Temp", "C:\\t"), ("UserProfile", "C:\\u")]),
        Path::new("C:\\t")
    );
    assert_eq!(
        temp(Windows, &[("USERPROFILE", "C:\\u")]),
        Path::new("C:\\u")
    );
    assert_eq!(temp(Windows, &[("windir", "C:\\W")]), Path::new("C:\\W"));
    let snapshot = caller_env(Unix, own, ("/work", None), [("TMPDIR", "/her/tmp")]);
    assert_eq!(snapshot.temp_dir(), Path::new("/her/tmp"));
}

#[test]
fn a_call_with_no_environment_is_in_the_daemons_temporary_directory() {
    let own = Path::new("/daemon-tmp");
    assert_eq!(caller_temp(os::Flavor::Unix, &[], own), own);
    assert_eq!(caller_temp(os::Flavor::Windows, &[], own), own);
    let snapshot = caller_env(os::Flavor::Unix, own, ("/work", None), []);
    assert_eq!(snapshot.temp_dir(), own);
}

// A daemon that died before it served is told by the END of its log: its
// last lines, blank ones left out, in order.
#[test]
fn a_died_daemon_is_told_by_its_logs_last_lines() {
    let log = "1 serving at x\n\n2 a\n3 b\n   \n4 c\n";
    assert_eq!(tail_of(log, 2), "3 b\n4 c");
    assert_eq!(tail_of(log, 9), "1 serving at x\n2 a\n3 b\n4 c");
    assert_eq!(tail_of("", 3), "");
}

// Claim B: the daemon plans a child of a project as a process there would,
// which reads the bundles installed from its remotes. A profile naming one
// must resolve, as `ctxloom run --dry-run` resolves it; with no remotes in the
// child's library the bundle reads as not installed and the plan has a fatal
// finding, which the enqueue then counts as `runs-launch-refused`.
#[test]
fn a_childs_plan_reads_the_bundles_installed_from_remotes() {
    let (root, dir) = crate::fixtures::project_with_a_remote_bundle();
    let home = root.root().join("home");
    let env = BTreeMap::from([(
        crate::fixtures::home_var().to_string(),
        home.display().to_string(),
    )]);
    let previewed = super::child_plan(
        &home,
        Path::new("/tmp"),
        "claude-code",
        "orch",
        &dir,
        &env,
        true,
    )
    .expect("planned");
    let said: Vec<String> = previewed.fatal.iter().map(|f| f.text.clone()).collect();
    assert!(said.is_empty(), "fatal findings: {said:?}");
}

fn offset_of(value: Option<&str>) -> Result<std::time::Duration, duration_text::DurationError> {
    let env = match value {
        Some(text) => process_env::EnvSnapshot::of([(CLOCK_OFFSET_VARIABLE, text)]),
        None => process_env::EnvSnapshot::of(Vec::<(&str, &str)>::new()),
    };
    clock_offset(&env)
}

#[test]
fn test_the_clock_offset_is_zero_when_unset() {
    assert_eq!(offset_of(None).unwrap(), std::time::Duration::ZERO);
    assert_eq!(offset_of(Some("")).unwrap(), std::time::Duration::ZERO);
}

#[test]
fn test_the_clock_offset_reads_any_span_of_the_grammar() {
    let secs = std::time::Duration::from_secs;
    assert_eq!(offset_of(Some("3h")).unwrap(), secs(3 * 3600));
    assert_eq!(offset_of(Some("2d")).unwrap(), secs(2 * 86_400));
    assert_eq!(offset_of(Some("90m")).unwrap(), secs(5400));
    assert_eq!(offset_of(Some("1h30m")).unwrap(), secs(5400));
    assert_eq!(offset_of(Some("0s")).unwrap(), secs(0));
}

#[test]
fn test_a_bare_integer_offset_is_refused() {
    assert!(offset_of(Some("6")).is_err());
    assert!(offset_of(Some("0")).is_err());
}

#[test]
fn test_a_negative_offset_is_refused() {
    assert!(offset_of(Some("-3h")).is_err());
}

#[test]
fn test_an_offset_that_is_no_span_is_refused() {
    assert!(offset_of(Some("soon")).is_err());
}
