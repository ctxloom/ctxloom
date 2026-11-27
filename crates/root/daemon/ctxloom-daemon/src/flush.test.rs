//! A command's flush of its home's holds on the way in (s4-Q14, H3), over
//! the real hold (fact-spool on a temporary file system, its flock) and the
//! in-process kernel. A race is forced, never waited for: the second
//! command runs INSIDE the first one's delivery.

use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use angzarr_embedded::testing::MemoryStore;
use angzarr_embedded::v1::{CommandBook, Cover, Uuid};
use angzarr_embedded::CommandBookExt;
use run_ports::{Delivery, Edge, FactSink, Held, HeldEntry, HeldFact, HeldFacts};
use runs_application::{observe, Left};
use surface_ports::HookEvent;

use crate::InProcessCore;
use ctxloom_compose::capture::{Deadline, EdgeSink};
use ctxloom_compose::daemon::flush::*;
use runs_application::SessionFlush;

type TheEdge = embedded_edge::EmbeddedEdge<crate::CoreStores<MemoryStore>>;

const OWN: &str = "v0.2.0-aaaaaaa";
const OLDER: &str = "v0.1.0-bbbbbbb";

/// Two sessions of the home, by resource name.
const ONE: &str = "projects/amber-quiet-heron/sessions/amber-swift-owl";
const TWO: &str = "projects/calm-otter/sessions/brisk-copper-moth";

fn id_of(name: &str) -> Uuid {
    Uuid::from_bytes(*sessions::SessionName::parse(name).unwrap().id().as_bytes())
}

fn root_of(name: &str) -> uuid::Uuid {
    sessions::SessionName::parse(name).unwrap().id()
}

fn cover_of(name: &str) -> Cover {
    Cover {
        domain: sessions::SESSION_DOMAIN.to_string(),
        root: Some(id_of(name)),
        edition: None,
    }
}

/// A kernel with both sessions minted.
async fn kernel() -> Arc<TheEdge> {
    let core = InProcessCore::start(MemoryStore::default()).await;
    let edge = core.edge();
    for name in [ONE, TWO] {
        let parsed = sessions::SessionName::parse(name).unwrap();
        let mint = sessions::MintSession {
            harp: parsed.harp().to_string(),
            project_id: "calm-otter".to_string(),
            project_dir: "/w".to_string(),
            output_dir: "/o".to_string(),
            engine: "claude-code".to_string(),
            origin: sessions::Origin::Session,
            started_at: None,
            depth: 0,
            owner: None,
            workspace: None,
        };
        edge.handle(CommandBook::command_of(
            cover_of(name),
            &sessions::SessionCommand::from(mint),
        ))
        .await
        .unwrap();
    }
    edge
}

/// A home's holds on a temporary file system.
fn spool() -> fact_spool::Spool {
    let fs = Arc::new(file_system::Rooted::temp().unwrap());
    let home = std::path::PathBuf::from(os::host().flavor().from_top("/home/a"));
    fact_spool::Spool::at_home(fs, &home)
}

fn prompt(id: &str) -> HookEvent {
    HookEvent {
        event: "turn_start".to_string(),
        prompt_id: id.to_string(),
        prompt: format!("prompt {id}"),
        ..HookEvent::default()
    }
}

fn turn_end(id: &str) -> HookEvent {
    HookEvent {
        event: "turn_end".to_string(),
        prompt_id: id.to_string(),
        ..HookEvent::default()
    }
}

/// `event`'s fact, held for `name` by a hook of `OWN`.
fn hold(held: &fact_spool::Spool, name: &str, event: &HookEvent) -> Held {
    let observation = &observe(event)[0];
    let fact = HeldFact::of_book(
        OWN,
        name,
        observation.external_id(&id_of(name)),
        &observation.book(&id_of(name)),
        None,
    );
    held.hold(&root_of(name), fact).unwrap()
}

fn id(name: &str, event: &HookEvent) -> String {
    observe(event)[0].external_id(&id_of(name))
}

