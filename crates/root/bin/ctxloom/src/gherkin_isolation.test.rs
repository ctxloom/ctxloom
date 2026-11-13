//! Gherkin features (S11, the standalone half), run with cucumber-rs over the
//! real binary: Go ctxloom's own `cli/container.feature` (revision
//! f36e8dfb21, release/0.7; the file's header names what was left out) with
//! new Rust step definitions.
//!
//! plan supersedes Go, where a step differs:
//! - "Available Commands:" is clap's "Commands:" heading (plan 8.2: the
//!   command line is clap's, not cobra's).
//! - "no container runtime is installed" hides docker and podman by giving
//!   the process a PATH with neither, as Go's mask does.
//! - "the host's container runtimes are hidden behind stubs" gives it a PATH
//!   of one directory holding a fixture `docker` and `podman` that write down
//!   each call and never answer, so a probe reaches no runtime of the host's
//!   (which would write its own state outside the test's home).
//!
//! The worktree scenarios (cli/session_worktrees.feature and the sweep) run
//! in gherkin.test.rs beside the session steps they share.

use std::collections::BTreeMap;
use std::fmt;
use std::path::Path;

use cucumber::{given, then};

use super::gherkin_common::{common_steps, Common, CommonWorld};
use super::library::reports_as;
use crate::tests::files;

#[derive(cucumber::World)]
#[world(init = Self::new)]
struct World {
    common: Common,
    /// Fake companions, and the empty directory that hides the runtimes.
    bin: Option<file_system::Rooted>,
    /// Where the stub runtimes write down each call, when they are the PATH.
    asked: Option<std::path::PathBuf>,
    recorded: Option<BTreeMap<String, Vec<u8>>>,
}

impl fmt::Debug for World {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "World")
    }
}

impl CommonWorld for World {
    fn common(&self) -> &Common {
        &self.common
    }

    fn common_mut(&mut self) -> &mut Common {
        &mut self.common
    }
}

impl World {
    fn new() -> World {
        World {
            common: Common::new(),
            bin: None,
            asked: None,
            recorded: None,
        }
    }

    /// The directory of fakes, put first on the PATH with the system's.
    fn bin(&mut self) -> &Path {
        if self.bin.is_none() {
            let dir = files::root();
            let mut paths = vec![dir.root().to_path_buf()];
            paths.extend(files::search_path(
                &crate::tests::host_var("PATH").unwrap_or_default(),
            ));
            let path = std::env::join_paths(paths).expect("a PATH");
            self.bin = Some(dir);
            self.common
                .set_env("PATH", path.to_string_lossy().into_owned());
        }
        self.bin.as_ref().expect("just made").root()
    }

    /// A fake `ctxloom-companion-<name>` declaring `marker` as its typed
    /// `init.tooling` (Go: `toolingCompanion`).
    fn companion(&mut self, name: &str, marker: &str) {
        let bin = format!("ctxloom-companion-{name}");
        let script = format!(
            "#!/bin/sh\ncase \"$1\" in\n  loadout) printf 'init:\\n  tooling: \"%s\"\\n' '{marker}: install the tools this companion content needs.';;\n  version) printf '{{\"name\":\"{bin}\",\"version\":\"0.0.0-fixture\"}}\\n';;\nesac\n"
        );
        let path = self.bin().join(&bin);
        files::put_program(&path, &script);
    }
}

// ------------------------------------------------------------------ given

#[given(expr = "the mock LLM responds {string}")]
fn mock_responds(w: &mut World, _reply: String) {
    // The mock engine is the project's primary: a bare `container check`
    // resolves it.
    w.common.project.write(
        ".ctxloom/config.yaml",
        "schema_version: 9\nagents:\n  default:\n    engine: mock\ndefault_agent: default\n",
    );
}

#[given("no container runtime is installed")]
fn no_runtime(w: &mut World) {
    // PATH holds neither docker nor podman, nor anything else.
    let empty = files::root();
    let path = empty.root().to_string_lossy().into_owned();
    w.bin = Some(empty);
    w.common.set_env("PATH", path);
}

/// The runtimes `container check` looks for on the PATH.
const RUNTIMES: [&str; 2] = ["docker", "podman"];

