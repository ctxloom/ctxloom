//! Acceptance: the real `ctxloom` binary, run as a separate process.
//!
//! S0's red tests (`port/foundation`). The tests live beside the code (no
//! `tests/` directory), where cargo does not build the binary for them; so
//! nextest's setup script builds it once per run (`prebuilt`), and a test
//! run without it builds it, once per process, with the cargo running these
//! tests, into the same target directory and profile.
//!
//! THE TEST-ONLY SURFACE this file proposes (built only with the binary's
//! `test-support` feature; S0 has no real command that does these):
//!
//! - `ctxloom test refuse`: a refusal (exit 48) that carries a remedy.
//! - `ctxloom test findings`: fatal findings (exit 49).
//! - `ctxloom test sample [--empty]`: a sample list document. Two rows,
//!   `{"name": "alpha", "count": 1}` and `{"name": "beta", "count": 2}`;
//!   with `--empty`, no rows.
//! - `ctxloom test diagnostics`: one JSON array of the `{code, args,
//!   message}` envelope of every diagnostic the binary can raise (every
//!   crate's samples), rendered from the binary's catalog.
//! - `ctxloom test session-id`: a minted session harp, on a line.
//! - `ctxloom test run-id <session-uuid> <incarnation>`: the run id of that
//!   incarnation of that session, on a line.
//! - `ctxloom test open-store <project-id>`: opens the project's store
//!   (`Stores::open`) under the process's home and prints its path.
//!
//! These tests cannot use presentation, coded-diagnostic or the
//! file-system seam yet: S0 has not added their pins. So they read the
//! binary's output, the lock file, the manifests and the catalogs' text
//! instead of the crates' types.

use std::path::{Path, PathBuf};

/// The temporary root the tests make their directories under, which every
/// spawned `ctxloom` is told (`TMPDIR`): its project walk stops there, so it
/// cannot climb out of a test's directory into the real home's `.ctxloom`.
pub(crate) fn temp_root() -> std::path::PathBuf {
    process_env::EnvSnapshot::from_process()
        .temp_dir()
        .to_path_buf()
}

/// The variable that names a test process's lifeline to every `ctxloom` it
/// spawns (the wiring's `LIFELINE_VARIABLE`, test-support only).
pub(crate) const LIFELINE: &str = "CTXLOOM_TEST_LIFELINE";

/// This test process's LIFELINE: a lock file it holds for its life, taken
/// once, handed to every `ctxloom` it spawns ([`LIFELINE`]). A daemon a test
/// starts (or that its command starts) that can take the lock has outlived
/// the test (a test killed by nextest's timeout drops no TempDir and stops
/// nothing) and exits, ending what it hosts. The files are under the
/// system's temporary directory, `ctxloom-test-lifelines/<pid>-<nanos>.lock`;
/// each one whose lock is free is removed when the next is made, so they do
/// not pile up.
pub(crate) fn lifeline() -> &'static Path {
    static LINE: OnceLock<(PathBuf, file_lock::Locked)> = OnceLock::new();
    &LINE
        .get_or_init(|| {
            let fs = files::fs();
            let dir = temp_root().join("ctxloom-test-lifelines");
            for stale in files::children(&dir) {
                if let Ok(free) = file_lock::lock_within(&*fs, &stale, std::time::Duration::ZERO) {
                    // Another process may have reaped it first.
                    let _ = fs.remove_file(&stale);
                    drop(free);
                }
            }
            let nanos = std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .map_or(0, |d| d.as_nanos());
            let path = dir.join(format!("{}-{nanos}.lock", std::process::id()));
            let held = take_own_lifeline(&*fs, &path);
            (path, held)
        })
        .0
}

/// How long a test process waits for its own new lifeline's lock: another
/// process's reaper, which locks every lifeline it finds free to see whether
/// it was left behind, may hold a NEW one for an instant.
const OWN_LIFELINE_WAIT: std::time::Duration = std::time::Duration::from_secs(10);

