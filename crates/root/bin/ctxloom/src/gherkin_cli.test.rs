//! The steps of the base World's CLI contract features (`features/cli/`)
//! that no journey's module has: `version.feature`'s, and the others the
//! un-pending of step 11 ran (each step names its feature).

use cucumber::{given, then};
use serde_json::Value;

use super::World;
use crate::stamp;
use crate::tests::deps::{tree_bundle, Remotes};

/// What the CLI contract features' steps keep.
#[derive(Default)]
pub(super) struct State {
    /// The git remotes a scenario seeds, made on first use.
    remotes: Option<Remotes>,
}

/// `ctxloom` with `args`, with the scenario's environment, now.
fn run_now(w: &World, args: &[&str]) -> crate::tests::Ran {
    let env: Vec<(&str, &str)> = w
        .common
        .env
        .iter()
        .map(|(k, v)| (k.as_str(), v.as_str()))
        .collect();
    w.common.project.run_with(&env, args)
}

/// version.feature: `ctxloom version <flags>` reports the version
/// `ctxloom --version` does (cobra's `ctxloom version <v>` line, the fixed
/// point), as a whole stamp; with no `--format` or `--format json` as the
/// JSON document's `version` (off a terminal the derived default is json),
/// with `--format text` as the one line.
#[then(expr = "the version reported for {string} agrees with {string}")]
fn version_agrees(w: &mut World, flags: String, fixed: String) {
    let fixed_words: Vec<&str> = fixed.split_whitespace().collect();
    assert_eq!(fixed_words.first(), Some(&"ctxloom"), "{fixed}");
    let flag = run_now(w, &fixed_words[1..]);
    assert_eq!(flag.code, 0, "{}", flag.said());
    let version = flag
        .stdout
        .strip_prefix("ctxloom version ")
        .and_then(|rest| rest.strip_suffix('\n'))
        .unwrap_or_else(|| panic!("not cobra's line: {:?}", flag.stdout))
        .to_string();
    assert!(stamp::valid(&version), "not a version stamp: {version:?}");
    let mut args = vec!["version"];
    args.extend(flags.split_whitespace());
    let asked = run_now(w, &args);
    assert_eq!(asked.code, 0, "{}", asked.said());
    let reported = if flags.contains("text") {
        asked
            .stdout
            .strip_suffix('\n')
            .unwrap_or_else(|| panic!("not one line: {:?}", asked.stdout))
            .to_string()
    } else {
        asked.json()["version"]
            .as_str()
            .unwrap_or_else(|| panic!("no version string: {}", asked.stdout))
            .to_string()
    };
    assert_eq!(reported, version, "`ctxloom {}`", args.join(" "));
}

/// session.feature: a finished session of `backend` whose transcript was
/// never captured (no prompt recorded). The seed's engine is claude-code.
#[given(expr = "a recorded session {string} for backend {string}")]
fn recorded_without_transcript(w: &mut World, harp: String, backend: String) {
    assert_eq!(backend, "claude-code", "the seed records claude-code alone");
    let ran = w.common.project.run(&[
        "test",
        "seed-session",
        &harp,
        "--lock",
        "free",
        "--started-ago",
        "7200",
        "--ended-ago",
        "3600",
    ]);
    assert_eq!(ran.code, 0, "seeding {harp}: {}", ran.said());
}

/// session.feature: stdout is NDJSON, at least one line, each a JSON
/// object.
#[then("every line of stdout is a JSON object")]
fn stdout_is_ndjson(w: &mut World) {
    let stdout = &w.common.ran().stdout;
    assert!(!stdout.is_empty(), "nothing on stdout");
    for line in stdout.lines() {
        let value: Value = serde_json::from_str(line).unwrap_or_else(|e| panic!("{e}: {line:?}"));
        assert!(value.is_object(), "not an object: {line}");
    }
}

/// run.feature: a profile of `bundle` that denies `tool` (`deny_tools`).
#[given(expr = "a profile {string} with bundle {string} and deny_tools {string}")]
fn profile_denying(w: &mut World, name: String, bundle: String, tool: String) {
    w.common.project.profile(&name, &bundle);
    w.common.project.write(
        &format!(".ctxloom/content/bundles/v2/project/profiles/{name}.yaml"),
        &format!(
            "description: acceptance fixture profile\nbundles:\n  - {bundle}\ndeny_tools:\n  - {tool}\n"
        ),
    );
}

/// run.feature: a session cannot be recorded where its run would mint it:
/// the project's sessions directory, `.ctxloom/sessions`, is a file. (The
/// one store itself unopenable is no daemon at all, a fault of its own.)
#[given("the session store cannot be opened")]
fn store_unopenable(w: &mut World) {
    w.common
        .project
        .write(".ctxloom/sessions", "not a directory\n");
}

/// run.feature: the run's session, as `session show` gives it, names no
/// MCP endpoint (a one-shot serves its hooks' listener and no MCP server).
#[then("the run's session record carries no MCP endpoint")]
fn session_has_no_mcp(w: &mut World) {
    let harp = w.common.json()["harp"]
        .as_str()
        .unwrap_or_else(|| panic!("the reply names its session: {}", w.common.ran().said()))
        .to_string();
    let shown = run_now(w, &["session", "show", &harp, "--format", "json"]);
    assert_eq!(shown.code, 0, "{}", shown.said());
    let text = shown.stdout.to_lowercase();
    assert!(
        !text.contains("mcp"),
        "the session names an MCP endpoint: {text}"
    );
}

/// bundle.feature: Go's `a git remote "<name>" serving a ctxloom bundle`: a
/// bare repository whose `main` publishes the bundle `demo`, registered in
/// the project (deps_feature.test.rs's fixture).
#[given(expr = "a git remote {string} serving a ctxloom bundle")]
fn git_remote(w: &mut World, name: String) {
    let env: Vec<(&str, &str)> = w
        .common
        .env
        .iter()
        .map(|(k, v)| (k.as_str(), v.as_str()))
        .collect();
    w.cli
        .remotes
        .get_or_insert_with(Remotes::new)
        .git_remote(&w.common.project, &env, &name);
}

/// bundle.feature: the remote's default branch holds `file` of `bundle`'s
/// tree, saying `text`.
#[then(expr = "the remote {string} holds file {string} of bundle {string} containing {string}")]
fn remote_holds(w: &mut World, name: String, file: String, bundle: String, text: String) {
    let held = w
        .cli
        .remotes
        .as_ref()
        .expect("a git remote was seeded")
        .remote_file(&name, &format!("{}/{file}", tree_bundle(&bundle)));
    assert!(held.contains(&text), "{bundle}/{file} on {name}: {held}");
}

/// config.feature: Go's inline `profiles:` block, added to the project's
/// config as Go once read it: `profiles.<name>.bundles: [<bundle>]`.
#[given(expr = "a profile {string} is defined inline in config with bundle {string}")]
fn inline_profile(w: &mut World, name: String, bundle: String) {
    let config = w
        .common
        .project
        .read(".ctxloom/config.yaml")
        .unwrap_or_else(|| crate::tests::library::MINIMAL_CONFIG.to_string());
    w.common.project.write(
        ".ctxloom/config.yaml",
        &format!("{config}profiles:\n  {name}:\n    bundles:\n      - {bundle}\n"),
    );
}

/// fault_tolerance.feature: a profile of the project's bundle naming the
/// bundle `does-not-exist`, which nothing holds.
#[given(expr = "a profile {string} referencing a missing bundle")]
fn profile_missing_bundle(w: &mut World, name: String) {
    w.common.project.profile(&name, "does-not-exist");
}
