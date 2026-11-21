//! The daemon's scheduler end to end in the wiring (step 8 U5a): the core
//! the daemon starts, over a real store under a temporary home, with the
//! scheduler's test-only consumer (`scheduler-fixture`: a dummy domain whose
//! deadline arms an alarm and whose close cancels it) registered beside
//! production's. A fake clock the test moves, a wake the test forces, and
//! nothing sleeps: every wait is for what the loop has done, bounded.

use std::sync::{Arc, Mutex};
use std::time::Duration;

use angzarr_embedded::angzarr_store_sqlite::SqliteStore;
use angzarr_embedded::v1::{CommandBook, Cover, Uuid};
use angzarr_embedded::{CommandBookExt as _, Payload as _, Registry, StreamRef, Timestamp};
use run_ports::Edge as _;
use scheduler::{stream_for, SchedulerEvent, Stream, SCHEDULER_DOMAIN};
use scheduler_fixture::{
    ArmsDeadline, Close, DeliverToDummy, DummyAggregate, DummyCommand, DummyEvent, SetDeadline,
    DUMMY_DOMAIN,
};
use scheduler_ports::FakeClock;

use super::{start, Say, Scheduler};
use crate::core_link::AlarmConsumer;
use crate::{InProcessCore, Projectors, SchedulerDues};

type Core = InProcessCore<SqliteStore>;

/// The dummy, as a domain that arms alarms registers itself.
fn dummy(registry: &mut Registry) {
    registry
        .aggregate::<DummyAggregate>(DummyAggregate::default)
        .expect("the dummy registers")
        .policy(ArmsDeadline)
        .expect("its arming registers")
        .policy(DeliverToDummy)
        .expect("its delivery registers");
}

/// The dummy without its delivery: as a process that fired an alarm and
/// died before delivering it left the store.
fn dummy_undelivered(registry: &mut Registry) {
    registry
        .aggregate::<DummyAggregate>(DummyAggregate::default)
        .expect("the dummy registers")
        .policy(ArmsDeadline)
        .expect("its arming registers");
}

const CONSUMERS: &[AlarmConsumer] = &[dummy];

fn at(seconds: i64) -> Timestamp {
    Timestamp { seconds, nanos: 0 }
}

/// A temporary home and a runtime of the daemon's kind.
fn home() -> (Arc<file_system::Rooted>, tokio::runtime::Runtime) {
    let runtime = tokio::runtime::Builder::new_multi_thread()
        .worker_threads(2)
        .enable_all()
        .build()
        .unwrap();
    (Arc::new(file_system::Rooted::temp().unwrap()), runtime)
}

/// The daemon's core over the home's store, every read model registered,
/// `consumers` the domains that arm alarms; and its due model.
async fn core_over(
    fs: &Arc<file_system::Rooted>,
    consumers: &[AlarmConsumer],
) -> (Core, SchedulerDues) {
    let store = ctxloom_store_sqlite::Stores::new(fs.clone(), "/home/u")
        .open()
        .await
        .unwrap();
    let mut kept = Projectors::default();
    let core = InProcessCore::start_consuming(
        store,
        |store| {
            kept = Projectors::filled(store);
            kept.clone()
        },
        consumers,
    )
    .await;
    (
        core,
        kept.due.expect("the daemon's core folds the due alarms"),
    )
}

/// What the scheduler said.
fn log() -> (Arc<Mutex<Vec<String>>>, Say) {
    let lines = Arc::new(Mutex::new(Vec::new()));
    let kept = Arc::clone(&lines);
    let say: Say = Arc::new(move |line: &str| kept.lock().unwrap().push(line.to_string()));
    (lines, say)
}

/// The scheduler over `core`, on `clock`, polling so seldom that only a
/// wake moves it.
fn scheduler(core: &Core, dues: SchedulerDues, clock: &Arc<FakeClock>, say: Say) -> Scheduler {
    start(
        core.share(),
        dues,
        Arc::clone(clock) as Arc<dyn scheduler_ports::Clock>,
        Duration::from_secs(3600),
        say,
    )
}

