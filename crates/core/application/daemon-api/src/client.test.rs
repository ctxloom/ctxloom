//! M3: a client of another version stops an idle daemon, and is refused by
//! a busy one.

use std::sync::Mutex;

use daemon_ports::{Any, Called, DaemonApi};
use daemon_proto::v1;
use exit_code::{Exit, ExitClass};

use super::{newer, reconcile, reconcile_session, resolve, stop, CallError, Reconciled, SkewError};

/// A daemon that hosts `hosted`, answers `stop` as told, and keeps what it
/// was asked.
#[derive(Default)]
struct Daemon {
    hosted: Vec<&'static str>,
    /// How `GetDaemon` is answered: none, the daemon as it is.
    get: Option<Called>,
    stop: Option<Called>,
    asked: Mutex<Vec<&'static str>>,
    gets: Mutex<Vec<v1::GetDaemonRequest>>,
    stops: Mutex<Vec<v1::StopDaemonRequest>>,
}

impl Daemon {
    fn about(&self) -> v1::Daemon {
        v1::Daemon {
            runs: self
                .hosted
                .iter()
                .map(|harp| v1::HostedRun {
                    harp: harp.to_string(),
                    ..v1::HostedRun::default()
                })
                .collect(),
            name: "daemon".to_string(),
            ..v1::Daemon::default()
        }
    }
}

impl DaemonApi for Daemon {
    async fn get_daemon(&self, r: v1::GetDaemonRequest) -> Result<v1::Daemon, Called> {
        self.asked.lock().unwrap().push("get");
        self.gets.lock().unwrap().push(r);
        match &self.get {
            Some(called) => Err(called.clone()),
            None => Ok(self.about()),
        }
    }

    async fn stop_daemon(&self, r: v1::StopDaemonRequest) -> Result<v1::Daemon, Called> {
        self.asked.lock().unwrap().push("stop");
        self.stops.lock().unwrap().push(r);
        match &self.stop {
            Some(called) => Err(called.clone()),
            None => Ok(self.about()),
        }
    }

    async fn resolve_project(&self, r: v1::ResolveProjectRequest) -> Result<v1::Project, Called> {
        match r.name.as_str() {
            "" => Ok(v1::Project {
                name: "projects/calm-otter".to_string(),
                ..v1::Project::default()
            }),
            _ => Err(Called::Refused {
                code: 9,
                refusal: Any::from_msg(&sessions_proto::v1::ProjectHarpMismatch {
                    project: "projects/calm-otter".to_string(),
                    expected: "far-heron".to_string(),
                })
                .unwrap(),
            }),
        }
    }

    async fn get_project(&self, _: v1::GetProjectRequest) -> Result<v1::Project, Called> {
        unreachable!()
    }

    async fn get_session(&self, _: v1::GetSessionRequest) -> Result<v1::Session, Called> {
        unreachable!()
    }

    async fn list_sessions(
        &self,
        _: v1::ListSessionsRequest,
    ) -> Result<v1::ListSessionsResponse, Called> {
        unreachable!()
    }

    async fn search_sessions(
        &self,
        _: v1::SearchSessionsRequest,
    ) -> Result<v1::ListSessionsResponse, Called> {
        unreachable!()
    }
}

// The same stamp: the daemon is used, and asked nothing.
#[tokio::test]
async fn the_same_stamp_uses_the_daemon() {
    let daemon = Daemon::default();
    assert_eq!(
        reconcile(&daemon, "0.1.0+a", "0.1.0+a").await,
        Ok(Reconciled::Same)
    );
    assert!(daemon.asked.lock().unwrap().is_empty());
}

// Another stamp, hosting nothing: asked, then stopped.
#[tokio::test]
async fn another_stamp_stops_an_idle_daemon() {
    let daemon = Daemon::default();
    assert_eq!(
        reconcile(&daemon, "0.2.0+b", "0.1.0+a").await,
        Ok(Reconciled::Stopped)
    );
    assert_eq!(*daemon.asked.lock().unwrap(), ["get", "stop"]);
    // It asks the daemon by its name, and stops the daemon it was told of.
    assert_eq!(daemon.gets.lock().unwrap()[0].name, "daemon");
    let stopped = &daemon.stops.lock().unwrap()[0];
    assert_eq!(stopped.name, "daemon");
    assert!(!stopped.even_if_live);
}

