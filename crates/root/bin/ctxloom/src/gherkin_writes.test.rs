//! The steps of step 10b's spec (`cli/atomic_writes.feature`, the write
//! Rules of `cli/library_changes.feature`, and the write rows of
//! `cli/daemon.feature`, `cli/bundle.feature` and
//! `cli/session_remove.feature`): what a write leaves in the tree, in its
//! staging and in its trash, the record a write leaves (`ctxloom test
//! library-events`), the commits ctxloom makes in the home, and a file
//! changed by hand while her editor is open. Every step drives the real
//! program: git itself, the files, or the binary.
//!
//! What a write is checked against is taken just before the command runs
//! ([`before_run`]): `git status` in the project, and the library tree's
//! bytes for a removal or an edit.

use std::path::{Path, PathBuf};
use std::thread::JoinHandle;
use std::time::{Duration, Instant};

use cucumber::gherkin::Step;
use cucumber::{given, then, when};
use serde_json::Value;

use super::World;
use crate::tests::files;
use crate::tests::gherkin_common::{doc, split};
use crate::tests::library::reports_as;

/// The test-support clock offset, a span, a daemon a command starts
/// adds to its clock (QUESTIONS.md 5).
pub(super) const CLOCK_OFFSET: &str = "CTXLOOM_TEST_CLOCK_OFFSET";

/// The test-support switch that turns the CLI's one retry off for the
/// command it is set on.
const NO_RETRY: &str = "CTXLOOM_TEST_NO_RETRY";

/// The kill switch at a step of a write (the daemon's `test-die-at`).
const DIE_AT: &str = "test-die-at";

/// The test-support switch that holds a daemon at a step of a write until
/// the file is gone (its one line the step's word, as `test-die-at`'s).
const HOLD_AT: &str = "test-hold-at";

/// The staging and the trash of the project's checkout (D3(a), G12).
const STAGING: &str = ".ctxloom/state/staging";
const TRASH: &str = ".ctxloom/state/trash";

/// How long the harness waits for what a daemon does.
const WAIT: Duration = Duration::from_secs(20);

/// What the write steps keep between steps.
#[derive(Default)]
pub(super) struct State {
    /// The command line about to run.
    line: Option<String>,
    /// `git status --porcelain -uall` in the project before the last write.
    git_before: Option<String>,
    /// Every file of the project's `.ctxloom` but `state/`, relative to it,
    /// before the last removal or edit.
    tree_before: Vec<(String, Vec<u8>)>,
    /// The span the daemon's clock has been moved on by.
    moved: std::time::Duration,
    /// The other process that creates a bundle while a creation is staged:
    /// the bundle, and whether it found the staging and wrote its bundle.
    other: Option<(String, JoinHandle<bool>)>,
}

/// The file at `rel` in the project, by its `/`-separated steps.
pub(super) fn in_project(w: &World, rel: &str) -> PathBuf {
    rel.split('/')
        .fold(w.common.project.dir().to_path_buf(), |path, step| {
            path.join(step)
        })
}

/// The file at `rel` in the home.
fn in_home(w: &World, rel: &str) -> PathBuf {
    rel.split('/')
        .fold(w.common.project.home().to_path_buf(), |path, step| {
            path.join(step)
        })
}

/// `ctxloom args` in the project now, with the scenario's environment and
/// `extra`.
pub(super) fn run_now(w: &World, extra: &[(&str, &str)], args: &[&str]) -> crate::tests::Ran {
    let mut env: Vec<(&str, &str)> = w
        .common
        .env
        .iter()
        .map(|(k, v)| (k.as_str(), v.as_str()))
        .collect();
    env.extend_from_slice(extra);
    w.common.project.run_with(&env, args)
}

/// The words of a command line after `ctxloom`.
fn words(line: &str) -> Vec<String> {
    let words = split(line);
    match words.first().map(String::as_str) {
        Some("ctxloom") => words[1..].to_vec(),
        _ => words,
    }
}

/// The verbs that write.
const WRITE_VERBS: [&str; 10] = [
    "create", "remove", "edit", "set", "export", "import", "move", "add", "distill", "rename",
];

/// Whether `line` is a write: a verb that writes, or `clean`.
fn is_write(line: &str) -> bool {
    let words = words(line);
    if words.first().map(String::as_str) == Some("test") {
        return false;
    }
    words.first().map(String::as_str) == Some("clean")
        || words
            .iter()
            .take(3)
            .any(|word| WRITE_VERBS.contains(&word.as_str()))
}

