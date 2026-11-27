//! The permission asks (step 8 U4): recorded by the daemon with what only
//! it knows, named under the root's project, read back and waited on; and
//! the client's naming of an ask, its answer and its withdrawal.

use std::sync::{Arc, Mutex};

use daemon_ports::{Called, CommandBook, DaemonApi, Daemons, Projection, Synced, Unreached};
use daemon_proto::v1;
use prost::Message;
use sessions::SessionName;

use super::*;

const ROOT_NAME: &str = "projects/calm-quiet-heron/sessions/amber-quiet-heron";
fn uid(name: &str) -> String {
    SessionName::parse(name).unwrap().uid()
}

const CHILD_NAME: &str = "projects/calm-quiet-heron/sessions/brisk-copper-moth";

/// What the fake daemon saw, and what it answers.
#[derive(Default)]
struct Seen {
    asked: Vec<String>,
    books: Vec<CommandBook>,
    listed: Vec<v1::Approval>,
    got: Vec<v1::Approval>,
    refuse: Option<prost_types::Any>,
}

#[derive(Clone)]
struct Fake(Arc<Mutex<Seen>>);

impl DaemonApi for Fake {
    async fn get_daemon(&self, _: v1::GetDaemonRequest) -> Result<v1::Daemon, Called> {
        unreachable!()
    }
    async fn stop_daemon(&self, _: v1::StopDaemonRequest) -> Result<v1::Daemon, Called> {
        unreachable!()
    }
    async fn resolve_project(&self, _: v1::ResolveProjectRequest) -> Result<v1::Project, Called> {
        unreachable!()
    }
    async fn get_project(&self, _: v1::GetProjectRequest) -> Result<v1::Project, Called> {
        unreachable!()
    }
    async fn get_session(&self, _: v1::GetSessionRequest) -> Result<v1::Session, Called> {
        unreachable!()
    }
    async fn list_sessions(
        &self,
        _: v1::ListSessionsRequest,
    ) -> Result<v1::ListSessionsResponse, Called> {
        unreachable!()
    }
    async fn search_sessions(
        &self,
        _: v1::SearchSessionsRequest,
    ) -> Result<v1::ListSessionsResponse, Called> {
        unreachable!()
    }
    async fn get_approval(&self, r: v1::GetApprovalRequest) -> Result<v1::Approval, Called> {
        let mut seen = self.0.lock().unwrap();
        seen.asked.push(format!("get {}", r.name));
        Ok(match seen.got.len() {
            0 => v1::Approval {
                name: r.name,
                ..v1::Approval::default()
            },
            1 => seen.got[0].clone(),
            _ => seen.got.remove(0),
        })
    }
    async fn list_approvals(
        &self,
        r: v1::ListApprovalsRequest,
    ) -> Result<v1::ListApprovalsResponse, Called> {
        let mut seen = self.0.lock().unwrap();
        seen.asked
            .push(format!("list {} {} {}", r.parent, r.root, r.waiting));
        Ok(v1::ListApprovalsResponse {
            approvals: seen.listed.clone(),
            ..v1::ListApprovalsResponse::default()
        })
    }
    async fn create_approval(&self, r: v1::CreateApprovalRequest) -> Result<v1::Approval, Called> {
        self.0.lock().unwrap().asked.push(format!(
            "create {} {} {} {} {}",
            r.parent, r.session, r.engine_request_id, r.tool_name, r.input_json
        ));
        Ok(v1::Approval::default())
    }
    async fn wait_approval(
        &self,
        r: v1::WaitApprovalRequest,
        until: crate::Until,
    ) -> Result<v1::Approval, Called> {
        self.0
            .lock()
            .unwrap()
            .asked
            .push(format!("wait {} {}", r.name, until.seconds));
        Ok(v1::Approval::default())
    }
    async fn handle(&self, book: CommandBook) -> Result<bool, Called> {
        let mut seen = self.0.lock().unwrap();
        seen.asked.push("handle".to_string());
        seen.books.push(book);
        Ok(true)
    }
    async fn handle_sync(
        &self,
        book: CommandBook,
        projectors: Vec<String>,
    ) -> Result<Synced, Called> {
        assert_eq!(projectors, [PENDING_APPROVALS]);
        let mut seen = self.0.lock().unwrap();
        seen.asked.push("answer".to_string());
        seen.books.push(book);
        if let Some(refusal) = seen.refuse.clone() {
            return Err(Called::Refused { code: 9, refusal });
        }
        Ok(Synced {
            applied: true,
            projections: vec![Projection {
                projector: PENDING_APPROVALS.to_string(),
                projection: Some(
                    prost_types::Any::from_msg(&v1::Approval {
                        state: "answered".to_string(),
                        ..v1::Approval::default()
                    })
                    .unwrap(),
                ),
                ..Projection::default()
            }],
        })
    }
}

