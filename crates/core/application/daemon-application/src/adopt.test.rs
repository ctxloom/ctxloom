//! A daemon dying under a live TUI (owner, 2026-11-14): a new daemon adopts,
//! at its start, the runs its sweep leaves, and takes over their end path,
//! over the real aggregates (run-launcher's `TestEdge`) and locks a test
//! turns from held to free.

use std::sync::{Arc, Mutex};

use angzarr_embedded::v1::Cover;
use angzarr_embedded::Payload;
use run_launcher::fakes::{uuid, TestEdge};
use run_ports::{LockProbe, RunLocks};
use runs::RunEvent;

use crate::tests::{ended, events, mint, session_events, Deaf, NoHomes, Plans};
use crate::{Daemon, Folds, Place, Ports};

const TUI: &str = "5f0c7a52-3333-5333-8333-000000000003";
/// The run lock the frontend took before it asked (U5's race).
const FRONTEND_LOCK: &str = "/home/u/.ctxloom/runs/0193a8f0-request.lock";
/// The session's checkout, under ctxloom's own worktrees.
const CHECKOUT: &str = "/home/u/.ctxloom/worktrees/calm-otter/tui/dev";

/// The frontend's locks, as a test turns them: the run's own (taken before
/// it asked) and the session's.
#[derive(Clone)]
struct Frontend(Arc<Mutex<(LockProbe, LockProbe)>>);

impl Frontend {
    fn holding(run: LockProbe, session: LockProbe) -> Frontend {
        Frontend(Arc::new(Mutex::new((run, session))))
    }

    fn set(&self, run: LockProbe, session: LockProbe) {
        *self.0.lock().unwrap() = (run, session);
    }
}

impl RunLocks for Frontend {
    fn hold_run(&self, run: &str) -> Result<run_ports::HeldLock, run_ports::LockError> {
        Ok(run_ports::HeldLock::new(format!("/locks/{run}.lock"), ()))
    }

    fn probe(&self, path: &str) -> LockProbe {
        match path == FRONTEND_LOCK {
            true => self.0.lock().unwrap().0,
            false => LockProbe::Unproven,
        }
    }

    fn session(&self, project_dir: &str, harp: &str) -> LockProbe {
        match (project_dir, harp) {
            ("/work", "tui") => self.0.lock().unwrap().1,
            _ => LockProbe::Unproven,
        }
    }
}

/// The project's workspaces: what each was asked to remove, and why it
/// leaves one (work in it), when it does.
#[derive(Default)]
struct Checkouts(Mutex<Vec<String>>, Option<String>);

impl isolation_ports::Workspaces for Checkouts {
    fn prepare(
        &self,
        _: &isolation_ports::Request,
    ) -> Result<isolation_ports::Prepared, isolation_ports::Failure> {
        unreachable!("an adopted run is placed by its first daemon")
    }

    fn session_known(&self, _: &str) -> Result<bool, isolation_ports::Failure> {
        Ok(true)
    }

    fn scratch(
        &self,
        _: Option<&str>,
    ) -> Result<Vec<isolation_ports::Scratch>, isolation_ports::Failure> {
        Ok(Vec::new())
    }

    fn place_files(
        &self,
        _: &str,
        _: &[isolation_ports::PlacedFile],
    ) -> Result<(), isolation_ports::Failure> {
        Ok(())
    }

    fn remove_files(&self, _: &str, _: &[String]) {}

    fn inspect(&self, path: &str) -> isolation_ports::Standing {
        isolation_ports::Standing::Unresolved(path.to_string())
    }

    fn remove(&self, repo_dir: &str, path: &str) -> isolation_ports::Removal {
        self.0.lock().unwrap().push(format!("{repo_dir} {path}"));
        match &self.1 {
            Some(why) => isolation_ports::Removal::Left(why.clone()),
            None => isolation_ports::Removal::Removed,
        }
    }
}

struct AllCheckouts(Arc<Checkouts>);

impl crate::ChildWorkspaces for AllCheckouts {
    fn of(
        &self,
        _: &str,
        _: &str,
        _: Option<&std::collections::BTreeMap<String, String>>,
    ) -> Arc<dyn isolation_ports::Workspaces + Send + Sync> {
        Arc::clone(&self.0) as Arc<dyn isolation_ports::Workspaces + Send + Sync>
    }
}

struct Rig {
    edge: Arc<TestEdge>,
    locks: Frontend,
    checkouts: Arc<Checkouts>,
    said: Arc<Mutex<Vec<String>>>,
}

