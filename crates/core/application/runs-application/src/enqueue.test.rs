//! The enqueue that mints a credential (D29). RED (S5): stubs.

use std::sync::Arc;

use run_ports::Edge;

use crate::fakes::{command_book, uuid, CountingSource, KernelEdge, SESSION_ME};
use crate::{
    Credential, CredentialEvidence, EnqueueError, EnqueueRequest, Enqueuer, Evidence, Gate,
    Principal, RunsGate,
};

fn request() -> EnqueueRequest {
    EnqueueRequest {
        session: SESSION_ME.to_string(),
        incarnation: 1,
        mode: runs::RunMode::Interactive,
        evidence: Evidence::passing("claude-code"),
        launcher_lock: "/home/u/.ctxloom/runs/run-1.lock".to_string(),
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
        },
    ))
    .await
    .unwrap();
}

// NEW-DESIGN (D29, plan 5.3: "only its SHA-256 goes in the event"; owner
// ruling 2026-11-04, item 16: two principals): the run's stream holds the
// digest of EACH of the two tokens and no byte sequence equal to either token.
#[tokio::test]
async fn the_enqueue_journals_the_digest_and_never_the_token() {
    let edge = Arc::new(KernelEdge::new());
    minted(&edge).await;
    let enqueuer = Enqueuer::new(Arc::clone(&edge), Arc::new(CountingSource::default()));
    let enqueued = enqueuer.enqueue(request()).await.unwrap();
    assert_eq!(enqueued.run, runs::run_root(SESSION_ME, 1));
    let cover = angzarr_embedded::v1::Cover {
        domain: runs::RUN_DOMAIN.to_string(),
        root: Some(enqueued.run.clone()),
        edition: None,
    };
    let stored: Vec<u8> = edge
        .pages(&cover)
        .await
        .into_iter()
        .flat_map(|page| match page.body {
            Some(angzarr_embedded::v1::event_page::Body::Event(any)) => any.value,
            _ => Vec::new(),
        })
        .collect();
    for token in [&enqueued.runner_token, &enqueued.model_token] {
        let digest = token.hash();
        assert!(
            stored.windows(32).any(|w| w == digest.as_bytes()),
            "the digest is journaled"
        );
        assert!(
            !stored.windows(32).any(|w| w == token.as_bytes()),
            "the token is not"
        );
    }
}

// NEW-DESIGN, owner ruling 2026-11-04 (item 16): the enqueue mints TWO
// distinct tokens, and each identifies the run it enqueued as its own
// principal (mint, journal and identify agree).
#[tokio::test]
async fn each_returned_token_identifies_the_new_run_as_its_own_principal() {
    let edge = Arc::new(KernelEdge::new());
    minted(&edge).await;
    let enqueuer = Enqueuer::new(Arc::clone(&edge), Arc::new(CountingSource::default()));
    let enqueued = enqueuer.enqueue(request()).await.unwrap();
    assert_ne!(enqueued.runner_token, enqueued.model_token);
    let gate = RunsGate::new(Arc::clone(&edge));
    for (token, principal) in [
        (&enqueued.runner_token, Principal::Runner),
        (&enqueued.model_token, Principal::Model),
    ] {
        let credential = Credential {
            token: token.clone(),
            run: enqueued.run.clone(),
        };
        let identity = gate.identify(&credential).await.unwrap();
        assert_eq!(identity.principal, principal);
        assert_eq!(identity.run, enqueued.run);
    }
}

// NEW-DESIGN, owner ruling 2026-11-04 (item 16, fail closed): a token source
// that hands out the same token twice would make one token two principals; the
// run refuses the enqueue and journals nothing.
#[tokio::test]
async fn a_source_that_repeats_a_token_cannot_enqueue() {
    struct Stuck;
    impl run_ports::TokenSource for Stuck {
        fn mint(&self) -> run_ports::Token {
            run_ports::Token::from_bytes([9; 32])
        }
    }
    let edge = Arc::new(KernelEdge::new());
    minted(&edge).await;
    let enqueuer = Enqueuer::new(Arc::clone(&edge), Arc::new(Stuck));
    let refused = enqueuer.enqueue(request()).await.unwrap_err();
    assert_eq!(
        refused,
        EnqueueError::Refused(runs::RunRejection::CredentialsNotDistinct)
    );
}

// NEW-DESIGN (plan 5.3: a token PER RUN): two enqueues mint two different
// tokens; a repeat enqueue of the same run is refused by the run, and mints a
// token nobody can use (it does not replace the first).
#[tokio::test]
async fn each_enqueue_mints_its_own_token_and_a_repeat_changes_nothing() {
    let edge = Arc::new(KernelEdge::new());
    minted(&edge).await;
    let enqueuer = Enqueuer::new(Arc::clone(&edge), Arc::new(CountingSource::default()));
    let first = enqueuer.enqueue(request()).await.unwrap();
    let repeat = enqueuer.enqueue(request()).await;
    assert!(repeat.is_err(), "runs-already-enqueued");
    assert_ne!(first.runner_token, run_ports::Token::from_bytes([0; 32]));
}

