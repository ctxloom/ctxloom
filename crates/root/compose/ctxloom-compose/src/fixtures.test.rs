//! The tests' own files: real paths on the host, reached through the
//! file-system seam, under a root the test owns and removes.

use std::path::{Path, PathBuf};
use std::sync::Arc;

use file_system::{Fs, Kind, Perms, Rooted};
use os::Mode;

/// The host's own filesystem (no root), for real paths.
pub fn fs() -> Arc<dyn Fs> {
    let os = os::host();
    let top = os.flavor().top();
    Arc::new(file_system::Os::new(os, Path::new(top)).unwrap())
}

/// A temporary root, removed when dropped.
pub fn root() -> Rooted {
    Rooted::temp().unwrap()
}

/// Whether this host runs the tests' fake programs: unix, the one flavor
/// `fixture_program::install_program` does not refuse as `Unsupported`, with
/// the `sh` (and links) they need. Elsewhere the asking test is skipped and
/// says so on stderr, `SKIP <test>: <why>`, never silently; the same test
/// runs unchanged on unix.
pub fn runs_scripts() -> bool {
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
pub fn home_var() -> &'static str {
    os::host().flavor().home_var()
}

/// The variable the host's temporary directory is read from first
/// (`std::env::temp_dir`): `TMPDIR` on unix, `TMP` on Windows.
pub fn temp_var() -> &'static str {
    match os::host().flavor() {
        os::Flavor::Unix => "TMPDIR",
        os::Flavor::Windows => "TMP",
    }
}

