//! A command as its daemon's client: reached, its project resolved, its
//! environment sent, and retried once on a new daemon (S6-S8, S14, S16).

use std::sync::{Arc, Mutex};

use daemon_ports::{Called, DaemonApi, Daemons, Unreached};
use daemon_proto::v1;
use exit_code::{Exit, ExitClass};

use super::{Client, Removed, SessionReads, SessionRemovals};
use crate::CallError;

/// What every daemon of a test was asked, in order, `<daemon>:<rpc>`.
type Asked = Arc<Mutex<Vec<String>>>;

/// A daemon numbered `n`: its project is `projects/calm-otter`, it lists
/// one session, and it answers `fail` (once) where told.
#[derive(Clone)]
struct Fake {
    n: usize,
    asked: Asked,
    /// Each request's environment, as it came.
    envs: Arc<Mutex<Vec<Option<v1::EnvVars>>>>,
    /// The requests' resource names and parents, as they came.
    names: Arc<Mutex<Vec<String>>>,
    /// The daemons that cannot be reached mid-call.
    dying: Arc<Mutex<Vec<usize>>>,
    /// The writes' request ids, as they came, `<daemon>:<id>`.
    request_ids: Arc<Mutex<Vec<String>>>,
}

impl Fake {
    fn ask(&self, rpc: &str, env: Option<v1::EnvVars>) -> Result<(), Called> {
        self.asked.lock().unwrap().push(format!("{}:{rpc}", self.n));
        self.envs.lock().unwrap().push(env);
        match self.dying.lock().unwrap().contains(&self.n) && rpc != "resolve" {
            true => Err(Called::Unreachable {
                detail: "gone".to_string(),
            }),
            false => Ok(()),
        }
    }
}

impl DaemonApi for Fake {
    async fn get_daemon(&self, r: v1::GetDaemonRequest) -> Result<v1::Daemon, Called> {
        self.ask("get", r.env)?;
        Ok(v1::Daemon {
            pid: 7,
            ..v1::Daemon::default()
        })
    }

    async fn stop_daemon(&self, r: v1::StopDaemonRequest) -> Result<v1::Daemon, Called> {
        self.ask(&format!("stop {}", r.even_if_live), r.env)?;
        self.names.lock().unwrap().push(r.name);
        Ok(v1::Daemon::default())
    }

    async fn resolve_project(&self, r: v1::ResolveProjectRequest) -> Result<v1::Project, Called> {
        self.ask("resolve", r.env)?;
        self.names.lock().unwrap().push(r.project_id);
        Ok(v1::Project {
            name: "projects/calm-otter".to_string(),
            ..v1::Project::default()
        })
    }

    async fn get_project(&self, _: v1::GetProjectRequest) -> Result<v1::Project, Called> {
        unreachable!()
    }

    async fn get_session(&self, r: v1::GetSessionRequest) -> Result<v1::Session, Called> {
        self.ask("get-session", r.env)?;
        self.names.lock().unwrap().push(r.name.clone());
        Ok(v1::Session {
            name: r.name,
            ..v1::Session::default()
        })
    }

    async fn list_sessions(
        &self,
        r: v1::ListSessionsRequest,
    ) -> Result<v1::ListSessionsResponse, Called> {
        self.ask(&format!("list {}", r.state), r.env)?;
        self.names.lock().unwrap().push(r.parent);
        Ok(v1::ListSessionsResponse {
            sessions: vec![v1::Session::default()],
            ..v1::ListSessionsResponse::default()
        })
    }

    async fn search_sessions(
        &self,
        r: v1::SearchSessionsRequest,
    ) -> Result<v1::ListSessionsResponse, Called> {
        self.ask(&format!("search {} {}", r.state, r.words.join(" ")), r.env)?;
        self.names.lock().unwrap().push(r.parent);
        Ok(v1::ListSessionsResponse::default())
    }

    async fn delete_session(
        &self,
        r: v1::DeleteSessionRequest,
    ) -> Result<v1::DeleteSessionResponse, Called> {
        let rpc = format!("delete {} {}", r.validate_only, r.even_if_live);
        self.request_ids
            .lock()
            .unwrap()
            .push(format!("{}:{}", self.n, r.request_id));
        self.ask(&rpc, r.env)?;
        self.names.lock().unwrap().push(r.name.clone());
        let plan = v1::DeleteSessionResponse {
            name: r.name.clone(),
            harp: "brisk-copper-moth".to_string(),
            authored: vec!["notes.md".to_string()],
            ..v1::DeleteSessionResponse::default()
        };
        if r.name.ends_with("/kept-heron") {
            let refusal = crate::ApiRefusal::of(&v1::RemovalRefused {
                name: r.name,
                reason: v1::RemovalRefusal::Kept as i32,
                plan: Some(plan),
                ..v1::RemovalRefused::default()
            });
            return Err(Called::Refused {
                code: refusal.code,
                refusal: refusal.refusal,
            });
        }
        Ok(plan)
    }

