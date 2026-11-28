use std::any::Any;
use std::path::Path;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::Arc;

use file_system::Perms;
use run_ports::{KeyInfo, KeySet, ProjectEvidence, ProjectFacts, ProjectSecrets, SealingError};

use super::{Cached, Registered};
use crate::fixtures;

/// Counts how often its keys are read, and makes a distinct set each time.
struct Reading(AtomicUsize);

impl ProjectSecrets for Reading {
    fn key_set(&self, _: &str, _: &ProjectEvidence) -> Result<KeySet, SealingError> {
        let n = self.0.fetch_add(1, Ordering::SeqCst);
        let material: Arc<dyn Any + Send + Sync> = Arc::new(());
        Ok(KeySet::new(
            KeyInfo {
                id: format!("k-{n}"),
                fingerprint: String::new(),
            },
            Vec::new(),
            material,
        ))
    }
}

fn write(root: &Path, text: &str) {
    fixtures::fs()
        .write_atomic(
            &root.join(".env"),
            text.as_bytes(),
            Perms::Exact(os::Mode::from_bits(0o600)),
        )
        .unwrap();
}

#[test]
fn a_projects_key_set_is_read_once_until_its_env_or_its_trust_moves() {
    let root = fixtures::root();
    write(root.root(), "CTXLOOM_SEAL_KEY=a\n");
    let inner = Arc::new(Reading(AtomicUsize::new(0)));
    let cached = Cached::new(
        fixtures::fs(),
        Arc::clone(&inner) as Arc<dyn ProjectSecrets>,
    );
    let evidence = ProjectEvidence {
        root: root.root().display().to_string(),
        trusted: true,
    };
    let first = cached.key_set("demo", &evidence).unwrap();
    assert_eq!(
        cached.key_set("demo", &evidence).unwrap().current(),
        first.current(),
        "the same set, read once"
    );
    assert_eq!(inner.0.load(Ordering::SeqCst), 1);
    let revoked = ProjectEvidence {
        trusted: false,
        ..evidence.clone()
    };
    cached.key_set("demo", &revoked).unwrap();
    assert_eq!(
        inner.0.load(Ordering::SeqCst),
        2,
        "a moved trust reads again"
    );
    write(root.root(), "CTXLOOM_SEAL_KEY=a-longer-value\n");
    cached.key_set("demo", &revoked).unwrap();
    assert_eq!(
        inner.0.load(Ordering::SeqCst),
        3,
        "a changed file reads again"
    );
}

#[test]
fn a_project_the_daemon_has_not_registered_has_no_evidence() {
    let registered = Registered {
        index: session_index::SessionIndexes::default(),
        trusts: trust_index::TrustIndexes::default(),
    };
    assert_eq!(registered.evidence("calm-otter"), None);
}