impl Rig {
    fn new(run: LockProbe, session: LockProbe) -> Rig {
        Rig {
            edge: Arc::new(TestEdge::new()),
            locks: Frontend::holding(run, session),
            checkouts: Arc::new(Checkouts::default()),
            said: Arc::default(),
        }
    }

    /// A daemon started over the rig's store, after the one that prepared
    /// the session was killed.
    fn daemon(&self) -> Daemon<TestEdge> {
        let said = Arc::clone(&self.said);
        Daemon::new(Ports {
            edge: Arc::clone(&self.edge),
            engines: Vec::new(),
            plans: Box::new(Plans::default()),
            sealing: std::sync::Arc::new(run_ports::TestSealing),
            homes: Box::new(NoHomes),
            ears: Box::new(Deaf),
            run_locks: Arc::new(self.locks.clone()),
            place: Place {
                home: "/home/u".to_string(),
                program_dir: None,
                os: "linux".to_string(),
                user_home: None,
            },
            folds: Folds::default(),
            say: Box::new(move |line| said.lock().unwrap().push(line.to_string())),
        })
        .placing(Arc::new(AllCheckouts(Arc::clone(&self.checkouts))))
    }

    fn who(&self) -> runs_application::Identity {
        runs_application::Identity {
            run: runs::run_root(TUI, 1),
            session: uuid(TUI),
            incarnation: 1,
            harp: "tui".to_string(),
            depth: 0,
        }
    }

    /// The prepared session's run, enqueued with the frontend's run lock,
    /// its start recorded when `started`.
    async fn prepared(&self, started: bool) {
        mint(&self.edge, TUI, "tui", "claude-code", None).await;
        runs_application::Enqueuer::new(Arc::clone(&self.edge))
            .enqueue(runs_application::EnqueueRequest {
                session: TUI.to_string(),
                incarnation: 1,
                mode: runs::RunMode::Interactive,
                evidence: runs_application::Evidence::passing("claude-code"),
                launcher_lock: FRONTEND_LOCK.to_string(),
                grant: None,
                spec: None,
            })
            .await
            .unwrap();
        if started {
            self.report_started().await;
        }
    }

    /// The frontend's start report.
    async fn report_started(&self) {
        runs_application::Facts::new(Arc::clone(&self.edge))
            .record(
                &self.who(),
                runs_application::RunnerFact::RunnerStarted(runs::RecordRunnerStarted {
                    runtime: "claude-code".to_string(),
                    workspace: CHECKOUT.to_string(),
                    pid: 4242,
                }),
            )
            .await
            .unwrap();
    }

    /// The frontend's exit report.
    async fn report_exited(&self) {
        runs_application::Facts::new(Arc::clone(&self.edge))
            .record(
                &self.who(),
                runs_application::RunnerFact::RunExited(runs::RecordRunExited {
                    code: 0,
                    signal: 0,
                }),
            )
            .await
            .unwrap();
    }

    /// The run's ends, as its stream holds them.
    async fn run_ends(&self) -> Vec<RunEvent> {
        let run = Cover {
            domain: runs::RUN_DOMAIN.to_string(),
            root: Some(runs::run_root(TUI, 1)),
            edition: None,
        };
        events(&self.edge, &run)
            .await
            .into_iter()
            .map(|any| RunEvent::from_any(&any).unwrap())
            .filter(|event| matches!(event, RunEvent::RunEnded(_)))
            .collect()
    }

    async fn incarnations_ended(&self) -> Vec<(u32, String)> {
        ended(&session_events(&self.edge, TUI).await)
    }

    fn removed(&self) -> Vec<String> {
        self.checkouts.0.lock().unwrap().clone()
    }
}

fn causes(ends: &[RunEvent]) -> Vec<runs::EndCause> {
    ends.iter()
        .filter_map(|event| match event {
            RunEvent::RunEnded(ended) => Some(ended.cause),
            _ => None,
        })
        .collect()
}