    async fn sweep_sessions(
        &self,
        r: v1::SweepSessionsRequest,
    ) -> Result<v1::SweepSessionsResponse, Called> {
        let rpc = format!("sweep {} {}", r.validate_only, r.older_than);
        self.request_ids
            .lock()
            .unwrap()
            .push(format!("{}:{}", self.n, r.request_id));
        self.ask(&rpc, r.env)?;
        self.names.lock().unwrap().push(r.parent);
        self.names.lock().unwrap().push(r.project);
        Ok(v1::SweepSessionsResponse::default())
    }
}

/// The daemons a test reaches: `serving` says whether one serves at first;
/// each reach is the next daemon, or `unreached` when it is set.
#[derive(Clone)]
struct Fakes {
    serving: bool,
    unreached: Option<Unreached>,
    reached: Arc<Mutex<usize>>,
    template: Fake,
}

impl Fakes {
    fn new() -> Fakes {
        Fakes {
            serving: true,
            unreached: None,
            reached: Arc::new(Mutex::new(0)),
            template: Fake {
                n: 0,
                asked: Asked::default(),
                envs: Arc::default(),
                names: Arc::default(),
                dying: Arc::default(),
                request_ids: Arc::default(),
            },
        }
    }

    fn asked(&self) -> Vec<String> {
        self.template.asked.lock().unwrap().clone()
    }

    fn names(&self) -> Vec<String> {
        self.template.names.lock().unwrap().clone()
    }

    fn reaches(&self) -> usize {
        *self.reached.lock().unwrap()
    }
}

impl Daemons for Fakes {
    type Api = Fake;

    async fn serving(&self) -> Result<Option<Fake>, Unreached> {
        if let Some(unreached) = &self.unreached {
            return Err(unreached.clone());
        }
        Ok(self.serving.then(|| self.template.clone()))
    }

    async fn reach(&self) -> Result<Fake, Unreached> {
        if let Some(unreached) = &self.unreached {
            return Err(unreached.clone());
        }
        let mut reached = self.reached.lock().unwrap();
        *reached += 1;
        Ok(Fake {
            n: *reached,
            ..self.template.clone()
        })
    }
}

fn env() -> v1::EnvVars {
    v1::EnvVars {
        vars: vec![v1::EnvVar {
            name: "CLAUDE_CONFIG_DIR".to_string(),
            value: "/home/alice/.claude-work".to_string(),
        }],
    }
}

fn in_project(daemons: &Fakes) -> Client<Fakes> {
    let project = v1::ResolveProjectRequest {
        project_id: "p-1".to_string(),
        root: "/work".to_string(),
        ..v1::ResolveProjectRequest::default()
    };
    Client::new(daemons.clone(), Some(project), "/work".to_string(), env())
}

fn outside(daemons: &Fakes) -> Client<Fakes> {
    Client::new(daemons.clone(), None, "/tmp".to_string(), env())
}

// A listing reaches a daemon, resolves its project with it first (S6), and
// lists under the project the daemon named; every request carries the
// caller's whole environment (S7).
#[tokio::test]
async fn a_listing_resolves_its_project_first_and_sends_its_environment() {
    let daemons = Fakes::new();
    let listed = in_project(&daemons)
        .list_sessions(false, v1::SessionState::Live)
        .await
        .unwrap();
    assert_eq!(listed.sessions.len(), 1);
    assert_eq!(daemons.asked(), ["1:resolve", "1:list 1"]);
    assert_eq!(daemons.names(), ["p-1", "projects/calm-otter"]);
    let envs = daemons.template.envs.lock().unwrap().clone();
    assert_eq!(envs, [Some(env()), Some(env())]);
}

// --all lists every project's; in a project it is resolved all the same,
// and outside one there is nothing to resolve.
#[tokio::test]
async fn every_projects_listing_is_the_wildcard_parent() {
    let daemons = Fakes::new();
    in_project(&daemons)
        .list_sessions(true, v1::SessionState::Unspecified)
        .await
        .unwrap();
    assert_eq!(daemons.asked(), ["1:resolve", "1:list 0"]);
    assert_eq!(daemons.names(), ["p-1", "projects/-"]);
    let outside_daemons = Fakes::new();
    outside(&outside_daemons)
        .list_sessions(true, v1::SessionState::Unspecified)
        .await
        .unwrap();
    assert_eq!(outside_daemons.asked(), ["1:list 0"]);
    assert_eq!(outside_daemons.names(), ["projects/-"]);
}

