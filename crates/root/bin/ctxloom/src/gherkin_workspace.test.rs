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
printf '%s\t%s\n' "$HOME" "$CLAUDE_CONFIG_DIR" >> "$here/homes"
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

// ------------------------------------------------- the session-home outlines
//
// Step 6a (OD-47): where the agent's engine runs from. Her own claude setup
// is a fixed set of files in her home, so whether the run touched it is read
// against what was put there. The agent declares it in its own binding
// (`agents.isolated.session_home`, owner ruling of 2026-11-13, superseding
// 44(b) for this key), and `host` is honoured from the home config alone
// (ruling 46, at either level), so that is where it is written.

/// Alice's own claude setup, in her home: her login, her settings and her
/// memory (`~/.claude`).
const HER_CLAUDE: [(&str, &str); 3] = [
    (
        ".claude/.credentials.json",
        "{\"claudeAiOauth\":{\"accessToken\":\"alice-own-login-not-real\"}}\n", // gitleaks:allow
    ),
    (".claude/settings.json", "{\"model\":\"alice-own-model\"}\n"),
    (".claude/CLAUDE.md", "Alice's own memory.\n"),
];

/// The agent `isolated`, on claude-code; its binding holds `session_home`
/// when it is said.
fn isolated_config(session_home: Option<&str>) -> String {
    let home = session_home.map_or_else(String::new, |home| format!("    session_home: {home}\n"));
    format!(
        "schema_version: 9\nagents:\n  isolated:\n    engine: claude-code\n{home}default_agent: isolated\n"
    )
}

impl World {
    /// Each turn's `HOME` and `CLAUDE_CONFIG_DIR`, in order, as the stub saw
    /// them.
    fn turn_homes(&self) -> Vec<(PathBuf, PathBuf)> {
        files::read(self.workspace_bin().join("homes"))
            .unwrap_or_default()
            .lines()
            .map(|line| {
                let (home, config) = line.split_once('\t').expect("HOME\tCLAUDE_CONFIG_DIR");
                (PathBuf::from(home), PathBuf::from(config))
            })
            .collect()
    }

    /// The one turn's `HOME` and `CLAUDE_CONFIG_DIR`, the run having
    /// succeeded.
    fn one_turn_home(&self) -> (PathBuf, PathBuf) {
        self.ran().succeeds().contains("J002200-NOTE-WRITTEN");
        match self.turn_homes().as_slice() {
            [one] => one.clone(),
            other => panic!("one turn, not {other:?}"),
        }
    }

    /// The codes of the json documents the run said on its error stream.
    fn said_codes(&self) -> Vec<String> {
        serde_json::Deserializer::from_str(&self.ran().stderr)
            .into_iter::<serde_json::Value>()
            .map(|doc| {
                let doc = doc.unwrap_or_else(|e| panic!("{e}: {}", self.ran().said()));
                doc["code"].as_str().unwrap_or_default().to_string()
            })
            .collect()
    }

    /// Her own claude setup is exactly what was put there.
    fn her_claude_untouched(&self) {
        let home = self.common.project.home();
        let mut held = Vec::new();
        collect(&home.join(".claude"), home, &mut held);
        held.sort();
        let mut put: Vec<(String, String)> = HER_CLAUDE
            .iter()
            .map(|(rel, body)| ((*rel).to_string(), (*body).to_string()))
            .collect();
        put.sort();
        assert_eq!(held, put, "her own claude setup changed");
    }
}

/// Every file below `dir`, by its path relative to `top`, with its text.
fn collect(dir: &Path, top: &Path, out: &mut Vec<(String, String)>) {
    for path in files::children(dir) {
        if files::is_dir(&path) {
            collect(&path, top, out);
        } else {
            let rel = path.strip_prefix(top).expect("below the home");
            out.push((
                rel.to_string_lossy().replace('\\', "/"),
                files::read(&path).unwrap_or_else(|e| panic!("{}: {e}", path.display())),
            ));
        }
    }
}

#[given("Alice has a git-backed project")]
fn git_backed_project(w: &mut World) {
    // The Background made it one, with its first commit.
    w.git_backed();
    assert!(files::exists(w.common.project.dir().join(".git")));
}

