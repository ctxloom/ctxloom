//! Gherkin features (S12), run with cucumber-rs over the real binary: Go
//! ctxloom's own `.feature` text (revision f36e8dfb21, release/0.7; see each
//! file's header for what was left out) with new Rust step definitions.
//!
//! The steps build a project and a home the way Go's steps do (see
//! `library.test.rs`), seed sessions through `ctxloom test seed-session`
//! (session.test.rs), run the binary, and read what it printed.

use std::fmt;
use std::fs::File;
use std::path::{Path, PathBuf};

use cucumber::gherkin::Step;
use cucumber::{given, then, when};
use serde_json::Value;

use super::gherkin_common::{common_steps, doc, Common, CommonWorld};
use super::library::{at, Project};
use crate::tests::files;

const DAY: u64 = 86_400;

/// The uncommitted work a reaper must never destroy (Go: `j001300WIPMarker`).
const WIP_MARKER: &str = "J001300-WIP-MARKER-5b8e21";

/// How a session is to be seeded, decided when the first command runs (a
/// step such as "every session has been idle for 120 days" comes after the
/// sessions it dates).
struct Planted {
    harp: String,
    /// `finished`, `running`, `unproven` or `oneshot`.
    kind: &'static str,
    /// Seconds since it ended, when the step says; else the kind's default.
    ended_ago: Option<u64>,
    keep: bool,
    summary: bool,
    no_lock: bool,
}

#[derive(cucumber::World)]
#[world(init = Self::new)]
struct World {
    /// What a daemon scenario started: dropped first, before the home goes.
    daemon_spec: daemon_spec::State,
    common: Common,
    planted: Vec<Planted>,
    seeded: bool,
    idle_days: Option<u64>,
    /// The locks of the sessions that are running, held until the scenario
    /// ends.
    held: Vec<File>,
    /// Scratch worktrees the steps left in sessions: the session, the
    /// checkout's suffix and whether it holds uncommitted work.
    scratch: Vec<(String, String, bool)>,
    /// A worktree of Alice's own, outside the sessions root: its branch and
    /// directory.
    foreign: Vec<(String, PathBuf)>,
    /// What `seed_scratch` planted, for the assertions.
    planted_scratch: Vec<(String, String, bool)>,
    planted_foreign: Vec<PathBuf>,
    /// The sessions planted with a free lock that is not held.
    lock_free_dead: Vec<String>,
    /// The design notes nobody filed: the session, and each note's path
    /// relative to its directory (j001300).
    design_notes: Vec<(String, Vec<String>)>,
    /// Every session the steps planted, by harp, once seeded.
    seeded_harps: Vec<String>,
    /// Every byte of the sessions' directories and scratch checkouts, as the
    /// last command found them.
    before: Vec<(PathBuf, Vec<u8>)>,
    /// What the j000100 scenarios keep.
    adopt: adopt::State,
    /// What j000200's scenario 2 keeps.
    setup: setup::State,
    /// What j001000's stub-engine scenario keeps.
    capture: capture::State,
    /// What j000700's scenarios keep.
    authoring: authoring::State,
    /// What j000400's and j002000's sessions keep.
    engines: engines::State,
    /// What the j001900 scenarios keep.
    diagnosis: diagnosis::State,
    /// What the distill steps keep.
    distill: distill::State,
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

    fn alice_runs(&mut self, step: &Step) {
        if setup::stages(self, step)
            || capture::fires(self, step)
            || capture_opencode::forwards(self, step)
            || capture_opencode::reports_login(self, step)
            || daemon_spec::claims(self, step)
            || engines::starts(self, step)
            || diagnosis::runs(self, step)
        {
            return;
        }
        self.before_run();
        self.common.run_lines(&doc(step), "");
    }

    fn before_run(&mut self) {
        self.adopt_record();
        self.seed();
        self.seed_scratch();
        self.before = self.session_bytes();
    }
}

impl World {
    fn new() -> World {
        World {
            daemon_spec: daemon_spec::State::default(),
            common: Common::new(),
            planted: Vec::new(),
            seeded: false,
            idle_days: None,
            held: Vec::new(),
            scratch: Vec::new(),
            foreign: Vec::new(),
            planted_scratch: Vec::new(),
            planted_foreign: Vec::new(),
            lock_free_dead: Vec::new(),
            design_notes: Vec::new(),
            seeded_harps: Vec::new(),
            before: Vec::new(),
            adopt: adopt::State::default(),
            setup: setup::State::default(),
            capture: capture::State::default(),
            authoring: authoring::State::default(),
            engines: engines::State::default(),
            diagnosis: diagnosis::State::default(),
            distill: distill::State::default(),
        }
    }

