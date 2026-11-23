//! `CreateRun`, `GetRun` and `WatchRun` (U4, T7) over the daemon's
//! one-shots, their host scripted: what the daemon is asked to host, and
//! how a placed and an ended run are answered and watched.

use std::sync::{Arc, Mutex};

use daemon_application::{HostRequest, OneShotControl, OneShotHost, OneShots, Told};
use daemon_proto::v1;
use run_application::{Answer, Done, Placement, RunError};

use crate::server::tests::{fixture, now, refused, watched};
use crate::ApiError;

const ROOT: &str = "0195f0a4-7b1e-7000-8000-000000000001";
const SESSION: &str = "projects/calm-quiet-heron/sessions/amber-quiet-heron";

/// A host that answers each one-shot at once, as scripted, keeping what it
/// was asked.
struct Scripted {
    refuse: bool,
    asked: Arc<Mutex<Vec<HostRequest>>>,
}

struct Nothing;

impl OneShotControl for Nothing {
    fn stop(&self) {}
}

impl OneShotHost for Scripted {
    fn host(&self, request: HostRequest, told: Told) -> Box<dyn OneShotControl> {
        self.asked.lock().unwrap().push(request);
        if self.refuse {
            now(told.done(Done {
                result: Err(RunError::Refused(runs::RunRejection::NoPrompt)),
                notices: vec![library_application::Notice::text("before")],
            }));
        } else {
            told.placed(&Placement {
                run: ROOT.to_string(),
                session: "s".to_string(),
                name: SESSION.to_string(),
                harp: "amber-quiet-heron".to_string(),
                engine: "claude-code".to_string(),
                ..Default::default()
            });
            now(told.done(Done {
                result: Ok(Answer {
                    harp: "amber-quiet-heron".to_string(),
                    session: "s".to_string(),
                    engine: "claude-code".to_string(),
                    answer: "HELLO".to_string(),
                }),
                notices: Vec::new(),
            }));
        }
        Box::new(Nothing)
    }
}

fn one_shots(refuse: bool) -> (Arc<OneShots>, Arc<Mutex<Vec<HostRequest>>>) {
    let asked = Arc::new(Mutex::new(Vec::new()));
    let hosted = Arc::new(OneShots::new(Box::new(Scripted {
        refuse,
        asked: Arc::clone(&asked),
    })));
    (hosted, asked)
}

fn one_shot(parent: &str) -> v1::CreateRunRequest {
    v1::CreateRunRequest {
        parent: parent.to_string(),
        mode: v1::run::Mode::OneShot as i32,
        prompt: "hi".to_string(),
        agent: "dev".to_string(),
        caller: Some(v1::Caller {
            cwd: "/work/sub".to_string(),
            env: Some(v1::EnvVars {
                vars: vec![v1::EnvVar {
                    name: "PATH".to_string(),
                    value: "/bin".to_string(),
                }],
            }),
            ..v1::Caller::default()
        }),
        ..v1::CreateRunRequest::default()
    }
}

#[tokio::test]
async fn a_one_shot_is_hosted_in_its_project_and_answered_once_placed() {
    let f = fixture(vec!["calm-quiet-heron"]);
    f.api
        .resolve_project(v1::ResolveProjectRequest {
            caller: Some(v1::Caller {
                cwd: "/work".to_string(),
                ..v1::Caller::default()
            }),
            project_id: "calm".to_string(),
            root: "/work".to_string(),
            ..v1::ResolveProjectRequest::default()
        })
        .await
        .unwrap();
    f.catch_up().await;
    let (hosted, asked) = one_shots(false);
    let api = f.restarted().await.hosting_one_shots(Arc::clone(&hosted));
    let run = api
        .create_run(one_shot("projects/calm-quiet-heron"))
        .await
        .unwrap();
    let name = format!("projects/calm-quiet-heron/runs/{ROOT}");
    assert_eq!(run.name, name);
    assert_eq!(run.session, SESSION);
    assert_eq!(run.engine, "projects/calm-quiet-heron/engines/claude-code");
    assert_eq!(run.mode, v1::run::Mode::OneShot as i32);
    assert_eq!(run.agent, "dev");
    assert!(run.end.is_none());
    let request = asked.lock().unwrap()[0].clone();
    let project = request.project.expect("its project");
    assert_eq!(
        (
            project.id.as_str(),
            project.harp.as_str(),
            project.root.as_str()
        ),
        ("calm", "calm-quiet-heron", "/work")
    );
    assert_eq!(request.cwd, "/work/sub");
    assert_eq!(request.env.get("PATH").map(String::as_str), Some("/bin"));
    assert_eq!(request.request.run.prompt, "hi");
    let got = api
        .get_run(v1::GetRunRequest { name: name.clone() })
        .await
        .unwrap();
    assert_eq!(got.session, SESSION);
    assert_eq!(got.name, name);
    let events = watched(api.watch_run(v1::WatchRunRequest { name }).await.unwrap());
    assert_eq!(events.len(), 2, "its reply and its end: {events:?}");
    assert_eq!(
        events[0].event,
        Some(v1::run_event::Event::Reply("HELLO".to_string()))
    );
}

