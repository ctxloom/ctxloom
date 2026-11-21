//! `CreateHandOff` and `DeleteHandOff` (slice 4 U3, T6, S10) over the real
//! aggregates and the warm index: the owner and the credential are checked
//! BEFORE anything is written, so a refused launch leaves nothing.

use std::collections::BTreeMap;
use std::sync::{Arc, Mutex};

use daemon_proto::v1;
use run_ports::Edge as _;

use crate::server::tests::{fixture, refused, resolve, Fixture, COMMIT};
use crate::{HandOffContent, HandOffs, HandingOff, Planned};

const PARENT: &str = "projects/calm-quiet-heron";
const CHILD: &str = "5f0c7a52-1111-5111-8111-000000000002";

/// The hand-offs a test scripts: a plan whose credential is or is not set,
/// and what was written and deleted.
#[derive(Default)]
struct Kept {
    planned: Mutex<Vec<HandingOff>>,
    written: Mutex<BTreeMap<String, HandOffContent>>,
    deleted: Mutex<Vec<String>>,
    credential_set: bool,
    unplanned: bool,
    /// The untrusted settings the plan says its launch reads.
    untrusted: Vec<String>,
    /// The observation's scans, counted; how many there had been at each
    /// plan.
    scans: Option<Arc<std::sync::atomic::AtomicU32>>,
    scans_at_plan: Mutex<Vec<u32>>,
}

impl HandOffs for Kept {
    fn plan(&self, request: &HandingOff) -> Result<Planned, String> {
        self.planned.lock().unwrap().push(request.clone());
        if let Some(scans) = &self.scans {
            let seen = scans.load(std::sync::atomic::Ordering::SeqCst);
            self.scans_at_plan.lock().unwrap().push(seen);
        }
        if self.unplanned {
            return Err("library-application-config-unreadable".to_string());
        }
        // None asked: the agent's configured engine, here opencode.
        let engine = match request.engine.is_empty() {
            true => "opencode".to_string(),
            false => request.engine.clone(),
        };
        Ok(Planned {
            engine,
            drivable_engines: vec!["claude-code".to_string(), "opencode".to_string()],
            credential: runs::CredentialEvidence {
                file_only: false,
                builtin_variables: vec!["CLAUDE_CODE_OAUTH_TOKEN".to_string()],
                accepted_variables: vec!["CLAUDE_CODE_OAUTH_TOKEN".to_string()],
                accepted_set: self.credential_set,
            },
            fatal_findings: 0,
            untrusted_settings: self.untrusted.clone(),
            content: HandOffContent {
                env: request.env.clone(),
                launcher: BTreeMap::new(),
                agent: request.agent.clone(),
                program: None,
            },
        })
    }

    fn write(&self, child: &str, content: &HandOffContent) -> Result<(), String> {
        self.written
            .lock()
            .unwrap()
            .insert(child.to_string(), content.clone());
        Ok(())
    }

    fn delete(&self, child: &str) -> Result<(), String> {
        self.deleted.lock().unwrap().push(child.to_string());
        self.written.lock().unwrap().remove(child);
        Ok(())
    }
}

/// A fixture whose project is registered and whose owner is minted, its
/// hand-offs `kept`.
async fn with_owner(kept: &Arc<Kept>) -> (Fixture, crate::Api<crate::server::tests::World>) {
    let f = fixture(vec!["calm-quiet-heron"]);
    f.api.resolve_project(resolve(COMMIT, "")).await.unwrap();
    f.mint("amber-quiet-heron").await;
    f.catch_up().await;
    let api = f
        .restarted()
        .await
        .handing_off(Arc::clone(kept) as Arc<dyn HandOffs>);
    (f, api)
}

fn create(owner: &str) -> v1::CreateHandOffRequest {
    v1::CreateHandOffRequest {
        parent: PARENT.to_string(),
        hand_off_id: CHILD.to_string(),
        owner: owner.to_string(),
        agent: "dev".to_string(),
        caller: Some(v1::Caller {
            env: Some(v1::EnvVars {
                vars: vec![v1::EnvVar {
                    name: "CLAUDE_CODE_OAUTH_TOKEN".to_string(),
                    value: "a-token".to_string(), // gitleaks:allow
                }],
            }),
            ..v1::Caller::default()
        }),
        ..v1::CreateHandOffRequest::default()
    }
}

