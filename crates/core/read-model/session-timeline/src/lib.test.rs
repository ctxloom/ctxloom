//! The timeline folded from session and approvals pages: typed log entries
//! (lifecycle, asks, the transcript's), all in time order.

use angzarr_embedded::angzarr_proto::v1::EventMeta;
use angzarr_embedded::v1::{event_page, Cover, EventBook, EventPage, Uuid};
use angzarr_embedded::{Payload, Timestamp};
use approvals::{
    claude_options, ApprovalAnswered, ApprovalAsked, ApprovalEvent, ApprovalExpired,
    ApprovalWithdrawn, OptionKind, SessionRef, APPROVALS_DOMAIN,
};
use sessions::{
    IncarnationEnded, IncarnationOwed, Origin, PromptSubmitted, SessionClosed, SessionEvent,
    SessionMinted, SESSION_DOMAIN,
};

use daemon_proto::v1::{log_ask, log_entry::Entry as Arm, log_lifecycle, LogEntry};

use crate::{Entry, TimelineIndex};

fn session_id(n: u8) -> String {
    uuid::Uuid::from_bytes([n; 16]).hyphenated().to_string()
}

fn at(seconds: i64) -> Timestamp {
    Timestamp { seconds, nanos: 0 }
}

fn book<E: Payload>(domain: &str, n: u8, first: u64, events: Vec<(i64, E)>) -> EventBook {
    let pages: Vec<EventPage> = (first..)
        .zip(events)
        .map(|(sequence, (seconds, event))| EventPage {
            sequence,
            body: Some(event_page::Body::Event(event.to_any())),
            meta: Some(EventMeta {
                recorded_at: Some(at(seconds)),
                ..EventMeta::default()
            }),
        })
        .collect();
    let next = pages.last().map_or(0, |page| page.sequence + 1);
    EventBook {
        cover: Some(Cover {
            domain: domain.to_string(),
            root: Some(Uuid::from_bytes([n; 16])),
            edition: None,
        }),
        pages,
        next_sequence: next,
        snapshot: None,
    }
}

fn minted() -> SessionEvent {
    SessionEvent::SessionMinted(SessionMinted {
        harp: "brisk-copper-moth".to_string(),
        project_id: "calm-otter".to_string(),
        project_dir: "/work/proj".to_string(),
        output_dir: "/out".to_string(),
        engine: "claude-code".to_string(),
        origin: Origin::Session,
        started_at: None,
        depth: 0,
        owner: None,
        workspace: None,
    })
}

fn prompt(text: &str) -> SessionEvent {
    SessionEvent::PromptSubmitted(PromptSubmitted {
        prompt_id: "p1".to_string(),
        prompt: text.to_string(),
        ..Default::default()
    })
}

fn owed(reason: &str) -> SessionEvent {
    SessionEvent::IncarnationOwed(IncarnationOwed {
        incarnation: 2,
        reason: reason.to_string(),
    })
}

fn ended(cause: &str) -> SessionEvent {
    SessionEvent::IncarnationEnded(IncarnationEnded {
        incarnation: 1,
        cause: cause.to_string(),
        notice_parent: false,
    })
}

/// What an entry is, in a word: a lifecycle's kind (`started`, `exited`,
/// ...), an ask's state (`asked`, `answered`, ...), else its arm.
fn word_of(entry: &Entry) -> String {
    match &entry.log.entry {
        Some(Arm::Lifecycle(l)) => log_lifecycle::Kind::try_from(l.kind)
            .map_or("?", |k| k.as_str_name())
            .to_lowercase(),
        Some(Arm::Ask(a)) => log_ask::State::try_from(a.state)
            .map_or("?", |s| s.as_str_name())
            .to_lowercase(),
        Some(Arm::Prompt(_)) => "prompt".to_string(),
        other => format!("{other:?}"),
    }
}

fn words(entries: &[Entry]) -> Vec<String> {
    entries.iter().map(word_of).collect()
}

fn lifecycle(entry: &Entry) -> &daemon_proto::v1::LogLifecycle {
    match &entry.log.entry {
        Some(Arm::Lifecycle(l)) => l,
        other => panic!("not a lifecycle entry: {other:?}"),
    }
}

fn ask_of(entry: &Entry) -> &daemon_proto::v1::LogAsk {
    match &entry.log.entry {
        Some(Arm::Ask(a)) => a,
        other => panic!("not an ask entry: {other:?}"),
    }
}

fn ask(child: u8) -> ApprovalEvent {
    ApprovalEvent::ApprovalAsked(ApprovalAsked {
        ask_id: "ask-a".to_string(),
        child: SessionRef {
            project_id: "p".to_string(),
            session: session_id(child),
        },
        run: "r-1".to_string(),
        engine: "claude-code".to_string(),
        root: SessionRef {
            project_id: "p".to_string(),
            session: session_id(child),
        },
        tool_name: "Bash".to_string(),
        input_excerpt: "rm -rf build/".to_string(),
        input_bytes: 13,
        input_sha256: "abc".to_string(),
        timeout_seconds: 300,
        engine_request_id: "toolu-a".to_string(),
        options: claude_options("Bash", "{}"),
        name: "projects/amber/approvals/ask-a".to_string(),
        child_name: "projects/amber/sessions/brisk".to_string(),
        root_name: "projects/amber/sessions/root".to_string(),
        agent: "default".to_string(),
    })
}

