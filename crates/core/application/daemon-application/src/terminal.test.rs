//! A session the daemon prepares: its launch handed over and answered, its
//! start and exit the frontend's reports, its close told as a stop, and a
//! frontend gone without a word, each forced by the script.

use std::collections::VecDeque;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use daemon_proto::v1;
use run_ports::{
    Exit, Launch, LaunchAt, LaunchFailure, LaunchKind, Limits, LockProbe, ProgramNotFound,
    RunLocks, RunnerLauncher, Stdio, TurnCommand,
};

use super::{
    Created, FrontendReports, Frontended, Placed, Seen, TerminalHost, TerminalRequest, Terminals,
    Told, Waits, START_WITHIN,
};

/// Reports as the run's stream holds them at each read, in turn; the last
/// is held after.
struct Script(Mutex<VecDeque<Seen>>);

impl Script {
    fn of(seen: Vec<Seen>) -> Arc<Script> {
        Arc::new(Script(Mutex::new(seen.into())))
    }
}

impl FrontendReports for Script {
    fn seen(&self, _: &LaunchAt) -> Seen {
        let mut seen = self.0.lock().unwrap();
        if seen.len() > 1 {
            seen.pop_front().unwrap()
        } else {
            seen.front().cloned().unwrap_or_default()
        }
    }
}

#[derive(Default)]
struct Clock(Mutex<Duration>);

impl Waits for Clock {
    fn elapsed(&self) -> Duration {
        *self.0.lock().unwrap()
    }

    fn wait(&self, pause: Duration) {
        *self.0.lock().unwrap() += pause;
    }
}

/// The session's lock as the frontend holds it: held, or let go.
struct Locks(LockProbe);

impl RunLocks for Locks {
    fn hold_run(&self, _: &str) -> Result<run_ports::HeldLock, run_ports::LockError> {
        unreachable!()
    }

    fn probe(&self, _: &str) -> LockProbe {
        unreachable!()
    }

    fn session(&self, project_dir: &str, harp: &str) -> LockProbe {
        assert_eq!((project_dir, harp), ("/work", "amber-quiet-heron"));
        self.0
    }
}

/// A finder that finds every program under /bin.
struct Finder;

impl RunnerLauncher for Finder {
    fn launch(
        &self,
        _: &TurnCommand,
        _: &Limits,
    ) -> Result<Box<dyn run_ports::Running>, LaunchFailure> {
        unreachable!()
    }

    fn launch_terminal(
        &self,
        _: &TurnCommand,
    ) -> Result<Box<dyn run_ports::Running>, LaunchFailure> {
        unreachable!()
    }

    fn find_program(&self, command: &TurnCommand) -> Result<String, ProgramNotFound> {
        Ok(format!("/bin/{}", command.program))
    }
}

/// At most this many of a watch's events are read: more is a watch that
/// never ends.
const WATCH_BOUND: usize = 16;

/// A watch's events, read to its end, which must come within
/// [`WATCH_BOUND`] of them. Bounded, so a watch that never ends (its cursor
/// stuck, or an event made up each call) fails its test at once instead of
/// growing a `Vec` until the machine has no memory left: such a mutant did
/// that twice on 2026-11-15, to about 70 GB.
fn watched(watch: impl Iterator<Item = v1::RunEvent>) -> Vec<v1::RunEvent> {
    let events: Vec<v1::RunEvent> = watch.take(WATCH_BOUND).collect();
    assert!(
        events.len() < WATCH_BOUND,
        "the watch never ended: {events:?}"
    );
    events
}

/// A host that tells, inside its own call, what it is scripted to: each
/// session prepared as its run (`Some`), or ended before its launch
/// (`None`); it keeps each `Told`, so the test ends it. Every order is
/// forced, none waited for.
#[derive(Default)]
struct Host {
    script: Mutex<VecDeque<Option<String>>>,
    told: Mutex<Vec<Told>>,
}

impl TerminalHost for Arc<Host> {
    fn host(&self, _: TerminalRequest, told: Told) {
        match self.script.lock().unwrap().pop_front().flatten() {
            Some(run) => told.prepared(placed(&run), launch()),
            None => told.done(vec![end(20)]),
        }
        self.told.lock().unwrap().push(told);
    }
}

fn placed(run: &str) -> Placed {
    Placed {
        run: run.to_string(),
        name: "projects/p/sessions/amber-quiet-heron".to_string(),
        engine: "claude-code".to_string(),
    }
}

fn launch() -> Launch {
    let mut launch = Launch::new(
        LaunchKind::Terminal,
        "claude-code",
        Stdio::Inherit,
        TurnCommand::new("/bin/claude"),
    );
    launch.at.harp = "amber-quiet-heron".to_string();
    launch.at.run = "run-1".to_string();
    launch
}