#[tokio::test]
async fn a_one_shot_outside_a_project_is_hosted_with_none() {
    let f = fixture(vec![]);
    let (hosted, asked) = one_shots(false);
    let api = f.restarted().await.hosting_one_shots(hosted);
    let run = api.create_run(one_shot("users/me")).await.unwrap();
    assert_eq!(run.name, format!("projects/calm-quiet-heron/runs/{ROOT}"));
    assert_eq!(run.engine, "claude-code");
    assert!(asked.lock().unwrap()[0].project.is_none());
}

#[tokio::test]
async fn a_one_shot_that_ended_before_it_was_hosted_is_answered_with_its_end() {
    let f = fixture(vec![]);
    let (hosted, _) = one_shots(true);
    let api = f.restarted().await.hosting_one_shots(hosted);
    let run = api.create_run(one_shot("users/me")).await.unwrap();
    assert_eq!(run.name, "users/me/runs/-");
    assert_eq!(run.notices.len(), 1);
    let end = run.end.expect("its end");
    assert_eq!(end.code, "runs-no-prompt");
    assert_eq!(end.exit, 48);
}

#[tokio::test]
async fn a_one_shot_in_a_project_nobody_named_is_not_found() {
    let f = fixture(vec![]);
    let (hosted, asked) = one_shots(false);
    let api = f.restarted().await.hosting_one_shots(hosted);
    let _: (_, v1::ProjectNotFound) = refused(api.create_run(one_shot("projects/far-heron")).await);
    let _: (_, v1::NameInvalid) = refused(api.create_run(one_shot("projects/")).await);
    assert!(asked.lock().unwrap().is_empty(), "nothing hosted");
}

#[tokio::test]
async fn a_run_of_no_mode_is_not_served_yet() {
    let f = fixture(vec![]);
    let (hosted, asked) = one_shots(false);
    let api = f.restarted().await.hosting_one_shots(hosted);
    let answered = api
        .create_run(v1::CreateRunRequest {
            mode: v1::run::Mode::Unspecified as i32,
            ..one_shot("users/me")
        })
        .await;
    assert!(
        matches!(answered, Err(ApiError::Unimplemented { .. })),
        "{answered:?}"
    );
    assert!(asked.lock().unwrap().is_empty());
}

// Step 10a U2: a preview is the dry run's, planned as a library read, never
// hosted: with no library to read it fails, and nothing is hosted.
#[tokio::test]
async fn a_preview_is_planned_as_a_library_read_and_never_hosted() {
    let f = fixture(vec![]);
    let (hosted, asked) = one_shots(false);
    let api = f.restarted().await.hosting_one_shots(hosted);
    let answered = api
        .create_run(v1::CreateRunRequest {
            validate_only: true,
            ..one_shot("users/me")
        })
        .await;
    assert!(
        matches!(&answered, Err(ApiError::Failed { detail }) if detail.contains("reads no library")),
        "{answered:?}"
    );
    assert!(asked.lock().unwrap().is_empty());
}

#[tokio::test]
async fn a_run_it_does_not_host_is_not_found() {
    let f = fixture(vec![]);
    let (hosted, _) = one_shots(false);
    let api = f.restarted().await.hosting_one_shots(hosted);
    let name = "projects/calm-quiet-heron/runs/nope".to_string();
    let (_, missing): (_, v1::RunNotFound) =
        refused(api.get_run(v1::GetRunRequest { name: name.clone() }).await);
    assert_eq!(missing.name, name);
    let watched = api.watch_run(v1::WatchRunRequest { name }).await;
    assert!(matches!(watched, Err(ApiError::Refused(_))), "not found");
}