fn dummy_cover(n: u8) -> Cover {
    Cover {
        domain: DUMMY_DOMAIN.to_string(),
        root: Some(Uuid::from_bytes([n; 16])),
        edition: None,
    }
}

async fn send(core: &Core, n: u8, command: impl Into<DummyCommand>) {
    core.edge()
        .handle(CommandBook::command_of(dummy_cover(n), &command.into()))
        .await
        .unwrap_or_else(|failure| panic!("{failure:?}"));
}

async fn set_deadline(core: &Core, n: u8, seconds: i64) {
    send(
        core,
        n,
        SetDeadline {
            deadline: at(seconds),
        },
    )
    .await;
}

async fn pages(core: &Core, cover: &Cover) -> Vec<angzarr_embedded::Any> {
    let stream = StreamRef::try_from(cover).unwrap();
    angzarr_embedded::EventStore::read(core.store(), stream, 0, None)
        .await
        .unwrap()
        .pages
        .into_iter()
        .filter_map(|page| match page.body {
            Some(angzarr_embedded::v1::event_page::Body::Event(event)) => Some(event),
            _ => None,
        })
        .collect()
}

/// The instants dummy `n` recorded its deadline passed at.
async fn passed(core: &Core, n: u8) -> Vec<i64> {
    pages(core, &dummy_cover(n))
        .await
        .iter()
        .filter_map(|event| match DummyEvent::from_any(event).unwrap() {
            DummyEvent::DeadlinePassed(passed) => Some(passed.at.seconds),
            _ => None,
        })
        .collect()
}

/// How many alarms dummy `n`'s scheduler stream recorded fired.
async fn fired(core: &Core, n: u8) -> usize {
    let target = Stream {
        domain: DUMMY_DOMAIN.to_string(),
        root: vec![n; 16],
        edition: None,
    };
    let cover = Cover {
        domain: SCHEDULER_DOMAIN.to_string(),
        root: Some(stream_for(&target)),
        edition: None,
    };
    pages(core, &cover)
        .await
        .iter()
        .filter(|event| {
            matches!(
                SchedulerEvent::from_any(event),
                Ok(SchedulerEvent::AlarmFired(_))
            )
        })
        .count()
}

// An alarm armed through the daemon's core is fired by its scheduler once
// the clock reaches it, and the target is handed `RecordAlarmReached`, all
// in the loop's one kernel call; the stopped scheduler lets the core go, so
// the store closes.
#[test]
fn an_armed_alarm_fires_at_its_instant_and_the_target_records_it() {
    let (fs, runtime) = home();
    runtime.block_on(async {
        let (core, dues) = core_over(&fs, CONSUMERS).await;
        let clock = Arc::new(FakeClock::at(1_000));
        let (said, say) = log();
        let mut scheduler = scheduler(&core, dues, &clock, say);
        set_deadline(&core, 1, 1_100).await;
        scheduler.ticked_at(at(1_000)).await;
        clock.set(at(1_099));
        scheduler.wake();
        scheduler.ticked_at(at(1_099)).await;
        assert_eq!(passed(&core, 1).await, Vec::<i64>::new(), "not yet");
        clock.set(at(1_100));
        scheduler.wake();
        scheduler.ticked_at(at(1_100)).await;
        assert_eq!(passed(&core, 1).await, vec![1_100]);
        assert_eq!(fired(&core, 1).await, 1);
        let target = Stream {
            domain: DUMMY_DOMAIN.to_string(),
            root: vec![1; 16],
            edition: None,
        };
        let line = format!("alarm fired: {} deadline", scheduler::stream_text(&target));
        assert!(
            said.lock().unwrap().contains(&line),
            "{:?}",
            said.lock().unwrap()
        );
        assert!(scheduler.stop(Duration::from_secs(10)).await, "it ended");
        core.close().await.expect("nothing else holds the store");
    });
}

