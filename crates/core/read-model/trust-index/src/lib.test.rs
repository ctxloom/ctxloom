//! The trust index (step 8 U1): a project's trust folded from its stream,
//! listed only while it is trusted, and answered as its resource.

use angzarr_embedded::angzarr_proto::v1::EventMeta;
use angzarr_embedded::angzarr_router::hpb::ProjectRequest;
use angzarr_embedded::testing::block_on_ready as ready;
use angzarr_embedded::v1::{event_page, Cover, EventBook, EventPage};
use angzarr_embedded::{Payload, ProjectorHandler, Timestamp};
use trust::{trust_root, Actor, TrustEvent, TrustGranted, TrustRevoked, TRUST_DOMAIN};

use crate::{caught_up, TrustIndex, TrustIndexes, TrustRow};

fn name(harp: &str) -> String {
    format!("projects/{harp}/trust")
}

fn granted(harp: &str, user: &str) -> TrustEvent {
    TrustEvent::TrustGranted(TrustGranted {
        name: name(harp),
        project_id: format!("id-{harp}"),
        actor: Actor {
            user: user.to_string(),
        },
    })
}

fn revoked(harp: &str, user: &str) -> TrustEvent {
    TrustEvent::TrustRevoked(TrustRevoked {
        name: name(harp),
        project_id: format!("id-{harp}"),
        actor: Actor {
            user: user.to_string(),
        },
    })
}

/// The trust stream of `harp`: `events` from `first` on, each recorded at
/// its sequence's second.
fn on(harp: &str, domain: &str, first: u64, events: Vec<TrustEvent>) -> EventBook {
    let pages: Vec<EventPage> = (first..)
        .zip(events)
        .map(|(sequence, event)| EventPage {
            sequence,
            body: Some(event_page::Body::Event(event.to_any())),
            meta: Some(EventMeta {
                recorded_at: Some(Timestamp {
                    seconds: 1000 + sequence as i64,
                    nanos: 0,
                }),
                ..EventMeta::default()
            }),
        })
        .collect();
    let next = pages.last().map_or(0, |p| p.sequence + 1);
    EventBook {
        cover: Some(Cover {
            domain: domain.to_string(),
            root: Some(trust_root(&name(harp))),
            edition: None,
        }),
        pages,
        next_sequence: next,
        snapshot: None,
    }
}

fn at(seconds: i64) -> Option<Timestamp> {
    Some(Timestamp { seconds, nanos: 0 })
}

#[test]
fn a_granted_trust_is_trusted_now_with_its_user_and_when_it_was_granted() {
    let mut index = TrustIndex::new();
    index
        .project(&on(
            "amber",
            TRUST_DOMAIN,
            0,
            vec![granted("amber", "alice")],
        ))
        .unwrap();
    let row = TrustRow {
        name: name("amber"),
        project_id: "id-amber".to_string(),
        trusted: true,
        user: "alice".to_string(),
        grant_time: at(1000),
    };
    assert_eq!(index.named(&name("amber")), Some(row.clone()));
    assert_eq!(index.trusted_now(), vec![row]);
}

#[test]
fn a_revoked_trust_is_not_listed_and_says_who_took_it_back() {
    let mut index = TrustIndex::new();
    index
        .project(&on(
            "amber",
            TRUST_DOMAIN,
            0,
            vec![granted("amber", "alice")],
        ))
        .unwrap();
    index
        .project(&on("amber", TRUST_DOMAIN, 1, vec![revoked("amber", "bob")]))
        .unwrap();
    assert_eq!(index.trusted_now(), vec![]);
    let row = index.named(&name("amber")).unwrap();
    assert!(!row.trusted);
    assert_eq!(row.user, "bob");
    assert_eq!(row.grant_time, None);
}

#[test]
fn a_trust_granted_again_is_listed_with_its_new_time() {
    let mut index = TrustIndex::new();
    index
        .project(&on(
            "amber",
            TRUST_DOMAIN,
            0,
            vec![
                granted("amber", "alice"),
                revoked("amber", "alice"),
                granted("amber", "alice"),
            ],
        ))
        .unwrap();
    assert_eq!(index.trusted_now()[0].grant_time, at(1002));
}

