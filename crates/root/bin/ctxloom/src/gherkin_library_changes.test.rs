//! The steps of step 10a's spec (`cli/library_changes.feature`, and the git
//! rows of init, daemon and trust): the project's and the home's git
//! repositories, changes made outside ctxloom (by hand, or by her own git),
//! and what git then holds. Every step drives the real program: git itself,
//! the files, or the binary.
//!
//! The record a read leaves is read with `ctxloom test library-events`,
//! the test build's reader of the tree's stream, run as any command.

use std::path::{Path, PathBuf};

use cucumber::gherkin::Step;
use cucumber::{given, then, when};

use super::World;
use crate::tests::files;
use crate::tests::gherkin_common::{doc, split, CommonWorld};
use crate::tests::library::reports_as;

/// The file at `rel` in the project, by its `/`-separated steps.
fn in_project(w: &World, rel: &str) -> PathBuf {
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

// ------------------------------------------------------------------ git

#[given("the project's changes are committed")]
#[when("the project's changes are committed")]
fn changes_committed(w: &mut World) {
    let project = &w.common.project;
    project.git(&["add", "-A", ".ctxloom"]);
    project.git(&[
        "commit",
        "-q",
        "--allow-empty",
        "-m",
        "the project's changes",
    ]);
}

/// Her own git, run in the project: `args` split as a shell splits them.
#[given(expr = "I run git {string} in the project")]
#[when(expr = "I run git {string} in the project")]
fn her_git(w: &mut World, args: String) {
    let words = split(&args);
    let words: Vec<&str> = words.iter().map(String::as_str).collect();
    w.common.project.git(&words);
}

/// A real merge or rebase stopped on a conflict in `rel`, or the index lock
/// a concurrent git holds, with `rel` edited meanwhile.
#[given(
    regex = r#"^git in the project is in the middle of (a merge stopped on a conflict|a rebase stopped on a conflict|a commit that holds the index lock) on "([^"]+)"$"#
)]
fn in_the_middle_of(w: &mut World, operation: String, rel: String) {
    let project = &w.common.project;
    let path = in_project(w, &rel);
    let edit = |text: &str, message: &str| {
        files::put(&path, format!("{text}\n"));
        project.git(&["commit", "-qam", message]);
    };
    let stopped = |args: &[&str]| {
        let output = project.git_output(project.dir(), args);
        assert!(
            !output.status.success(),
            "git {args:?} stops on the conflict: {}",
            String::from_utf8_lossy(&output.stdout)
        );
    };
    match operation.as_str() {
        "a merge stopped on a conflict" => {
            project.git(&["checkout", "-qb", "side"]);
            edit("the side's line", "the side");
            project.git(&["checkout", "-q", "main"]);
            edit("main's line", "main");
            stopped(&["merge", "-q", "side"]);
        }
        "a rebase stopped on a conflict" => {
            project.git(&["checkout", "-qb", "side"]);
            edit("the side's line", "the side");
            project.git(&["checkout", "-q", "main"]);
            edit("main's line", "main");
            project.git(&["checkout", "-q", "side"]);
            stopped(&["rebase", "-q", "main"]);
        }
        _ => {
            files::put(&path, "edited while git holds the index\n");
            files::put(project.dir().join(".git").join("index.lock"), "");
        }
    }
}

/// Every later command's `PATH` is the host's without each directory that
/// holds a git (`git`, or `git.exe`); an empty directory of the scenario's
/// own when none is left.
#[given("git is not on the PATH")]
fn no_git(w: &mut World) {
    let path = crate::tests::host_var("PATH").unwrap_or_default();
    let mut kept: Vec<PathBuf> = files::search_path(&path)
        .into_iter()
        .filter(|dir| !files::exists(dir.join("git")) && !files::exists(dir.join("git.exe")))
        .collect();
    if kept.is_empty() {
        let empty = w.common.project.home().join("..").join("no-programs");
        files::mkdirs(&empty);
        kept.push(empty);
    }
    let joined = std::env::join_paths(kept).expect("a PATH");
    w.common
        .set_env("PATH", joined.to_str().expect("utf-8").to_string());
}

// ------------------------------------------------------------------ files by hand

#[given(expr = "the file {string} is changed by hand to:")]
#[when(expr = "the file {string} is changed by hand to:")]
fn changed_by_hand(w: &mut World, step: &Step, rel: String) {
    w.daemon_spec.edited_by_hand = true;
    files::put(in_project(w, &rel), format!("{}\n", doc(step)));
}

