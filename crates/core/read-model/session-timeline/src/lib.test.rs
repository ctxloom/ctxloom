//! The timeline folded from session and approvals pages: lifecycle words,
//! asks, transcript lines, all in time order.

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

use crate::{Content, Entry, TimelineIndex};

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

fn daemon(entries: &[Entry]) -> Vec<(String, String)> {
    entries
        .iter()
        .filter_map(|entry| match &entry.content {
            Content::Daemon { event, text } => Some((event.clone(), text.clone())),
            Content::Transcript { .. } => None,
        })
        .collect()
}

fn words(entries: &[Entry]) -> Vec<String> {
    entries
        .iter()
        .map(|entry| match &entry.content {
            Content::Daemon { event, .. } => event.clone(),
            Content::Transcript { .. } => "line".to_string(),
        })
        .collect()
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
    assert_eq!(
        daemon(&entries).first().map(|e| e.0.as_str()),
        Some("started")
    );
    assert_eq!(entries[0].time, at(100));
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
            ],
        ))
        .unwrap();
    let said = daemon(&timeline.entries(&session_id(1)));
    assert_eq!(
        said.iter().map(|e| e.0.as_str()).collect::<Vec<_>>(),
        ["started", "exited", "rehosted", "restarted"]
    );
    assert!(said[1].1.contains("runner-exit"), "{said:?}");
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
    assert_eq!(words(&entries), ["started", "line", "exited"]);
    let Content::Transcript { line } = &entries[1].content else {
        panic!("{entries:?}")
    };
    assert!(line.contains("review the migration"), "{line}");
    assert!(!line.ends_with('\n'), "{line:?}");
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
    let said = daemon(&timeline.entries(&session_id(1)));
    assert_eq!(said[1].0, "approval_asked");
    assert!(
        said[1].1.contains("Bash") && said[1].1.contains("rm -rf build/"),
        "{said:?}"
    );
    assert_eq!(said[2].0, "approval_settled");
    assert!(said[2].1.contains("allow-once"), "{said:?}");
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
        let said = daemon(&timeline.entries(&session_id(1)));
        assert!(said[1].1.contains(word), "{said:?}");
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
    assert_eq!(words(&entries), ["started", "approval_asked", "exited"]);
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
