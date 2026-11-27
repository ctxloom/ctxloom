//! A project's trust (step 8 U1): previewed at the project's registered
//! root with the etag of what it showed, listed while trusted, and the
//! user's grant and revoke through the coordinator, keyed once per call.

use std::sync::{Arc, Mutex};

use daemon_ports::{Called, CommandBook, DaemonApi, Daemons, Projection, Synced, Unreached};
use daemon_proto::v1;
use prost::Message;

use super::*;
use crate::Rejection;

const NAME: &str = "projects/calm-quiet-heron/trust";

/// What the user's daemons saw.
#[derive(Default)]
struct Seen {
    asked: Vec<String>,
    books: Vec<CommandBook>,
}

/// A daemon `n`; the first of them dies under a write it applied, when
/// `dying`; every grant is refused stale when `stale`.
#[derive(Clone)]
struct Fake {
    n: usize,
    seen: Arc<Mutex<Seen>>,
    dying: bool,
    projecting: bool,
    /// The etags each preview answers, in turn (the last repeated).
    etags: Arc<Mutex<Vec<&'static str>>>,
}

impl Fake {
    fn ask(&self, what: String) {
        self.seen
            .lock()
            .unwrap()
            .asked
            .push(format!("{}:{what}", self.n));
    }
}

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
    async fn get_project(&self, r: v1::GetProjectRequest) -> Result<v1::Project, Called> {
        self.ask(format!("project {}", r.name));
        Ok(v1::Project {
            uid: "calm-otter".to_string(),
            ..v1::Project::default()
        })
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

    async fn get_trust(&self, r: v1::GetTrustRequest) -> Result<v1::Trust, Called> {
        self.ask(format!("trust {}", r.name));
        let mut etags = self.etags.lock().unwrap();
        let etag = match etags.len() {
            1 => etags[0],
            _ => etags.remove(0),
        };
        Ok(v1::Trust {
            name: r.name,
            etag: etag.to_string(),
            ..v1::Trust::default()
        })
    }

    async fn list_trusts(
        &self,
        _: v1::ListTrustsRequest,
    ) -> Result<v1::ListTrustsResponse, Called> {
        self.ask("list".to_string());
        Ok(v1::ListTrustsResponse::default())
    }

    async fn handle_sync(
        &self,
        book: CommandBook,
        projectors: Vec<String>,
    ) -> Result<Synced, Called> {
        assert_eq!(projectors, [TRUST_INDEX]);
        self.seen.lock().unwrap().books.push(book);
        self.ask("write".to_string());
        if self.dying && self.n == 1 {
            return Err(Called::Unreachable {
                detail: "killed".to_string(),
            });
        }
        let projections = match self.projecting {
            true => vec![Projection {
                projector: TRUST_INDEX.to_string(),
                projection: Some(
                    prost_types::Any::from_msg(&v1::Trust {
                        name: NAME.to_string(),
                        trusted: true,
                        ..v1::Trust::default()
                    })
                    .unwrap(),
                ),
                ..Projection::default()
            }],
            false => Vec::new(),
        };
        Ok(Synced {
            applied: self.n == 1,
            projections,
        })
    }
}

struct Reached {
    next: Mutex<usize>,
    seen: Arc<Mutex<Seen>>,
    dying: bool,
    projecting: bool,
    etags: Arc<Mutex<Vec<&'static str>>>,
}

impl Daemons for Reached {
    type Api = Fake;

    async fn serving(&self) -> Result<Option<Fake>, Unreached> {
        unreachable!()
    }

    async fn reach(&self) -> Result<Fake, Unreached> {
        let mut next = self.next.lock().unwrap();
        *next += 1;
        Ok(Fake {
            n: *next,
            seen: Arc::clone(&self.seen),
            dying: self.dying,
            projecting: self.projecting,
            etags: Arc::clone(&self.etags),
        })
    }
}

fn user(
    dying: bool,
    projecting: bool,
    etags: Vec<&'static str>,
) -> (Caller<Reached>, Arc<Mutex<Seen>>) {
    let seen = Arc::new(Mutex::new(Seen::default()));
    let ids = Arc::new(Mutex::new(0));
    let caller = Caller::user(
        Reached {
            next: Mutex::new(0),
            seen: Arc::clone(&seen),
            dying,
            projecting,
            etags: Arc::new(Mutex::new(etags)),
        },
        "projects/calm-quiet-heron",
        v1::Caller::default(),
        Box::new(move || {
            let mut n = ids.lock().unwrap();
            *n += 1;
            format!("id-{n}")
        }),
    )
    .unwrap();
    (caller, seen)
}

fn command_of<T: Message + Default>(book: &CommandBook) -> T {
    match book.page.as_ref().and_then(|p| p.body.as_ref()) {
        Some(angzarr_embedded::v1::command_page::Body::Command(command)) => {
            T::decode(command.payload.as_ref().unwrap().value.as_slice()).unwrap()
        }
        other => panic!("no command: {other:?}"),
    }
}