#[tokio::test]
async fn a_daemon_that_hosts_no_one_shot_fails_its_runs() {
    let f = fixture(vec![]);
    let answered = f.api.create_run(one_shot("users/me")).await;
    assert!(
        matches!(answered, Err(ApiError::Failed { .. })),
        "{answered:?}"
    );
    let answered = f
        .api
        .get_run(v1::GetRunRequest {
            name: "x".to_string(),
        })
        .await;
    assert!(
        matches!(answered, Err(ApiError::Failed { .. })),
        "{answered:?}"
    );
}

/// A host that places each one-shot and keeps it running, its `Told` kept
/// for the test to end it when it chooses.
struct Placing(Arc<Mutex<Option<Told>>>);

impl OneShotHost for Placing {
    fn host(&self, _: HostRequest, told: Told) -> Box<dyn OneShotControl> {
        told.placed(&Placement {
            run: ROOT.to_string(),
            session: "s".to_string(),
            name: SESSION.to_string(),
            harp: "amber-quiet-heron".to_string(),
            engine: "claude-code".to_string(),
            ..Default::default()
        });
        *self.0.lock().unwrap() = Some(told);
        Box::new(Nothing)
    }
}

// The drain/`WatchRun` race (2026-11-15's flake of
// `a_one_shot_still_running_after_the_period_is_stopped`, exit 48 not
// 143): the one-shot's client watches its run only once `CreateRun` has
// answered, so a drain asked in between came first. A watch of a run the
// daemon already hosts starts no work (OD-62 refuses NEW invocations), so
// the draining daemon serves it, and the client hears its run stopped at
// the period, its end as the stop says.
#[tokio::test]
async fn a_draining_daemon_serves_the_watch_of_a_run_it_already_hosts() {
    use crate::Calls;
    let f = fixture(vec![]);
    let told = Arc::new(Mutex::new(None));
    let hosted = Arc::new(OneShots::new(Box::new(Placing(Arc::clone(&told)))));
    let api = f.restarted().await.hosting_one_shots(Arc::clone(&hosted));
    let calls: &dyn Calls = &api;
    let run = calls.create_run(one_shot("users/me")).await.unwrap();
    assert!(run.end.is_none(), "placed, still running");
    calls
        .stop_daemon(v1::StopDaemonRequest {
            drain: true,
            ..v1::StopDaemonRequest::default()
        })
        .await
        .unwrap();
    let watch = calls
        .watch_run(v1::WatchRunRequest {
            name: run.name.clone(),
        })
        .await
        .map_err(|error| format!("{error:?}"))
        .expect("a draining daemon serves the watch of a run it hosts");
    assert!(hosted.stop(ROOT, "SIGTERM"));
    now(told.lock().unwrap().take().expect("hosted").done(Done {
        result: Err(RunError::EmptyAnswer),
        notices: Vec::new(),
    }));
    let events = watched(watch);
    assert!(
        matches!(
            events.first().and_then(|e| e.event.as_ref()),
            Some(v1::run_event::Event::Stop(_))
        ),
        "its stop first: {events:?}"
    );
    let Some(v1::run_event::Event::End(end)) = events.last().and_then(|e| e.event.clone()) else {
        panic!("its end last: {events:?}");
    };
    assert_eq!(end.code, "run-application-stopped", "{end:?}");
    assert_eq!(end.exit, 143, "{end:?}");
}

// A watch of a run the draining daemon does not host is told the run is
// not found, as an open daemon tells it: nothing is started either way.
#[tokio::test]
async fn a_draining_daemon_answers_the_watch_of_a_run_it_does_not_host_not_found() {
    use crate::Calls;
    let f = fixture(vec![]);
    let (hosted, _) = one_shots(false);
    let api = f.restarted().await.hosting_one_shots(hosted);
    let calls: &dyn Calls = &api;
    calls
        .stop_daemon(v1::StopDaemonRequest {
            drain: true,
            ..v1::StopDaemonRequest::default()
        })
        .await
        .unwrap();
    let name = "projects/calm-quiet-heron/runs/nope".to_string();
    let watch = calls
        .watch_run(v1::WatchRunRequest { name: name.clone() })
        .await
        .map(|_| "a watch");
    let (_, missing): (_, v1::RunNotFound) = refused(watch);
    assert_eq!(missing.name, name);
}