/// The lock of this test process's own lifeline at `path`, waited for within
/// [`OWN_LIFELINE_WAIT`] (a reaper that removed the file meanwhile is the
/// lock's own re-check: it is taken again on a new file).
fn take_own_lifeline(fs: &dyn file_system::Fs, path: &Path) -> file_lock::Locked {
    file_lock::lock_within(fs, path, OWN_LIFELINE_WAIT).expect("this test process's lifeline")
}

// Run directly as a libtest binary with a cleared environment, the fallback
// build compiled sqlx with the toolchain its own rust-toolchain.toml names
// (1.78), which rejects the flags cargo passes. The compiler named beside
// cargo is no rustup proxy: a directory's toolchain file does not choose it.
#[test]
fn test_the_rustc_beside_cargo_ignores_a_toolchain_file_of_the_directory_it_runs_in() {
    let cargo = Path::new(env!("CARGO"));
    assert!(cargo.is_absolute(), "cargo is named by its path: {cargo:?}");
    let dir = temp_root().join(format!("ctxloom-test-rustc-beside-{}", std::process::id()));
    files::mkdirs(&dir);
    files::put(
        dir.join("rust-toolchain.toml"),
        "[toolchain]\nchannel = \"0.0.1-no-such-toolchain\"\n",
    );
    let out = Command::new(rustc_beside(cargo))
        .arg("--version")
        .current_dir(&dir)
        .env_clear()
        .output()
        .expect("rustc runs");
    files::remove_tree(&dir);
    assert!(
        out.status.success(),
        "{}",
        String::from_utf8_lossy(&out.stderr)
    );
    assert!(String::from_utf8_lossy(&out.stdout).starts_with("rustc "));
}

// CI on macOS (2026-11-19): a test process took its new lifeline's lock
// without waiting, and failed (`Timeout { waited_ms: 0 }`) because another
// test process's reaper held it at that instant. The race forced: another
// taker holds the lock when it is taken, and lets it go a moment later.
#[test]
fn a_lifeline_another_process_holds_for_an_instant_is_still_taken() {
    let fs = files::fs();
    let dir = temp_root().join(format!("ctxloom-test-lifeline-race-{}", std::process::id()));
    let path = dir.join("own.lock");
    let reaper =
        file_lock::lock_within(&*fs, &path, std::time::Duration::ZERO).expect("the lock is free");
    let let_go = std::thread::spawn(move || {
        std::thread::sleep(std::time::Duration::from_millis(200));
        drop(reaper);
    });
    let own = take_own_lifeline(&*fs, &path);
    let_go.join().expect("the reaper let go");
    assert!(
        file_lock::lock_within(&*fs, &path, std::time::Duration::ZERO).is_err(),
        "the process holds its lifeline"
    );
    drop(own);
    let _ = fs.remove_dir_all(&dir);
}

/// A variable of the process the tests run in, for passing on to the binary
/// they spawn (the process's environment is read once, by the snapshot).
pub(crate) fn host_var(name: &str) -> Option<std::ffi::OsString> {
    process_env::EnvSnapshot::from_process()
        .var(name)
        .map(std::ffi::OsStr::to_os_string)
}
use std::process::{Command, Output};
use std::sync::OnceLock;

use serde_json::Value;

/// The program nextest's setup script built for this run (owner ruling 144:
/// the binaries are built ONCE per test run, not once per test process), as
/// the variable `name` it set says, when it is in this test's own profile
/// directory (cargo's path and the test executable's are both absolute, from
/// the one target directory); else `None`, and the caller builds it. The
/// script is `scripts/nextest-build-binaries.sh`, run by `.config/nextest.toml`
/// (https://nexte.st/docs/configuration/setup-scripts/).
pub(crate) fn prebuilt(name: &str, profile_dir: &Path) -> Option<PathBuf> {
    let path = PathBuf::from(host_var(name)?);
    let own = path.parent() == Some(profile_dir)
        || path.parent() == Some(profile_dir.join(TEST_BINARIES).as_path());
    own.then_some(path)
}

/// The directory below a profile's, where the test-support `ctxloom` is
/// kept: a copy, because `<profile>/ctxloom` is where any cargo build of the
/// package, a plain one included, writes its binary.
pub(crate) const TEST_BINARIES: &str = "test-binaries";