struct Reached(Arc<Mutex<Seen>>);

impl Daemons for Reached {
    type Api = Fake;

    async fn serving(&self) -> Result<Option<Fake>, Unreached> {
        unreachable!()
    }

    async fn reach(&self) -> Result<Fake, Unreached> {
        Ok(Fake(Arc::clone(&self.0)))
    }
}

fn ids() -> crate::RequestIds {
    let n = Arc::new(Mutex::new(0));
    Box::new(move || {
        let mut n = n.lock().unwrap();
        *n += 1;
        format!("id-{n}")
    })
}

fn user() -> (Caller<Reached>, Arc<Mutex<Seen>>) {
    let seen = Arc::new(Mutex::new(Seen::default()));
    let caller = Caller::user(
        Reached(Arc::clone(&seen)),
        "projects/calm-quiet-heron",
        v1::Caller::default(),
        ids(),
    )
    .unwrap();
    (caller, seen)
}

fn waiting(ask: &str, child: &str) -> v1::Approval {
    v1::Approval {
        name: format!("projects/calm-quiet-heron/approvals/{ask}"),
        child: child.to_string(),
        engine: "claude-code".to_string(),
        state: "waiting".to_string(),
        uid: "0b0b0b0b-0b0b-4b0b-8b0b-0b0b0b0b0b0b".to_string(),
        options: ["allow-once", "deny"]
            .iter()
            .map(|id| v1::ApprovalOption {
                id: id.to_string(),
                ..v1::ApprovalOption::default()
            })
            .collect(),
        ..v1::Approval::default()
    }
}

fn api_refusal(error: CallError) -> ApiRejection {
    match error {
        CallError::Refused(Rejection::Api(api)) => api,
        other => panic!("not the daemon's refusal: {other:?}"),
    }
}

fn approvals_refusal(error: CallError) -> ApprovalsRejection {
    match error {
        CallError::Refused(Rejection::Approvals(refused)) => refused,
        other => panic!("not an approvals refusal: {other:?}"),
    }
}

#[tokio::test]
async fn the_listing_is_this_projects_waiting_asks_under_a_root_named_by_its_harp() {
    let (caller, seen) = user();
    caller.list_approvals(None).await.unwrap();
    caller
        .list_approvals(Some("amber-quiet-heron"))
        .await
        .unwrap();
    assert_eq!(
        seen.lock().unwrap().asked,
        [
            "list projects/calm-quiet-heron  true".to_string(),
            format!("list projects/calm-quiet-heron {ROOT_NAME} true"),
        ]
    );
}

#[tokio::test]
async fn an_ask_is_named_by_its_name_or_by_its_harp_while_one_waits() {
    let (caller, seen) = user();
    caller.approval("ask-tidy-wren").await.unwrap();
    assert_eq!(
        seen.lock().unwrap().asked,
        ["get projects/calm-quiet-heron/approvals/ask-tidy-wren"]
    );
    let none = caller.approval("brisk-copper-moth").await.unwrap_err();
    assert_eq!(
        api_refusal(none),
        ApiRejection::ApprovalNoneWaiting {
            harp: "brisk-copper-moth".to_string()
        }
    );
    seen.lock().unwrap().listed = vec![
        waiting("ask-a-b", CHILD_NAME),
        waiting("ask-c-d", ROOT_NAME),
    ];
    let one = caller.approval("brisk-copper-moth").await.unwrap();
    assert_eq!(one.name, "projects/calm-quiet-heron/approvals/ask-a-b");
    seen.lock()
        .unwrap()
        .listed
        .push(waiting("ask-e-f", CHILD_NAME));
    let two = caller.approval("brisk-copper-moth").await.unwrap_err();
    assert_eq!(
        api_refusal(two),
        ApiRejection::ApprovalAmbiguous {
            harp: "brisk-copper-moth".to_string(),
            asks: vec!["ask-a-b".to_string(), "ask-e-f".to_string()],
        }
    );
}