fn frontended(told: Told, reports: Arc<Script>, lock: LockProbe) -> (Frontended, Arc<Clock>) {
    let clock = Arc::new(Clock::default());
    let frontended = Frontended {
        told,
        project_harp: "calm-quiet-otter".to_string(),
        reports,
        waits: Arc::clone(&clock) as Arc<dyn Waits>,
        locks: Arc::new(Locks(lock)),
        project_dir: "/work".to_string(),
        lock_path: Arc::new(|harp: &str| format!("/work/.ctxloom/sessions/{harp}/session.lock")),
        finder: Arc::new(Finder),
    };
    (frontended, clock)
}

fn limits() -> Limits {
    Limits::new(Duration::from_secs(1))
}

fn told() -> Told {
    Told(Arc::default())
}

fn started(pid: u32) -> Seen {
    Seen {
        started: Some(pid),
        ..Seen::default()
    }
}

fn exited(code: i32) -> Seen {
    Seen {
        started: Some(7),
        exited: Some(Exit {
            code: Some(code),
            signal: None,
        }),
        ..Seen::default()
    }
}

#[test]
fn test_the_launch_is_handed_over_with_the_session_lock_and_its_start_is_the_frontend_s_pid() {
    let told = told();
    let reports = Script::of(vec![Seen::default(), Seen::default(), started(4242)]);
    let (frontended, clock) = frontended(told.clone(), reports, LockProbe::Held);
    let running = frontended.start(&launch(), &limits()).unwrap();
    assert_eq!(running.pid(), 4242);
    // It waited for the report, polling.
    assert_eq!(clock.elapsed(), super::POLL * 2);
    let state = told.0.state();
    let (placed, handed) = state.prepared.clone().unwrap();
    assert_eq!(
        placed,
        Placed {
            run: "run-1".to_string(),
            name: "projects/calm-quiet-otter/sessions/amber-quiet-heron".to_string(),
            engine: "claude-code".to_string(),
        }
    );
    let mut expected = launch();
    expected.session_lock = "/work/.ctxloom/sessions/amber-quiet-heron/session.lock".to_string();
    assert_eq!(handed, expected);
}

#[test]
fn test_a_launch_the_frontend_never_started_is_its_failure() {
    let reports = Script::of(vec![Seen {
        failed: Some(LaunchFailure {
            deterministic: true,
            detail: "no such program".to_string(),
        }),
        ..Seen::default()
    }]);
    let (frontended, _) = frontended(told(), reports, LockProbe::Held);
    let failure = frontended.start(&launch(), &limits()).err().unwrap();
    assert!(failure.deterministic);
    assert_eq!(failure.detail, "no such program");
}

#[test]
fn test_a_frontend_that_never_reports_its_start_is_given_up_after_the_bound() {
    let reports = Script::of(vec![Seen::default()]);
    let (frontended, clock) = frontended(told(), reports, LockProbe::Free);
    let failure = frontended.start(&launch(), &limits()).err().unwrap();
    assert!(!failure.deterministic);
    assert!(failure.detail.contains("within 1m"), "{}", failure.detail);
    assert!(clock.elapsed() >= START_WITHIN);
    assert!(clock.elapsed() < START_WITHIN + super::POLL * 2);
}

#[test]
fn test_the_exit_is_the_frontend_s_report() {
    let reports = Script::of(vec![started(7), started(7), exited(3)]);
    let (frontended, _) = frontended(told(), reports, LockProbe::Held);
    let running = frontended.start(&launch(), &limits()).unwrap();
    let output = running.finish().unwrap();
    assert_eq!(output.exit.code, Some(3));
    assert_eq!(output.exit.signal, None);
}

#[test]
fn test_a_frontend_gone_without_its_exit_report_ended_as_a_kill() {
    let reports = Script::of(vec![started(7)]);
    let (frontended, _) = frontended(told(), reports, LockProbe::Free);
    let running = frontended.start(&launch(), &limits()).unwrap();
    let output = running.finish().unwrap();
    assert_eq!(output.exit.code, None);
    assert_eq!(output.exit.signal, Some(9));
}

#[test]
fn test_an_exit_reported_as_the_frontend_lets_its_lock_go_is_its_exit() {
    // The frontend records its exit, then lets go: a read that found the
    // lock free reads again, and finds the exit.
    let reports = Script::of(vec![started(7), started(7), exited(0)]);
    let (frontended, _) = frontended(told(), reports, LockProbe::Free);
    let running = frontended.start(&launch(), &limits()).unwrap();
    assert_eq!(running.finish().unwrap().exit.code, Some(0));
}