/// Whether `line` is one of the setup nouns' writes (step 10c U4-U6),
/// which name no write verb: git's status is taken before it too.
fn is_setup_write(line: &str) -> bool {
    let words = words(line);
    let first = |verb: &str| words.first().map(String::as_str) == Some(verb);
    let second = |verb: &str| words.get(1).map(String::as_str) == Some(verb);
    (first("deps") && (second("pull") || second("upgrade")))
        || first("init") && words.len() == 1
        || first("manage") && words.iter().any(|w| w == "install")
        || first("materialize")
        || first("session") && (second("keep") || second("rename") || second("import"))
        || first("doctor") && words.iter().any(|w| w == "--fix")
}

/// Whether `line` takes something out of, or changes a file in, the tree:
/// what the trash and an edit's one file are checked against.
fn takes_or_edits(line: &str) -> bool {
    let words = words(line);
    words.first().map(String::as_str) == Some("clean")
        || words
            .iter()
            .take(3)
            .any(|word| matches!(word.as_str(), "remove" | "edit" | "import"))
}

/// The write's verb as the CLI names it (`bundle create`, `config set`,
/// `mcp server set`): the record's `verb`.
pub(super) fn verb_of(line: &str) -> String {
    let words = words(line);
    let take = if words.first().map(String::as_str) == Some("mcp") {
        3
    } else {
        2
    };
    words
        .iter()
        .take(take)
        .cloned()
        .collect::<Vec<_>>()
        .join(" ")
}

/// Whether `line` writes the library (not a session's removal).
pub(super) fn is_library_write(line: &str) -> bool {
    (is_write(line) || is_setup_write(line))
        && words(line).first().map(String::as_str) != Some("session")
}

/// Before a command: its line, kept for [`before_run`].
pub(super) fn before_line(w: &mut World, line: &str) {
    w.writes.line = Some(line.to_string());
}

/// Just before a command runs (after the sessions are seeded): what a write
/// is later checked against.
pub(super) fn before_run(w: &mut World) {
    let Some(line) = w.writes.line.take() else {
        return;
    };
    if !is_write(&line) && !is_setup_write(&line) {
        return;
    }
    w.writes.git_before = git_status(w);
    if takes_or_edits(&line) {
        w.writes.tree_before = tree_of(&in_project(w, ".ctxloom"))
            .into_iter()
            .filter(|(rel, _)| rel != "state" && !rel.starts_with("state/"))
            .collect();
    }
}

/// `git status --porcelain -uall` in the project, when it is in git, but
/// for `.ctxloom/state/`: ctxloom's private working state (a read mints the
/// checkout harp there), which a scaffolded project's `.ctxloom/.gitignore`
/// (`/state`) hides and the fixture project, made without the scaffold, does
/// not; as [`before_run`]'s tree leaves it out.
fn git_status(w: &World) -> Option<String> {
    let project = &w.common.project;
    if !files::exists(project.dir().join(".git")) {
        return None;
    }
    let output = project.git_output(
        project.dir(),
        &[
            "status",
            "--porcelain",
            "-uall",
            "--",
            ".",
            ":(exclude).ctxloom/state",
        ],
    );
    output
        .status
        .success()
        .then(|| String::from_utf8_lossy(&output.stdout).into_owned())
}

/// Every file below `dir` (or `dir` itself, a file), relative to it with
/// `/` between its steps, and its bytes; sorted.
pub(super) fn tree_of(dir: &Path) -> Vec<(String, Vec<u8>)> {
    fn walk(dir: &Path, prefix: &str, out: &mut Vec<(String, Vec<u8>)>) {
        for entry in files::children(dir) {
            let name = entry.file_name().expect("a name").to_string_lossy();
            let rel = if prefix.is_empty() {
                name.to_string()
            } else {
                format!("{prefix}/{name}")
            };
            if files::is_dir(&entry) {
                walk(&entry, &rel, out);
            } else if let Ok(bytes) = files::read_bytes(&entry) {
                out.push((rel, bytes));
            }
        }
    }
    let mut out = Vec::new();
    if files::is_dir(dir) {
        walk(dir, "", &mut out);
    } else if let Ok(bytes) = files::read_bytes(dir) {
        out.push((String::new(), bytes));
    }
    out.sort();
    out
}

/// Whether nothing is under `dir`: it is absent, or empty.
pub(super) fn empty(dir: &Path) -> bool {
    !files::exists(dir) || files::children(dir).is_empty()
}

/// The names of what is under `dir`, for a message.
fn listing(dir: &Path) -> Vec<String> {
    tree_of(dir).into_iter().map(|(rel, _)| rel).collect()
}

