//! The frontend's order of steps over scripted ports: the locks around the
//! TUI (the session's, and the run's own it took before it asked, U5's
//! start-report race), each report after the step it reports, one attempt
//! each, held when no daemon takes it (slice 4, a daemon dying under a live
//! TUI), each race forced by the script.

use std::collections::VecDeque;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use run_ports::{
    BoxFuture, Delivery, Exit, HeldLock, Launch, LaunchFailure, LaunchKind, Limits, Running, Stdio,
    TurnCommand, TurnOutput,
};
use session_ports::{Held, LockError, SessionLock};

use super::{Frontend, Prepared, Report, Reported, Reports, Terminal};
use crate::RunError;

/// Everything that happened, in order.
type Timeline = Arc<Mutex<Vec<String>>>;

fn said(timeline: &Timeline, what: impl Into<String>) {
    timeline.lock().unwrap().push(what.into());
}

/// A lock that says when it is taken and let go, or is held by another.
struct Locks {
    timeline: Timeline,
    held_by_another: bool,
}

struct Guard(Timeline, String);

impl Drop for Guard {
    fn drop(&mut self) {
        said(&self.0, format!("unlock {}", self.1));
    }
}

impl SessionLock for Locks {
    fn hold(&mut self, harp: &str) -> Result<Held, LockError> {
        if self.held_by_another {
            return Err(LockError::Held(angzarr_diagnostic::Report::new(
                "file-lock-held",
            )));
        }
        said(&self.timeline, format!("lock {harp}"));
        Ok(Held::new(Guard(
            Arc::clone(&self.timeline),
            harp.to_string(),
        )))
    }
}

struct Spawned(u32);

impl Running for Spawned {
    fn pid(&self) -> u32 {
        self.0
    }

    fn finish(self: Box<Self>) -> Result<TurnOutput, LaunchFailure> {
        unreachable!("the frontend waits through its terminal")
    }
}

/// A terminal that spawns (or refuses to) and exits as told.
struct Tty {
    timeline: Timeline,
    refuse: bool,
    exit: Exit,
    /// What it was started with.
    started: Mutex<Option<Launch>>,
}

impl Terminal for Tty {
    fn start(&self, launch: &Launch, _: &Limits) -> Result<Box<dyn Running>, LaunchFailure> {
        if self.refuse {
            return Err(LaunchFailure {
                deterministic: true,
                detail: "no such program".to_string(),
            });
        }
        said(&self.timeline, "spawn");
        *self.started.lock().unwrap() = Some(launch.clone());
        Ok(Box::new(Spawned(4242)))
    }

    fn wait(&self, _: Box<dyn Running>) -> BoxFuture<'_, Result<TurnOutput, LaunchFailure>> {
        said(&self.timeline, "exited");
        let exit = self.exit;
        Box::pin(async move {
            Ok(TurnOutput {
                exit,
                stdout: Vec::new(),
                stderr: Vec::new(),
                timed_out: false,
                truncated: false,
                drain_incomplete: false,
            })
        })
    }
}

/// A daemon that answers each attempt as scripted (then applies).
struct Daemon {
    timeline: Timeline,
    script: Mutex<VecDeque<Reported>>,
}

impl Reports for Daemon {
    fn deliver<'a>(&'a self, _: &'a Launch, report: &'a Report) -> BoxFuture<'a, Reported> {
        let reported = self
            .script
            .lock()
            .unwrap()
            .pop_front()
            .unwrap_or(Reported::Delivered(Delivery::Applied));
        said(
            &self.timeline,
            format!("report {} {}", name(report), outcome(&reported)),
        );
        Box::pin(async move { reported })
    }
}

fn name(report: &Report) -> &'static str {
    match report {
        Report::Started { .. } => "started",
        Report::Exited { .. } => "exited",
        Report::NotStarted { .. } => "not-started",
    }
}

fn outcome(reported: &Reported) -> &'static str {
    match reported {
        Reported::Delivered(Delivery::Applied) => "applied",
        Reported::Delivered(Delivery::Duplicate) => "duplicate",
        Reported::Delivered(Delivery::Parked) => "parked",
        Reported::Delivered(Delivery::Refused { .. }) => "refused",
        Reported::Delivered(Delivery::NoSuchSession) => "no-such-session",
        Reported::Delivered(Delivery::Unavailable { .. } | Delivery::TimedOut) => "unreached",
        Reported::Held { .. } => "held",
        Reported::Lost { .. } => "lost",
    }
}

fn held() -> Reported {
    Reported::Held {
        why: "no ctxloom daemon could be reached or started".to_string(),
    }
}

/// The run's own lock the frontend took before it asked: says when it is
/// let go.
fn run_lock(timeline: &Timeline) -> HeldLock {
    HeldLock::new(
        "/home/u/.ctxloom/runs/request.lock".to_string(),
        Guard(Arc::clone(timeline), "run".to_string()),
    )
}