/// `built` copied into [`TEST_BINARIES`] (`files::copy` writes whole and
/// renames in, so a process running an older copy keeps it).
fn kept_apart(built: &Path, profile_dir: &Path) -> PathBuf {
    let dir = profile_dir.join(TEST_BINARIES);
    files::mkdirs(&dir);
    let kept = dir.join(built.file_name().expect("a file name"));
    files::copy(built, &kept);
    kept
}

/// The `rustc` of the toolchain whose `cargo` is `cargo`. Run by itself (no
/// nextest, a cleared environment: nothing names the toolchain through
/// `RUSTUP_TOOLCHAIN`), the fallback build below has rustup's `rustc` proxy
/// choose a toolchain per crate from the directory cargo compiles it in, and
/// a dependency's own `rust-toolchain.toml` (sqlx's pins 1.78) then compiles
/// that dependency with another compiler than the rest.
pub(crate) fn rustc_beside(cargo: &Path) -> PathBuf {
    cargo.with_file_name(format!("rustc{}", std::env::consts::EXE_SUFFIX))
}

/// The `ctxloom` binary with its test-only commands: the one nextest's setup
/// script built for the run (`prebuilt`), else built once per test process.
pub(crate) fn binary() -> &'static Path {
    static BINARY: OnceLock<PathBuf> = OnceLock::new();
    BINARY.get_or_init(|| {
        // This test executable is <target>/<profile>/deps/ctxloom-<hash>.
        let exe = std::env::current_exe().expect("the test executable");
        let profile_dir = exe
            .parent()
            .and_then(Path::parent)
            .expect("<target>/<profile>");
        if let Some(built) = prebuilt("CTXLOOM_TEST_BINARY", profile_dir) {
            return built;
        }
        let target_dir = profile_dir.parent().expect("<target>");
        let mut build = Command::new(env!("CARGO"));
        build
            .args([
                "build",
                "--quiet",
                "--package",
                "ctxloom",
                "--bin",
                "ctxloom",
                "--features",
                "test-support,session-destroy",
            ])
            .arg("--manifest-path")
            .arg(concat!(env!("CARGO_MANIFEST_DIR"), "/Cargo.toml"))
            .env("CARGO_TARGET_DIR", target_dir);
        if host_var("RUSTC").is_none() {
            build.env("RUSTC", rustc_beside(Path::new(env!("CARGO"))));
        }
        if profile_dir.file_name().is_some_and(|p| p == "release") {
            build.arg("--release");
        }
        let status = build.status().expect("cargo runs");
        assert!(status.success(), "building ctxloom failed");
        kept_apart(
            &profile_dir.join(format!("ctxloom{}", std::env::consts::EXE_SUFFIX)),
            profile_dir,
        )
    })
}

/// Write `payload` to the child's stdin and close it; whether the child took
/// it. A child that exited, or closed stdin, without reading (a command that
/// refuses its arguments first) is EPIPE here, which is the child's answer and
/// not the step's failure: the step then asserts on the exit it got. Any other
/// write error is a real one.
pub(crate) fn feed(mut stdin: std::process::ChildStdin, payload: &str) -> bool {
    use std::io::{ErrorKind, Write};
    match stdin.write_all(payload.as_bytes()) {
        Ok(()) => true,
        Err(e) if e.kind() == ErrorKind::BrokenPipe => false,
        Err(e) => panic!("the payload is not written: {e}"),
    }
}

/// One finished process.
#[derive(Clone)]
pub(crate) struct Ran {
    /// The exit code; -1 when a signal ended the process.
    pub(crate) code: i32,
    pub(crate) stdout: String,
    pub(crate) stderr: String,
}

impl Ran {
    pub(crate) fn of(output: Output) -> Ran {
        Ran {
            code: output.status.code().unwrap_or(-1),
            stdout: String::from_utf8_lossy(&output.stdout).into_owned(),
            stderr: String::from_utf8_lossy(&output.stderr).into_owned(),
        }
    }

    /// stdout as one JSON document.
    pub(crate) fn json(&self) -> Value {
        parse_one(&self.stdout)
    }

    /// stderr as one JSON document.
    pub(crate) fn err_json(&self) -> Value {
        parse_one(&self.stderr)
    }