/// The library tree's record, as `ctxloom test library-events` prints it:
/// the project checkout's, or the home's.
pub(super) fn library_events(w: &World, home: bool) -> Vec<Value> {
    let mut args = vec!["test", "library-events", "--format", "json"];
    if home {
        args.push("--home");
    }
    let ran = run_now(w, &[], &args);
    assert_eq!(ran.code, 0, "library events: {}", ran.said());
    ran.json()["events"].as_array().cloned().unwrap_or_default()
}

/// The `ChangeIntended` events of `events` whose verb is `verb`.
pub(super) fn intents<'a>(events: &'a [Value], verb: &str) -> Vec<&'a Value> {
    events
        .iter()
        .filter(|e| e["type"] == "ChangeIntended" && e["verb"] == verb)
        .collect()
}

/// The records of a write the record holds as made, not intended: a
/// `bundle export`'s `BundleExported` (AIP-155: the one write with no
/// intent); none for any other verb.
pub(super) fn made<'a>(events: &'a [Value], verb: &str) -> Vec<&'a Value> {
    events
        .iter()
        .filter(|e| verb == "bundle export" && e["type"] == "BundleExported")
        .collect()
}

// ------------------------------------------------------------------ git

#[then("git sees nothing changed in the project")]
fn git_unchanged(w: &mut World) {
    let before = w
        .writes
        .git_before
        .clone()
        .expect("git's status was taken before the write (the project is in git)");
    let now = git_status(w).expect("the project is still in git");
    assert_eq!(
        now,
        before,
        "git sees a change the write made: {}",
        w.common.ran().said()
    );
}

#[then(expr = "the project's last commit is still {string}")]
fn project_last_commit(w: &mut World, message: String) {
    let last = w.common.project.git(&["log", "-1", "--format=%s"]);
    assert_eq!(last.trim(), message, "the project's last commit");
}

#[then("the home directory's repository has no commit")]
fn home_dir_no_commit(w: &mut World) {
    let home = w.common.project.home().to_path_buf();
    let head = w
        .common
        .project
        .git_output(&home, &["rev-parse", "--verify", "-q", "HEAD"]);
    assert!(
        !head.status.success(),
        "the home has a commit: {}",
        String::from_utf8_lossy(&head.stdout)
    );
}

/// The home's repository at `rel`: its own (its `.git`), with a commit.
fn home_repository(w: &World, rel: &str) -> PathBuf {
    let dir = in_home(w, rel);
    assert!(
        files::exists(dir.join(".git")),
        "{} is no repository of its own",
        dir.display()
    );
    dir
}

/// The number of commits on the home repository's HEAD (none: 0).
fn commits(w: &World, dir: &Path) -> usize {
    let project = &w.common.project;
    if !project
        .git_output(dir, &["rev-parse", "--verify", "-q", "HEAD"])
        .status
        .success()
    {
        return 0;
    }
    project
        .git_in(dir, &["rev-list", "--count", "HEAD"])
        .trim()
        .parse()
        .expect("a count")
}

#[then(regex = r#"^the home's "([^"]+)" repository has exactly (\d+) commits?$"#)]
fn home_commit_count(w: &mut World, rel: String, count: usize) {
    let dir = home_repository(w, &rel);
    assert_eq!(commits(w, &dir), count, "the commits of {}", dir.display());
}

/// One field of the home repository's last commit (`git log -1`'s format).
fn last_commit(w: &World, rel: &str, format: &str) -> String {
    let dir = home_repository(w, rel);
    assert!(commits(w, &dir) > 0, "{} has no commit", dir.display());
    w.common
        .project
        .git_in(&dir, &["log", "-1", &format!("--format={format}")])
        .trim()
        .to_string()
}

#[then(expr = "the home's {string} repository's last commit is by {string}")]
fn home_last_commit_by(w: &mut World, rel: String, ident: String) {
    assert_eq!(last_commit(w, &rel, "%an <%ae>"), ident, "its author");
    assert_eq!(last_commit(w, &rel, "%cn <%ce>"), ident, "its committer");
}

/// The request id of the home tree's last write: its last `ChangeIntended`.
fn home_request_id(w: &World) -> String {
    let events = library_events(w, true);
    events
        .iter()
        .rev()
        .find(|e| e["type"] == "ChangeIntended")
        .and_then(|e| e["request_id"].as_str())
        .unwrap_or_else(|| panic!("the home's record holds no ChangeIntended: {events:?}"))
        .to_string()
}