// Step 10 U11: a hand-off's plan reads its project's library, so the
// tree is observed BEFORE the plan reads it, as a read's is (nothing in
// the daemon reads a library tree outside the observation).
#[tokio::test]
async fn a_hand_off_observes_its_tree_before_it_is_planned() {
    let gits = Arc::new(crate::server::tests::Gits::default());
    let kept = Arc::new(Kept {
        credential_set: true,
        scans: Some(Arc::clone(&gits.scans)),
        ..Kept::default()
    });
    let (f, api) = with_owner(&kept).await;
    let api = api.observing(crate::server::tests::observing(&f.world, &gits));
    api.create_hand_off(create("amber-quiet-heron"))
        .await
        .unwrap();
    assert_eq!(*kept.scans_at_plan.lock().unwrap(), vec![1]);
}

// The child is planned at the project's registered root, on the one engine
// a child is hosted on when none is named, for its agent, in the sent
// environment; then its hand-off is written, named under its project.
#[tokio::test]
async fn a_hand_off_is_planned_judged_and_written() {
    let kept = Arc::new(Kept {
        credential_set: true,
        ..Kept::default()
    });
    let (_f, api) = with_owner(&kept).await;
    let made = api
        .create_hand_off(create("amber-quiet-heron"))
        .await
        .unwrap();
    assert_eq!(made.name, format!("{PARENT}/handOffs/{CHILD}"));
    let planned = kept.planned.lock().unwrap().clone();
    let [planned] = planned.as_slice() else {
        panic!("planned once: {planned:?}");
    };
    assert_eq!(planned.root, crate::server::tests::ROOT);
    assert_eq!(planned.engine, "", "none asked: the plan's to resolve");
    // Ruling 4: the engine the child was planned on, its agent's, is named.
    assert_eq!(made.engine, "opencode");
    assert_eq!(planned.agent, "dev");
    assert_eq!(planned.child, CHILD);
    assert_eq!(
        planned
            .env
            .get("CLAUDE_CODE_OAUTH_TOKEN")
            .map(String::as_str),
        Some("a-token")
    );
    let written = kept.written.lock().unwrap().clone();
    assert_eq!(written.get(CHILD).map(|c| c.agent.as_str()), Some("dev"));
}

// An owner nobody recorded is refused in the sessions domain's words, and
// nothing is planned or written.
#[tokio::test]
async fn a_hand_off_for_an_owner_nobody_recorded_is_refused_and_writes_nothing() {
    let kept = Arc::new(Kept {
        credential_set: true,
        ..Kept::default()
    });
    let (_f, api) = with_owner(&kept).await;
    let (code, unknown): (_, sessions_proto::v1::SessionUnknown) =
        refused(api.create_hand_off(create("nobody-ever-ran")).await);
    assert_eq!(unknown.harp, "nobody-ever-ran");
    assert_eq!(
        code,
        crate::ApiRefusal::typed(
            &angzarr_embedded::markup::TypedRefusal::of(&sessions::SessionUnknown {
                harp: String::new()
            }),
            <sessions::SessionUnknown as sessions::Refusal>::REFUSAL_STATUS,
        )
        .code
    );
    assert!(kept.planned.lock().unwrap().is_empty());
    assert!(kept.written.lock().unwrap().is_empty());
}

// A child with no credential is refused as its enqueue would refuse it
// (rulings 53, 73e), before its hand-off is written.
#[tokio::test]
async fn a_hand_off_whose_child_has_no_credential_is_refused_and_writes_nothing() {
    let kept = Arc::new(Kept::default());
    let (_f, api) = with_owner(&kept).await;
    let refused = api.create_hand_off(create("amber-quiet-heron")).await;
    let Err(crate::ApiError::Refused(refusal)) = refused else {
        panic!("refused: {refused:?}");
    };
    assert!(kept.written.lock().unwrap().is_empty());
    let rejection = crate::rejection_of(&refusal.refusal);
    assert_eq!(
        rejection,
        Some(crate::Rejection::Runs(runs::RunRejection::NoCredential {
            engine: "opencode".to_string(),
            variables: "CLAUDE_CODE_OAUTH_TOKEN".to_string(),
        }))
    );
    assert_eq!(
        refusal.code,
        crate::ApiRefusal::typed(
            &angzarr_embedded::markup::TypedRefusal::of(&runs::NoCredential {
                engine: String::new(),
                variables: String::new(),
            }),
            <runs::NoCredential as runs::Refusal>::REFUSAL_STATUS,
        )
        .code
    );
}

