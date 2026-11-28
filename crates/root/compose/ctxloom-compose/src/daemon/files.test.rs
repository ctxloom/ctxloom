//! The daemon's files under a temporary home (S7: never the real one).

use std::time::Duration;

use file_system::Rooted;
use run_ports::{Discovery, PublicCert, Token};

use daemon_application::HandOff;

use super::{instance_lock_path, DaemonFiles, Unheld};
use crate::fixtures;

fn home() -> (Rooted, std::path::PathBuf) {
    let root = fixtures::root();
    let home = root.root().join("home");
    (root, home)
}

fn files(_root: &Rooted, home: &std::path::Path) -> DaemonFiles {
    DaemonFiles::new(fixtures::fs(), home)
}

fn sample() -> Discovery {
    Discovery {
        stamp: "v0.1.0-abc1234".to_string(),
        pid: 7,
        endpoint: "https://127.0.0.1:4242".to_string(),
        cert: PublicCert::from_der(vec![0x30, 0x00]).unwrap(),
        bearer: Token::from_bytes([7; 32]),
        home: "/home/u".to_string(),
    }
}

// The daemon cluster's cap (owner ruling, 2026-11-18): a lock another holds
// is told apart from one that cannot be taken at all (its directory cannot
// be made), so a daemon that cannot start says so, and its spawner stops
// waiting for it.
#[test]
fn a_lock_another_holds_is_told_apart_from_one_that_cannot_be_taken() {
    let (root, home) = home();
    let files = files(&root, &home);
    let _held = files
        .take_instance(Duration::ZERO)
        .expect("the first holds it");
    assert_eq!(
        files.take_instance(Duration::ZERO).err(),
        Some(Unheld::Held {
            lock: instance_lock_path(&home, None).display().to_string()
        }),
        "a second finds it held"
    );
    let (root, home) = self::home();
    fixtures::mkdirs(home.join(".ctxloom"));
    fixtures::put_mode(home.join(".ctxloom/daemon"), "not a directory", 0o644);
    let blocked = self::files(&root, &home);
    assert!(
        matches!(
            blocked.take_instance(Duration::ZERO),
            Err(Unheld::Failed(_))
        ),
        "a lock whose directory cannot be made is not held by anyone"
    );
}

#[test]
fn the_single_instance_lock_is_one_holder_at_a_time() {
    let (root, home) = home();
    let files = files(&root, &home);
    let held = files
        .hold_instance(Duration::ZERO)
        .expect("the first holds it");
    assert!(
        files.hold_instance(Duration::ZERO).is_none(),
        "a second is not"
    );
    drop(held);
    assert!(
        files.hold_instance(Duration::ZERO).is_some(),
        "the lock dies with its holder"
    );
    assert!(files.dir().ends_with(".ctxloom/daemon"));
}

#[test]
fn the_discovery_is_published_private_and_withdrawn() {
    let (root, home) = home();
    let files = files(&root, &home);
    assert_eq!(files.discovery(), None);
    files.publish(&sample()).unwrap();
    assert_eq!(files.discovery(), Some(sample()));
    let fs = fixtures::fs();
    let path = files.dir().join(run_ports::DISCOVERY_FILE);
    // Mode bits are unix's alone.
    if os::host().flavor() == os::Flavor::Unix {
        assert_eq!(fs.metadata(&path).unwrap().mode.unwrap().bits(), 0o600);
        assert_eq!(
            fs.metadata(files.dir()).unwrap().mode.unwrap().bits(),
            0o700
        );
    }
    files.withdraw();
    assert_eq!(files.discovery(), None);
    files.withdraw();
}

// M11: a daemon that exits takes its own advertisement down, and never one
// another daemon published over it.
#[test]
fn a_daemon_withdraws_its_own_advertisement_and_never_anothers() {
    let (root, home) = home();
    let files = files(&root, &home);
    let other = Discovery {
        pid: 8,
        bearer: Token::from_bytes([8; 32]),
        ..sample()
    };
    files.publish(&sample()).unwrap();
    files.withdraw_own(&other);
    assert_eq!(files.discovery(), Some(sample()), "another's is left");
    files.withdraw_own(&sample());
    assert_eq!(files.discovery(), None, "its own is withdrawn");
    files.withdraw_own(&sample());
}

