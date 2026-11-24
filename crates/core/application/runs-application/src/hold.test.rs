//! The hook's hold policy (s4-Q14, H2): each row of the design's table
//! (hook-hold.design.md §1, H-Q1, H-Q2 and H-Q5 revised), over a fake sink,
//! a fake clock and an in-memory hold. A race is forced, never waited for:
//! the second hook runs INSIDE the first one's delivery.

use std::cell::RefCell;
use std::collections::VecDeque;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use angzarr_embedded::v1::{command_page, CommandBook, Uuid};
use daemon_proto::google::protobuf::Timestamp;
use run_ports::{
    Batch, Bound, Delivery, FactSink, Fate, Held, HeldEntry, HeldFact, HeldFacts, HeldGap,
    HoldError, HoldSummary, Settle, Settled,
};
use surface_ports::{HookEvent, MessageDisplay};

use super::*;
use crate::fakes::{uuid, SESSION_ME, SESSION_OTHER};
use crate::{observe, Observation};

const OWN: &str = "v0.2.0-aaaaaaa";
const OLDER: &str = "v0.1.0-bbbbbbb";
const NEWER: &str = "v0.3.0-ccccccc";
const NAME: &str = "projects/amber-quiet-heron/sessions/brisk-copper-moth";

fn session() -> Uuid {
    uuid(SESSION_ME)
}

fn root() -> uuid::Uuid {
    uuid::Uuid::parse_str(SESSION_ME).unwrap()
}

fn prompt(id: &str) -> HookEvent {
    HookEvent {
        event: "turn_start".to_string(),
        prompt_id: id.to_string(),
        prompt: format!("prompt {id}"),
        ..HookEvent::default()
    }
}

/// A MessageDisplay piece: one observation (`n` is 1, its only piece).
fn pieces(n: u32) -> HookEvent {
    HookEvent {
        event: "message_display".to_string(),
        prompt_id: "p1".to_string(),
        message: Some(MessageDisplay {
            turn_id: "t1".to_string(),
            message_id: "m1".to_string(),
            index: 0,
            is_final: n == 1,
            delta: "words".to_string(),
            ..Default::default()
        }),
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

/// The external ids `event`'s observations have, in order.
fn ids_of(event: &HookEvent) -> Vec<String> {
    observe(event)
        .iter()
        .map(|o| o.external_id(&session()))
        .collect()
}

/// A held record of `event`'s first observation, held by a hook of `stamp`.
fn held_of(event: &HookEvent, stamp: &str) -> HeldEntry {
    let observation = &observe(event)[0];
    HeldEntry::Fact(HeldFact::of_book(
        stamp,
        NAME,
        observation.external_id(&session()),
        &observation.book(&session()),
        None,
    ))
}

/// A gap record: the fact `dropped` could not be kept by a hook of `stamp`.
fn gap_of(dropped: &str, stamp: &str) -> HeldEntry {
    HeldEntry::Gap(HeldGap {
        stamp: stamp.to_string(),
        external_id: dropped.to_string(),
        held_at: Some(Timestamp {
            seconds: 1_700_000_050,
            nanos: 7,
        }),
        why: "bound".to_string(),
    })
}

fn ids_held(memory: &Memory) -> Vec<String> {
    memory
        .entries()
        .iter()
        .map(|e| match e {
            HeldEntry::Fact(f) => f.external_id.clone(),
            HeldEntry::Gap(g) => format!("gap:{}", g.external_id),
        })
        .collect()
}

// ------------------------------------------------------------------ fakes

#[derive(Default)]
struct Inner {
    entries: Vec<HeldEntry>,
    /// A take's flush lock is held.
    taking: bool,
    /// What `hold` answers instead of keeping.
    refuse: Option<Result<Held, HoldError>>,
    /// Calls of the port, by name.
    calls: Vec<&'static str>,
    /// What a settle set aside, in order.
    aside: Vec<HeldEntry>,
}

/// One session's hold in memory, with the adapter's take and settle
/// semantics: a take holds the flush lock until settled or dropped, and a
/// settle keeps what was appended since the snapshot.
#[derive(Default, Clone)]
struct Memory(Arc<Mutex<Inner>>);

impl Memory {
    fn holding(entries: Vec<HeldEntry>) -> Memory {
        let memory = Memory::default();
        memory.0.lock().unwrap().entries = entries;
        memory
    }

    fn entries(&self) -> Vec<HeldEntry> {
        self.0.lock().unwrap().entries.clone()
    }

    fn calls(&self) -> Vec<&'static str> {
        self.0.lock().unwrap().calls.clone()
    }

    /// What was set aside, by external id.
    fn aside(&self) -> Vec<String> {
        self.0
            .lock()
            .unwrap()
            .aside
            .iter()
            .map(|e| match e {
                HeldEntry::Fact(f) => f.external_id.clone(),
                HeldEntry::Gap(g) => format!("gap:{}", g.external_id),
            })
            .collect()
    }

    fn refusing(&self, answer: Result<Held, HoldError>) {
        self.0.lock().unwrap().refuse = Some(answer);
    }
}

impl HeldFacts for Memory {
    fn hold(&self, session: &uuid::Uuid, fact: HeldFact) -> Result<Held, HoldError> {
        assert_eq!(*session, root(), "the hold is the session's");
        let mut inner = self.0.lock().unwrap();
        inner.calls.push("hold");
        if let Some(answer) = inner.refuse.clone() {
            return answer;
        }
        inner.entries.push(HeldEntry::Fact(fact));
        Ok(Held::Kept)
    }

    fn is_empty(&self, _: &uuid::Uuid) -> Result<bool, HoldError> {
        let mut inner = self.0.lock().unwrap();
        inner.calls.push("is_empty");
        Ok(inner.entries.is_empty())
    }

    fn sessions(&self) -> Result<Vec<uuid::Uuid>, HoldError> {
        Ok(vec![root()])
    }

    fn take(&self, _: &uuid::Uuid) -> Result<Option<Batch>, HoldError> {
        let mut inner = self.0.lock().unwrap();
        inner.calls.push("take");
        if inner.taking {
            return Ok(None);
        }
        inner.taking = true;
        let snapshot = inner.entries.clone();
        let len = snapshot.len();
        Ok(Some(Batch::new(
            snapshot,
            Box::new(Settling {
                memory: self.clone(),
                len,
            }),
        )))
    }

    fn discard(&self, _: &uuid::Uuid) -> Result<u64, HoldError> {
        Ok(0)
    }

    fn survey(&self) -> Result<Vec<HoldSummary>, HoldError> {
        Ok(Vec::new())
    }
}

struct Settling {
    memory: Memory,
    len: usize,
}

impl Settle for Settling {
    fn settle(self: Box<Self>, fates: &[Fate]) -> Result<Settled, HoldError> {
        let mut inner = self.memory.0.lock().unwrap();
        let since = inner.entries.split_off(self.len);
        let mut kept = Vec::new();
        for (entry, fate) in std::mem::take(&mut inner.entries).into_iter().zip(fates) {
            match fate {
                Fate::Keep => kept.push(entry),
                Fate::SetAside => inner.aside.push(entry),
                Fate::Cut => {}
            }
        }
        let count = kept.len();
        inner.entries = kept;
        inner.entries.extend(since);
        Ok(if inner.entries.is_empty() {
            Settled::Emptied
        } else {
            Settled::Cut { kept: count }
        })
    }
}

impl Drop for Settling {
    fn drop(&mut self) {
        self.memory.0.lock().unwrap().taking = false;
    }
}

/// The hook's time: what is left, spent by each delivery the sink takes.
struct Clock(RefCell<Duration>);

impl Clock {
    fn with(left: Duration) -> Clock {
        Clock(RefCell::new(left))
    }

    fn spend(&self, spent: Duration) {
        let left = *self.0.borrow();
        *self.0.borrow_mut() = left.saturating_sub(spent);
    }
}

impl HookClock for Clock {
    fn remaining(&self) -> Duration {
        *self.0.borrow()
    }

    fn now(&self) -> Option<Timestamp> {
        Some(Timestamp {
            seconds: 1_700_000_000,
            nanos: 0,
        })
    }
}

/// A sink that records each book's external id and answers from a script
/// (`Applied` once it runs out), spending `cost` of the clock each time,
/// and runs `during` (once) inside its first delivery.
struct Sink<'c> {
    books: RefCell<Vec<CommandBook>>,
    clock: Option<&'c Clock>,
    cost: Duration,
    answers: RefCell<VecDeque<Delivery>>,
    delivered: RefCell<Vec<String>>,
    within: RefCell<Vec<Duration>>,
    during: RefCell<Option<Box<dyn FnOnce() + 'c>>>,
}