// An alarm armed in this process wakes the loop as it commits: nothing else
// does here (the poll is an hour, no wake is forced), and it fires.
#[test]
fn an_alarm_armed_in_process_wakes_the_loop() {
    let (fs, runtime) = home();
    runtime.block_on(async {
        let (core, dues) = core_over(&fs, CONSUMERS).await;
        let clock = Arc::new(FakeClock::at(2_000));
        let (_said, say) = log();
        let mut scheduler = scheduler(&core, dues, &clock, say);
        scheduler.ticked_at(at(2_000)).await;
        set_deadline(&core, 1, 1_500).await;
        scheduler.fired(1).await;
        assert_eq!(passed(&core, 1).await, vec![1_500]);
        assert!(scheduler.stop(Duration::from_secs(10)).await);
        core.close().await.unwrap();
    });
}

// A cancelled alarm never fires: the dummy's close cancels it through its
// policy, and a tick past its instant fires nothing.
#[test]
fn a_cancelled_alarm_never_fires() {
    let (fs, runtime) = home();
    runtime.block_on(async {
        let (core, dues) = core_over(&fs, CONSUMERS).await;
        let clock = Arc::new(FakeClock::at(1_000));
        let (said, say) = log();
        let mut scheduler = scheduler(&core, dues, &clock, say);
        set_deadline(&core, 1, 1_100).await;
        send(&core, 1, Close {}).await;
        clock.set(at(1_200));
        scheduler.wake();
        scheduler.ticked_at(at(1_200)).await;
        assert_eq!(passed(&core, 1).await, Vec::<i64>::new());
        assert_eq!(fired(&core, 1).await, 0);
        assert!(
            !said
                .lock()
                .unwrap()
                .iter()
                .any(|l| l.starts_with("alarm fired")),
            "{:?}",
            said.lock().unwrap()
        );
        assert!(scheduler.stop(Duration::from_secs(10)).await);
        core.close().await.unwrap();
    });
}

// A daemon killed with an alarm armed (its loop cut where it waits, its
// core dropped, never closed), then a new one over the same store: the new
// scheduler rebuilds its model from the store, and the alarm, which came due
// while none ran, fires once, late, at the instant armed.
#[test]
fn an_alarm_armed_before_a_kill_fires_in_the_next_daemon() {
    let (fs, runtime) = home();
    runtime.block_on(async {
        let (core, dues) = core_over(&fs, CONSUMERS).await;
        let clock = Arc::new(FakeClock::at(1_000));
        let (_said, say) = log();
        let mut first = scheduler(&core, dues, &clock, say);
        set_deadline(&core, 1, 1_100).await;
        first.wake();
        first.ticked_at(at(1_000)).await;
        first.kill();
        drop(core);
        assert_eq!(fired_in_a_new_daemon(&fs, 1_200).await, vec![1_100]);
        // And once is all: another daemon fires nothing more.
        assert_eq!(fired_in_a_new_daemon(&fs, 1_300).await, vec![1_100]);
    });
}

/// A new daemon's core over `fs`'s store and its scheduler at `now`, ticked
/// once: what dummy 1 then holds passed.
async fn fired_in_a_new_daemon(fs: &Arc<file_system::Rooted>, now: i64) -> Vec<i64> {
    let (core, dues) = core_over(fs, CONSUMERS).await;
    let clock = Arc::new(FakeClock::at(now));
    let (_said, say) = log();
    let mut scheduler = scheduler(&core, dues, &clock, say);
    scheduler.ticked_at(at(now)).await;
    let passed = passed(&core, 1).await;
    assert!(scheduler.stop(Duration::from_secs(10)).await);
    core.close().await.unwrap();
    passed
}