// (1) A daemon killed under a live TUI: the next daemon's start finds the
// session's lock held (its frontend alive), sweeps nothing and adopts the
// run; the frontend's exit report then ends the incarnation `runner-exit`
// and lets the checkout go WIP-safe, as the first daemon would have.
#[tokio::test]
async fn a_new_daemon_adopts_a_live_tuis_run_and_its_exit_ends_the_incarnation_and_the_checkout() {
    let rig = Rig::new(LockProbe::Unproven, LockProbe::Held);
    rig.prepared(true).await;
    let mut daemon = rig.daemon();
    let swept = daemon
        .start_up(&rig.edge.store, rig.edge.catalog())
        .await
        .unwrap();
    assert!(swept.is_empty(), "its owner is alive: {swept:?}");
    assert_eq!(daemon.adopted_runs().len(), 1, "adopted");
    // While the TUI runs, nothing is ended.
    assert!(
        !daemon
            .end_the_adopted(&rig.edge.store, rig.edge.catalog())
            .await
    );
    assert!(rig.run_ends().await.is_empty());
    assert!(rig.incarnations_ended().await.is_empty());
    // The frontend reports the exit, then lets the session's lock go.
    rig.report_exited().await;
    rig.locks.set(LockProbe::Unproven, LockProbe::Free);
    assert!(
        daemon
            .end_the_adopted(&rig.edge.store, rig.edge.catalog())
            .await
    );
    assert_eq!(causes(&rig.run_ends().await), [runs::EndCause::RunnerExit]);
    assert_eq!(
        rig.incarnations_ended().await,
        [(1, "runner-exit".to_string())]
    );
    assert_eq!(rig.removed(), [format!("/work {CHECKOUT}")]);
    assert!(daemon.adopted_runs().is_empty(), "its end path is done");
    // Nothing more, round after round.
    assert!(
        !daemon
            .end_the_adopted(&rig.edge.store, rig.edge.catalog())
            .await
    );
    assert_eq!(rig.removed().len(), 1);
    let said = rig.said.lock().unwrap().join("\n");
    assert!(said.contains("adopted"), "{said}");
}

// U5's start-report race, forced: the daemon is killed between the
// frontend's plan request and its start report. The frontend took the run's
// own lock BEFORE it asked, so the next daemon's start reads it held: the
// run is not orphaned but adopted, and once the frontend reports its start
// and its exit the incarnation ends `runner-exit`.
#[tokio::test]
async fn a_daemon_started_between_the_plan_and_the_start_report_reads_the_frontend_alive() {
    let rig = Rig::new(LockProbe::Held, LockProbe::Unproven);
    rig.prepared(false).await;
    let mut daemon = rig.daemon();
    let swept = daemon
        .start_up(&rig.edge.store, rig.edge.catalog())
        .await
        .unwrap();
    assert!(
        swept.is_empty(),
        "its frontend holds the run's lock: {swept:?}"
    );
    assert!(
        !daemon
            .end_the_adopted(&rig.edge.store, rig.edge.catalog())
            .await
    );
    assert!(rig.run_ends().await.is_empty(), "not orphaned");
    // Its frontend starts the TUI (the session's lock taken), reports the
    // start and lets the run's lock go; then the TUI exits.
    rig.locks.set(LockProbe::Held, LockProbe::Held);
    rig.report_started().await;
    rig.locks.set(LockProbe::Unproven, LockProbe::Held);
    assert!(
        !daemon
            .end_the_adopted(&rig.edge.store, rig.edge.catalog())
            .await
    );
    rig.report_exited().await;
    rig.locks.set(LockProbe::Unproven, LockProbe::Free);
    assert!(
        daemon
            .end_the_adopted(&rig.edge.store, rig.edge.catalog())
            .await
    );
    assert_eq!(causes(&rig.run_ends().await), [runs::EndCause::RunnerExit]);
    assert_eq!(
        rig.incarnations_ended().await,
        [(1, "runner-exit".to_string())]
    );
    assert_eq!(rig.removed(), [format!("/work {CHECKOUT}")]);
}

// A frontend gone without reporting (the session's lock free, no exit in
// the run's stream) is a kill, as the first daemon's `Frontended` read it:
// its run's exit is recorded (signal 9), keyed as the frontend's own exit,
// and then its incarnation ends `runner-exit`.
#[tokio::test]
async fn an_adopted_frontend_gone_without_reporting_ends_as_a_kill() {
    let rig = Rig::new(LockProbe::Unproven, LockProbe::Held);
    rig.prepared(true).await;
    let mut daemon = rig.daemon();
    daemon
        .start_up(&rig.edge.store, rig.edge.catalog())
        .await
        .unwrap();
    rig.locks.set(LockProbe::Unproven, LockProbe::Free);
    assert!(
        daemon
            .end_the_adopted(&rig.edge.store, rig.edge.catalog())
            .await
    );
    assert_eq!(causes(&rig.run_ends().await), [runs::EndCause::RunnerExit]);
    assert!(
        daemon
            .end_the_adopted(&rig.edge.store, rig.edge.catalog())
            .await
    );
    assert_eq!(
        rig.incarnations_ended().await,
        [(1, "runner-exit".to_string())]
    );
    // Its late exit report is the same fact: a duplicate, nothing more.
    rig.report_exited().await;
    assert_eq!(causes(&rig.run_ends().await), [runs::EndCause::RunnerExit]);
}

