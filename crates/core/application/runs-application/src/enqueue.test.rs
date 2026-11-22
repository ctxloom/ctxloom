//! The run's enqueue, which mints no credential (owner ruling s4-Q3).

use std::sync::Arc;

use angzarr_embedded::Payload;
use run_ports::Edge;

use crate::fakes::{command_book, uuid, KernelEdge, SESSION_ME};
use crate::{CredentialEvidence, EnqueueError, EnqueueRequest, Enqueuer, Evidence};

fn request() -> EnqueueRequest {
    EnqueueRequest {
        session: SESSION_ME.to_string(),
        incarnation: 1,
        mode: runs::RunMode::Interactive,
        evidence: Evidence::passing("claude-code"),
        launcher_lock: "/home/u/.ctxloom/runs/run-1.lock".to_string(),
        grant: None,
    }
}

fn session_cover() -> angzarr_embedded::v1::Cover {
    angzarr_embedded::v1::Cover {
        domain: sessions::SESSION_DOMAIN.to_string(),
        root: Some(uuid(SESSION_ME)),
        edition: None,
    }
}

async fn minted(edge: &KernelEdge) {
    edge.handle(command_book(
        session_cover(),
        sessions::MintSession {
            harp: "amber-swift-owl".to_string(),
            project_id: "calm-otter".to_string(),
            project_dir: "/w".to_string(),
            output_dir: "/o".to_string(),
            engine: "claude-code".to_string(),
            origin: sessions::Origin::Session,
            started_at: None,
            depth: 0,
            owner: None,
            workspace: None,
        },
    ))
    .await
    .unwrap();
}

// Owner ruling s4-Q3: the enqueue journals the run (its session, its
// incarnation, its mode) and mints no credential; a repeat enqueue of the
// same run is refused by the run and journals nothing more.
#[tokio::test]
async fn the_enqueue_journals_the_run_once_and_no_credential() {
    let edge = Arc::new(KernelEdge::new());
    minted(&edge).await;
    let enqueuer = Enqueuer::new(Arc::clone(&edge));
    let enqueued = enqueuer.enqueue(request()).await.unwrap();
    assert_eq!(enqueued.run, runs::run_root(SESSION_ME, 1));
    let repeat = enqueuer.enqueue(request()).await;
    assert!(repeat.is_err(), "runs-already-enqueued");
    let cover = angzarr_embedded::v1::Cover {
        domain: runs::RUN_DOMAIN.to_string(),
        root: Some(enqueued.run.clone()),
        edition: None,
    };
    let pages = edge.pages(&cover).await;
    let [page] = pages.as_slice() else {
        panic!("one page: {pages:?}");
    };
    let Some(angzarr_embedded::v1::event_page::Body::Event(any)) = &page.body else {
        panic!("an event: {page:?}");
    };
    let Ok(runs::RunEvent::RunEnqueued(event)) = runs::RunEvent::from_any(any) else {
        panic!("a RunEnqueued: {any:?}");
    };
    assert_eq!(
        (event.session.as_str(), event.incarnation, event.mode),
        (SESSION_ME, 1, request().mode)
    );
}

// Rulings 73e, 73f: the evidence reaches the run's decision, and the refusal
// comes back as the run's own rejection, whole: an aggregate keeps every
// refusal it holds, in check order.
#[tokio::test]
async fn the_evidence_reaches_the_runs_decision_and_its_refusal_comes_back_whole() {
    let edge = Arc::new(KernelEdge::new());
    let enqueuer = Enqueuer::new(Arc::clone(&edge));
    let request = EnqueueRequest {
        mode: runs::RunMode::OneShot,
        evidence: Evidence {
            prompt_given: false,
            credential: Some(CredentialEvidence {
                accepted_variables: vec!["ANTHROPIC_API_KEY".to_string()],
                ..CredentialEvidence::default()
            }),
            ..Evidence::passing("claude-code")
        },
        ..request()
    };
    let refused = enqueuer.enqueue(request).await.unwrap_err();
    assert_eq!(
        refused,
        EnqueueError::Refused(runs::RunRejection::LaunchRefused {
            refusals: vec![
                runs::RunRejection::NoPrompt,
                runs::RunRejection::NoCredential {
                    engine: "claude-code".to_string(),
                    variables: "ANTHROPIC_API_KEY".to_string(),
                },
            ],
            fatal_findings: 0,
            findings: Vec::new(),
        })
    );
    let cover = angzarr_embedded::v1::Cover {
        domain: runs::RUN_DOMAIN.to_string(),
        root: Some(runs::run_root(SESSION_ME, 1)),
        edition: None,
    };
    assert!(
        edge.pages(&cover).await.is_empty(),
        "a refusal journals nothing"
    );
}

