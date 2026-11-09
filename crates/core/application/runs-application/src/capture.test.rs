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
use surface_ports::{HookEvent, MessageDisplay, ToolCall};

use crate::fakes::{uuid, World, SESSION_ME, SESSION_OTHER};
use crate::{delivery_budget, observe, Allowlist, Capture, Observation, Recorded};

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

fn compaction(native: &str, prompt_id: &str) -> HookEvent {
    HookEvent {
        event: native.to_string(),
        native_session: "n1".to_string(),
        prompt_id: prompt_id.to_string(),
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
    assert_eq!(call.input, r#"{"command":"echo hi"}"#);
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
            prompt_id: "p1".to_string()
        })]
    );
}

#[test]
fn a_session_end_names_the_engine_session_and_the_reason() {
    assert_eq!(
        observe(&session_end()),
        [Observation::SessionEnded(sessions::RecordSessionEnded {
            native_session: "n1".to_string(),
            reason: "prompt_input_exit".to_string(),
        })]
    );
}

#[test]
fn a_compaction_is_before_or_after_by_the_event() {
    assert_eq!(
        observe(&compaction("PreCompact", "p1")),
        [Observation::Compaction(sessions::RecordCompaction {
            prompt_id: "p1".to_string(),
            phase: sessions::CompactionPhase::Before,
        })]
    );
    assert_eq!(
        observe(&compaction("PostCompact", "p1")),
        [Observation::Compaction(sessions::RecordCompaction {
            prompt_id: "p1".to_string(),
            phase: sessions::CompactionPhase::After,
        })]
    );
}

// A PostToolUse and a PostToolBatch both fire for one call: only the batch
// reports it, so a call is never recorded twice. The bind is SessionStart's
// own fact.
#[test]
fn events_that_observe_nothing_hold_no_observation() {
    for event in [
        "post_tool",
        "post_file_edit",
        "pre_tool",
        "session_start",
        "permission_ask",
    ] {
        let held = observe(&HookEvent {
            event: event.to_string(),
            tool: "Bash".to_string(),
            ..HookEvent::default()
        });
        assert!(held.is_empty(), "{event}: {held:?}");
    }
}

// ------------------------------------------------------------ external ids

#[test]
fn an_external_id_spells_its_kind_session_and_cause() {
    let all = [
        turn_start("p1", "x"),
        delta("m1", 3, false, "x"),
        batch(&[("toolu_a", "Bash", "{}", "")]),
        turn_end(),
        session_end(),
        compaction("PreCompact", "p1"),
        compaction("PostCompact", "p1"),
    ];
    assert_eq!(
        ids(&all),
        [
            format!("hook:prompt:{S}:p1"),
            format!("hook:text:{S}:m1:3"),
            format!("hook:tool:{S}:toolu_a"),
            format!("hook:turn-end:{S}:p1"),
            format!("hook:session-end:{S}:n1"),
            format!("hook:compact:{S}:p1:before"),
            format!("hook:compact:{S}:p1:after"),
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
    let mut events = vec![session_end()];
    for p in ["p1", "p2"] {
        events.push(turn_start(p, "x"));
        events.push(compaction("PreCompact", p));
        events.push(compaction("PostCompact", p));
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

// The hook is launched by the runner and holds the runner's credential: its
// facts are the runner's, and the model's credential cannot forge them.
#[test]
fn the_runner_may_record_observations_and_the_model_may_not() {
    let all = [
        turn_start("p1", "x"),
        delta("m1", 0, true, "x"),
        batch(&[("a", "Bash", "{}", "")]),
        turn_end(),
        session_end(),
        compaction("PreCompact", "p1"),
    ];
    for observation in all.iter().flat_map(observe) {
        let url = observation.type_url();
        assert!(Allowlist::runner().permits_fact(&url), "runner: {url}");
        assert!(!Allowlist::model().permits_fact(&url), "model: {url}");
    }
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
        "SessionEnd",
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
// the short one it gives a session end, the middle one a streamed piece, and
// the default for the rest.
#[test]
fn each_native_events_delivery_budget_is_exact() {
    for (native, seconds) in [
        ("UserPromptSubmit", 20),
        ("SessionEnd", 1),
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