// The single-instance lock makes the directory first, with the default mode: the
// publish holds it to 0700 all the same.
#[test]
fn the_discovery_is_published_into_a_private_directory_whoever_made_it() {
    let (root, home) = home();
    let files = files(&root, &home);
    let _held = files.hold_instance(Duration::ZERO).expect("held");
    files.publish(&sample()).unwrap();
    if os::host().flavor() == os::Flavor::Unix {
        let fs = fixtures::fs();
        assert_eq!(
            fs.metadata(files.dir()).unwrap().mode.unwrap().bits(),
            0o700
        );
    }
}

#[test]
fn the_log_is_in_the_daemons_directory() {
    let (root, home) = home();
    let files = files(&root, &home);
    assert!(files.log_path().ends_with(".ctxloom/daemon/daemon.log"));
}

const CHILD: &str = "0b0b0b0b-0b0b-4b0b-8b0b-0b0b0b0b0b0b";

fn env(pairs: &[(&str, &str)]) -> std::collections::BTreeMap<String, String> {
    pairs
        .iter()
        .map(|(k, v)| ((*k).to_string(), (*v).to_string()))
        .collect()
}

// D-N45: a launcher's hand-off reads back whole, values with lines,
// backslashes and `=` alike, with the agent its child is planned for; one
// an older daemon wrote (format 2) reads as one for the default agent.
#[test]
fn a_hand_off_reads_back_what_was_written() {
    let sent = env(&[
        ("PATH", "/bin:/usr/bin"),
        ("ODD", "a=b\\nc\nd\re\\"),
        ("EMPTY", ""),
    ]);
    let handed = HandOff {
        env: sent,
        launcher: env(&[("CLAUDE_CONFIG_DIR", "/home/alice/.claude-work")]),
        agent: "reviewer".to_string(),
        program: None,
    };
    let text = super::encode_hand_off(&handed);
    assert!(text.starts_with("ctxloom-launch-env 3\n"), "{text}");
    assert_eq!(text.lines().count(), 6, "one line a variable: {text}");
    assert!(
        text.contains("\n@CLAUDE_CONFIG_DIR=/home/alice/.claude-work\n"),
        "{text}"
    );
    assert!(text.ends_with("\n!agent=reviewer\n"), "{text}");
    assert_eq!(super::decode_hand_off(&text), Some(handed));
    assert_eq!(
        super::decode_hand_off("ctxloom-launch-env 2\nA=b\n"),
        Some(HandOff {
            env: env(&[("A", "b")]),
            ..HandOff::default()
        })
    );
    let unnamed = HandOff {
        env: env(&[("A", "b")]),
        ..HandOff::default()
    };
    assert!(
        !super::encode_hand_off(&unnamed).contains("!agent"),
        "no agent, no line"
    );
    assert_eq!(super::decode_hand_off("other 1\nA=b\n"), None);
    assert_eq!(super::decode_hand_off("ctxloom-launch-env 1\nA=b\n"), None);
    assert_eq!(
        super::decode_hand_off("ctxloom-launch-env 3\nnoequals\n"),
        None
    );
    assert_eq!(
        super::decode_hand_off("ctxloom-launch-env 3\nA=bad\\q\n"),
        None
    );
    assert_eq!(
        super::decode_hand_off("ctxloom-launch-env 3\nA=end\\\n"),
        None
    );
}

