//! The tests' own files: real paths on the host, reached through the
//! file-system seam, under a root the test owns and removes.

use std::path::{Path, PathBuf};
use std::sync::Arc;

use file_system::{Fs, Kind, Perms, Rooted};
use os::Mode;

/// The host's own filesystem (no root), for real paths.
pub(crate) fn fs() -> Arc<dyn Fs> {
    let os = os::host();
    let top = os.flavor().top();
    Arc::new(file_system::Os::new(os, Path::new(top)).unwrap())
}

/// A temporary root, removed when dropped.
pub(crate) fn root() -> Rooted {
    Rooted::temp().unwrap()
}

/// Whether this host runs the tests' fake programs: unix, the one flavor
/// `fixture_program::install_program` does not refuse as `Unsupported`, with
/// the `sh` (and links) they need. Elsewhere the asking test is skipped and
/// says so on stderr, `SKIP <test>: <why>`, never silently; the same test
/// runs unchanged on unix.
pub(crate) fn runs_scripts() -> bool {
    if os::host().flavor() == os::Flavor::Unix {
        return true;
    }
    let test = std::thread::current();
    eprintln!(
        "SKIP {}: its fake programs are sh scripts (unix only)",
        test.name().unwrap_or("a test")
    );
    false
}

/// The variable the host's user home is read from: `HOME` on unix,
/// `USERPROFILE` on Windows (`os::Flavor::home_var`). A test's world names its
/// home by it, so the wiring finds the test's home and never the host's.
pub(crate) fn home_var() -> &'static str {
    os::host().flavor().home_var()
}

/// The variable the host's temporary directory is read from first
/// (`std::env::temp_dir`): `TMPDIR` on unix, `TMP` on Windows.
pub(crate) fn temp_var() -> &'static str {
    match os::host().flavor() {
        os::Flavor::Unix => "TMPDIR",
        os::Flavor::Windows => "TMP",
    }
}

/// Whether this host lets a process remove its own working directory: unix
/// does, Windows refuses while any process is in it ("being used by another
/// process"). Elsewhere the asking test is skipped and says so on stderr,
/// `SKIP <test>: <why>`, as [`runs_scripts`] does.
pub(crate) fn removes_its_cwd() -> bool {
    if os::host().flavor() == os::Flavor::Unix {
        return true;
    }
    let test = std::thread::current();
    eprintln!(
        "SKIP {}: Windows does not remove a process's working directory",
        test.name().unwrap_or("a test")
    );
    false
}

pub(crate) fn mkdirs(path: impl AsRef<Path>) {
    fs().create_dir_all(path.as_ref(), Mode::from_bits(0o777))
        .unwrap();
}

/// A file with this mode.
pub(crate) fn put_mode(path: impl AsRef<Path>, text: &str, mode: u32) {
    fs().write_atomic(
        path.as_ref(),
        text.as_bytes(),
        Perms::Exact(Mode::from_bits(mode)),
    )
    .unwrap();
}

pub(crate) fn exists(path: impl AsRef<Path>) -> bool {
    fs().exists(path.as_ref()).unwrap()
}

pub(crate) fn remove_dir(path: impl AsRef<Path>) {
    fs().remove_dir_all(path.as_ref()).unwrap();
}

pub(crate) fn canonical(path: impl AsRef<Path>) -> PathBuf {
    fs().canonicalize(path.as_ref()).unwrap()
}

/// The entries of a directory, with whether each is a directory.
pub(crate) fn entries(path: impl AsRef<Path>) -> Vec<(String, bool)> {
    fs().read_dir(path.as_ref())
        .unwrap()
        .into_iter()
        .map(|e| (e.name.to_string_lossy().into_owned(), e.kind == Kind::Dir))
        .collect()
}

/// A project whose agent `orch` runs a profile that includes a bundle INSTALLED
/// from a remote (`hub/demo`), as `ctxloom deps pull` leaves it: the registry,
/// the lock and the installed tree. Returns the root and the project directory.
pub(crate) fn project_with_a_remote_bundle() -> (file_system::Rooted, String) {
    const SHA: &str = "0123456789abcdef0123456789abcdef01234567";
    let root = crate::fixtures::root();
    let project = root.root().join("work");
    let app = project.join(".ctxloom");
    crate::fixtures::mkdirs(root.root().join("home"));
    let installed = app.join(format!("cache/bundles/example.test/o/r/demo/{SHA}"));
    for (path, text) in [
        (
            app.join("config.yaml"),
            "schema_version: 9\nagents:\n  orch:\n    engine: claude-code\n    profiles: [orch]\n    runtime: host\ndefault_agent: orch\n".to_string(),
        ),
        (
            app.join("remotes.yaml"),
            "schema_version: 1\nremotes:\n  hub:\n    url: https://example.test/o/r\n".to_string(),
        ),
        (
            app.join("lock.yaml"),
            format!(
                "schema_version: 3\nbundles:\n  ctxloom+git://example.test/o/r//bundles/demo:\n    sha: {SHA}\n    url: https://example.test/o/r\n"
            ),
        ),
        (
            app.join("content/bundles/v2/project/bundle.yaml"),
            "schema_version: 4\nversion: 1.0.0\n".to_string(),
        ),
        (
            app.join("content/bundles/v2/project/profiles/orch.yaml"),
            "bundles: [hub/demo]\n".to_string(),
        ),
        (
            installed.join("bundle.yaml"),
            "schema_version: 4\nversion: 1.0.0\n".to_string(),
        ),
        (installed.join("fragments/rules.md"), "RULES".to_string()),
        (
            installed.join(".identity"),
            "ctxloom+git://example.test/o/r//bundles/demo".to_string(),
        ),
    ] {
        crate::fixtures::mkdirs(path.parent().unwrap());
        crate::fixtures::put_mode(path, text.as_str(), 0o644);
    }
    let dir = project.display().to_string();
    (root, dir)
}