    /// The session's directory: in the project, where the seed writes it.
    fn session_dir(&self, harp: &str) -> PathBuf {
        self.common
            .project
            .dir()
            .join(".ctxloom/sessions")
            .join(harp)
    }

    /// `<project>/ctxloom/<harp>`: `output.dir`'s default (rulings 89, 106).
    fn output_dir(&self, harp: &str) -> PathBuf {
        self.common.project.dir().join("ctxloom").join(harp)
    }

    /// Plant the sessions the steps described, once.
    fn seed(&mut self) {
        if self.seeded {
            return;
        }
        self.seeded = true;
        let planted = std::mem::take(&mut self.planted);
        self.seeded_harps
            .extend(planted.iter().map(|session| session.harp.clone()));
        for session in &planted {
            let ended = session
                .ended_ago
                .or(self.idle_days.map(|d| d * DAY))
                .unwrap_or(match session.kind {
                    "oneshot" => 3_600,
                    _ => 60 * DAY,
                });
            let started = (ended + DAY).to_string();
            let ended = ended.to_string();
            let mut flags: Vec<&str> = vec!["test", "seed-session", &session.harp, "--transcript"];
            match session.kind {
                "running" => flags.extend(["--lock", "free", "--started-ago", "86400"]),
                _ => {
                    if !session.no_lock {
                        flags.extend(["--lock", "free"]);
                    }
                    flags.extend(["--started-ago", &started, "--ended-ago", &ended]);
                }
            }
            if session.kind == "oneshot" {
                flags.push("--oneshot");
            }
            if session.keep {
                flags.push("--keep");
            }
            let ran = self.common.project.run(&flags);
            assert_eq!(ran.code, 0, "seeding {}: {}", session.harp, ran.said());
            if session.kind != "running" && !session.no_lock {
                self.lock_free_dead.push(session.harp.clone());
            }
            if session.summary {
                let dir = self.output_dir(&session.harp);
                files::put(dir.join("essence.md"), "an older ctxloom's summary\n");
            }
            if session.kind == "running" {
                let lock = files::open_read(
                    self.common
                        .project
                        .dir()
                        .join(".ctxloom/sessions")
                        .join(format!("{}.lock", session.harp)),
                )
                .expect("a lock file");
                lock.lock().expect("the lock is free to take");
                self.held.push(lock);
            }
        }
    }

    /// The project's git, with a clean identity and none of this process's
    /// environment.
    fn git(&self, dir: &Path, args: &[&str]) -> String {
        let output = std::process::Command::new("git")
            .args([
                "-c",
                "user.name=Alice",
                "-c",
                "user.email=alice@example.com",
            ])
            .args([
                "-c",
                "commit.gpgsign=false",
                "-c",
                "init.defaultBranch=main",
            ])
            .args(args)
            .current_dir(dir)
            .env_clear()
            .env("PATH", crate::tests::host_var("PATH").unwrap_or_default())
            .env("HOME", self.common.project.home())
            .env("TMPDIR", crate::tests::temp_root())
            .output()
            .expect("git runs");
        assert!(
            output.status.success(),
            "git {args:?}: {}",
            String::from_utf8_lossy(&output.stderr)
        );
        String::from_utf8_lossy(&output.stdout).into_owned()
    }

    /// Make the project a git repository, once, with one commit.
    fn git_backed(&self) {
        let project = self.common.project.dir();
        if files::exists(project.join(".git")) {
            return;
        }
        self.git(project, &["init", "-q"]);
        files::put(project.join("README.md"), "hello\n");
        self.git(project, &["add", "README.md"]);
        self.git(project, &["commit", "-q", "-m", "initial"]);
    }

    /// Every file under the project's sessions directory and the project's
    /// scratch checkouts, with its bytes, sorted by path.
    fn session_bytes(&self) -> Vec<(PathBuf, Vec<u8>)> {
        fn walk(dir: &Path, found: &mut Vec<(PathBuf, Vec<u8>)>) {
            for path in files::children(dir) {
                if files::is_dir(&path) {
                    walk(&path, found);
                } else if let Ok(bytes) = files::read_bytes(&path) {
                    found.push((path, bytes));
                }
            }
        }
        let mut found = Vec::new();
        walk(
            &self.common.project.dir().join(".ctxloom/sessions"),
            &mut found,
        );
        walk(
            &self.common.project.home().join(".ctxloom/worktrees"),
            &mut found,
        );
        found.sort();
        found
    }