    /// A description of the run, for an assertion message.
    pub(crate) fn said(&self) -> String {
        format!(
            "exit {}; stdout {:?}; stderr {:?}",
            self.code, self.stdout, self.stderr
        )
    }
}

/// `text` as exactly one JSON document, as jq reads it.
pub(crate) fn parse_one(text: &str) -> Value {
    let mut documents = serde_json::Deserializer::from_str(text).into_iter::<Value>();
    let first = documents
        .next()
        .unwrap_or_else(|| panic!("no JSON: {text:?}"))
        .unwrap_or_else(|e| panic!("not JSON ({e}): {text:?}"));
    assert!(
        documents.next().is_none(),
        "more than one document: {text:?}"
    );
    first
}

/// An isolated home: a process sees nothing of this one's environment but
/// what a process needs to start, and this directory as its home and its
/// working directory. Never the real home.
pub(crate) struct Env {
    home: file_system::Rooted,
}

impl Env {
    pub(crate) fn new() -> Env {
        Env {
            home: files::root(),
        }
    }

    pub(crate) fn home(&self) -> &Path {
        self.home.root()
    }

    /// Run `ctxloom args`.
    pub(crate) fn run(&self, args: &[&str]) -> Ran {
        let mut command = Command::new(binary());
        command
            .args(args)
            .env_clear()
            .env("HOME", self.home())
            .env("USERPROFILE", self.home())
            .env("TMPDIR", crate::tests::temp_root())
            .env(crate::tests::LIFELINE, crate::tests::lifeline())
            .current_dir(self.home());
        for name in ["PATH", "SystemRoot"] {
            if let Some(value) = crate::tests::host_var(name) {
                command.env(name, value);
            }
        }
        Ran::of(command.output().expect("ctxloom runs"))
    }
}

/// Run `ctxloom args` in a fresh home.
pub(crate) fn run(args: &[&str]) -> Ran {
    Env::new().run(args)
}

/// The repository's root: this crate is crates/root/bin/ctxloom.
pub(crate) fn repo_root() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../../../..")
}

/// `cargo metadata` of the workspace, offline, for the host's platform: the
/// graph this binary is built from. Every platform's would need the packages
/// of every platform, and a build fetches only its own (on Windows the unix
/// ones are not there to read offline).
pub(crate) fn cargo_metadata() -> serde_json::Value {
    let cargo = env!("CARGO");
    let version = std::process::Command::new(cargo)
        .arg("-vV")
        .output()
        .expect("cargo runs");
    let version = String::from_utf8_lossy(&version.stdout).into_owned();
    let host = version
        .lines()
        .find_map(|line| line.strip_prefix("host: "))
        .unwrap_or_else(|| panic!("cargo -vV names no host: {version}"))
        .trim()
        .to_string();
    let output = std::process::Command::new(cargo)
        .args(["metadata", "--format-version", "1", "--offline"])
        .args(["--filter-platform", &host])
        .arg("--manifest-path")
        .arg(repo_root().join("Cargo.toml"))
        .output()
        .expect("cargo runs");
    assert!(
        output.status.success(),
        "cargo metadata failed: {}",
        String::from_utf8_lossy(&output.stderr)
    );
    serde_json::from_slice(&output.stdout).expect("metadata is JSON")
}

#[path = "files.test.rs"]
pub(crate) mod files;

#[path = "procs.test.rs"]
pub(crate) mod procs;

#[path = "catalog.test.rs"]
mod catalog;

#[path = "companion_stub.test.rs"]
pub(crate) mod companion_stub;

#[path = "companion_assembly.test.rs"]
mod companion_assembly;

#[path = "daemon.test.rs"]
pub(crate) mod daemon;

#[path = "mcp_serve.test.rs"]
pub(crate) mod mcp_serve;

#[path = "deps.test.rs"]
mod deps;

#[path = "errors.test.rs"]
mod errors;

#[path = "exit.test.rs"]
mod exit;

#[path = "format.test.rs"]
mod format;

#[path = "gherkin.test.rs"]
mod gherkin;

#[path = "gherkin_common.test.rs"]
mod gherkin_common;

#[path = "gherkin_features.test.rs"]
mod gherkin_features;