// Step 8 U2: the untrusted settings of the evidence reach the run's
// decision, refused by their paths, and nothing is journaled.
#[tokio::test]
async fn the_untrusted_settings_reach_the_runs_decision() {
    let edge = Arc::new(KernelEdge::new());
    let enqueuer = Enqueuer::new(Arc::clone(&edge));
    let request = EnqueueRequest {
        mode: runs::RunMode::OneShot,
        evidence: Evidence {
            untrusted_settings: vec!["engines.claude-code.command".to_string()],
            ..Evidence::passing("claude-code")
        },
        ..request()
    };
    let refused = enqueuer.enqueue(request).await.unwrap_err();
    assert_eq!(
        refused,
        EnqueueError::Refused(runs::RunRejection::UntrustedProjectSettings {
            paths: "engines.claude-code.command".to_string(),
        })
    );
}

// Ruling 73f (the proposal for a refused run's residue): the run's decision
// needs no session to exist, so the enqueue can be sent before a name is
// claimed or the session minted, and a refused run leaves neither.
#[tokio::test]
async fn a_run_is_enqueued_before_its_session_is_minted() {
    let edge = Arc::new(KernelEdge::new());
    let enqueuer = Enqueuer::new(Arc::clone(&edge));
    let enqueued = enqueuer.enqueue(request()).await.unwrap();
    assert_eq!(enqueued.run, runs::run_root(SESSION_ME, 1));
}

// Rulings 86, 92: the enqueue journals the run's own lock, which its launcher
// holds.
#[tokio::test]
async fn the_enqueue_journals_the_lock_its_launcher_holds() {
    let edge = Arc::new(KernelEdge::new());
    minted(&edge).await;
    let enqueuer = Enqueuer::new(Arc::clone(&edge));
    let enqueued = enqueuer.enqueue(request()).await.unwrap();
    let cover = angzarr_embedded::v1::Cover {
        domain: runs::RUN_DOMAIN.to_string(),
        root: Some(enqueued.run.clone()),
        edition: None,
    };
    let pages = edge.events(&cover, 0).await.unwrap();
    let launcher = pages.iter().find_map(|page| match &page.body {
        Some(angzarr_embedded::v1::event_page::Body::Event(any)) => {
            match runs::RunEvent::from_any(any) {
                Ok(runs::RunEvent::RunEnqueued(enqueued)) => Some(enqueued.launcher_lock),
                _ => None,
            }
        }
        _ => None,
    });
    assert_eq!(
        launcher.as_deref(),
        Some("/home/u/.ctxloom/runs/run-1.lock")
    );
}

// Step 9 (owner ruling 2026-11-17): what a run is given is journaled in its
// RunEnqueued, as the request says it.
#[tokio::test]
async fn the_enqueue_journals_what_the_run_was_given() {
    let edge = Arc::new(KernelEdge::new());
    minted(&edge).await;
    let enqueuer = Enqueuer::new(Arc::clone(&edge));
    let grant = runs::Grant {
        mcp_servers: vec!["ctxloom".to_string(), "review-tools".to_string()],
        permissions: r#"{"defaultMode":"plan"}"#.to_string(),
    };
    let enqueued = enqueuer
        .enqueue(EnqueueRequest {
            grant: Some(grant.clone()),
            ..request()
        })
        .await
        .unwrap();
    let cover = angzarr_embedded::v1::Cover {
        domain: runs::RUN_DOMAIN.to_string(),
        root: Some(enqueued.run.clone()),
        edition: None,
    };
    let pages = edge.events(&cover, 0).await.unwrap();
    let journaled = pages.iter().find_map(|page| match &page.body {
        Some(angzarr_embedded::v1::event_page::Body::Event(any)) => {
            match runs::RunEvent::from_any(any) {
                Ok(runs::RunEvent::RunEnqueued(enqueued)) => Some(enqueued.grant),
                _ => None,
            }
        }
        _ => None,
    });
    assert_eq!(journaled, Some(Some(grant)));
}

// Owner ruling 2026-11-21: the findings of the evidence are the enqueue's
// refusal's, beside their count, as the hand-off's pre-check holds them.
#[tokio::test]
async fn the_fatal_findings_of_the_evidence_come_back_in_the_refusal() {
    let edge = Arc::new(KernelEdge::new());
    let enqueuer = Enqueuer::new(Arc::clone(&edge));
    let request = EnqueueRequest {
        evidence: Evidence {
            fatal_findings: 1,
            fatal: vec![runs::LaunchFinding {
                text: "failed to load bundle".to_string(),
                remedy: "run `ctxloom deps pull`".to_string(),
            }],
            ..Evidence::passing("claude-code")
        },
        ..request()
    };
    let refused = enqueuer.enqueue(request).await.unwrap_err();
    assert_eq!(
        refused,
        EnqueueError::Refused(runs::RunRejection::LaunchRefused {
            refusals: Vec::new(),
            fatal_findings: 1,
            findings: vec![runs::LaunchFinding {
                text: "failed to load bundle".to_string(),
                remedy: "run `ctxloom deps pull`".to_string(),
            }],
        })
    );
}