// Another stamp, hosting live runs: refused (exit 20), naming them, and the
// daemon is not stopped.
#[tokio::test]
async fn another_stamp_is_refused_by_a_busy_daemon() {
    let daemon = Daemon {
        hosted: vec!["brisk-copper-moth"],
        ..Daemon::default()
    };
    let refused = reconcile(&daemon, "0.2.0+b", "0.1.0+a").await.unwrap_err();
    assert_eq!(
        refused,
        SkewError::Mismatch {
            own: "0.2.0+b".to_string(),
            daemon: "0.1.0+a".to_string(),
            harps: vec!["brisk-copper-moth".to_string()],
        }
    );
    assert_eq!(refused.exit(), Exit::REFUSED);
    assert_eq!(*daemon.asked.lock().unwrap(), ["get"]);
}

// A run that started between the two calls: the daemon refuses the stop
// (DaemonHosting), and the client is refused as by a busy one.
#[tokio::test]
async fn a_stop_refused_for_live_runs_is_a_busy_daemon() {
    let hosting = v1::DaemonHosting {
        runs: 1,
        harps: vec!["brisk-copper-moth".to_string()],
    };
    let daemon = Daemon {
        stop: Some(Called::Refused {
            code: 9,
            refusal: Any::from_msg(&hosting).unwrap(),
        }),
        ..Daemon::default()
    };
    let refused = reconcile(&daemon, "0.2.0+b", "0.1.0+a").await.unwrap_err();
    assert!(
        matches!(&refused, SkewError::Mismatch { harps, .. } if harps == &["brisk-copper-moth"]),
        "{refused:?}"
    );
}

/// Stamps of the versionator shape: an older and a newer version.
const OLDER: &str = "v0.1.0-a1";
const NEWER: &str = "v0.2.0-b2";

/// A daemon that hosts `hosted`.
fn hosting(hosted: Vec<&'static str>) -> Daemon {
    Daemon {
        hosted,
        ..Daemon::default()
    }
}

// OD-61/OD-62 (owner, 2026-11-15): no client drains a daemon for its
// version. A NEWER client meeting a busy older daemon is refused as by any
// busy daemon of another stamp (its fix the plain stop), and asks nothing
// of it; an older client alike.
#[tokio::test]
async fn no_client_drains_a_busy_daemon_of_another_version() {
    for (own, theirs) in [(NEWER, OLDER), (OLDER, NEWER)] {
        let daemon = hosting(vec!["brisk-copper-moth"]);
        let refused = reconcile(&daemon, own, theirs).await.unwrap_err();
        assert_eq!(
            refused,
            SkewError::Mismatch {
                own: own.to_string(),
                daemon: theirs.to_string(),
                harps: vec!["brisk-copper-moth".to_string()],
            }
        );
        assert!(daemon.stops.lock().unwrap().is_empty(), "{own}");
    }
}

// A newer client meeting an idle older daemon stops it plainly (never a
// drain), and starts its own.
#[tokio::test]
async fn a_newer_client_stops_an_idle_daemon_plainly() {
    let daemon = Daemon::default();
    assert_eq!(
        reconcile(&daemon, NEWER, OLDER).await,
        Ok(Reconciled::Stopped)
    );
    let stops = daemon.stops.lock().unwrap();
    assert_eq!(stops.len(), 1, "{stops:?}");
    assert!(!stops[0].drain && !stops[0].even_if_live, "{stops:?}");
}

// A daemon that cannot be asked is a failure (exit 10).
#[tokio::test]
async fn a_daemon_that_cannot_be_asked_fails() {
    let daemon = Daemon {
        stop: Some(Called::Unauthenticated),
        ..Daemon::default()
    };
    let failed = reconcile(&daemon, "0.2.0+b", "0.1.0+a").await.unwrap_err();
    assert_eq!(failed, SkewError::Call(Called::Unauthenticated));
    assert_eq!(failed.exit(), Exit::FAILED);
}

// `daemon stop`: a refusal is told in its crate's words, a decline.
#[tokio::test]
async fn a_refused_stop_is_told_in_its_words_and_declines() {
    let hosting = v1::DaemonHosting {
        runs: 1,
        harps: vec!["brisk-copper-moth".to_string()],
    };
    let daemon = Daemon {
        stop: Some(Called::Refused {
            code: 9,
            refusal: Any::from_msg(&hosting).unwrap(),
        }),
        ..Daemon::default()
    };
    let refused = stop(&daemon, false).await.unwrap_err();
    assert_eq!(
        angzarr_diagnostic::Diagnostic::code(&refused),
        "daemon-api-daemon-hosting"
    );
    assert_eq!(refused.exit(), Exit::REFUSED);
    assert!(stop(&Daemon::default(), false).await.is_ok());
    let failed = stop(
        &Daemon {
            stop: Some(Called::Unauthenticated),
            ..Daemon::default()
        },
        false,
    )
    .await
    .unwrap_err();
    assert_eq!(failed, CallError::Call(Called::Unauthenticated));
    assert_eq!(failed.exit(), Exit::FAILED);
}

