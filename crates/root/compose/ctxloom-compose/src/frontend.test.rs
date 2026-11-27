//! The frontend's wiring, its pure parts: what a report is on the wire,
//! what its holding came to, what the daemon reads back of the reports, and
//! what a prepared session's end says on its watch.

use angzarr_embedded::v1::command_page;
use daemon_proto::v1;
use prost::Message;
use run_application::frontend::Report;
use run_ports::{Delivery, Launch, LaunchKind, Stdio, TurnCommand};

use super::{beside, external_id_of, project_of, report_book, reported_of, StopCause, Watch};

const RUN: &str = "0195f0a4-7b1e-7000-8000-000000000001";
const SESSION: &str = "0195f0a4-7b1e-7000-8000-000000000002";

fn launch() -> Launch {
    let mut command = TurnCommand::new("/bin/claude");
    command.cwd = Some("/work/checkout".to_string());
    let mut launch = Launch::new(LaunchKind::Terminal, "claude-code", Stdio::Inherit, command);
    launch.at.run = RUN.to_string();
    launch.at.session = SESSION.to_string();
    launch.at.harp = "amber-quiet-heron".to_string();
    launch.at.incarnation = 1;
    launch.session_lock = "/work/.ctxloom/sessions/amber-quiet-heron.lock".to_string();
    launch
}

/// The fact a book carries: its type, its key and its payload.
fn fact_of(book: &angzarr_embedded::v1::CommandBook) -> (String, String, Vec<u8>) {
    let Some(command_page::Body::Fact(fact)) = book.page.clone().and_then(|page| page.body) else {
        panic!("a fact body")
    };
    let payload = fact.payload.expect("a payload");
    (
        payload.type_url,
        fact.origin.expect("an origin").external_id,
        payload.value,
    )
}

#[test]
fn each_report_is_its_run_s_fact_keyed_as_the_launcher_keys_its_own() {
    let started = report_book(&launch(), &Report::Started { pid: 4242 });
    let (url, key, bytes) = fact_of(&started);
    assert!(url.ends_with(".RecordRunnerStarted"), "{url}");
    assert_eq!(key, format!("started:{RUN}"));
    let wire =
        <runs::RecordRunnerStarted as angzarr_embedded::markup::DomainMessage>::Wire::decode(
            bytes.as_slice(),
        )
        .unwrap();
    assert_eq!(wire.pid, 4242);
    assert_eq!(wire.runtime, "claude-code");
    assert_eq!(wire.workspace, "/work/checkout");
    let exited = report_book(
        &launch(),
        &Report::Exited {
            code: None,
            signal: Some(15),
        },
    );
    let (url, key, bytes) = fact_of(&exited);
    assert!(url.ends_with(".RecordRunExited"), "{url}");
    assert_eq!(key, format!("exit:{RUN}"));
    let wire = <runs::RecordRunExited as angzarr_embedded::markup::DomainMessage>::Wire::decode(
        bytes.as_slice(),
    )
    .unwrap();
    assert_eq!((wire.code, wire.signal), (0, 15));
    let failed = report_book(
        &launch(),
        &Report::NotStarted {
            deterministic: true,
            detail: "held".to_string(),
        },
    );
    let (url, key, _) = fact_of(&failed);
    assert!(url.ends_with(".RecordLaunchFailed"), "{url}");
    assert_eq!(key, format!("launch-failed:{RUN}"));
    for book in [started, exited, failed] {
        assert_eq!(book.cover.expect("a cover").domain, runs::RUN_DOMAIN);
    }
}

#[test]
fn the_lock_s_project_is_three_up_from_it() {
    let wiring = crate::Wiring::new(std::sync::Arc::new(process_env::EnvSnapshot::from_parts(
        os::host().flavor(),
        Ok(std::path::PathBuf::from("/elsewhere")),
        std::path::PathBuf::from("/tmp"),
        Vec::new(),
    )));
    let Ok(wiring) = wiring else { return };
    assert_eq!(
        project_of(&launch(), &wiring),
        std::path::PathBuf::from("/work")
    );
}

// Slice 4: what a report's one attempt came to, as the frontend is told it.
#[test]
fn a_reports_holding_is_what_the_frontend_is_told() {
    use run_application::frontend::Reported;
    use runs_application::{Kept, NotHeld, Outcome, Why};
    let kept = |outcome| Kept {
        external_id: "started:r".to_string(),
        outcome,
    };
    assert_eq!(
        reported_of(Some(&kept(Outcome::Delivered(Delivery::Duplicate)))),
        Reported::Delivered(Delivery::Duplicate)
    );
    assert_eq!(
        reported_of(Some(&kept(Outcome::Held(Why::Unreached {
            why: "no daemon".to_string()
        })))),
        Reported::Held {
            why: "no daemon".to_string()
        }
    );
    let Reported::Held { why } = reported_of(Some(&kept(Outcome::Held(Why::Older {
        daemon: "v0.1.0-a".to_string(),
        own: "v0.2.0-b".to_string(),
    })))) else {
        panic!("held");
    };
    assert!(
        why.contains("v0.1.0-a") && why.contains("v0.2.0-b"),
        "{why}"
    );
    assert!(matches!(
        reported_of(Some(&kept(Outcome::HeldBehind))),
        Reported::Held { .. }
    ));
    assert!(matches!(
        reported_of(Some(&kept(Outcome::NotHeld(NotHeld::Foreign)))),
        Reported::Lost { .. }
    ));
    assert!(matches!(reported_of(None), Reported::Lost { .. }));
}