impl<'c> Sink<'c> {
    fn new() -> Sink<'c> {
        Sink {
            books: RefCell::new(Vec::new()),
            clock: None,
            cost: Duration::ZERO,
            answers: RefCell::new(VecDeque::new()),
            delivered: RefCell::new(Vec::new()),
            within: RefCell::new(Vec::new()),
            during: RefCell::new(None),
        }
    }

    fn spending(clock: &'c Clock, cost: Duration) -> Sink<'c> {
        Sink {
            clock: Some(clock),
            cost,
            ..Sink::new()
        }
    }

    fn answering(self, answers: impl IntoIterator<Item = Delivery>) -> Sink<'c> {
        *self.answers.borrow_mut() = answers.into_iter().collect();
        self
    }

    fn delivered(&self) -> Vec<String> {
        self.delivered.borrow().clone()
    }
}

fn external_id(book: &CommandBook) -> String {
    match book.page.as_ref().and_then(|p| p.body.as_ref()) {
        Some(command_page::Body::Fact(fact)) => fact.origin.clone().unwrap().external_id,
        _ => panic!("a fact book"),
    }
}

impl FactSink for Sink<'_> {
    fn deliver(&self, book: CommandBook, within: Duration) -> Delivery {
        self.delivered.borrow_mut().push(external_id(&book));
        self.books.borrow_mut().push(book.clone());
        self.within.borrow_mut().push(within);
        if let Some(during) = self.during.borrow_mut().take() {
            during();
        }
        if let Some(clock) = self.clock {
            clock.spend(self.cost);
        }
        self.answers
            .borrow_mut()
            .pop_front()
            .unwrap_or(Delivery::Applied)
    }
}

fn daemon<'a, 'c>(stamp: &'a str, sink: &'a Sink<'c>) -> Serving<'a, Sink<'c>> {
    Serving::Daemon { stamp, sink }
}

/// A PostToolBatch of `n` calls: one observation each, in call order.
fn batch_of(n: usize) -> HookEvent {
    HookEvent {
        event: "post_tool_batch".to_string(),
        prompt_id: "p1".to_string(),
        tool_calls: (0..n)
            .map(|i| surface_ports::ToolCall {
                tool: "Bash".to_string(),
                tool_use_id: format!("t{i}"),
                input: "{}".to_string(),
                response: "ok".to_string(),
                ..surface_ports::ToolCall::default()
            })
            .collect(),
        ..HookEvent::default()
    }
}

fn outcomes(kept: &[Kept]) -> Vec<Outcome> {
    kept.iter().map(|k| k.outcome.clone()).collect()
}

fn long() -> Clock {
    Clock::with(Duration::from_secs(10))
}

// ------------------------------------------------------------- the table

// No daemon could be reached or started (H-Q2 (b)): every observation is
// held, in order, by the hook's stamp and the session's name, and nothing
// is delivered.
#[test]
fn a_hook_that_reaches_no_daemon_holds_every_fact_in_order() {
    let memory = Memory::default();
    let clock = long();
    let holding = Holding::new(&memory, &clock, OWN, NAME);
    let report = holding.record::<Sink>(
        &session(),
        &pieces(1),
        Serving::Unreached {
            why: "no daemon".to_string(),
        },
    );
    assert_eq!(
        outcomes(&report.own),
        vec![Outcome::Held(Why::Unreached {
            why: "no daemon".to_string()
        })]
    );
    assert_eq!(ids_held(&memory), ids_of(&pieces(1)));
    let HeldEntry::Fact(fact) = &memory.entries()[0] else {
        panic!("a fact")
    };
    assert_eq!(fact.stamp, OWN);
    assert_eq!(fact.session, NAME);
    assert_eq!(fact.held_at, clock.now());
    assert_eq!(
        fact.decode_book().unwrap(),
        observe(&pieces(1))[0].book(&session()),
        "the book exactly as the observation made it"
    );
    assert!(report.flushed.is_empty());
}