    /// Whether `harp`'s owner is provably dead: it was planted with a free
    /// lock and is not running.
    fn reapable(&self, harp: &str) -> bool {
        self.lock_free_dead.iter().any(|h| h == harp)
    }

    /// A session's scratch checkout: under ctxloom's home, by the project's
    /// id and the session's harp (owner ruling 2026-11-12, Q1), named by its
    /// agent. The id is the project's marker, which the seed writes.
    fn scratch_dir(&self, harp: &str, name: &str) -> PathBuf {
        let marker = self.common.project.dir().join(".ctxloom/project-id");
        let id = files::read(&marker).unwrap_or_else(|e| panic!("{}: {e}", marker.display()));
        self.common
            .project
            .home()
            .join(".ctxloom/worktrees")
            .join(id.trim())
            .join(harp)
            .join(name)
    }

    /// Plant the scratch worktrees the steps described: genuine `git
    /// worktree add` checkouts in the sessions' checkouts directories, and the
    /// uncommitted work that no reaper may ever destroy.
    fn seed_scratch(&mut self) {
        if self.scratch.is_empty() && self.foreign.is_empty() {
            return;
        }
        self.git_backed();
        for (harp, name, dirty) in std::mem::take(&mut self.scratch) {
            let dir = self.scratch_dir(&harp, &name);
            let branch = format!("wt-{harp}-{name}");
            self.git(
                self.common.project.dir(),
                &[
                    "worktree",
                    "add",
                    "-q",
                    "-b",
                    &branch,
                    &dir.display().to_string(),
                ],
            );
            if dirty {
                files::put(dir.join("in-flight.go"), format!("// {WIP_MARKER}\n"));
            }
            self.planted_scratch.push((harp, name, dirty));
        }
        for (branch, dir) in std::mem::take(&mut self.foreign) {
            files::mkdirs(dir.parent().expect("a parent"));
            self.git(
                self.common.project.dir(),
                &[
                    "worktree",
                    "add",
                    "-q",
                    "-b",
                    &branch,
                    &dir.display().to_string(),
                ],
            );
            files::put(dir.join("feature.txt"), "unmerged work\n");
            self.git(&dir, &["add", "-A"]);
            self.git(&dir, &["commit", "-q", "-m", "unmerged feature work"]);
            files::put(dir.join("uncommitted.txt"), format!("{WIP_MARKER}\n"));
            self.planted_foreign.push(dir);
        }
    }

    fn plant(&mut self, harp: &str, kind: &'static str) -> &mut Planted {
        self.planted.push(Planted {
            harp: harp.to_string(),
            kind,
            ended_ago: None,
            keep: false,
            summary: false,
            no_lock: false,
        });
        self.planted.last_mut().expect("just pushed")
    }
}

#[path = "gherkin_adopt.test.rs"]
mod adopt;

#[path = "gherkin_setup.test.rs"]
mod setup;

#[path = "gherkin_authoring.test.rs"]
mod authoring;

#[path = "gherkin_capture.test.rs"]
mod capture;

#[path = "gherkin_capture_opencode.test.rs"]
mod capture_opencode;

#[path = "gherkin_daemon.test.rs"]
mod daemon_spec;
#[path = "gherkin_workspace.test.rs"]
mod workspace;

#[path = "gherkin_recall.test.rs"]
mod recall;

#[path = "gherkin_diagnosis.test.rs"]
mod diagnosis;
#[path = "gherkin_engines.test.rs"]
mod engines;

#[path = "gherkin_distill.test.rs"]
mod distill;

// ------------------------------------------------------------------ given

#[given(expr = "the environment variable {string} is set to {string}")]
fn env_set(w: &mut World, name: String, value: String) {
    w.common.env.retain(|(n, _)| *n != name);
    w.common.env.push((name, value));
}

/// The mock LLM. In j001200 it is that journey's stub claude
/// ([`recall::mock_responds`]); elsewhere the mock engine is the default
/// agent's, and no reply is ever asked of it on this World (a dry run
/// launches nothing), so the reply is not kept.
#[given(expr = "the mock LLM responds {string}")]
fn mock_responds(w: &mut World, reply: String) {
    distill::remember(w, &reply);
    if recall::mock_responds(w, &reply) {
        return;
    }
    w.common.project.write(
        ".ctxloom/config.yaml",
        "schema_version: 9\nagents:\n  default:\n    engine: mock\ndefault_agent: default\n",
    );
}

