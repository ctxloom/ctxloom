//! A project's trust (step 8 U1): previewed at the project's registered
//! root with the etag of what it showed, listed while trusted, and the
//! user's grant and revoke through the coordinator, keyed once per call.

use std::sync::{Arc, Mutex};

use daemon_proto::v1;
use run_ports::Edge;

use super::{etag_of, Trusting, Trusts};
use crate::server::tests::{fixture, refused, resolve, COMMIT, ROOT};
use crate::ApiError;

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