/// The external ids of the facts `name`'s stream recorded, in order.
async fn recorded(edge: &TheEdge, name: &str) -> Vec<String> {
    edge.events(&cover_of(name), 0)
        .await
        .unwrap()
        .iter()
        .filter_map(|page| match page.meta.as_ref()?.causation.as_ref()? {
            angzarr_embedded::v1::event_meta::Causation::Fact(fact) => {
                Some(fact.origin.as_ref()?.external_id.clone())
            }
            _ => None,
        })
        .collect()
}

/// What `name`'s hold holds, by external id, in order.
fn held_ids(held: &fact_spool::Spool, name: &str) -> Vec<String> {
    let batch = held.take(&root_of(name)).unwrap().expect("no other taker");
    batch
        .entries()
        .iter()
        .map(|entry| match entry {
            HeldEntry::Fact(fact) => fact.external_id.clone(),
            HeldEntry::Gap(gap) => format!("gap:{}", gap.external_id),
        })
        .collect()
}

/// The in-process sink, running `during` inside its first delivery.
struct Racing {
    sink: EdgeSink<TheEdge>,
    during: Mutex<Option<Box<dyn FnOnce() + Send>>>,
}

impl Racing {
    fn over(edge: &Arc<TheEdge>) -> Racing {
        Racing {
            sink: EdgeSink::new(Arc::clone(edge)),
            during: Mutex::new(None),
        }
    }
}

impl FactSink for Racing {
    fn deliver(&self, book: CommandBook, within: Duration) -> Delivery {
        let during = self.during.lock().unwrap().take();
        if let Some(during) = during {
            during();
        }
        self.sink.deliver(book, within)
    }
}

fn soon() -> Deadline {
    Deadline(Instant::now() + FLUSH_BOUND)
}

fn lefts(flushed: &Flushed) -> Vec<(uuid::Uuid, Left)> {
    let Flushed::Done(done) = flushed else {
        panic!("a flush: {flushed:?}")
    };
    done.iter().map(|d| (d.root, d.left.clone())).collect()
}

/// The two sessions' roots in the order the hold lists them.
fn sorted_roots() -> Vec<uuid::Uuid> {
    let mut roots = vec![root_of(ONE), root_of(TWO)];
    roots.sort();
    roots
}

// A command on its way in delivers what every session of its home holds,
// each in its own order, and leaves no hold behind.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn a_command_delivers_every_sessions_hold_in_held_order() {
    let edge = kernel().await;
    let held = spool();
    hold(&held, ONE, &prompt("p1"));
    hold(&held, ONE, &turn_end("p1"));
    hold(&held, TWO, &prompt("q1"));
    let (h, e) = (held.clone(), Arc::clone(&edge));
    let flushed =
        tokio::task::spawn_blocking(move || flush_with(&h, &soon(), OWN, OWN, &Racing::over(&e)))
            .await
            .unwrap();
    assert_eq!(
        lefts(&flushed),
        sorted_roots()
            .into_iter()
            .map(|r| (r, Left::Nothing))
            .collect::<Vec<_>>()
    );
    assert_eq!(
        recorded(&edge, ONE).await,
        vec![id(ONE, &prompt("p1")), id(ONE, &turn_end("p1"))]
    );
    assert_eq!(recorded(&edge, TWO).await, vec![id(TWO, &prompt("q1"))]);
    assert!(held.sessions().unwrap().is_empty(), "no hold is left");
}

// A fact the hold dropped past its bound is delivered as the gap fact, in
// its place: after what was held before it.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn a_gap_is_delivered_in_its_place() {
    let edge = kernel().await;
    let held = spool();
    assert_eq!(hold(&held, ONE, &prompt("p1")), Held::Kept);
    // At its bound now: the next fact is dropped and a gap kept for it.
    let full = held.clone().bounded(1, 256 << 20);
    assert!(matches!(
        hold(&full, ONE, &turn_end("p1")),
        Held::Full { gap: true, .. }
    ));
    assert_eq!(
        held_ids(&held, ONE),
        vec![
            id(ONE, &prompt("p1")),
            format!("gap:{}", id(ONE, &turn_end("p1")))
        ]
    );
    let (h, e) = (held.clone(), Arc::clone(&edge));
    tokio::task::spawn_blocking(move || flush_with(&h, &soon(), OWN, OWN, &Racing::over(&e)))
        .await
        .unwrap();
    assert_eq!(
        recorded(&edge, ONE).await,
        vec![
            id(ONE, &prompt("p1")),
            format!("hook:gap:{}", id(ONE, &turn_end("p1")))
        ]
    );
}