// A registration's refusal is the sessions domain's, told in its words.
#[tokio::test]
async fn a_refused_registration_is_the_sessions_domains() {
    let daemon = Daemon::default();
    let named = resolve(&daemon, v1::ResolveProjectRequest::default()).await;
    assert_eq!(named.unwrap().name, "projects/calm-otter");
    let refused = resolve(
        &daemon,
        v1::ResolveProjectRequest {
            name: "projects/far-heron".to_string(),
            ..v1::ResolveProjectRequest::default()
        },
    )
    .await
    .unwrap_err();
    assert_eq!(
        angzarr_diagnostic::Diagnostic::code(&refused),
        "sessions-project-harp-mismatch"
    );
    assert_eq!(refused.exit(), Exit::REFUSED);
}

// `daemon stop` names the daemon, and says whether it may end live runs.
#[tokio::test]
async fn a_stop_names_the_daemon_and_its_override() {
    let daemon = Daemon::default();
    stop(&daemon, true).await.unwrap();
    stop(&daemon, false).await.unwrap();
    let stops = daemon.stops.lock().unwrap();
    assert_eq!(
        stops
            .iter()
            .map(|s| (s.name.as_str(), s.even_if_live))
            .collect::<Vec<_>>(),
        [("daemon", true), ("daemon", false)]
    );
}

// Each error displays as its code and its args.
#[test]
fn the_errors_display_their_code_and_args() {
    let busy = SkewError::Mismatch {
        own: "0.2.0+b".to_string(),
        daemon: "0.1.0+a".to_string(),
        harps: vec!["brisk-copper-moth".to_string(), "calm-otter".to_string()],
    };
    assert_eq!(
        busy.to_string(),
        "daemon-api-version-skew own=0.2.0+b daemon=0.1.0+a harps=\"brisk-copper-moth, calm-otter\""
    );
    assert_eq!(
        mismatch("0.2.0+b", "0.1.0+a").to_string(),
        "daemon-api-version-skew own=0.2.0+b daemon=0.1.0+a harps=nothing"
    );
    let failed = Called::Failed {
        code: 13,
        detail: "the store".to_string(),
    };
    assert_eq!(
        SkewError::Call(failed.clone()).to_string(),
        "daemon-ports-failed code=13 ext_err_txt=\"the store\""
    );
    assert_eq!(
        CallError::Call(failed).to_string(),
        "daemon-ports-failed code=13 ext_err_txt=\"the store\""
    );
    let refused = CallError::from(Called::Refused {
        code: 9,
        refusal: Any::from_msg(&v1::DaemonHosting {
            runs: 1,
            harps: vec!["brisk-copper-moth".to_string()],
        })
        .unwrap(),
    });
    assert_eq!(
        refused.to_string(),
        "daemon-api-daemon-hosting runs=1 harps=brisk-copper-moth"
    );
}

// ---------------------------------------------------------------- OD-3: a session's call

/// Two stamps of versionator's shape, of the versions given.
const OLD: &str = "v0.1.0-abc1234";
const NEW: &str = "v0.2.0-def5678";

fn mismatch(own: &str, daemon: &str) -> SkewError {
    SkewError::Mismatch {
        own: own.to_string(),
        daemon: daemon.to_string(),
        harps: Vec::new(),
    }
}

/// The refusal of a busy daemon of another stamp, hosting the child.
fn busy(own: &str, daemon: &str) -> SkewError {
    SkewError::Mismatch {
        own: own.to_string(),
        daemon: daemon.to_string(),
        harps: vec!["brisk-copper-moth".to_string()],
    }
}

// The same stamp: the daemon is used, and asked nothing.
#[tokio::test]
async fn a_sessions_call_uses_a_daemon_of_its_own_stamp() {
    let daemon = Daemon::default();
    assert_eq!(
        reconcile_session(&daemon, NEW, NEW).await,
        Ok(Reconciled::Same)
    );
    assert!(daemon.asked.lock().unwrap().is_empty());
}