// A daemon of an older version (H-Q1 (b)): held, nothing delivered, the
// hold not even taken.
#[test]
fn a_hook_newer_than_its_daemon_holds_and_delivers_nothing() {
    let memory = Memory::holding(vec![held_of(&prompt("p0"), OWN)]);
    let clock = long();
    let sink = Sink::new();
    let report = Holding::new(&memory, &clock, OWN, NAME).record(
        &session(),
        &prompt("p1"),
        daemon(OLDER, &sink),
    );
    assert_eq!(
        outcomes(&report.own),
        vec![Outcome::Held(Why::Older {
            daemon: OLDER.to_string(),
            own: OWN.to_string()
        })]
    );
    assert!(sink.delivered().is_empty());
    assert_eq!(
        ids_held(&memory),
        [ids_of(&prompt("p0")), ids_of(&prompt("p1"))].concat()
    );
    assert!(!memory.calls().contains(&"take"));
}

// A daemon up to date for the hook, its session holding nothing: the facts
// are delivered as before, and the hold is only looked at.
#[test]
fn a_hook_of_the_daemons_version_with_nothing_held_delivers_directly() {
    let memory = Memory::default();
    let clock = long();
    let sink = Sink::new();
    let report = Holding::new(&memory, &clock, OWN, NAME).record(
        &session(),
        &prompt("p1"),
        daemon(OWN, &sink),
    );
    assert_eq!(
        outcomes(&report.own),
        vec![Outcome::Delivered(Delivery::Applied)]
    );
    assert_eq!(sink.delivered(), ids_of(&prompt("p1")));
    assert!(memory.entries().is_empty());
    assert_eq!(memory.calls(), vec!["is_empty"]);
    assert!(report.said().is_empty());
}

// A hook older than its daemon delivers (up to date is "not older").
#[test]
fn a_hook_older_than_its_daemon_delivers() {
    let memory = Memory::default();
    let clock = long();
    let sink = Sink::new();
    let report = Holding::new(&memory, &clock, OWN, NAME).record(
        &session(),
        &prompt("p1"),
        daemon(NEWER, &sink),
    );
    assert_eq!(
        outcomes(&report.own),
        vec![Outcome::Delivered(Delivery::Applied)]
    );
    assert_eq!(sink.delivered(), ids_of(&prompt("p1")));
}

// H-Q5 revised: an up-to-date hook that finds its session's hold non-empty
// delivers it in held order, cuts it, then delivers its own facts; a gap
// record is delivered as the gap fact in its place (H3).
#[test]
fn an_up_to_date_hook_flushes_its_sessions_hold_in_order_then_delivers_its_own() {
    let memory = Memory::holding(vec![
        held_of(&prompt("p1"), OLDER),
        held_of(&pieces(1), OWN),
        gap_of("dropped", OWN),
    ]);
    let clock = long();
    let sink = Sink::new();
    let report = Holding::new(&memory, &clock, OWN, NAME).record(
        &session(),
        &turn_end("p1"),
        daemon(OWN, &sink),
    );
    assert_eq!(
        sink.delivered(),
        [
            ids_of(&prompt("p1")),
            ids_of(&pieces(1)),
            vec!["hook:gap:dropped".to_string()],
            ids_of(&turn_end("p1"))
        ]
        .concat(),
        "held first, in order, the gap in its place"
    );
    assert!(memory.entries().is_empty(), "the hold is cut");
    assert_eq!(
        outcomes(&report.flushed),
        vec![Outcome::Delivered(Delivery::Applied); 3]
    );
    assert_eq!(
        outcomes(&report.own),
        vec![Outcome::Delivered(Delivery::Applied)]
    );
    assert!(report.said().is_empty());
}

// The hook's time runs out mid-flush: what was delivered is cut, the rest
// stays, and the hook's own fact is held behind it.
#[test]
fn a_budget_spent_mid_flush_cuts_what_was_delivered_and_holds_the_rest() {
    let memory = Memory::holding(vec![
        held_of(&prompt("p1"), OWN),
        held_of(&prompt("p2"), OWN),
        held_of(&prompt("p3"), OWN),
    ]);
    let clock = Clock::with(Duration::from_secs(2));
    let sink = Sink::spending(&clock, Duration::from_secs(1));
    let report = Holding::new(&memory, &clock, OWN, NAME).record(
        &session(),
        &turn_end("p3"),
        daemon(OWN, &sink),
    );
    assert_eq!(
        sink.delivered(),
        [ids_of(&prompt("p1")), ids_of(&prompt("p2"))].concat()
    );
    assert_eq!(
        *sink.within.borrow(),
        vec![Duration::from_secs(2), Duration::from_secs(1)],
        "each delivery waits only what is left"
    );
    assert_eq!(
        ids_held(&memory),
        [ids_of(&prompt("p3")), ids_of(&turn_end("p3"))].concat(),
        "the undelivered one first, then the hook's own"
    );
    assert_eq!(outcomes(&report.own), vec![Outcome::HeldBehind]);
}

// A held fact's delivery left unsettled stops the flush: it and everything
// after it stay, and the hook's own fact goes behind.
#[test]
fn an_unsettled_held_delivery_keeps_it_and_holds_the_hook_behind() {
    let memory = Memory::holding(vec![
        held_of(&prompt("p1"), OWN),
        held_of(&prompt("p2"), OWN),
    ]);
    let clock = long();
    let sink = Sink::new().answering([Delivery::Applied, Delivery::TimedOut]);
    let report = Holding::new(&memory, &clock, OWN, NAME).record(
        &session(),
        &turn_end("p2"),
        daemon(OWN, &sink),
    );
    assert_eq!(
        ids_held(&memory),
        [ids_of(&prompt("p2")), ids_of(&turn_end("p2"))].concat()
    );
    assert_eq!(outcomes(&report.own), vec![Outcome::HeldBehind]);
    assert_eq!(report.said().len(), 1, "the own fact held behind is said");
}