// A command older than the serving daemon delivers nothing, and leaves
// every hold as it was.
#[test]
fn an_older_command_delivers_nothing() {
    let held = spool();
    hold(&held, ONE, &prompt("p1"));
    struct Never;
    impl FactSink for Never {
        fn deliver(&self, _: CommandBook, _: Duration) -> Delivery {
            panic!("nothing is delivered")
        }
    }
    assert_eq!(
        flush_with(&held, &soon(), OLDER, OWN, &Never),
        Flushed::Older {
            daemon: OWN.to_string()
        }
    );
    assert_eq!(held_ids(&held, ONE), vec![id(ONE, &prompt("p1"))]);
}

// A flush whose bound is already spent delivers nothing more and leaves
// every hold as it was, so a command never stalls on it.
#[test]
fn a_spent_bound_delivers_nothing() {
    let held = spool();
    hold(&held, ONE, &prompt("p1"));
    struct Never;
    impl FactSink for Never {
        fn deliver(&self, _: CommandBook, _: Duration) -> Delivery {
            panic!("nothing is delivered")
        }
    }
    let spent = Deadline(Instant::now());
    let flushed = flush_with(&held, &spent, OWN, OWN, &Never);
    assert_eq!(lefts(&flushed), vec![(root_of(ONE), Left::OutOfTime)]);
    assert_eq!(held_ids(&held, ONE), vec![id(ONE, &prompt("p1"))]);
}

/// A clock that only the sink's deliveries move: `remaining` starts at the
/// bound and each delivery spends its cost, as a slow host's would.
struct Spent(Arc<Mutex<Duration>>);

impl runs_application::HookClock for Spent {
    fn remaining(&self) -> Duration {
        *self.0.lock().unwrap()
    }

    fn now(&self) -> Option<daemon_proto::google::protobuf::Timestamp> {
        None
    }
}

struct Costing(Arc<Mutex<Duration>>, Duration);

impl FactSink for Costing {
    fn deliver(&self, _: CommandBook, _: Duration) -> Delivery {
        let mut left = self.0.lock().unwrap();
        *left = left.saturating_sub(self.1);
        Delivery::Applied
    }
}

// The bound a command's flush gets is wide enough for a full hold of ten
// facts on a slow host: each delivery paying 450 ms (a thread and a TLS
// handshake each; Linux pays about 55 ms) is 4.5 s, so every fact is
// delivered and nothing stays held. A bound of 2 s leaves facts held.
#[test]
fn the_flush_bound_holds_ten_facts_at_a_slow_hosts_cost() {
    let held = spool();
    for n in 0..10 {
        hold(&held, ONE, &prompt(&format!("p{n}")));
    }
    let left = Arc::new(Mutex::new(FLUSH_BOUND));
    let sink = Costing(Arc::clone(&left), Duration::from_millis(450));
    let flushed = flush_with(&held, &Spent(left), OWN, OWN, &sink);
    assert_eq!(lefts(&flushed), vec![(root_of(ONE), Left::Nothing)]);
    assert_eq!(held_ids(&held, ONE), Vec::<String>::new());
}

// The documented figure: a change to the bound is a change to this.
#[test]
fn the_flush_bound_is_five_seconds() {
    assert_eq!(FLUSH_BOUND, Duration::from_secs(5));
}