/// A plain rename: not git's.
#[when(expr = "the file {string} is moved by hand to {string}")]
fn moved_by_hand(w: &mut World, from: String, to: String) {
    files::rename(in_project(w, &from), in_project(w, &to));
}

#[given(expr = "the home's file {string} has the line {string} added by hand")]
fn home_line_added(w: &mut World, rel: String, line: String) {
    let path = in_home(w, &rel);
    let held = files::read(&path).unwrap_or_else(|e| panic!("{}: {e}", path.display()));
    let joined = if held.is_empty() || held.ends_with('\n') {
        format!("{held}{line}\n")
    } else {
        format!("{held}\n{line}\n")
    };
    files::put(&path, joined);
}

// ------------------------------------------------------------------ the home's git

/// A line added to a project file, by hand (step 10c U13: a config change
/// reaches every live session).
#[when(expr = "the file {string} has the line {string} added by hand")]
fn line_added(w: &mut World, rel: String, line: String) {
    w.daemon_spec.edited_by_hand = true;
    let path = in_project(w, &rel);
    let mut text = files::read(&path).unwrap_or_default();
    if !text.is_empty() && !text.ends_with('\n') {
        text.push('\n');
    }
    text.push_str(&line);
    text.push('\n');
    files::put(&path, &text);
}

/// The home's `.ctxloom` is in no git work tree (the harness's home is
/// none's): asserted, so a host whose temporary directory is in one fails
/// the step rather than the scenario's assertion.
#[given(expr = "the home's {string} is in no git repository yet")]
fn home_in_no_repository(w: &mut World, rel: String) {
    let dir = in_home(w, &rel);
    files::mkdirs(&dir);
    let inside = w
        .common
        .project
        .git_output(&dir, &["rev-parse", "--is-inside-work-tree"]);
    assert!(
        !inside.status.success(),
        "{} is in a git work tree already",
        dir.display()
    );
}

#[given("the home directory is a git repository of its own")]
fn home_is_a_repository(w: &mut World) {
    let home = w.common.project.home().to_path_buf();
    w.common.project.git_in(&home, &["init", "-q"]);
}

#[given(expr = "the home's {string} is already a git repository with the commit {string}")]
fn home_dir_is_a_repository(w: &mut World, rel: String, message: String) {
    let dir = in_home(w, &rel);
    files::mkdirs(&dir);
    let project = &w.common.project;
    project.git_in(&dir, &["init", "-q"]);
    project.git_in(&dir, &["commit", "-q", "--allow-empty", "-m", &message]);
}

/// Whether `dir` is the root of a repository of its own: its own `.git`.
fn own_repository(dir: &Path) -> bool {
    files::exists(dir.join(".git"))
}

#[then(expr = "the home's {string} is a git repository with no commit")]
fn home_repository_without_commit(w: &mut World, rel: String) {
    let dir = in_home(w, &rel);
    assert!(
        own_repository(&dir),
        "{} is no repository of its own",
        dir.display()
    );
    let head = w
        .common
        .project
        .git_output(&dir, &["rev-parse", "--verify", "-q", "HEAD"]);
    assert!(
        !head.status.success(),
        "{} has a commit: {}",
        dir.display(),
        String::from_utf8_lossy(&head.stdout)
    );
}

#[then(expr = "the home's {string} is not a git repository of its own")]
fn home_not_own_repository(w: &mut World, rel: String) {
    let dir = in_home(w, &rel);
    assert!(
        !own_repository(&dir),
        "{} is a repository of its own",
        dir.display()
    );
}

#[then(expr = "the home's {string} repository's last commit is still {string}")]
fn home_last_commit(w: &mut World, rel: String, message: String) {
    let dir = in_home(w, &rel);
    let last = w.common.project.git_in(&dir, &["log", "-1", "--format=%s"]);
    assert_eq!(last.trim(), message, "the last commit of {}", dir.display());
}

#[then(expr = "the home's file {string} is exactly:")]
fn home_file_exactly(w: &mut World, step: &Step, rel: String) {
    let path = in_home(w, &rel);
    let held = files::read(&path).unwrap_or_else(|e| panic!("{}: {e}", path.display()));
    assert_eq!(held.trim_end(), doc(step), "{}", path.display());
}