// An alarm fired and not delivered (the process died between the two, or
// the delivery policy is new) is delivered by the next core's resume at its
// start (`CoreStores::at_start`), before any scheduler runs.
#[test]
fn an_alarm_fired_and_not_delivered_is_delivered_at_the_next_start() {
    let (fs, runtime) = home();
    runtime.block_on(async {
        let (core, dues) = core_over(&fs, &[dummy_undelivered]).await;
        let clock = Arc::new(FakeClock::at(1_000));
        let (_said, say) = log();
        let mut scheduler = scheduler(&core, dues, &clock, say);
        set_deadline(&core, 1, 1_000).await;
        scheduler.wake();
        scheduler.fired(1).await;
        assert_eq!(passed(&core, 1).await, Vec::<i64>::new(), "not delivered");
        assert!(scheduler.stop(Duration::from_secs(10)).await);
        core.close().await.unwrap();
        let (core, dues) = core_over(&fs, CONSUMERS).await;
        drop(dues);
        assert_eq!(
            passed(&core, 1).await,
            vec![1_000],
            "the resume delivered it"
        );
        core.close().await.unwrap();
    });
}

// A scheduler still running holds the core: the store does not close while
// it runs, so the daemon stops it first.
#[test]
fn a_running_scheduler_holds_the_store_until_it_is_stopped() {
    let (fs, runtime) = home();
    runtime.block_on(async {
        let (core, dues) = core_over(&fs, CONSUMERS).await;
        let clock = Arc::new(FakeClock::at(1_000));
        let (_said, say) = log();
        let mut scheduler = scheduler(&core, dues, &clock, say);
        scheduler.ticked_at(at(1_000)).await;
        let unclosed = core.close().await;
        assert!(
            matches!(unclosed, Err(crate::Unclosed::StillHeld { .. })),
            "{unclosed:?}"
        );
        assert!(scheduler.stop(Duration::from_secs(10)).await);
    });
}

// The production core registers the scheduler and the due model: an alarm
// armed on it by hand is folded, and the system clock reads now.
#[test]
fn the_production_core_registers_the_scheduler_and_the_system_clock_reads_now() {
    use scheduler_ports::Clock as _;
    let (fs, runtime) = home();
    runtime.block_on(async {
        let store = ctxloom_store_sqlite::Stores::new(fs.clone(), "/home/u")
            .open()
            .await
            .unwrap();
        let mut kept = Projectors::default();
        let core = InProcessCore::start_with(store, |store| {
            kept = Projectors::filled(store);
            kept.clone()
        })
        .await;
        let dues = kept.due.clone().unwrap();
        arm(&core, "a", 5).await;
        let due =
            scheduler_due::ReadsDue::read_due(&dues, |due| due.due_at_or_before(&at(5)).len());
        assert_eq!(due, 1, "the kernel folded the arming inline");
        drop((dues, kept));
        core.close().await.unwrap();
    });
    let now = crate::SystemClock.now();
    let system = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap()
        .as_secs() as i64;
    assert!(
        (now.seconds - system).abs() <= 1,
        "{now:?} against {system}"
    );
}

/// Arm `alarm_id` at `seconds` for dummy 7 on `core`, by hand.
async fn arm(core: &Core, alarm_id: &str, seconds: i64) {
    let target = Stream {
        domain: DUMMY_DOMAIN.to_string(),
        root: vec![7; 16],
        edition: None,
    };
    let cover = Cover {
        domain: SCHEDULER_DOMAIN.to_string(),
        root: Some(stream_for(&target)),
        edition: None,
    };
    let arm = scheduler::ArmAlarm {
        target,
        alarm_id: alarm_id.to_string(),
        at: at(seconds),
    };
    core.edge()
        .handle(CommandBook::command_of(
            cover,
            &scheduler::SchedulerCommand::from(arm),
        ))
        .await
        .unwrap_or_else(|failure| panic!("{failure:?}"));
}

