//! `CreateHandOff` (slice 4 U3, T6, S10) over the real aggregates and the
//! warm index: the owner and the credential are checked, nothing is kept, and
//! what the caller needs for its `LaunchChild` comes back, the secrets sealed.

use std::collections::BTreeMap;
use std::sync::{Arc, Mutex};

use daemon_proto::v1;

use crate::server::tests::{fixture, refused, resolve, Fixture, COMMIT, PROJECT};
use crate::{HandOffContent, HandingOff, LaunchPlans, Planned};

const PARENT: &str = "projects/calm-quiet-heron";
const CHILD: &str = "5f0c7a52-1111-5111-8111-000000000002";

/// The plans a test scripts: a plan whose credential is or is not set.
#[derive(Default)]
struct Kept {
    planned: Mutex<Vec<HandingOff>>,
    credential_set: bool,
    unplanned: bool,
    /// The untrusted settings the plan says its launch reads.
    untrusted: Vec<String>,
    /// The fatal findings the plan has.
    fatal: Vec<crate::FatalFinding>,
    /// The observation's scans, counted; how many there had been at each
    /// plan.
    scans: Option<Arc<std::sync::atomic::AtomicU32>>,
    scans_at_plan: Mutex<Vec<u32>>,
}

impl LaunchPlans for Kept {
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
            fatal_findings: u32::try_from(self.fatal.len()).unwrap(),
            fatal: self.fatal.clone(),
            untrusted_settings: self.untrusted.clone(),
            content: HandOffContent {
                env: request.env.clone(),
                launcher: BTreeMap::new(),
                agent: request.agent.clone(),
            },
        })
    }
}

/// A fixture whose project is registered and whose owner is minted, its
/// plans `kept`.
async fn with_owner(kept: &Arc<Kept>) -> (Fixture, crate::Api<crate::server::tests::World>) {
    let f = fixture(vec!["calm-quiet-heron"]);
    f.api.resolve_project(resolve(COMMIT, "")).await.unwrap();
    f.mint("amber-quiet-heron").await;
    f.catch_up().await;
    let api = f.restarted().await.handing_off(
        Arc::clone(kept) as Arc<dyn LaunchPlans>,
        Arc::new(run_ports::TestSealing),
    );
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
// environment, and answered under its project, with the inputs its
// `LaunchChild` records.
#[tokio::test]
async fn a_hand_off_is_planned_and_judged_and_answers_the_launchs_inputs() {
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
}

// An owner nobody recorded is refused in the sessions domain's words, and
// nothing is planned or written.
#[tokio::test]
async fn a_hand_off_for_an_owner_nobody_recorded_is_refused() {
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
}

// A child with no credential is refused as its enqueue would refuse it
// (rulings 53, 73e), before its hand-off is written.
#[tokio::test]
async fn a_hand_off_whose_child_has_no_credential_is_refused() {
    let kept = Arc::new(Kept::default());
    let (_f, api) = with_owner(&kept).await;
    let refused = api.create_hand_off(create("amber-quiet-heron")).await;
    let Err(crate::ApiError::Refused(refusal)) = refused else {
        panic!("refused: {refused:?}");
    };
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
async fn a_hand_off_that_cannot_be_planned_is_refused() {
    let kept = Arc::new(Kept {
        unplanned: true,
        ..Kept::default()
    });
    let (_f, api) = with_owner(&kept).await;
    let (_, unreadable): (_, v1::ConfigUnreadable) =
        refused(api.create_hand_off(create("amber-quiet-heron")).await);
    assert_eq!(unreadable.root, crate::server::tests::ROOT);
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

// What comes back for the owner's `LaunchChild`: each variable the child may
// be given sealed to the daemon's key and bound to its name (a secret is
// never in the answer in the clear), and where her settings are, plain.
#[tokio::test]
async fn a_hand_offs_answer_seals_its_environment_and_keeps_nothing() {
    use run_ports::Sealing as _;
    let kept = Arc::new(Kept {
        credential_set: true,
        ..Kept::default()
    });
    let (_f, api) = with_owner(&kept).await;
    let made = api
        .create_hand_off(create("amber-quiet-heron"))
        .await
        .unwrap();
    let [token] = made.env.as_slice() else {
        panic!("one variable: {:?}", made.env);
    };
    assert_eq!(token.name, "CLAUDE_CODE_OAUTH_TOKEN");
    assert!(token.plain.is_empty());
    assert!(!String::from_utf8_lossy(&token.sealed).contains("a-token"));
    let opened = run_ports::TestSealing
        .open(
            PROJECT,
            &daemon_application::child_env_field("CLAUDE_CODE_OAUTH_TOKEN"),
            &token.sealed,
        )
        .unwrap();
    assert_eq!(opened, b"a-token");
}

// A daemon with no way to seal refuses to answer a launch's inputs rather
// than send a secret in the clear.
#[tokio::test]
async fn a_daemon_that_seals_nothing_answers_no_hand_off() {
    let f = fixture(vec!["calm-quiet-heron"]);
    f.api.resolve_project(resolve(COMMIT, "")).await.unwrap();
    f.mint("amber-quiet-heron").await;
    f.catch_up().await;
    let api = f.restarted().await;
    let failed = api.create_hand_off(create("amber-quiet-heron")).await;
    assert!(
        matches!(failed, Err(crate::ApiError::Failed { .. })),
        "{failed:?}"
    );
}
