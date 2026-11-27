//! `CreateRun{mode: TERMINAL}` (U5) over the daemon's terminals, their host
//! scripted inside its own call: what the daemon is asked to prepare, the
//! launch it answers, and the stop and end a watch streams.

use std::sync::{Arc, Mutex};

use daemon_application::{HostRequest, TerminalHost, TerminalPlaced, TerminalTold, Terminals};
use daemon_proto::v1;
use run_ports::{Launch, LaunchKind, Stdio, TurnCommand};

use crate::server::tests::{fixture, watched};

const ROOT: &str = "0195f0a4-7b1e-7000-8000-000000000001";
const SESSION: &str = "projects/calm-quiet-heron/sessions/amber-quiet-heron";

/// A host that prepares each session at once (or ends it before its
/// launch), keeping what it was asked and the `Told` of each.
struct Scripted {
    refuse: bool,
    asked: Arc<Mutex<Vec<HostRequest>>>,
    told: Arc<Mutex<Vec<TerminalTold>>>,
}

fn launch() -> Launch {
    let mut command = TurnCommand::new("/bin/claude");
    command
        .env
        .insert("ANTHROPIC_API_KEY".to_string(), "k".to_string()); // gitleaks:allow
    command.cwd = Some("/work".to_string());
    let mut launch = Launch::new(LaunchKind::Terminal, "claude-code", Stdio::Inherit, command);
    launch.at.run = ROOT.to_string();
    launch.at.harp = "amber-quiet-heron".to_string();
    launch.session_lock = "/work/.ctxloom/sessions/amber-quiet-heron.lock".to_string();
    launch
}

fn end(code: &str) -> v1::RunEvent {
    v1::RunEvent {
        event: Some(v1::run_event::Event::End(v1::RunEnd {
            exit: 48,
            code: code.to_string(),
            ..v1::RunEnd::default()
        })),
    }
}

fn notice(text: &str) -> v1::RunEvent {
    v1::RunEvent {
        event: Some(v1::run_event::Event::Notice(v1::Notice {
            text: text.to_string(),
            ..v1::Notice::default()
        })),
    }
}

impl TerminalHost for Scripted {
    fn host(&self, request: HostRequest, told: TerminalTold) {
        self.asked.lock().unwrap().push(request);
        if self.refuse {
            told.done(vec![notice("before"), end("runs-no-credential")]);
        } else {
            told.prepared(
                TerminalPlaced {
                    run: ROOT.to_string(),
                    name: SESSION.to_string(),
                    engine: "claude-code".to_string(),
                },
                launch(),
            );
        }
        self.told.lock().unwrap().push(told);
    }
}

struct Rig {
    terminals: Arc<Terminals>,
    asked: Arc<Mutex<Vec<HostRequest>>>,
    told: Arc<Mutex<Vec<TerminalTold>>>,
}

fn terminals(refuse: bool) -> Rig {
    let asked = Arc::new(Mutex::new(Vec::new()));
    let told = Arc::new(Mutex::new(Vec::new()));
    let terminals = Arc::new(Terminals::new(Box::new(Scripted {
        refuse,
        asked: Arc::clone(&asked),
        told: Arc::clone(&told),
    })));
    Rig {
        terminals,
        asked,
        told,
    }
}

fn terminal(parent: &str) -> v1::CreateRunRequest {
    v1::CreateRunRequest {
        parent: parent.to_string(),
        mode: v1::run::Mode::Terminal as i32,
        agent: "dev".to_string(),
        caller: Some(v1::Caller {
            cwd: "/work/sub".to_string(),
            env: Some(v1::EnvVars {
                vars: vec![v1::EnvVar {
                    name: "TERM".to_string(),
                    value: "xterm".to_string(),
                }],
            }),
            ..v1::Caller::default()
        }),
        ..v1::CreateRunRequest::default()
    }
}

async fn resolved() -> crate::server::tests::Fixture {
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
    f
}

