//! `run --one-shot` as the daemon's client (U4, T7): what crosses the wire
//! and back, and how a run is followed and stopped. Each race is forced
//! (the race rule): the fake daemon's stream holds its next event until the
//! test's own step lets it go.

use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};

use angzarr_diagnostic::{Arg, Diagnostic, Report};
use daemon_ports::{Called, DaemonApi, RunEvents};
use daemon_proto::v1;
use exit_code::{Exit, ExitClass};
use library_application::{Flags, RunRequest};

use super::*;
use crate::one_shot::OneShot;
use daemon_proto::Carries as _;

const RUN: &str = "projects/calm-otter/runs/0195f0a4-7b1e-7000-8000-000000000001";
const SESSION: &str = "projects/calm-otter/sessions/amber-quiet-heron";

fn asked() -> Asked {
    Asked {
        parent: "projects/calm-otter".to_string(),
        cwd: "/work".to_string(),
        env: [("PATH".to_string(), "/bin".to_string())].into(),
        flags: Flags {
            degraded: true,
            config_set: vec!["a=b".to_string()],
            no_companions: true,
            trusted: false,
        },
        request: OneShot {
            run: RunRequest {
                agent: "dev".to_string(),
                profile: "review".to_string(),
                fragments: vec!["f".to_string()],
                tags: vec!["t".to_string()],
                engine: "claude-code".to_string(),
                engine_named: false,
                prompt: "hi".to_string(),
                saved_command: "c".to_string(),
                workspace: "worktree".to_string(),
                no_context: true,
            },
            engine: "claude-code".to_string(),
            session: "old-heron".to_string(),
        },
        request_id: "r1".to_string(),
        client: v1::Client {
            kind: v1::client::Kind::Cli as i32,
            program: "ctxloom".to_string(),
            program_dir: "/opt/ctxloom".to_string(),
            version: "v1.2.3-abc".to_string(),
        },
    }
}

#[test]
fn test_a_create_request_carries_the_whole_line_and_reads_back_as_it() {
    let asked = asked();
    let request = create_request(&asked);
    assert_eq!(request.mode, v1::run::Mode::OneShot as i32);
    assert_eq!(request.caller_cwd(), "/work");
    assert_eq!(request.caller_client(), Some(&asked.client));
    assert_eq!(request.request_id, "r1");
    assert_eq!(request.profiles, ["review"]);
    let env = request.caller_env().cloned().unwrap();
    assert_eq!(env.vars[0].name, "PATH");
    assert_eq!(env.vars[0].value, "/bin");
    let (one_shot, flags) = asked_of(&request);
    assert_eq!(
        (flags.degraded, flags.config_set, flags.no_companions),
        (true, vec!["a=b".to_string()], true)
    );
    assert_eq!(format!("{one_shot:?}"), format!("{:?}", asked.request));
}

#[test]
fn test_a_create_request_with_no_profile_names_none() {
    let mut asked = asked();
    asked.request.run.profile = String::new();
    assert!(create_request(&asked).profiles.is_empty());
    assert_eq!(asked_of(&create_request(&asked)).0.run.profile, "");
}

/// A diagnostic with every kind of arg.
fn every_arg() -> Report {
    Report::new("outer-code")
        .arg("count", Arg::Int(-3))
        .arg("size", Arg::Uint(u64::MAX))
        .arg("path", "/a b")
        .arg(
            "cause",
            Arg::Nested(Box::new(
                Report::new("inner-code").arg("n", Arg::Int(2)).arg(
                    "deeper",
                    Arg::Nested(Box::new(Report::new("deep").arg("x", "y"))),
                ),
            )),
        )
        .arg("after", "007")
}

#[test]
fn test_args_flatten_as_angzarr_flattens_a_cause() {
    let flat: Vec<(String, String)> = flat_args(&every_arg())
        .into_iter()
        .map(|arg| (arg.name, arg.value))
        .collect();
    let want: Vec<(String, String)> = [
        ("count", "-3"),
        ("size", "18446744073709551615"),
        ("path", "/a b"),
        ("cause", "inner-code"),
        ("cause.n", "2"),
        ("cause.deeper", "deep"),
        ("cause.deeper.x", "y"),
        ("after", "007"),
    ]
    .into_iter()
    .map(|(n, v)| (n.to_string(), v.to_string()))
    .collect();
    assert_eq!(flat, want);
}

