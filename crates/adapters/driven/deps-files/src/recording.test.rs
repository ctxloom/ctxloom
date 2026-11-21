//! The recording deps files: writes kept, never made, and read back.

use std::collections::BTreeSet;
use std::path::{Path, PathBuf};
use std::sync::Arc;

use deps_ports::{BundleSlot, CachePath, DepsFiles, RepoTree};
use file_system::{Fs, Perms, Rooted};
use os::Mode;

use super::*;

const IDENTITY: &str = "ctxloom+git://example.test/o/r//bundles/demo";
const OLD: &str = "0123456789abcdef0123456789abcdef01234567";
const NEW: &str = "fedcba9876543210fedcba9876543210fedcba98";

fn cache_path(path: &str) -> CachePath {
    CachePath::new(path.split('/').map(str::to_string).collect()).expect("plain names")
}

fn slot() -> BundleSlot {
    BundleSlot {
        identity: IDENTITY.to_string(),
        repository: cache_path("example.test/o/r"),
        bundle: cache_path("demo"),
    }
}

fn app_dir() -> PathBuf {
    PathBuf::from(os::host().flavor().from_top("/proj/.ctxloom"))
}

fn recording() -> (RecordingDepsFiles, Arc<ProjectFiles>, Arc<Rooted>) {
    let fs = Arc::new(Rooted::temp().expect("a temporary root"));
    let inner = Arc::new(ProjectFiles::new(fs.clone(), app_dir()));
    (RecordingDepsFiles::over(inner.clone()), inner, fs)
}

fn put(fs: &Rooted, path: &Path, text: &str) {
    fs.create_dir_all(path.parent().expect("a parent"), Mode::from_bits(0o755))
        .expect("a directory");
    fs.write_atomic(path, text.as_bytes(), Perms::Exact(Mode::from_bits(0o644)))
        .expect("a file");
}

fn demo() -> RepoTree {
    [
        ("bundle.yaml", "version: 1\n"),
        ("scripts/run.sh", "#!/bin/sh\n"),
    ]
    .iter()
    .map(|(path, text)| (path.to_string(), text.as_bytes().to_vec()))
    .collect()
}

// Step 10c U4: the registry and the lock a use case writes are recorded,
// the last of each kept, and read back by the reads after; the files on
// disk are left as they were.
#[test]
fn test_the_registry_and_the_lock_are_recorded_and_read_back_and_never_written() {
    let (recording, inner, fs) = recording();
    put(&fs, &app_dir().join("lock.yaml"), "on disk\n");
    assert_eq!(
        recording.read_lock().expect("reads").as_deref(),
        Some("on disk\n")
    );
    recording.write_registry("first\n").expect("recorded");
    recording.write_registry("second\n").expect("recorded");
    recording.write_lock("planned\n").expect("recorded");
    assert_eq!(
        recording.read_registry().expect("reads").as_deref(),
        Some("second\n")
    );
    assert_eq!(
        recording.read_lock().expect("reads").as_deref(),
        Some("planned\n")
    );
    assert_eq!(inner.read_registry().expect("reads"), None);
    assert_eq!(
        inner.read_lock().expect("reads").as_deref(),
        Some("on disk\n")
    );
    let recorded = recording.take();
    assert_eq!(recorded.registry.as_deref(), Some("second\n"));
    assert_eq!(recorded.lock.as_deref(), Some("planned\n"));
}

// An install is recorded as the tree an install leaves (its files, the
// declared executable, the identity marker), installed for the reads
// after it, and the identity's tree at another commit recorded taken
// away; nothing is written.
#[test]
fn test_an_install_is_recorded_whole_and_replaces_the_identitys_other_commits() {
    let (recording, inner, fs) = recording();
    let old = app_dir()
        .join("cache/bundles/example.test/o/r/demo")
        .join(OLD);
    put(&fs, &old.join("bundle.yaml"), "version: 0\n");
    put(&fs, &old.join(IDENTITY_MARKER), IDENTITY);
    let executable: BTreeSet<String> = ["scripts/run.sh".to_string()].into();
    assert!(!recording.is_installed(&slot(), NEW));
    recording
        .install(&slot(), NEW, &demo(), &executable)
        .expect("recorded");
    assert!(recording.is_installed(&slot(), NEW));
    assert!(!inner.is_installed(&slot(), NEW));
    assert_eq!(
        recording.read_installed(&slot(), NEW).expect("reads"),
        Some(demo())
    );
    let recorded = recording.take();
    assert_eq!(recorded.installs.len(), 1);
    let install = &recorded.installs[0];
    assert_eq!(
        install.path,
        format!("cache/bundles/example.test/o/r/demo/{NEW}")
    );
    assert_eq!(
        install.files,
        vec![
            ("bundle.yaml".to_string(), b"version: 1\n".to_vec(), false),
            ("scripts/run.sh".to_string(), b"#!/bin/sh\n".to_vec(), true),
            (
                IDENTITY_MARKER.to_string(),
                IDENTITY.as_bytes().to_vec(),
                false
            ),
        ]
    );
    assert_eq!(
        recorded.removed,
        [format!("cache/bundles/example.test/o/r/demo/{OLD}")]
    );
    assert!(fs.exists(&old.join("bundle.yaml")).expect("asks"));
}

// A prune records what it would take, once each though an install named it
// too, and answers the identities that would go; it removes nothing.
#[test]
fn test_a_prune_is_recorded_and_removes_nothing() {
    let (recording, _, fs) = recording();
    let old = app_dir()
        .join("cache/bundles/example.test/o/r/demo")
        .join(OLD);
    put(&fs, &old.join("bundle.yaml"), "version: 0\n");
    put(&fs, &old.join(IDENTITY_MARKER), IDENTITY);
    let gone = recording.prune(&[]).expect("planned");
    assert_eq!(gone, [IDENTITY.to_string()]);
    assert_eq!(
        recording.take().removed,
        [format!("cache/bundles/example.test/o/r/demo/{OLD}")]
    );
    // An install that replaced the same tree names it once.
    recording
        .install(&slot(), NEW, &demo(), &BTreeSet::new())
        .expect("recorded");
    recording.prune(&[]).expect("planned");
    assert_eq!(
        recording.take().removed,
        [format!("cache/bundles/example.test/o/r/demo/{OLD}")]
    );
    assert!(fs.exists(&old.join("bundle.yaml")).expect("asks"));
    assert!(recording.remove_local_bundle("demo").is_err());
}