#[then(expr = "the home's {string} repository's last commit's message is {string}")]
fn home_last_commit_message(w: &mut World, rel: String, message: String) {
    let said = last_commit(w, &rel, "%B");
    let expected = if message.contains("<its request id>") {
        message.replace("<its request id>", &home_request_id(w))
    } else {
        message
    };
    assert_eq!(said, expected, "the last commit's message");
}

#[then(expr = "the home's {string} repository's last commit changed only {string}")]
fn home_last_commit_changed(w: &mut World, rel: String, path: String) {
    let dir = home_repository(w, &rel);
    assert!(commits(w, &dir) > 0, "{} has no commit", dir.display());
    let changed = w
        .common
        .project
        .git_in(&dir, &["show", "--name-only", "--format=", "HEAD"]);
    let changed: Vec<&str> = changed.lines().filter(|l| !l.is_empty()).collect();
    assert_eq!(changed, [path.as_str()], "what the last commit changed");
}

// ------------------------------------------------------------------ the output

#[then(expr = "the output reports {string} as the value of {string}")]
fn reports_same(w: &mut World, path: String, other: String) {
    let doc = w.common.json();
    assert_eq!(
        reports_as(&doc, &path),
        reports_as(&doc, &other),
        "{path} and {other} in {doc}"
    );
}

// ------------------------------------------------------------------ staging and trash

#[then(expr = "nothing is left under {string}")]
fn nothing_left(w: &mut World, rel: String) {
    let dir = in_project(w, &rel);
    assert!(empty(&dir), "{} holds {:?}", dir.display(), listing(&dir));
}

/// The request id of the removal that took `rel` (relative to `.ctxloom`)
/// to the trash: the last `ChangeIntended` with a trash step of that path.
fn removal_of(w: &World, rel: &str) -> String {
    let events = library_events(w, false);
    events
        .iter()
        .rev()
        .filter(|e| e["type"] == "ChangeIntended")
        .find(|e| {
            e["steps"].as_array().is_some_and(|steps| {
                steps
                    .iter()
                    .any(|s| s["kind"] == "trash" && s["path"] == rel)
            })
        })
        .and_then(|e| e["request_id"].as_str())
        .unwrap_or_else(|| panic!("no removal of {rel} was intended: {events:?}"))
        .to_string()
}

#[then(expr = "the checkout's trash holds {string} of that removal, whole")]
fn trash_holds(w: &mut World, path: String) {
    let rel = path
        .strip_prefix(".ctxloom/")
        .unwrap_or_else(|| panic!("{path} is under .ctxloom"));
    let request = removal_of(w, rel);
    let slot = in_project(w, &format!("{TRASH}/{request}/{rel}"));
    assert!(
        files::exists(&slot),
        "{} is not in the trash; it holds {:?}",
        slot.display(),
        listing(&in_project(w, TRASH))
    );
    let below = format!("{rel}/");
    let before: Vec<(String, Vec<u8>)> = w
        .writes
        .tree_before
        .iter()
        .filter_map(|(at, bytes)| {
            if at == rel {
                Some((String::new(), bytes.clone()))
            } else {
                at.strip_prefix(&below)
                    .map(|inner| (inner.to_string(), bytes.clone()))
            }
        })
        .collect();
    assert!(!before.is_empty(), "{path} held nothing before the removal");
    let kept = tree_of(&slot);
    let names = |tree: &[(String, Vec<u8>)]| -> Vec<String> {
        tree.iter().map(|(rel, _)| rel.clone()).collect()
    };
    assert_eq!(names(&kept), names(&before), "the trash holds {path} whole");
    assert!(kept == before, "a file of {path} changed in the trash");
}

#[then("the checkout's trash holds nothing")]
fn trash_empty(w: &mut World) {
    let dir = in_project(w, TRASH);
    assert!(empty(&dir), "the trash holds {:?}", listing(&dir));
}

/// Wait, bounded, until no daemon serves this home.
fn none_serving(w: &World) {
    let deadline = Instant::now() + WAIT;
    loop {
        let status = run_now(w, &[], &["daemon", "status", "--format", "json"]);
        assert_eq!(status.code, 0, "{}", status.said());
        if status.json()["daemons"][0].get("pid").is_none() {
            return;
        }
        assert!(Instant::now() < deadline, "the daemon never stopped");
        std::thread::sleep(Duration::from_millis(50));
    }
}