#[tokio::test]
async fn a_grant_names_the_previews_etag_and_the_one_read_just_before_it() {
    let (caller, seen) = user(false, true, vec!["now"]);
    let granted = caller.grant_trust("seen", "alice").await.unwrap();
    assert!(granted.applied);
    assert!(granted.trust.trusted);
    let seen = seen.lock().unwrap();
    assert_eq!(
        seen.asked,
        [
            format!("1:trust {NAME}"),
            "1:project projects/calm-quiet-heron".to_string(),
            "1:write".to_string()
        ]
    );
    let book = &seen.books[0];
    let cover = book.cover.as_ref().unwrap();
    assert_eq!(cover.domain, trust::TRUST_DOMAIN);
    assert_eq!(cover.root, Some(trust::trust_root(NAME)));
    let grant: trust_proto::v1::GrantTrust = command_of(book);
    assert_eq!(grant.name, NAME);
    assert_eq!(grant.project_id, "calm-otter");
    assert_eq!(grant.etag, "seen");
    assert_eq!(grant.current_etag, "now");
    assert_eq!(grant.actor.unwrap().user, "alice");
}

#[tokio::test]
async fn a_grant_retried_on_a_new_daemon_is_the_same_command() {
    let (caller, seen) = user(true, false, vec!["e"]);
    let granted = caller.grant_trust("e", "alice").await.unwrap();
    // The second daemon found it applied, and answered no projection: the
    // trust as `GetTrust` reads it.
    assert!(!granted.applied);
    assert_eq!(granted.trust.name, NAME);
    let seen = seen.lock().unwrap();
    assert_eq!(seen.books.len(), 2);
    let ids: Vec<_> = seen
        .books
        .iter()
        .map(|b| b.page.as_ref().unwrap().command_id.clone())
        .collect();
    assert_eq!(ids[0], ids[1]);
    assert!(
        seen.asked.last().unwrap().starts_with("2:trust"),
        "{:?}",
        seen.asked
    );
}

#[tokio::test]
async fn a_revoke_is_the_users_through_the_coordinator() {
    let (caller, seen) = user(false, true, vec!["e"]);
    caller.revoke_trust("alice").await.unwrap();
    {
        let seen = seen.lock().unwrap();
        let revoke: trust_proto::v1::RevokeTrust = command_of(&seen.books[0]);
        assert_eq!(revoke.name, NAME);
        assert_eq!(revoke.project_id, "calm-otter");
        assert_eq!(revoke.actor.clone().unwrap().user, "alice");
    }
    // Two calls, two ids: a grant's key is not a revoke's.
    let (caller, seen) = user(false, true, vec!["e"]);
    caller.grant_trust("e", "alice").await.unwrap();
    caller.revoke_trust("alice").await.unwrap();
    let seen = seen.lock().unwrap();
    assert_ne!(
        seen.books[0].page.as_ref().unwrap().command_id,
        seen.books[1].page.as_ref().unwrap().command_id
    );
}

#[tokio::test]
async fn the_reads_ask_the_projects_trust_and_the_list() {
    let (caller, seen) = user(false, true, vec!["e"]);
    assert_eq!(caller.get_trust().await.unwrap().name, NAME);
    caller.list_trusts().await.unwrap();
    assert_eq!(
        seen.lock().unwrap().asked,
        [format!("1:trust {NAME}"), "2:list".to_string()]
    );
}

#[test]
fn a_stale_plan_a_client_is_answered_reads_as_the_trust_domains_rejection() {
    let typed = angzarr_embedded::markup::TypedRefusal::of(&trust::PlanStale {
        seen: "a".to_string(),
        now: "b".to_string(),
    });
    let refusal = daemon_api_contract::ApiRefusal::typed(
        &typed,
        <trust::PlanStale as trust::Refusal>::REFUSAL_STATUS,
    );
    let called = Called::Refused {
        code: refusal.code,
        refusal: refusal.refusal,
    };
    let error = CallError::from(called);
    assert!(
        matches!(
            &error,
            CallError::Refused(Rejection::Trust(trust::TrustRejection::PlanStale))
        ),
        "{error:?}"
    );
    assert_eq!(
        coded_diagnostic::Diagnostic::code(&error),
        "trust-plan-stale"
    );
    assert_eq!(cli_exit(&error), 48);
}

fn cli_exit(error: &CallError) -> u8 {
    exit_code::ExitClass::exit(error).code(true)
}

// The client's trust reads, as session-cli reaches them (`TrustWrites`):
// each asks the daemon.
#[tokio::test]
async fn the_trust_writes_port_reads_through_the_daemon() {
    let (caller, seen) = user(false, true, vec!["e"]);
    assert_eq!(
        super::TrustWrites::get_trust(&caller).await.unwrap().name,
        NAME
    );
    super::TrustWrites::list_trusts(&caller).await.unwrap();
    assert_eq!(
        seen.lock().unwrap().asked,
        [format!("1:trust {NAME}"), "2:list".to_string()]
    );
}