#[given(expr = "the home already has the file {string}:")]
fn home_file(w: &mut World, step: &Step, rel: String) {
    w.common
        .project
        .write_home(&rel, &format!("{}\n", doc(step)));
}

/// `CTXLOOM_HOME` set to `rel` in her home (made): ctxloom's home is not her
/// `HOME`.
#[given(expr = "ctxloom's home is {string} in her home")]
fn ctxloom_home(w: &mut World, rel: String) {
    let dir = w.common.project.home().join(rel);
    files::mkdirs(&dir);
    let dir = dir.to_str().expect("utf-8").to_string();
    w.common.set_env("CTXLOOM_HOME", dir);
}

#[given("a malformed ctxloom config")]
fn malformed(w: &mut World) {
    w.common.project.write(
        ".ctxloom/config.yaml",
        "schema_version: [unclosed\n  - : :\n",
    );
}

#[given(expr = "a finished session {string} with a transcript")]
fn finished(w: &mut World, harp: String) {
    w.plant(&harp, "finished");
}

#[given(expr = "a finished session {string} with a transcript and a summary an older ctxloom left")]
fn finished_with_summary(w: &mut World, harp: String) {
    w.plant(&harp, "finished").summary = true;
}

#[given(expr = "a finished session {string} with a transcript but no session lock")]
fn finished_unlocked(w: &mut World, harp: String) {
    w.plant(&harp, "finished").no_lock = true;
}

#[given(expr = "a finished session {string} whose transcript retention has passed")]
fn finished_retention_passed(w: &mut World, harp: String) {
    w.plant(&harp, "finished");
}

#[given(expr = "a finished session {string} marked keep")]
fn finished_kept(w: &mut World, harp: String) {
    w.plant(&harp, "finished").keep = true;
}

#[given(expr = "a session {string} with a transcript that is running again")]
fn running_again(w: &mut World, harp: String) {
    // The index says ended; the lock says running.
    w.plant(&harp, "running");
}

#[given(expr = "a session {string} that is still running")]
fn still_running(w: &mut World, harp: String) {
    w.plant(&harp, "running");
}

#[given(expr = "a session {string} nothing can prove the liveness of")]
fn unproven(w: &mut World, harp: String) {
    w.plant(&harp, "unproven").no_lock = true;
}

#[given(expr = "a session {string} with a transcript that finished an hour ago")]
fn finished_an_hour_ago(w: &mut World, harp: String) {
    w.plant(&harp, "finished").ended_ago = Some(3_600);
}

#[given(expr = "an internal one-shot session {string} that finished an hour ago")]
fn oneshot(w: &mut World, harp: String) {
    w.plant(&harp, "oneshot");
}

#[given(expr = "session {string} holds a file {string} a person wrote")]
fn authored_file(w: &mut World, harp: String, rel: String) {
    files::put(w.session_dir(&harp).join(rel), "notes nobody filed\n");
}

/// The notes a person wrote in a session's directory and never filed
/// (j001300): what a destroy takes with it after a warning (ruling 104).
const DESIGN_NOTES: [&str; 2] = ["design-notes.md", "notes/api-shape.md"];

#[given(
    expr = "a finished session {string} carrying design notes nobody filed, whose transcript retention has passed"
)]
fn finished_with_notes(w: &mut World, harp: String) {
    w.plant(&harp, "finished");
    for rel in DESIGN_NOTES {
        files::put(w.session_dir(&harp).join(rel), format!("{harp}: {rel}\n"));
    }
    w.design_notes
        .push((harp, DESIGN_NOTES.iter().map(|r| r.to_string()).collect()));
}

#[given("the feature shipped on Friday and Alice is closing the workstream out")]
#[given("the feature shipped on Monday and Alice is closing the workstream out the next week")]
fn closing_out(w: &mut World) {
    w.common.project = Project::initialized();
}

#[given(expr = "session {string} left a clean scratch worktree")]
fn left_clean(w: &mut World, harp: String) {
    w.scratch.push((harp, "clean".to_string(), false));
}

#[given(expr = "session {string} left a scratch worktree holding uncommitted work")]
fn left_dirty(w: &mut World, harp: String) {
    w.scratch.push((harp, "wip".to_string(), true));
}

#[given(expr = "session {string} left a scratch worktree of its own")]
fn left_own(w: &mut World, harp: String) {
    w.scratch.push((harp, "own".to_string(), false));
}