/// Whether this host lets a process remove its own working directory: unix
/// does, Windows refuses while any process is in it ("being used by another
/// process"). Elsewhere the asking test is skipped and says so on stderr,
/// `SKIP <test>: <why>`, as [`runs_scripts`] does.
pub fn removes_its_cwd() -> bool {
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

pub fn mkdirs(path: impl AsRef<Path>) {
    fs().create_dir_all(path.as_ref(), Mode::from_bits(0o777))
        .unwrap();
}

/// A file with this mode.
pub fn put_mode(path: impl AsRef<Path>, text: &str, mode: u32) {
    fs().write_atomic(
        path.as_ref(),
        text.as_bytes(),
        Perms::Exact(Mode::from_bits(mode)),
    )
    .unwrap();
}

pub fn exists(path: impl AsRef<Path>) -> bool {
    fs().exists(path.as_ref()).unwrap()
}

pub fn remove_dir(path: impl AsRef<Path>) {
    fs().remove_dir_all(path.as_ref()).unwrap();
}

pub fn canonical(path: impl AsRef<Path>) -> PathBuf {
    fs().canonicalize(path.as_ref()).unwrap()
}

/// The entries of a directory, with whether each is a directory.
pub fn entries(path: impl AsRef<Path>) -> Vec<(String, bool)> {
    fs().read_dir(path.as_ref())
        .unwrap()
        .into_iter()
        .map(|e| (e.name.to_string_lossy().into_owned(), e.kind == Kind::Dir))
        .collect()
}

/// A project whose agent `orch` runs a profile that includes a bundle INSTALLED
/// from a remote (`hub/demo`), as `ctxloom deps pull` leaves it: the registry,
/// the lock and the installed tree. Returns the root and the project directory.
pub fn project_with_a_remote_bundle() -> (file_system::Rooted, String) {
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

use ::process_env::EnvSnapshot;
use std::ffi::{OsStr, OsString};

/// The version stamp the tests build their commands with.
pub const VERSION: &str = "v0.1.0-abc1234";

/// The process's environment is one: tests that set it take this first, and
/// tests that read it read it through `host_snapshot`, never while an `Env`
/// holds it changed (one with PATH unset finds no `git`).
pub static ENV_LOCK: std::sync::Mutex<()> = std::sync::Mutex::new(());

/// Whether the variable `name` is one an enclosing ctxloom session gives the
/// engine its tests may be run under (an agent's shell runs `just test`):
/// ctxloom's own (`CTXLOOM_*`: its home, the run's listener, bearer and
/// certificate, the session's id and harp, a permit) and the directories the
/// engine is relocated into (`CLAUDE_CONFIG_DIR`, `OPENCODE_*`, the XDG
/// bases). A test that kept them would read the caller's ctxloom home and
/// deliver its hooks' facts to the caller's listener; none of them is the
/// test's, so no test starts from them.
pub fn enclosing_session(name: &OsStr) -> bool {
    name.to_str().is_some_and(|name| {
        name.starts_with("CTXLOOM_")
            || name.starts_with("OPENCODE_")
            || name.starts_with("XDG_")
            || name == "CLAUDE_CONFIG_DIR"
    })
}

/// The process's environment and working directory, read under `ENV_LOCK`
/// (owner ruling 144): what a test that changes neither starts from, without
/// the variables of an enclosing ctxloom session ([`enclosing_session`]), so a
/// test passes alike inside and outside one. A test holding an `Env` reads
/// them through `from_process` instead (the lock is not reentrant), and the
/// `Env` has unset those variables.
pub fn host_snapshot() -> EnvSnapshot {
    let _held = ENV_LOCK.lock().unwrap_or_else(|e| e.into_inner());
    without_enclosing_session(&EnvSnapshot::from_process())
}

/// `process` without the variables of an enclosing ctxloom session.
pub fn without_enclosing_session(process: &EnvSnapshot) -> EnvSnapshot {
    let vars: Vec<(OsString, OsString)> = process
        .vars()
        .filter(|(name, _)| !enclosing_session(name))
        .map(|(name, value)| (name.to_os_string(), value.to_os_string()))
        .collect();
    EnvSnapshot::from_parts(
        process.flavor(),
        process.cwd(),
        process.temp_dir().to_path_buf(),
        vars,
    )
}

/// A world of the test's own: the home (the host's home variable, `HOME` or
/// `USERPROFILE`), the working directory and the temporary directory in a
/// directory of the test's own (the temporary one being that directory, so
/// no walk up from the work directory looks above it), the root and project
/// variables unset, the rest of the process's environment as it is. The program reads it as a snapshot, so
/// the process's own environment is never touched.
pub struct World {
    _root: file_system::Rooted,
    pub dir: PathBuf,
    base: EnvSnapshot,
    vars: Vec<(std::ffi::OsString, std::ffi::OsString)>,
}

impl World {
    pub fn new(_name: &str) -> World {
        let temp = root();
        let dir = temp.root().to_path_buf();
        mkdirs(dir.join("home"));
        mkdirs(dir.join("work"));
        let base = host_snapshot();
        let mut world = World {
            _root: temp,
            dir,
            base,
            vars: Vec::new(),
        };
        let flavor = world.base.flavor();
        let unset = [
            home_var(),
            "CTXLOOM_ROOT",
            "CTXLOOM_PROJECT_ID",
            "CLAUDE_CODE_OAUTH_TOKEN",
        ]
        .map(|name| flavor.env_key(name));
        world.vars = world
            .base
            .vars()
            .filter(|(name, _)| {
                name.to_str()
                    .is_none_or(|name| !unset.contains(&flavor.env_key(name)))
            })
            .map(|(name, value)| (name.to_os_string(), value.to_os_string()))
            .collect();
        world.set(home_var(), world.dir.join("home"));
        world
    }

    pub fn set(&mut self, name: &str, value: impl Into<std::ffi::OsString>) {
        let (flavor, key) = (self.base.flavor(), self.base.flavor().env_key(name));
        self.vars
            .retain(|(set, _)| set.to_str().is_none_or(|set| flavor.env_key(set) != key));
        self.vars.push((name.into(), value.into()));
    }

    /// The world's root directory.
    pub fn dir(&self) -> &std::path::Path {
        &self.dir
    }

    #[cfg(feature = "test-support")]
    pub fn home(&self) -> PathBuf {
        self.dir.join("home")
    }

    #[cfg(feature = "test-support")]
    pub fn work(&self) -> PathBuf {
        self.dir.join("work")
    }

    /// The world's environment as the program takes it.
    pub fn env(&self) -> Arc<EnvSnapshot> {
        Arc::new(EnvSnapshot::from_parts(
            self.base.flavor(),
            Ok(self.dir.join("work")),
            self.dir.clone(),
            self.vars.clone(),
        ))
    }

    pub fn wiring(&self) -> crate::Wiring {
        crate::Wiring::new(self.env()).unwrap()
    }
}

/// A world in which no engine's program can be found, so nothing is launched.
pub fn world_without_programs(name: &str) -> World {
    let mut world = World::new(name);
    let empty = world.dir.join("no-programs");
    mkdirs(empty.clone());
    world.set("PATH", empty);
    mkdirs(world.dir.join("work/.ctxloom"));
    put_mode(
        world.dir.join("work/.ctxloom/config.yaml"),
        "schema_version: 9\nagents:\n  dev:\n    engine: claude-code\n    profiles: [dev]\n    runtime: host\ndefault_agent: dev\n",
        0o644,
    );
    let bundles = world.dir.join("work/.ctxloom/content/bundles/v2");
    for (rel, text) in [
        ("demo/bundle.yaml", "schema_version: 4\nversion: 1.0.0\n"),
        ("demo/fragments/rules.md", "RULES"),
        ("project/bundle.yaml", "schema_version: 4\nversion: 1.0.0\n"),
        ("project/profiles/dev.yaml", "bundles: [demo]\n"),
    ] {
        let path = bundles.join(rel);
        mkdirs(path.parent().unwrap());
        put_mode(path, text, 0o644);
    }
    world
}

// `run --one-shot` is the daemon's (U4, T7): its refusals (an engine nobody
// drives, no prompt beside no credential, rulings 73, 73b) are held by the
// binary's acceptance tests (run.feature), as a dry run's are: the command
// reaches the daemon, which an in-process test cannot spawn (its program is
// the test harness).

// Without `--one-shot` it is the human's session, prepared by the daemon
// and started by the command as its frontend (U5): its refusals (an engine
// nobody drives) are held by the binary's acceptance tests, for the same
// reason.

// `--dry-run` stays the library's (the plan printed, nothing run, its
// detailed code 1) is held by the binary's `library_cli_run.test.rs`: every
// dry run reaches the daemon (owner ruling 2026-11-14), which an in-process
// test cannot spawn (its program is the test harness).
