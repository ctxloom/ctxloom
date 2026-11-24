//! A command as its daemon's client: reached, its project resolved, its
//! environment sent, and retried once on a new daemon (S6-S8, S14, S16).

use std::sync::{Arc, Mutex};

use daemon_ports::{Called, DaemonApi, Daemons, Unreached};
use daemon_proto::v1;
use daemon_proto::Carries as _;
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
    /// Each request's caller, as it came (none for a request that carries
    /// no `Caller`).
    envs: Arc<Mutex<Vec<Option<v1::Caller>>>>,
    /// The requests' resource names and parents, as they came.
    names: Arc<Mutex<Vec<String>>>,
    /// The daemons that cannot be reached mid-call.
    dying: Arc<Mutex<Vec<usize>>>,
    /// The writes' request ids, as they came, `<daemon>:<id>`.
    request_ids: Arc<Mutex<Vec<String>>>,
}

impl Fake {
    fn ask(&self, rpc: &str, env: Option<v1::Caller>) -> Result<(), Called> {
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
    async fn get_daemon(&self, _: v1::GetDaemonRequest) -> Result<v1::Daemon, Called> {
        self.ask("get", None)?;
        Ok(v1::Daemon {
            pid: 7,
            ..v1::Daemon::default()
        })
    }

    async fn stop_daemon(&self, r: v1::StopDaemonRequest) -> Result<v1::Daemon, Called> {
        let rpc = match r.drain {
            true => format!("drain {:?} {}", r.drain_timeout_seconds, r.even_if_live),
            false => format!("stop {}", r.even_if_live),
        };
        self.ask(&rpc, None)?;
        self.names.lock().unwrap().push(r.name);
        Ok(v1::Daemon::default())
    }

    async fn resolve_project(&self, r: v1::ResolveProjectRequest) -> Result<v1::Project, Called> {
        self.ask("resolve", r.caller.clone())?;
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
        self.ask("get-session", None)?;
        self.names.lock().unwrap().push(r.name.clone());
        Ok(v1::Session {
            name: r.name,
            ..v1::Session::default()
        })
    }

    async fn redeliver_context(
        &self,
        r: v1::RedeliverContextRequest,
    ) -> Result<v1::RedeliveredContext, Called> {
        self.ask("redeliver", r.caller.clone())?;
        self.names.lock().unwrap().push(format!(
            "{} cwd {} budget {}",
            r.name,
            r.caller_cwd(),
            r.budget
        ));
        Ok(v1::RedeliveredContext {
            context: format!("owed:{}", r.carried_by),
            changed: Vec::new(),
        })
    }

    async fn get_transcript(&self, r: v1::GetTranscriptRequest) -> Result<v1::Transcript, Called> {
        self.ask("get-transcript", None)?;
        self.names.lock().unwrap().push(r.name.clone());
        Ok(v1::Transcript {
            name: r.name,
            entries: vec![v1::LogEntry::default()],
        })
    }

    async fn list_sessions(
        &self,
        r: v1::ListSessionsRequest,
    ) -> Result<v1::ListSessionsResponse, Called> {
        self.ask(&format!("list {}", r.state), None)?;
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
        self.ask(&format!("search {} {}", r.state, r.words.join(" ")), None)?;
        self.names.lock().unwrap().push(r.parent);
        Ok(v1::ListSessionsResponse::default())
    }

    async fn delete_session(
        &self,
        r: v1::DeleteSessionRequest,
    ) -> Result<v1::DeleteSessionResponse, Called> {
        let mut rpc = format!("delete {} {}", r.validate_only, r.even_if_live);
        // An apply of a preview (step 10c U11): its tag and each answer.
        if !r.etag.is_empty() {
            rpc.push_str(&format!(" etag={}", r.etag));
            for answer in &r.answers {
                rpc.push_str(&format!(" {}={:?}", answer.need_id, answer.answer));
            }
        }
        self.request_ids
            .lock()
            .unwrap()
            .push(format!("{}:{}", self.n, r.request_id));
        self.ask(&rpc, r.caller.clone())?;
        self.names.lock().unwrap().push(r.name.clone());
        let plan = v1::DeleteSessionResponse {
            name: r.name.clone(),
            harp: "brisk-copper-moth".to_string(),
            authored: vec!["notes.md".to_string()],
            ..v1::DeleteSessionResponse::default()
        };
        if r.name.ends_with("/kept-heron") {
            let refusal = crate::ApiRefusal::of(&v1::RemovalRefusal {
                name: r.name,
                reason: v1::removal_refusal::Reason::Kept as i32,
                plan: Some(plan),
                ..v1::RemovalRefusal::default()
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
        self.ask(&rpc, r.caller.clone())?;
        self.names.lock().unwrap().push(r.parent);
        self.names.lock().unwrap().push(r.project);
        Ok(v1::SweepSessionsResponse::default())
    }

    async fn assemble_context(
        &self,
        r: v1::AssembleContextRequest,
    ) -> Result<v1::AssembledContext, Called> {
        self.ask("assemble", r.caller.clone())?;
        self.names.lock().unwrap().push(r.name.clone());
        self.names
            .lock()
            .unwrap()
            .push(format!("cwd {}", r.caller_cwd()));
        Ok(v1::AssembledContext {
            context: "ASSEMBLED".to_string(),
            ..v1::AssembledContext::default()
        })
    }

    async fn create_bundle(
        &self,
        r: v1::CreateBundleRequest,
    ) -> Result<v1::CreateBundleResponse, Called> {
        self.ask("create-bundle", r.caller.clone())?;
        self.names.lock().unwrap().push(r.parent.clone());
        self.names
            .lock()
            .unwrap()
            .push(format!("cwd {}", r.caller_cwd()));
        Ok(v1::CreateBundleResponse::default())
    }

    async fn create_run(&self, r: v1::CreateRunRequest) -> Result<v1::Run, Called> {
        self.ask(&format!("run {}", r.validate_only), r.caller.clone())?;
        self.names.lock().unwrap().push(r.parent.clone());
        self.names
            .lock()
            .unwrap()
            .push(format!("cwd {}", r.caller_cwd()));
        Ok(v1::Run {
            context: "PLANNED".to_string(),
            ..v1::Run::default()
        })
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

/// Which client the tests' is.
fn client() -> v1::Client {
    v1::Client {
        kind: v1::client::Kind::Cli as i32,
        program: "ctxloom".to_string(),
        program_dir: "/opt/ctxloom".to_string(),
        version: "v1.2.3-abc".to_string(),
    }
}

/// The tests' client at `cwd`, in [`env`].
fn caller(cwd: &str) -> v1::Caller {
    v1::Caller {
        cwd: cwd.to_string(),
        env: Some(env()),
        client: Some(client()),
        ..v1::Caller::default()
    }
}

fn in_project(daemons: &Fakes) -> Client<Fakes> {
    let project = v1::ResolveProjectRequest {
        project_id: "p-1".to_string(),
        root: "/work".to_string(),
        ..v1::ResolveProjectRequest::default()
    };
    Client::new(daemons.clone(), Some(project), caller("/work"))
}

fn outside(daemons: &Fakes) -> Client<Fakes> {
    Client::new(daemons.clone(), None, caller("/tmp"))
}

// A listing reaches a daemon, resolves its project with it first (S6), and
// lists under the project the daemon named; the resolve carries the
// client's whole caller (its directory, environment and client, owner
// 2026-11-18), the listing (a read by name) none.
#[tokio::test]
async fn a_listing_resolves_its_project_first_and_sends_its_environment() {
    let daemons = Fakes::new();
    let listed = in_project(&daemons)
        .list_sessions(false, v1::session::State::Live)
        .await
        .unwrap();
    assert_eq!(listed.sessions.len(), 1);
    assert_eq!(daemons.asked(), ["1:resolve", "1:list 1"]);
    assert_eq!(daemons.names(), ["p-1", "projects/calm-otter"]);
    let envs = daemons.template.envs.lock().unwrap().clone();
    assert_eq!(envs, [Some(caller("/work")), None]);
}

// --all lists every project's; in a project it is resolved all the same,
// and outside one there is nothing to resolve.
#[tokio::test]
async fn every_projects_listing_is_the_wildcard_parent() {
    let daemons = Fakes::new();
    in_project(&daemons)
        .list_sessions(true, v1::session::State::Unspecified)
        .await
        .unwrap();
    assert_eq!(daemons.asked(), ["1:resolve", "1:list 0"]);
    assert_eq!(daemons.names(), ["p-1", "projects/-"]);
    let outside_daemons = Fakes::new();
    outside(&outside_daemons)
        .list_sessions(true, v1::session::State::Unspecified)
        .await
        .unwrap();
    assert_eq!(outside_daemons.asked(), ["1:list 0"]);
    assert_eq!(outside_daemons.names(), ["projects/-"]);
}

// Outside a project, a read of this project's is refused (exit 48) before
// it asks the daemon anything but where it is.
#[tokio::test]
async fn outside_a_project_its_own_listing_is_refused() {
    let daemons = Fakes::new();
    let refused = outside(&daemons)
        .list_sessions(false, v1::session::State::Unspecified)
        .await
        .unwrap_err();
    assert_eq!(
        angzarr_diagnostic::Diagnostic::code(&refused),
        "daemon-api-project-unknown"
    );
    assert_eq!(refused.exit(), Exit::REFUSED);
    assert!(daemons.asked().is_empty());
}

// Owner ruling 2026-11-14: a session read names no project, so it mints no
// project id, and a project with none has recorded nothing. Its own listing and search are empty,
// asked of no daemon; every project's listing is still every project's, and
// what addresses one of its sessions is refused as outside a project.
#[tokio::test]
async fn an_unnamed_projects_own_listing_and_search_are_empty_and_ask_no_daemon() {
    let daemons = Fakes::new();
    let client = outside(&daemons).unnamed();
    let listed = client
        .list_sessions(false, v1::session::State::Unspecified)
        .await
        .unwrap();
    assert_eq!(listed, v1::ListSessionsResponse::default());
    let found = client
        .search_sessions(
            vec!["x".to_string()],
            false,
            v1::session::State::Unspecified,
        )
        .await
        .unwrap();
    assert_eq!(found, v1::ListSessionsResponse::default());
    assert!(daemons.asked().is_empty(), "{:?}", daemons.asked());
    client
        .list_sessions(true, v1::session::State::Unspecified)
        .await
        .unwrap();
    assert_eq!(daemons.asked(), ["1:list 0"]);
    assert_eq!(daemons.names(), ["projects/-"]);
    let refused = client.get_session("amber-swift-owl").await.unwrap_err();
    assert_eq!(
        angzarr_diagnostic::Diagnostic::code(&refused),
        "daemon-api-project-unknown"
    );
}

// A client in a named project is asked as ever, whatever `unnamed` says.
#[tokio::test]
async fn a_named_project_is_listed_by_its_daemon_even_marked_unnamed() {
    let daemons = Fakes::new();
    in_project(&daemons)
        .unnamed()
        .list_sessions(false, v1::session::State::Unspecified)
        .await
        .unwrap();
    assert_eq!(daemons.asked(), ["1:resolve", "1:list 0"]);
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
            v1::session::State::Live,
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
    assert_eq!(
        envs,
        [Some(caller("/work")), None, Some(caller("/work")), None]
    );
}

// S8: a daemon that dies mid-call is replaced: the call is made once more,
// whole, on the daemon reached again; and only once.
#[tokio::test]
async fn a_call_whose_daemon_dies_is_made_once_more_on_a_new_one() {
    let daemons = Fakes::new();
    daemons.template.dying.lock().unwrap().push(1);
    in_project(&daemons)
        .list_sessions(false, v1::session::State::Unspecified)
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
        .list_sessions(false, v1::session::State::Unspecified)
        .await
        .unwrap_err();
    assert!(
        matches!(failed, CallError::Call(Called::Unreachable { .. })),
        "{failed:?}"
    );
    assert_eq!(failed.exit(), Exit::FAILED);
    assert_eq!(twice.reaches(), 2, "retried once, not again");
}

// A daemon that could not be started is a failure (32); one that serves
// and declines this client is a decline (48).
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
    assert_eq!(
        envs,
        vec![None; 2],
        "the daemon's lifecycle carries no caller"
    );
    assert_eq!(daemons.reaches(), 0);
}

// OD-54 (3): `daemon stop --drain` (and `ctxloom shutdown`, its timeout
// sent) asks the serving daemon to drain (never `even_if_live`), its
// environment sent; with none
// serving it does nothing and starts none.
#[tokio::test]
async fn a_drain_asks_the_serving_daemon_to_hand_over_and_starts_none() {
    let mut daemons = Fakes::new();
    daemons.serving = false;
    assert_eq!(outside(&daemons).drain(None).await.unwrap(), None);
    assert!(daemons.asked().is_empty());
    daemons.serving = true;
    assert!(outside(&daemons)
        .drain(Some(std::time::Duration::from_secs(90)))
        .await
        .unwrap()
        .is_some());
    assert_eq!(daemons.asked(), ["0:drain Some(90) false"]);
    assert_eq!(daemons.names(), ["daemon"]);
    assert_eq!(daemons.reaches(), 0, "it starts no daemon");
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
    assert_eq!(envs.last(), Some(&Some(caller("/work"))));
    in_project(&daemons)
        .delete_session("brisk-copper-moth", true, false)
        .await
        .unwrap();
    assert_eq!(daemons.asked().last().unwrap(), "2:delete false false");
}

// Step 10c U11: the apply of a preview sends its tag and the answers, as
// an apply (not `validate_only`), of this project's session.
#[tokio::test]
async fn a_removals_apply_sends_its_previews_tag_and_answers() {
    let daemons = Fakes::new();
    let answers = vec![v1::Answer {
        need_id: "confirm".to_string(),
        answer: Some(v1::answer::Answer::Confirmed(true)),
    }];
    in_project(&daemons)
        .apply_removal("brisk-copper-moth", true, "e-7", answers)
        .await
        .unwrap();
    assert_eq!(
        daemons.asked(),
        [
            "1:resolve",
            "1:delete false true etag=e-7 confirm=Some(Confirmed(true))"
        ]
    );
    assert_eq!(
        daemons.names().last().unwrap(),
        "projects/calm-otter/sessions/brisk-copper-moth"
    );
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
    assert_eq!(envs.last(), Some(&Some(caller("/work"))));
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

// OD-40 (OQ-2, OQ-3): a library verb's names are rooted at the project the
// daemon's ResolveProject names, or at `users/me` outside a project, where
// no daemon is reached.
#[tokio::test]
async fn a_library_verbs_parent_is_the_resolved_project_or_the_home() {
    let daemons = Fakes::new();
    let parent = in_project(&daemons).library_parent().await.unwrap();
    assert_eq!(parent, "projects/calm-otter");
    assert_eq!(daemons.asked(), ["1:resolve"]);
    let outside_daemons = Fakes::new();
    let home = outside(&outside_daemons).library_parent().await.unwrap();
    assert_eq!(home, "users/me");
    assert!(outside_daemons.asked().is_empty());
}

// Owner ruling 2026-11-14: every dry run calls the daemon, spawning it if
// needed. Its parent is the resolved project's, `users/me` outside one, and
// none in a project nothing has named (a dry run never mints); where there
// is nothing to resolve, the daemon is reached and asked `GetDaemon`, so a
// daemon that answers is proven, and one that cannot be had fails the call.
#[tokio::test]
async fn a_dry_runs_parent_reaches_the_daemon_even_with_nothing_to_resolve() {
    let daemons = Fakes::new();
    let parent = in_project(&daemons).named_parent().await.unwrap();
    assert_eq!(parent.as_deref(), Some("projects/calm-otter"));
    assert_eq!(daemons.asked(), ["1:resolve"]);
    let home_daemons = Fakes::new();
    let home = outside(&home_daemons).named_parent().await.unwrap();
    assert_eq!(home.as_deref(), Some("users/me"));
    assert_eq!(home_daemons.asked(), ["1:get"]);
    assert_eq!(home_daemons.reaches(), 1, "reached, so started if none");
    let unnamed_daemons = Fakes::new();
    let unnamed = outside(&unnamed_daemons).unnamed().named_parent().await;
    assert_eq!(unnamed.unwrap(), None);
    assert_eq!(unnamed_daemons.asked(), ["1:get"]);
    let envs = unnamed_daemons.template.envs.lock().unwrap().clone();
    assert_eq!(envs, vec![None], "GetDaemon carries no caller");
    // A daemon gone mid-call: asked once more, on the daemon reached again.
    let dying = Fakes::new();
    dying.template.dying.lock().unwrap().push(1);
    outside(&dying).unnamed().named_parent().await.unwrap();
    assert_eq!(dying.asked(), ["1:get", "2:get"]);
    let mut unstartable = Fakes::new();
    unstartable.unreached = Some(Unreached::NotStarted {
        log: "~/.ctxloom/daemon/daemon.log".to_string(),
    });
    let failed = outside(&unstartable)
        .unnamed()
        .named_parent()
        .await
        .unwrap_err();
    assert_eq!(
        angzarr_diagnostic::Diagnostic::code(&failed),
        "daemon-ports-not-started"
    );
    assert_eq!(failed.exit(), Exit::FAILED);
}

// Owner, H3 follow-up: `session transcript show` is a daemon read: it
// addresses the session's transcript in the project the daemon named, and
// is made once more on a new daemon if its own dies.
#[tokio::test]
async fn a_transcript_is_read_from_the_daemon_of_the_resolved_project() {
    let daemons = Fakes::new();
    daemons.template.dying.lock().unwrap().push(1);
    let read = in_project(&daemons)
        .get_transcript("amber-swift-owl")
        .await
        .unwrap();
    assert_eq!(
        read.name,
        "projects/calm-otter/sessions/amber-swift-owl/transcript"
    );
    assert_eq!(read.entries.len(), 1);
    assert_eq!(
        daemons.asked(),
        [
            "1:resolve",
            "1:get-transcript",
            "2:resolve",
            "2:get-transcript"
        ]
    );
}

// Step 10a U2: an assembly and a dry run are asked of the daemon under the
// library's parent (the resolved project, else the home), from the
// caller's directory in its environment, a dry run validate-only; what the
// daemon answers is answered.
#[tokio::test]
async fn an_assembly_and_a_dry_run_are_asked_under_the_library_parent_from_the_callers_place() {
    use super::LibraryReads;
    let daemons = Fakes::new();
    let client = in_project(&daemons);
    let assembled = client
        .assemble_context(v1::AssembleContextRequest::default())
        .await
        .unwrap();
    assert_eq!(assembled.context, "ASSEMBLED");
    let run = client
        .preview_run(v1::CreateRunRequest::default())
        .await
        .unwrap();
    assert_eq!(run.context, "PLANNED");
    assert_eq!(
        daemons.asked(),
        ["1:resolve", "1:assemble", "2:resolve", "2:run true"]
    );
    assert_eq!(
        daemons.names(),
        [
            "p-1",
            "projects/calm-otter",
            "cwd /work",
            "p-1",
            "projects/calm-otter",
            "cwd /work"
        ]
    );
    let envs = daemons.template.envs.lock().unwrap().clone();
    assert_eq!(envs[1], Some(caller("/work")));
    assert_eq!(envs[3], Some(caller("/work")));
    let home = Fakes::new();
    outside(&home)
        .assemble_context(v1::AssembleContextRequest::default())
        .await
        .unwrap();
    assert_eq!(home.names(), ["users/me", "cwd /tmp"]);
}

// E1, extended to the writes (2026-11-17): a write is asked under the
// library's parent from the caller's directory, in its environment, as a
// read is, so the daemon resolves the project and runs git as the caller
// would.
#[tokio::test]
async fn a_write_is_asked_from_the_callers_directory_in_its_environment() {
    use super::LibraryWrites;
    let daemons = Fakes::new();
    in_project(&daemons)
        .create_bundle("", v1::CreateBundleRequest::default())
        .await
        .unwrap();
    assert_eq!(daemons.asked(), ["1:resolve", "1:create-bundle"]);
    assert_eq!(daemons.names(), ["p-1", "projects/calm-otter", "cwd /work"]);
    let envs = daemons.template.envs.lock().unwrap().clone();
    assert_eq!(envs[1], Some(caller("/work")));
}

// Owner, 2026-11-18: a command's own part of its caller (its flags, what it
// can present) is kept, and the client fills the rest: its directory, its
// environment and which client it is.
#[tokio::test]
async fn a_write_keeps_its_own_flags_and_presentable_and_carries_the_clients_context() {
    use super::LibraryWrites;
    let daemons = Fakes::new();
    let globals = v1::Globals {
        degraded: true,
        ..v1::Globals::default()
    };
    let presentable = v1::Presentable {
        can_ask: false,
        can_launch: true,
    };
    in_project(&daemons)
        .create_bundle(
            "",
            v1::CreateBundleRequest {
                caller: v1::Caller::of(Some(globals.clone()), Some(presentable)),
                ..v1::CreateBundleRequest::default()
            },
        )
        .await
        .unwrap();
    let envs = daemons.template.envs.lock().unwrap().clone();
    assert_eq!(
        envs[1],
        Some(v1::Caller {
            globals: Some(globals),
            presentable: Some(presentable),
            ..caller("/work")
        })
    );
}

// Test-support's `CTXLOOM_TEST_NO_RETRY` (step 10b's harness): a client
// that does not retry answers the first daemon's loss as it is, and never
// reaches a second daemon, so what the killed one left is left.
#[tokio::test]
async fn a_client_that_does_not_retry_reaches_one_daemon_only() {
    let daemons = Fakes::new();
    daemons.template.dying.lock().unwrap().push(1);
    let client = numbering(&daemons).without_retry();
    let lost = client
        .delete_session("brisk-copper-moth", true, false)
        .await;
    assert!(lost.is_err(), "{lost:?}");
    assert_eq!(daemons.reaches(), 1);
    assert_eq!(
        daemons.template.request_ids.lock().unwrap().clone(),
        ["1:req-1"]
    );
}

// Step 10c U13's harness (`CTXLOOM_TEST_BEFORE_RETRY`): what a client is
// told to do before its one retry runs once, after the first daemon was
// lost and before a second is reached (the race forced, never waited
// for); a call that needs no retry never runs it.
#[tokio::test]
async fn what_runs_before_a_retry_runs_once_between_the_two_daemons() {
    let daemons = Fakes::new();
    daemons.template.dying.lock().unwrap().push(1);
    let reached = Arc::clone(&daemons.reached);
    let ran: Arc<Mutex<Vec<usize>>> = Arc::default();
    let seen = Arc::clone(&ran);
    let client = in_project(&daemons).before_retry(Box::new(move || {
        seen.lock().unwrap().push(*reached.lock().unwrap());
    }));
    client
        .list_sessions(false, v1::session::State::Unspecified)
        .await
        .unwrap();
    assert_eq!(*ran.lock().unwrap(), [1], "once, after the first reach");
    assert_eq!(daemons.reaches(), 2);
    let fine = Fakes::new();
    let none: Arc<Mutex<usize>> = Arc::default();
    let counted = Arc::clone(&none);
    in_project(&fine)
        .before_retry(Box::new(move || *counted.lock().unwrap() += 1))
        .list_sessions(false, v1::session::State::Unspecified)
        .await
        .unwrap();
    assert_eq!(*none.lock().unwrap(), 0, "no retry, nothing run");
}

// Step 10c U13: a hook asks the daemon for what its session is owed, named
// under its project as resolved, at its engine's hook, with the client's
// directory and environment.
#[tokio::test]
async fn a_hook_asks_for_its_sessions_owed_context_by_name() {
    let daemons = Fakes::new();
    let answered = in_project(&daemons)
        .redeliver_context("brisk-copper-moth", "PostToolUse", 4_321)
        .await
        .unwrap();
    assert_eq!(answered.context, "owed:PostToolUse");
    assert_eq!(daemons.asked(), ["1:resolve", "1:redeliver"]);
    assert_eq!(
        daemons.names(),
        [
            "p-1",
            "projects/calm-otter/sessions/brisk-copper-moth cwd /work budget 4321"
        ]
    );
    let envs = daemons.template.envs.lock().unwrap().clone();
    assert_eq!(envs[1], Some(caller("/work")));
}