#[tokio::test]
async fn an_answer_is_sent_to_the_asks_stream_with_its_option_and_message() {
    let (caller, seen) = user();
    let answered = caller
        .answer_approval(&waiting("ask-a-b", CHILD_NAME), "deny", "not yet")
        .await
        .unwrap();
    assert!(answered.applied);
    assert_eq!(answered.approval.state, "answered");
    let seen = seen.lock().unwrap();
    let book = &seen.books[0];
    let cover = book.cover.as_ref().unwrap();
    assert_eq!(cover.domain, approvals::APPROVALS_DOMAIN);
    assert_eq!(
        cover.root.as_ref().map(|r| r.value.to_vec()),
        Some(
            uuid::Uuid::parse_str("0b0b0b0b-0b0b-4b0b-8b0b-0b0b0b0b0b0b")
                .unwrap()
                .as_bytes()
                .to_vec()
        )
    );
    let page = book.page.as_ref().unwrap();
    assert_eq!(page.command_id, Some(sessions::command_id("answer:id-1")));
    let command = match page.body.as_ref() {
        Some(angzarr_embedded::v1::command_page::Body::Command(command)) => {
            approvals_proto::v1::AnswerApproval::decode(
                command.payload.as_ref().unwrap().value.as_slice(),
            )
            .unwrap()
        }
        other => panic!("no command: {other:?}"),
    };
    assert_eq!(command.ask_id, "ask-a-b");
    assert_eq!(command.option, "deny");
    assert_eq!(command.message, "not yet");
}

#[tokio::test]
async fn a_settled_ask_or_an_option_not_offered_is_refused_before_anything_is_sent() {
    let (caller, seen) = user();
    let mut settled = waiting("ask-a-b", CHILD_NAME);
    settled.state = "expired".to_string();
    assert_eq!(
        approvals_refusal(
            caller
                .answer_approval(&settled, "deny", "")
                .await
                .unwrap_err()
        ),
        ApprovalsRejection::ApprovalSettled {
            ask_id: "ask-a-b".to_string(),
            how: "expired".to_string()
        }
    );
    assert_eq!(
        approvals_refusal(
            caller
                .answer_approval(&waiting("ask-a-b", CHILD_NAME), "deny-always", "")
                .await
                .unwrap_err()
        ),
        ApprovalsRejection::OptionNotOffered {
            ask_id: "ask-a-b".to_string(),
            option: "deny-always".to_string(),
            offered: "allow-once, deny".to_string(),
        }
    );
    assert!(seen.lock().unwrap().asked.is_empty());
}

#[tokio::test]
async fn a_refusal_named_by_its_type_is_said_with_the_ask_as_it_is_now() {
    let (caller, seen) = user();
    let mut now = waiting("ask-a-b", CHILD_NAME);
    now.state = "answered".to_string();
    now.option = "allow-once".to_string();
    {
        let mut seen = seen.lock().unwrap();
        seen.got = vec![now];
        seen.refuse = Some(prost_types::Any {
            type_url: "type.googleapis.com/ctxloom.approvals.v1.ApprovalIsSettled".to_string(),
            value: Vec::new(),
        });
    }
    let refused = caller
        .answer_approval(&waiting("ask-a-b", CHILD_NAME), "deny", "")
        .await
        .unwrap_err();
    assert_eq!(
        approvals_refusal(refused),
        ApprovalsRejection::ApprovalSettled {
            ask_id: "ask-a-b".to_string(),
            how: "answered (allow-once)".to_string()
        }
    );
    seen.lock().unwrap().refuse = Some(prost_types::Any {
        type_url: "type.googleapis.com/ctxloom.approvals.v1.MessageNotRelayed".to_string(),
        value: Vec::new(),
    });
    let mut acp = waiting("ask-a-b", CHILD_NAME);
    acp.engine = "opencode".to_string();
    seen.lock().unwrap().got = vec![acp.clone()];
    assert_eq!(
        approvals_refusal(caller.answer_approval(&acp, "deny", "x").await.unwrap_err()),
        ApprovalsRejection::MessageNotRelayed {
            ask_id: "ask-a-b".to_string(),
            engine: "opencode".to_string()
        }
    );
}

#[test]
fn a_type_only_refusal_is_filled_from_the_ask() {
    let ask = waiting("ask-a-b", CHILD_NAME);
    let empty = || String::new();
    assert_eq!(
        filled(
            ApprovalsRejection::ApprovalUnknown { ask_id: empty() },
            &ask,
            "x"
        ),
        ApprovalsRejection::ApprovalUnknown {
            ask_id: "ask-a-b".to_string()
        }
    );
    assert_eq!(
        filled(
            ApprovalsRejection::OptionNotOffered {
                ask_id: empty(),
                option: empty(),
                offered: empty()
            },
            &ask,
            "x"
        ),
        ApprovalsRejection::OptionNotOffered {
            ask_id: "ask-a-b".to_string(),
            option: "x".to_string(),
            offered: "allow-once, deny".to_string()
        }
    );
    assert_eq!(
        filled(ApprovalsRejection::NameTaken { ask_id: empty() }, &ask, "x"),
        ApprovalsRejection::NameTaken { ask_id: empty() }
    );
}