// Two commands at once over the real hold and its flock, forced: the
// second runs INSIDE the first one's first delivery. It cannot take the
// hold the first is delivering (its flush lock), so it skips it and never
// waits; it delivers the other session's. Every fact is recorded once.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn two_commands_at_once_one_flushes_a_hold_the_other_skips_it() {
    let edge = kernel().await;
    let held = spool();
    let roots = sorted_roots();
    let name = |root: &uuid::Uuid| if *root == root_of(ONE) { ONE } else { TWO };
    let (first, second) = (name(&roots[0]), name(&roots[1]));
    hold(&held, first, &prompt("p1"));
    hold(&held, second, &prompt("q1"));
    let (told, heard) = std::sync::mpsc::channel();
    let sink = Racing::over(&edge);
    {
        let (held, edge) = (held.clone(), Arc::clone(&edge));
        *sink.during.lock().unwrap() = Some(Box::new(move || {
            let started = Instant::now();
            let flushed = flush_with(&held, &soon(), OWN, OWN, &Racing::over(&edge));
            told.send((flushed, started.elapsed())).unwrap();
        }));
    }
    let h = held.clone();
    let flushed = tokio::task::spawn_blocking(move || flush_with(&h, &soon(), OWN, OWN, &sink))
        .await
        .unwrap();
    let (other, took) = heard.recv().unwrap();
    assert_eq!(
        lefts(&other),
        vec![(roots[0], Left::Taken), (roots[1], Left::Nothing)],
        "the second skips the hold being taken, and flushes the other"
    );
    assert!(took < FLUSH_BOUND, "it never waited: {took:?}");
    assert_eq!(
        lefts(&flushed),
        vec![(roots[0], Left::Nothing), (roots[1], Left::Nothing)]
    );
    assert_eq!(recorded(&edge, first).await, vec![id(first, &prompt("p1"))]);
    assert_eq!(
        recorded(&edge, second).await,
        vec![id(second, &prompt("q1"))]
    );
    assert!(held.sessions().unwrap().is_empty());
}

// The way in is once a process: the first ask is the first, never again.
#[test]
fn the_way_in_is_once() {
    let way_in = WayIn::new();
    assert!(way_in.first());
    assert!(!way_in.first());
    assert!(!way_in.first());
}

// With no daemon serving the home, the frontend's flush (U5's call) starts
// none and delivers nothing.
#[test]
fn with_no_daemon_serving_nothing_is_flushed() {
    let temp = crate::fixtures::root();
    let home = temp.root().join("home");
    crate::fixtures::mkdirs(&home);
    let base = ctxloom_compose::fixtures::host_snapshot();
    let env = process_env::EnvSnapshot::from_parts(
        base.flavor(),
        Ok(home.clone()),
        base.temp_dir().to_path_buf(),
        vec![
            (
                crate::fixtures::home_var().into(),
                home.clone().into_os_string(),
            ),
            (
                run_ports::DAEMON_ROOT_VARIABLE.into(),
                home.into_os_string(),
            ),
        ],
    );
    let wiring = crate::Wiring::new(Arc::new(env)).unwrap();
    assert_eq!(flush_holds(&wiring, OWN), Flushed::NotServing);
}

// ------------------------------------------- H3: no silent drop on flush

/// A sink that answers every delivery `answer`.
struct Answering(Delivery);

impl FactSink for Answering {
    fn deliver(&self, _: CommandBook, _: Duration) -> Delivery {
        self.0.clone()
    }
}

/// Each hold the home has, with how many set-aside parts and facts it has.
fn held_files(held: &fact_spool::Spool) -> Vec<(uuid::Uuid, u32, u64)> {
    held.survey()
        .unwrap()
        .into_iter()
        .map(|h| (h.root, h.set_aside, h.facts))
        .collect()
}

// The daemon finally refuses what a session holds: each record goes to the
// hold's set-aside part, not into the void, and the flush says it ONCE for
// the session, naming its harp, how many and why.
#[test]
fn what_the_daemon_refuses_is_set_aside_and_said_once_a_session() {
    let held = spool();
    hold(&held, ONE, &prompt("p1"));
    hold(&held, ONE, &turn_end("p1"));
    let refused = Answering(Delivery::Refused {
        detail: "the domain sessions is not served".to_string(),
    });
    let flushed = flush_with(&held, &soon(), OWN, OWN, &refused);
    let said = notices(&flushed);
    assert_eq!(
        said,
        vec![HeldSetAside {
            session: "amber-swift-owl".to_string(),
            count: 2,
            why: "the daemon refused it: the domain sessions is not served".to_string(),
        }]
    );
    assert!(held.is_empty(&root_of(ONE)).unwrap(), "nothing held");
    assert_eq!(
        held_files(&held),
        vec![(root_of(ONE), 1, 0)],
        "set aside beside the hold, in one part"
    );
}