// A frontend that died before its start (the run's own lock left free, no
// start recorded) leaves its run to the sweep: `launch-orphaned`, its
// incarnation `orphaned`.
#[tokio::test]
async fn an_adopted_run_whose_frontend_died_before_its_start_is_orphaned() {
    let rig = Rig::new(LockProbe::Held, LockProbe::Unproven);
    rig.prepared(false).await;
    let mut daemon = rig.daemon();
    daemon
        .start_up(&rig.edge.store, rig.edge.catalog())
        .await
        .unwrap();
    rig.locks.set(LockProbe::Free, LockProbe::Unproven);
    assert!(
        daemon
            .end_the_adopted(&rig.edge.store, rig.edge.catalog())
            .await
    );
    assert_eq!(
        causes(&rig.run_ends().await),
        [runs::EndCause::LaunchOrphaned]
    );
    assert_eq!(
        rig.incarnations_ended().await,
        [(1, "orphaned".to_string())]
    );
    assert!(daemon.adopted_runs().is_empty());
    assert!(rig.removed().is_empty(), "it never had a checkout");
}

// The idle count's sweep never takes an adopted run: its end path is the
// adoption's (a kill, its exit, then `runner-exit`), never `runner-lost`.
#[tokio::test]
async fn the_idle_sweep_leaves_an_adopted_run_to_its_end_path() {
    let rig = Rig::new(LockProbe::Unproven, LockProbe::Held);
    rig.prepared(true).await;
    let mut daemon = rig.daemon();
    daemon
        .start_up(&rig.edge.store, rig.edge.catalog())
        .await
        .unwrap();
    rig.locks.set(LockProbe::Unproven, LockProbe::Free);
    assert_eq!(
        daemon.live_runs(&rig.edge.store, rig.edge.catalog()).await,
        Ok(1)
    );
    assert!(rig.run_ends().await.is_empty(), "not swept as lost");
}

// Adoption is the start's: a start tried again adopts nothing twice.
#[tokio::test]
async fn a_start_tried_again_adopts_a_run_once() {
    let rig = Rig::new(LockProbe::Unproven, LockProbe::Held);
    rig.prepared(true).await;
    let mut daemon = rig.daemon();
    for _ in 0..2 {
        daemon
            .start_up(&rig.edge.store, rig.edge.catalog())
            .await
            .unwrap();
    }
    assert_eq!(daemon.adopted_runs().len(), 1);
    let said = rig.said.lock().unwrap();
    assert_eq!(said.iter().filter(|l| l.contains("adopted")).count(), 1);
}

/// The hold of the TUI's session, as the start-up flush reaches it (OD-59
/// (2)): what it delivers when flushed (the frontend's exit report, held
/// while no daemon served), what it answers, and whom it was asked for.
struct Hold {
    edge: Arc<TestEdge>,
    who: runs_application::Identity,
    /// The start report it holds, delivered first when flushed.
    start: Option<runs::RecordRunnerStarted>,
    /// The exit report it holds, delivered when flushed; none, it delivers
    /// nothing.
    exit: Option<runs::RecordRunExited>,
    answer: crate::HoldFlushed,
    asked: Mutex<Vec<String>>,
}

impl crate::StartHolds for Hold {
    fn flush<'a>(
        &'a self,
        session: &'a str,
    ) -> std::pin::Pin<Box<dyn std::future::Future<Output = crate::HoldFlushed> + Send + 'a>> {
        Box::pin(async move {
            self.asked.lock().unwrap().push(session.to_string());
            if let (TUI, Some(start)) = (session, &self.start) {
                runs_application::Facts::new(Arc::clone(&self.edge))
                    .record(
                        &self.who,
                        runs_application::RunnerFact::RunnerStarted(start.clone()),
                    )
                    .await
                    .unwrap();
            }
            if let (TUI, Some(exit)) = (session, &self.exit) {
                runs_application::Facts::new(Arc::clone(&self.edge))
                    .record(
                        &self.who,
                        runs_application::RunnerFact::RunExited(exit.clone()),
                    )
                    .await
                    .unwrap();
            }
            self.answer.clone()
        })
    }
}

