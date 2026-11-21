//! A project's trust (step 8 U1): previewed at the project's registered
//! root with the etag of what it showed, listed while trusted, and the
//! user's grant and revoke through the coordinator, keyed once per call.

use std::sync::{Arc, Mutex};

use daemon_ports::{Called, CommandBook, DaemonApi, Daemons, Projection, Synced, Unreached};
use daemon_proto::v1;
use prost::Message;
use run_ports::Edge;

use super::{etag_of, Trusting, Trusts, TRUST_INDEX};
use crate::server::tests::{fixture, refused, resolve, COMMIT, ROOT};
use crate::{ApiError, CallError, Caller, Rejection};

const NAME: &str = "projects/calm-quiet-heron/trust";

fn setting(path: &str, value: &str) -> v1::TrustSetting {
    v1::TrustSetting {
        path: path.to_string(),
        value: value.to_string(),
    }
}

/// A project's settings as a test sets them, and where they were read.
#[derive(Default)]
struct Settings {
    said: Mutex<Vec<v1::TrustSetting>>,
    read_at: Mutex<Vec<String>>,
    /// The observation's scans, counted; how many there had been at each
    /// read.
    scans: Option<Arc<std::sync::atomic::AtomicU32>>,
    scans_at_read: Mutex<Vec<u32>>,
}

impl Trusts for Settings {
    fn settings(&self, root: &str) -> Result<Vec<v1::TrustSetting>, ApiError> {
        self.read_at.lock().unwrap().push(root.to_string());
        if let Some(scans) = &self.scans {
            let seen = scans.load(std::sync::atomic::Ordering::SeqCst);
            self.scans_at_read.lock().unwrap().push(seen);
        }
        Ok(self.said.lock().unwrap().clone())
    }
}

#[test]
fn an_etag_names_the_settings_and_changes_with_any_of_them() {
    let one = [setting("engines.claude-code.command", "/opt/a")];
    let other = [setting("engines.claude-code.command", "/opt/b")];
    assert_eq!(etag_of(&one), etag_of(&one));
    assert_ne!(etag_of(&one), etag_of(&other));
    assert_ne!(etag_of(&one), etag_of(&[]));
    let two = [setting("a", "1"), setting("b", "2")];
    let joined = [setting("a", "1\nb=2")];
    assert_ne!(etag_of(&two), etag_of(&joined));
}

#[tokio::test]
async fn get_trust_previews_at_the_registered_root_sorted_with_its_etag() {
    let f = fixture(vec!["calm-quiet-heron"]);
    let settings = Arc::new(Settings::default());
    *settings.said.lock().unwrap() = vec![
        setting("engines.claude-code.command", "/opt/their-claude"),
        setting("agents.default.session_home", "host"),
    ];
    let index = trust_index::TrustIndexes::default();
    let api = f.restarted().await.trusting(Trusting {
        trusts: Arc::clone(&settings) as Arc<dyn Trusts>,
        index: index.clone(),
    });
    let (_, unknown): (_, v1::ProjectNotFound) = refused(
        api.get_trust(v1::GetTrustRequest {
            name: NAME.to_string(),
        })
        .await,
    );
    assert_eq!(unknown.name, "projects/calm-quiet-heron");
    f.api.resolve_project(resolve(COMMIT, "")).await.unwrap();
    let api = f.restarted().await.trusting(Trusting {
        trusts: Arc::clone(&settings) as Arc<dyn Trusts>,
        index,
    });
    let trust = api
        .get_trust(v1::GetTrustRequest {
            name: NAME.to_string(),
        })
        .await
        .unwrap();
    assert_eq!(trust.name, NAME);
    assert!(!trust.trusted);
    let paths: Vec<&str> = trust.settings.iter().map(|s| s.path.as_str()).collect();
    assert_eq!(
        paths,
        ["agents.default.session_home", "engines.claude-code.command"]
    );
    assert_eq!(trust.etag, etag_of(&trust.settings));
    assert_eq!(*settings.read_at.lock().unwrap(), [ROOT]);
    let (_, invalid): (_, v1::NameInvalid) = refused(
        api.get_trust(v1::GetTrustRequest {
            name: "projects/calm-quiet-heron".to_string(),
        })
        .await,
    );
    assert_eq!(invalid.name, "projects/calm-quiet-heron");
}

// Step 10 U11: the preview reads the project's own layer at its
// registered root, so that tree is observed BEFORE it is read, as a read's
// is (nothing in the daemon reads a library tree outside the observation).
#[tokio::test]
async fn get_trust_observes_the_projects_tree_before_it_reads_its_settings() {
    let f = fixture(vec!["calm-quiet-heron"]);
    f.api.resolve_project(resolve(COMMIT, "")).await.unwrap();
    f.catch_up().await;
    let gits = Arc::new(crate::server::tests::Gits::default());
    let settings = Arc::new(Settings {
        scans: Some(Arc::clone(&gits.scans)),
        ..Settings::default()
    });
    let api = f
        .restarted()
        .await
        .trusting(Trusting {
            trusts: Arc::clone(&settings) as Arc<dyn Trusts>,
            index: trust_index::TrustIndexes::default(),
        })
        .observing(crate::server::tests::observing(&f.world, &gits));
    api.get_trust(v1::GetTrustRequest {
        name: NAME.to_string(),
    })
    .await
    .unwrap();
    assert_eq!(*settings.scans_at_read.lock().unwrap(), vec![1]);
}