#[tokio::test]
async fn a_terminal_run_is_prepared_in_its_project_and_answered_with_its_launch() {
    let f = resolved().await;
    let rig = terminals(false);
    let api = f
        .restarted()
        .await
        .preparing_terminals(Arc::clone(&rig.terminals));
    let run = api
        .create_run(terminal("projects/calm-quiet-heron"))
        .await
        .unwrap();
    let name = format!("projects/calm-quiet-heron/runs/{ROOT}");
    assert_eq!(run.name, name);
    assert_eq!(run.session, SESSION);
    assert_eq!(run.mode, v1::run::Mode::Terminal as i32);
    assert_eq!(run.engine, "projects/calm-quiet-heron/engines/claude-code");
    assert_eq!(run.agent, "dev");
    assert!(run.end.is_none());
    let wired = run.launch.expect("its launch");
    assert_eq!(
        daemon_api_contract::launch::from_wire(&wired),
        Some(launch())
    );
    let request = rig.asked.lock().unwrap()[0].clone();
    let project = request.project.expect("its project");
    assert_eq!(
        (project.id.as_str(), project.harp.as_str()),
        ("calm", "calm-quiet-heron")
    );
    assert_eq!(request.cwd, "/work/sub");
    assert_eq!(request.env.get("TERM").map(String::as_str), Some("xterm"));
    assert_eq!(request.request.run.agent, "dev");
    let got = api
        .get_run(v1::GetRunRequest { name: name.clone() })
        .await
        .unwrap();
    assert_eq!(got.session, SESSION);
    assert_eq!(got.mode, v1::run::Mode::Terminal as i32);
    assert!(got.launch.is_none(), "a read never carries the launch");
    // The session is closed, then its frontend reports the exit.
    rig.terminals.stop(ROOT, "closed");
    rig.told.lock().unwrap()[0].done(vec![notice("after")]);
    let events = watched(api.watch_run(v1::WatchRunRequest { name }).await.unwrap());
    assert_eq!(events.len(), 2, "{events:?}");
    assert!(matches!(
        &events[0].event,
        Some(v1::run_event::Event::Stop(stop)) if stop.reason == "closed" && stop.grace_ms == 5000
    ));
    assert_eq!(events[1], notice("after"));
}

#[tokio::test]
async fn a_terminal_run_that_ended_before_its_launch_answers_its_end() {
    let f = resolved().await;
    let rig = terminals(true);
    let api = f
        .restarted()
        .await
        .preparing_terminals(Arc::clone(&rig.terminals));
    let run = api
        .create_run(terminal("projects/calm-quiet-heron"))
        .await
        .unwrap();
    assert_eq!(run.name, "projects/calm-quiet-heron/runs/-");
    assert!(run.launch.is_none());
    assert_eq!(run.notices.len(), 1);
    assert_eq!(
        run.end.map(|e| e.code),
        Some("runs-no-credential".to_string())
    );
}

#[tokio::test]
async fn a_daemon_that_prepares_no_session_fails_a_terminal_run() {
    let f = resolved().await;
    let api = f.restarted().await;
    let failed = api
        .create_run(terminal("projects/calm-quiet-heron"))
        .await
        .err()
        .unwrap();
    assert!(
        matches!(failed, crate::ApiError::Failed { .. }),
        "{failed:?}"
    );
    assert!(api.get_terminal("projects/p/runs/x").is_none());
    assert!(api.watch_terminal("projects/p/runs/x").is_none());
}

#[tokio::test]
async fn a_watch_of_a_run_the_terminals_do_not_hold_is_not_theirs() {
    let f = resolved().await;
    let rig = terminals(false);
    let api = f.restarted().await.preparing_terminals(rig.terminals);
    assert!(api
        .get_terminal("projects/calm-quiet-heron/runs/other")
        .is_none());
    assert!(api
        .watch_terminal("projects/calm-quiet-heron/runs/other")
        .is_none());
}

// U5's start-report race: the run lock the frontend took before it asked
// reaches the daemon's session as its launcher lock; a request naming none
// (an older frontend's) names none.
#[tokio::test]
async fn the_frontends_run_lock_reaches_the_session_it_asks_for() {
    let f = resolved().await;
    let rig = terminals(false);
    let api = f
        .restarted()
        .await
        .preparing_terminals(Arc::clone(&rig.terminals));
    let lock = "/home/u/.ctxloom/runs/0195f0a4-request.lock";
    let mut asked = terminal("projects/calm-quiet-heron");
    asked.launcher_lock = lock.to_string();
    api.create_run(asked).await.unwrap();
    api.create_run(terminal("projects/calm-quiet-heron"))
        .await
        .unwrap();
    let requests = rig.asked.lock().unwrap().clone();
    assert_eq!(requests[0].launcher_lock.as_deref(), Some(lock));
    assert_eq!(requests[1].launcher_lock, None);
}
