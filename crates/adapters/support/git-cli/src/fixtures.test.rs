//! The tests' own files: real paths on the host, reached through the
//! file-system seam, under a root the test owns and removes.

use std::path::Path;
use std::sync::Arc;

use file_system::{Fs, Perms, Rooted};
use os::Mode;
use process_env::EnvSnapshot;

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
/// `fixture_program::install_program` does not refuse as `Unsupported`.
/// Elsewhere the asking test is skipped and says so on stderr, `SKIP <test>:
/// <why>`, never silently; the same test runs unchanged on unix.
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

pub(crate) fn put(path: impl AsRef<Path>, text: &str) {
    fs().write_atomic(path.as_ref(), text.as_bytes(), Perms::Exact(Mode::FILE))
        .unwrap();
}

pub(crate) fn slurp(path: impl AsRef<Path>) -> String {
    fs().read_to_string(path.as_ref()).unwrap()
}

pub(crate) fn exists(path: impl AsRef<Path>) -> bool {
    fs().exists(path.as_ref()).unwrap()
}

/// The process's environment, as the program reads it once.
pub(crate) fn env() -> Arc<EnvSnapshot> {
    Arc::new(EnvSnapshot::from_process())
}