#[test]
fn a_session_that_started_says_started() {
    let mut timeline = TimelineIndex::default();
    timeline
        .project(&book(SESSION_DOMAIN, 1, 0, vec![(100, minted())]))
        .unwrap();
    let entries = timeline.entries(&session_id(1));
    assert_eq!(words(&entries), ["started"]);
    assert_eq!(lifecycle(&entries[0]).engine, "claude-code");
    assert_eq!(entries[0].time, at(100));
    // The entry is dated as its event, and its seq is the timeline's order.
    assert_eq!(entries[0].log.time.as_ref().map(|t| t.seconds), Some(100));
    assert_eq!(entries[0].log.seq, entries[0].n - 1);
}

#[test]
fn the_end_of_a_run_is_exited_with_its_cause_and_a_rehost_is_rehosted() {
    let mut timeline = TimelineIndex::default();
    timeline
        .project(&book(
            SESSION_DOMAIN,
            1,
            0,
            vec![
                (100, minted()),
                (110, ended("runner-exit")),
                (120, owed("rehost")),
                (130, owed("restart")),
                (140, owed("drain")),
            ],
        ))
        .unwrap();
    let said = timeline.entries(&session_id(1));
    assert_eq!(
        words(&said),
        ["started", "exited", "rehosted", "restarted", "rehosted"]
    );
    assert_eq!(lifecycle(&said[1]).cause, "runner-exit");
    assert_eq!(lifecycle(&said[1]).run, 1);
    assert_eq!(lifecycle(&said[2]).run, 2);
}

#[test]
fn a_prompt_is_a_transcript_line_between_the_events_around_it() {
    let mut timeline = TimelineIndex::default();
    timeline
        .project(&book(
            SESSION_DOMAIN,
            1,
            0,
            vec![
                (100, minted()),
                (105, prompt("review the migration")),
                (110, ended("runner-exit")),
            ],
        ))
        .unwrap();
    let entries = timeline.entries(&session_id(1));
    assert_eq!(words(&entries), ["started", "prompt", "exited"]);
    let Some(Arm::Prompt(prompt)) = &entries[1].log.entry else {
        panic!("{entries:?}")
    };
    assert_eq!(prompt.text, "review the migration");
}

#[test]
fn an_ask_and_its_settling_are_entries_of_the_asking_session() {
    let mut timeline = TimelineIndex::default();
    timeline
        .project(&book(SESSION_DOMAIN, 1, 0, vec![(100, minted())]))
        .unwrap();
    timeline
        .project(&book(APPROVALS_DOMAIN, 9, 0, vec![(120, ask(1))]))
        .unwrap();
    timeline
        .project(&book(
            APPROVALS_DOMAIN,
            9,
            1,
            vec![(
                130,
                ApprovalEvent::ApprovalAnswered(ApprovalAnswered {
                    option: "allow-once".to_string(),
                    kind: OptionKind::AllowOnce,
                    message: String::new(),
                }),
            )],
        ))
        .unwrap();
    let said = timeline.entries(&session_id(1));
    assert_eq!(words(&said), ["started", "asked", "answered"]);
    let asked = ask_of(&said[1]);
    assert_eq!(asked.ask, "ask-a");
    assert_eq!(asked.tool, "Bash");
    assert_eq!(asked.input_excerpt, "rm -rf build/");
    let settled = ask_of(&said[2]);
    assert_eq!(settled.ask, "ask-a");
    assert_eq!(settled.option, "allow-once");
    assert!(timeline.entries(&session_id(2)).is_empty());
}

#[test]
fn an_ask_that_expires_or_is_withdrawn_says_how() {
    for (settle, word) in [
        (
            ApprovalEvent::ApprovalExpired(ApprovalExpired {}),
            "expired",
        ),
        (
            ApprovalEvent::ApprovalWithdrawn(ApprovalWithdrawn {
                reason: "remote-cancel".to_string(),
            }),
            "withdrawn",
        ),
    ] {
        let mut timeline = TimelineIndex::default();
        timeline
            .project(&book(APPROVALS_DOMAIN, 9, 0, vec![(120, ask(1))]))
            .unwrap();
        timeline
            .project(&book(APPROVALS_DOMAIN, 9, 1, vec![(130, settle)]))
            .unwrap();
        let said = timeline.entries(&session_id(1));
        assert_eq!(word_of(&said[1]), word, "{said:?}");
        assert_eq!(ask_of(&said[1]).ask, "ask-a");
    }
}

