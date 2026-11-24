//! Hook capture (PROPOSAL, design/hook-capture): the unified hook event as
//! typed facts of the session, their idempotency keys, and the hook-side
//! delivery policy. Owner rulings 2026-11-08: every MessageDisplay delta is
//! recorded; a tool result is size and digest only; a failed delivery is
//! dropped and reported; thinking blocks are not captured.

use std::collections::{BTreeSet, VecDeque};
use std::sync::Mutex;
use std::time::Duration;

use angzarr_embedded::v1::{command_page, CommandBook};
use angzarr_embedded::Payload;
use run_ports::{Arrival, Delivery, Edge, FactSink};
use sha2::{Digest, Sha256};
use surface_ports::{HookEvent, MessageDisplay, ToolCall, ToolStatus};

use crate::fakes::{uuid, World, SESSION_ME, SESSION_OTHER};
use crate::{delivery_budget, no_such_session, observe, Capture, Observation, Recorded};

const S: &str = SESSION_ME;

fn turn_start(prompt_id: &str, prompt: &str) -> HookEvent {
    HookEvent {
        event: "turn_start".to_string(),
        prompt_id: prompt_id.to_string(),
        prompt: prompt.to_string(),
        ..HookEvent::default()
    }
}

fn delta(message_id: &str, index: u32, is_final: bool, text: &str) -> HookEvent {
    HookEvent {
        event: "message_display".to_string(),
        prompt_id: "p1".to_string(),
        message: Some(MessageDisplay {
            turn_id: "t1".to_string(),
            message_id: message_id.to_string(),
            index,
            is_final,
            delta: text.to_string(),
            ..Default::default()
        }),
        ..HookEvent::default()
    }
}

fn batch(calls: &[(&str, &str, &str, &str)]) -> HookEvent {
    HookEvent {
        event: "post_tool_batch".to_string(),
        prompt_id: "p1".to_string(),
        tool_calls: calls
            .iter()
            .map(|(id, tool, input, response)| ToolCall {
                tool: tool.to_string(),
                tool_use_id: id.to_string(),
                input: input.to_string(),
                response: response.to_string(),
                ..ToolCall::default()
            })
            .collect(),
        ..HookEvent::default()
    }
}

fn turn_end() -> HookEvent {
    HookEvent {
        event: "turn_end".to_string(),
        prompt_id: "p1".to_string(),
        last_assistant_message: "all done".to_string(),
        ..HookEvent::default()
    }
}

fn session_end() -> HookEvent {
    HookEvent {
        event: "session_end".to_string(),
        native_session: "n1".to_string(),
        prompt_id: "p9".to_string(),
        reason: "prompt_input_exit".to_string(),
        ..HookEvent::default()
    }
}

fn compaction(event: &str, prompt_id: &str) -> HookEvent {
    HookEvent {
        event: event.to_string(),
        native_session: "n1".to_string(),
        prompt_id: prompt_id.to_string(),
        trigger: "manual".to_string(),
        ..HookEvent::default()
    }
}

fn ids(events: &[HookEvent]) -> Vec<String> {
    let session = uuid(S);
    events
        .iter()
        .flat_map(observe)
        .map(|o| o.external_id(&session))
        .collect()
}

// ----------------------------------------------------------------- observe

#[test]
fn a_submitted_prompt_is_one_observation() {
    let observed = observe(&turn_start("p1", "echo hi"));
    assert_eq!(
        observed,
        [Observation::PromptSubmitted(
            sessions::RecordPromptSubmitted {
                prompt_id: "p1".to_string(),
                prompt: "echo hi".to_string(),
                ..Default::default()
            }
        )]
    );
}

#[test]
fn every_message_display_delta_is_its_own_observation() {
    let observed = observe(&delta("m1", 2, true, "last piece"));
    assert_eq!(
        observed,
        [Observation::AssistantText(sessions::RecordAssistantText {
            prompt_id: "p1".to_string(),
            turn_id: "t1".to_string(),
            message_id: "m1".to_string(),
            index: 2,
            is_final: true,
            text: "last piece".to_string(),
            ..Default::default()
        })]
    );
    let first = observe(&delta("m1", 0, false, "first"));
    let [Observation::AssistantText(first)] = first.as_slice() else {
        panic!("one text fact, not {first:?}");
    };
    assert!(!first.is_final, "only the last piece is flagged final");
}

