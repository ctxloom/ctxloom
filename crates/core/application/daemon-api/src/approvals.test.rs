//! The permission asks (step 8 U4): recorded by the daemon with what only
//! it knows, named under the root's project, read back and waited on; and
//! the client's naming of an ask, its answer and its withdrawal.

use std::sync::Arc;

use angzarr_embedded::v1::{Cover, Uuid};
use daemon_proto::v1;
use run_ports::Edge;
use sessions::{MintSession, Origin, SessionName};

use super::*;
use crate::server::tests::{fixture, refused, resolve, Fixture, COMMIT, PROJECT, ROOT};
use crate::{Api, ApiError};

const ROOT_NAME: &str = "projects/calm-quiet-heron/sessions/amber-quiet-heron";
const CHILD_NAME: &str = "projects/calm-quiet-heron/sessions/brisk-copper-moth";

/// The ask names a test offers, in order.
struct Words(Vec<&'static str>);

impl AskNames for Words {
    fn fresh(&self) -> Vec<String> {
        self.0.iter().map(|w| w.to_string()).collect()
    }
}

fn uid(name: &str) -> String {
    SessionName::parse(name).unwrap().uid()
}

fn asker(timeout: u64) -> daemon_application::Asker {
    daemon_application::Asker {
        session: uid(CHILD_NAME),
        run: "run-1".to_string(),
        engine: "claude-code".to_string(),
        agent: "default".to_string(),
        approval_timeout: std::time::Duration::from_secs(timeout),
    }
}

/// The project registered, its root session and that session's child
/// minted, the child hosted asking with `timeout`, and the API over them
/// offering `words`: the API and its pending approvals.
async fn asking(
    f: &Fixture,
    timeout: u64,
    words: Vec<&'static str>,
) -> (Api<crate::server::tests::World>, PendingApprovals) {
    f.api.resolve_project(resolve(COMMIT, "")).await.unwrap();
    f.mint("amber-quiet-heron").await;
    let root = SessionName::parse(CHILD_NAME).unwrap().root();
    let mint = MintSession {
        harp: "brisk-copper-moth".to_string(),
        project_id: PROJECT.to_string(),
        project_dir: ROOT.to_string(),
        output_dir: format!("{ROOT}/ctxloom/brisk-copper-moth"),
        engine: "claude-code".to_string(),
        origin: Origin::Session,
        started_at: None,
        depth: 1,
        owner: Some(sessions::SessionRef {
            project_id: PROJECT.to_string(),
            session: uid(ROOT_NAME),
        }),
        workspace: None,
    };
    let cover = Cover {
        domain: sessions::SESSION_DOMAIN.to_string(),
        root: Some(root),
        edition: None,
    };
    f.world
        .handle(sessions::keyed_book(cover, &mint, "mint:child"))
        .await
        .unwrap();
    f.hosting.askers.lock().unwrap().push(asker(timeout));
    let index = PendingApprovals::default();
    let api = f.restarted().await.approving(Approving {
        index: index.clone(),
        names: Arc::new(Words(words)),
    });
    (api, index)
}

fn create(request_id: &str) -> v1::CreateApprovalRequest {
    v1::CreateApprovalRequest {
        parent: "projects/calm-quiet-heron".to_string(),
        session: uid(CHILD_NAME),
        engine_request_id: request_id.to_string(),
        tool_name: "Bash".to_string(),
        input_json: r#"{"command":"rm -rf build/"}"#.to_string(),
        request_id: "r".to_string(),
    }
}

async fn caught(f: &Fixture, index: &PendingApprovals) {
    let mut caught = pending_approvals::caught_up(index.clone());
    caught
        .step(&f.world.store, f.world.registry.catalog())
        .await
        .unwrap();
}

#[tokio::test]
async fn an_ask_is_recorded_with_what_the_daemon_knows_and_named_under_its_root_project() {
    let f = fixture(vec!["calm-quiet-heron"]);
    let (api, index) = asking(&f, 300, vec!["tidy-wren"]).await;
    let made = api.create_approval(create("toolu_1")).await.unwrap();
    assert_eq!(
        made.name,
        "projects/calm-quiet-heron/approvals/ask-tidy-wren"
    );
    assert_eq!(made.child, CHILD_NAME);
    assert_eq!(made.root, ROOT_NAME);
    assert_eq!(made.engine, "claude-code");
    assert_eq!(made.agent, "default");
    assert_eq!(made.tool_name, "Bash");
    assert!(made.input_excerpt.contains("rm -rf build/"));
    assert_eq!(made.state, "waiting");
    assert_eq!(made.timeout_seconds, 300);
    assert_eq!(
        made.uid,
        approvals::ask_root(&uid(CHILD_NAME), "run-1", "toolu_1")
            .hyphenated()
            .to_string()
    );
    let ids: Vec<&str> = made.options.iter().map(|o| o.id.as_str()).collect();
    assert_eq!(ids, ["allow-once", "allow-session", "allow-always", "deny"]);
    let expire = made.expire_time.unwrap().seconds - made.ask_time.unwrap().seconds;
    assert_eq!(expire, 300);
    // A retry of the same request is the approval it made.
    caught(&f, &index).await;
    let again = api.create_approval(create("toolu_1")).await.unwrap();
    assert_eq!(again, made);
    // Read, and listed under its root.
    let got = api
        .get_approval(v1::GetApprovalRequest {
            name: made.name.clone(),
        })
        .await
        .unwrap();
    assert_eq!(got, made);
    let listed = api
        .list_approvals(v1::ListApprovalsRequest {
            parent: "projects/calm-quiet-heron".to_string(),
            root: ROOT_NAME.to_string(),
            waiting: true,
            ..v1::ListApprovalsRequest::default()
        })
        .await
        .unwrap();
    assert_eq!(listed.approvals, vec![made.clone()]);
    let elsewhere = api
        .list_approvals(v1::ListApprovalsRequest {
            parent: "projects/calm-quiet-heron".to_string(),
            root: CHILD_NAME.to_string(),
            ..v1::ListApprovalsRequest::default()
        })
        .await
        .unwrap();
    assert!(elsewhere.approvals.is_empty());
}

#[tokio::test]
async fn a_name_another_ask_holds_is_passed_over_and_none_free_fails() {
    let f = fixture(vec!["calm-quiet-heron"]);
    let (api, _) = asking(&f, 300, vec!["tidy-wren", "pale-moth"]).await;
    let first = api.create_approval(create("toolu_1")).await.unwrap();
    let second = api.create_approval(create("toolu_2")).await.unwrap();
    assert!(first.name.ends_with("/ask-tidy-wren"), "{}", first.name);
    assert!(second.name.ends_with("/ask-pale-moth"), "{}", second.name);
    let none = api.create_approval(create("toolu_3")).await;
    assert!(matches!(none, Err(ApiError::Failed { .. })), "{none:?}");
}

#[tokio::test]
async fn an_ask_with_no_time_is_expired_at_once() {
    let f = fixture(vec!["calm-quiet-heron"]);
    let (api, _) = asking(&f, 0, vec!["tidy-wren"]).await;
    let made = api.create_approval(create("toolu_1")).await.unwrap();
    assert_eq!(made.state, "expired");
    assert_eq!(made.timeout_seconds, 0);
}

#[tokio::test]
async fn an_ask_of_a_session_not_hosted_or_unknown_is_refused() {
    let f = fixture(vec!["calm-quiet-heron"]);
    let (api, _) = asking(&f, 300, vec!["tidy-wren"]).await;
    f.hosting.askers.lock().unwrap().clear();
    let (_, hosted): (_, v1::SessionNotHosted) = refused(api.create_approval(create("t")).await);
    assert_eq!(hosted.name, CHILD_NAME);
    let mut nobody = create("t");
    nobody.session = "0b0b0b0b-0b0b-4b0b-8b0b-0b0b0b0b0b0b".to_string();
    let (_, found): (_, v1::SessionNotFound) = refused(api.create_approval(nobody).await);
    assert_eq!(found.name, "0b0b0b0b-0b0b-4b0b-8b0b-0b0b0b0b0b0b");
}

#[tokio::test]
async fn a_daemon_that_records_no_ask_fails_every_approvals_call() {
    let f = fixture(vec![]);
    let failed = |r: Result<v1::Approval, ApiError>| matches!(r, Err(ApiError::Failed { .. }));
    assert!(failed(f.api.create_approval(create("t")).await));
    let name = "projects/calm-quiet-heron/approvals/ask-a".to_string();
    assert!(failed(
        f.api
            .get_approval(v1::GetApprovalRequest { name: name.clone() })
            .await
    ));
    assert!(failed(
        f.api.wait_approval(v1::WaitApprovalRequest { name }).await
    ));
    let listed = f
        .api
        .list_approvals(v1::ListApprovalsRequest::default())
        .await;
    assert!(matches!(listed, Err(ApiError::Failed { .. })));
}

#[tokio::test]
async fn an_ask_nobody_asked_is_refused_unknown_by_the_approvals_domain() {
    let f = fixture(vec!["calm-quiet-heron"]);
    let (api, _) = asking(&f, 300, vec!["tidy-wren"]).await;
    for name in ["projects/calm-quiet-heron/approvals/ask-none", "nonsense"] {
        let (_, unknown): (_, approvals_proto::v1::ApprovalUnknown) = refused(
            api.get_approval(v1::GetApprovalRequest {
                name: name.to_string(),
            })
            .await,
        );
        assert!(!unknown.ask_id.is_empty());
        let (_, _): (_, approvals_proto::v1::ApprovalUnknown) = refused(
            api.wait_approval(v1::WaitApprovalRequest {
                name: name.to_string(),
            })
            .await,
        );
    }
}

/// A settling of the ask `made`, as its stream's next page.
fn settling(
    made: &v1::Approval,
    event: approvals::ApprovalEvent,
) -> angzarr_embedded::v1::EventBook {
    use angzarr_embedded::Payload;
    let root = uuid::Uuid::parse_str(&made.uid).unwrap();
    angzarr_embedded::v1::EventBook {
        cover: Some(Cover {
            domain: approvals::APPROVALS_DOMAIN.to_string(),
            root: Some(Uuid::from_bytes(*root.as_bytes())),
            edition: None,
        }),
        pages: vec![angzarr_embedded::v1::EventPage {
            sequence: 1,
            body: Some(angzarr_embedded::v1::event_page::Body::Event(
                event.to_any(),
            )),
            meta: None,
        }],
        next_sequence: 2,
        snapshot: None,
    }
}

#[tokio::test]
async fn a_wait_is_answered_once_its_ask_is_settled_and_not_before() {
    let f = fixture(vec!["calm-quiet-heron"]);
    let (api, index) = asking(&f, 300, vec!["tidy-wren"]).await;
    let made = api.create_approval(create("toolu_1")).await.unwrap();
    caught(&f, &index).await;
    let settled = settling(
        &made,
        approvals::ApprovalEvent::ApprovalWithdrawn(approvals::ApprovalWithdrawn {
            reason: "r".to_string(),
        }),
    );
    let later = index.clone();
    // Two rounds still waiting; the third settles it.
    let before = *f.hosting.freshened.lock().unwrap();
    *f.hosting.rounds.lock().unwrap() = vec![
        Box::new(|| {}),
        Box::new(|| {}),
        Box::new(move || {
            use angzarr_embedded::ProjectorHandler as _;
            angzarr_embedded::testing::block_on_ready(later.dispatch(
                &angzarr_embedded::angzarr_router::hpb::ProjectRequest {
                    events: Some(settled),
                },
            ))
            .unwrap();
        }),
    ];
    let got = api
        .wait_approval(v1::WaitApprovalRequest {
            name: made.name.clone(),
        })
        .await
        .unwrap();
    assert_eq!(got.state, "withdrawn");
    assert_eq!(*f.hosting.freshened.lock().unwrap() - before, 3);
}

#[tokio::test]
async fn every_approvals_call_is_served_while_the_daemon_drains() {
    let f = fixture(vec!["calm-quiet-heron"]);
    let (api, index) = asking(&f, 300, vec!["tidy-wren"]).await;
    crate::Hosting::drain(&*f.hosting, None);
    let calls: &dyn crate::Calls = &api;
    let made = calls.create_approval(create("toolu_1")).await.unwrap();
    caught(&f, &index).await;
    calls
        .get_approval(v1::GetApprovalRequest {
            name: made.name.clone(),
        })
        .await
        .unwrap();
    calls
        .list_approvals(v1::ListApprovalsRequest::default())
        .await
        .unwrap();
    let withdrawn = settling(
        &made,
        approvals::ApprovalEvent::ApprovalWithdrawn(approvals::ApprovalWithdrawn {
            reason: String::new(),
        }),
    );
    {
        use angzarr_embedded::ProjectorHandler as _;
        index
            .dispatch(&angzarr_embedded::angzarr_router::hpb::ProjectRequest {
                events: Some(withdrawn),
            })
            .await
            .unwrap();
    }
    calls
        .wait_approval(v1::WaitApprovalRequest {
            name: made.name.clone(),
        })
        .await
        .unwrap();
}

#[test]
fn a_streams_pages_are_a_book_whose_next_follows_the_last() {
    let cover = Cover {
        domain: "approvals".to_string(),
        root: None,
        edition: None,
    };
    let page = |sequence| angzarr_embedded::v1::EventPage {
        sequence,
        ..angzarr_embedded::v1::EventPage::default()
    };
    assert_eq!(
        book_of(&cover, vec![page(0), page(1), page(2)]).next_sequence,
        3
    );
    assert_eq!(book_of(&cover, vec![page(0)]).next_sequence, 1);
    assert_eq!(book_of(&cover, Vec::new()).next_sequence, 0);
}