impl Rig {
    fn hold(&self, exit: Option<runs::RecordRunExited>, answer: crate::HoldFlushed) -> Arc<Hold> {
        Arc::new(Hold {
            edge: Arc::clone(&self.edge),
            who: self.who(),
            start: None,
            exit,
            answer,
            asked: Mutex::new(Vec::new()),
        })
    }
}

fn exited_with(code: i32) -> Option<runs::RecordRunExited> {
    Some(runs::RecordRunExited { code, signal: 0 })
}

/// The exit code each of the run's ends carries.
async fn exit_codes(rig: &Rig) -> Vec<Option<i32>> {
    rig.run_ends()
        .await
        .into_iter()
        .filter_map(|event| match event {
            RunEvent::RunEnded(ended) => Some(ended.exit_code),
            _ => None,
        })
        .collect()
}

// OD-59 (2), owner 2026-11-16 ("ALLOW the check"): a TUI frontend that
// exited while no daemon served left its exit report in its session's hold,
// and its locks free. The next daemon's start delivers that session's hold
// BEFORE its sweep judges the run, so the run ends as the report says (its
// exit code, `runner-exit`, through the adoption's end path: the incarnation
// ended and the checkout let go), never `runner-lost`.
#[tokio::test]
async fn the_start_delivers_a_held_exit_report_before_it_judges_the_run() {
    let rig = Rig::new(LockProbe::Unproven, LockProbe::Free);
    rig.prepared(true).await;
    let hold = rig.hold(
        exited_with(3),
        crate::HoldFlushed {
            delivered: 1,
            ..crate::HoldFlushed::default()
        },
    );
    let mut daemon = rig
        .daemon()
        .flushing_at_start(Arc::clone(&hold) as Arc<dyn crate::StartHolds>);
    let swept = daemon
        .start_up(&rig.edge.store, rig.edge.catalog())
        .await
        .unwrap();
    assert!(swept.is_empty(), "its exit was held, not lost: {swept:?}");
    assert_eq!(*hold.asked.lock().unwrap(), [TUI.to_string()]);
    assert!(
        daemon
            .end_the_adopted(&rig.edge.store, rig.edge.catalog())
            .await
    );
    assert_eq!(causes(&rig.run_ends().await), [runs::EndCause::RunnerExit]);
    assert_eq!(
        exit_codes(&rig).await,
        [Some(3)],
        "the frontend's own exit code"
    );
    assert_eq!(
        rig.incarnations_ended().await,
        [(1, "runner-exit".to_string())]
    );
    assert_eq!(rig.removed(), [format!("/work {CHECKOUT}")]);
    let said = rig.said.lock().unwrap().join("\n");
    assert!(said.contains("its hold delivered at the start"), "{said}");
    assert!(!said.contains("runner-lost"), "{said}");
}

// A hold the start cannot deliver whole (a record of a newer ctxloom, one
// that does not read, one set aside) leaves the run to the sweep as before:
// `runner-lost`, and the log says why the hold did not save it.
#[tokio::test]
async fn a_hold_the_start_cannot_deliver_leaves_the_run_lost_and_says_so() {
    let rig = Rig::new(LockProbe::Unproven, LockProbe::Free);
    rig.prepared(true).await;
    let hold = rig.hold(
        None,
        crate::HoldFlushed {
            delivered: 0,
            set_aside: 1,
            left: Some("held by a newer ctxloom (v9.0.0)".to_string()),
        },
    );
    let mut daemon = rig
        .daemon()
        .flushing_at_start(Arc::clone(&hold) as Arc<dyn crate::StartHolds>);
    let swept = daemon
        .start_up(&rig.edge.store, rig.edge.catalog())
        .await
        .unwrap();
    assert_eq!(swept.len(), 1, "{swept:?}");
    assert_eq!(causes(&rig.run_ends().await), [runs::EndCause::RunnerLoss]);
    assert_eq!(*hold.asked.lock().unwrap(), [TUI.to_string()]);
    let said = rig.said.lock().unwrap().join("\n");
    assert!(
        said.contains(&format!(
            "{TUI}: its hold delivered at the start, before its run is judged: 0 delivered; \
             1 set aside; left held: held by a newer ctxloom (v9.0.0)"
        )),
        "{said}"
    );
    assert!(
        said.contains(&format!(
            "{TUI}: ended runner-lost without what its hold held: 1 set aside; left held: \
             held by a newer ctxloom (v9.0.0)"
        )),
        "{said}"
    );
}