// A child that cannot be planned is refused, and nothing is written.
#[tokio::test]
async fn a_hand_off_that_cannot_be_planned_is_refused_and_writes_nothing() {
    let kept = Arc::new(Kept {
        unplanned: true,
        ..Kept::default()
    });
    let (_f, api) = with_owner(&kept).await;
    let (_, unreadable): (_, v1::ConfigUnreadable) =
        refused(api.create_hand_off(create("amber-quiet-heron")).await);
    assert_eq!(unreadable.root, crate::server::tests::ROOT);
    assert!(kept.written.lock().unwrap().is_empty());
}

// A hand-off no open session holds is deleted; one an open session holds is
// its child's, kept.
#[tokio::test]
async fn a_hand_off_is_deleted_only_while_no_open_session_holds_it() {
    let kept = Arc::new(Kept::default());
    let (f, api) = with_owner(&kept).await;
    let deleted = api
        .delete_hand_off(v1::DeleteHandOffRequest {
            name: format!("{PARENT}/handOffs/{CHILD}"),
            ..v1::DeleteHandOffRequest::default()
        })
        .await
        .unwrap();
    assert_eq!(deleted.name, format!("{PARENT}/handOffs/{CHILD}"));
    assert_eq!(*kept.deleted.lock().unwrap(), [CHILD.to_string()]);
    let owner = sessions::SessionName::parse(&format!("{PARENT}/sessions/amber-quiet-heron"))
        .unwrap()
        .uid();
    let _ = f;
    api.delete_hand_off(v1::DeleteHandOffRequest {
        name: format!("{PARENT}/handOffs/{owner}"),
        ..v1::DeleteHandOffRequest::default()
    })
    .await
    .unwrap();
    assert_eq!(
        *kept.deleted.lock().unwrap(),
        [CHILD.to_string()],
        "the open session's is kept"
    );
    let (_, invalid): (_, v1::NameInvalid) = refused(
        api.delete_hand_off(v1::DeleteHandOffRequest {
            name: format!("{PARENT}/sessions/x"),
            ..v1::DeleteHandOffRequest::default()
        })
        .await,
    );
    assert_eq!(invalid.reason, "shape");
}

// Ruling 4: an engine asked is the one the child is planned and judged on,
// and named back.
#[tokio::test]
async fn a_hand_off_on_an_engine_asked_is_planned_on_it() {
    let kept = Arc::new(Kept {
        credential_set: true,
        ..Kept::default()
    });
    let (_f, api) = with_owner(&kept).await;
    let made = api
        .create_hand_off(v1::CreateHandOffRequest {
            engine: "claude-code".to_string(),
            ..create("amber-quiet-heron")
        })
        .await
        .unwrap();
    assert_eq!(made.engine, "claude-code");
    assert_eq!(kept.planned.lock().unwrap()[0].engine, "claude-code");
}

// A launch whose hand-off was made before the drain began, and whose
// `LaunchChild` the drain then refused, withdraws its hand-off: the
// withdrawal starts no work (OD-62 refuses NEW invocations), so the
// draining daemon serves it, and no environment of a child that will never
// start is left for the sweep to find.
#[tokio::test]
async fn a_draining_daemon_withdraws_a_hand_off_made_before_the_drain() {
    use crate::Calls;
    let kept = Arc::new(Kept {
        credential_set: true,
        ..Kept::default()
    });
    let (_f, api) = with_owner(&kept).await;
    let calls: &dyn Calls = &api;
    let made = calls
        .create_hand_off(create("amber-quiet-heron"))
        .await
        .unwrap();
    calls
        .stop_daemon(v1::StopDaemonRequest {
            drain: true,
            ..v1::StopDaemonRequest::default()
        })
        .await
        .unwrap();
    let withdrawn = calls
        .delete_hand_off(v1::DeleteHandOffRequest {
            name: made.name.clone(),
            ..v1::DeleteHandOffRequest::default()
        })
        .await
        .map_err(|error| format!("{error:?}"))
        .expect("a draining daemon withdraws a hand-off");
    assert_eq!(withdrawn.name, made.name);
    assert_eq!(*kept.deleted.lock().unwrap(), [CHILD.to_string()]);
    assert!(kept.written.lock().unwrap().is_empty(), "nothing left");
    let (_, draining): (_, v1::DaemonDraining) =
        refused(calls.create_hand_off(create("amber-quiet-heron")).await);
    assert!(draining.harps.is_empty(), "{draining:?}");
}