#[given(
    expr = "a long-lived worktree {string} of her own, outside the sessions root, with unmerged work"
)]
fn long_lived(w: &mut World, branch: String) {
    let dir = w
        .common
        .project
        .home()
        .join("workspace/worktrees")
        .join(format!("proj--{branch}"));
    w.foreign.push((branch, dir));
}

#[given(expr = "every session has been idle for {int} days")]
fn idle(w: &mut World, days: u64) {
    w.idle_days = Some(days);
}

// ------------------------------------------------------------------- when

/// The notes the steps planted in `harp`.
fn notes_of(w: &World, harp: &str) -> Vec<String> {
    w.design_notes
        .iter()
        .find(|(h, _)| h == harp)
        .map(|(_, notes)| notes.clone())
        .unwrap_or_else(|| panic!("no design notes were planted in {harp}"))
}

/// She files them where a session's outputs live, `<project>/<output.dir>/
/// <harp>`: outside the session's directory, so a destroy leaves them.
#[when(expr = "Alice files her design notes into the output dir of {string}")]
fn files_notes(w: &mut World, harp: String) {
    for rel in notes_of(w, &harp) {
        let to = w.output_dir(&harp).join(&rel);
        files::mkdirs(to.parent().expect("a parent"));
        files::rename(w.session_dir(&harp).join(&rel), to);
    }
}

/// She takes her uncommitted work out of the checkout, to a place of her
/// own, so the checkout is clean.
#[when(expr = "Alice saves the work she wants out of the scratch worktree of {string}")]
fn saves_work(w: &mut World, harp: String) {
    let dirty: Vec<String> = w
        .planted_scratch
        .iter()
        .filter(|(h, _, dirty)| *h == harp && *dirty)
        .map(|(_, name, _)| name.clone())
        .collect();
    assert!(
        !dirty.is_empty(),
        "no uncommitted work was seeded in {harp}"
    );
    for name in dirty {
        let saved = w
            .common
            .project
            .home()
            .join("saved-work")
            .join(&harp)
            .join(&name);
        files::mkdirs(&saved);
        files::rename(
            w.scratch_dir(&harp, &name).join("in-flight.go"),
            saved.join("in-flight.go"),
        );
    }
}

// ------------------------------------------------------------------- then

/// Go: "exactly N home files match" a glob below the home, one `*` per
/// path segment.
#[then(expr = "exactly {int} home files match {string}")]
fn home_files_match(w: &mut World, count: usize, pattern: String) {
    fn walk(dir: &Path, rel: &str, parts: &[&str], found: &mut usize) {
        let Some((first, rest)) = parts.split_first() else {
            *found += 1;
            return;
        };
        for path in files::children(dir) {
            let name = path
                .file_name()
                .expect("a name")
                .to_string_lossy()
                .into_owned();
            let ok = *first == "*" || *first == name;
            if ok {
                walk(&path, &format!("{rel}/{name}"), rest, found);
            }
        }
    }
    let parts: Vec<&str> = pattern.split('/').collect();
    let mut found = 0;
    walk(w.common.project.home(), "", &parts, &mut found);
    assert_eq!(found, count, "{pattern}");
}

#[then(expr = "the home file {string} exists")]
fn home_exists(w: &mut World, rel: String) {
    assert!(
        files::exists(w.common.project.home().join(&rel)),
        "{rel} should exist"
    );
}

#[then(expr = "the home file {string} does not exist")]
fn home_absent(w: &mut World, rel: String) {
    assert!(
        !files::exists(w.common.project.home().join(&rel)),
        "{rel} should not exist"
    );
}

#[then(expr = "the home file {string} contains {string}")]
fn home_holds(w: &mut World, rel: String, text: String) {
    let held = w.common.project.read_home(&rel).expect("a home file");
    assert!(held.contains(&text), "{rel}: {held}");
}

#[then(expr = "the home file {string} does not contain {string}")]
fn home_lacks(w: &mut World, rel: String, text: String) {
    let held = w.common.project.read_home(&rel).expect("a home file");
    assert!(!held.contains(&text), "{rel}: {held}");
}

#[then(expr = "the output file {string} of session {string} exists")]
fn output_file_exists(w: &mut World, file: String, harp: String) {
    let path = w.output_dir(&harp).join(&file);
    assert!(files::exists(&path), "{} should exist", path.display());
}

fn array<'a>(doc: &'a Value, path: &str) -> &'a Vec<Value> {
    at(doc, path)
        .and_then(Value::as_array)
        .unwrap_or_else(|| panic!("no array at {path} in {doc}"))
}

