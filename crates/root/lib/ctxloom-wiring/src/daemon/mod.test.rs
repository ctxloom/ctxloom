//! What the daemon's server half builds for a call it serves: the caller's
//! wiring and environment, its temporary directory, a child's plan, the log.

use std::collections::BTreeMap;
use std::path::Path;

use super::client::spawn_waits_on;
use super::{caller_env, caller_temp, caller_wiring, logger, served_exit};

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