// A stream another process wrote first (a CLI command's core, which folds
// no due model): the daemon's kernel, handed only its own commit, reads the
// pages before it from the store, so the model holds both alarms at once,
// before any catch-up.
#[test]
fn an_arming_after_another_processs_reads_what_that_one_wrote() {
    let (fs, runtime) = home();
    runtime.block_on(async {
        let store = ctxloom_store_sqlite::Stores::new(fs.clone(), "/home/u")
            .open()
            .await
            .unwrap();
        let other = InProcessCore::start(store).await;
        let (core, dues) = core_over(&fs, CONSUMERS).await;
        arm(&other, "first", 5).await;
        arm(&core, "second", 6).await;
        let ids = scheduler_due::ReadsDue::read_due(&dues, |due| {
            due.due_at_or_before(&at(10))
                .into_iter()
                .map(|alarm| alarm.alarm_id)
                .collect::<Vec<_>>()
        });
        assert_eq!(ids, vec!["first".to_string(), "second".to_string()]);
        drop(dues);
        other.close().await.unwrap();
        core.close().await.unwrap();
    });
}

// The due model answers no resource message: nothing asks for one.
#[test]
fn the_due_model_has_no_view() {
    use held_projection::Model as _;
    assert_eq!(super::DueFold::default().view(&Cover::default()), None);
}

// The approvals' expiry (step 8 U5, D11, D12): production's consumers, an
// ask recorded through the core, the scheduler on a fake clock.
mod approvals {
    use std::sync::Arc;
    use std::time::Duration;

    use angzarr_embedded::v1::{CommandBook, Cover, Uuid};
    use angzarr_embedded::{CommandBookExt as _, Payload as _, StreamRef, Timestamp};
    use approvals::{
        claude_options, fact_book, AnswerApproval, ApprovalCommand, ApprovalEvent,
        RecordApprovalAsked, SessionRef, APPROVALS_DOMAIN,
    };
    use run_ports::Edge as _;
    use scheduler_ports::FakeClock;

    use super::{at, core_over, home, log, scheduler, Core};
    use crate::core_link::ALARM_CONSUMERS;

    fn cover(n: u8) -> Cover {
        Cover {
            domain: APPROVALS_DOMAIN.to_string(),
            root: Some(Uuid::from_bytes([n; 16])),
            edition: None,
        }
    }