#[test]
fn test_flattened_args_are_rebuilt_whole_integers_and_causes_included() {
    let rebuilt = report_of("outer-code", &flat_args(&every_arg()));
    assert_eq!(rebuilt, every_arg());
}

#[test]
fn test_an_end_names_the_exit_by_its_block() {
    assert_eq!(exit_of(0), Exit::OK);
    assert_eq!(exit_of(16), Exit::OK);
    assert_eq!(exit_of(17), Exit::WOULD_CHANGE);
    assert_eq!(exit_of(32), Exit::FAILED);
    assert_eq!(exit_of(47), Exit::failed(47));
    assert_eq!(exit_of(48), Exit::REFUSED);
    assert_eq!(exit_of(49), Exit::FATAL_FINDINGS);
    assert_eq!(exit_of(63), Exit::declined(63));
    assert_eq!(exit_of(130), Exit::signal(2));
    assert_eq!(exit_of(129), Exit::signal(1));
    for odd in [1, 10, 15, 64, 128, -1, 1000] {
        assert_eq!(exit_of(odd), Exit::FAILED, "{odd}");
    }
}

#[test]
fn test_an_error_crosses_as_its_code_args_exit_and_refusals() {
    let error = RunError::Refused(runs::RunRejection::LaunchRefused {
        refusals: vec![
            runs::RunRejection::NoPrompt,
            runs::RunRejection::UnknownEngine {
                name: "bogus".to_string(),
                known: "claude-code".to_string(),
            },
        ],
        fatal_findings: 1,
        findings: Vec::new(),
    });
    let end = end_of(&error);
    assert_eq!(end.exit, 49);
    assert_eq!(end.code, error.code());
    assert_eq!(end.text, error.to_string());
    assert_eq!(end.refusals.len(), 2);
    let back = error_of(&end).expect("an error");
    assert_eq!(back.code(), error.code());
    assert_eq!(back.exit(), Exit::FATAL_FINDINGS);
    assert_eq!(
        Report::of(&back),
        report_of(&end.code, &end.args),
        "its args"
    );
    let codes: Vec<String> = back
        .refusals()
        .iter()
        .map(|r| r.code().to_string())
        .collect();
    assert_eq!(codes, ["runs-no-prompt", "runs-unknown-engine"]);
    assert_eq!(
        Report::of(back.refusals()[1].as_ref()),
        Report::of(&runs::RunRejection::UnknownEngine {
            name: "bogus".to_string(),
            known: "claude-code".to_string(),
        })
    );
}

#[test]
fn test_an_answered_end_is_no_error() {
    assert!(error_of(&answered()).is_none());
    assert!(error_of(&v1::RunEnd {
        exit: 10,
        ..v1::RunEnd::default()
    })
    .is_some());
    assert!(error_of(&v1::RunEnd {
        code: "x".to_string(),
        ..v1::RunEnd::default()
    })
    .is_some());
}

#[test]
fn test_a_notice_crosses_as_its_code_and_args() {
    let notice = library_application::Notice::Finding {
        text: "t".to_string(),
        remedy: "r".to_string(),
    };
    let wire = notice_of(&notice);
    assert_eq!(wire.code, notice.code());
    let back = noticed(&wire);
    assert_eq!(back.code(), notice.code());
    assert_eq!(Report::of(&back), Report::of(&notice));
}

#[test]
fn test_a_signal_is_a_shells_status() {
    assert_eq!(status_of("SIGHUP"), 129);
    assert_eq!(status_of("SIGINT"), 130);
    assert_eq!(status_of("SIGTERM"), 143);
}

#[test]
fn test_a_session_name_gives_its_harp_and_stable_id() {
    let (harp, id) = session_of(SESSION).expect("a session's name");
    assert_eq!(harp, "amber-quiet-heron");
    assert_eq!(id, SessionName::parse(SESSION).unwrap().uid());
    assert!(session_of("not a name").is_none());
}