#[test]
fn a_reports_external_id_is_its_books() {
    let book = report_book(&launch(), &Report::Started { pid: 7 });
    assert_eq!(external_id_of(&book), format!("started:{RUN}"));
    assert_eq!(
        external_id_of(&angzarr_embedded::v1::CommandBook::default()),
        ""
    );
}

fn told(stop_at: Option<u64>) -> liveness::FrontendNotice {
    liveness::FrontendNotice {
        text: "ctxloom is shutting down: this session will be stopped in 2s; finish and exit"
            .to_string(),
        stop_at,
    }
}

// A notice is shown once, when it is first seen or replaced; nothing is
// shown or stopped for none (owner, 2026-11-15, OD-63).
#[test]
fn a_frontend_shows_each_notice_once() {
    let mut watch = Watch::default();
    assert_eq!(watch.look(false, None, 10), (None, None));
    assert_eq!(
        watch.look(false, Some(told(None)), 10),
        (Some(told(None).text), None)
    );
    assert_eq!(
        watch.look(false, Some(told(None)), 11),
        (None, None),
        "once"
    );
    let later = liveness::FrontendNotice {
        text: "another".to_string(),
        stop_at: None,
    };
    assert_eq!(
        watch.look(false, Some(later), 12),
        (Some("another".to_string()), None),
        "a replaced notice is shown again"
    );
}

// A stop asked stops it at once; a notice's `stop_at` stops it once its
// time has come, even once its file is gone, and not before.
#[test]
fn a_frontend_stops_when_asked_or_when_the_drain_told_of_ends() {
    let mut watch = Watch::default();
    assert_eq!(watch.look(true, None, 0), (None, Some(StopCause::Asked)));
    let mut watch = Watch::default();
    let _ = watch.look(false, Some(told(Some(100))), 98);
    assert_eq!(watch.look(false, None, 99), (None, None), "not yet");
    assert_eq!(
        watch.look(false, None, 100),
        (None, Some(StopCause::DrainEnded)),
        "its time came: the file being gone changes nothing"
    );
    assert_eq!(
        watch.look(true, None, 100),
        (None, Some(StopCause::Asked)),
        "an ask is said as one"
    );
}

#[test]
fn a_frontend_says_why_it_stopped() {
    assert!(StopCause::Asked.said().contains("ctxloom shutdown"));
    assert!(StopCause::DrainEnded.said().contains("drain"));
}

// FORCED (2026-11-15): the command's main thread holds std's stderr lock for
// its whole run, and the frontend's watch writes its notices to stderr. Run
// beside the session, on the thread that polls it (the lock's holder), the
// write is made at once and the session ends; spawned on the runtime's one
// worker, it waited for the lock for good.
#[test]
fn a_watch_beside_the_session_writes_while_the_command_holds_stderr() {
    use std::io::Write;
    let (tell, heard) = std::sync::mpsc::channel();
    std::thread::spawn(move || {
        let runtime = super::run_runtime().expect("a runtime");
        let held = std::io::stderr().lock();
        let wrote = std::sync::Arc::new(std::sync::atomic::AtomicBool::new(false));
        let marked = std::sync::Arc::clone(&wrote);
        let out = runtime.block_on(beside(
            async {
                tokio::time::sleep(std::time::Duration::from_millis(200)).await;
                7
            },
            Some(async move {
                let _ = writeln!(std::io::stderr(), "a notice");
                marked.store(true, std::sync::atomic::Ordering::SeqCst);
            }),
        ));
        drop(held);
        let _ = tell.send((out, wrote.load(std::sync::atomic::Ordering::SeqCst)));
    });
    assert_eq!(
        heard.recv_timeout(std::time::Duration::from_secs(20)),
        Ok((7, true))
    );
}

// What runs beside ends when the session does, unfinished.
#[test]
fn what_runs_beside_the_session_is_dropped_when_it_ends() {
    let runtime = super::run_runtime().expect("a runtime");
    let out = runtime.block_on(beside(async { 3 }, Some(std::future::pending::<()>())));
    assert_eq!(out, 3);
    let none: Option<std::future::Ready<()>> = None;
    assert_eq!(runtime.block_on(beside(async { 4 }, none)), 4);
}

fn waiting_ask(name: &str) -> v1::Approval {
    v1::Approval {
        name: format!("projects/calm-quiet-heron/approvals/{name}"),
        child: "projects/calm-quiet-heron/sessions/brisk-copper-moth".to_string(),
        tool_name: "Bash".to_string(),
        state: "waiting".to_string(),
        ..v1::Approval::default()
    }
}

fn asks_record(wiring: &crate::Wiring) -> String {
    wiring
        .fs()
        .read_to_string(&wiring.home().join(".ctxloom/test-frontend-asks"))
        .unwrap_or_default()
}

#[test]
fn test_a_look_rings_the_bell_once_for_each_new_ask_and_records_it_then_the_look() {
    let world = crate::fixtures::World::new("asks");
    let wiring = world.wiring();
    crate::fixtures::mkdirs(wiring.home().join(".ctxloom"));
    let mut told = crate::frontend_asks::AsksTold::default();
    let mut rung = 0;
    let asks = [waiting_ask("ask-a-b"), waiting_ask("ask-c-d")];

    super::tell_news(&mut told, &asks, &wiring, || rung += 1);
    assert_eq!(rung, 2);
    assert_eq!(asks_record(&wiring), "told ask-a-b\ntold ask-c-d\nlook\n");

    // The same asks again: no bell, the look recorded after the rest.
    super::tell_news(&mut told, &asks, &wiring, || rung += 1);
    assert_eq!(rung, 2);
    assert_eq!(
        asks_record(&wiring),
        "told ask-a-b\ntold ask-c-d\nlook\nlook\n"
    );
}