fn prepared() -> Prepared {
    let mut command = TurnCommand::new("/bin/claude");
    command.args = vec!["--settings".to_string(), "hooks.json".to_string()];
    command
        .env
        .insert("HOME".to_string(), "/s/home".to_string());
    command.cwd = Some("/work".to_string());
    let mut launch = Launch::new(LaunchKind::Terminal, "claude-code", Stdio::Inherit, command);
    launch.at.harp = "amber-quiet-heron".to_string();
    launch.at.run = "00000000-0000-0000-0000-000000000001".to_string();
    launch.at.incarnation = 1;
    Prepared {
        run: "projects/p/runs/00000000-0000-0000-0000-000000000001".to_string(),
        session: "projects/p/sessions/amber-quiet-heron".to_string(),
        launch,
    }
}

struct Rig {
    timeline: Timeline,
    locks: Locks,
    tty: Tty,
    daemon: Daemon,
    /// Whether the frontend took the run's own lock before it asked.
    run_lock: bool,
}

fn rig(script: Vec<Reported>) -> Rig {
    let timeline = Timeline::default();
    Rig {
        locks: Locks {
            timeline: Arc::clone(&timeline),
            held_by_another: false,
        },
        tty: Tty {
            timeline: Arc::clone(&timeline),
            refuse: false,
            exit: Exit {
                code: Some(0),
                signal: None,
            },
            started: Mutex::new(None),
        },
        daemon: Daemon {
            timeline: Arc::clone(&timeline),
            script: Mutex::new(script.into()),
        },
        run_lock: false,
        timeline,
    }
}

async fn run(rig: &mut Rig) -> super::Started {
    let run_lock = rig.run_lock.then(|| run_lock(&rig.timeline));
    let mut frontend = Frontend {
        locks: &mut rig.locks,
        terminal: &rig.tty,
        reports: &rig.daemon,
        limits: Limits::new(Duration::from_secs(1)),
        run_lock,
    };
    frontend.run(&prepared()).await
}

fn timeline(rig: &Rig) -> Vec<String> {
    rig.timeline.lock().unwrap().clone()
}

#[tokio::test]
async fn test_the_lock_is_taken_before_the_start_and_let_go_after_the_exit_is_reported() {
    let mut rig = rig(Vec::new());
    let done = run(&mut rig).await;
    let ended = done.result.unwrap();
    assert_eq!(ended.harp, "amber-quiet-heron");
    assert_eq!(ended.session, "projects/p/sessions/amber-quiet-heron");
    assert_eq!(ended.engine, "claude-code");
    assert_eq!(ended.code, Some(0));
    assert!(done.notices.is_empty());
    assert_eq!(
        timeline(&rig),
        [
            "lock amber-quiet-heron",
            "spawn",
            "report started applied",
            "exited",
            "report exited applied",
            "unlock amber-quiet-heron",
        ]
    );
}

#[tokio::test]
async fn test_the_launch_is_started_exactly_as_the_daemon_said() {
    let mut rig = rig(Vec::new());
    let _ = run(&mut rig).await;
    let started = rig.tty.started.lock().unwrap().clone().unwrap();
    assert_eq!(started, prepared().launch);
}

#[tokio::test]
async fn test_a_signal_s_end_is_reported_as_the_signal() {
    let mut rig = rig(Vec::new());
    rig.tty.exit = Exit {
        code: None,
        signal: Some(15),
    };
    let done = run(&mut rig).await;
    assert_eq!(done.result.unwrap().code, None);
}

#[tokio::test]
async fn test_a_lock_another_holds_starts_nothing_and_reports_the_launch_failed() {
    let mut rig = rig(Vec::new());
    rig.locks.held_by_another = true;
    let done = run(&mut rig).await;
    assert!(matches!(
        done.result,
        Err(RunError::Lock(LockError::Held(_)))
    ));
    assert_eq!(timeline(&rig), ["report not-started applied"]);
}

#[tokio::test]
async fn test_a_launch_that_does_not_start_is_reported_and_its_lock_let_go() {
    let mut rig = rig(Vec::new());
    rig.tty.refuse = true;
    let done = run(&mut rig).await;
    assert!(
        matches!(&done.result, Err(RunError::NotStarted { detail }) if detail == "no such program")
    );
    assert_eq!(
        timeline(&rig),
        [
            "lock amber-quiet-heron",
            "report not-started applied",
            "unlock amber-quiet-heron",
        ]
    );
}

// U5's start-report race: the run's own lock, taken before the frontend
// asked, is held until its start report is recorded, and no longer.
#[tokio::test]
async fn test_the_runs_own_lock_is_let_go_once_the_start_is_recorded() {
    let mut rig = rig(Vec::new());
    rig.run_lock = true;
    let done = run(&mut rig).await;
    assert!(done.result.is_ok());
    assert_eq!(
        timeline(&rig),
        [
            "lock amber-quiet-heron",
            "spawn",
            "report started applied",
            "unlock run",
            "exited",
            "report exited applied",
            "unlock amber-quiet-heron",
        ]
    );
}