/// The daemon is stopped, and the next one a command starts runs with its
/// clock a span on (the test-support offset); two reads through it, each
/// answered after a round of its own, so a round has run.
#[when(regex = r#"^"([^"]+)" (more )?pass on the daemon's clock, and its next round runs$"#)]
fn time_passes(w: &mut World, span: String, more: String) {
    let span = duration_text::parse(&span).expect("a span of the duration grammar");
    w.writes.moved = if more.is_empty() {
        span
    } else {
        w.writes.moved + span
    };
    let stopped = run_now(w, &[], &["daemon", "stop"]);
    assert!(
        matches!(stopped.code, 0 | 20),
        "daemon stop: {}",
        stopped.said()
    );
    none_serving(w);
    w.common
        .set_env(CLOCK_OFFSET, format!("{}s", w.writes.moved.as_secs()));
    for _ in 0..2 {
        let read = run_now(w, &[], &["bundle", "list", "--format", "json"]);
        assert_eq!(read.code, 0, "a read on the later daemon: {}", read.said());
    }
}

/// A `bundle create` killed outright once it is staged, with the CLI's
/// one retry off: what that daemon staged is left.
#[given(expr = "a write left its staging behind under {string} when its daemon was killed")]
fn staging_left(w: &mut World, rel: String) {
    let switch = w.common.project.home().join(".ctxloom/daemon").join(DIE_AT);
    files::put(&switch, "staged");
    let ran = run_now(
        w,
        &[(NO_RETRY, "1")],
        &["bundle", "create", "notes", "--description", "my notes"],
    );
    let taken = !files::exists(&switch);
    if !taken {
        files::remove(&switch);
    }
    assert!(
        taken,
        "the daemon was never killed once the write was staged: {}",
        ran.said()
    );
    assert_ne!(
        ran.code,
        0,
        "the killed write, not retried, fails: {}",
        ran.said()
    );
    let dir = in_project(w, &rel);
    assert!(!empty(&dir), "nothing was left staged under {rel}");
}

// ------------------------------------------------------------------ the other process

/// What the other process writes as its bundle: its marker.
const OTHER_PROCESS: &str = "OTHER-PROCESS-WROTE-THIS";

/// Arm the daemon's hold at `staged`; a thread of the harness, standing for
/// another process, waits for the creation's staging, writes the bundle
/// itself, then lets the daemon go on.
pub(super) fn other_creates(w: &mut World, bundle: String) {
    let hold = w
        .common
        .project
        .home()
        .join(".ctxloom/daemon")
        .join(HOLD_AT);
    files::put(&hold, "staged");
    let staging = in_project(w, STAGING);
    let manifest = in_project(
        w,
        &format!(".ctxloom/content/bundles/v2/{bundle}/bundle.yaml"),
    );
    let other = std::thread::spawn(move || {
        let deadline = Instant::now() + WAIT;
        let mut staged = false;
        while Instant::now() < deadline {
            if !empty(&staging) {
                staged = true;
                break;
            }
            std::thread::sleep(Duration::from_millis(20));
        }
        if staged {
            files::put(
                &manifest,
                format!("version: 1.0.0\ndescription: {OTHER_PROCESS}\n"),
            );
        }
        if files::exists(&hold) {
            files::remove(&hold);
        }
        staged
    });
    w.writes.other = Some((bundle, other));
}

pub(super) fn other_untouched(w: &mut World, bundle: String) {
    let (made, other) = w.writes.other.take().expect("another process was set up");
    assert_eq!(made, bundle, "the other process's bundle");
    let staged = other.join().expect("the other process ran");
    assert!(
        staged,
        "nothing was ever staged under {STAGING} while the creation ran: {}",
        w.common.ran().said()
    );
    let manifest = format!(".ctxloom/content/bundles/v2/{bundle}/bundle.yaml");
    let held = w.common.project.read(&manifest).unwrap_or_default();
    assert!(
        held.contains(OTHER_PROCESS),
        "{manifest} is not the other process's: {held:?}"
    );
    let staging = in_project(w, STAGING);
    assert!(empty(&staging), "left staged: {:?}", listing(&staging));
}

// ------------------------------------------------------------------ the tree a write left

/// The kill a step armed was taken: the daemon reached its point and was
/// killed there (its `test-die-at` is gone).
pub(super) fn kill_taken(w: &World) {
    let switch = w.common.project.home().join(".ctxloom/daemon").join(DIE_AT);
    assert!(
        !files::exists(&switch),
        "the daemon was never killed at {:?}: {}",
        files::read(&switch).unwrap_or_default().trim(),
        w.common.ran().said()
    );
}