// A parked record and one that is no fact of its hold's session are set
// aside too, each reason said once, in held order.
#[test]
fn a_parked_and_a_foreign_record_are_set_aside_each_reason_said_once() {
    let held = spool();
    hold(&held, ONE, &prompt("p1"));
    hold(&held, ONE, &prompt("p2"));
    // TWO's fact, in ONE's hold.
    let observation = &observe(&prompt("q1"))[0];
    held.hold(
        &root_of(ONE),
        HeldFact::of_book(
            OWN,
            ONE,
            observation.external_id(&id_of(TWO)),
            &observation.book(&id_of(TWO)),
            None,
        ),
    )
    .unwrap();
    let flushed = flush_with(&held, &soon(), OWN, OWN, &Answering(Delivery::Parked));
    assert_eq!(
        notices(&flushed),
        vec![HeldSetAside {
            session: "amber-swift-owl".to_string(),
            count: 3,
            why: "the daemon parked it, as a fact it can never record; \
                  it is not a fact of this session's stream"
                .to_string(),
        }]
    );
    assert!(held.is_empty(&root_of(ONE)).unwrap());
}

// A flush that delivered everything says nothing; one that did not run
// says nothing either.
#[test]
fn a_flush_that_set_nothing_aside_says_nothing() {
    let held = spool();
    hold(&held, ONE, &prompt("p1"));
    let flushed = flush_with(&held, &soon(), OWN, OWN, &Answering(Delivery::Applied));
    assert!(notices(&flushed).is_empty());
    assert!(notices(&Flushed::NotServing).is_empty());
    assert!(held_files(&held).is_empty());
}

// What the way in set aside is said once: taken, it is gone.
#[test]
fn what_is_to_be_said_is_taken_once() {
    let notice = HeldSetAside {
        session: "s".to_string(),
        count: 1,
        why: "w".to_string(),
    };
    to_say(vec![notice.clone()]);
    assert_eq!(take_said(), vec![notice]);
    assert!(take_said().is_empty());
}

// s4-Q14, H3 follow-up: a hold of a session the daemon has no record of
// (never minted, or destroyed) is set aside, never "applied" into nothing,
// and said like the other reasons.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn a_hold_of_a_session_the_daemon_has_no_record_of_is_set_aside_and_said() {
    const NOBODY: &str = "projects/amber-quiet-heron/sessions/never-minted-wren";
    let edge = kernel().await;
    let held = spool();
    hold(&held, NOBODY, &prompt("p1"));
    let (h, e) = (held.clone(), Arc::clone(&edge));
    let flushed =
        tokio::task::spawn_blocking(move || flush_with(&h, &soon(), OWN, OWN, &Racing::over(&e)))
            .await
            .unwrap();
    assert_eq!(
        notices(&flushed),
        vec![HeldSetAside {
            session: "never-minted-wren".to_string(),
            count: 1,
            why: "the daemon has no record of this session".to_string(),
        }]
    );
    assert_eq!(held_files(&held), vec![(root_of(NOBODY), 1, 0)]);
    assert!(recorded(&edge, NOBODY).await.is_empty());
}

// ------------------------------------------- the start's flush (OD-59 (2))

/// The daemon's start-up flush over a home on a temporary file system, and
/// that home's holds, for a daemon of `stamp` behind `edge`.
fn start_flush(edge: &Arc<TheEdge>, stamp: &str) -> (StartFlush<TheEdge>, fact_spool::Spool) {
    let fs: Arc<dyn file_system::Fs> = Arc::new(file_system::Rooted::temp().unwrap());
    let home = std::path::PathBuf::from(os::host().flavor().from_top("/home/a"));
    let held = fact_spool::Spool::at_home(Arc::clone(&fs), &home);
    let flush = StartFlush {
        fs,
        home,
        edge: Arc::clone(edge),
        stamp: stamp.to_string(),
    };
    (flush, held)
}

