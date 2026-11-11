//! The daemon's files under a temporary home (S7: never the real one).

use std::time::Duration;

use file_system::Rooted;
use run_ports::{Discovery, PublicCert};

use super::DaemonFiles;
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
        version: "0.1.0".to_string(),
        pid: 7,
        endpoint: "https://127.0.0.1:4242".to_string(),
        cert: PublicCert::from_der(vec![0x30, 0x00]).unwrap(),
    }
}

#[test]
fn the_election_is_one_holder_and_serving_says_whether_one_holds_it() {
    let (root, home) = home();
    let files = files(&root, &home);
    assert!(!files.serving(), "nobody serves yet");
    let held = files.elect(Duration::ZERO).expect("the first elected");
    assert!(files.serving());
    assert!(files.elect(Duration::ZERO).is_none(), "a second is not");
    drop(held);
    assert!(!files.serving(), "the lock dies with its holder");
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
    if fixtures::unix() {
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

#[test]
fn a_stop_is_asked_seen_and_forgotten() {
    let (root, home) = home();
    let files = files(&root, &home);
    assert!(!files.stop_requested());
    files.request_stop().unwrap();
    assert!(files.stop_requested());
    files.clear_stop();
    assert!(!files.stop_requested());
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
// backslashes and `=` alike.
#[test]
fn a_hand_off_reads_back_what_was_written() {
    let sent = env(&[
        ("PATH", "/bin:/usr/bin"),
        ("ODD", "a=b\\nc\nd\re\\"),
        ("EMPTY", ""),
    ]);
    let text = super::encode_env(&sent);
    assert!(text.starts_with("ctxloom-launch-env 1\n"), "{text}");
    assert_eq!(text.lines().count(), 4, "one line a variable: {text}");
    assert_eq!(super::decode_env(&text), Some(sent));
    assert_eq!(super::decode_env("other 1\nA=b\n"), None);
    assert_eq!(super::decode_env("ctxloom-launch-env 1\nnoequals\n"), None);
    assert_eq!(super::decode_env("ctxloom-launch-env 1\nA=bad\\q\n"), None);
    assert_eq!(super::decode_env("ctxloom-launch-env 1\nA=end\\\n"), None);
}

// D-N45: the hand-off is the user's alone, taken once (nothing left at
// rest), withdrawn on a refusal, and only ever named by a child's UUID.
#[test]
fn a_hand_off_is_the_users_alone_and_taken_once() {
    let (root, home) = home();
    let files = files(&root, &home);
    let sent = env(&[("ANTHROPIC_API_KEY", "k")]); // gitleaks:allow
    files.hand_off(CHILD, &sent).unwrap();
    let path = files.dir().join("launch").join(CHILD);
    assert_eq!(
        fixtures::fs().metadata(&path).unwrap().mode.unwrap().bits(),
        0o600
    );
    assert_eq!(files.take_hand_off(CHILD), Some(sent.clone()));
    assert!(!fixtures::fs().exists(&path).unwrap());
    assert_eq!(files.take_hand_off(CHILD), None, "taken once");
    files.hand_off(CHILD, &sent).unwrap();
    files.withdraw_hand_off(CHILD);
    assert_eq!(files.take_hand_off(CHILD), None, "withdrawn");
    assert!(files.hand_off("../escape", &sent).is_err());
    assert_eq!(files.take_hand_off("../escape"), None);
}
