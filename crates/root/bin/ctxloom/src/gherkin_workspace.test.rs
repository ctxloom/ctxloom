//! j002200's workspace axis (`run --workspace`), host lane: the "mock
//! agent" is claude-code, a stub first on `PATH` that records where it was
//! run, writes a note there and replies, so where its work lands is read off
//! the disk.

use std::path::{Path, PathBuf};

use cucumber::{given, then};

use super::World;
use crate::tests::files;
use crate::tests::gherkin_common::CommonWorld;

/// The stub claude: each turn's working directory appended to `cwds`, a
/// note written where it works, and a reply.
const STUB: &str = r#"here="$(dirname "$0")"
pwd -P >> "$here/cwds"
cat > /dev/null
echo "a short note" > note.txt
echo '{"type":"system","subtype":"init","session_id":"0c3f2c1e-7b9a-4d1e-9a51-2f6c1d0e8b7a"}'
echo '{"type":"result","subtype":"success","is_error":false,"result":"J002200-NOTE-WRITTEN","session_id":"0c3f2c1e-7b9a-4d1e-9a51-2f6c1d0e8b7a"}'
"#;

/// The project's config: the agent, on claude-code.
fn config(agent: &str) -> String {
    format!(
        "schema_version: 9\nagents:\n  {agent}:\n    engine: claude-code\ndefault_agent: {agent}\n"
    )
}

impl World {
    fn workspace_bin(&self) -> PathBuf {
        self.adopt_scratch().join("bin")
    }

    /// Each turn's working directory, in order, as the stub saw it.
    fn turn_dirs(&self) -> Vec<PathBuf> {
        files::read(self.workspace_bin().join("cwds"))
            .unwrap_or_default()
            .lines()
            .map(PathBuf::from)
            .collect()
    }

    /// Where ctxloom keeps this project's checkouts: under its home, by the
    /// project's id.
    fn checkouts_root(&self) -> PathBuf {
        let marker = self.common.project.dir().join(".ctxloom/project-id");
        let id = files::read(&marker).unwrap_or_else(|e| panic!("{}: {e}", marker.display()));
        self.common
            .project
            .home()
            .join(".ctxloom/worktrees")
            .join(id.trim())
    }
}

/// A path as the host resolves it (the stub prints `pwd -P`).
fn resolved(path: &Path) -> PathBuf {
    if files::exists(path) {
        files::canonical(path)
    } else {
        path.to_path_buf()
    }
}

#[given(expr = "Alice has a git-backed project with a mock agent {string}")]
fn git_backed_with_mock(w: &mut World, agent: String) {
    let bin = w.workspace_bin();
    files::put_program(bin.join("claude"), STUB);
    let host = crate::tests::host_var("PATH").unwrap_or_default();
    w.common.set_env(
        "PATH",
        format!("{}:{}", bin.display(), host.to_string_lossy()),
    );
    w.common.set_env(
        "CLAUDE_CODE_OAUTH_TOKEN",
        "sk-ant-oat01-acceptance-fixture-not-real".to_string(), // gitleaks:allow
    );
    let project = &w.common.project;
    project.write(".ctxloom/config.yaml", &config(&agent));
    project.write(
        ".ctxloom/.gitignore",
        &library::write::scaffold::nested_gitignore(),
    );
    w.git_backed();
    w.git(project.dir(), &["add", "-A"]);
    w.git(project.dir(), &["commit", "-q", "-m", "ctxloom"]);
}

#[given("Alice's project is not a git repository")]
fn not_git(w: &mut World) {
    let git = w.common.project.dir().join(".git");
    files::remove_tree(&git);
}

#[then("what the agent wrote is in the project")]
fn wrote_in_project(w: &mut World) {
    w.ran().succeeds().contains("J002200-NOTE-WRITTEN");
    let project = resolved(w.common.project.dir());
    assert_eq!(w.turn_dirs(), std::slice::from_ref(&project));
    assert!(files::exists(project.join("note.txt")));
}

#[then("what the agent wrote is in a checkout its session owns, under ctxloom's home")]
fn wrote_in_checkout(w: &mut World) {
    let ran = w.ran();
    ran.succeeds().contains("J002200-NOTE-WRITTEN");
    let harp = w.json()["harp"]
        .as_str()
        .expect("the run's harp")
        .to_string();
    let expected = resolved(&w.checkouts_root().join(&harp).join("mock"));
    assert_eq!(w.turn_dirs(), std::slice::from_ref(&expected));
    // The note is work: the checkout is left at the run's end, and said.
    assert!(files::exists(expected.join("note.txt")));
    assert!(!files::exists(w.common.project.dir().join("note.txt")));
    ran.contains("was left in place");
}

#[then(
    "the two runs' workdirs are distinct, neither is under the project directory, and each is in its own session's directory under ctxloom's home"
)]
fn two_workdirs(w: &mut World) {
    let dirs = w.turn_dirs();
    assert_eq!(dirs.len(), 2, "{dirs:?}");
    assert_ne!(dirs[0], dirs[1]);
    let project = resolved(w.common.project.dir());
    let root = resolved(&w.checkouts_root());
    for dir in &dirs {
        assert!(!dir.starts_with(&project), "{}", dir.display());
        let session = dir.parent().expect("a session's directory");
        assert_eq!(session.parent(), Some(root.as_path()), "{}", dir.display());
    }
    assert_ne!(dirs[0].parent(), dirs[1].parent(), "a session each");
}

#[then("Alice's project shows no change in git")]
fn project_clean(w: &mut World) {
    w.ran().succeeds();
    let status = w.git(w.common.project.dir(), &["status", "--porcelain"]);
    assert_eq!(status.trim(), "", "{status}");
}