// The named exception's flush delivers ONE session's hold (the judged
// run's), in held order, through the daemon's own edge, and leaves every
// other session's hold where it is.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn the_starts_flush_delivers_the_judged_sessions_hold_alone() {
    use daemon_application::StartHolds;
    let edge = kernel().await;
    let (flush, held) = start_flush(&edge, OWN);
    hold(&held, ONE, &prompt("p1"));
    hold(&held, ONE, &turn_end("p1"));
    hold(&held, TWO, &prompt("q1"));
    let done = flush.flush(&root_of(ONE).hyphenated().to_string()).await;
    assert_eq!(
        done,
        daemon_application::HoldFlushed {
            delivered: 2,
            set_aside: 0,
            left: None,
        }
    );
    assert_eq!(
        recorded(&edge, ONE).await,
        [id(ONE, &prompt("p1")), id(ONE, &turn_end("p1"))]
    );
    assert!(
        recorded(&edge, TWO).await.is_empty(),
        "not judged, not read"
    );
    assert_eq!(held_ids(&held, TWO), [id(TWO, &prompt("q1"))]);
}

// A record this daemon is not up to date for (held by a newer ctxloom)
// stays held, and the flush says so: the sweep then judges the run as
// before, `runner-lost`.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn the_starts_flush_leaves_a_newer_ctxloom_s_record_and_says_so() {
    use daemon_application::StartHolds;
    let edge = kernel().await;
    let (flush, held) = start_flush(&edge, OLDER);
    hold(&held, ONE, &prompt("p1"));
    let done = flush.flush(&root_of(ONE).hyphenated().to_string()).await;
    assert_eq!(done.delivered, 0);
    assert_eq!(
        done.left.as_deref(),
        Some("records held by a newer ctxloom")
    );
    assert!(!done.whole());
    assert!(recorded(&edge, ONE).await.is_empty());
    assert_eq!(held_ids(&held, ONE), [id(ONE, &prompt("p1"))]);
}

// A session the hold has nothing of says nothing, and one that is no
// session id is left, said.
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn the_starts_flush_of_an_empty_hold_is_whole_and_of_no_id_is_left() {
    use daemon_application::StartHolds;
    let edge = kernel().await;
    let (flush, _held) = start_flush(&edge, OWN);
    let done = flush.flush(&root_of(ONE).hyphenated().to_string()).await;
    assert_eq!(done, daemon_application::HoldFlushed::default());
    assert!(done.whole());
    let none = flush.flush("not-a-session").await;
    assert_eq!(none.left.as_deref(), Some("not-a-session is no session id"));
}

// Each reason a flush left something is said; a final refusal is counted
// set aside, apart from what was delivered.
#[test]
fn a_start_flush_says_why_it_left_what_it_left() {
    let flushed = |left: Left| SessionFlush {
        root: root_of(ONE),
        session: None,
        flushed: vec![
            runs_application::Kept {
                external_id: "a".to_string(),
                outcome: runs_application::Outcome::Delivered(Delivery::Applied),
            },
            runs_application::Kept {
                external_id: "b".to_string(),
                outcome: runs_application::Outcome::Delivered(Delivery::Parked),
            },
        ],
        left,
    };
    let said = |left: Left, empty| held_flushed(&flushed(left), empty);
    let whole = said(Left::Nothing, Ok(true));
    assert_eq!((whole.delivered, whole.set_aside, whole.left), (1, 1, None));
    let gone = run_ports::HoldError::Format {
        path: "/h".to_string(),
    };
    for (left, empty, why) in [
        (
            Left::Nothing,
            Ok(false),
            "records held by a newer ctxloom".to_string(),
        ),
        (Left::Nothing, Err(gone.clone()), gone.to_string()),
        (Left::Failed(gone.clone()), Ok(true), gone.to_string()),
        (
            Left::Taken,
            Ok(false),
            "another process is taking it".to_string(),
        ),
        (
            Left::Unsettled(Delivery::TimedOut),
            Ok(false),
            "a delivery was not settled: TimedOut".to_string(),
        ),
        (Left::OutOfTime, Ok(false), "its time was spent".to_string()),
        (Left::Rounds, Ok(false), "facts kept arriving".to_string()),
    ] {
        assert_eq!(said(left, empty).left, Some(why));
    }
}