#[then(expr = "the JSON output array {string} contains an object whose {string} is {string}")]
fn array_has(w: &mut World, path: String, key: String, value: String) {
    let doc = w.json();
    assert!(
        array(&doc, &path).iter().any(|o| is(o, &key, &value)),
        "no object with {key}={value}: {doc}"
    );
}

#[then(
    expr = "the JSON output array {string} contains an object whose {string} is {string} and whose {string} is {string}"
)]
fn array_has_two(
    w: &mut World,
    path: String,
    key: String,
    value: String,
    key2: String,
    value2: String,
) {
    let doc = w.json();
    assert!(
        array(&doc, &path)
            .iter()
            .any(|o| is(o, &key, &value) && is(o, &key2, &value2)),
        "no object with {key}={value} and {key2}={value2}: {doc}"
    );
}

/// Go's comparison: the value as the text of its JSON (a bool is `true`).
fn is(object: &Value, key: &str, want: &str) -> bool {
    match &object[key] {
        Value::String(s) => s == want,
        Value::Null => false,
        other => {
            let text = other.to_string();
            text == want
        }
    }
}

#[then(expr = "every object in the JSON output array {string} has a non-empty {string}")]
fn every_non_empty(w: &mut World, path: String, key: String) {
    let doc = w.json();
    for object in array(&doc, &path) {
        assert!(
            object[&key].as_str().is_some_and(|s| !s.is_empty()),
            "{object} lacks {key}"
        );
    }
}

fn hook_commands(w: &World, rel: &str, event: &str) -> Vec<String> {
    let text = w.common.project.read(rel).unwrap_or_default();
    let doc: Value = serde_json::from_str(&text).unwrap_or(Value::Null);
    let mut commands = Vec::new();
    for group in doc["hooks"][event].as_array().into_iter().flatten() {
        for hook in group["hooks"].as_array().into_iter().flatten() {
            if let Some(command) = hook["command"].as_str() {
                commands.push(command.to_string());
            }
        }
    }
    commands
}

#[then(expr = "the file {string} registers no SessionStart hook whose command contains {string}")]
fn registers_no_hook(w: &mut World, rel: String, text: String) {
    let commands = hook_commands(w, &rel, "SessionStart");
    assert!(!commands.iter().any(|c| c.contains(&text)), "{commands:?}");
}

#[then(expr = "the file {string} registers a SessionStart hook whose command contains {string}")]
fn registers_hook(w: &mut World, rel: String, text: String) {
    let commands = hook_commands(w, &rel, "SessionStart");
    assert!(commands.iter().any(|c| c.contains(&text)), "{commands:?}");
}

// The sweep (j001300).

/// The report names each (its exit is the scenario's to say: a preview
/// that would change something is 1 under `--detailed-exitcode`).
#[then(expr = "the sweep report names {string} and {string}")]
fn sweep_names(w: &mut World, a: String, b: String) {
    w.ran().contains(&a).contains(&b);
}

/// Nothing the command was run on changed: every session is still
/// recorded (its transcript is its events, ruling 87a), and every file of
/// the sessions' directories and scratch checkouts is as it was, byte for
/// byte.
#[then("every byte of every session is still on disk")]
fn bytes_on_disk(w: &mut World) {
    assert!(!w.seeded_harps.is_empty(), "no session was planted");
    for harp in &w.seeded_harps {
        assert!(recorded(w, harp), "{harp} is no longer recorded");
    }
    let after = w.session_bytes();
    let paths = |files: &[(PathBuf, Vec<u8>)]| -> Vec<PathBuf> {
        files.iter().map(|(path, _)| path.clone()).collect()
    };
    assert_eq!(paths(&after), paths(&w.before), "{}", w.ran().said());
    assert!(
        after == w.before,
        "a session's file changed: {}",
        w.ran().said()
    );
}

#[then(expr = "the sweep skipped {string} and {string} without touching either")]
fn sweep_skipped(w: &mut World, running: String, unproven: String) {
    w.ran()
        .succeeds()
        .contains("it is running")
        .contains("its liveness cannot be proven")
        .contains(&format!("ctxloom session remove {unproven} --even-if-live"));
    for harp in [running, unproven] {
        assert!(recorded(w, &harp), "{harp} is no longer recorded");
        for (h, name, _) in w.planted_scratch.iter().filter(|(h, _, _)| *h == harp) {
            assert!(
                files::is_dir(w.scratch_dir(h, name)),
                "{h}'s worktree {name} was removed"
            );
        }
    }
}