// A final answer to a held fact (refused, parked) is settled: it leaves the
// hold for its set-aside part (no silent drop, H3), is said, and is never
// retried; what follows it is still delivered.
#[test]
fn a_refused_or_parked_held_fact_is_set_aside_and_said() {
    for answer in [
        Delivery::Refused {
            detail: "no".to_string(),
        },
        Delivery::Parked,
        // A session the daemon has no record of (H3 follow-up).
        Delivery::NoSuchSession,
    ] {
        let memory = Memory::holding(vec![
            held_of(&prompt("p1"), OWN),
            held_of(&prompt("p2"), OWN),
        ]);
        let clock = long();
        let sink = Sink::new().answering([answer.clone()]);
        let report = Holding::new(&memory, &clock, OWN, NAME).record(
            &session(),
            &turn_end("p2"),
            daemon(OWN, &sink),
        );
        assert!(memory.entries().is_empty());
        assert_eq!(
            memory.aside(),
            ids_of(&prompt("p1")),
            "set aside, not dropped"
        );
        assert_eq!(
            outcomes(&report.flushed),
            vec![
                Outcome::Delivered(answer.clone()),
                Outcome::Delivered(Delivery::Applied)
            ]
        );
        assert_eq!(
            outcomes(&report.own),
            vec![Outcome::Delivered(Delivery::Applied)]
        );
        assert_eq!(report.said().len(), 1);
    }
}

// An own delivery left unsettled (the daemon died under the hook) is held,
// and every later one of the hook behind it (H-Q2).
#[test]
fn an_unsettled_own_delivery_is_held_and_the_rest_behind_it() {
    let event = batch_of(3);
    let memory = Memory::default();
    let clock = long();
    let unavailable = Delivery::Unavailable {
        detail: "gone".to_string(),
    };
    let sink = Sink::new().answering([Delivery::Applied, unavailable.clone()]);
    let report =
        Holding::new(&memory, &clock, OWN, NAME).record(&session(), &event, daemon(OWN, &sink));
    assert_eq!(
        outcomes(&report.own),
        vec![
            Outcome::Delivered(Delivery::Applied),
            Outcome::Held(Why::Unsettled(unavailable)),
            Outcome::HeldBehind
        ]
    );
    assert_eq!(sink.delivered().len(), 2, "nothing tried after it");
    assert_eq!(ids_held(&memory), ids_of(&event)[1..].to_vec());
}

// The hook's time already spent: its own fact is held, never sent.
#[test]
fn an_own_fact_with_no_time_left_is_held() {
    let memory = Memory::default();
    let clock = Clock::with(Duration::ZERO);
    let sink = Sink::new();
    let report = Holding::new(&memory, &clock, OWN, NAME).record(
        &session(),
        &prompt("p1"),
        daemon(OWN, &sink),
    );
    assert!(sink.delivered().is_empty());
    assert_eq!(outcomes(&report.own), vec![Outcome::Held(Why::OutOfTime)]);
}

// Another process is taking the hold (its flush lock is held): the hook
// skips it, never waits, and holds its own fact behind.
#[test]
fn a_hold_another_process_is_taking_holds_the_hook_behind() {
    let memory = Memory::holding(vec![held_of(&prompt("p1"), OWN)]);
    let other = memory.take(&root()).unwrap().expect("the first take");
    let clock = long();
    let sink = Sink::new();
    let report = Holding::new(&memory, &clock, OWN, NAME).record(
        &session(),
        &turn_end("p1"),
        daemon(OWN, &sink),
    );
    assert!(sink.delivered().is_empty());
    assert_eq!(outcomes(&report.own), vec![Outcome::HeldBehind]);
    drop(other);
    assert_eq!(
        ids_held(&memory),
        [ids_of(&prompt("p1")), ids_of(&turn_end("p1"))].concat()
    );
}

// Two hooks of one session at once, forced: the second fires INSIDE the
// first one's delivery of the held fact. It finds the hold being taken and
// holds behind; the first, once its cut is made, takes the hold again,
// delivers the second's fact, then its own. Nothing is lost or delivered
// twice, and the session's facts reach the stream in held order.
#[test]
fn two_hooks_of_one_session_at_once_keep_its_facts_first_in_first_out() {
    let memory = Memory::holding(vec![held_of(&prompt("p1"), OWN)]);
    let clock = long();
    let second_report = RefCell::new(None);
    let sink = Sink::new();
    {
        let memory = memory.clone();
        let second_report = &second_report;
        *sink.during.borrow_mut() = Some(Box::new(move || {
            let clock = long();
            let other = Sink::new();
            let report = Holding::new(&memory, &clock, OWN, NAME).record(
                &session(),
                &pieces(1),
                daemon(OWN, &other),
            );
            assert!(other.delivered().is_empty(), "the second delivers nothing");
            *second_report.borrow_mut() = Some(report);
        }));
    }
    let first = Holding::new(&memory, &clock, OWN, NAME).record(
        &session(),
        &turn_end("p1"),
        daemon(OWN, &sink),
    );
    let second = second_report
        .borrow_mut()
        .take()
        .expect("the second hook ran");
    assert_eq!(outcomes(&second.own), vec![Outcome::HeldBehind]);
    assert_eq!(
        sink.delivered(),
        [
            ids_of(&prompt("p1")),
            ids_of(&pieces(1)),
            ids_of(&turn_end("p1"))
        ]
        .concat()
    );
    assert!(memory.entries().is_empty());
    assert_eq!(first.flushed.len(), 2);
}

