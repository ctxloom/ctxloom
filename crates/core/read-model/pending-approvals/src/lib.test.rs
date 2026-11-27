//! The pending approvals (step 8 U4): an ask folded from its stream, listed
//! under its root's project while it waits, and answered as its resource.

use angzarr_embedded::angzarr_proto::v1::EventMeta;
use angzarr_embedded::angzarr_router::hpb::ProjectRequest;
use angzarr_embedded::testing::block_on_ready as ready;
use angzarr_embedded::v1::{event_page, Cover, EventBook, EventPage, Uuid};
use angzarr_embedded::{Payload, ProjectorHandler, Timestamp};
use approvals::{
    claude_options, ApprovalAnswered, ApprovalAsked, ApprovalEvent, ApprovalExpired,
    ApprovalWithdrawn, OptionKind, SessionRef, APPROVALS_DOMAIN,
};

use crate::{caught_up, ApprovalIndex, PendingApprovals, State};

fn reference(session: &str) -> SessionRef {
    SessionRef {
        project_id: "p-amber".to_string(),
        session: session.to_string(),
    }
}

fn asked(ask: &str, root: &str, timeout: u32) -> ApprovalEvent {
    ApprovalEvent::ApprovalAsked(ApprovalAsked {
        ask_id: ask.to_string(),
        child: reference("c-1"),
        run: "r-1".to_string(),
        engine: "claude-code".to_string(),
        root: reference("root-1"),
        tool_name: "Bash".to_string(),
        input_excerpt: r#"{"command":"rm -rf build/"}"#.to_string(),
        input_bytes: 26,
        input_sha256: "abc".to_string(),
        timeout_seconds: timeout,
        engine_request_id: format!("toolu-{ask}"),
        options: claude_options("Bash", r#"{"command":"rm -rf build/"}"#),
        name: format!("projects/amber/approvals/{ask}"),
        child_name: "projects/amber/sessions/brisk".to_string(),
        root_name: format!("projects/amber/sessions/{root}"),
        agent: "default".to_string(),
    })
}

/// The stream `n`: `events` from `first` on, each recorded at 1000 plus its
/// sequence plus `n`.
/// A book a purge's tombstone at `at` opens, then `events` after it.
fn after_tombstone(n: u8, domain: &str, at: u64, events: Vec<ApprovalEvent>) -> EventBook {
    let mut after = on(n, domain, at + 1, events);
    after
        .pages
        .insert(0, angzarr_embedded::angzarr_ports::tombstone::page(at));
    after.next_sequence = after.pages.last().map_or(at + 1, |p| p.sequence + 1);
    after
}

fn on(n: u8, domain: &str, first: u64, events: Vec<ApprovalEvent>) -> EventBook {
    let pages: Vec<EventPage> = (first..)
        .zip(events)
        .map(|(sequence, event)| EventPage {
            sequence,
            body: Some(event_page::Body::Event(event.to_any())),
            meta: Some(EventMeta {
                recorded_at: Some(Timestamp {
                    seconds: 1000 + sequence as i64 + i64::from(n),
                    nanos: 5,
                }),
                ..EventMeta::default()
            }),
        })
        .collect();
    let next = pages.last().map_or(0, |p| p.sequence + 1);
    EventBook {
        cover: Some(cover(n, domain)),
        pages,
        next_sequence: next,
        snapshot: None,
    }
}

fn cover(n: u8, domain: &str) -> Cover {
    Cover {
        domain: domain.to_string(),
        root: Some(Uuid::from_bytes([n; 16])),
        edition: None,
    }
}

fn uid(n: u8) -> String {
    uuid::Uuid::from_bytes([n; 16]).hyphenated().to_string()
}

#[test]
fn an_ask_is_listed_waiting_with_what_it_asks_and_when_it_runs_out() {
    let mut index = ApprovalIndex::new();
    index
        .project(&on(
            1,
            APPROVALS_DOMAIN,
            0,
            vec![asked("ask-a", "root", 300)],
        ))
        .unwrap();
    let row = index
        .named("projects/amber/approvals/ask-a")
        .expect("the ask");
    assert_eq!(row.uid, uid(1));
    assert_eq!(row.state, State::Waiting);
    assert_eq!(
        row.ask_time,
        Some(Timestamp {
            seconds: 1001,
            nanos: 5
        })
    );
    assert_eq!(
        row.expire_time(),
        Some(Timestamp {
            seconds: 1301,
            nanos: 5
        })
    );
    let resource = row.resource();
    assert_eq!(resource.name, "projects/amber/approvals/ask-a");
    assert_eq!(resource.child, "projects/amber/sessions/brisk");
    assert_eq!(resource.root, "projects/amber/sessions/root");
    assert_eq!(resource.engine, "claude-code");
    assert_eq!(resource.tool_name, "Bash");
    assert!(resource.input_excerpt.contains("rm -rf build/"));
    assert_eq!(resource.state, "waiting");
    assert_eq!(resource.uid, uid(1));
    assert_eq!(resource.timeout_seconds, 300);
    assert_eq!(resource.agent, "default");
    assert_eq!(resource.ask_time.map(|t| t.seconds), Some(1001));
    assert_eq!(resource.expire_time.map(|t| t.seconds), Some(1301));
    let options: Vec<(&str, &str, &str)> = resource
        .options
        .iter()
        .map(|o| (o.id.as_str(), o.kind.as_str(), o.rule.as_str()))
        .collect();
    assert_eq!(
        options,
        [
            ("allow-once", "allow_once", ""),
            ("allow-session", "allow_session", "Bash(rm -rf build/)"),
            ("allow-always", "allow_always", "Bash(rm -rf build/)"),
            ("deny", "reject_once", ""),
        ]
    );
    assert_eq!(index.by_uid(&uid(1)), Some(row.clone()));
    assert_eq!(index.asked_by("c-1", "toolu-ask-a"), Some(row));
    assert_eq!(index.asked_by("c-2", "toolu-ask-a"), None);
    assert_eq!(index.asked_by("c-1", "toolu-other"), None);
}

#[test]
fn an_answered_ask_says_its_option_and_message_and_waits_no_more() {
    let mut index = ApprovalIndex::new();
    index
        .project(&on(
            1,
            APPROVALS_DOMAIN,
            0,
            vec![
                asked("ask-a", "root", 300),
                ApprovalEvent::ApprovalAnswered(ApprovalAnswered {
                    option: "deny".to_string(),
                    kind: OptionKind::RejectOnce,
                    message: "not yet".to_string(),
                }),
            ],
        ))
        .unwrap();
    let row = index.by_uid(&uid(1)).unwrap();
    assert_eq!(row.state, State::Answered);
    assert_eq!(row.option, "deny");
    assert_eq!(row.option_kind, "reject_once");
    assert_eq!(row.message, "not yet");
    let resource = row.resource();
    assert_eq!(resource.state, "answered");
    assert_eq!(resource.option, "deny");
    assert_eq!(resource.option_kind, "reject_once");
    assert_eq!(resource.message, "not yet");
    assert_eq!(index.listed("projects/amber", None, true), vec![]);
    assert_eq!(index.listed("projects/amber", None, false).len(), 1);
}

#[test]
fn an_expired_or_withdrawn_ask_is_settled_so() {
    let mut index = ApprovalIndex::new();
    index
        .project(&on(
            1,
            APPROVALS_DOMAIN,
            0,
            vec![
                asked("ask-a", "root", 0),
                ApprovalEvent::ApprovalExpired(ApprovalExpired {}),
            ],
        ))
        .unwrap();
    index
        .project(&on(
            2,
            APPROVALS_DOMAIN,
            0,
            vec![asked("ask-b", "root", 300)],
        ))
        .unwrap();
    index
        .project(&on(
            2,
            APPROVALS_DOMAIN,
            1,
            vec![ApprovalEvent::ApprovalWithdrawn(ApprovalWithdrawn {
                reason: "r".to_string(),
            })],
        ))
        .unwrap();
    assert_eq!(index.by_uid(&uid(1)).unwrap().state, State::Expired);
    assert_eq!(index.by_uid(&uid(2)).unwrap().state, State::Withdrawn);
    assert_eq!(index.by_uid(&uid(1)).unwrap().resource().state, "expired");
    assert_eq!(index.by_uid(&uid(2)).unwrap().resource().state, "withdrawn");
    assert_eq!(index.listed("projects/amber", None, true), vec![]);
}

#[test]
fn the_listing_is_a_projects_asks_oldest_first_and_narrows_to_a_root() {
    let mut index = ApprovalIndex::new();
    index
        .project(&on(
            3,
            APPROVALS_DOMAIN,
            0,
            vec![asked("ask-c", "steady", 300)],
        ))
        .unwrap();
    index
        .project(&on(
            1,
            APPROVALS_DOMAIN,
            0,
            vec![asked("ask-b", "root", 300)],
        ))
        .unwrap();
    let mut elsewhere = asked("ask-z", "root", 300);
    if let ApprovalEvent::ApprovalAsked(a) = &mut elsewhere {
        a.name = "projects/birch/approvals/ask-z".to_string();
    }
    let other = on(2, APPROVALS_DOMAIN, 0, vec![elsewhere]);
    index.project(&other).unwrap();
    let names = |rows: Vec<crate::ApprovalRow>| -> Vec<String> {
        rows.into_iter().map(|r| r.name).collect()
    };
    assert_eq!(
        names(index.listed("projects/amber", None, true)),
        [
            "projects/amber/approvals/ask-b",
            "projects/amber/approvals/ask-c"
        ]
    );
    assert_eq!(
        names(index.listed(
            "projects/amber",
            Some("projects/amber/sessions/steady"),
            true
        )),
        ["projects/amber/approvals/ask-c"]
    );
    assert_eq!(
        names(index.listed("projects/birch", None, false)),
        ["projects/birch/approvals/ask-z"]
    );
    assert_eq!(
        names(index.listed("projects/amb", None, false)),
        Vec::<String>::new()
    );
}

#[test]
fn asks_asked_at_once_are_listed_by_name() {
    let mut index = ApprovalIndex::new();
    for (n, ask) in [(1, "ask-b"), (1, "ask-a")] {
        let mut book = on(n, APPROVALS_DOMAIN, 0, vec![asked(ask, "root", 300)]);
        book.cover = Some(cover(if ask == "ask-a" { 9 } else { 8 }, APPROVALS_DOMAIN));
        index.project(&book).unwrap();
    }
    let names: Vec<String> = index
        .listed("projects/amber", None, true)
        .into_iter()
        .map(|r| r.name)
        .collect();
    assert_eq!(
        names,
        [
            "projects/amber/approvals/ask-a",
            "projects/amber/approvals/ask-b"
        ]
    );
}

#[test]
fn a_tombstone_closes_the_stream_and_another_domains_is_ignored() {
    let mut index = ApprovalIndex::new();
    index
        .project(&on(
            1,
            APPROVALS_DOMAIN,
            0,
            vec![asked("ask-a", "root", 300)],
        ))
        .unwrap();
    index
        .project(&after_tombstone(1, APPROVALS_DOMAIN, 1, vec![]))
        .unwrap();
    assert_eq!(index.by_uid(&uid(1)), None);
    index
        .project(&on(1, "sessions", 0, vec![asked("ask-a", "root", 300)]))
        .unwrap();
    assert_eq!(index.by_uid(&uid(1)), None);
    // A settling event on a stream it holds no ask of changes nothing.
    index
        .project(&on(
            4,
            APPROVALS_DOMAIN,
            1,
            vec![
                ApprovalEvent::ApprovalExpired(ApprovalExpired {}),
                ApprovalEvent::ApprovalWithdrawn(ApprovalWithdrawn {
                    reason: String::new(),
                }),
                ApprovalEvent::ApprovalAnswered(ApprovalAnswered {
                    option: "deny".to_string(),
                    kind: OptionKind::RejectOnce,
                    message: String::new(),
                }),
            ],
        ))
        .unwrap();
    assert_eq!(index.by_uid(&uid(4)), None);
}

#[test]
fn a_state_is_worded_as_the_api_says_it() {
    assert_eq!(State::Waiting.word(), "waiting");
    assert_eq!(State::Answered.word(), "answered");
    assert_eq!(State::Expired.word(), "expired");
    assert_eq!(State::Withdrawn.word(), "withdrawn");
}

// S32: what the port answers for a stream is its ask as the resource
// message (`ctxloom.v1.Approval`).
#[test]
fn the_projection_is_the_streams_ask_as_its_resource() {
    use held_projection::Model as _;
    use prost::Message as _;
    let model = PendingApprovals::default();
    let view = ready(model.dispatch(&ProjectRequest {
        events: Some(on(
            1,
            APPROVALS_DOMAIN,
            0,
            vec![asked("ask-a", "root", 300)],
        )),
    }))
    .unwrap()
    .projection
    .expect("a view");
    assert_eq!(
        view.type_url,
        <daemon_proto::v1::Approval as prost::Name>::type_url()
    );
    let approval = daemon_proto::v1::Approval::decode(view.value.as_slice()).unwrap();
    assert_eq!(approval.name, "projects/amber/approvals/ask-a");
    assert_eq!(model.name(), "pending-approvals");
    assert_eq!(model.index().listed("projects/amber", None, true).len(), 1);
    assert!(ApprovalIndex::new()
        .view(&cover(1, APPROVALS_DOMAIN))
        .is_none());
    let _ = caught_up(model);
}
