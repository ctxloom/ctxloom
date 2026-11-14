//! One project resolver (owner rulings 2026-11-13, afternoon): runs, sessions
//! and library reads all belong to the project of the nearest `.ctxloom` above
//! the working directory (the git top only when there is none; `CTXLOOM_ROOT`
//! overrides), and a read never mints a project id: only `run` and `init` do,
//! at the resolved root. A read in a project that has `.ctxloom/` and no id is
//! refused (`ctxloom-wiring-project-unnamed`, exit 20), its fix naming `ctxloom
//! init`, and writes nothing.

use std::path::{Path, PathBuf};
use std::process::Command;

use serde_json::Value;

use super::library::{reports_as, Project, MINIMAL_CONFIG};
use super::Ran;
use crate::tests::files;

/// The refusal of a read in a project no run or init has named.
const UNNAMED: &str = "ctxloom-wiring-project-unnamed";

/// A child that answers its turn and fires no hook.
const CHILD: &str = r#"cat > /dev/null
echo '{"type":"system","subtype":"init","session_id":"0c1d6a52-7f61-4f0e-9b43-5d1c2a7e8f90"}'
echo '{"type":"result","subtype":"success","is_error":false,"result":"RESOLVER-REPLY","session_id":"0c1d6a52-7f61-4f0e-9b43-5d1c2a7e8f90"}'
"#;

/// A ctxloom project at `prefix` (`""` is the project's top, else `sub/`):
/// its config, a bundle and a profile `dev` a one-shot can run.
fn ctxloom_at(project: &Project, prefix: &str) {
    let at = |rel: &str| format!("{prefix}.ctxloom/{rel}");
    project.write(&at("config.yaml"), MINIMAL_CONFIG);
    project.write(
        &at("content/bundles/v2/demo/bundle.yaml"),
        "schema_version: 4\nversion: 1.0.0\ndescription: resolver fixture\n",
    );
    project.write(
        &at("content/bundles/v2/demo/fragments/example.md"),
        "---\nno_distill: true\n---\nResolver fixture.",
    );
    project.write(
        &at("content/bundles/v2/project/bundle.yaml"),
        "schema_version: 4\nversion: 1.0.0\n",
    );
    project.write(
        &at("content/bundles/v2/project/profiles/dev.yaml"),
        "description: resolver fixture\nbundles:\n  - demo\n",
    );
}

/// The project's top a git repository, as `git init` makes one.
fn git_init(dir: &Path) {
    let output = Command::new("git")
        .args(["init", "-q"])
        .current_dir(dir)
        .env("GIT_CONFIG_NOSYSTEM", "1")
        .output()
        .expect("git runs");
    assert!(output.status.success(), "git init: {output:?}");
}

/// The home config, and a fake `claude` on the PATH: the environment a
/// one-shot runs in.
struct Engine {
    path: String,
}

impl Engine {
    fn on(project: &Project) -> Engine {
        project.write_home(
            ".ctxloom/config.yaml",
            "schema_version: 9\neditor:\n  command: \"true\"\n",
        );
        let bin = project.home().join("bin");
        files::put_program(bin.join("claude"), CHILD);
        let path = match crate::tests::host_var("PATH") {
            Some(host) => format!("{}:{}", bin.display(), host.to_string_lossy()),
            None => bin.display().to_string(),
        };
        Engine { path }
    }

    fn env<'a>(&'a self, more: &[(&'a str, &'a str)]) -> Vec<(&'a str, &'a str)> {
        let mut env = vec![
            ("PATH", self.path.as_str()),
            ("CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat01-fixture-not-real"), // gitleaks:allow
        ];
        env.extend_from_slice(more);
        env
    }
}

