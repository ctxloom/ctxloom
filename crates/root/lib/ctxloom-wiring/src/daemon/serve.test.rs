//! The ports the root gives the daemon's launcher (T18), each over a
//! temporary home.

use std::collections::BTreeMap;
use std::sync::{Arc, Mutex};

use daemon_application::{ChildHomes, Ears, HandOffs};
use run_ports::{ChildObservation, Exit};

use super::{advertised, said, Advertised, HandOffFiles, Homes, Pumps, Say};
use crate::daemon::files::DaemonFiles;
use crate::fixtures;

const SESSION: &str = "5f0c7a52-2222-5222-8222-000000000002";

// A launcher's hand-off is the child's environment, taken once.
#[test]
fn a_hand_off_is_taken_once_as_it_was_left() {
    let root = fixtures::root();
    let home = root.root().join("home");
    let files = DaemonFiles::new(fixtures::fs(), &home);
    let env = BTreeMap::from([("KEY".to_string(), "v".to_string())]);
    files.hand_off(SESSION, &env).unwrap();
    let hand_offs = HandOffFiles(DaemonFiles::new(fixtures::fs(), &home));
    assert_eq!(hand_offs.take(SESSION), Some(env));
    assert_eq!(hand_offs.take(SESSION), None, "taken");
}

// A child's session home and its files are in its project's session
// directory.
#[test]
fn a_childs_home_and_its_files_are_in_its_projects_session_directory() {
    let root = fixtures::root();
    let project = root.root().join("work");
    fixtures::mkdirs(&project);
    let homes = Homes(fixtures::fs());
    let dir = project.display().to_string();
    let home = homes.home(&dir, "kid");
    assert_eq!(home, format!("{dir}/.ctxloom/sessions/kid/home"));
    homes
        .write(&dir, "kid", "home/settings.json", b"{}")
        .unwrap();
    assert!(fixtures::exists(
        project.join(".ctxloom/sessions/kid/home/settings.json")
    ));
}

// What a child says is pumped off its own thread and heard, by run, in the
// order said.
#[test]
fn a_childs_observations_are_heard_by_run_in_order() {
    let pumps = Pumps::new();
    let said = vec![
        ChildObservation::Started {
            native_id: "n1".to_string(),
        },
        ChildObservation::Exited {
            exit: Exit {
                code: Some(0),
                signal: None,
            },
            detail: String::new(),
        },
    ];
    pumps.listen("run-1".to_string(), Box::new(said.clone().into_iter()));
    let mut heard = Vec::new();
    for _ in 0..200 {
        heard.extend(pumps.heard());
        if heard.len() == said.len() {
            break;
        }
        std::thread::sleep(std::time::Duration::from_millis(10));
    }
    let expected: Vec<(String, ChildObservation)> =
        said.into_iter().map(|o| ("run-1".to_string(), o)).collect();
    assert_eq!(heard, expected);
}

// The log is the one writer the loop and the launcher share.
#[test]
fn a_line_said_reaches_the_log() {
    let lines = Arc::new(Mutex::new(Vec::new()));
    let kept = Arc::clone(&lines);
    let say: Say = Arc::new(Mutex::new(Box::new(move |line: &str| {
        kept.lock().unwrap().push(line.to_string());
    })));
    said(&say, "serving");
    assert_eq!(*lines.lock().unwrap(), ["serving"]);
}

// M11: the lock is the single-instance lock alone; the discovery file is how a daemon
// is found. Its own is left be, one gone is published again (it still holds
// the single-instance lock), and another daemon's means this one lost it.
#[test]
fn a_daemon_keeps_its_advertisement_republishes_a_gone_one_and_yields_to_anothers() {
    let own = run_ports::Discovery {
        stamp: "v0.1.0-abc1234".to_string(),
        pid: 7,
        endpoint: "https://127.0.0.1:4242".to_string(),
        cert: run_ports::PublicCert::from_der(vec![0x30, 0x00]).unwrap(),
        bearer: run_ports::Token::from_bytes([7; 32]),
    };
    assert_eq!(advertised(&own, Some(&own)), Advertised::Ours);
    assert_eq!(advertised(&own, None), Advertised::Gone);
    let another = run_ports::Discovery {
        bearer: run_ports::Token::from_bytes([8; 32]),
        ..own.clone()
    };
    assert_eq!(advertised(&own, Some(&another)), Advertised::Replaced);
    let moved = run_ports::Discovery {
        endpoint: "https://127.0.0.1:4343".to_string(),
        ..own.clone()
    };
    assert_eq!(advertised(&own, Some(&moved)), Advertised::Replaced);
}