#[then(expr = "the home's file {string} contains {string}")]
fn home_file_contains(w: &mut World, rel: String, text: String) {
    let path = in_home(w, &rel);
    let held = files::read(&path).unwrap_or_else(|e| panic!("{}: {e}", path.display()));
    assert!(
        held.contains(&text),
        "{} should contain {text:?}: {held:?}",
        path.display()
    );
}

#[then(expr = "the home's file {string} does not exist")]
fn home_file_absent(w: &mut World, rel: String) {
    let path = in_home(w, &rel);
    assert!(!files::exists(&path), "{} should not exist", path.display());
}

/// `git status --porcelain -uall` in that directory: its untracked files
/// are exactly the table's (one path a row), and nothing else is listed.
#[then(expr = "git in the home's {string} sees exactly these untracked files:")]
fn home_untracked(w: &mut World, step: &Step, rel: String) {
    let dir = in_home(w, &rel);
    let status = w
        .common
        .project
        .git_in(&dir, &["status", "--porcelain", "-uall"]);
    let mut seen: Vec<&str> = status.lines().collect();
    seen.sort_unstable();
    let mut expected: Vec<String> = step
        .table
        .as_ref()
        .expect("a table")
        .rows
        .iter()
        .map(|row| format!("?? {}", row[0].trim()))
        .collect();
    expected.sort();
    assert_eq!(seen, expected, "git status in {}", dir.display());
}

// ------------------------------------------------------------------ the project's git

#[then(expr = "the output reports {string} as the project's commit {string}")]
fn reports_commit(w: &mut World, path: String, rev: String) {
    let commit = w.common.project.git(&["rev-parse", &rev]);
    let doc = w.json();
    assert_eq!(reports_as(&doc, &path), commit.trim(), "{path} in {doc}");
}

#[then("the project's git repository has no commit")]
fn no_commit(w: &mut World) {
    let project = &w.common.project;
    let head = project.git_output(project.dir(), &["rev-parse", "--verify", "-q", "HEAD"]);
    assert!(
        !head.status.success(),
        "the project has a commit: {}",
        String::from_utf8_lossy(&head.stdout)
    );
}

#[then(expr = "the file {string} is not the same as {string}")]
fn files_differ(w: &mut World, a: String, b: String) {
    let read = |rel: &str| {
        let path = in_project(w, rel);
        files::read_bytes(&path).unwrap_or_else(|e| panic!("{}: {e}", path.display()))
    };
    assert_ne!(read(&a), read(&b), "{a} and {b} hold the same");
}

// ------------------------------------------------------------------ commands

/// `line`, run from `rel` (relative to the project; no `CTXLOOM_ROOT`, so
/// the project is found by walking up from there).
#[given(expr = "I run {string} in the directory {string}")]
#[when(expr = "I run {string} in the directory {string}")]
fn run_in_directory(w: &mut World, line: String, rel: String) {
    let dir = in_project(w, &rel);
    w.before_line(&line);
    w.before_run();
    w.common.run_lines_at(Some(&dir), &line, "");
}

/// Two processes of `line`, both started before either is waited for; the
/// later steps see the second's outcome, and each must have succeeded.
#[when(expr = "{string} is run twice at once")]
fn twice_at_once(w: &mut World, line: String) {
    w.before_line(&line);
    w.before_run();
    let words = split(&line);
    assert_eq!(words.first().map(String::as_str), Some("ctxloom"), "{line}");
    let args: Vec<&str> = words[1..].iter().map(String::as_str).collect();
    let env: Vec<(&str, &str)> = w
        .common
        .env
        .iter()
        .map(|(k, v)| (k.as_str(), v.as_str()))
        .collect();
    let first = w.common.project.start_with(&env, &args);
    let second = w.common.project.start_with(&env, &args);
    let mut last = None;
    for child in [first, second] {
        let ran = crate::tests::Ran::of(child.wait_with_output().expect("ctxloom finishes"));
        assert_eq!(ran.code, 0, "{line}: {}", ran.said());
        last = Some(ran);
    }
    w.common.line = line;
    w.common.ran = last;
}

/// A failure's typed error: its envelope's code, as `the refusal is` reads
/// a refusal's.
#[then(expr = "the error is {string}")]
fn error_is(w: &mut World, code: String) {
    let envelope = super::adopt::envelope_of(w);
    assert_eq!(envelope["code"], code, "{}", w.ran().said());
}