// The owner's ruling of 2026-11-17: a hand-off asked for once the daemon's
// home is gone is refused `DaemonHomeGone`, before anything is planned or
// written: nothing is created there.
#[tokio::test]
async fn a_hand_off_asked_for_once_the_home_is_gone_is_refused_and_writes_nothing() {
    use crate::Calls;
    let kept = Arc::new(Kept {
        credential_set: true,
        ..Kept::default()
    });
    let (f, api) = with_owner(&kept).await;
    *f.hosting.home_gone.lock().unwrap() = Some("/home/u/.ctxloom".to_string());
    let calls: &dyn Calls = &api;
    let (_, gone): (_, v1::DaemonHomeGone) =
        refused(calls.create_hand_off(create("amber-quiet-heron")).await);
    assert_eq!(gone.home, "/home/u/.ctxloom");
    assert!(kept.planned.lock().unwrap().is_empty(), "planned");
    assert!(kept.written.lock().unwrap().is_empty(), "written");
}

// ---------------------------------------- step 8 U2: trust and the target

/// The second project, `api`, as `ResolveProject` registers it.
const API: &str = "projects/bold-api-pine";
const API_ROOT: &str = "/work/api";

/// A fixture whose two projects are registered (this one and `api`), whose
/// owner is minted in this one, its hand-offs `kept`, its trusts read from
/// a warm index: `trusted` names the project ids the user has trusted.
async fn two_projects(
    kept: &Arc<Kept>,
    trusted: &[&str],
) -> (Fixture, crate::Api<crate::server::tests::World>) {
    let f = fixture(vec!["calm-quiet-heron", "bold-api-pine"]);
    f.api.resolve_project(resolve(COMMIT, "")).await.unwrap();
    f.api
        .resolve_project(v1::ResolveProjectRequest {
            caller: Some(v1::Caller {
                cwd: API_ROOT.to_string(),
                ..v1::Caller::default()
            }),
            project_id: "api-otter".to_string(),
            root: API_ROOT.to_string(),
            ..v1::ResolveProjectRequest::default()
        })
        .await
        .unwrap();
    f.mint("amber-quiet-heron").await;
    let index = trust_index::TrustIndexes::default();
    for (n, id) in trusted.iter().enumerate() {
        let harp = match *id {
            "api-otter" => "bold-api-pine",
            _ => "calm-quiet-heron",
        };
        let name = format!("projects/{harp}/trust");
        let grant = trust::GrantTrust {
            name: name.clone(),
            project_id: (*id).to_string(),
            etag: "e".to_string(),
            current_etag: "e".to_string(),
            actor: trust::Actor {
                user: "alice".to_string(),
            },
        };
        let cover = angzarr_embedded::v1::Cover {
            domain: trust::TRUST_DOMAIN.to_string(),
            root: Some(trust::trust_root(&name)),
            edition: None,
        };
        f.world
            .handle(sessions::keyed_book(cover, &grant, &format!("grant-{n}")))
            .await
            .unwrap();
    }
    let mut caught = trust_index::caught_up(index.clone());
    caught
        .step(&f.world.store, f.world.registry.catalog())
        .await
        .unwrap();
    f.catch_up().await;
    let api = f
        .restarted()
        .await
        .handing_off(Arc::clone(kept) as Arc<dyn HandOffs>)
        .trusting(crate::Trusting {
            trusts: Arc::new(NoSettings),
            index,
        });
    (f, api)
}

struct NoSettings;

impl crate::Trusts for NoSettings {
    fn settings(&self, _: &str) -> Result<Vec<v1::TrustSetting>, crate::ApiError> {
        Ok(Vec::new())
    }
}

fn into(project: &str) -> v1::CreateHandOffRequest {
    v1::CreateHandOffRequest {
        project: project.to_string(),
        ..create("amber-quiet-heron")
    }
}

// The child of an untrusted project is planned untrusted, and the
// untrusted settings its launch reads refuse it as its enqueue would, before
// its hand-off is written.
#[tokio::test]
async fn a_hand_off_reading_untrusted_settings_is_refused_and_writes_nothing() {
    let kept = Arc::new(Kept {
        credential_set: true,
        untrusted: vec!["engines.opencode.command".to_string()],
        ..Kept::default()
    });
    let (_f, api) = two_projects(&kept, &[]).await;
    let refused = api.create_hand_off(create("amber-quiet-heron")).await;
    let Err(crate::ApiError::Refused(refusal)) = refused else {
        panic!("refused: {refused:?}");
    };
    assert_eq!(
        crate::rejection_of(&refusal.refusal),
        Some(crate::Rejection::Runs(
            runs::RunRejection::UntrustedProjectSettings {
                paths: "engines.opencode.command".to_string(),
            }
        ))
    );
    assert!(!kept.planned.lock().unwrap()[0].trusted);
    assert!(kept.written.lock().unwrap().is_empty());
}