// A start the daemon already had (it recorded it too) is as recorded.
#[tokio::test]
async fn test_a_duplicate_start_lets_the_runs_lock_go() {
    let mut rig = rig(vec![Reported::Delivered(Delivery::Duplicate)]);
    rig.run_lock = true;
    let _ = run(&mut rig).await;
    let line = timeline(&rig);
    let at = |what: &str| line.iter().position(|t| t == what).unwrap();
    assert!(at("unlock run") < at("exited"), "{line:?}");
}

// No daemon takes the start: it is HELD (one attempt, no retry), the run's
// own lock kept, so a daemon started meanwhile reads the run's owner alive;
// the exit's attempt delivers what is held first (the adapter's), and both
// locks go once the exit is reported.
#[tokio::test]
async fn test_a_held_start_keeps_the_runs_lock_until_the_exit_is_reported() {
    let mut rig = rig(vec![held()]);
    rig.run_lock = true;
    let done = run(&mut rig).await;
    assert!(done.result.is_ok());
    assert_eq!(
        timeline(&rig),
        [
            "lock amber-quiet-heron",
            "spawn",
            "report started held",
            "exited",
            "report exited applied",
            "unlock run",
            "unlock amber-quiet-heron",
        ]
    );
    assert_eq!(done.notices.len(), 1, "{:?}", done.notices);
    let said = format!("{:?}", done.notices[0]);
    assert!(said.contains("held the session's start"), "{said}");
}

// A report is ONE attempt: one the daemon left unsettled is held by the
// adapter, never retried here.
#[tokio::test]
async fn test_a_report_is_one_attempt() {
    let mut rig = rig(vec![Reported::Delivered(Delivery::Applied), held()]);
    let done = run(&mut rig).await;
    assert!(done.result.is_ok());
    assert_eq!(
        timeline(&rig)
            .iter()
            .filter(|t| t.starts_with("report exited"))
            .count(),
        1
    );
    let said = format!("{:?}", done.notices);
    assert!(said.contains("held the session's exit"), "{said}");
}

// A report neither recorded nor held is said, and changes nothing of the
// TUI.
#[tokio::test]
async fn test_a_report_neither_recorded_nor_held_is_said() {
    let mut rig = rig(vec![Reported::Lost {
        why: "the hold is full".to_string(),
    }]);
    let done = run(&mut rig).await;
    assert!(done.result.is_ok());
    let said = format!("{:?}", done.notices);
    assert!(said.contains("nor hold it: the hold is full"), "{said}");
}

// A launch refused its session's lock lets the run's own lock go after
// reporting it.
#[tokio::test]
async fn test_a_lock_another_holds_lets_the_runs_lock_go_after_the_report() {
    let mut rig = rig(Vec::new());
    rig.run_lock = true;
    rig.locks.held_by_another = true;
    let _ = run(&mut rig).await;
    assert_eq!(timeline(&rig), ["report not-started applied", "unlock run"]);
}

// A launch that does not start lets the run's own lock go after reporting
// it, before the session's.
#[tokio::test]
async fn test_a_launch_that_does_not_start_lets_both_locks_go_after_the_report() {
    let mut rig = rig(Vec::new());
    rig.run_lock = true;
    rig.tty.refuse = true;
    let _ = run(&mut rig).await;
    assert_eq!(
        timeline(&rig),
        [
            "lock amber-quiet-heron",
            "report not-started applied",
            "unlock run",
            "unlock amber-quiet-heron",
        ]
    );
}

#[tokio::test]
async fn test_a_refused_report_is_said_at_once_and_not_retried() {
    let mut rig = rig(vec![Reported::Delivered(Delivery::Refused {
        detail: "the run has ended".to_string(),
    })]);
    let done = run(&mut rig).await;
    assert!(done.result.is_ok());
    assert_eq!(done.notices.len(), 1);
    let said = format!("{:?}", done.notices[0]);
    assert!(said.contains("the session's start"), "{said}");
    assert!(said.contains("the run has ended"), "{said}");
    assert_eq!(
        timeline(&rig)
            .iter()
            .filter(|t| t.starts_with("report started"))
            .count(),
        1
    );
}

#[tokio::test]
async fn test_a_parked_report_is_said() {
    let mut rig = rig(vec![
        Reported::Delivered(Delivery::Applied),
        Reported::Delivered(Delivery::Parked),
    ]);
    let done = run(&mut rig).await;
    assert_eq!(done.notices.len(), 1);
    assert!(format!("{:?}", done.notices[0]).contains("parked"));
}

// s4-Q14, H3 follow-up: a report the daemon answers no such session is
// final, and said as such.
#[test]
fn a_report_to_a_session_with_no_record_is_said_so() {
    let library_application::Notice::Finding { text, .. } =
        super::undelivered(&Report::Started { pid: 1 }, &Delivery::NoSuchSession)
    else {
        panic!("a finding");
    };
    assert_eq!(
        text,
        "could not tell the daemon the session's start: the daemon has no record of this session"
    );
}