// Outside a project, a read of this project's is refused (exit 20) before
// it asks the daemon anything but where it is.
#[tokio::test]
async fn outside_a_project_its_own_listing_is_refused() {
    let daemons = Fakes::new();
    let refused = outside(&daemons)
        .list_sessions(false, v1::SessionState::Unspecified)
        .await
        .unwrap_err();
    assert_eq!(
        angzarr_diagnostic::Diagnostic::code(&refused),
        "daemon-api-project-unknown"
    );
    assert_eq!(refused.exit(), Exit::REFUSED);
    assert!(daemons.asked().is_empty());
}

// show and search address the project the daemon named.
#[tokio::test]
async fn show_and_search_address_the_resolved_project() {
    let daemons = Fakes::new();
    let client = in_project(&daemons);
    let shown = client.get_session("amber-swift-owl").await.unwrap();
    assert_eq!(shown.name, "projects/calm-otter/sessions/amber-swift-owl");
    client
        .search_sessions(
            vec!["lock".to_string(), "file".to_string()],
            false,
            v1::SessionState::Live,
        )
        .await
        .unwrap();
    assert_eq!(
        daemons.asked(),
        [
            "1:resolve",
            "1:get-session",
            "2:resolve",
            "2:search 1 lock file"
        ]
    );
    let envs = daemons.template.envs.lock().unwrap().clone();
    assert_eq!(envs, vec![Some(env()); 4]);
}

// S8: a daemon that dies mid-call is replaced: the call is made once more,
// whole, on the daemon reached again; and only once.
#[tokio::test]
async fn a_call_whose_daemon_dies_is_made_once_more_on_a_new_one() {
    let daemons = Fakes::new();
    daemons.template.dying.lock().unwrap().push(1);
    in_project(&daemons)
        .list_sessions(false, v1::SessionState::Unspecified)
        .await
        .unwrap();
    assert_eq!(
        daemons.asked(),
        ["1:resolve", "1:list 0", "2:resolve", "2:list 0"]
    );
    assert_eq!(daemons.reaches(), 2);
    let twice = Fakes::new();
    twice.template.dying.lock().unwrap().extend([1, 2]);
    let failed = in_project(&twice)
        .list_sessions(false, v1::SessionState::Unspecified)
        .await
        .unwrap_err();
    assert!(
        matches!(failed, CallError::Call(Called::Unreachable { .. })),
        "{failed:?}"
    );
    assert_eq!(failed.exit(), Exit::FAILED);
    assert_eq!(twice.reaches(), 2, "retried once, not again");
}

// A daemon that could not be started is a failure (10); one that serves
// and declines this client is a decline (20).
#[tokio::test]
async fn an_unreached_daemon_is_a_failure_or_a_decline() {
    let mut daemons = Fakes::new();
    daemons.unreached = Some(Unreached::NotStarted {
        log: "~/.ctxloom/daemon/daemon.log".to_string(),
    });
    let failed = in_project(&daemons)
        .get_session("amber-swift-owl")
        .await
        .unwrap_err();
    assert_eq!(
        angzarr_diagnostic::Diagnostic::code(&failed),
        "daemon-ports-not-started"
    );
    assert_eq!(failed.exit(), Exit::FAILED);
    let declined: Arc<dyn angzarr_diagnostic::Diagnostic + Send + Sync> =
        Arc::new(crate::ApiRejection::ProjectUnknown {
            cwd: "/tmp".to_string(),
        });
    daemons.unreached = Some(Unreached::Declined(declined));
    let refused = in_project(&daemons)
        .get_session("amber-swift-owl")
        .await
        .unwrap_err();
    assert_eq!(refused.exit(), Exit::REFUSED);
    assert!(daemons.asked().is_empty());
}

// S14: status and stop never start a daemon: with none serving, status
// says none and stop does nothing.
#[tokio::test]
async fn status_and_stop_start_no_daemon() {
    let mut daemons = Fakes::new();
    daemons.serving = false;
    assert_eq!(outside(&daemons).status().await.unwrap(), None);
    assert_eq!(outside(&daemons).stop(true).await.unwrap(), None);
    assert_eq!(daemons.reaches(), 0);
    assert!(daemons.asked().is_empty());
    daemons.serving = true;
    let status = outside(&daemons).status().await.unwrap();
    assert_eq!(status.map(|d| d.pid), Some(7));
    outside(&daemons).stop(true).await.unwrap();
    assert_eq!(daemons.asked(), ["0:get", "0:stop true"]);
    assert_eq!(daemons.names(), ["daemon"]);
    let envs = daemons.template.envs.lock().unwrap().clone();
    assert_eq!(envs, vec![Some(env()); 2]);
    assert_eq!(daemons.reaches(), 0);
}