/// What a write left, by its command line (atomic_writes' rows): the file
/// that must hold what was asked, and the text.
fn written(line: &str) -> (String, String) {
    let words = words(line);
    let words: Vec<&str> = words.iter().map(String::as_str).collect();
    let v2 = ".ctxloom/content/bundles/v2";
    match words.as_slice() {
        ["bundle", "create", name, "--description", description, ..] => {
            (format!("{v2}/{name}/bundle.yaml"), description.to_string())
        }
        ["fragment", "create", item, "--body", body, ..] => {
            let (bundle, name) = item
                .split_once("#fragments/")
                .expect("bundle#fragments/name");
            (
                format!("{v2}/{bundle}/fragments/{name}.md"),
                body.to_string(),
            )
        }
        ["skill", "create", item, ..] => {
            let (bundle, name) = item.split_once("#skills/").expect("bundle#skills/name");
            (
                format!("{v2}/{bundle}/skills/{name}/SKILL.md"),
                String::new(),
            )
        }
        ["config", "set", key, value, ..] => {
            let last = key.rsplit('.').next().expect("a key");
            (
                ".ctxloom/config.yaml".to_string(),
                format!("{last}: {value}"),
            )
        }
        _ => panic!("no write the harness knows: {line}"),
    }
}

/// The change the last command made is in the tree, whole, nothing is left
/// staged, every bundle still reads, and the record holds one intent of it.
pub(super) fn change_whole(w: &mut World) {
    kill_taken(w);
    let line = w.common.line.clone();
    let (file, text) = written(&line);
    let held = w
        .common
        .project
        .read(&file)
        .unwrap_or_else(|| panic!("{file} was not written: {}", w.common.ran().said()));
    assert!(held.contains(&text), "{file} lacks {text:?}: {held:?}");
    let staging = in_project(w, STAGING);
    assert!(empty(&staging), "left staged: {:?}", listing(&staging));
    let listed = run_now(w, &[], &["bundle", "list", "--format", "json"]);
    assert_eq!(listed.code, 0, "every bundle reads: {}", listed.said());
    assert!(
        !listed.stdout.contains("unreadable") && !listed.stderr.contains("unreadable"),
        "a bundle no longer reads: {}",
        listed.said()
    );
    let events = library_events(w, false);
    let verb = verb_of(&line);
    assert_eq!(
        intents(&events, &verb).len(),
        1,
        "one intent of {verb}: {events:?}"
    );
}

/// Only `path` of the bundle changed in the last edit.
pub(super) fn only_changed(w: &mut World, path: String, bundle: String) {
    let below = format!("content/bundles/v2/{bundle}/");
    let before: Vec<(String, Vec<u8>)> = w
        .writes
        .tree_before
        .iter()
        .filter_map(|(rel, bytes)| {
            rel.strip_prefix(&below)
                .map(|inner| (inner.to_string(), bytes.clone()))
        })
        .collect();
    assert!(
        !before.is_empty(),
        "the bundle {bundle} was seen before the edit"
    );
    let now = tree_of(&in_project(w, &format!(".ctxloom/{below}")));
    let mut changed: Vec<String> = Vec::new();
    for (rel, bytes) in &now {
        if before.iter().find(|(at, _)| at == rel).map(|(_, b)| b) != Some(bytes) {
            changed.push(rel.clone());
        }
    }
    for (rel, _) in &before {
        if !now.iter().any(|(at, _)| at == rel) {
            changed.push(rel.clone());
        }
    }
    changed.sort();
    changed.dedup();
    assert_eq!(changed, [path], "what the edit changed of {bundle}");
}

/// The skill is one directory of its bundle, its metadata inside it, and
/// nothing beside it.
pub(super) fn skill_one_dir(w: &mut World, skill: String, dir: String, bundle: String) {
    let base = format!(".ctxloom/content/bundles/v2/{bundle}");
    let at = in_project(w, &format!("{base}/{dir}"));
    assert!(files::is_dir(&at), "{} is no directory", at.display());
    assert!(
        files::exists(at.join(".meta.yaml")),
        "{} holds no .meta.yaml: {:?}",
        at.display(),
        listing(&at)
    );
    let beside = in_project(w, &format!("{base}/skills/.{skill}.meta.yaml"));
    assert!(
        !files::exists(&beside),
        "{} is still beside the skill",
        beside.display()
    );
}

/// The bundle is the one `exported/<bundle>` holds, file for file, and no
/// trash is left.
pub(super) fn import_converged(w: &mut World, bundle: String) {
    kill_taken(w);
    let source = tree_of(&in_project(w, &format!("exported/{bundle}")));
    assert!(!source.is_empty(), "exported/{bundle} holds nothing");
    let installed = tree_of(&in_project(
        w,
        &format!(".ctxloom/content/bundles/v2/{bundle}"),
    ));
    let names = |tree: &[(String, Vec<u8>)]| -> Vec<String> {
        tree.iter().map(|(rel, _)| rel.clone()).collect()
    };
    assert_eq!(names(&installed), names(&source), "the imported {bundle}");
    assert!(
        installed == source,
        "a file of {bundle} is not the imported one"
    );
    let trash = in_project(w, TRASH);
    assert!(empty(&trash), "the trash holds {:?}", listing(&trash));
}