// T6: the hand-off is the user's alone (0600 in the 0700 launch
// directory), READ and left for the child's life, deleted, and only ever
// named by a child's UUID.
#[test]
fn a_hand_off_is_the_users_alone_and_read_for_the_childs_life() {
    let (root, home) = home();
    let files = files(&root, &home);
    let sent = HandOff {
        env: env(&[("ANTHROPIC_API_KEY", "k")]), // gitleaks:allow
        ..HandOff::default()
    };
    files.hand_off(CHILD, &sent).unwrap();
    let path = files.dir().join("launch").join(CHILD);
    // Mode bits are unix's alone.
    if os::host().flavor() == os::Flavor::Unix {
        assert_eq!(
            fixtures::fs().metadata(&path).unwrap().mode.unwrap().bits(),
            0o600
        );
        assert_eq!(
            fixtures::fs()
                .metadata(&files.dir().join("launch"))
                .unwrap()
                .mode
                .unwrap()
                .bits(),
            0o700
        );
    }
    assert_eq!(files.read_hand_off(CHILD), Some(sent.clone()));
    assert_eq!(files.read_hand_off(CHILD), Some(sent.clone()), "read again");
    files.delete_hand_off(CHILD);
    assert_eq!(files.read_hand_off(CHILD), None, "deleted");
    assert!(!fixtures::fs().exists(&path).unwrap());
    assert!(files.hand_off("../escape", &sent).is_err());
    assert_eq!(files.read_hand_off("../escape"), None);
}

// T6 (the owner's ruling on U3): a launch's hand-off is written marked in
// launch, listed with whether its creator still holds its launch lock,
// claimed (its mark cleared, the hand-off kept), and deleted with its mark;
// a file no UUID names is none.
#[test]
fn a_launchs_hand_off_is_marked_listed_by_its_creators_lock_claimed_and_deleted() {
    use run_ports::RunLocks;
    let (root, home) = home();
    let files = files(&root, &home);
    assert!(files.hand_offs().is_empty());
    files.launching(CHILD, &HandOff::default()).unwrap();
    fixtures::put_mode(files.dir().join("launch").join("not-a-child"), "x", 0o600);
    let kept = |marked, creator_holds| daemon_application::Kept {
        session: CHILD.to_string(),
        marked,
        creator_holds,
    };
    // No launch lock there: its creator let it go, or took none.
    assert_eq!(files.hand_offs(), [kept(true, false)]);
    // The sweep between Create and Launch: its creator holds the lock.
    let held = files
        .launch_locks()
        .hold_run(&daemon_api_client::launch_lock_name(CHILD))
        .unwrap();
    assert_eq!(files.hand_offs(), [kept(true, true)]);
    // Two launches of one child: the second cannot take the lock.
    assert!(matches!(
        files
            .launch_locks()
            .hold_run(&daemon_api_client::launch_lock_name(CHILD)),
        Err(run_ports::LockError::Held(_))
    ));
    drop(held);
    // A creator crashed mid-launch: its lock file is left, held by no one.
    let lock = files
        .launch_locks()
        .path(&daemon_api_client::launch_lock_name(CHILD));
    fixtures::put_mode(&lock, "", 0o600);
    assert_eq!(files.hand_offs(), [kept(true, false)], "proven gone");
    files.claim_hand_off(CHILD);
    assert_eq!(files.hand_offs(), [kept(false, false)], "claimed");
    assert!(files.read_hand_off(CHILD).is_some(), "kept");
    files.launching(CHILD, &HandOff::default()).unwrap();
    files.delete_hand_off(CHILD);
    assert!(files.hand_offs().is_empty(), "deleted with its mark");
    let left = fixtures::entries(files.dir().join("launch"));
    assert_eq!(left, [("not-a-child".to_string(), false)], "{left:?}");
}