// A held record the daemon is not up to date for (held by a newer hook, a
// downgrade) is kept and passed over: the records after it are delivered,
// and so is the hook's own fact.
#[test]
fn a_record_held_for_a_newer_daemon_is_kept_and_blocks_nothing() {
    let memory = Memory::holding(vec![
        held_of(&prompt("p1"), NEWER),
        held_of(&prompt("p2"), OWN),
    ]);
    let clock = long();
    let sink = Sink::new();
    let report = Holding::new(&memory, &clock, OWN, NAME).record(
        &session(),
        &turn_end("p2"),
        daemon(OWN, &sink),
    );
    assert_eq!(
        sink.delivered(),
        [ids_of(&prompt("p2")), ids_of(&turn_end("p2"))].concat()
    );
    assert_eq!(ids_held(&memory), ids_of(&prompt("p1")));
    assert_eq!(
        outcomes(&report.own),
        vec![Outcome::Delivered(Delivery::Applied)]
    );
}

// A held record that is not a fact of this session (another stream's book,
// or an external id not its book's) is dropped, said, and never sent.
#[test]
fn a_held_record_of_another_stream_is_dropped_and_said() {
    let observation = &observe(&prompt("p1"))[0];
    let elsewhere = HeldEntry::Fact(HeldFact::of_book(
        OWN,
        NAME,
        observation.external_id(&uuid(SESSION_OTHER)),
        &observation.book(&uuid(SESSION_OTHER)),
        None,
    ));
    let relabelled = HeldEntry::Fact(HeldFact::of_book(
        OWN,
        NAME,
        "hook:prompt:not-its-book",
        &observation.book(&session()),
        None,
    ));
    let memory = Memory::holding(vec![elsewhere, relabelled]);
    let clock = long();
    let sink = Sink::new();
    let report = Holding::new(&memory, &clock, OWN, NAME).record(
        &session(),
        &turn_end("p1"),
        daemon(OWN, &sink),
    );
    assert_eq!(sink.delivered(), ids_of(&turn_end("p1")));
    assert!(memory.entries().is_empty());
    assert_eq!(
        memory.aside(),
        vec![
            observation.external_id(&uuid(SESSION_OTHER)),
            "hook:prompt:not-its-book".to_string()
        ],
        "set aside, never dropped (H3)"
    );
    assert_eq!(
        outcomes(&report.flushed),
        vec![
            Outcome::NotHeld(NotHeld::Foreign),
            Outcome::NotHeld(NotHeld::Foreign)
        ]
    );
    assert_eq!(report.said().len(), 2);
}

// A hold at its bound keeps nothing more: the fact is said as dropped.
#[test]
fn a_hold_at_its_bound_drops_the_fact_and_says_so() {
    let memory = Memory::default();
    memory.refusing(Ok(Held::Full {
        bound: Bound::Session(32 << 20),
        gap: true,
    }));
    let clock = long();
    let report = Holding::new(&memory, &clock, OWN, NAME).record::<Sink>(
        &session(),
        &prompt("p1"),
        Serving::Unreached {
            why: "no daemon".to_string(),
        },
    );
    assert_eq!(
        outcomes(&report.own),
        vec![Outcome::NotHeld(NotHeld::Full(Bound::Session(32 << 20)))]
    );
}

// A hold that cannot be used fails open: the fact is said, with the error.
#[test]
fn a_hold_that_cannot_be_used_says_why() {
    let memory = Memory::default();
    let error = HoldError::Lock {
        path: "/h/.ctxloom/held/x.lock".to_string(),
        ext_err_txt: "held".to_string(),
    };
    memory.refusing(Err(error.clone()));
    let clock = long();
    let sink = Sink::new();
    let report = Holding::new(&memory, &clock, OWN, NAME).record(
        &session(),
        &prompt("p1"),
        daemon(OLDER, &sink),
    );
    assert_eq!(
        outcomes(&report.own),
        vec![Outcome::NotHeld(NotHeld::Failed(error))]
    );
}

// An event that observes nothing touches nothing.
#[test]
fn an_event_of_no_observation_touches_no_hold() {
    let memory = Memory::holding(vec![held_of(&prompt("p1"), OWN)]);
    let clock = long();
    let sink = Sink::new();
    let report = Holding::new(&memory, &clock, OWN, NAME).record(
        &session(),
        &HookEvent::default(),
        daemon(OWN, &sink),
    );
    assert_eq!(report, HoldReport::default());
    assert!(memory.calls().is_empty());
    assert!(sink.delivered().is_empty());
}

// ------------------------------------------------- H3: the client's flush

// A gap record is delivered as the gap fact, in its place in the order, on
// the hold's own stream: which fact was dropped, why, when (the dropping
// hook's clock) and by which hook (s4-Q14, H3).
#[test]
fn a_gap_is_delivered_as_the_gap_fact_in_its_place() {
    let memory = Memory::holding(vec![
        held_of(&prompt("p1"), OWN),
        gap_of("hook:turn-end:s:p1", OWN),
        held_of(&prompt("p2"), OWN),
    ]);
    let clock = long();
    let sink = Sink::new();
    let done = Flush::new(&memory, &clock).session(&root(), OWN, &sink);
    assert_eq!(done.left, Left::Nothing);
    assert_eq!(
        sink.delivered(),
        [
            ids_of(&prompt("p1")),
            vec!["hook:gap:hook:turn-end:s:p1".to_string()],
            ids_of(&prompt("p2"))
        ]
        .concat()
    );
    let book = sink.books.borrow()[1].clone();
    let cover = book.cover.clone().unwrap();
    assert_eq!(cover.domain, sessions::SESSION_DOMAIN);
    assert_eq!(cover.root, Some(session()), "the hold's own stream");
    let HeldEntry::Gap(held) = gap_of("hook:turn-end:s:p1", OWN) else {
        unreachable!()
    };
    let expected = Observation::gap(&held);
    assert_eq!(book, expected.book(&session()));
    let Observation::CaptureGap(gap) = expected else {
        panic!("a gap");
    };
    assert_eq!(gap.dropped, "hook:turn-end:s:p1");
    assert_eq!(gap.why, "bound");
    assert_eq!(gap.stamp, OWN);
    assert_eq!(
        (gap.dropped_at.seconds, gap.dropped_at.nanos),
        (1_700_000_050, 7)
    );
    assert!(memory.entries().is_empty());
    assert_eq!(done.flushed.len(), 3);
}