/// A one-shot from `dir`, with `more` in its environment; it succeeds.
fn run_from(project: &Project, engine: &Engine, dir: &Path, more: &[(&str, &str)]) {
    let ran = project.run_in_with(
        dir,
        &engine.env(more),
        &[
            "run",
            "--profile",
            "dev",
            "--one-shot",
            "hello",
            "--format",
            "json",
        ],
    );
    assert_eq!(ran.code, 0, "run from {}: {}", dir.display(), ran.said());
    assert!(ran.stdout.contains("RESOLVER-REPLY"), "{}", ran.said());
}

/// The project a library json read from `dir` names (`projects/<harp>`).
fn library_names(project: &Project, dir: &Path, env: &[(&str, &str)]) -> String {
    let ran = project.run_in_with(dir, env, &["config", "show", "--format", "json"]);
    assert_eq!(ran.code, 0, "config show: {}", ran.said());
    let name = reports_as(&ran.json(), "name");
    name.strip_suffix("/config")
        .unwrap_or_else(|| panic!("{name} is no config's name"))
        .to_string()
}

/// The projects the sessions a listing from `dir` gives are named under.
fn sessions_name(project: &Project, dir: &Path, env: &[(&str, &str)]) -> Vec<String> {
    let ran = project.run_in_with(dir, env, &["session", "list", "--format", "json"]);
    assert_eq!(ran.code, 0, "session list: {}", ran.said());
    ran.json()
        .get("sessions")
        .and_then(Value::as_array)
        .cloned()
        .unwrap_or_default()
        .iter()
        .map(|row| {
            let name = row["name"].as_str().expect("a resource name");
            name.split("/sessions/")
                .next()
                .expect("a project's session")
                .to_string()
        })
        .collect()
}

/// The error envelope a json command said on stderr.
fn envelope(ran: &Ran) -> Value {
    serde_json::Deserializer::from_str(&ran.stderr)
        .into_iter::<Value>()
        .map_while(Result::ok)
        .find(|v| v.get("code").is_some() && v.get("message").is_some())
        .unwrap_or_else(|| panic!("an envelope on stderr: {}", ran.said()))
}

/// A monorepo: a git top with its own `.ctxloom`, a subfolder `app` with
/// one of its own, and a subfolder `lib` with none.
fn monorepo() -> (Project, Engine, PathBuf, PathBuf) {
    let project = Project::bare();
    git_init(project.dir());
    ctxloom_at(&project, "");
    ctxloom_at(&project, "app/");
    let lib = project.dir().join("lib");
    files::mkdirs(&lib);
    let engine = Engine::on(&project);
    let app = project.dir().join("app");
    (project, engine, app, lib)
}

// Ruling 1: from a subfolder with a `.ctxloom` of its own, a run mints and
// records there, and a session listing and a library json read from it name
// that subfolder's project; from a subfolder with none, all three name the
// top's.
#[test]
fn test_runs_sessions_and_library_reads_name_the_nearest_ctxloom_project() {
    if !files::runs_scripts() {
        return;
    }
    let (project, engine, app, lib) = monorepo();
    let env = engine.env(&[]);

    run_from(&project, &engine, &app, &[]);
    assert!(
        project.exists("app/.ctxloom/project-id"),
        "the run minted at the subfolder's project: {:?}",
        project.tree("").keys()
    );
    assert!(
        !project.exists(".ctxloom/project-id"),
        "the top got the subfolder's run"
    );
    let app_project = library_names(&project, &app, &env);
    assert_eq!(
        sessions_name(&project, &app, &env),
        std::slice::from_ref(&app_project)
    );

    run_from(&project, &engine, &lib, &[]);
    assert!(
        project.exists(".ctxloom/project-id"),
        "a run from a folder with no .ctxloom minted at the top"
    );
    assert!(
        !files::exists(lib.join(".ctxloom")),
        "the folder with no .ctxloom got one"
    );
    let top_project = library_names(&project, &lib, &env);
    assert_ne!(top_project, app_project);
    assert_eq!(library_names(&project, project.dir(), &env), top_project);
    assert_eq!(
        sessions_name(&project, &lib, &env),
        std::slice::from_ref(&top_project)
    );
    assert_eq!(sessions_name(&project, &app, &env), [app_project]);
}

