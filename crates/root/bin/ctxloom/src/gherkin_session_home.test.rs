//! agent.feature's `--session-home` Rule (owner rulings of 2026-11-13 and
//! 2026-11-14): the binding written with the ctxloom CLI alone, then the
//! effect read where it lands, out of the spawned engine's own environment,
//! through j002200's recording spy (`gherkin_spy`: the same stub, the same
//! token, the same session-home and notice checks its outlines use).
//!
//! The project binds `developer` on claude-code, as its default agent, and
//! renders no `session_home`: the CLI is its sole writer. A binding replaces
//! a lower layer's whole, so an agent Alice binds `host` in her home config
//! is one the project no longer binds.

use std::path::PathBuf;

use cucumber::{given, then, when};

use super::World;
use crate::tests::deps::git_in;
use crate::tests::files;
use crate::tests::gherkin_common::CommonWorld;
use crate::tests::gherkin_spy::{self as spy, resolved};

/// The agent the Rule's scenarios bind.
const AGENT: &str = "developer";

/// The project's config: `developer` on claude-code, no session_home.
fn config() -> String {
    format!(
        "schema_version: 9\nagents:\n  {AGENT}:\n    engine: claude-code\ndefault_agent: {AGENT}\n"
    )
}

impl World {
    /// Where the spy lives, beside the project and the home.
    fn spy_bin(&self) -> PathBuf {
        self.common
            .project
            .dir()
            .parent()
            .expect("the project has a parent")
            .join("bin")
    }

    /// `ctxloom` with `line`'s words, which must succeed.
    fn declare(&mut self, lines: &str) {
        self.common.run_lines(lines, "");
        self.ran().succeeds();
    }
}

#[given("Alice has a git-backed project")]
fn git_backed_project(w: &mut World) {
    let bin = w.spy_bin();
    spy::install(&mut w.common, &bin);
    let project = &w.common.project;
    project.write(".ctxloom/config.yaml", &config());
    project.write(
        ".ctxloom/.gitignore",
        &library::write::scaffold::nested_gitignore(),
    );
    files::put(project.dir().join("README.md"), "hello\n");
    let dir = project.dir().to_path_buf();
    git_in(&dir, &["init", "-q"]);
    git_in(&dir, &["add", "-A"]);
    git_in(&dir, &["commit", "-q", "-m", "initial"]);
}

#[given(regex = r#"^Alice has whatever credentials "([^"]+)" needs to authenticate$"#)]
fn credentials_for(w: &mut World, engine: String) {
    assert_eq!(engine, "claude-code", "the spy's engine is claude-code");
    // A child authenticates by its token alone: nothing of hers is put in
    // her home, so whether the run made her engine's home is read off it.
    spy::authenticate(&mut w.common);
}

#[given(regex = r#"^Alice declares session_home "([^"]+)" on her agent with the ctxloom CLI$"#)]
fn declares_with_the_cli(w: &mut World, home: String) {
    w.declare(&format!("ctxloom agent edit {AGENT} --session-home {home}"));
}

#[given(
    regex = r#"^Alice declares session_home "([^"]+)" on her agent in her home config with the ctxloom CLI$"#
)]
fn declares_in_her_home_config(w: &mut World, home: String) {
    // The project's binding would replace her home's whole, so the agent
    // moves to her home config: the project's goes, hers is written there.
    w.declare(&format!(
        "ctxloom agent remove {AGENT} --yes\nctxloom agent create {AGENT} --engine claude-code --session-home {home} --home"
    ));
}

#[when(regex = r#"^Alice runs the isolated "([^"]+)" agent under workspace "([^"]+)"$"#)]
fn runs_the_agent(w: &mut World, engine: String, workspace: String) {
    assert_eq!(engine, "claude-code", "the spy's engine is claude-code");
    w.common.run_lines(
        &format!(
            "ctxloom run --one-shot --agent {AGENT} --workspace {workspace} \"write a short note\""
        ),
        "",
    );
}

#[then(
    regex = r#"^the spy "([^"]+)" process's "CLAUDE_CONFIG_DIR" env var points at this session's config-home instance$"#
)]
fn config_dir_is_the_session_home(w: &mut World, engine: String) {
    assert_eq!(engine, "claude-code");
    let (_, config) = spy::one_turn_home(w.ran(), &w.spy_bin());
    assert_eq!(resolved(&config), spy::session_home(&w.common));
}

#[then(
    regex = r#"^the spy "([^"]+)" process's "CLAUDE_CONFIG_DIR" env var points at Alice's own "([^"]+)" home$"#
)]
fn config_dir_is_hers(w: &mut World, spied: String, engine: String) {
    assert_eq!(
        (spied.as_str(), engine.as_str()),
        ("claude-code", "claude-code")
    );
    let (_, config) = spy::one_turn_home(w.ran(), &w.spy_bin());
    let hers = resolved(w.common.project.home()).join(".claude");
    assert_eq!(resolved(&config), resolved(&hers));
}

#[then(regex = r#"^Alice's own "([^"]+)" home directory was never created by the run$"#)]
fn her_home_never_created(w: &mut World, engine: String) {
    assert_eq!(engine, "claude-code");
    w.ran().succeeds().contains(spy::REPLY);
    let home = w.common.project.home();
    for hers in [home.join(".claude"), home.join(".claude.json")] {
        assert!(!files::exists(&hers), "{} was made", hers.display());
    }
}

#[then("the run says it runs in her own home")]
fn says_her_own_home(w: &mut World) {
    w.ran().succeeds().contains(spy::REPLY);
    // the agent's own key said it (owner ruling of 2026-11-13)
    spy::says_her_own_home(w.ran(), &format!("agents.{AGENT}"));
}