#[tokio::test]
async fn a_daemon_that_reads_no_trust_fails_both_calls() {
    let f = fixture(vec!["calm-quiet-heron"]);
    let got = f
        .api
        .get_trust(v1::GetTrustRequest {
            name: NAME.to_string(),
        })
        .await;
    assert!(matches!(got, Err(ApiError::Failed { .. })), "{got:?}");
    let listed = f.api.list_trusts(v1::ListTrustsRequest::default()).await;
    assert!(matches!(listed, Err(ApiError::Failed { .. })), "{listed:?}");
    // Nor does it trust any project a launch is made in.
    assert!(!f.api.trusts("calm-otter"));
}

/// `command` to the project's trust stream, through the world's kernel.
async fn sent(
    f: &crate::server::tests::Fixture,
    command: &impl angzarr_embedded::markup::DomainMessage,
    key: &str,
) {
    let cover = angzarr_embedded::v1::Cover {
        domain: trust::TRUST_DOMAIN.to_string(),
        root: Some(trust::trust_root(NAME)),
        edition: None,
    };
    f.world
        .handle(sessions::keyed_book(cover, command, key))
        .await
        .unwrap();
}

#[tokio::test]
async fn a_granted_trust_is_listed_and_shown_until_it_is_revoked() {
    let f = fixture(vec!["calm-quiet-heron"]);
    f.api.resolve_project(resolve(COMMIT, "")).await.unwrap();
    let index = trust_index::TrustIndexes::default();
    let api = f.restarted().await.trusting(Trusting {
        trusts: Arc::new(Settings::default()) as Arc<dyn Trusts>,
        index: index.clone(),
    });
    let actor = trust::Actor {
        user: "alice".to_string(),
    };
    let grant = trust::GrantTrust {
        name: NAME.to_string(),
        project_id: "calm-otter".to_string(),
        etag: "e".to_string(),
        current_etag: "e".to_string(),
        actor: actor.clone(),
    };
    sent(&f, &grant, "grant").await;
    let mut caught = trust_index::caught_up(index.clone());
    caught
        .step(&f.world.store, f.world.registry.catalog())
        .await
        .unwrap();
    let listed = api
        .list_trusts(v1::ListTrustsRequest::default())
        .await
        .unwrap();
    assert_eq!(listed.trusts.len(), 1);
    assert_eq!(listed.trusts[0].name, NAME);
    assert_eq!(listed.trusts[0].actor, "alice");
    assert!(listed.trusts[0].grant_time.is_some());
    let shown = api
        .get_trust(v1::GetTrustRequest {
            name: NAME.to_string(),
        })
        .await
        .unwrap();
    assert!(shown.trusted);
    assert_eq!(shown.actor, "alice");
    // What a launch in the project is planned with (step 8 U2).
    assert!(api.trusts("calm-otter"));
    assert!(!api.trusts("another-project"));
    let revoke = trust::RevokeTrust {
        name: NAME.to_string(),
        project_id: "calm-otter".to_string(),
        actor,
    };
    sent(&f, &revoke, "revoke").await;
    caught
        .step(&f.world.store, f.world.registry.catalog())
        .await
        .unwrap();
    let listed = api
        .list_trusts(v1::ListTrustsRequest::default())
        .await
        .unwrap();
    assert!(listed.trusts.is_empty(), "{listed:?}");
    assert!(!api.trusts("calm-otter"));
}

#[tokio::test]
async fn a_draining_daemon_refuses_both_calls() {
    let f = fixture(vec!["calm-quiet-heron"]);
    let api = f.restarted().await.trusting(Trusting {
        trusts: Arc::new(Settings::default()) as Arc<dyn Trusts>,
        index: trust_index::TrustIndexes::default(),
    });
    crate::Hosting::drain(&*f.hosting, None);
    let calls: &dyn crate::Calls = &api;
    let (_, _): (_, v1::DaemonDraining) = refused(
        calls
            .get_trust(v1::GetTrustRequest {
                name: NAME.to_string(),
            })
            .await,
    );
    let (_, _): (_, v1::DaemonDraining) =
        refused(calls.list_trusts(v1::ListTrustsRequest::default()).await);
}

// ------------------------------------------------------ the user's client

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
    let refusal =
        crate::ApiRefusal::typed(&typed, <trust::PlanStale as trust::Refusal>::REFUSAL_STATUS);
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
        angzarr_diagnostic::Diagnostic::code(&error),
        "trust-plan-stale"
    );
    assert_eq!(cli_exit(&error), 20);
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