// The start reads no hold of a session whose run it does not judge, and a
// hold that held nothing says nothing.
#[tokio::test]
async fn the_start_asks_only_the_judged_runs_holds_and_an_empty_one_says_nothing() {
    let rig = Rig::new(LockProbe::Unproven, LockProbe::Held);
    let hold = rig.hold(None, crate::HoldFlushed::default());
    let mut daemon = rig
        .daemon()
        .flushing_at_start(Arc::clone(&hold) as Arc<dyn crate::StartHolds>);
    daemon
        .start_up(&rig.edge.store, rig.edge.catalog())
        .await
        .unwrap();
    assert!(
        hold.asked.lock().unwrap().is_empty(),
        "no run, no hold read"
    );
    rig.prepared(true).await;
    let mut daemon = rig
        .daemon()
        .flushing_at_start(Arc::clone(&hold) as Arc<dyn crate::StartHolds>);
    daemon
        .start_up(&rig.edge.store, rig.edge.catalog())
        .await
        .unwrap();
    assert_eq!(*hold.asked.lock().unwrap(), [TUI.to_string()]);
    let said = rig.said.lock().unwrap().join("\n");
    assert!(!said.contains("its hold"), "{said}");
}

/// A rig whose checkouts are left (work in them) when `left` says why.
fn rig_leaving(run: LockProbe, session: LockProbe, left: Option<&str>) -> Rig {
    let mut rig = Rig::new(run, session);
    rig.checkouts = Arc::new(Checkouts(Mutex::default(), left.map(str::to_string)));
    rig
}

/// The run owed a runner whose frontend's start report AND exit report were
/// both held while no daemon served (found by OD-59 (2)): its locks free,
/// the next daemon's start flushes both, so the run is started and ended
/// before its sweep judges it, and it is adopted from the list of runs OWED
/// a runner. Its end path ends its incarnation and lets its checkout go,
/// in the session's own project directory (its mint's).
async fn both_reports_held(left: Option<&str>) -> (Rig, Daemon<TestEdge>) {
    let rig = rig_leaving(LockProbe::Free, LockProbe::Free, left);
    rig.prepared(false).await;
    let hold = Arc::new(Hold {
        edge: Arc::clone(&rig.edge),
        who: rig.who(),
        start: Some(runs::RecordRunnerStarted {
            runtime: "claude-code".to_string(),
            workspace: CHECKOUT.to_string(),
            pid: 4242,
        }),
        exit: exited_with(0),
        answer: crate::HoldFlushed {
            delivered: 2,
            ..crate::HoldFlushed::default()
        },
        asked: Mutex::new(Vec::new()),
    });
    let mut daemon = rig
        .daemon()
        .flushing_at_start(Arc::clone(&hold) as Arc<dyn crate::StartHolds>);
    let swept = daemon
        .start_up(&rig.edge.store, rig.edge.catalog())
        .await
        .unwrap();
    assert!(
        swept.is_empty(),
        "its reports were held, not lost: {swept:?}"
    );
    assert_eq!(daemon.adopted_runs().len(), 1, "adopted");
    assert!(
        daemon
            .end_the_adopted(&rig.edge.store, rig.edge.catalog())
            .await
    );
    assert_eq!(causes(&rig.run_ends().await), [runs::EndCause::RunnerExit]);
    assert_eq!(
        rig.incarnations_ended().await,
        [(1, "runner-exit".to_string())]
    );
    (rig, daemon)
}

#[tokio::test]
async fn an_adopted_owed_run_whose_reports_were_both_held_releases_its_checkout_in_its_project() {
    let (rig, daemon) = both_reports_held(None).await;
    assert_eq!(rig.removed(), [format!("/work {CHECKOUT}")]);
    assert!(daemon.adopted_runs().is_empty(), "its end path is done");
    let said = rig.said.lock().unwrap().join("\n");
    assert!(
        said.contains(&format!("{TUI}: its checkout {CHECKOUT} removed")),
        "{said}"
    );
}

#[tokio::test]
async fn an_adopted_owed_run_whose_reports_were_both_held_leaves_a_checkout_holding_work_and_says_so(
) {
    let (rig, _) = both_reports_held(Some("1 uncommitted change")).await;
    assert_eq!(
        rig.removed(),
        [format!("/work {CHECKOUT}")],
        "asked, never forced"
    );
    let said = rig.said.lock().unwrap().join("\n");
    assert!(
        said.contains(&format!(
            "{TUI}: its checkout {CHECKOUT} left: 1 uncommitted change"
        )),
        "{said}"
    );
}