// A daemon found serving that no longer answers serves no more: status
// says none, and stop has nothing to stop.
#[tokio::test]
async fn a_daemon_gone_since_it_was_found_serves_no_more() {
    let daemons = Fakes::new();
    daemons.template.dying.lock().unwrap().push(0);
    assert_eq!(outside(&daemons).status().await.unwrap(), None);
    assert_eq!(outside(&daemons).stop(false).await.unwrap(), None);
}

// `session remove`: the daemon's DeleteSession, of this project's session,
// its preview `validate_only`, its override and the environment sent.
#[tokio::test]
async fn a_removal_asks_the_daemon_of_this_projects_session() {
    let daemons = Fakes::new();
    let removed = in_project(&daemons)
        .delete_session("brisk-copper-moth", false, true)
        .await
        .unwrap();
    let Removed::Done(plan) = removed else {
        panic!("{removed:?}")
    };
    assert_eq!(plan.authored, ["notes.md"]);
    assert_eq!(daemons.asked(), ["1:resolve", "1:delete true true"]);
    assert_eq!(
        daemons.names(),
        ["p-1", "projects/calm-otter/sessions/brisk-copper-moth"]
    );
    let envs = daemons.template.envs.lock().unwrap().clone();
    assert_eq!(envs.last(), Some(&Some(env())));
    in_project(&daemons)
        .delete_session("brisk-copper-moth", true, false)
        .await
        .unwrap();
    assert_eq!(daemons.asked().last().unwrap(), "2:delete false false");
}

// A refused removal is its plan and why, in the refusing use case's words.
#[tokio::test]
async fn a_refused_removal_carries_its_plan() {
    let daemons = Fakes::new();
    let removed = in_project(&daemons)
        .delete_session("kept-heron", true, false)
        .await
        .unwrap();
    let Removed::Refused { plan, why } = removed else {
        panic!("{removed:?}")
    };
    assert_eq!(plan.authored, ["notes.md"]);
    assert_eq!(
        why,
        crate::RemovalRejection::Session(sessions_application::Refusal::Kept {
            harp: "kept-heron".to_string()
        })
    );
    assert_eq!(why.exit(), Exit::REFUSED);
}

// `session sweep`: this project's (or every project's) sessions, its
// project named for its configuration, its age and preview sent.
#[tokio::test]
async fn a_sweep_names_its_parent_and_its_project() {
    let daemons = Fakes::new();
    in_project(&daemons)
        .sweep_sessions(true, Some("90d".to_string()), false)
        .await
        .unwrap();
    assert_eq!(
        daemons.names(),
        ["p-1", "p-1", "projects/-", "projects/calm-otter"]
    );
    assert_eq!(daemons.asked().last().unwrap(), "1:sweep true 90d");
    let envs = daemons.template.envs.lock().unwrap().clone();
    assert_eq!(envs.last(), Some(&Some(env())));
    let outside_daemons = Fakes::new();
    outside(&outside_daemons)
        .sweep_sessions(true, None, true)
        .await
        .unwrap();
    assert_eq!(outside_daemons.names(), ["projects/-", ""]);
    assert_eq!(outside_daemons.asked(), ["1:sweep false "]);
}

/// A client in the project whose request ids are `req-1`, `req-2`, ...
fn numbering(daemons: &Fakes) -> Client<Fakes> {
    let minted = Arc::new(Mutex::new(0_u32));
    in_project(daemons).with_request_ids(Box::new(move || {
        let mut n = minted.lock().unwrap();
        *n += 1;
        format!("req-{n}")
    }))
}

// S8: a removal and a sweep each carry a request id the client chose, and
// a call made once more on a new daemon carries the SAME id, so the daemon
// can answer one already applied with its first result.
#[tokio::test]
async fn a_write_retried_on_a_new_daemon_carries_the_same_request_id() {
    let daemons = Fakes::new();
    daemons.template.dying.lock().unwrap().push(1);
    let client = numbering(&daemons);
    client
        .delete_session("brisk-copper-moth", true, false)
        .await
        .unwrap();
    client.sweep_sessions(false, None, true).await.unwrap();
    assert_eq!(
        daemons.template.request_ids.lock().unwrap().clone(),
        ["1:req-1", "2:req-1", "3:req-2"]
    );
}

// With no ids given, a client sends none (the daemon then answers a retry
// as it finds the session).
#[tokio::test]
async fn a_client_given_no_ids_sends_none() {
    let daemons = Fakes::new();
    in_project(&daemons)
        .delete_session("brisk-copper-moth", true, false)
        .await
        .unwrap();
    assert_eq!(daemons.template.request_ids.lock().unwrap().clone(), ["1:"]);
}
