use std::path::Path;
use std::sync::Arc;

use file_system::{Fs, Perms, Rooted};
use run_ports::{KeySet, ProjectEvidence, ProjectSecrets, Sealer, SealingError};

use super::{values_of, AngzarrSealer, DotenvSecrets};

const KEY: &str = "0101010101010101010101010101010101010101010101010101010101010101";
const OTHER: &str = "0303030303030303030303030303030303030303030303030303030303030303";
const OLD: &str = "0202020202020202020202020202020202020202020202020202020202020202";

fn host() -> Arc<dyn Fs> {
    let os = os::host();
    let top = os.flavor().top();
    Arc::new(file_system::Os::new(os, Path::new(top)).unwrap())
}

/// A project root holding the `.env` `text`, if any.
fn project(text: Option<&str>) -> (Rooted, ProjectEvidence) {
    let root = Rooted::temp().unwrap();
    if let Some(text) = text {
        host()
            .write_atomic(
                &root.root().join(".env"),
                text.as_bytes(),
                Perms::Exact(os::Mode::from_bits(0o600)),
            )
            .unwrap();
    }
    let evidence = ProjectEvidence {
        root: root.root().display().to_string(),
        trusted: true,
    };
    (root, evidence)
}

fn keys(text: &str) -> KeySet {
    let (_root, evidence) = project(Some(text));
    DotenvSecrets::new(host())
        .key_set("demo", &evidence)
        .unwrap()
}

#[test]
fn a_secret_sealed_opens_with_the_same_set_and_is_not_the_secret() {
    let set = keys(&format!("CTXLOOM_SEAL_KEY={KEY}\n"));
    let sealed = AngzarrSealer
        .seal(&set, "ANTHROPIC_API_KEY", b"sk-ant-secret")
        .unwrap();
    assert!(!String::from_utf8_lossy(&sealed).contains("sk-ant-secret"));
    assert_eq!(
        AngzarrSealer
            .open(&set, "ANTHROPIC_API_KEY", &sealed)
            .unwrap(),
        b"sk-ant-secret"
    );
}

#[test]
fn a_sealed_value_opens_for_its_own_field_alone() {
    let set = keys(&format!("CTXLOOM_SEAL_KEY={KEY}\n"));
    let sealed = AngzarrSealer.seal(&set, "A", b"v").unwrap();
    assert!(matches!(
        AngzarrSealer.open(&set, "B", &sealed),
        Err(SealingError::Open { .. })
    ));
}

#[test]
fn sealing_twice_gives_two_values() {
    let set = keys(&format!("CTXLOOM_SEAL_KEY={KEY}\n"));
    assert_ne!(
        AngzarrSealer.seal(&set, "A", b"v").unwrap(),
        AngzarrSealer.seal(&set, "A", b"v").unwrap()
    );
}

#[test]
fn a_value_sealed_to_a_key_the_set_lost_is_an_unknown_key_naming_its_id() {
    let old = keys(&format!("CTXLOOM_SEAL_KEY={OLD}\n"));
    let sealed = AngzarrSealer.seal(&old, "A", b"v").unwrap();
    let now = keys(&format!("CTXLOOM_SEAL_KEY={KEY}\n"));
    let Err(SealingError::UnknownKey { key_id, .. }) = AngzarrSealer.open(&now, "A", &sealed)
    else {
        panic!("an unknown key");
    };
    assert_eq!(key_id, old.current().id);
}

#[test]
fn a_rotated_key_still_opens_what_its_predecessor_sealed() {
    let old = keys(&format!("CTXLOOM_SEAL_KEY={OLD}\n"));
    let sealed = AngzarrSealer.seal(&old, "A", b"v").unwrap();
    let rotated = keys(&format!(
        "CTXLOOM_SEAL_KEY={KEY}\nCTXLOOM_SEAL_KEY_PREVIOUS={OLD}, {OTHER}\n"
    ));
    assert_eq!(
        rotated.current().id,
        keys(&format!("CTXLOOM_SEAL_KEY={KEY}")).current().id
    );
    assert_eq!(rotated.earlier().len(), 2);
    assert_eq!(AngzarrSealer.open(&rotated, "A", &sealed).unwrap(), b"v");
}

#[test]
fn a_key_set_names_its_keys_by_id_and_fingerprint_and_debug_shows_no_key() {
    let set = keys(&format!("CTXLOOM_SEAL_KEY={KEY}\n"));
    assert!(set.current().id.starts_with("k-"));
    assert_eq!(set.current().fingerprint.len(), 32);
    assert!(!format!("{set:?}").contains(KEY));
    assert!(set.announce(), "the first announcement");
    assert!(!set.clone().announce(), "told once, by any clone");
}

#[test]
fn a_project_with_no_env_or_an_untrusted_one_or_a_keyless_one_has_no_key() {
    let secrets = DotenvSecrets::new(host());
    let (_none, evidence) = project(None);
    assert!(matches!(
        secrets.key_set("demo", &evidence),
        Err(SealingError::NoKey { ref file, .. }) if file.ends_with(".env")
    ));
    let (_untrusted, mut evidence) = project(Some(&format!("CTXLOOM_SEAL_KEY={KEY}")));
    evidence.trusted = false;
    assert!(matches!(
        secrets.key_set("demo", &evidence),
        Err(SealingError::NoKey { .. })
    ));
    let (_keyless, evidence) = project(Some("OTHER=1\n"));
    assert!(matches!(
        secrets.key_set("demo", &evidence),
        Err(SealingError::NoKey { .. })
    ));
}

#[test]
fn a_key_that_is_no_key_is_refused_naming_the_project_not_the_value() {
    for bad in ["not hex", "0101", &"zz".repeat(32)] {
        let (_root, evidence) = project(Some(&format!("CTXLOOM_SEAL_KEY={bad}\n")));
        let error = DotenvSecrets::new(host())
            .key_set("demo", &evidence)
            .unwrap_err();
        assert!(matches!(&error, SealingError::BadKey { project, .. } if project == "demo"));
        assert!(!error.to_string().contains(bad));
    }
}

#[test]
fn a_project_with_no_env_at_all_takes_the_test_builds_fallback_alone() {
    let secrets = DotenvSecrets::new(host()).with_fallback(Some(KEY.to_string()));
    let (_none, evidence) = project(None);
    assert!(secrets.key_set("demo", &evidence).is_ok());
    let (_keyless, evidence) = project(Some("OTHER=1\n"));
    assert!(secrets.key_set("demo", &evidence).is_err(), "a .env judges");
}

#[test]
fn the_names_asked_are_read_from_plain_quoted_and_exported_lines() {
    let found = values_of(
        "# a comment\nOTHER=1\nCTXLOOM_SEAL_KEY=abc\nexport CTXLOOM_SEAL_KEY_PREVIOUS=\"d, e\"\n#CTXLOOM_X=1\nCTXLOOM_Y\n",
        &["CTXLOOM_SEAL_KEY", "CTXLOOM_SEAL_KEY_PREVIOUS", "CTXLOOM_X", "CTXLOOM_Y"],
    );
    assert_eq!(found["CTXLOOM_SEAL_KEY"], "abc");
    assert_eq!(found["CTXLOOM_SEAL_KEY_PREVIOUS"], "d, e");
    assert_eq!(found.len(), 2, "no commented line or bare name is read");
}