/// A daemon that hosts one run: `CreateRun` answered `created` (after one
/// yield when `yields`, so a signal ready at once is heard before it), then
/// its stream gives `events` one by one; the last is held until a stop is
/// requested when `held` (the forced race).
struct Fake {
    created: Result<v1::Run, Called>,
    yields: bool,
    events: Arc<Mutex<Vec<v1::RunEvent>>>,
    held: bool,
    watch: Option<Called>,
    stopped: Arc<AtomicBool>,
    stops: Mutex<Vec<(String, String)>>,
    stop_fails: bool,
    asked: Mutex<Vec<v1::CreateRunRequest>>,
}

impl Fake {
    fn new(created: Result<v1::Run, Called>, events: Vec<v1::RunEvent>) -> Fake {
        Fake {
            created,
            yields: false,
            events: Arc::new(Mutex::new(events)),
            held: false,
            watch: None,
            stopped: Arc::new(AtomicBool::new(false)),
            stops: Mutex::new(Vec::new()),
            stop_fails: false,
            asked: Mutex::new(Vec::new()),
        }
    }
}

/// The fake's stream.
struct Events {
    events: Arc<Mutex<Vec<v1::RunEvent>>>,
    stopped: Arc<AtomicBool>,
    held: bool,
}

impl RunEvents for Events {
    fn next(&mut self) -> daemon_ports::NextEvent<'_> {
        Box::pin(std::future::poll_fn(move |_| {
            let mut events = self.events.lock().unwrap();
            let last = events.len() == 1;
            if last && self.held && !self.stopped.load(Ordering::SeqCst) {
                return std::task::Poll::Pending;
            }
            match events.is_empty() {
                true => std::task::Poll::Ready(None),
                false => std::task::Poll::Ready(Some(Ok(events.remove(0)))),
            }
        }))
    }
}

/// Pending once (waking itself), then ready.
async fn yield_once() {
    let mut yielded = false;
    std::future::poll_fn(move |cx| {
        if yielded {
            return std::task::Poll::Ready(());
        }
        yielded = true;
        cx.waker().wake_by_ref();
        std::task::Poll::Pending
    })
    .await;
}