#[tokio::test]
async fn a_sessions_ask_wait_and_withdrawal_reach_the_daemon_as_its_own() {
    let seen = Arc::new(Mutex::new(Seen::default()));
    let caller = Caller::new(
        Reached(Arc::clone(&seen)),
        CHILD_NAME,
        v1::Caller::default(),
        ids(),
    )
    .unwrap();
    caller
        .create_approval("Bash", r#"{"command":"ls"}"#, "toolu_1")
        .await
        .unwrap();
    caller
        .wait_approval(
            "projects/calm-quiet-heron/approvals/ask-a-b",
            crate::Until {
                seconds: 360,
                nanos: 0,
            },
        )
        .await
        .unwrap();
    let withdrawn = caller
        .withdraw_approval(&waiting("ask-a-b", CHILD_NAME), "AbortError")
        .await
        .unwrap();
    assert!(withdrawn);
    let seen = seen.lock().unwrap();
    assert_eq!(
        seen.asked,
        [
            format!(
                "create projects/calm-quiet-heron {} toolu_1 Bash {{\"command\":\"ls\"}}",
                uid(CHILD_NAME)
            ),
            "wait projects/calm-quiet-heron/approvals/ask-a-b 360".to_string(),
            "handle".to_string(),
        ]
    );
    let page = seen.books[0].page.as_ref().unwrap();
    match page.body.as_ref() {
        Some(angzarr_embedded::v1::command_page::Body::Fact(fact)) => {
            assert_eq!(
                fact.origin.as_ref().unwrap().external_id,
                "approval-withdrawn:ask-a-b"
            );
            let withdrawn = approvals_proto::v1::RecordApprovalWithdrawn::decode(
                fact.payload.as_ref().unwrap().value.as_slice(),
            )
            .unwrap();
            assert_eq!(withdrawn.reason, "AbortError");
        }
        other => panic!("not a fact: {other:?}"),
    }
}

#[tokio::test]
async fn the_user_has_no_session_to_ask_as() {
    let (caller, seen) = user();
    assert!(caller.create_approval("Bash", "{}", "t").await.is_err());
    assert!(seen.lock().unwrap().asked.is_empty());
}

// A settled ask's refusal is read back as the ask and how it was settled;
// any other error is none.
#[test]
fn a_settled_refusal_is_read_back_as_its_ask_and_how() {
    let expired = v1::Approval {
        name: "projects/p/approvals/ask-a-b".to_string(),
        state: "expired".to_string(),
        ..v1::Approval::default()
    };
    let refused = settled_refusal(&expired).expect("a settled refusal");
    assert_eq!(
        settled_of(&refused),
        Some(("ask-a-b".to_string(), "expired".to_string()))
    );
    let other = CallError::Refused(Rejection::Approvals(ApprovalsRejection::ApprovalUnknown {
        ask_id: "ask-a-b".to_string(),
    }));
    assert_eq!(settled_of(&other), None);
}

// The client's approvals reads, as session-cli reaches them
// (`ApprovalAnswers`): each asks the daemon.
#[tokio::test]
async fn the_answers_port_reads_through_the_daemon() {
    let (caller, seen) = user();
    ApprovalAnswers::list_approvals(&caller, None)
        .await
        .unwrap();
    let got = ApprovalAnswers::approval(&caller, "ask-tidy-wren")
        .await
        .unwrap();
    assert_eq!(
        got.name,
        "projects/calm-quiet-heron/approvals/ask-tidy-wren"
    );
    let asked = seen.lock().unwrap().asked.clone();
    assert_eq!(asked.len(), 2, "{asked:?}");
    assert!(asked[0].starts_with("list "), "{asked:?}");
    assert!(asked[1].starts_with("get "), "{asked:?}");
}

// A stream's pages read from its start are a book whose next sequence
// follows its last page; none, the next is 0.

#[test]
fn a_settled_approval_says_how_and_a_waiting_one_nothing() {
    let approval = |state: &str| v1::Approval {
        name: "projects/p/approvals/ask-a-b".to_string(),
        state: state.to_string(),
        option: "deny".to_string(),
        ..v1::Approval::default()
    };
    assert_eq!(
        settled_how(&approval("answered")).as_deref(),
        Some("answered (deny)")
    );
    assert_eq!(
        settled_how(&approval("expired")).as_deref(),
        Some("expired")
    );
    assert_eq!(
        settled_how(&approval("withdrawn")).as_deref(),
        Some("withdrawn")
    );
    assert_eq!(settled_how(&approval("waiting")), None);
    assert_eq!(ask_of(&approval("waiting")), "ask-a-b");
}

// ------------------------------------------------------------- the client