#[given("the host's container runtimes are hidden behind stubs")]
fn stub_runtimes(w: &mut World) {
    // PATH is this one directory: a docker and a podman that write down each
    // call and never answer, and nothing else.
    let dir = files::root();
    let asked = dir.root().join("asked");
    for name in RUNTIMES {
        let script = format!(
            "#!/bin/sh\necho \"{name} $*\" >> '{}'\nexit 1\n",
            asked.display()
        );
        files::put_program(dir.root().join(name), &script);
    }
    w.common
        .set_env("PATH", dir.root().to_string_lossy().into_owned());
    w.bin = Some(dir);
    w.asked = Some(asked);
}

/// The calls the stub runtimes were given, one per line; none when there
/// were none. The run's PATH must be the stubs' directory alone, which holds
/// nothing but them, so no runtime of the host's could have been found.
fn stub_calls(w: &World) -> Vec<String> {
    let bin = w.bin.as_ref().expect("stub runtimes").root();
    let path = w
        .common
        .env
        .iter()
        .find(|(name, _)| name == "PATH")
        .map(|(_, value)| value.as_str());
    assert_eq!(path, bin.to_str(), "the PATH is the stubs' directory alone");
    let mut held: Vec<String> = files::fs()
        .read_dir(bin)
        .expect("the stubs' directory")
        .into_iter()
        .map(|entry| entry.name.to_string_lossy().into_owned())
        .filter(|name| name != "asked")
        .collect();
    held.sort();
    let mut expected: Vec<String> = RUNTIMES
        .iter()
        .flat_map(|name| [name.to_string(), format!("{name}.body")])
        .collect();
    expected.sort();
    assert_eq!(held, expected, "the stubs' directory holds the stubs alone");
    let asked = w.asked.as_ref().expect("stub runtimes");
    if !files::exists(asked) {
        return Vec::new();
    }
    files::read(asked)
        .expect("the stubs' calls")
        .lines()
        .map(String::from)
        .collect()
}

#[then("the runtimes probed were the stubs alone")]
fn probed_the_stubs(w: &mut World) {
    let calls = stub_calls(w);
    for name in RUNTIMES {
        let info = format!("{name} info");
        assert!(calls.contains(&info), "{info} was not asked: {calls:?}");
    }
}

#[then("no container runtime was asked anything")]
fn asked_nothing(w: &mut World) {
    let calls = stub_calls(w);
    assert!(calls.is_empty(), "the runtimes were asked {calls:?}");
}

#[given(
    expr = "a companion {string} declaring container tooling {string}, on PATH but not registered"
)]
fn unregistered_companion(w: &mut World, name: String, marker: String) {
    w.companion(&name, &marker);
}

#[given(expr = "a companion {string} declaring container tooling {string}")]
fn registered_companion(w: &mut World, name: String, marker: String) {
    w.companion(&name, &marker);
    // Registered by name (OD-10): its program is found on the PATH.
    w.common
        .run_lines(&format!("ctxloom companion add {name}"), "");
    w.ran().succeeds();
}

#[given("I record the project tree")]
fn record_tree(w: &mut World) {
    w.recorded = Some(w.common.project.tree("."));
}

// ------------------------------------------------------------------- when

// ------------------------------------------------------------------- then

#[then(expr = "the output reports {string} matching {string}")]
fn reports_matching(w: &mut World, path: String, expected: String) {
    if w.common.text_asked() {
        // The rendered forms carry their prose: matched as a substring.
        w.ran().contains(&expected);
        return;
    }
    let doc = w.ran().json();
    // The hermetic rows give literal expectations; the pattern rows
    // (`^(true|false)$`, `\S`) are the excluded live Rule's.
    let got = reports_as(&doc, &path);
    assert!(
        got.contains(&expected),
        "{path} is {got:?}, not {expected:?}"
    );
}

#[then("the project tree is unchanged")]
fn tree_unchanged(w: &mut World) {
    let before = w.recorded.as_ref().expect("a recorded tree");
    assert_eq!(&w.common.project.tree("."), before);
}

// ------------------------------------------------------------ the features

common_steps!();

/// Run one feature file with this World: whether it passed, and each
/// scenario's outcome.
pub(super) fn run(file: &Path) -> super::gherkin_common::FeatureRun {
    super::gherkin_common::run_feature::<World>(file)
}