    pub(super) fn asked(n: u8, timeout_seconds: u32) -> RecordApprovalAsked {
        let session = |id: &str| SessionRef {
            project_id: "p-1".to_string(),
            session: id.to_string(),
        };
        RecordApprovalAsked {
            ask_id: format!("ask-tidy-wren{n}"),
            child: session("child"),
            run: "r-1".to_string(),
            engine: "claude-code".to_string(),
            root: session("root"),
            tool_name: "Bash".to_string(),
            input_excerpt: r#"{"command":"touch hi.txt"}"#.to_string(),
            input_bytes: 26,
            input_sha256: "abc".to_string(),
            timeout_seconds,
            engine_request_id: format!("toolu_{n}"),
            options: claude_options("Bash", r#"{"command":"touch hi.txt"}"#),
            name: format!("projects/amber/approvals/ask-tidy-wren{n}"),
            child_name: "projects/amber/sessions/brisk".to_string(),
            root_name: "projects/amber/sessions/root".to_string(),
            agent: "default".to_string(),
        }
    }

    /// Ask `n`, on `core`, with `timeout_seconds`; when its time runs out.
    pub(super) async fn ask(core: &Core, n: u8, timeout_seconds: u32) -> Timestamp {
        core.edge()
            .handle(fact_book(
                cover(n),
                &asked(n, timeout_seconds),
                &format!("ask:{n}"),
            ))
            .await
            .unwrap_or_else(|failure| panic!("{failure:?}"));
        let cover = cover(n);
        let stream = StreamRef::try_from(&cover).unwrap();
        let book = angzarr_embedded::EventStore::read(core.store(), stream, 0, None)
            .await
            .unwrap();
        let recorded = book.pages[0].meta.as_ref().unwrap().recorded_at.unwrap();
        approvals::expiry_at(recorded, timeout_seconds)
    }

    pub(super) async fn answer(core: &Core, n: u8) {
        let command: ApprovalCommand = AnswerApproval {
            ask_id: format!("ask-tidy-wren{n}"),
            option: "allow-once".to_string(),
            message: String::new(),
        }
        .into();
        core.edge()
            .handle(CommandBook::command_of(cover(n), &command))
            .await
            .unwrap_or_else(|failure| panic!("{failure:?}"));
    }

    /// How many times ask `n` recorded it expired.
    async fn expired(core: &Core, n: u8) -> usize {
        let cover = cover(n);
        let stream = StreamRef::try_from(&cover).unwrap();
        angzarr_embedded::EventStore::read(core.store(), stream, 0, None)
            .await
            .unwrap()
            .pages
            .into_iter()
            .filter_map(|page| match page.body {
                Some(angzarr_embedded::v1::event_page::Body::Event(event)) => Some(event),
                _ => None,
            })
            .filter(|event| {
                matches!(
                    ApprovalEvent::from_any(event),
                    Ok(ApprovalEvent::ApprovalExpired(_))
                )
            })
            .count()
    }

    // An ask nobody answers is expired by its alarm at its time, once, and
    // not a moment before.
    #[test]
    fn an_ask_nobody_answers_is_expired_by_its_alarm_at_its_time() {
        let (fs, runtime) = home();
        runtime.block_on(async {
            let (core, dues) = core_over(&fs, ALARM_CONSUMERS).await;
            let expires = ask(&core, 1, 300).await;
            let before = Timestamp {
                seconds: expires.seconds - 1,
                nanos: expires.nanos,
            };
            let clock = Arc::new(FakeClock::at(before.seconds));
            clock.set(before);
            let (said, say) = log();
            let mut scheduler = scheduler(&core, dues, &clock, say);
            scheduler.ticked_at(before).await;
            assert_eq!(expired(&core, 1).await, 0, "not yet");
            clock.set(expires);
            scheduler.wake();
            scheduler.fired(1).await;
            assert_eq!(expired(&core, 1).await, 1);
            assert!(
                said.lock()
                    .unwrap()
                    .iter()
                    .any(|line| line.starts_with("alarm fired: ")
                        && line.contains("approval-expiry:")),
                "{:?}",
                said.lock().unwrap()
            );
            // Its time past again changes nothing.
            clock.set(at(expires.seconds + 60));
            scheduler.wake();
            scheduler.ticked_at(at(expires.seconds + 60)).await;
            assert_eq!(expired(&core, 1).await, 1);
            assert!(scheduler.stop(Duration::from_secs(10)).await);
            core.close().await.unwrap();
        });
    }

    // An answered ask's alarm is cancelled: its time passing expires
    // nothing, and no alarm fires.
    #[test]
    fn an_answered_ask_is_never_expired() {
        let (fs, runtime) = home();
        runtime.block_on(async {
            let (core, dues) = core_over(&fs, ALARM_CONSUMERS).await;
            let expires = ask(&core, 2, 300).await;
            answer(&core, 2).await;
            let clock = Arc::new(FakeClock::at(expires.seconds + 1));
            let (said, say) = log();
            let mut scheduler = scheduler(&core, dues, &clock, say);
            scheduler.ticked_at(at(expires.seconds + 1)).await;
            assert_eq!(expired(&core, 2).await, 0);
            assert!(
                !said
                    .lock()
                    .unwrap()
                    .iter()
                    .any(|line| line.starts_with("alarm fired")),
                "{:?}",
                said.lock().unwrap()
            );
            assert!(scheduler.stop(Duration::from_secs(10)).await);
            core.close().await.unwrap();
        });
    }
}
