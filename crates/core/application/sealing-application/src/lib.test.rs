use std::any::Any;
use std::sync::{Arc, Mutex};

use run_ports::{
    KeyInfo, KeySet, KeysSeen, ProjectEvidence, ProjectFacts, ProjectSecrets, Sealer, Sealing,
    SealingError,
};

use super::ProjectSealing;

fn info(id: &str) -> KeyInfo {
    KeyInfo {
        id: id.to_string(),
        fingerprint: format!("fp-{id}"),
    }
}

/// One registered project, `demo`, trusted or not.
struct Facts {
    trusted: bool,
}

impl ProjectFacts for Facts {
    fn evidence(&self, project: &str) -> Option<ProjectEvidence> {
        (project == "demo").then(|| ProjectEvidence {
            root: "/work/demo".to_string(),
            trusted: self.trusted,
        })
    }
}

/// The same key set every time it is asked, as the root's cache gives it;
/// its judgement is the evidence's.
struct Secrets(KeySet);

impl ProjectSecrets for Secrets {
    fn key_set(&self, project: &str, evidence: &ProjectEvidence) -> Result<KeySet, SealingError> {
        match evidence.trusted {
            true => Ok(self.0.clone()),
            false => Err(SealingError::NoKey {
                project: project.to_string(),
                file: format!("{}/.env", evidence.root),
            }),
        }
    }
}

/// Seals by tagging with the set's current id; opens what carries a held id.
struct Tagging;

impl Sealer for Tagging {
    fn seal(&self, keys: &KeySet, field: &str, plain: &[u8]) -> Result<Vec<u8>, SealingError> {
        let mut out = format!("{}|{field}|", keys.current().id).into_bytes();
        out.extend_from_slice(plain);
        Ok(out)
    }

    fn open(&self, keys: &KeySet, field: &str, sealed: &[u8]) -> Result<Vec<u8>, SealingError> {
        let text = String::from_utf8_lossy(sealed).into_owned();
        let mut parts = text.splitn(3, '|');
        let (id, tagged, rest) = (
            parts.next().unwrap_or_default(),
            parts.next().unwrap_or_default(),
            parts.next().unwrap_or_default(),
        );
        let held = keys.current().id == id || keys.earlier().iter().any(|k| k.id == id);
        match (held, tagged == field) {
            (false, _) => Err(SealingError::UnknownKey {
                project: String::new(),
                key_id: id.to_string(),
            }),
            (true, false) => Err(SealingError::Open {
                field: field.to_string(),
                ext_err_txt: "another field".to_string(),
            }),
            (true, true) => Ok(rest.as_bytes().to_vec()),
        }
    }
}

#[derive(Default)]
struct Told(Mutex<Vec<(String, String)>>);

impl KeysSeen for Told {
    fn seen(&self, project: &str, keys: &KeySet) {
        self.0
            .lock()
            .unwrap()
            .push((project.to_string(), keys.current().id.clone()));
    }
}

fn set(id: &str, earlier: &[&str]) -> KeySet {
    let material: Arc<dyn Any + Send + Sync> = Arc::new(());
    KeySet::new(
        info(id),
        earlier.iter().map(|e| info(e)).collect(),
        material,
    )
}

fn sealing(trusted: bool, keys: KeySet) -> (ProjectSealing, Arc<Told>) {
    let told = Arc::new(Told::default());
    (
        ProjectSealing::new(
            Arc::new(Facts { trusted }),
            Arc::new(Secrets(keys)),
            Arc::new(Tagging),
            Arc::clone(&told) as Arc<dyn KeysSeen>,
        ),
        told,
    )
}

#[test]
fn a_projects_secret_is_sealed_and_opened_with_its_keys() {
    let (sealing, _) = sealing(true, set("k-1", &[]));
    let sealed = sealing.seal("demo", "TOKEN", b"sk-secret").unwrap();
    assert_eq!(
        sealing.open("demo", "TOKEN", &sealed).unwrap(),
        b"sk-secret"
    );
}

#[test]
fn the_keys_ids_are_told_once_however_many_values_are_sealed() {
    let (sealing, told) = sealing(true, set("k-1", &[]));
    for field in ["A", "B", "C"] {
        sealing.seal("demo", field, b"v").unwrap();
    }
    assert_eq!(
        *told.0.lock().unwrap(),
        [("demo".to_string(), "k-1".to_string())]
    );
}

#[test]
fn a_project_the_daemon_does_not_know_or_does_not_trust_has_no_key() {
    let (sealing, told) = sealing(false, set("k-1", &[]));
    assert!(matches!(
        sealing.seal("demo", "A", b"v"),
        Err(SealingError::NoKey { ref file, .. }) if file == "/work/demo/.env"
    ));
    let (sealing, _) = self::sealing(true, set("k-1", &[]));
    assert!(matches!(
        sealing.seal("stranger", "A", b"v"),
        Err(SealingError::NoKey { ref project, .. }) if project == "stranger"
    ));
    assert!(told.0.lock().unwrap().is_empty(), "nothing told of no key");
}

#[test]
fn a_value_sealed_to_a_lost_key_names_the_key_and_the_project() {
    let (old, _) = sealing(true, set("k-old", &[]));
    let sealed = old.seal("demo", "A", b"v").unwrap();
    let (now, _) = sealing(true, set("k-new", &[]));
    assert_eq!(
        now.open("demo", "A", &sealed).unwrap_err(),
        SealingError::UnknownKey {
            project: "demo".to_string(),
            key_id: "k-old".to_string()
        }
    );
    let (rotated, _) = sealing(true, set("k-new", &["k-old"]));
    assert_eq!(rotated.open("demo", "A", &sealed).unwrap(), b"v");
}
