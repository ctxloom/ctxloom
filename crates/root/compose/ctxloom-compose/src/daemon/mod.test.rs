//! What a client gives the daemon it spawns, and how long it waits for it.

use std::ffi::OsString;
use std::path::Path;

use super::{
    account_home, clock_offset, daemon_env, daemon_home, declared_harness, instance_lock,
    own_executable, request_id, tail_of, under_home, wait_for_daemon, Died, Probe, Waited,
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

// A project's seal key is its `.env`'s (owner ruling 2026-11-27): no
// daemon-wide key crosses to the detached daemon, whatever the spawner's
// environment holds; the test build's own variable alone passes.
#[test]
fn a_daemon_is_given_no_seal_key_of_the_spawners() {
    let given = daemon_env(
        os::Flavor::Unix,
        vars(&[
            ("CTXLOOM_SEAL_KEY", "k"),            // gitleaks:allow
            ("CTXLOOM_SEAL_KEY_PREVIOUS", "p,q"), // gitleaks:allow
            ("CTXLOOM_TEST_SEAL_KEY", "t"),       // gitleaks:allow
            ("ANTHROPIC_API_KEY", "a key"),       // gitleaks:allow
        ]),
        None,
    );
    assert_eq!(names(&given), ["CTXLOOM_TEST_SEAL_KEY"]);
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
    let world = crate::fixtures::World::new("hook-unprobed");
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

// A daemon that died before it served is told by the END of its log: its
// last lines, blank ones left out, in order.
#[test]
fn a_died_daemon_is_told_by_its_logs_last_lines() {
    let log = "1 serving at x\n\n2 a\n3 b\n   \n4 c\n";
    assert_eq!(tail_of(log, 2), "3 b\n4 c");
    assert_eq!(tail_of(log, 9), "1 serving at x\n2 a\n3 b\n4 c");
    assert_eq!(tail_of("", 3), "");
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

#[test]
fn test_only_a_test_support_build_reads_the_harness_variable() {
    let set = std::ffi::OsString::from("1");
    assert!(declared_harness(true, Some(&set)));
    // A plain build ignores it: nothing in an install tells a binary not to spawn.
    assert!(!declared_harness(false, Some(&set)));
    assert!(!declared_harness(true, None));
    assert!(!declared_harness(true, Some(std::ffi::OsStr::new(""))));
}

#[test]
fn test_this_very_test_binary_is_never_the_executable_spawned_as_the_daemon() {
    // The decision itself, asserted where a regression would fork-bomb: the
    // run declares its harness (nextest's `guard-homes`), so a test binary
    // never names itself as the daemon.
    assert!(
        process_env::EnvSnapshot::from_process()
            .non_empty(super::HARNESS_VARIABLE)
            .is_some(),
        "run through nextest"
    );
    assert_eq!(own_executable(), None);
}

fn unserved(pid: Option<u32>) -> Probe {
    Probe {
        serving: false,
        advertised: pid,
    }
}

fn died(status: String) -> Died {
    Died {
        status,
        tail: "held".to_string(),
    }
}

fn exit(code: i32) -> Option<(Option<i32>, String)> {
    Some((Some(code), format!("exit status: {code}")))
}

// A daemon holds the single-instance lock and advertises itself, but never
// answers its health check: the spawned one exits 48, and the wait ends
// once the holder has had its grace, naming the holder, not after the whole
// wait.
#[test]
fn a_holder_that_advertises_and_never_answers_ends_the_wait_as_hung() {
    let started = std::time::Instant::now();
    let waited = wait_for_daemon(
        started + std::time::Duration::from_secs(30),
        std::time::Duration::from_millis(100),
        || unserved(Some(4242)),
        || exit(48),
        died,
    );
    assert_eq!(waited, Waited::Hung { pid: 4242 });
    assert!(started.elapsed() < std::time::Duration::from_secs(5));
}

// The usual race: another client's daemon was still starting and answers
// within the grace, so it is waited for and served.
#[test]
fn a_holder_that_answers_within_its_grace_is_served() {
    let mut looks = 0;
    let waited = wait_for_daemon(
        std::time::Instant::now() + std::time::Duration::from_secs(30),
        std::time::Duration::from_secs(5),
        || {
            looks += 1;
            if looks < 5 {
                unserved(Some(7))
            } else {
                Probe {
                    serving: true,
                    advertised: Some(7),
                }
            }
        },
        || exit(48),
        died,
    );
    assert_eq!(waited, Waited::Serving);
}

// A holder never seen (nothing advertised) is no proven hung daemon: the
// wait runs out as it always did.
#[test]
fn a_holder_never_visible_is_a_plain_timeout_at_the_end_of_the_wait() {
    let waited = wait_for_daemon(
        std::time::Instant::now() + std::time::Duration::from_millis(200),
        std::time::Duration::from_millis(50),
        || unserved(None),
        || exit(48),
        died,
    );
    assert_eq!(waited, Waited::TimedOut);
}

// An advertisement that changes holder restarts the grace: a stale file is
// not a hung daemon.
#[test]
fn a_changing_advertisement_restarts_the_grace() {
    let mut looks = 0u32;
    let waited = wait_for_daemon(
        std::time::Instant::now() + std::time::Duration::from_millis(300),
        std::time::Duration::from_millis(150),
        || {
            looks += 1;
            unserved(Some(looks))
        },
        || exit(48),
        died,
    );
    assert_eq!(waited, Waited::TimedOut);
}

// A spawn that exited for another reason has no daemon coming: it died.
#[test]
fn a_spawn_that_died_otherwise_ends_the_wait_at_once() {
    let waited = wait_for_daemon(
        std::time::Instant::now() + std::time::Duration::from_secs(30),
        std::time::Duration::from_secs(1),
        || unserved(None),
        || exit(101),
        died,
    );
    assert_eq!(waited, Waited::Died(died("exit status: 101".to_string())));
}

// A spawn still running that never serves is the plain timeout.
#[test]
fn a_spawn_still_running_that_never_serves_times_out() {
    let waited = wait_for_daemon(
        std::time::Instant::now() + std::time::Duration::from_millis(100),
        std::time::Duration::from_millis(10),
        || unserved(Some(1)),
        || None,
        died,
    );
    assert_eq!(waited, Waited::TimedOut);
}

/// A discovery naming `endpoint`, the rest as a daemon's.
fn advertising(endpoint: &str) -> run_ports::Discovery {
    run_ports::Discovery {
        stamp: "v0.1.0-abc1234".to_string(),
        pid: 7,
        endpoint: endpoint.to_string(),
        cert: run_ports::PublicCert::from_der(vec![0x30, 0x00]).unwrap(),
        bearer: run_ports::Token::from_bytes([7; 32]),
        home: "/home/u".to_string(),
    }
}

// A stale discovery file (a killed daemon's, left for the next one to
// publish over) names a port nothing listens on, which a daemon that is
// starting, or merely hung, is told apart from: only a daemon whose
// endpoint still accepts connections can be one that holds the lock and does
// not answer.
#[test]
fn an_advertised_endpoint_listens_only_while_something_accepts_on_it() {
    use super::listens;
    let listener = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
    let port = listener.local_addr().unwrap().port();
    let endpoint = format!("https://127.0.0.1:{port}");
    assert!(listens(&advertising(&endpoint)), "it accepts");
    drop(listener);
    assert!(
        !listens(&advertising(&endpoint)),
        "nothing accepts any more"
    );
    assert!(!listens(&advertising("not an endpoint")), "no address");
}

fn found() -> run_ports::Discovery {
    advertising("https://127.0.0.1:1")
}

// A daemon on a loaded machine can miss the first health check's second
// (a TLS handshake in a debug build under load), and `daemon stop` and
// `shutdown`, which never start one, then found "nothing to stop" and left
// it running with its store open. A daemon that is advertised and still
// accepts connections is asked again, longer, before it is called absent.
#[test]
fn a_listening_daemon_that_misses_the_first_check_is_asked_again_before_it_is_absent() {
    use super::{live_patiently, HEALTH_WAIT, PATIENT_TRIES};
    let mut asked = Vec::new();
    let late = live_patiently(
        &found(),
        |_, within| {
            asked.push(within);
            asked.len() == 3
        },
        |_| true,
    );
    assert!(late, "it answered the third time");
    assert_eq!(asked[0], HEALTH_WAIT, "the first check is the usual one");
    assert!(asked[1] > HEALTH_WAIT, "the later ones wait longer");

    let mut asked = 0;
    assert!(!live_patiently(
        &found(),
        |_, _| {
            asked += 1;
            false
        },
        |_| true
    ));
    assert_eq!(asked, 1 + PATIENT_TRIES, "a bounded number of asks");
}

// Nothing listening (a killed daemon's stale file) is absent at the first
// check: no wait on a port nobody holds.
#[test]
fn a_daemon_nothing_listens_for_is_absent_at_the_first_check() {
    use super::live_patiently;
    let mut asked = 0;
    assert!(!live_patiently(
        &found(),
        |_, _| {
            asked += 1;
            false
        },
        |_| false
    ));
    assert_eq!(asked, 1);
    assert!(
        live_patiently(&found(), |_, _| true, |_| false),
        "one that answers is live"
    );
}