// A gap held by a hook newer than the daemon waits, as a fact of it would.
#[test]
fn a_gap_held_by_a_newer_hook_is_kept() {
    let memory = Memory::holding(vec![gap_of("x", NEWER), held_of(&prompt("p1"), OWN)]);
    let clock = long();
    let sink = Sink::new();
    let done = Flush::new(&memory, &clock).session(&root(), OWN, &sink);
    assert_eq!(sink.delivered(), ids_of(&prompt("p1")));
    assert_eq!(ids_held(&memory), vec!["gap:x".to_string()]);
    assert_eq!(done.left, Left::Nothing);
}

// A command flushes only when it is of an up-to-date version: not older than
// the daemon's (semver; two builds of one version flush to each other).
#[test]
fn a_command_flushes_only_when_it_is_not_older_than_the_daemon() {
    assert!(command_flushes(OWN, OWN));
    assert!(command_flushes(NEWER, OWN));
    assert!(command_flushes("v0.2.0-ddddddd", OWN));
    assert!(!command_flushes(OLDER, OWN));
}

/// Holds of several sessions, each a [`Memory`], listed in root order.
struct Many(Vec<(uuid::Uuid, Memory)>);

impl Many {
    fn of(&self, session: &uuid::Uuid) -> &Memory {
        &self.0.iter().find(|(r, _)| r == session).expect("a hold").1
    }
}

impl HeldFacts for Many {
    fn hold(&self, _: &uuid::Uuid, _: HeldFact) -> Result<Held, HoldError> {
        panic!("a flush holds nothing")
    }

    fn is_empty(&self, session: &uuid::Uuid) -> Result<bool, HoldError> {
        self.of(session).is_empty(session)
    }

    fn sessions(&self) -> Result<Vec<uuid::Uuid>, HoldError> {
        Ok(self.0.iter().map(|(r, _)| *r).collect())
    }

    fn take(&self, session: &uuid::Uuid) -> Result<Option<Batch>, HoldError> {
        self.of(session).take(session)
    }

    fn discard(&self, _: &uuid::Uuid) -> Result<u64, HoldError> {
        Ok(0)
    }

    fn survey(&self) -> Result<Vec<HoldSummary>, HoldError> {
        Ok(Vec::new())
    }
}

fn other_root() -> uuid::Uuid {
    uuid::Uuid::parse_str(SESSION_OTHER).unwrap()
}

/// A held record of `event`'s first observation on the stream `on`.
fn held_on(on: &str, event: &HookEvent) -> HeldEntry {
    let observation = &observe(event)[0];
    HeldEntry::Fact(HeldFact::of_book(
        OWN,
        NAME,
        observation.external_id(&uuid(on)),
        &observation.book(&uuid(on)),
        None,
    ))
}

fn ids_on(on: &str, event: &HookEvent) -> Vec<String> {
    observe(event)
        .iter()
        .map(|o| o.external_id(&uuid(on)))
        .collect()
}

// A command on its way in delivers what EVERY session's hold holds, each in
// its own order, and leaves every hold it emptied empty.
#[test]
fn a_command_flushes_every_sessions_hold_in_order() {
    let many = Many(vec![
        (
            root(),
            Memory::holding(vec![
                held_on(SESSION_ME, &prompt("p1")),
                held_on(SESSION_ME, &turn_end("p1")),
            ]),
        ),
        (
            other_root(),
            Memory::holding(vec![held_on(SESSION_OTHER, &prompt("q1"))]),
        ),
    ]);
    let clock = long();
    let sink = Sink::new();
    let done = Flush::new(&many, &clock).every(OWN, &sink).unwrap();
    assert_eq!(
        sink.delivered(),
        [
            ids_on(SESSION_ME, &prompt("p1")),
            ids_on(SESSION_ME, &turn_end("p1")),
            ids_on(SESSION_OTHER, &prompt("q1"))
        ]
        .concat()
    );
    assert_eq!(
        done.iter()
            .map(|d| (d.root, d.left.clone()))
            .collect::<Vec<_>>(),
        vec![(root(), Left::Nothing), (other_root(), Left::Nothing)]
    );
    assert!(many.of(&root()).entries().is_empty());
    assert!(many.of(&other_root()).entries().is_empty());
}

// The flush's time runs out mid-flush (forced: each delivery spends a
// second of two): what was delivered is cut, the rest stays in order, and a
// hold the time never reached is untouched and said so.
#[test]
fn a_bound_spent_mid_flush_cuts_what_went_and_leaves_the_rest() {
    let many = Many(vec![
        (
            root(),
            Memory::holding(vec![
                held_on(SESSION_ME, &prompt("p1")),
                held_on(SESSION_ME, &prompt("p2")),
                held_on(SESSION_ME, &prompt("p3")),
            ]),
        ),
        (
            other_root(),
            Memory::holding(vec![held_on(SESSION_OTHER, &prompt("q1"))]),
        ),
    ]);
    let clock = Clock::with(Duration::from_secs(2));
    let sink = Sink::spending(&clock, Duration::from_secs(1));
    let done = Flush::new(&many, &clock).every(OWN, &sink).unwrap();
    assert_eq!(
        sink.delivered(),
        [
            ids_on(SESSION_ME, &prompt("p1")),
            ids_on(SESSION_ME, &prompt("p2"))
        ]
        .concat()
    );
    assert_eq!(
        ids_held(many.of(&root())),
        ids_on(SESSION_ME, &prompt("p3"))
    );
    assert_eq!(ids_held(many.of(&other_root())).len(), 1, "untouched");
    assert_eq!(
        done.iter()
            .map(|d| (d.root, d.left.clone()))
            .collect::<Vec<_>>(),
        vec![(root(), Left::OutOfTime), (other_root(), Left::OutOfTime)]
    );
    assert!(many.of(&other_root()).calls().is_empty(), "never taken");
}