/// Whether the store still records `harp`: its transcript is its events
/// (ruling 87a), so the machine-written bulk is the session's record, and
/// `session show` finds it.
fn recorded(w: &World, harp: &str) -> bool {
    w.common
        .project
        .run(&["session", "show", harp, "--format", "json"])
        .code
        == 0
}

#[then(expr = "the machine-written bulk of {string} is gone")]
fn bulk_gone(w: &mut World, harp: String) {
    assert!(
        !recorded(w, &harp),
        "{harp} is still recorded: {}",
        w.ran().said()
    );
}

/// The preview names each note it would destroy, in ruling 104's warning
/// (`sessions-application-destroys-authored`), and destroyed nothing.
#[then(expr = "the preview warns it would destroy the design notes of {string}, naming each")]
fn warns_of_notes(w: &mut World, harp: String) {
    let notes = notes_of(w, &harp);
    let mut ran = w.ran().succeeds().contains(&format!(
        "session {harp} holds files a person wrote, and removing it destroys them with it"
    ));
    for rel in &notes {
        ran = ran.contains(rel);
    }
    assert!(recorded(w, &harp), "the preview destroyed {harp}");
    for rel in &notes {
        assert!(
            files::exists(w.session_dir(&harp).join(rel)),
            "the preview destroyed {rel}"
        );
    }
}

#[then(expr = "her filed design notes are in the output dir of {string}")]
fn notes_filed(w: &mut World, harp: String) {
    for rel in notes_of(w, &harp) {
        let body = files::read(w.output_dir(&harp).join(&rel))
            .unwrap_or_else(|e| panic!("her filed note {rel} is GONE: {e}"));
        assert_eq!(body, format!("{harp}: {rel}\n"));
    }
}

#[then(expr = "the machine-written bulk of {string} is still there")]
fn bulk_still_there(w: &mut World, harp: String) {
    assert!(
        recorded(w, &harp),
        "{harp} is no longer recorded: {}",
        w.ran().said()
    );
}

#[then(expr = "the sweep kept the transcript of {string} and named {string}")]
fn sweep_kept(w: &mut World, harp: String, named: String) {
    // Its transcript is its events (ruling 87a): kept while it is recorded.
    assert!(recorded(w, &harp), "{harp}'s transcript was destroyed");
    w.ran().contains(&harp).contains(&named);
}

// The scratch worktrees (j001300, S11).

#[then("the report names each scratch worktree with its harp, its owner and its verdict")]
fn worktrees_named(w: &mut World) {
    // plan supersedes Go: the owner is named by the state its lock proves,
    // not by the pid Go's lock file carried.
    // Each row by its fields: its path is the platform's (`C:\...\clean`
    // on Windows), so it is read as a path, never matched as text.
    w.ran().succeeds();
    let doc = w.json();
    for (name, verdict) in [("clean", "reapable"), ("wip", "spared")] {
        let row = at(&doc, &format!("worktrees[worktree={name}]"))
            .unwrap_or_else(|| panic!("no worktree {name} in {doc}"));
        assert_eq!(row["harp"], "amber-quiet-heron", "{row}");
        assert_eq!(row["owner_state"], "dead", "{row}");
        assert_eq!(row["verdict"], verdict, "{row}");
        let path = Path::new(row["path"].as_str().unwrap_or_default());
        assert!(
            path.ends_with(Path::new("amber-quiet-heron").join(name)),
            "{row}"
        );
    }
}

#[then("only the clean, provably-orphaned worktree is gone from disk")]
fn only_clean_gone(w: &mut World) {
    let mut problems = Vec::new();
    for (harp, name, _) in &w.planted_scratch {
        let dir = w.scratch_dir(harp, name);
        let gone = !files::is_dir(&dir);
        let reapable = name == "clean" && w.reapable(harp);
        if reapable && !gone {
            problems.push(format!(
                "{} is clean, its owner dead, and still on disk",
                dir.display()
            ));
        } else if !reapable && gone {
            problems.push(format!(
                "{} was removed and nothing made it safe",
                dir.display()
            ));
        }
    }
    assert!(problems.is_empty(), "{problems:?}: {}", w.ran().said());
}

#[then("the uncommitted work is still there, spared in place")]
fn wip_spared(w: &mut World) {
    let wip: Vec<_> = w
        .planted_scratch
        .iter()
        .filter(|(_, name, _)| name == "wip")
        .collect();
    assert!(!wip.is_empty(), "no uncommitted work was seeded");
    for (harp, name, _) in wip {
        let body = files::read(w.scratch_dir(harp, name).join("in-flight.go"))
            .unwrap_or_else(|e| panic!("the uncommitted work in {harp} is GONE: {e}"));
        assert!(body.contains(WIP_MARKER), "{body}");
    }
}