impl DaemonApi for Fake {
    async fn get_daemon(&self, _: v1::GetDaemonRequest) -> Result<v1::Daemon, Called> {
        unreachable!()
    }
    async fn stop_daemon(&self, _: v1::StopDaemonRequest) -> Result<v1::Daemon, Called> {
        unreachable!()
    }
    async fn resolve_project(&self, _: v1::ResolveProjectRequest) -> Result<v1::Project, Called> {
        unreachable!()
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
    async fn create_run(&self, request: v1::CreateRunRequest) -> Result<v1::Run, Called> {
        if self.yields {
            yield_once().await;
        }
        self.asked.lock().unwrap().push(request);
        self.created.clone()
    }
    async fn watch_run(&self, request: v1::WatchRunRequest) -> Result<Box<dyn RunEvents>, Called> {
        assert_eq!(request.name, RUN);
        if let Some(called) = &self.watch {
            return Err(called.clone());
        }
        Ok(Box::new(Events {
            events: Arc::clone(&self.events),
            stopped: Arc::clone(&self.stopped),
            held: self.held,
        }))
    }
    async fn handle(&self, book: daemon_ports::CommandBook) -> Result<bool, Called> {
        let page = book.page.expect("a page");
        let id = page.command_id.expect("a command id");
        let any = match page.body {
            Some(angzarr_embedded::v1::command_page::Body::Command(command)) => {
                command.payload.expect("a payload")
            }
            other => panic!("a command: {other:?}"),
        };
        let stop: <runs::RequestStop as angzarr_embedded::markup::DomainMessage>::Wire =
            any.to_msg().expect("a RequestStop");
        let cover = book.cover.expect("a cover");
        assert_eq!(cover.domain, runs::RUN_DOMAIN);
        self.stops
            .lock()
            .unwrap()
            .push((stop.reason, format!("{id:?}")));
        // The held end is let go either way: a stop the daemon never heard
        // leaves its turn to end by itself.
        self.stopped.store(true, Ordering::SeqCst);
        if self.stop_fails {
            return Err(Called::Unreachable {
                detail: "gone".to_string(),
            });
        }
        Ok(true)
    }
}

fn placed() -> v1::Run {
    v1::Run {
        name: RUN.to_string(),
        mode: v1::run::Mode::OneShot as i32,
        session: SESSION.to_string(),
        engine: "projects/calm-otter/engines/claude-code".to_string(),
        ..v1::Run::default()
    }
}

fn notice(code: &str) -> v1::RunEvent {
    v1::RunEvent {
        event: Some(v1::run_event::Event::Notice(v1::Notice {
            code: code.to_string(),
            args: vec![v1::NeedArg {
                name: "text".to_string(),
                value: "t".to_string(),
            }],
            text: String::new(),
        })),
    }
}

fn reply(text: &str) -> v1::RunEvent {
    v1::RunEvent {
        event: Some(v1::run_event::Event::Reply(text.to_string())),
    }
}

fn end(end: v1::RunEnd) -> v1::RunEvent {
    v1::RunEvent {
        event: Some(v1::run_event::Event::End(end)),
    }
}

fn never<'a>() -> Heard<'a> {
    Box::pin(std::future::pending())
}

fn now<'a>(signal: &str) -> Heard<'a> {
    let signal = signal.to_string();
    Box::pin(async move { signal })
}

#[tokio::test]
async fn test_a_hosted_one_shot_answers_its_reply_under_its_session() {
    let fake = Fake::new(
        Ok(placed()),
        vec![notice("n-one"), reply("HELLO"), end(answered())],
    );
    let done = one_shot(&fake, &asked(), never()).await;
    let answer = done.result.expect("answered");
    assert_eq!(answer.answer, "HELLO");
    assert_eq!(answer.harp, "amber-quiet-heron");
    assert_eq!(answer.session, SessionName::parse(SESSION).unwrap().uid());
    assert_eq!(answer.engine, "claude-code");
    let codes: Vec<&str> = done.notices.iter().map(|n| n.code()).collect();
    assert_eq!(codes, ["n-one"]);
    assert_eq!(fake.asked.lock().unwrap()[0], create_request(&asked()));
    assert!(
        fake.stops.lock().unwrap().is_empty(),
        "nothing asked a stop"
    );
}

#[tokio::test]
async fn test_a_run_that_ended_before_it_was_hosted_says_its_notices_and_its_end() {
    let refused = RunError::Refused(runs::RunRejection::NoPrompt);
    let fake = Fake::new(
        Ok(v1::Run {
            name: "projects/calm-otter/runs/-".to_string(),
            notices: vec![v1::Notice {
                code: "before".to_string(),
                ..v1::Notice::default()
            }],
            end: Some(end_of(&refused)),
            ..v1::Run::default()
        }),
        Vec::new(),
    );
    let done = one_shot(&fake, &asked(), never()).await;
    let error = done.result.expect_err("refused");
    assert_eq!(error.code(), "runs-no-prompt");
    assert_eq!(error.exit(), Exit::REFUSED);
    let codes: Vec<&str> = done.notices.iter().map(|n| n.code()).collect();
    assert_eq!(codes, ["before"]);
}

#[tokio::test]
async fn test_a_hosted_run_that_failed_ends_the_command_as_it_failed() {
    let failed = RunError::Turn {
        class: run_ports::FailureClass::EngineError,
        detail: "boom".to_string(),
    };
    let fake = Fake::new(Ok(placed()), vec![end(end_of(&failed))]);
    let error = one_shot(&fake, &asked(), never())
        .await
        .result
        .expect_err("failed");
    assert_eq!(error.code(), "run-application-engine-failed");
    assert_eq!(error.exit(), Exit::FAILED);
    assert_eq!(Report::of(&error), Report::of(&failed));
}

#[tokio::test]
async fn test_an_answered_end_with_no_reply_is_an_empty_answer() {
    let fake = Fake::new(Ok(placed()), vec![end(answered())]);
    let error = one_shot(&fake, &asked(), never())
        .await
        .result
        .expect_err("no reply");
    assert_eq!(error.code(), RunError::EmptyAnswer.code());
}

#[tokio::test]
async fn test_a_signal_during_the_turn_asks_the_daemon_to_stop_the_run_once() {
    let stopped = RunError::Stopped {
        signal: "SIGINT".to_string(),
        status: 130,
    };
    let mut fake = Fake::new(Ok(placed()), vec![notice("working"), end(end_of(&stopped))]);
    // The end is held until the stop is asked: the signal is heard while
    // the turn is in flight, never after it ended.
    fake.held = true;
    let done = one_shot(&fake, &asked(), now("SIGINT")).await;
    let error = done.result.expect_err("stopped");
    assert_eq!(error.code(), "run-application-stopped");
    assert_eq!(error.exit(), Exit::signal(2));
    assert_eq!(error.exit().code(false), 130);
    let stops = fake.stops.lock().unwrap().clone();
    assert_eq!(stops.len(), 1, "asked once: {stops:?}");
    assert_eq!(stops[0].0, "SIGINT");
    let keyed = sessions::keyed_book(
        run_cover(RUN),
        &runs::RequestStop {
            reason: "SIGINT".to_string(),
        },
        &format!("stop:{RUN}"),
    );
    assert_eq!(
        stops[0].1,
        format!("{:?}", keyed.page.unwrap().command_id.unwrap()),
        "keyed by the run"
    );
}

#[tokio::test]
async fn test_a_signal_heard_before_the_run_is_placed_is_sent_once_it_is() {
    let mut fake = Fake::new(Ok(placed()), vec![end(answered())]);
    // The signal is ready at once and the create only after a yield: it is
    // heard before the run is placed, and the stream's end is held until
    // the stop is asked.
    fake.yields = true;
    fake.held = true;
    let done = one_shot(&fake, &asked(), now("SIGTERM")).await;
    let error = done.result.expect_err("stopped");
    assert_eq!(error.exit().code(false), 143);
    assert_eq!(fake.stops.lock().unwrap().len(), 1);
}

#[tokio::test]
async fn test_a_stop_that_cannot_reach_the_daemon_is_said_and_the_run_followed() {
    let mut fake = Fake::new(Ok(placed()), vec![reply("late"), end(answered())]);
    fake.stop_fails = true;
    fake.held = true;
    let done = one_shot(&fake, &asked(), now("SIGHUP")).await;
    let error = done.result.expect_err("stopped");
    assert_eq!(error.exit().code(false), 129);
    let said: Vec<String> = done.notices.iter().map(|n| format!("{n:?}")).collect();
    assert!(
        said.iter().any(|n| n.contains("could not be delivered")),
        "{said:?}"
    );
    assert_eq!(fake.stops.lock().unwrap().len(), 1, "asked once");
}

#[tokio::test]
async fn test_a_stream_that_breaks_before_the_end_fails_the_command() {
    let fake = Fake::new(Ok(placed()), vec![notice("n")]);
    let error = one_shot(&fake, &asked(), never())
        .await
        .result
        .expect_err("broken");
    assert_eq!(error.exit(), Exit::FAILED);
    let mut fake = Fake::new(Ok(placed()), Vec::new());
    fake.watch = Some(Called::Unreachable {
        detail: "gone".to_string(),
    });
    let error = one_shot(&fake, &asked(), never())
        .await
        .result
        .expect_err("unwatched");
    assert_eq!(error.code(), "daemon-ports-unreachable");
    assert_eq!(error.exit(), Exit::FAILED);
}

#[tokio::test]
async fn test_a_create_the_daemon_refused_ends_the_command_refused() {
    let fake = Fake::new(
        Err(Called::Refused {
            code: 5,
            refusal: daemon_ports::Any::from_msg(&v1::ProjectNotFound {
                name: "projects/x".to_string(),
            })
            .unwrap(),
        }),
        Vec::new(),
    );
    let error = one_shot(&fake, &asked(), never())
        .await
        .result
        .expect_err("refused");
    assert_eq!(error.exit(), Exit::REFUSED);
    let fake = Fake::new(
        Err(Called::Unimplemented {
            method: "m".to_string(),
        }),
        Vec::new(),
    );
    let error = one_shot(&fake, &asked(), never())
        .await
        .result
        .expect_err("unserved");
    assert_eq!(error.exit(), Exit::FAILED);
}

#[test]
fn test_no_daemon_ends_the_command_as_the_reach_says() {
    let error = RunError::Unreached(daemon_ports::Unreached::NotStarted {
        log: "~/l".to_string(),
    });
    assert_eq!(error.code(), "daemon-ports-not-started");
    assert_eq!(error.exit(), Exit::FAILED);
    let declined = RunError::Unreached(daemon_ports::Unreached::Declined(Arc::new(Report::new(
        "x-y",
    ))));
    assert_eq!(declined.exit(), Exit::REFUSED);
    assert_eq!(declined.code(), "x-y");
}