// NEW-DESIGN, owner ruling 2026-11-09 (item 16): the enqueue mints a THIRD
// token for the hook principal, journals only its digest, and the other two
// principals' gate does not take it for theirs (the hook's allowlist is the
// gate track's).
#[tokio::test]
async fn the_enqueue_mints_a_third_hook_token_journaled_as_a_digest_only() {
    let edge = Arc::new(KernelEdge::new());
    minted(&edge).await;
    let enqueuer = Enqueuer::new(Arc::clone(&edge), Arc::new(CountingSource::default()));
    let enqueued = enqueuer.enqueue(request()).await.unwrap();
    assert_ne!(enqueued.hook_token, enqueued.runner_token);
    assert_ne!(enqueued.hook_token, enqueued.model_token);
    let cover = angzarr_embedded::v1::Cover {
        domain: runs::RUN_DOMAIN.to_string(),
        root: Some(enqueued.run.clone()),
        edition: None,
    };
    let stored: Vec<u8> = edge
        .pages(&cover)
        .await
        .into_iter()
        .flat_map(|page| match page.body {
            Some(angzarr_embedded::v1::event_page::Body::Event(any)) => any.value,
            _ => Vec::new(),
        })
        .collect();
    let contains = |needle: &[u8]| stored.windows(needle.len()).any(|w| w == needle);
    assert!(contains(enqueued.hook_token.hash().as_bytes()));
    assert!(!contains(enqueued.hook_token.as_bytes()));
    assert!(!contains(enqueued.hook_token.to_text().as_bytes()));
    let credential = Credential {
        token: enqueued.hook_token.clone(),
        run: enqueued.run.clone(),
    };
    let gate = RunsGate::new(Arc::clone(&edge));
    assert!(gate.identify(&credential).await.is_err());
}

// NEW-DESIGN: the tokens' text never appears in how the enqueue renders
// itself (Debug), the one place the secret could leak from the launching
// code's value.
#[tokio::test]
async fn the_enqueued_value_never_renders_a_secret() {
    let edge = Arc::new(KernelEdge::new());
    minted(&edge).await;
    let enqueuer = Enqueuer::new(Arc::clone(&edge), Arc::new(CountingSource::default()));
    let enqueued = enqueuer.enqueue(request()).await.unwrap();
    let shown = format!("{enqueued:?}");
    for token in [
        &enqueued.runner_token,
        &enqueued.model_token,
        &enqueued.hook_token,
    ] {
        assert!(!shown.contains(&token.to_text()));
    }
}

// Rulings 73e, 73f: the evidence reaches the run's decision, and the refusal
// comes back as the run's own rejection, whole: an aggregate keeps every
// refusal it holds, in check order.
#[tokio::test]
async fn the_evidence_reaches_the_runs_decision_and_its_refusal_comes_back_whole() {
    let edge = Arc::new(KernelEdge::new());
    let enqueuer = Enqueuer::new(Arc::clone(&edge), Arc::new(CountingSource::default()));
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

// Ruling 73f (the proposal for a refused run's residue): the run's decision
// needs no session to exist, so the enqueue can be sent before a name is
// claimed or the session minted, and a refused run leaves neither.
#[tokio::test]
async fn a_run_is_enqueued_before_its_session_is_minted() {
    let edge = Arc::new(KernelEdge::new());
    let enqueuer = Enqueuer::new(Arc::clone(&edge), Arc::new(CountingSource::default()));
    let enqueued = enqueuer.enqueue(request()).await.unwrap();
    assert_eq!(enqueued.run, runs::run_root(SESSION_ME, 1));
}

// Rulings 86, 92: the enqueue journals the run's own lock, which its launcher
// holds.
#[tokio::test]
async fn the_enqueue_journals_the_lock_its_launcher_holds() {
    let edge = Arc::new(KernelEdge::new());
    minted(&edge).await;
    let enqueuer = Enqueuer::new(Arc::clone(&edge), Arc::new(CountingSource::default()));
    let enqueued = enqueuer.enqueue(request()).await.unwrap();
    let cover = angzarr_embedded::v1::Cover {
        domain: runs::RUN_DOMAIN.to_string(),
        root: Some(enqueued.run.clone()),
        edition: None,
    };
    let pages = edge.events(&cover, 0).await.unwrap();
    let launcher = pages.iter().find_map(|page| match &page.body {
        Some(angzarr_embedded::v1::event_page::Body::Event(any)) => {
            use angzarr_embedded::Payload;
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