// One daemon per OS account: the single-instance lock is where
// `instance_lock_path` says, a test's override first, and the discovery file
// is beside it; the rest stays under the ctxloom home.
#[test]
fn the_instance_lock_is_where_the_override_says_and_the_discovery_beside_it() {
    let (root, home) = home();
    let shared = root.root().join("instance");
    assert_eq!(
        instance_lock_path(&home, None),
        home.join(".ctxloom/daemon/daemon.lock")
    );
    assert_eq!(
        instance_lock_path(&home, Some(&shared)),
        shared.join("daemon.lock")
    );
    let files = DaemonFiles::with_instance(
        fixtures::fs(),
        &home,
        instance_lock_path(&home, Some(&shared)),
    );
    files.publish(&sample()).unwrap();
    let fs = fixtures::fs();
    assert!(fs.exists(&shared.join("daemon.discovery")).unwrap());
    assert!(!fs
        .exists(&home.join(".ctxloom/daemon/daemon.discovery"))
        .unwrap());
    assert_eq!(files.dir(), home.join(".ctxloom/daemon"));
}

// Two ctxloom homes of one account share its one lock: one holder.
#[test]
fn two_homes_on_one_instance_lock_are_one_holder() {
    let (root, home) = home();
    let other = root.root().join("other");
    let lock = instance_lock_path(&home, Some(&root.root().join("instance")));
    let first = DaemonFiles::with_instance(fixtures::fs(), &home, lock.clone());
    let second = DaemonFiles::with_instance(fixtures::fs(), &other, lock);
    let _held = first
        .hold_instance(Duration::ZERO)
        .expect("the first holds it");
    assert!(second.hold_instance(Duration::ZERO).is_none());
}

// The replaced-lock gap: a lock file removed, or replaced, under its holder
// excludes nobody, so the holder is told it no longer holds the lock.
#[test]
fn a_lock_file_replaced_or_removed_under_its_holder_is_no_longer_held() {
    let (root, home) = home();
    let files = files(&root, &home);
    let fs = fixtures::fs();
    let lock = home.join(".ctxloom/daemon/daemon.lock");
    let held = files.hold_instance(Duration::ZERO).expect("held");
    assert!(files.still_holds(&held), "its own file is there");
    fs.remove_file(&lock).unwrap();
    assert!(!files.still_holds(&held), "removed");
    let replaced = files
        .hold_instance(Duration::ZERO)
        .expect("a new file, a new lock");
    assert!(!files.still_holds(&held), "replaced by another");
    assert!(files.still_holds(&replaced));
}

// OD-60: a daemon whose home is deleted exits, the single-instance lock
// elsewhere or not; so its files say when the home's ctxloom directory is
// gone, and only then.
#[test]
fn the_home_is_gone_only_once_its_ctxloom_directory_is() {
    let (root, home) = home();
    let fs = fixtures::fs();
    let elsewhere = root.root().join("account");
    let files = DaemonFiles::with_instance(
        fs.clone(),
        &home,
        instance_lock_path(&home, Some(&elsewhere)),
    );
    let _held = files.hold_instance(Duration::ZERO).expect("held");
    fs.create_dir_all(&home.join(".ctxloom"), os::Mode::from_bits(0o700))
        .unwrap();
    assert!(!files.home_gone(), "its home is there");
    fs.remove_dir_all(&home).unwrap();
    assert!(files.home_gone(), "its home was removed");
    assert!(
        fs.exists(&elsewhere.join("daemon.lock")).unwrap(),
        "the lock elsewhere is left"
    );
}

// A daemon that holds the lock and advertises itself but does not answer
// its health check is the silent holder `daemon status` reports; one that
// answers, one whose lock is free (a stale file left by a killed daemon)
// and one that advertises nothing are not.
#[test]
fn a_silent_holder_is_an_advertised_daemon_that_holds_the_lock_and_does_not_answer() {
    let (root, home) = home();
    let files = files(&root, &home);
    assert_eq!(files.silent_holder(|_| false), None, "nothing advertised");
    files.publish(&sample()).unwrap();
    assert_eq!(
        files.silent_holder(|_| false),
        None,
        "its lock is free: a stale file"
    );
    let held = files.hold_instance(Duration::ZERO).expect("held");
    assert_eq!(files.silent_holder(|_| true), None, "it answers");
    assert_eq!(files.silent_holder(|_| false), Some(sample()));
    drop(held);
}