#[then("the report says why each spared worktree was left alone")]
fn spared_why(w: &mut World) {
    w.ran().contains("spared").contains("uncommitted changes");
}

#[then(expr = "no worktree of {string} is removed, and ctxloom says it could prove nothing safe")]
fn nothing_safe(w: &mut World, harp: String) {
    let own: Vec<_> = w
        .planted_scratch
        .iter()
        .filter(|(h, _, _)| *h == harp)
        .collect();
    assert!(!own.is_empty(), "no worktree was seeded under {harp}");
    for (h, name, _) in own {
        assert!(
            files::is_dir(w.scratch_dir(h, name)),
            "{h}'s {name} was REMOVED"
        );
    }
    // An action verb that changed nothing refuses.
    w.ran().fails().contains("skipped");
}

#[then("her own long-lived worktree is untouched and was never listed")]
fn foreign_untouched(w: &mut World) {
    assert!(
        !w.planted_foreign.is_empty(),
        "no foreign worktree was seeded"
    );
    for dir in &w.planted_foreign {
        assert!(files::is_dir(dir), "{} was removed", dir.display());
        // As the json spells it too: a Windows path's `\` is escaped there.
        let shown = dir.display().to_string();
        let quoted = serde_json::to_string(&shown).expect("a string");
        w.ran().lacks(&shown).lacks(quoted.trim_matches('"'));
    }
}

/// j001300: doctor's row names her worktree by its path, its branch unmerged
/// and its tree dirty, and the exact commands that remove it, the forced
/// forms (the safe ones would refuse it); and nothing touched it.
#[then(
    "the checks name the foreign worktree, that it is unmerged and dirty, and the exact commands to remove it"
)]
fn doctor_names_foreign(w: &mut World) {
    let doc = w.json();
    let row = array(&doc, "checks")
        .iter()
        .find(|c| c["marker"] == "DOCTOR-CHECK-FOREIGN-WORKTREES-s9")
        .unwrap_or_else(|| panic!("no foreign-worktrees row: {doc}"))
        .clone();
    assert_eq!(row["status"], "warn", "{row}");
    let detail = row["detail"].as_str().unwrap_or_default();
    let remedy = row["remedy"].as_str().unwrap_or_default();
    assert!(
        !w.planted_foreign.is_empty(),
        "no foreign worktree was seeded"
    );
    for dir in &w.planted_foreign {
        // As git lists it: the path it was added at.
        let shown = dir.display().to_string();
        assert!(
            detail.contains(&format!("{shown} (branch stale-feature, unmerged, dirty)")),
            "{row}"
        );
        assert!(
            remedy.contains(&format!("worktree remove --force {shown}")),
            "{row}"
        );
        assert!(remedy.contains("branch -D stale-feature"), "{row}");
        let body = files::read(dir.join("uncommitted.txt"))
            .unwrap_or_else(|e| panic!("her uncommitted work is GONE: {e}"));
        assert!(body.contains(WIP_MARKER), "{body}");
    }
}

#[then("the scratch worktree is still registered with git")]
fn still_registered(w: &mut World) {
    let listed = w.git(w.common.project.dir(), &["worktree", "list", "--porcelain"]);
    for (harp, name, dirty) in &w.planted_scratch {
        if *dirty {
            let dir = files::canonical(w.scratch_dir(harp, name));
            assert!(
                listed_worktrees(&listed).contains(&dir),
                "{} is not registered: {listed}",
                dir.display()
            );
        }
    }
}

/// The worktrees `git worktree list --porcelain` names, each resolved: git
/// spells a path its own way (`C:/a/b` on Windows), so a listed worktree is
/// compared as a path, never as text.
fn listed_worktrees(porcelain: &str) -> Vec<PathBuf> {
    porcelain
        .lines()
        .filter_map(|line| line.strip_prefix("worktree "))
        .map(|p| {
            files::fs()
                .canonicalize(Path::new(p))
                .unwrap_or_else(|_| PathBuf::from(p))
        })
        .collect()
}

// ------------------------------------------------------------ the features

common_steps!();

/// Run one feature file with this World: whether it passed, and each
/// scenario's outcome.
pub(super) fn run(file: &std::path::Path) -> super::gherkin_common::FeatureRun {
    super::gherkin_common::run_feature::<World>(file)
}