#[test]
fn a_tool_batch_is_one_observation_per_call_in_call_order() {
    let observed = observe(&batch(&[
        ("toolu_a", "Bash", r#"{"command":"echo hi"}"#, "hi"),
        ("toolu_b", "Read", r#"{"file_path":"/x"}"#, "contents of x"),
    ]));
    let tools: Vec<&str> = observed
        .iter()
        .map(|o| match o {
            Observation::ToolCall(t) => t.tool_use_id.as_str(),
            other => panic!("not a tool call: {other:?}"),
        })
        .collect();
    assert_eq!(tools, ["toolu_a", "toolu_b"]);
}

// Ruling 58: how a call ended reaches its fact, whatever the engine said.
#[test]
fn a_tool_calls_status_is_carried_to_its_fact() {
    let cases = [
        (
            ToolStatus::Completed,
            Some(sessions::ToolCallStatus::Completed),
        ),
        (ToolStatus::Error, Some(sessions::ToolCallStatus::Error)),
        (ToolStatus::Unspecified, None),
    ];
    for (said, recorded) in cases {
        let mut event = batch(&[("prt_a", "bash", "{}", "denied")]);
        event.tool_calls[0].status = said;
        let observed = observe(&event);
        let [Observation::ToolCall(call)] = observed.as_slice() else {
            panic!("one tool call, not {observed:?}");
        };
        assert_eq!(call.status, recorded, "{said:?}");
    }
}

// Ruling B: input whole; the result as size and digest, never its body.
#[test]
fn a_tool_result_is_recorded_as_size_and_digest_only() {
    let response = "hi\u{e9}\nthere";
    let observed = observe(&batch(&[(
        "toolu_a",
        "Bash",
        r#"{"command":"echo hi"}"#,
        response,
    )]));
    let [Observation::ToolCall(call)] = observed.as_slice() else {
        panic!("one tool call, not {observed:?}");
    };
    assert_eq!(call.tool, "Bash");
    assert_eq!(call.input, "", "the input's text is not recorded either");
    assert_eq!(
        call.response_bytes,
        response.len() as u64,
        "bytes, not chars"
    );
    assert_eq!(
        call.response_sha256,
        format!("{:x}", Sha256::digest(response.as_bytes()))
    );
    assert!(
        !format!("{call:?}").contains("there"),
        "the result's body is nowhere in the fact"
    );
}

// Ruling A: Stop's closing text is not a second record of what the deltas
// already carried; the turn's end carries no text.
#[test]
fn a_turn_end_is_one_observation_without_text() {
    assert_eq!(
        observe(&turn_end()),
        [Observation::TurnEnded(sessions::RecordTurnEnded {
            prompt_id: "p1".to_string(),
            ..Default::default()
        })]
    );
}

#[test]
fn a_compaction_is_before_or_after_by_the_event() {
    assert_eq!(
        observe(&compaction("compaction_before", "p1")),
        [Observation::Compaction(sessions::RecordCompaction {
            prompt_id: "p1".to_string(),
            phase: sessions::CompactionPhase::Before,
            trigger: "manual".to_string(),
            summary_bytes: 0,
            summary_sha256: String::new(),
            extra: Default::default()
        })]
    );
    // PostCompact carries the summary: its size and digest are recorded and
    // its text is not (ruling B). sha256("abc") is the well-known digest.
    let mut after = compaction("compaction_after", "p1");
    after.compact_summary = "abc".to_string();
    let observed = observe(&after);
    assert_eq!(
        observed,
        [Observation::Compaction(sessions::RecordCompaction {
            prompt_id: "p1".to_string(),
            phase: sessions::CompactionPhase::After,
            trigger: "manual".to_string(),
            summary_bytes: 3,
            summary_sha256: "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
                .to_string(),
            extra: Default::default()
        })]
    );
    assert!(!format!("{observed:?}").contains("abc"));
}

// A compaction is matched on the unified event, as every other observation
// is, so any engine's codec can produce one: an engine's native names (claude's
// PreCompact, PostCompact) reach observe only through its codec, and alone hold
// nothing.
#[test]
fn a_compaction_is_its_unified_event_and_a_native_name_holds_nothing() {
    for native in ["PreCompact", "PostCompact"] {
        assert_eq!(observe(&compaction(native, "p1")), [], "{native}");
    }
    let phases: Vec<_> = ["compaction_before", "compaction_after"]
        .iter()
        .flat_map(|event| observe(&compaction(event, "p1")))
        .map(|o| match o {
            Observation::Compaction(c) => c.phase,
            other => panic!("not a compaction: {other:?}"),
        })
        .collect();
    assert_eq!(
        phases,
        [
            sessions::CompactionPhase::Before,
            sessions::CompactionPhase::After
        ]
    );
}

// Live probe (2026-11-09): two compactions of one turn used to share an id.
// The trigger and the phase tell them apart, as does the prompt.
#[test]
fn two_compactions_of_one_turn_have_two_ids() {
    let with = |native: &str, prompt: &str, trigger: &str| HookEvent {
        trigger: trigger.to_string(),
        ..compaction(native, prompt)
    };
    let all = [
        with("compaction_before", "p1", "manual"),
        with("compaction_after", "p1", "manual"),
        with("compaction_before", "p1", "auto"),
        with("compaction_after", "p1", "auto"),
        with("compaction_before", "p2", "manual"),
        with("compaction_before", "p3", ""),
    ];
    assert_eq!(
        ids(&all),
        [
            format!("hook:compact:{S}:p1:manual:before"),
            format!("hook:compact:{S}:p1:manual:after"),
            format!("hook:compact:{S}:p1:auto:before"),
            format!("hook:compact:{S}:p1:auto:after"),
            format!("hook:compact:{S}:p2:manual:before"),
            format!("hook:compact:{S}:p3:unknown:before"),
        ]
    );
}

// A PostToolUse and a PostToolBatch both fire for one call: only the batch
// reports it, so a call is never recorded twice. (The bind is SessionStart's
// observation, below.)
#[test]
fn events_that_observe_nothing_hold_no_observation() {
    for event in ["post_tool", "post_file_edit", "pre_tool", "permission_ask"] {
        let held = observe(&HookEvent {
            event: event.to_string(),
            tool: "Bash".to_string(),
            ..HookEvent::default()
        });
        assert!(held.is_empty(), "{event}: {held:?}");
    }
}

// Owner 2026-11-15 (SessionEnded is lost entirely): a session's end
// observes nothing. An older home that still registers
// SessionEnd is answered, and records nothing.
#[test]
fn a_session_end_observes_nothing() {
    assert!(observe(&session_end()).is_empty());
}

fn session_start(source: &str, native: &str) -> HookEvent {
    HookEvent {
        event: "session_start".to_string(),
        native_session: native.to_string(),
        transcript: format!("/home/u/.claude/projects/p/{native}.jsonl"),
        source: source.to_string(),
        ..HookEvent::default()
    }
}

// Owner 2026-11-15 (H-Q9 folded in): SessionStart's bind of the
// engine's own conversation is an observation like the rest, delivered and
// held like any capture fact, keyed as `bind_book` keys it
// (`bind:<session>:<native_id>`), for every source: a startup, a resume and
// a compaction (the same id, so it records nothing new) alike.
#[test]
fn a_session_start_observes_its_conversations_bind() {
    for source in ["startup", "resume", "compact"] {
        assert_eq!(
            ids(&[session_start(source, "n1")]),
            [format!("bind:{S}:n1")],
            "{source}"
        );
    }
}

// Owner 2026-11-15 (clears captured, typed): a SessionStart
// whose source is `clear` observes the clear, keyed by the NEW conversation
// (`hook:clear:<session>:<native>`, so the driver's report of the same clear
// is one fact), and then the new conversation's bind, in that order.
#[test]
fn a_clear_observes_the_clear_then_the_new_conversations_bind() {
    assert_eq!(
        ids(&[session_start("clear", "n2")]),
        [format!("hook:clear:{S}:n2"), format!("bind:{S}:n2")]
    );
}

// ------------------------------------------------------------ external ids

#[test]
fn an_external_id_spells_its_kind_session_and_cause() {
    let all = [
        turn_start("p1", "x"),
        delta("m1", 3, false, "x"),
        batch(&[("toolu_a", "Bash", "{}", "")]),
        turn_end(),
        session_start("clear", "n1"),
        compaction("compaction_before", "p1"),
        compaction("compaction_after", "p1"),
    ];
    assert_eq!(
        ids(&all),
        [
            format!("hook:prompt:{S}:p1"),
            format!("hook:text:{S}:m1:3"),
            format!("hook:tool:{S}:toolu_a"),
            format!("hook:turn-end:{S}:p1"),
            format!("hook:clear:{S}:n1"),
            format!("bind:{S}:n1"),
            format!("hook:compact:{S}:p1:manual:before"),
            format!("hook:compact:{S}:p1:manual:after"),
        ]
    );
}

#[test]
fn the_same_event_twice_has_the_same_ids() {
    let event = batch(&[("a", "Bash", "{}", "x"), ("b", "Bash", "{}", "y")]);
    assert_eq!(
        ids(std::slice::from_ref(&event)),
        ids(std::slice::from_ref(&event))
    );
}

// The id is the cause alone: what the event says besides it does not enter.
#[test]
fn an_id_does_not_depend_on_the_payload_beyond_its_cause() {
    assert_eq!(
        ids(&[delta("m1", 0, false, "one text")]),
        ids(&[delta("m1", 0, true, "another text")])
    );
    assert_eq!(ids(&[turn_start("p1", "a")]), ids(&[turn_start("p1", "b")]));
}

// Collision-free: every distinct cause, across kinds, has its own id; the
// same cause in another session has another.
#[test]
fn distinct_causes_never_share_an_id() {
    let mut events = vec![session_start("clear", "n1")];
    for p in ["p1", "p2"] {
        events.push(turn_start(p, "x"));
        events.push(compaction("compaction_before", p));
        events.push(compaction("compaction_after", p));
        events.push(HookEvent {
            prompt_id: p.to_string(),
            ..turn_end()
        });
    }
    for m in ["m1", "m2"] {
        for i in 0..3 {
            events.push(delta(m, i, i == 2, "x"));
        }
    }
    events.push(batch(&[("t1", "Bash", "{}", ""), ("t2", "Bash", "{}", "")]));
    let all = ids(&events);
    let distinct: BTreeSet<&String> = all.iter().collect();
    assert_eq!(distinct.len(), all.len(), "{all:?}");
    let other = observe(&turn_start("p1", "x"))[0].external_id(&uuid(SESSION_OTHER));
    assert!(!all.contains(&other));
}

// A kind never collides with another that spells the same cause.
#[test]
fn the_same_cause_in_two_kinds_has_two_ids() {
    let prompt = observe(&turn_start("same", "x"))[0].external_id(&uuid(S));
    let end = observe(&HookEvent {
        prompt_id: "same".to_string(),
        ..turn_end()
    })[0]
        .external_id(&uuid(S));
    assert_ne!(prompt, end);
}

// -------------------------------------------------------------------- book

#[test]
fn an_observation_is_a_fact_on_the_session_stream_keyed_by_its_id() {
    let session = uuid(S);
    let observation = &observe(&turn_start("p1", "x"))[0];
    let book = observation.book(&session);
    let cover = book.cover.clone().unwrap();
    assert_eq!(cover.domain, sessions::SESSION_DOMAIN);
    assert_eq!(cover.root, Some(session.clone()));
    let Some(command_page::Body::Fact(fact)) = book.page.unwrap().body else {
        panic!("a fact body");
    };
    assert_eq!(
        fact.origin.unwrap().external_id,
        observation.external_id(&session)
    );
    assert_eq!(fact.payload.unwrap().type_url, observation.type_url());
}

// ---------------------------------------------------------- delivery policy

#[test]
fn the_delivery_budget_is_under_the_engines_hook_timeout() {
    assert!(delivery_budget("UserPromptSubmit") < Duration::from_secs(30));
    assert!(delivery_budget("MessageDisplay") < Duration::from_secs(10));
    for native in [
        "UserPromptSubmit",
        "MessageDisplay",
        "Stop",
        "PostToolBatch",
        "PreCompact",
    ] {
        assert!(delivery_budget(native) > Duration::ZERO, "{native}");
    }
}

/// A sink that answers from a script (then `Applied`), keeping what it was
/// given.
#[derive(Default)]
struct Sink {
    script: Mutex<VecDeque<Delivery>>,
    got: Mutex<Vec<(CommandBook, Duration)>>,
}

impl Sink {
    fn answering(script: impl IntoIterator<Item = Delivery>) -> Sink {
        Sink {
            script: Mutex::new(script.into_iter().collect()),
            ..Sink::default()
        }
    }
}

impl FactSink for &Sink {
    fn deliver(&self, book: CommandBook, within: Duration) -> Delivery {
        self.got.lock().unwrap().push((book, within));
        self.script
            .lock()
            .unwrap()
            .pop_front()
            .unwrap_or(Delivery::Applied)
    }
}

fn external_ids(sink: &Sink) -> Vec<String> {
    sink.got
        .lock()
        .unwrap()
        .iter()
        .map(|(book, _)| match &book.page.as_ref().unwrap().body {
            Some(command_page::Body::Fact(f)) => f.origin.as_ref().unwrap().external_id.clone(),
            other => panic!("not a fact: {other:?}"),
        })
        .collect()
}

const BUDGET: Duration = Duration::from_secs(2);

fn three_calls() -> HookEvent {
    batch(&[
        ("a", "Bash", "{}", ""),
        ("b", "Bash", "{}", ""),
        ("c", "Bash", "{}", ""),
    ])
}

#[test]
fn capture_delivers_every_observation_in_order_within_the_budget() {
    let sink = Sink::default();
    let report = Capture::new(&sink).record(&uuid(S), &three_calls(), BUDGET);
    assert_eq!(
        external_ids(&sink),
        [
            format!("hook:tool:{S}:a"),
            format!("hook:tool:{S}:b"),
            format!("hook:tool:{S}:c")
        ]
    );
    assert!(sink
        .got
        .lock()
        .unwrap()
        .iter()
        .all(|(_, within)| *within <= BUDGET));
    assert_eq!(report.attempts.len(), 3);
    assert!(report
        .attempts
        .iter()
        .all(|a| a.delivery == Some(Delivery::Applied)));
    assert!(report.unrecorded().is_empty());
}

#[test]
fn a_duplicate_is_recorded_not_a_failure() {
    let sink = Sink::answering([Delivery::Duplicate]);
    let report = Capture::new(&sink).record(&uuid(S), &turn_start("p1", "x"), BUDGET);
    assert!(report.unrecorded().is_empty());
}

#[test]
fn an_unavailable_sink_is_tried_once_and_the_rest_are_dropped_unattempted() {
    let sink = Sink::answering([Delivery::Unavailable {
        detail: "connection refused".to_string(),
    }]);
    let report = Capture::new(&sink).record(&uuid(S), &three_calls(), BUDGET);
    assert_eq!(
        sink.got.lock().unwrap().len(),
        1,
        "the budget is shared, not multiplied"
    );
    assert_eq!(report.attempts.len(), 3);
    assert_eq!(report.unrecorded().len(), 3);
    assert!(report.attempts[1..].iter().all(|a| a.delivery.is_none()));
}

#[test]
fn a_timeout_stops_the_remaining_deliveries_too() {
    let sink = Sink::answering([Delivery::Applied, Delivery::TimedOut]);
    let report = Capture::new(&sink).record(&uuid(S), &three_calls(), BUDGET);
    assert_eq!(sink.got.lock().unwrap().len(), 2);
    let unrecorded: Vec<&str> = report
        .unrecorded()
        .iter()
        .map(|a| a.external_id.as_str())
        .collect();
    assert_eq!(
        unrecorded,
        [format!("hook:tool:{S}:b"), format!("hook:tool:{S}:c")]
    );
}

// A refused or parked fact is final for that fact, not a sign the sink is
// gone: the rest are still delivered.
#[test]
fn a_refusal_of_one_fact_does_not_stop_the_others() {
    let sink = Sink::answering([
        Delivery::Refused {
            detail: "not allowlisted".to_string(),
        },
        Delivery::Parked,
    ]);
    let report = Capture::new(&sink).record(&uuid(S), &three_calls(), BUDGET);
    assert_eq!(sink.got.lock().unwrap().len(), 3);
    assert!(
        report.unrecorded().is_empty(),
        "settled, so nothing to retry"
    );
}

#[test]
fn an_event_that_observes_nothing_touches_no_sink() {
    let sink = Sink::default();
    let report = Capture::new(&sink).record(&uuid(S), &HookEvent::default(), BUDGET);
    assert!(sink.got.lock().unwrap().is_empty());
    assert!(report.attempts.is_empty());
}

// ------------------------------------------------- through the real kernel

#[tokio::test]
async fn delivered_to_the_kernel_the_observations_become_events_and_a_redelivery_is_a_duplicate() {
    let world = World::new().await;
    let session = world.me.identity.session.clone();
    let cover = world.me.identity.session_cover();
    let before = world.edge.pages(&cover).await.len();

    let sink = Sink::default();
    let event = batch(&[("a", "Bash", "{}", "x")]);
    Capture::new(&sink).record(&session, &turn_start("p1", "echo hi"), BUDGET);
    Capture::new(&sink).record(&session, &event, BUDGET);
    let books: Vec<CommandBook> = sink
        .got
        .lock()
        .unwrap()
        .iter()
        .map(|(b, _)| b.clone())
        .collect();
    assert_eq!(books.len(), 2);

    for book in &books {
        let answered = world
            .edge
            .serve(book.clone(), Arrival::Loopback)
            .await
            .unwrap();
        assert_eq!(Recorded::of(&answered), Some(Recorded::Applied));
    }
    let pages = world.edge.pages(&cover).await;
    assert_eq!(pages.len(), before + 2);
    let recorded: Vec<sessions::SessionEvent> = pages[before..]
        .iter()
        .map(|page| match &page.body {
            Some(angzarr_embedded::v1::event_page::Body::Event(any)) => {
                sessions::SessionEvent::from_any(any).unwrap()
            }
            other => panic!("{other:?}"),
        })
        .collect();
    assert!(matches!(
        recorded[0],
        sessions::SessionEvent::PromptSubmitted(_)
    ));
    assert!(matches!(recorded[1], sessions::SessionEvent::ToolCalled(_)));

    // The hook fires again (the engine retried): nothing more is written.
    for book in &books {
        let answered = world
            .edge
            .serve(book.clone(), Arrival::Loopback)
            .await
            .unwrap();
        assert_eq!(Recorded::of(&answered), Some(Recorded::Duplicate));
    }
    assert_eq!(world.edge.pages(&cover).await.len(), before + 2);
}

// Each native event's budget is exact: the long one the engine gives a prompt,
// the middle one a streamed piece, and
// the default for the rest.
#[test]
fn each_native_events_delivery_budget_is_exact() {
    for (native, seconds) in [
        ("UserPromptSubmit", 20),
        ("MessageDisplay", 5),
        ("Stop", 8),
        ("PostToolBatch", 8),
        ("PreCompact", 8),
        ("something-new", 8),
    ] {
        assert_eq!(
            delivery_budget(native),
            Duration::from_secs(seconds),
            "{native}"
        );
    }
}

// s4-Q14, H3: a hold's gap record, delivered through the real kernel, is
// recorded as the gap event on the session's stream, once: a redelivery is
// a DUPLICATE.
#[tokio::test]
async fn a_gap_delivered_to_the_kernel_is_recorded_once() {
    let world = World::new().await;
    let session = world.me.identity.session.clone();
    let cover = world.me.identity.session_cover();
    let before = world.edge.pages(&cover).await.len();
    let gap = Observation::gap(&run_ports::HeldGap {
        stamp: "v0.2.0-aaaaaaa".to_string(),
        external_id: "hook:turn-end:s:p1".to_string(),
        held_at: Some(daemon_proto::google::protobuf::Timestamp {
            seconds: 1_700_000_050,
            nanos: 0,
        }),
        why: "bound".to_string(),
    });
    assert_eq!(gap.external_id(&session), "hook:gap:hook:turn-end:s:p1");
    for expected in [Recorded::Applied, Recorded::Duplicate] {
        let answered = world
            .edge
            .serve(gap.book(&session), Arrival::Loopback)
            .await
            .unwrap();
        assert_eq!(Recorded::of(&answered), Some(expected));
    }
    let pages = world.edge.pages(&cover).await;
    assert_eq!(pages.len(), before + 1);
    let Some(angzarr_embedded::v1::event_page::Body::Event(any)) = &pages[before].body else {
        panic!("an event");
    };
    let sessions::SessionEvent::CaptureGapped(recorded) =
        sessions::SessionEvent::from_any(any).unwrap()
    else {
        panic!("the gap event");
    };
    assert_eq!(recorded.dropped, "hook:turn-end:s:p1");
    assert_eq!(recorded.why, "bound");
    assert_eq!(recorded.dropped_at.seconds, 1_700_000_050);
}

// A gap record with no time is recorded at the epoch: it still says what
// was dropped, and why.
#[test]
fn a_gap_with_no_time_is_dropped_at_the_epoch() {
    let Observation::CaptureGap(gap) = Observation::gap(&run_ports::HeldGap {
        stamp: String::new(),
        external_id: "x".to_string(),
        held_at: None,
        why: "disk".to_string(),
    }) else {
        panic!("a gap");
    };
    assert_eq!(gap.dropped_at, angzarr_embedded::Timestamp::default());
    assert_eq!(gap.why, "disk");
}

// s4-Q14, H3 follow-up: a capture fact the kernel applied with no event was
// addressed to a session the daemon has no record of; anything else is not.
#[test]
fn a_capture_fact_applied_with_no_event_had_no_session() {
    use angzarr_embedded::v1::DeliveryOutcome;
    let session = uuid(S);
    let fact = observe(&turn_start("p1", "echo hi"))[0].book(&session);
    let gap = Observation::gap(&run_ports::HeldGap {
        stamp: String::new(),
        external_id: "x".to_string(),
        held_at: None,
        why: "bound".to_string(),
    })
    .book(&session);
    let answered = |delivery, events: usize| angzarr_embedded::Answered {
        delivery,
        events: Some(angzarr_embedded::v1::EventBook {
            pages: vec![angzarr_embedded::v1::EventPage::default(); events],
            ..angzarr_embedded::v1::EventBook::default()
        }),
    };
    let nothing = angzarr_embedded::Answered {
        delivery: DeliveryOutcome::Applied,
        events: None,
    };
    assert!(no_such_session(&fact, &nothing), "no event book at all");
    assert!(no_such_session(
        &fact,
        &answered(DeliveryOutcome::Applied, 0)
    ));
    assert!(no_such_session(
        &gap,
        &answered(DeliveryOutcome::Applied, 0)
    ));
    assert!(!no_such_session(
        &fact,
        &answered(DeliveryOutcome::Applied, 1)
    ));
    assert!(!no_such_session(
        &fact,
        &answered(DeliveryOutcome::Duplicate, 0)
    ));
    // A command, or a fact of no capture (a bind records nothing when it
    // changes nothing), is never read so.
    let command = crate::fakes::command_book(
        fact.cover.clone().unwrap(),
        sessions::SetKept { kept: true },
    );
    assert!(!no_such_session(
        &command,
        &answered(DeliveryOutcome::Applied, 0)
    ));
    let mut elsewhere = fact.clone();
    elsewhere.cover.as_mut().unwrap().domain = "runs".to_string();
    assert!(!no_such_session(
        &elsewhere,
        &answered(DeliveryOutcome::Applied, 0)
    ));
}

// The answer's reason key is the full name of the typed answer the protos
// declare: capture.proto, package ctxloom.sessions.v1, FactSessionUnknown.
#[test]
fn the_no_such_session_key_is_the_typed_answers_full_name() {
    let proto = include_str!("../../../../../proto/ctxloom/sessions/v1/capture.proto");
    let (package, name) = run_ports::NO_SUCH_SESSION.rsplit_once('.').unwrap();
    assert!(proto.contains(&format!("package {package};")), "{package}");
    assert!(proto.contains(&format!("\nmessage {name} {{")), "{name}");
}

// The engine extension point: what a codec attached to the event is on every
// fact it makes, a piece's or a call's own laid over it, and the common
// fields are untouched.
fn extra(pairs: &[(&str, &str)]) -> std::collections::BTreeMap<String, String> {
    pairs
        .iter()
        .map(|(k, v)| (k.to_string(), v.to_string()))
        .collect()
}

#[test]
fn test_observe_carries_the_codecs_engine_data_onto_every_fact() {
    let mut prompt = turn_start("p1", "hi");
    prompt.extra = extra(&[("opencode.step", "1")]);
    let [Observation::PromptSubmitted(fact)] = &observe(&prompt)[..] else {
        panic!("one prompt fact");
    };
    assert_eq!(fact.extra, extra(&[("opencode.step", "1")]));
    assert_eq!(fact.prompt, "hi", "the common fields are untouched");

    let mut calls = batch(&[("t1", "Bash", "{}", "ok"), ("t2", "Read", "{}", "ok")]);
    calls.extra = extra(&[("opencode.step", "1"), ("opencode.part", "event")]);
    calls.tool_calls[1].extra = extra(&[("opencode.part", "own")]);
    let made = observe(&calls);
    let [Observation::ToolCall(first), Observation::ToolCall(second)] = &made[..] else {
        panic!("two tool facts");
    };
    assert_eq!(
        first.extra,
        extra(&[("opencode.step", "1"), ("opencode.part", "event")])
    );
    assert_eq!(
        second.extra,
        extra(&[("opencode.step", "1"), ("opencode.part", "own")]),
        "a call's own data wins over the event's"
    );

    let mut piece = delta("m1", 0, true, "done");
    piece.extra = extra(&[("opencode.step", "1")]);
    if let Some(message) = piece.message.as_mut() {
        message.extra = extra(&[("opencode.part_id", "prt_9")]);
    }
    let [Observation::AssistantText(text)] = &observe(&piece)[..] else {
        panic!("one text fact");
    };
    assert_eq!(
        text.extra,
        extra(&[("opencode.step", "1"), ("opencode.part_id", "prt_9")])
    );

    let mut end = turn_end();
    end.extra = extra(&[("opencode.step", "1")]);
    let [Observation::TurnEnded(ended)] = &observe(&end)[..] else {
        panic!("one end fact");
    };
    assert_eq!(ended.extra, extra(&[("opencode.step", "1")]));
}

// The owner's ruling of 2026-11-24: a tool call's input is recorded as its
// size and SHA-256, as its result is, and its text is never set.
#[test]
fn test_observe_records_a_tool_inputs_size_and_digest_and_not_its_text() {
    let input = r#"{"file_path":"/work/a.rs","content":"fn main() {}"}"#;
    let made = observe(&batch(&[("t1", "Write", input, "ok")]));
    let [Observation::ToolCall(call)] = &made[..] else {
        panic!("one tool fact");
    };
    assert_eq!(call.input, "", "the input's text is not recorded");
    assert_eq!(call.input_bytes, Some(input.len() as u64));
    assert_eq!(
        call.input_sha256.as_deref(),
        Some(format!("{:x}", Sha256::digest(input.as_bytes())).as_str())
    );
    assert_eq!(call.tool, "Write", "the tool is named");
    assert_eq!(call.response_bytes, 2);
}