// ------------------------------------------------------------------ her editor

/// The marker editor, made to change `rel` by hand first (and, `back`,
/// read the library and put the file's bytes back), before it appends its
/// marker: an `sh` script the home config names.
#[given(
    regex = r#"^the file "([^"]+)" is changed by hand while the editor is open(, read, and changed back)?, to:$"#
)]
fn changed_while_open(w: &mut World, step: &Step, rel: String, back: String) {
    let project = &w.common.project;
    let target = in_project(w, &rel);
    let before = files::read_bytes(&target).unwrap_or_else(|e| panic!("{}: {e}", target.display()));
    let scratch = project.home().join("..").join("hand-edit");
    files::put(scratch.join("text"), format!("{}\n", doc(step)));
    files::put(scratch.join("before"), before);
    let quote = |path: &Path| format!("'{}'", path.to_str().expect("utf-8"));
    let mut script = format!(
        "cat {} > {}\n",
        quote(&scratch.join("text")),
        quote(&target)
    );
    if !back.is_empty() {
        script.push_str(&format!(
            "CTXLOOM_ROOT={} HOME={} USERPROFILE={} PATH={} {} bundle list --format json > /dev/null 2>&1 || exit 3\ncat {} > {}\n",
            quote(project.dir()),
            quote(project.home()),
            quote(project.home()),
            quote(Path::new(
                &crate::tests::host_var("PATH").unwrap_or_default()
            )),
            quote(crate::tests::binary()),
            quote(&scratch.join("before")),
            quote(&target),
        ));
    }
    script.push_str("printf '\\nEDITED-BY-TEST\\n' >> \"$1\"\n");
    let editor = scratch.join("editor.sh");
    files::put(&editor, script);
    project.write_home(
        ".ctxloom/config.yaml",
        &format!(
            "schema_version: 9\neditor:\n  command: sh\n  args:\n    - '{}'\n",
            editor.to_str().expect("utf-8")
        ),
    );
}

// ------------------------------------------------------------------ pulls (10c U4)

// A pull's git remotes are seeded by `cli`'s `a git remote {string}
// serving a ctxloom bundle` (the World's one such step).

/// The CLI's one retry is off for the next command alone.
#[given("the command's retry is turned off")]
fn retry_off(w: &mut World) {
    w.common
        .next_env
        .push((NO_RETRY.to_string(), "1".to_string()));
}

/// Each entry `lock.yaml` names, `(identity, sha, url)`, in its order: the
/// two-space key under `bundles:` and its four-space `sha:` and `url:`.
fn lock_pins(lock: &str) -> Vec<(String, String, String)> {
    let mut pins: Vec<(String, String, String)> = Vec::new();
    let mut in_bundles = false;
    for line in lock.lines() {
        if !line.starts_with(' ') {
            in_bundles = line.trim_end() == "bundles:";
            continue;
        }
        if !in_bundles {
            continue;
        }
        if let Some(key) = line
            .strip_prefix("  ")
            .filter(|rest| !rest.starts_with(' '))
        {
            let key = key.trim_end().trim_end_matches(':');
            pins.push((key.to_string(), String::new(), String::new()));
        } else if let Some(pin) = pins.last_mut() {
            let field = line.trim();
            if let Some(sha) = field.strip_prefix("sha: ") {
                pin.1 = sha.to_string();
            } else if let Some(url) = field.strip_prefix("url: ") {
                pin.2 = url.to_string();
            }
        }
    }
    pins
}

/// Every directory below `dir` named `name`.
fn dirs_named(dir: &Path, name: &str) -> Vec<PathBuf> {
    let mut found = Vec::new();
    for entry in files::children(dir) {
        if !files::is_dir(&entry) {
            continue;
        }
        if entry.file_name().is_some_and(|n| n == name) {
            found.push(entry.clone());
        }
        found.extend(dirs_named(&entry, name));
    }
    found
}