// A trusted project's child is planned trusted (S23).
#[tokio::test]
async fn a_trusted_projects_child_is_planned_trusted() {
    let kept = Arc::new(Kept {
        credential_set: true,
        ..Kept::default()
    });
    let (_f, api) = two_projects(&kept, &[crate::server::tests::PROJECT]).await;
    let made = api
        .create_hand_off(create("amber-quiet-heron"))
        .await
        .unwrap();
    assert_eq!(made.project, PARENT);
    assert!(kept.planned.lock().unwrap()[0].trusted);
}

// D8, Q1: a child launched into another project the user has not trusted
// is refused naming that project's root, before anything is planned or
// written.
#[tokio::test]
async fn a_hand_off_into_an_untrusted_project_is_refused_and_writes_nothing() {
    let kept = Arc::new(Kept {
        credential_set: true,
        ..Kept::default()
    });
    let (_f, api) = two_projects(&kept, &[crate::server::tests::PROJECT]).await;
    let refused = api.create_hand_off(into(API)).await;
    let Err(crate::ApiError::Refused(refusal)) = refused else {
        panic!("refused: {refused:?}");
    };
    assert_eq!(
        crate::rejection_of(&refusal.refusal),
        Some(crate::Rejection::Sessions(
            sessions::SessionRejection::UntrustedTarget {
                root: API_ROOT.to_string(),
            }
        ))
    );
    assert!(kept.planned.lock().unwrap().is_empty());
    assert!(kept.written.lock().unwrap().is_empty());
}

// Once the user trusts it, the child is planned at that project's root,
// trusted, and the hand-off names the project it works in.
#[tokio::test]
async fn a_hand_off_into_a_trusted_project_is_planned_there() {
    let kept = Arc::new(Kept {
        credential_set: true,
        ..Kept::default()
    });
    let (_f, api) = two_projects(&kept, &["api-otter"]).await;
    let made = api.create_hand_off(into(API)).await.unwrap();
    assert_eq!(made.project, API);
    assert_eq!(made.name, format!("{PARENT}/handOffs/{CHILD}"));
    let planned = kept.planned.lock().unwrap().clone();
    assert_eq!(planned[0].root, API_ROOT);
    assert!(planned[0].trusted);
    // Naming its own project is no other project.
    let (_f, api) = two_projects(&kept, &[]).await;
    assert!(api.create_hand_off(into(PARENT)).await.is_ok());
}

// A project nobody registered is not found, and nothing is written.
#[tokio::test]
async fn a_hand_off_into_a_project_nobody_registered_is_not_found() {
    let kept = Arc::new(Kept {
        credential_set: true,
        ..Kept::default()
    });
    let (_f, api) = two_projects(&kept, &[]).await;
    let (_, missing): (_, v1::ProjectNotFound) =
        refused(api.create_hand_off(into("projects/far-away-heron")).await);
    assert_eq!(missing.name, "projects/far-away-heron");
    assert!(kept.written.lock().unwrap().is_empty());
}

// Review 4: a hand-off's environments may hold credentials; their Debug
// names the variables and shows no value.
#[test]
fn test_a_hand_off_s_debug_names_its_variables_and_shows_no_value() {
    let env = BTreeMap::from([("TOKEN".to_string(), "sk-not-shown".to_string())]); // gitleaks:allow
    let launcher = BTreeMap::from([("SETTINGS".to_string(), "hidden-too".to_string())]);
    let handing = HandingOff {
        root: "/work/demo".to_string(),
        trusted: true,
        child: CHILD.to_string(),
        engine: "claude-code".to_string(),
        agent: String::new(),
        env: env.clone(),
    };
    let content = HandOffContent {
        env,
        launcher,
        ..HandOffContent::default()
    };
    for shown in [format!("{handing:?}"), format!("{content:?}")] {
        assert!(shown.contains("TOKEN"), "{shown}");
        assert!(!shown.contains("sk-not-shown"), "{shown}");
        assert!(!shown.contains("hidden-too"), "{shown}");
    }
    assert!(format!("{handing:?}").contains("/work/demo"));
    assert!(format!("{content:?}").contains("SETTINGS"));
}