// Ruling 1: `CTXLOOM_ROOT` still overrides, for the run, the sessions and the
// library reads alike, from a subfolder that has a `.ctxloom` of its own.
#[test]
fn test_ctxloom_root_overrides_the_nearest_ctxloom_for_all_three() {
    if !files::runs_scripts() {
        return;
    }
    let (project, engine, app, _) = monorepo();
    let top = project.dir().to_str().expect("utf-8").to_string();
    let root = [("CTXLOOM_ROOT", top.as_str())];
    let env = engine.env(&root);

    run_from(&project, &engine, &app, &root);
    assert!(project.exists(".ctxloom/project-id"), "minted at the root");
    assert!(
        !project.exists("app/.ctxloom/project-id"),
        "the subfolder got the root's run"
    );
    let named = library_names(&project, &app, &env);
    assert_eq!(library_names(&project, project.dir(), &env), named);
    assert_eq!(sessions_name(&project, &app, &env), [named]);
}

// Ruling 2: a json read in a project with `.ctxloom/` and no project id is
// refused, its fix naming `ctxloom init`, and writes nothing. A session
// listing there mints nothing either: nothing can be recorded in a project
// with no id, so it lists none (session.feature's "A fresh project has
// recorded nothing"). A run mints the id at the root, after which the read
// names the project.
#[test]
fn test_a_read_in_a_project_no_run_has_named_is_refused_and_writes_nothing() {
    if !files::runs_scripts() {
        return;
    }
    let project = Project::bare();
    ctxloom_at(&project, "");
    let engine = Engine::on(&project);
    let env = engine.env(&[]);
    let sub = project.dir().join("src");
    files::mkdirs(&sub);
    let before = project.tree("");

    for (dir, args) in [
        (project.dir(), &["config", "show", "--format", "json"][..]),
        (sub.as_path(), &["config", "show", "--format", "json"]),
    ] {
        let ran = project.run_in_with(dir, &env, args);
        assert_eq!(
            ran.code,
            20,
            "{args:?} from {}: {}",
            dir.display(),
            ran.said()
        );
        let said = envelope(&ran);
        assert_eq!(said["code"], UNNAMED, "{said}");
        let fix = said["fix"].as_str().unwrap_or_default();
        assert!(fix.contains("ctxloom init"), "{said}");
        assert_eq!(project.tree(""), before, "{args:?} wrote in the project");
        assert!(!files::exists(sub.join(".ctxloom")));
    }

    let listed = project.run_in_with(
        project.dir(),
        &env,
        &["session", "list", "--format", "json"],
    );
    assert_eq!(listed.code, 0, "{}", listed.said());
    assert_eq!(listed.json(), serde_json::json!({}), "{}", listed.said());
    assert_eq!(
        project.tree(""),
        before,
        "a session listing wrote in the project"
    );

    run_from(&project, &engine, &sub, &[]);
    assert!(project.exists(".ctxloom/project-id"), "minted at the root");
    assert!(
        !files::exists(sub.join(".ctxloom")),
        "never in the subfolder"
    );
    let named = library_names(&project, &sub, &env);
    assert!(named.starts_with("projects/"), "{named}");
    assert_eq!(library_names(&project, project.dir(), &env), named);
}

// Ruling 2: `init` mints the id the refusal's fix names it for, at the
// project's root, after which a json read names the project.
#[test]
fn test_init_names_the_project_a_read_then_names() {
    let project = Project::configured();
    let ran = project.run_in(project.dir(), &["config", "show", "--format", "json"]);
    assert_eq!(ran.code, 20, "{}", ran.said());
    let init = project.run_in(project.dir(), &["init", "--format", "json"]);
    assert_eq!(init.code, 0, "init: {}", init.said());
    assert!(project.exists(".ctxloom/project-id"), "init minted it");
    let named = library_names(&project, project.dir(), &[]);
    assert!(named.starts_with("projects/"), "{named}");
}