#[test]
fn test_the_session_closed_tells_the_frontend_to_stop_once() {
    let told = told();
    let closed = Seen {
        started: Some(7),
        closed: true,
        ..Seen::default()
    };
    let reports = Script::of(vec![
        started(7),
        closed.clone(),
        closed.clone(),
        Seen {
            closed: true,
            ..exited(143)
        },
    ]);
    let (frontended, _) = frontended(told.clone(), reports, LockProbe::Held);
    let running = frontended.start(&launch(), &limits()).unwrap();
    assert_eq!(running.finish().unwrap().exit.code, Some(143));
    let events = told.0.state().events.clone();
    assert_eq!(
        events,
        [v1::RunEvent {
            event: Some(v1::run_event::Event::Stop(v1::RunStop {
                reason: "closed".to_string(),
                grace_ms: 5000,
            })),
        }]
    );
}

#[test]
fn test_a_turn_or_a_child_is_not_this_launcher_s() {
    let (frontended, _) = frontended(told(), Script::of(Vec::new()), LockProbe::Held);
    let mut child = launch();
    child.kind = LaunchKind::Child;
    assert!(frontended.start(&child, &limits()).is_err());
    assert!(frontended
        .launch(&TurnCommand::new("claude"), &limits())
        .is_err());
    assert!(frontended
        .launch_terminal(&TurnCommand::new("claude"))
        .is_err());
    assert_eq!(
        frontended
            .find_program(&TurnCommand::new("claude"))
            .unwrap(),
        "/bin/claude"
    );
}

fn end(exit: i32) -> v1::RunEvent {
    v1::RunEvent {
        event: Some(v1::run_event::Event::End(v1::RunEnd {
            exit,
            ..v1::RunEnd::default()
        })),
    }
}

fn host(script: Vec<Option<&str>>) -> (Arc<Host>, Terminals) {
    let host = Arc::new(Host::default());
    *host.script.lock().unwrap() = script.into_iter().map(|r| r.map(str::to_string)).collect();
    let terminals = Terminals::new(Box::new(Arc::clone(&host)));
    (host, terminals)
}

fn last_told(host: &Host) -> Told {
    host.told.lock().unwrap().last().cloned().unwrap()
}

#[test]
fn test_create_answers_the_prepared_launch_and_its_watch_streams_the_stop_and_the_end() {
    let (host, terminals) = host(vec![Some("run-1")]);
    let created = terminals.create(TerminalRequest::default());
    assert_eq!(
        created,
        Created::Prepared(placed("run-1"), Box::new(launch()))
    );
    assert_eq!(terminals.get("run-1"), Some(placed("run-1")));
    // Prepared and not ended: a client the daemon serves.
    assert_eq!(terminals.hosting(), 1);
    assert!(terminals.stop("run-1", "closed"));
    let told = last_told(&host);
    told.done(vec![end(0)]);
    let events = watched(terminals.watch("run-1").unwrap());
    assert_eq!(events.len(), 2);
    assert!(matches!(
        events[0].event,
        Some(v1::run_event::Event::Stop(_))
    ));
    assert_eq!(events[1], end(0));
    assert_eq!(terminals.hosting(), 0);
    // Ended, it is told no stop.
    assert!(!terminals.stop("run-1", "closed"));
    assert!(terminals.watch("run-2").is_none());
    assert!(terminals.get("run-2").is_none());
    assert!(!terminals.stop("run-2", "closed"));
}

#[test]
fn test_a_stop_told_twice_is_one_stop() {
    let (host, terminals) = host(vec![Some("run-1")]);
    terminals.create(TerminalRequest::default());
    assert!(terminals.stop("run-1", "closed"));
    assert!(terminals.stop("run-1", "closed"));
    last_told(&host).done(Vec::new());
    assert_eq!(watched(terminals.watch("run-1").unwrap()).len(), 1);
}

#[test]
fn test_a_session_that_ended_before_its_launch_answers_its_end() {
    let (_, terminals) = host(vec![None]);
    assert_eq!(
        terminals.create(TerminalRequest::default()),
        Created::Ended(vec![end(20)])
    );
    assert_eq!(terminals.hosting(), 0);
}

#[test]
fn test_only_the_newest_ended_sessions_are_kept() {
    let runs: Vec<String> = (0..=super::ENDED_KEPT + 1)
        .map(|n| format!("run-{n:02}"))
        .collect();
    let mut script: Vec<Option<&str>> = runs.iter().map(|r| Some(r.as_str())).collect();
    script.push(None);
    let (host, terminals) = host(script);
    for _ in &runs {
        terminals.create(TerminalRequest::default());
        last_told(&host).done(vec![end(0)]);
    }
    // One more create forgets the oldest past the bound.
    terminals.create(TerminalRequest::default());
    assert!(terminals.get("run-00").is_none());
    assert!(terminals.get("run-01").is_none());
    assert!(terminals.get("run-02").is_some());
    assert!(terminals.get("run-17").is_some());
}
