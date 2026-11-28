//! The ports the root gives the daemon's launcher (T18), each over a
//! temporary home.

use std::collections::BTreeMap;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use daemon_application::{ChildHomes, Ears};
use run_ports::{ChildObservation, Exit};

use super::{
    advertised, said, stays_for, step_of, why_it_exits, Advertised, Homes, Pumps, Say, Workspaces,
};
use crate::files::DaemonFiles;
use crate::fixtures;

const SESSION: &str = "5f0c7a52-2222-5222-8222-000000000002";

// A child's session home and its files are in its project's session
// directory.
#[test]
fn a_childs_home_and_its_files_are_in_its_projects_session_directory() {
    let root = fixtures::root();
    let project = root.root().join("work");
    fixtures::mkdirs(&project);
    let homes = Homes(fixtures::fs());
    let dir = project.display().to_string();
    let mut records = homes.records(&dir);
    // Joined as the platform joins (`\` on Windows), as the records join it.
    assert_eq!(
        records.dir("kid"),
        project
            .join(".ctxloom")
            .join("sessions")
            .join("kid")
            .display()
            .to_string()
    );
    records.write("kid", "home/settings.json", b"{}").unwrap();
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
// is found. Its own is left be, one gone is gone (the daemon exits, owner
// 2026-11-17), and another daemon's means this one lost it.
#[test]
fn a_daemon_keeps_its_advertisement_and_tells_a_gone_one_from_anothers() {
    let own = run_ports::Discovery {
        stamp: "v0.1.0-abc1234".to_string(),
        pid: 7,
        endpoint: "https://127.0.0.1:4242".to_string(),
        cert: run_ports::PublicCert::from_der(vec![0x30, 0x00]).unwrap(),
        bearer: run_ports::Token::from_bytes([7; 32]),
        home: "/home/u".to_string(),
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

// OD-60, the owner's ruling of 2026-11-17: a daemon never makes a file in
// its home again once it has started. Its discovery file removed (its home
// still there: the first of a `rm -rf`'s depth-first removals), the tick
// says it exits, and nothing is advertised again.
#[test]
fn a_daemon_whose_discovery_file_is_gone_exits_and_makes_it_nothing_again() {
    let root = fixtures::root();
    let home = root.root().join("home");
    let files = DaemonFiles::new(fixtures::fs(), &home);
    let held = files.hold_instance(Duration::ZERO).expect("the lock");
    let own = run_ports::Discovery {
        stamp: "v0.1.0-abc1234".to_string(),
        pid: 7,
        endpoint: "https://127.0.0.1:4242".to_string(),
        cert: run_ports::PublicCert::from_der(vec![0x30, 0x00]).unwrap(),
        bearer: run_ports::Token::from_bytes([7; 32]),
        home: "/home/u".to_string(),
    };
    files.publish(&own).unwrap();
    assert_eq!(
        why_it_exits(&files, &own, &held),
        None,
        "its own: it serves on"
    );
    let discovery = home.join(".ctxloom/daemon/daemon.discovery");
    fixtures::fs().remove_file(&discovery).unwrap();
    assert!(fixtures::exists(home.join(".ctxloom")), "its home is there");
    assert_eq!(
        why_it_exits(&files, &own, &held),
        Some("the discovery file was removed: exiting")
    );
    assert!(!fixtures::exists(&discovery), "advertised again");
}

// U6: a hosted child's session lock is the platform's file lock at
// `<project>/.ctxloom/sessions/<harp>/session.lock`, its directory made: held, a
// second hold is refused; let go, it is free again.
#[test]
fn a_childs_session_lock_is_held_once_and_let_go_when_dropped() {
    let root = fixtures::root();
    let project = root.root().join("work");
    fixtures::mkdirs(&project);
    let homes = Homes(fixtures::fs());
    let dir = project.display().to_string();
    let held = homes.hold_session(&dir, "kid").unwrap();
    assert_eq!(
        held.path(),
        project
            .join(".ctxloom")
            .join("sessions")
            .join("kid")
            .join("session.lock")
            .display()
            .to_string()
    );
    assert!(homes.hold_session(&dir, "kid").is_err(), "held already");
    drop(held);
    assert!(homes.hold_session(&dir, "kid").is_ok(), "free again");
}

// U6: the kill switch's steps, by the type of the book applied.
#[test]
fn each_kill_step_is_the_book_it_names() {
    use angzarr_embedded::v1::{command_page, Command, CommandBook, CommandPage, Fact};
    let book = |name: &str, fact: bool| {
        let payload = Some(angzarr_embedded::Any {
            type_url: format!("type.googleapis.com/ctxloom.x.v1.{name}"),
            value: Vec::new(),
        });
        CommandBook {
            cover: None,
            page: Some(CommandPage {
                body: Some(match fact {
                    true => command_page::Body::Fact(Fact {
                        payload,
                        origin: None,
                    }),
                    false => command_page::Body::Command(Command {
                        payload,
                        handling: None,
                    }),
                }),
                ..CommandPage::default()
            }),
        }
    };
    for (name, fact, step) in [
        ("EnqueueRun", false, Some("enqueued")),
        ("MintSession", false, Some("minted")),
        ("RecordRunnerStarted", true, Some("started")),
        ("RecordTurnStarted", true, Some("turn")),
        ("RecordPromptSubmitted", true, Some("hook")),
        ("RecordTurnEnded", true, Some("hook")),
        ("SendMessage", false, None),
    ] {
        assert_eq!(step_of(&book(name, fact)), step, "{name}");
    }
    assert_eq!(step_of(&CommandBook::default()), None);
}

// U6: the switch names one step, and is taken only by it.
#[test]
fn the_kill_switch_is_taken_by_the_step_it_names_alone() {
    let root = fixtures::root();
    let home = root.root().join("home");
    let files = DaemonFiles::new(fixtures::fs(), &home);
    assert!(!files.take_die_at("turn"), "none there");
    fixtures::mkdirs(home.join(".ctxloom/daemon"));
    fixtures::put_mode(home.join(".ctxloom/daemon/test-die-at"), "turn\n", 0o600);
    assert!(!files.take_die_at("hook"), "another step's");
    assert!(files.take_die_at("turn"));
    assert!(!files.take_die_at("turn"), "taken once");
}

// ---------------------- a read catches up first (owner, H3's condition)

/// Whether `future` is ready on one poll (a waker that wakes nothing: the
/// test drives the loop's rounds itself, never a timer).
fn ready<F: std::future::Future + ?Sized>(future: &mut std::pin::Pin<Box<F>>) -> bool {
    let mut cx = std::task::Context::from_waker(std::task::Waker::noop());
    future.as_mut().poll(&mut cx).is_ready()
}

// A read of the API (`Hosting::fresh`, which every projection query the
// daemon serves awaits first: GetSession, ListSessions, SearchSessions,
// GetTranscript, GetProject, AssembleContext) answers only once a round
// that STARTED after it was asked has finished, so its catch-up read what
// was committed before the read; a round already running when it was asked
// does not do. Forced: the rounds are the test's.
#[tokio::test]
async fn a_read_waits_for_a_round_started_after_it_was_asked() {
    use daemon_api::Hosting;
    let one_shots = Arc::new(daemon_application::OneShots::new(Box::new(Kept(
        Arc::default(),
    ))));
    let shared = super::Shared::new(
        session_index::SessionIndexes::default(),
        one_shots,
        unsaid(),
    );
    let running = shared.round_starts();
    let mut read = shared.fresh();
    assert!(!ready(&mut read), "no round since it was asked");
    shared.round_done(running);
    assert!(
        !ready(&mut read),
        "the round running when it was asked is not enough"
    );
    let next = shared.round_starts();
    assert!(!ready(&mut read), "a round started, not finished");
    shared.round_done(next);
    assert!(ready(&mut read), "the round after it finished: caught up");
}

/// A host whose one-shot is placed at once and ended when the test says,
/// through the `Told` it keeps.
struct Kept(Arc<Mutex<Vec<daemon_application::Told>>>);

struct NoStop;

impl daemon_application::OneShotControl for NoStop {
    fn stop(&self) {}
}

impl daemon_application::OneShotHost for Kept {
    fn host(
        &self,
        _: daemon_application::HostRequest,
        told: daemon_application::Told,
    ) -> Box<dyn daemon_application::OneShotControl> {
        told.placed(&run_application::Placement {
            run: "0195f0a4-7b1e-7000-8000-000000000001".to_string(),
            session: SESSION.to_string(),
            name: "projects/calm-otter/sessions/harp-1".to_string(),
            harp: "harp-1".to_string(),
            engine: "claude-code".to_string(),
            ..Default::default()
        });
        self.0.lock().unwrap().push(told);
        Box::new(NoStop)
    }
}

// askew-glitter: a one-shot's client hears its reply the moment it ends
// (`Told::done`), and may ask the daemon to stop at once. What the API
// says it hosts is the one-shots as they are NOW, never the loop's last
// round's copy: the round below saw it live, it then ended before the
// next, and a stop between them is not refused for it.
#[test]
fn a_one_shot_that_ended_is_not_hosted_before_the_loops_next_round() {
    use daemon_api::Hosting;
    use daemon_proto::v1::hosted_run::Kind;
    let told = Arc::new(Mutex::new(Vec::new()));
    let one_shots = Arc::new(daemon_application::OneShots::new(Box::new(Kept(
        Arc::clone(&told),
    ))));
    let shared = super::Shared::new(
        session_index::SessionIndexes::default(),
        Arc::clone(&one_shots),
        unsaid(),
    );
    one_shots.create(daemon_application::HostRequest::default());
    // The round, as the loop tells it: its children.
    let child = (
        SESSION.to_string(),
        "0195f0a4-7b1e-7000-8000-000000000009".to_string(),
    );
    shared.tell(vec![child], std::time::Instant::now());
    assert_eq!(
        kinds(&shared.hosted()),
        [
            ("000000000009", Kind::Child),
            ("000000000001", Kind::OneShot)
        ],
        "the round's child, and the one-shot live"
    );
    told.lock().unwrap()[0].done(run_application::Done {
        result: Ok(run_application::Answer {
            harp: "harp-1".to_string(),
            session: SESSION.to_string(),
            engine: "claude-code".to_string(),
            answer: "HELLO".to_string(),
        }),
        notices: Vec::new(),
    });
    assert_eq!(
        kinds(&shared.hosted()),
        [("000000000009", Kind::Child)],
        "the one-shot ended, with no round since"
    );
}

/// Each hosted run's root's last twelve digits, and its kind.
fn kinds(
    hosted: &[daemon_proto::v1::HostedRun],
) -> Vec<(&str, daemon_proto::v1::hosted_run::Kind)> {
    hosted
        .iter()
        .map(|run| (&run.run[run.run.len() - 12..], run.kind()))
        .collect()
}

/// A drain's period with no config saying one: the default, five minutes.
fn unsaid() -> super::Period {
    super::Period {
        default: daemon_application::HAND_OVER_PERIOD,
        configured: Box::new(|| None),
    }
}

// The owner's ruling of 2026-11-17: what the loop shares with the API says
// the daemon's home is gone once its ctxloom directory is removed, naming
// it, so a call that would create files there is refused; and says nothing
// while it is there.
#[test]
fn the_shared_round_says_the_daemons_home_is_gone_once_it_is_removed() {
    use daemon_api::Hosting;
    let root = fixtures::root();
    let home = root.root().join("home");
    let files = DaemonFiles::new(fixtures::fs(), &home);
    let _held = files.hold_instance(Duration::ZERO).expect("the lock");
    let one_shots = Arc::new(daemon_application::OneShots::new(Box::new(Kept(
        Arc::default(),
    ))));
    let shared = super::Shared::new(
        session_index::SessionIndexes::default(),
        one_shots,
        unsaid(),
    )
    .watching(DaemonFiles::new(fixtures::fs(), &home));
    assert_eq!(shared.home_gone(), None, "its home is there");
    fixtures::remove_dir(home.join(".ctxloom"));
    assert_eq!(
        shared.home_gone(),
        Some(home.join(".ctxloom").display().to_string())
    );
}

// Owner, 2026-11-15: a drain's period is the one its request asked
// (`ctxloom shutdown <duration>`), else the home config's (read again each
// time, so a change while it drains is the drain's), else the default; the
// hand-over's warning says the same.
#[test]
fn a_drains_period_is_the_requests_else_the_configs_else_the_default() {
    use daemon_api::Hosting;
    use std::sync::atomic::{AtomicU64, Ordering};
    let one_shots = || {
        Arc::new(daemon_application::OneShots::new(Box::new(Kept(
            Arc::default(),
        ))))
    };
    let said = Arc::new(AtomicU64::new(0));
    let config = Arc::clone(&said);
    let shared = super::Shared::new(
        session_index::SessionIndexes::default(),
        one_shots(),
        super::Period {
            default: Duration::from_secs(300),
            configured: Box::new(move || match config.load(Ordering::SeqCst) {
                0 => None,
                seconds => Some(Duration::from_secs(seconds)),
            }),
        },
    );
    assert_eq!(shared.drain_period(), Duration::from_secs(300));
    said.store(60, Ordering::SeqCst);
    assert_eq!(
        shared.drain_period(),
        Duration::from_secs(60),
        "the config's"
    );
    shared.drain(None);
    said.store(5, Ordering::SeqCst);
    assert_eq!(shared.drain_period(), Duration::from_secs(5), "read again");
    assert_eq!(shared.hand_over_seconds(), 5);
    let asked = super::Shared::new(
        session_index::SessionIndexes::default(),
        one_shots(),
        super::Period {
            default: Duration::from_secs(300),
            configured: Box::new(|| Some(Duration::from_secs(60))),
        },
    );
    assert!(!asked.draining());
    asked.drain(Some(Duration::from_secs(900)));
    asked.drain(Some(Duration::from_secs(1)));
    assert_eq!(
        asked.drain_period(),
        Duration::from_secs(900),
        "the first ask's"
    );
    assert!(asked.draining());
}

// Owner ruling 2026-11-16 (PREVENT the early drain exit): a draining
// daemon hosting nothing stays only while a frontend is live, within its
// drain's time and grace, and never for a plain stop.
#[test]
fn a_draining_daemon_stays_only_for_a_live_frontend_within_its_time() {
    assert!(stays_for(&[42], true, false), "a live frontend, in time");
    assert!(!stays_for(&[], true, false), "no live frontend");
    assert!(!stays_for(&[42], false, false), "its time has passed");
    assert!(!stays_for(&[42], true, true), "a plain stop");
}

/// A `git` that records who it is and what `MARKER` it saw, then fails.
fn recording_git(dir: &std::path::Path, who: &str, log: &std::path::Path) {
    fixtures::mkdirs(dir);
    fixture_program::install_program(
        &*fixtures::fs(),
        &dir.join("git"),
        &format!(
            "#!/bin/sh\nprintf '%s MARKER=%s\\n' {who} \"$MARKER\" >> '{}'\nexit 1\n",
            log.display()
        ),
    )
    .unwrap();
}

// A child's checkout is made with git in the environment its LAUNCHER sent
// (the defect of 2026-11-18): the user's `GIT_*` and `SSH_AUTH_SOCK` reach
// it, never only the daemon's own PATH, LANG and TMPDIR. A sweep, which no
// launcher asked, runs in the daemon's.
#[test]
fn a_childs_checkout_is_made_with_git_in_its_launchers_environment() {
    if !fixtures::runs_scripts() {
        return;
    }
    let root = fixtures::root();
    let dir = root.root();
    let log = dir.join("git.log");
    let (daemons, launchers) = (dir.join("daemon-bin"), dir.join("launcher-bin"));
    recording_git(&daemons, "daemon", &log);
    recording_git(&launchers, "launcher", &log);
    let (home, project) = (dir.join("home"), dir.join("work"));
    fixtures::mkdirs(&home);
    fixtures::mkdirs(&project);
    let base = ctxloom_compose::fixtures::host_snapshot();
    let daemon_env = Arc::new(process_env::EnvSnapshot::from_parts(
        base.flavor(),
        Ok(home.clone()),
        base.temp_dir().to_path_buf(),
        vec![(
            "PATH".into(),
            format!("{}:/usr/bin:/bin", daemons.display()).into(),
        )],
    ));
    let workspaces = Workspaces {
        fs: fixtures::fs(),
        home: home.clone(),
        env: daemon_env,
    };
    let launcher = BTreeMap::from([
        (
            "PATH".to_string(),
            format!("{}:/usr/bin:/bin", launchers.display()),
        ),
        ("MARKER".to_string(), "the-launchers".to_string()),
    ]);
    let request = isolation_ports::Request {
        project_dir: project.display().to_string(),
        work_dir: home.join("checkouts/kid").display().to_string(),
        agent: "dev".to_string(),
        artifact_patterns: Vec::new(),
    };
    let of = |env| {
        daemon_application::ChildWorkspaces::of(
            &workspaces,
            "calm-otter",
            &project.display().to_string(),
            env,
        )
    };
    let _ = of(Some(&launcher)).prepare(&request);
    let ran = fixtures::fs().read_to_string(&log).unwrap_or_default();
    assert!(ran.contains("launcher MARKER=the-launchers"), "{ran}");
    assert!(!ran.contains("daemon"), "{ran}");
    let _ = of(None).prepare(&request);
    let ran = fixtures::fs().read_to_string(&log).unwrap_or_default();
    assert!(
        ran.contains("daemon MARKER="),
        "the sweep's is the daemon's: {ran}"
    );
}

/// What a task holds until it has finished being dropped, slowly: its
/// release is the test's to hold back (the race rule: the interleaving of a
/// cancelled task's drop with the store's close is forced, not waited for).
struct SlowRelease(Option<Arc<()>>);

impl Drop for SlowRelease {
    fn drop(&mut self) {
        std::thread::sleep(Duration::from_millis(400));
        self.0.take();
    }
}

// RED, EXPECTED (daemon/stop-flake; the CI flake "the stopped daemon never
// closed its store", `cli::run` on Linux, `cli::daemon` on macOS): the
// daemon's exit CUTS its SIGTERM listener with `JoinHandle::abort` and never
// waits for it, but the listener holds an `Arc<Shared>` (the read models,
// each through `StoreFill` a holder of the store), and a cancelled task is
// dropped later, by its worker. `core.close()` then finds the store held
// ("the run's store is still held (10 holders past the run's end) and was
// not closed"), leaves its `-wal` behind and the daemon exits with the
// store unclosed. The fix is `lifelong_ended` joining what it aborted
// (`terminated.abort(); let _ = terminated.await;`); the test passes then.
#[allow(
    clippy::disallowed_methods,
    reason = "a test kernel or store of its own"
)]
#[test]
fn a_terminate_listener_the_exit_cut_has_let_go_of_what_it_held_when_the_exit_goes_on() {
    let runtime = tokio::runtime::Builder::new_multi_thread()
        .worker_threads(2)
        .enable_all()
        .build()
        .expect("a runtime");
    runtime.block_on(async {
        let fs = Arc::new(file_system::Rooted::temp().expect("a temporary home"));
        let store = ctxloom_store_sqlite::Stores::new(fs.clone(), "/home/u")
            .open()
            .await
            .expect("the store opens");
        let mut kept = crate::Projectors::default();
        let core = crate::InProcessCore::start_with(store, |store| {
            kept = crate::Projectors::filled(store);
            kept.clone()
        })
        .await;
        let scheduler = crate::scheduling::start(
            core.share(),
            kept.due.clone().unwrap_or_default(),
            Arc::new(crate::SystemClock),
            Duration::from_secs(3600),
            Arc::new(|_: &str| {}),
        );
        let held = Arc::new(());
        let (started, running) = tokio::sync::oneshot::channel();
        let task = {
            let guard = SlowRelease(Some(Arc::clone(&held)));
            tokio::spawn(async move {
                let _guard = guard;
                let _ = started.send(());
                std::future::pending::<()>().await;
            })
        };
        running.await.expect("the listener is running");
        let say: Say = Arc::new(Mutex::new(Box::new(|_: &str| {})));
        super::lifelong_ended(Some(task), scheduler, &say).await;
        assert_eq!(
            Arc::strong_count(&held),
            1,
            "RED (daemon/stop-flake): the exit cut its SIGTERM listener and went on without \
             waiting for it to be dropped, so what it held (the read models, so the store) was \
             still held when the store closed: `lifelong_ended` must join what it aborts"
        );
        drop(kept);
        core.close().await.expect("the store closes");
    });
}

/// Recurse `depth` frames of about a kilobyte each, answering `depth`.
#[inline(never)]
fn deep(depth: usize) -> usize {
    let frame = std::hint::black_box([depth as u8; 1024]);
    if depth == 0 {
        0
    } else {
        deep(depth - 1) + 1 + usize::from(frame[1] / 255 > 1)
    }
}

// The daemon serves on the runtime `build(workers())` makes: its workers
// (never fewer than the minimum) are what keep messages flowing beside the
// turns parked, and its blocking pool has the daemon's stack, which a deep
// request needs (owner, 2026-11-19 evening). Seen from inside the served
// runtime, at the daemon's own "serving" line.
#[test]
fn the_daemon_serves_on_a_runtime_of_its_workers() {
    let root = fixtures::root();
    let home = root.root().join("home");
    fixtures::mkdirs(&home);
    let temp = root.root().join("tmp");
    fixtures::mkdirs(&temp);
    let home_text = home.display().to_string();
    let place = super::Place {
        stamp: "v0.0.0-test".to_string(),
        home: home.clone(),
        instance_lock: root.root().join("instance.lock"),
        temp,
        os: std::env::consts::OS.to_string(),
        user_home: None,
        engines: Vec::new(),
        env: Arc::new(process_env::EnvSnapshot::of([("HOME", home_text.as_str())])),
    };
    let pace = super::Pace {
        idle: Duration::from_millis(300),
        poll: Duration::from_millis(20),
        hand_over: Duration::from_secs(1),
    };
    let seen: Arc<Mutex<Vec<(usize, usize)>>> = Arc::default();
    let log = Arc::clone(&seen);
    let say: Box<dyn FnMut(&str) + Send> = Box::new(move |line: &str| {
        if line.starts_with("serving at ") {
            let runtime = tokio::runtime::Handle::current();
            let depth = 6 * 1024;
            let (done, deeply) = std::sync::mpsc::channel();
            runtime.spawn_blocking(move || done.send(deep(depth)).unwrap());
            let deeply = deeply.recv_timeout(Duration::from_secs(60)).unwrap();
            log.lock()
                .unwrap()
                .push((runtime.metrics().num_workers(), deeply));
        }
    });
    super::serve(fixtures::fs(), place, pace, say).expect("served to its idle end");
    assert_eq!(
        *seen.lock().unwrap(),
        [(crate::runtime::workers(), 6 * 1024)]
    );
}