#[test]
fn the_trusted_are_listed_by_name_whatever_order_they_came_in() {
    let mut index = TrustIndex::new();
    for harp in ["cedar", "amber", "birch"] {
        index
            .project(&on(harp, TRUST_DOMAIN, 0, vec![granted(harp, "alice")]))
            .unwrap();
    }
    let names: Vec<String> = index.trusted_now().into_iter().map(|r| r.name).collect();
    assert_eq!(names, [name("amber"), name("birch"), name("cedar")]);
}

#[test]
fn a_book_from_sequence_zero_starts_the_stream_afresh() {
    let mut index = TrustIndex::new();
    index
        .project(&on(
            "amber",
            TRUST_DOMAIN,
            0,
            vec![granted("amber", "alice")],
        ))
        .unwrap();
    // The stream purged and written again: only what it holds now counts.
    let mut again = on("amber", TRUST_DOMAIN, 0, vec![]);
    again.next_sequence = 0;
    index.project(&again).unwrap();
    assert_eq!(index.named(&name("amber")), None);
}

#[test]
fn another_domains_book_is_none_of_its() {
    let mut index = TrustIndex::new();
    index
        .project(&on("amber", "sessions", 0, vec![granted("amber", "alice")]))
        .unwrap();
    assert_eq!(index.trusted_now(), vec![]);
    assert_eq!(index.named(&name("amber")), None);
}

#[test]
fn nothing_is_named_that_was_never_granted() {
    let index = TrustIndex::new();
    assert_eq!(index.named(&name("amber")), None);
}

// S32: what the port answers for a stream is its trust as the resource
// message (`ctxloom.v1.Trust`).
#[test]
fn the_projection_is_the_streams_trust_as_its_resource() {
    use prost::Message as _;
    let model = TrustIndexes::default();
    let view = ready(model.dispatch(&ProjectRequest {
        events: Some(on(
            "amber",
            TRUST_DOMAIN,
            0,
            vec![granted("amber", "alice")],
        )),
    }))
    .unwrap()
    .projection
    .expect("a view");
    assert_eq!(
        view.type_url,
        <daemon_proto::v1::Trust as prost::Name>::type_url()
    );
    let trust = daemon_proto::v1::Trust::decode(view.value.as_slice()).unwrap();
    assert_eq!(trust.name, name("amber"));
    assert!(trust.trusted);
    assert_eq!(trust.actor, "alice");
    assert_eq!(trust.grant_time.map(|t| t.seconds), Some(1000));
    assert_eq!(model.name(), "trust-index");
    assert_eq!(model.index().trusted_now().len(), 1);
    let _ = caught_up(model);
}

#[test]
fn a_stream_it_holds_nothing_of_has_no_view() {
    use held_projection::Model as _;
    let index = TrustIndex::new();
    let cover = Cover {
        domain: TRUST_DOMAIN.to_string(),
        root: Some(trust_root(&name("amber"))),
        edition: None,
    };
    assert!(index.view(&cover).is_none());
}

// Step 8 U2: a launch asks whether the project it is in, by its id (a
// worktree shares its primary's), is trusted now.
#[test]
fn a_project_is_trusted_by_its_id_only_while_its_trust_stands() {
    let mut index = TrustIndex::new();
    assert!(!index.trusts("id-amber"));
    index
        .project(&on(
            "amber",
            TRUST_DOMAIN,
            0,
            vec![granted("amber", "alice")],
        ))
        .unwrap();
    assert!(index.trusts("id-amber"));
    assert!(!index.trusts("id-birch"));
    assert!(!index.trusts(""));
    index
        .project(&on(
            "amber",
            TRUST_DOMAIN,
            1,
            vec![revoked("amber", "alice")],
        ))
        .unwrap();
    assert!(!index.trusts("id-amber"));
}