// Two commands at once, forced: the second runs INSIDE the first's first
// delivery. It finds the first's hold being taken and skips it (never
// waits), and delivers the other hold; the first then finds that one empty.
// Every fact is delivered once.
#[test]
fn two_commands_at_once_one_flushes_a_hold_the_other_skips_it() {
    let many = Many(vec![
        (
            root(),
            Memory::holding(vec![held_on(SESSION_ME, &prompt("p1"))]),
        ),
        (
            other_root(),
            Memory::holding(vec![held_on(SESSION_OTHER, &prompt("q1"))]),
        ),
    ]);
    let second: RefCell<Option<(Vec<SessionFlush>, Vec<String>)>> = RefCell::new(None);
    let clock = long();
    let sink = Sink::new();
    {
        let many = &many;
        let second = &second;
        *sink.during.borrow_mut() = Some(Box::new(move || {
            let clock = long();
            let other = Sink::new();
            let done = Flush::new(many, &clock).every(OWN, &other).unwrap();
            *second.borrow_mut() = Some((done, other.delivered()));
        }));
    }
    let first = Flush::new(&many, &clock).every(OWN, &sink).unwrap();
    let (second, second_delivered) = second.borrow_mut().take().expect("the second ran");
    assert_eq!(
        second
            .iter()
            .map(|d| (d.root, d.left.clone()))
            .collect::<Vec<_>>(),
        vec![(root(), Left::Taken), (other_root(), Left::Nothing)]
    );
    assert_eq!(second_delivered, ids_on(SESSION_OTHER, &prompt("q1")));
    assert_eq!(sink.delivered(), ids_on(SESSION_ME, &prompt("p1")));
    assert_eq!(
        first
            .iter()
            .map(|d| (d.root, d.left.clone()))
            .collect::<Vec<_>>(),
        vec![(root(), Left::Nothing), (other_root(), Left::Nothing)]
    );
    assert!(many.of(&root()).entries().is_empty());
    assert!(many.of(&other_root()).entries().is_empty());
}

// A hold that cannot be used is said for itself, and the others still flush.
#[test]
fn a_hold_that_cannot_be_read_leaves_the_others_flushing() {
    struct Broken(Many);
    impl HeldFacts for Broken {
        fn hold(&self, s: &uuid::Uuid, f: HeldFact) -> Result<Held, HoldError> {
            self.0.hold(s, f)
        }
        fn is_empty(&self, s: &uuid::Uuid) -> Result<bool, HoldError> {
            if *s == root() {
                return Err(HoldError::Format {
                    path: "x".to_string(),
                });
            }
            self.0.is_empty(s)
        }
        fn sessions(&self) -> Result<Vec<uuid::Uuid>, HoldError> {
            self.0.sessions()
        }
        fn take(&self, s: &uuid::Uuid) -> Result<Option<Batch>, HoldError> {
            self.0.take(s)
        }
        fn discard(&self, s: &uuid::Uuid) -> Result<u64, HoldError> {
            self.0.discard(s)
        }
        fn survey(&self) -> Result<Vec<HoldSummary>, HoldError> {
            self.0.survey()
        }
    }
    let broken = Broken(Many(vec![
        (root(), Memory::default()),
        (
            other_root(),
            Memory::holding(vec![held_on(SESSION_OTHER, &prompt("q1"))]),
        ),
    ]));
    let clock = long();
    let sink = Sink::new();
    let done = Flush::new(&broken, &clock).every(OWN, &sink).unwrap();
    assert_eq!(
        done[0].left,
        Left::Failed(HoldError::Format {
            path: "x".to_string()
        })
    );
    assert_eq!(done[1].left, Left::Nothing);
    assert_eq!(sink.delivered(), ids_on(SESSION_OTHER, &prompt("q1")));
}

// A command's flush names each hold's session (its first fact's name) and
// what it set aside, so it is said once, by session (H3: no silent drop).
#[test]
fn a_session_flush_names_its_session_and_what_it_set_aside() {
    let memory = Memory::holding(vec![
        held_of(&prompt("p1"), OWN),
        held_of(&prompt("p2"), OWN),
        held_of(&prompt("p3"), OWN),
    ]);
    let clock = long();
    let sink = Sink::new().answering([
        Delivery::Parked,
        Delivery::Applied,
        Delivery::Refused {
            detail: "no".to_string(),
        },
    ]);
    let done = Flush::new(&memory, &clock).session(&root(), OWN, &sink);
    assert_eq!(done.session.as_deref(), Some(NAME));
    assert_eq!(
        done.set_aside()
            .iter()
            .map(|k| k.external_id.clone())
            .collect::<Vec<_>>(),
        [ids_of(&prompt("p1")), ids_of(&prompt("p3"))].concat()
    );
    assert_eq!(
        memory.aside(),
        [ids_of(&prompt("p1")), ids_of(&prompt("p3"))].concat()
    );
    assert_eq!(done.left, Left::Nothing);
}

// ------------------------------------------- a frontend's reports (slice 4)

/// A frontend's report of the session's run of `incarnation`: a
/// `RecordRunnerStarted` (`started`) or a `RecordRunExited`, keyed as the frontend
/// keys it.
fn run_report(incarnation: u32, started: bool) -> RunReport {
    let who = crate::Identity {
        run: runs::run_root(SESSION_ME, incarnation),
        session: session(),
        incarnation,
        harp: "brisk-copper-moth".to_string(),
        depth: 0,
    };
    let fact = match started {
        true => crate::RunnerFact::RunnerStarted(runs::RecordRunnerStarted {
            runtime: "claude-code".to_string(),
            workspace: "/work".to_string(),
            pid: 4242,
        }),
        false => crate::RunnerFact::RunExited(runs::RecordRunExited { code: 0, signal: 0 }),
    };
    RunReport {
        external_id: fact.external_id(&who),
        book: fact.book(&who),
        incarnation,
    }
}