/// For each pin of the lock, the cache holds one directory of its sha
/// whose `.identity` names it, and it holds the remote's bundle tree at
/// that commit, file for file; its bundle has no other commit's directory
/// beside it. A kill a Given planted was taken, so the pull was cut short
/// where the scenario says.
#[then("every tree the lock pins is installed whole, once")]
fn installed_whole(w: &mut World) {
    let switch = w.common.project.home().join(".ctxloom/daemon").join(DIE_AT);
    assert!(
        !files::exists(&switch),
        "the pull was never killed where the scenario says (the daemon never took {:?})",
        files::read(&switch).unwrap_or_default().trim()
    );
    let lock = files::read(in_project(w, ".ctxloom/lock.yaml")).expect("a lock");
    let pins = lock_pins(&lock);
    assert!(!pins.is_empty(), "the lock pins nothing:\n{lock}");
    let cache = in_project(w, ".ctxloom/cache/bundles");
    for (identity, sha, url) in pins {
        let slots: Vec<PathBuf> = dirs_named(&cache, &sha)
            .into_iter()
            .filter(|dir| {
                files::read(dir.join(".identity")).is_ok_and(|said| said.trim() == identity)
            })
            .collect();
        assert_eq!(slots.len(), 1, "{identity} at {sha}: {slots:?}");
        let slot = &slots[0];
        let bundle = identity.rsplit('/').next().expect("a bundle");
        // The remote publishes each bundle where every repository does
        // (deps-ports' `BUNDLES_IN_REPOSITORY`).
        let published = format!(".ctxloom/content/bundles/v2/{bundle}/");
        let repo = files::path_of_file_url(&url);
        let listed = w
            .common
            .project
            .git_output(&repo, &["ls-tree", "-r", "--name-only", &sha, &published]);
        assert!(listed.status.success(), "{identity}: git ls-tree in {url}");
        let mut want: Vec<String> = String::from_utf8_lossy(&listed.stdout)
            .lines()
            .map(|path| path.trim_start_matches(&published).to_string())
            .collect();
        want.sort();
        let have: Vec<String> = tree_of(slot)
            .into_iter()
            .map(|(rel, _)| rel)
            .filter(|rel| rel != ".identity")
            .collect();
        assert_eq!(have, want, "{identity} at {sha} is not the remote's tree");
        let parent = slot.parent().expect("a bundle directory");
        let others: Vec<PathBuf> = files::children(parent)
            .into_iter()
            .filter(|dir| files::is_dir(dir) && dir != slot)
            .filter(|dir| {
                !dir.file_name()
                    .is_some_and(|n| n.to_string_lossy().starts_with('.'))
            })
            .collect();
        assert!(
            others.is_empty(),
            "{identity}: another commit's tree beside it: {others:?}"
        );
    }
}

/// The lock's pins, each its identity, commit and address; a line outside
/// `bundles:` is none of them, and a pin's other fields are passed over.
#[test]
fn a_locks_pins_are_its_bundles_with_their_commit_and_address() {
    let lock = "schema_version: 3\nbundles:\n  ctxloom+git://h/r//bundles/a:\n    sha: 01ab\n    url: file:///r\n    kind: branch\n  ctxloom+git://h/r//bundles/b:\n    held: true\n    sha: 02cd\n    url: file:///s\nother:\n  x:\n    sha: 99\n";
    assert_eq!(
        lock_pins(lock),
        vec![
            (
                "ctxloom+git://h/r//bundles/a".to_string(),
                "01ab".to_string(),
                "file:///r".to_string()
            ),
            (
                "ctxloom+git://h/r//bundles/b".to_string(),
                "02cd".to_string(),
                "file:///s".to_string()
            ),
        ]
    );
    assert!(lock_pins("schema_version: 3\n").is_empty());
}

/// The setup nouns' writes (step 10c U4-U6) are writes too, though they
/// name no write verb; their reads, and the rest, are not.
#[test]
fn the_setup_nouns_writes_are_writes_and_their_reads_are_not() {
    for line in [
        "ctxloom deps pull",
        "ctxloom deps upgrade --yes",
        "ctxloom init",
        "ctxloom manage install --engine claude-code --no-pull",
        "ctxloom manage gitignore install",
        "ctxloom materialize --target out --yes",
        "ctxloom doctor --fix",
    ] {
        assert!(is_setup_write(line), "{line}");
        assert!(is_library_write(line), "{line}");
    }
    for line in [
        "ctxloom deps check",
        "ctxloom deps list --format json",
        "ctxloom init prompt",
        "ctxloom manage check",
        "ctxloom doctor",
        "ctxloom bundle list",
        "ctxloom session remove amber-swift-owl --yes",
    ] {
        assert!(!is_setup_write(line), "{line}");
    }
    assert!(!is_library_write(
        "ctxloom session remove amber-swift-owl --yes"
    ));
}