#[path = "gherkin_spy.test.rs"]
mod gherkin_spy;

#[path = "hook.test.rs"]
mod hook;

#[path = "hook_binary_name.test.rs"]
mod hook_binary_name;

#[path = "ids.test.rs"]
mod ids;

#[path = "launch_exit.test.rs"]
mod launch_exit;

#[path = "library.test.rs"]
mod library;

#[path = "library_events.test.rs"]
mod library_events;

#[path = "library_cli.test.rs"]
mod library_cli;

#[path = "library_cli_agent.test.rs"]
mod library_cli_agent;

#[path = "library_cli_bundle.test.rs"]
mod library_cli_bundle;

#[path = "library_cli_companion.test.rs"]
mod library_cli_companion;

#[path = "library_cli_context.test.rs"]
mod library_cli_context;

#[path = "library_cli_item.test.rs"]
mod library_cli_item;

#[path = "library_cli_mcp.test.rs"]
mod library_cli_mcp;

#[path = "library_cli_profile.test.rs"]
mod library_cli_profile;

#[path = "library_cli_run.test.rs"]
mod library_cli_run;

#[path = "library_cli_search.test.rs"]
mod library_cli_search;

#[path = "library_cli_skill.test.rs"]
mod library_cli_skill;

#[path = "library_config.test.rs"]
mod library_config;

#[path = "library_parity.test.rs"]
mod library_parity;

#[path = "library_write_agent.test.rs"]
mod library_write_agent;

#[path = "library_write_bundle.test.rs"]
mod library_write_bundle;

#[path = "library_write_companion.test.rs"]
mod library_write_companion;

#[path = "library_write_config.test.rs"]
mod library_write_config;

#[path = "library_write_items.test.rs"]
mod library_write_items;

#[path = "library_write_loadout.test.rs"]
mod library_write_loadout;

#[path = "library_write_mcp.test.rs"]
mod library_write_mcp;

#[path = "library_write_profile.test.rs"]
mod library_write_profile;

#[path = "library_write_skill.test.rs"]
mod library_write_skill;

#[path = "no_listener.test.rs"]
mod no_listener;

#[path = "tui_adopt.test.rs"]
mod tui_adopt;

#[path = "project_resolver.test.rs"]
mod project_resolver;

#[path = "session.test.rs"]
mod session;

#[path = "held.test.rs"]
mod held;

#[path = "gherkin_surfaces.test.rs"]
mod gherkin_surfaces;

#[path = "manage.test.rs"]
mod manage;

#[path = "materialize.test.rs"]
mod materialize;

#[path = "post_tool_hooks.test.rs"]
mod post_tool_hooks;

#[path = "session_start.test.rs"]
mod session_start;

#[path = "store_close.test.rs"]
mod store_close;

#[path = "store_migration.test.rs"]
mod store_migration;

#[path = "surfaces.test.rs"]
mod surfaces;

#[path = "util.test.rs"]
mod util;

#[path = "usage.test.rs"]
mod usage;

#[path = "feed.test.rs"]
mod feed_tests;

#[path = "version.test.rs"]
mod version;

#[path = "workspace.test.rs"]
mod workspace;

#[path = "gherkin_isolation.test.rs"]
mod gherkin_isolation;

#[path = "gherkin_lint.test.rs"]
mod gherkin_lint;

/// A plain `cargo build` (`just build`'s `plain-binary`, run in the same
/// checkout while a test run goes on) writes `<target>/<profile>/ctxloom`
/// over the test-support build. The tests then ran a plain binary, whose
/// account home is the passwd database's, and found the owner's real daemon
/// (`ctxloom-wiring-daemon-other-home`, 14 tests, 2026-11-23). The binary
/// the tests run is therefore never that path.
#[test]
fn test_the_binary_tests_run_is_not_the_path_a_plain_build_overwrites() {
    let exe = std::env::current_exe().expect("the test executable");
    let profile_dir = exe.parent().and_then(Path::parent).expect("profile dir");
    let uplifted = profile_dir.join(format!("ctxloom{}", std::env::consts::EXE_SUFFIX));
    assert_ne!(binary(), uplifted.as_path());
}