// A frontend's report no daemon takes is held in its session's hold, as a
// hook's facts are, with the incarnation whose run it is.
#[test]
fn a_frontends_report_no_daemon_takes_is_held_with_its_incarnation() {
    let memory = Memory::default();
    let clock = long();
    let started = run_report(2, true);
    let report = Holding::new(&memory, &clock, OWN, NAME).report::<Sink>(
        &session(),
        std::slice::from_ref(&started),
        Serving::Unreached {
            why: "no daemon".to_string(),
        },
    );
    assert_eq!(
        outcomes(&report.own),
        vec![Outcome::Held(Why::Unreached {
            why: "no daemon".to_string()
        })]
    );
    let HeldEntry::Fact(fact) = &memory.entries()[0] else {
        panic!("a fact")
    };
    assert_eq!(fact.incarnation, 2);
    assert_eq!(fact.external_id, started.external_id);
    assert_eq!(fact.decode_book().unwrap(), started.book);
}

// The frontend's next attempt (its exit report) reaches an up-to-date
// daemon: what the session's hold holds (its held start, behind a hook's
// prompt) is delivered first, in order, then the exit.
#[test]
fn a_frontends_next_report_delivers_what_is_held_first_in_order() {
    let started = run_report(1, true);
    let memory = Memory::holding(vec![
        held_of(&prompt("p1"), OWN),
        HeldEntry::Fact(
            HeldFact::of_book(OWN, NAME, started.external_id.clone(), &started.book, None)
                .of_run(1),
        ),
    ]);
    let clock = long();
    let sink = Sink::new();
    let exited = run_report(1, false);
    let report = Holding::new(&memory, &clock, OWN, NAME).report(
        &session(),
        std::slice::from_ref(&exited),
        daemon(OWN, &sink),
    );
    assert_eq!(
        sink.delivered(),
        [
            ids_of(&prompt("p1")),
            vec![started.external_id.clone(), exited.external_id.clone()]
        ]
        .concat()
    );
    assert!(memory.entries().is_empty(), "the hold is cut");
    assert_eq!(
        outcomes(&report.own),
        vec![Outcome::Delivered(Delivery::Applied)]
    );
}

// A held run fact is this session's only on the run of the incarnation it
// was held with: another incarnation's root, or a run fact held as the
// session's own, is set aside.
#[test]
fn a_held_run_fact_of_another_run_is_set_aside() {
    let started = run_report(1, true);
    let other_run = HeldEntry::Fact(
        HeldFact::of_book(OWN, NAME, started.external_id.clone(), &started.book, None).of_run(2),
    );
    let not_marked = HeldEntry::Fact(HeldFact::of_book(
        OWN,
        NAME,
        started.external_id.clone(),
        &started.book,
        None,
    ));
    let memory = Memory::holding(vec![other_run, not_marked]);
    let clock = long();
    let sink = Sink::new();
    let report = Holding::new(&memory, &clock, OWN, NAME).report(
        &session(),
        &[run_report(1, false)],
        daemon(OWN, &sink),
    );
    assert_eq!(sink.delivered(), [run_report(1, false).external_id]);
    assert_eq!(
        outcomes(&report.flushed),
        vec![
            Outcome::NotHeld(NotHeld::Foreign),
            Outcome::NotHeld(NotHeld::Foreign)
        ]
    );
}

// An up-to-date daemon that leaves a report unsettled: it is held, for the
// next attempt.
#[test]
fn a_frontends_report_left_unsettled_is_held() {
    let memory = Memory::default();
    let clock = long();
    let sink = Sink::new().answering([Delivery::TimedOut]);
    let report = Holding::new(&memory, &clock, OWN, NAME).report(
        &session(),
        &[run_report(1, false)],
        daemon(OWN, &sink),
    );
    assert_eq!(
        outcomes(&report.own),
        vec![Outcome::Held(Why::Unsettled(Delivery::TimedOut))]
    );
    assert_eq!(ids_held(&memory), [run_report(1, false).external_id]);
}

// ------------------------------------- "empty", as shutdown judges it

fn summary(root: &str) -> HoldSummary {
    HoldSummary {
        root: uuid::Uuid::parse_str(root).unwrap(),
        session: None,
        facts: 0,
        gaps: 0,
        bytes: 0,
        stamps: Vec::new(),
        oldest: None,
        set_aside: 0,
        unreadable: false,
        bad_tail: false,
    }
}

// Owner, 2026-11-16 ("flush before install"): a hold's live records are
// undelivered, a fact or a gap alike (a flush delivers both); a hold this
// ctxloom cannot read is undelivered too, its contents unknown.
#[test]
fn a_holds_live_records_are_undelivered() {
    let facts = HoldSummary {
        session: Some(NAME.to_string()),
        facts: 3,
        ..summary(SESSION_ME)
    };
    let gaps = HoldSummary {
        gaps: 1,
        ..summary(SESSION_OTHER)
    };
    let unreadable = HoldSummary {
        unreadable: true,
        bytes: 12,
        ..summary("00000000-0000-0000-0000-000000000003")
    };
    assert_eq!(
        undelivered(&[facts, gaps, unreadable]),
        vec![
            Undelivered {
                root: root(),
                session: Some(NAME.to_string()),
                facts: 3,
                gaps: 0,
                unreadable: false,
            },
            Undelivered {
                root: uuid::Uuid::parse_str(SESSION_OTHER).unwrap(),
                session: None,
                facts: 0,
                gaps: 1,
                unreadable: false,
            },
            Undelivered {
                root: uuid::Uuid::parse_str("00000000-0000-0000-0000-000000000003").unwrap(),
                session: None,
                facts: 0,
                gaps: 0,
                unreadable: true,
            },
        ]
    );
}

// A set-aside part is never delivered again, so it holds nothing a daemon
// will decode: it is empty, and so is a torn tail with no record before it
// (the next take cuts it).
#[test]
fn a_set_aside_part_or_a_torn_tail_alone_is_empty() {
    let aside = HoldSummary {
        set_aside: 2,
        ..summary(SESSION_ME)
    };
    let torn = HoldSummary {
        bad_tail: true,
        bytes: 20,
        ..summary(SESSION_OTHER)
    };
    assert_eq!(undelivered(&[aside, torn]), Vec::<Undelivered>::new());
    // Beside live records, a set-aside part changes nothing of the count.
    let both = HoldSummary {
        set_aside: 1,
        facts: 2,
        ..summary(SESSION_ME)
    };
    assert_eq!(undelivered(&[both])[0].facts, 2);
}