// OD-3: a session's call of a NEWER version, meeting an idle older daemon,
// asks it to stop (a stop that ends nothing: never `even_if_live`, never a
// drain), so the caller starts its own and its call goes there.
#[tokio::test]
async fn a_newer_sessions_call_stops_an_idle_older_daemon() {
    let daemon = Daemon::default();
    assert_eq!(
        reconcile_session(&daemon, NEW, OLD).await,
        Ok(Reconciled::Stopped)
    );
    assert_eq!(*daemon.asked.lock().unwrap(), ["get", "stop"]);
    let stops = daemon.stops.lock().unwrap();
    assert!(!stops[0].even_if_live, "a stop that ends nothing it hosts");
    assert!(!stops[0].drain, "never a drain (OD-62)");
}

// OD-62: a session's call of a newer version, meeting an older daemon that
// hosts runs, is refused, naming both stamps (exit 20), and asks it nothing.
#[tokio::test]
async fn a_newer_sessions_call_is_refused_by_a_busy_older_daemon() {
    let daemon = Daemon {
        hosted: vec!["brisk-copper-moth"],
        ..Daemon::default()
    };
    let refused = reconcile_session(&daemon, NEW, OLD).await.unwrap_err();
    assert_eq!(refused, busy(NEW, OLD));
    assert_eq!(refused.exit(), Exit::REFUSED);
    assert!(daemon.stops.lock().unwrap().is_empty());
}

// A daemon that refuses the stop for its runs (DaemonHosting: a run started
// between the look and the stop): the call is refused as by a busy daemon,
// and nothing it hosts is ended.
#[tokio::test]
async fn a_run_started_between_the_look_and_the_stop_refuses_the_call() {
    let daemon = Daemon {
        stop: Some(Called::Refused {
            code: 9,
            refusal: Any::from_msg(&v1::DaemonHosting {
                runs: 1,
                harps: vec!["brisk-copper-moth".to_string()],
            })
            .unwrap(),
        }),
        ..Daemon::default()
    };
    let refused = reconcile_session(&daemon, NEW, OLD).await.unwrap_err();
    assert_eq!(refused, busy(NEW, OLD));
    assert_eq!(*daemon.asked.lock().unwrap(), ["get", "stop"]);
    assert!(!daemon.stops.lock().unwrap()[0].even_if_live);
}

// A daemon gone between the discovery and the look (it exited, or was
// killed) cannot be asked: a failed call, which the caller meets by
// starting its own.
#[tokio::test]
async fn a_daemon_gone_before_it_is_asked_is_a_failed_call() {
    let gone = Called::Failed {
        code: 14,
        detail: "connection refused".to_string(),
    };
    let daemon = Daemon {
        get: Some(gone.clone()),
        ..Daemon::default()
    };
    assert_eq!(
        reconcile_session(&daemon, NEW, OLD).await,
        Err(SkewError::Call(gone))
    );
    assert_eq!(*daemon.asked.lock().unwrap(), ["get"]);
}

// A session's call of an OLDER version, or of the same version built
// otherwise, never stops the daemon (no ping-pong between two binaries):
// it is refused, typed, and the daemon asked nothing.
#[tokio::test]
async fn a_sessions_call_not_newer_than_the_daemon_asks_it_nothing() {
    for (own, daemon_stamp) in [
        (OLD, NEW),
        ("v0.2.0-aaaaaaa", "v0.2.0-bbbbbbb"),
        ("v0.2.0-aaaaaaa-20261114T101010-dirty", "v0.2.0-aaaaaaa"),
        ("not a stamp", OLD),
        (NEW, "not a stamp"),
    ] {
        let daemon = Daemon::default();
        let refused = reconcile_session(&daemon, own, daemon_stamp)
            .await
            .unwrap_err();
        assert_eq!(
            refused,
            mismatch(own, daemon_stamp),
            "{own} vs {daemon_stamp}"
        );
        assert!(
            daemon.asked.lock().unwrap().is_empty(),
            "{own} vs {daemon_stamp}"
        );
    }
}

// The skew rule's `newer` is run-ports' one function (s4-Q14, H-Q1), the
// hook's hold judging by the same: re-exported here, where it was named.
#[test]
fn newer_is_run_ports_one_function() {
    for (a, b) in [
        ("v0.2.0-aaaaaaa", "v0.1.0-bbbbbbb"),
        ("v0.1.0-a", "v0.1.0-b"),
    ] {
        assert_eq!(newer(a, b), run_ports::newer(a, b), "{a} vs {b}");
    }
    assert!(newer("v0.2.0-aaaaaaa", "v0.1.0-bbbbbbb"));
}