#[test]
fn entries_are_in_time_order_whichever_stream_they_arrive_from() {
    let mut timeline = TimelineIndex::default();
    timeline
        .project(&book(
            SESSION_DOMAIN,
            1,
            0,
            vec![(100, minted()), (140, ended("x"))],
        ))
        .unwrap();
    timeline
        .project(&book(APPROVALS_DOMAIN, 9, 0, vec![(120, ask(1))]))
        .unwrap();
    let entries = timeline.entries(&session_id(1));
    assert_eq!(words(&entries), ["started", "asked", "exited"]);
    let times: Vec<i64> = entries.iter().map(|e| e.time.seconds).collect();
    assert!(times.windows(2).all(|pair| pair[0] <= pair[1]), "{times:?}");
}

#[test]
fn after_gives_what_arrived_since_the_last_told_entry() {
    let mut timeline = TimelineIndex::default();
    timeline
        .project(&book(SESSION_DOMAIN, 1, 0, vec![(100, minted())]))
        .unwrap();
    let told = timeline.entries(&session_id(1)).last().unwrap().n;
    timeline
        .project(&book(
            SESSION_DOMAIN,
            1,
            1,
            vec![(
                150,
                SessionEvent::SessionClosed(SessionClosed {
                    cause: "user".to_string(),
                }),
            )],
        ))
        .unwrap();
    let later = timeline.after(&session_id(1), told);
    assert_eq!(words(&later), ["closed"]);
    assert!(timeline.after(&session_id(1), later[0].n).is_empty());
}

#[test]
fn a_stream_purged_takes_its_session_out() {
    let mut timeline = TimelineIndex::default();
    timeline
        .project(&book(SESSION_DOMAIN, 1, 0, vec![(100, minted())]))
        .unwrap();
    let gone = EventBook {
        cover: Some(Cover {
            domain: SESSION_DOMAIN.to_string(),
            root: Some(Uuid::from_bytes([1; 16])),
            edition: None,
        }),
        pages: Vec::new(),
        next_sequence: 0,
        snapshot: None,
    };
    timeline.project(&gone).unwrap();
    assert!(timeline.entries(&session_id(1)).is_empty());
}

#[test]
fn only_the_newest_entries_of_a_session_are_kept() {
    let mut timeline = TimelineIndex::default();
    let many: Vec<(i64, SessionEvent)> = std::iter::once((1, minted()))
        .chain((0..crate::KEPT as i64 + 10).map(|i| (10 + i, prompt("x"))))
        .collect();
    timeline.project(&book(SESSION_DOMAIN, 1, 0, many)).unwrap();
    let entries = timeline.entries(&session_id(1));
    assert_eq!(entries.len(), crate::KEPT);
    assert_eq!(
        entries.last().unwrap().time.seconds,
        10 + crate::KEPT as i64 + 9
    );
}

#[test]
fn the_other_lifecycle_events_are_typed_with_their_run_and_cause() {
    let mut timeline = TimelineIndex::default();
    timeline
        .project(&book(
            SESSION_DOMAIN,
            1,
            0,
            vec![
                (100, minted()),
                (110, owed("resume")),
                (
                    120,
                    SessionEvent::RestartOwed(sessions::RestartOwed {
                        reason: sessions::RestartReason::Operator,
                        incarnation: 2,
                        note: String::new(),
                    }),
                ),
                (
                    130,
                    SessionEvent::SessionClosed(SessionClosed {
                        cause: "user".to_string(),
                    }),
                ),
                (
                    140,
                    SessionEvent::SessionReopened(sessions::SessionReopened {
                        cause: "resume".to_string(),
                    }),
                ),
                (
                    150,
                    SessionEvent::ChildLaunchFailed(sessions::ChildLaunchFailed {
                        reason: "no credential".to_string(),
                        ..Default::default()
                    }),
                ),
            ],
        ))
        .unwrap();
    let said = timeline.entries(&session_id(1));
    assert_eq!(
        words(&said),
        [
            "started",
            "resumed",
            "restart_owed",
            "closed",
            "reopened",
            "launch_failed"
        ]
    );
    assert_eq!(lifecycle(&said[1]).run, 2);
    assert_eq!(lifecycle(&said[2]).run, 2);
    assert_eq!(lifecycle(&said[3]).cause, "user");
    assert_eq!(lifecycle(&said[4]).cause, "resume");
    assert_eq!(lifecycle(&said[5]).cause, "no credential");
}

// The log is one session's: an entry names no session, harp or engine of
// its own (the started entry's engine is the fact it records, not a stamp).
#[test]
fn an_entry_carries_the_log_and_nothing_that_names_the_session() {
    let mut timeline = TimelineIndex::default();
    timeline
        .project(&book(
            SESSION_DOMAIN,
            1,
            0,
            vec![(100, minted()), (105, prompt("hi"))],
        ))
        .unwrap();
    let entries = timeline.entries(&session_id(1));
    let json: Vec<LogEntry> = entries.iter().map(|e| e.log.clone()).collect();
    let text = serde_json::to_string(&json[1]).unwrap();
    assert!(!text.contains("brisk-copper-moth"), "{text}");
    assert!(!text.contains("claude-code"), "{text}");
}
