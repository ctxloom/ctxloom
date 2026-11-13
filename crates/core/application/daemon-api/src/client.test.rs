//! M3: a client of another version stops an idle daemon, and is refused by
//! a busy one.

use std::sync::Mutex;

use daemon_ports::{Any, Called, DaemonApi};
use daemon_proto::v1;
use exit_code::{Exit, ExitClass};

use super::{reconcile, resolve, stop, CallError, Reconciled, SkewError};

/// A daemon that hosts `hosted`, answers `stop` as told, and keeps what it
/// was asked.
#[derive(Default)]
struct Daemon {
    hosted: Vec<&'static str>,
    stop: Option<Called>,
    asked: Mutex<Vec<&'static str>>,
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
            ..v1::Daemon::default()
        }
    }
}

impl DaemonApi for Daemon {
    async fn get_daemon(&self, _: v1::GetDaemonRequest) -> Result<v1::Daemon, Called> {
        self.asked.lock().unwrap().push("get");
        Ok(self.about())
    }

    async fn stop_daemon(&self, _: v1::StopDaemonRequest) -> Result<v1::Daemon, Called> {
        self.asked.lock().unwrap().push("stop");
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
        SkewError::Busy {
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
        matches!(&refused, SkewError::Busy { harps, .. } if harps == &["brisk-copper-moth"]),
        "{refused:?}"
    );
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