#[given(regex = r#"^Alice has whatever credentials "([^"]+)" needs to authenticate$"#)]
fn credentials_for(w: &mut World, engine: String) {
    assert_eq!(
        engine, "claude-code",
        "the host lane's engine is claude-code"
    );
    // A child authenticates by its token (the Background's); her own login
    // and setup are in her home, as for anyone who uses claude herself.
    w.common.set_env(
        "CLAUDE_CODE_OAUTH_TOKEN",
        "sk-ant-oat01-acceptance-fixture-not-real".to_string(), // gitleaks:allow
    );
    for (rel, body) in HER_CLAUDE {
        w.common.project.write_home(rel, body);
    }
}

#[given("Alice has trusted the project in her own claude")]
fn trusted_in_her_claude(w: &mut World) {
    let project = resolved(w.common.project.dir())
        .to_string_lossy()
        .into_owned();
    let mut projects = serde_json::Map::new();
    projects.insert(
        project,
        serde_json::json!({ "hasTrustDialogAccepted": true }),
    );
    let state = serde_json::json!({
        "hasCompletedOnboarding": true,
        "projects": projects,
    });
    w.common
        .project
        .write_home(".claude.json", &format!("{state}\n"));
}

#[given(regex = r#"^Alice's agent declares session_home "([^"]+)"$"#)]
fn declares_session_home(w: &mut World, home: String) {
    // `host` is the home config's to say (ruling 46): the agent is bound
    // there (a binding replaces a lower layer's whole, so the project's
    // config binds none).
    if home == "host" {
        w.common
            .project
            .write(".ctxloom/config.yaml", "schema_version: 9\n");
        w.common
            .project
            .write_home(".ctxloom/config.yaml", &isolated_config(Some(&home)));
    } else {
        w.common
            .project
            .write(".ctxloom/config.yaml", &isolated_config(Some(&home)));
    }
}

#[given("Alice's agent declares no session_home")]
fn declares_no_session_home(w: &mut World) {
    w.common
        .project
        .write(".ctxloom/config.yaml", &isolated_config(None));
}

#[then("the run reports no isolation finding")]
fn no_isolation_finding(w: &mut World) {
    let ran = w.ran();
    ran.succeeds().contains("J002200-NOTE-WRITTEN");
    let codes = w.said_codes();
    assert!(
        codes.iter().all(|code| !code.starts_with("isolation-")),
        "{codes:?}"
    );
}

#[then("the agent ran from its own session home")]
fn ran_from_session_home(w: &mut World) {
    let (home, config) = w.one_turn_home();
    let harp = w.json()["harp"]
        .as_str()
        .expect("the run's harp")
        .to_string();
    let expected = resolved(
        &w.common
            .project
            .dir()
            .join(".ctxloom/sessions")
            .join(&harp)
            .join("home"),
    );
    assert_eq!(resolved(&home), expected);
    assert_eq!(resolved(&config), expected);
}

#[then(regex = r#"^Alice's own "([^"]+)" setup was never touched by the run$"#)]
fn her_setup_untouched(w: &mut World, engine: String) {
    assert_eq!(engine, "claude-code");
    w.ran().succeeds();
    w.her_claude_untouched();
    let state = w.common.project.home().join(".claude.json");
    assert!(!files::exists(&state), "{} was written", state.display());
}

#[then("the run tells Alice it is running on her own engine setup, which is unsafe")]
fn says_her_own_setup(w: &mut World) {
    let codes = w.said_codes();
    assert!(
        codes
            .iter()
            .any(|code| code == "library-application-notice-session-home-host"),
        "{codes:?}: {}",
        w.ran().said()
    );
    w.ran()
        .contains("runs in your own home")
        // the agent's own key said it (owner ruling of 2026-11-13)
        .contains("(agents.isolated.session_home: host)")
        .contains("reads and changes your own memory")
        .contains("a relocation, not confinement");
}

#[then("the agent ran with no session home of its own")]
fn ran_with_no_session_home(w: &mut World) {
    let (home, config) = w.one_turn_home();
    let her = resolved(w.common.project.home());
    assert_eq!(resolved(&home), her);
    assert_eq!(resolved(&config), her.join(".claude"));
    // her own setup is what the engine ran on, and ctxloom wrote none of it
    w.her_claude_untouched();
}
