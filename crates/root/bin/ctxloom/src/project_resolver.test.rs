//! One project resolver (owner rulings 2026-11-13, afternoon): runs, sessions
//! and library reads all belong to the project of the nearest `.ctxloom` above
//! the working directory (the git top only when there is none; `CTXLOOM_ROOT`
//! overrides). A command that must NAME the project mints its id at that
//! resolved root (owner ruling 2026-11-14, revising "only run and init
//! mint", since `.ctxloom/project-id` is gitignored and every fresh clone has
//! none), never in a subfolder; a read that prints no project name writes
//! nothing, and a dry run never mints (owner: "dry run should *not* mint").

use std::path::{Path, PathBuf};
use std::process::Command;

use serde_json::Value;

use super::library::{reports_as, Project, MINIMAL_CONFIG};
use crate::tests::files;

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

// Ruling 2 (owner, 2026-11-14): a fresh project's json read that names it
// mints its id at the ROOT, from a subfolder too, and nowhere else; a
// session listing there names no project and writes nothing (nothing is
// recorded in a project with no id, so it lists none: session.feature's "A
// fresh project has recorded nothing"). A later run is of the same project.
#[test]
fn test_a_fresh_projects_json_read_mints_at_the_root_and_names_it() {
    if !files::runs_scripts() {
        return;
    }
    // In git: a library read outside it is refused (step 10a, G1).
    let project = Project::bare_in_git();
    ctxloom_at(&project, "");
    let engine = Engine::on(&project);
    let env = engine.env(&[]);
    let sub = project.dir().join("src");
    files::mkdirs(&sub);
    let before = project.tree("");

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

    let named = library_names(&project, &sub, &env);
    assert!(named.starts_with("projects/"), "{named}");
    let mut minted = project.tree("");
    assert!(
        minted.remove(".ctxloom/project-id").is_some(),
        "minted at the root: {:?}",
        minted.keys()
    );
    // The read observes the checkout at the root first (step 10a U2, L2).
    assert!(minted.remove(".ctxloom/state/checkout").is_some());
    assert_eq!(minted, before, "the read wrote more than the root's id");
    assert!(
        !files::exists(sub.join(".ctxloom")),
        "never in the subfolder"
    );
    assert_eq!(library_names(&project, project.dir(), &env), named);

    run_from(&project, &engine, &sub, &[]);
    assert_eq!(
        library_names(&project, &sub, &env),
        named,
        "the run's project"
    );
}

// Owner: "dry run should *not* mint". In a fresh project a dry run, json or
// text, writes nothing: not the project's id, not anything else.
#[test]
fn test_a_dry_run_in_a_fresh_project_writes_nothing() {
    let project = Project::bare();
    ctxloom_at(&project, "");
    let before = project.tree("");
    for format in ["json", "text"] {
        let ran = project.run_in(
            project.dir(),
            &[
                "run",
                "--dry-run",
                "--profile",
                "dev",
                "--format",
                format,
                "hello",
            ],
        );
        assert!(ran.code == 0, "{format}: {}", ran.said());
        assert_eq!(project.tree(""), before, "a {format} dry run wrote");
        assert!(!project.exists(".ctxloom/project-id"), "{format}");
    }
}

// Owner rulings 2026-11-14: a json dry run asks the daemon for its names
// ("dry run *absolutely* calls (and spawns, if necessary) a daemon"), but
// never mints: in a project nothing has named, its `Run.name` prints as null
// and its references keep their configured text; once the project is named,
// its run is named under it and its agent, engine and profiles are resource
// names.
#[test]
fn test_a_json_dry_run_is_named_once_its_project_is_and_null_before() {
    // In git: a library read outside it is refused (step 10a, G1).
    let project = Project::bare_in_git();
    ctxloom_at(&project, "");
    project.write(
        ".ctxloom/config.yaml",
        "schema_version: 9\nagents:\n  dev:\n    engine: claude-code\n    profiles: [dev]\ndefault_agent: dev\n",
    );
    let dry = [
        "run",
        "--dry-run",
        "--agent",
        "dev",
        "--format",
        "json",
        "hello",
    ];
    let unnamed = project.run_in(project.dir(), &dry);
    assert_eq!(unnamed.code, 0, "{}", unnamed.said());
    let run = unnamed.json();
    let name = run.as_object().and_then(|run| run.get("name"));
    assert_eq!(name, Some(&Value::Null), "{run}");
    assert_eq!(run["agent"], "dev", "{run}");
    assert_eq!(run["engine"], "claude-code", "{run}");
    assert_eq!(run["profiles"], serde_json::json!(["dev"]), "{run}");
    assert!(!project.exists(".ctxloom/project-id"), "a dry run minted");
    let named = library_names(&project, project.dir(), &[]);
    let run = project.run_in(project.dir(), &dry).json();
    assert_eq!(run["name"], format!("{named}/runs/-"), "{run}");
    assert_eq!(run["agent"], format!("{named}/agents/dev"), "{run}");
    assert_eq!(
        run["engine"],
        format!("{named}/engines/claude-code"),
        "{run}"
    );
    assert_eq!(
        run["profiles"],
        serde_json::json!([format!("{named}/bundles/project/profiles/dev")]),
        "{run}"
    );
}

/// The discovery file a daemon of `project`'s home advertises itself in.
fn daemon_discovery(project: &Project) -> PathBuf {
    project.home().join(".ctxloom/daemon/daemon.discovery")
}

/// A daemon a test's command starts idles out soon after.
const IDLE_SOON: (&str, &str) = ("CTXLOOM_DAEMON_IDLE_SECONDS", "15");

// Owner ruling 2026-11-14: EVERY dry run calls the daemon, spawning it if
// needed, text or json alike. With none serving, a text dry run starts one.
#[test]
fn test_a_text_dry_run_with_no_daemon_starts_one() {
    // In git: a dry run is a library read, refused outside it (step 10a, G1).
    let project = Project::bare_in_git();
    ctxloom_at(&project, "");
    let project = project.named();
    assert!(!files::exists(daemon_discovery(&project)), "a fresh home");
    let ran = project.run_in_with(
        project.dir(),
        &[IDLE_SOON],
        &[
            "run",
            "--dry-run",
            "--profile",
            "dev",
            "--format",
            "text",
            "hello",
        ],
    );
    assert!(ran.code == 0, "{}", ran.said());
    assert!(
        files::exists(daemon_discovery(&project)),
        "a text dry run started no daemon: {}",
        ran.said()
    );
}

// Owner ruling 2026-11-14: a dry run in a project nothing has named calls
// the daemon too (it has nothing to resolve, so it is only reached), and
// still mints nothing: the project's tree is as it was.
#[test]
fn test_an_unnamed_projects_dry_run_starts_a_daemon_and_writes_nothing() {
    for format in ["json", "text"] {
        let project = Project::bare();
        ctxloom_at(&project, "");
        let before = project.tree("");
        let ran = project.run_in_with(
            project.dir(),
            &[IDLE_SOON],
            &[
                "run",
                "--dry-run",
                "--profile",
                "dev",
                "--format",
                format,
                "hello",
            ],
        );
        assert!(ran.code == 0, "{format}: {}", ran.said());
        assert!(
            files::exists(daemon_discovery(&project)),
            "a {format} dry run started no daemon: {}",
            ran.said()
        );
        assert_eq!(project.tree(""), before, "a {format} dry run wrote");
        assert!(!project.exists(".ctxloom/project-id"), "{format}");
    }
}

// A dry run whose daemon cannot be started fails as every daemon client's
// command does: exit 32, the client's error naming the daemon's log. A
// file where the daemon's directory goes: no daemon can start there.
#[test]
fn test_a_dry_run_whose_daemon_cannot_start_fails_with_the_clients_error() {
    for named in [true, false] {
        for format in ["json", "text"] {
            let project = Project::bare();
            ctxloom_at(&project, "");
            let project = if named { project.named() } else { project };
            project.write_home(".ctxloom/daemon", "not a directory");
            let ran = project.run_in(
                project.dir(),
                &[
                    "run",
                    "--dry-run",
                    "--profile",
                    "dev",
                    "--format",
                    format,
                    "hello",
                ],
            );
            assert_eq!(ran.code, 32, "named {named}, {format}: {}", ran.said());
            assert!(
                ran.said().contains("no ctxloom daemon could be started"),
                "named {named}, {format}: {}",
                ran.said()
            );
            // The log's path as the platform separates it (`\` is one on
            // Windows too).
            ran.contains_as_platform("~/.ctxloom/daemon/daemon.log");
        }
    }
}

// Ruling 2: `init` mints the project's id at its root, after which a json
// read names the project.
#[test]
fn test_init_names_the_project_a_read_then_names() {
    let project = Project::configured();
    let init = project.run_in(project.dir(), &["init", "--format", "json"]);
    assert_eq!(init.code, 0, "init: {}", init.said());
    assert!(project.exists(".ctxloom/project-id"), "init minted it");
    let id = project.read(".ctxloom/project-id");
    let named = library_names(&project, project.dir(), &[]);
    assert!(named.starts_with("projects/"), "{named}");
    assert_eq!(
        project.read(".ctxloom/project-id"),
        id,
        "the read kept init's id"
    );
}

// Owner ruling 2026-11-19 (batch 3, amending the smoke fix): a repository
// deleted and made again at the same path (another root commit), with no
// `.ctxloom` of its own, is initialized and RE-BOUND: the root is bound to
// the id the daemon names, replacing the home index's old binding, and the
// root's next command works. The old project's rows stay under the old id.
// DRAFT (spec text under the owner's standing ruling).
#[test]
fn test_an_init_where_another_repository_was_registered_rebinds_the_root() {
    let project = Project::bare_in_git();
    let init = project.run_in(project.dir(), &["init", "--format", "json"]);
    assert_eq!(init.code, 0, "init: {}", init.said());
    let old = project
        .read(".ctxloom/project-id")
        .expect("the id")
        .trim()
        .to_string();
    // A read that registers the project, with its root commit.
    let listed = project.run_in(project.dir(), &["session", "list", "--format", "json"]);
    assert_eq!(listed.code, 0, "session list: {}", listed.said());
    // The repository deleted and made again: another root commit.
    files::remove_tree(project.dir().join(".git"));
    files::remove_tree(project.dir().join(".ctxloom"));
    project.git(&["init", "-q"]);
    project.git(&["commit", "-q", "--allow-empty", "-m", "another repository"]);
    let again = project.run_in(project.dir(), &["init", "--format", "json"]);
    assert_eq!(again.code, 0, "re-bound: {}", again.said());
    let new = project
        .read(".ctxloom/project-id")
        .expect("the id")
        .trim()
        .to_string();
    assert_ne!(new, old, "the root is bound to the id the daemon named");
    let index = project
        .read_home(".ctxloom/projects/index.yaml")
        .expect("the home's index");
    let root = project.dir().display().to_string();
    assert_eq!(
        index.matches(&format!("path: {root}")).count(),
        1,
        "one binding for the root, the old one replaced: {index}"
    );
    assert!(index.contains(&new), "{index}");
    assert!(!index.contains(&old), "{index}");
    let listed = project.run_in(project.dir(), &["session", "list", "--format", "json"]);
    assert_eq!(listed.code, 0, "and it is usable: {}", listed.said());
}

// Owner ruling 2026-11-19 (init answers one message): a `.ctxloom` that is
// there and has no project named yet (a fresh clone's: no `project-id`, no
// binding in the home's index) still has its `--remote` registered by the
// apply: the daemon names the project for it, the answer carries the id, and
// the root is bound to it, so the next command works.
#[test]
fn test_an_init_remote_on_an_unnamed_ctxloom_names_the_project_and_registers_it() {
    let project = Project::bare_in_git();
    let init = project.run_in(project.dir(), &["init", "--format", "json"]);
    assert_eq!(init.code, 0, "init: {}", init.said());
    // As a clone has it: the `.ctxloom` tracked, no id, nothing bound.
    files::remove(project.dir().join(".ctxloom/project-id"));
    project.write_home(".ctxloom/projects/index.yaml", "projects: []\n");
    let again = project.run_in(
        project.dir(),
        &[
            "init",
            "--remote",
            "file:///nowhere/acceptance-remote.git",
            "--format",
            "json",
        ],
    );
    assert_eq!(again.code, 0, "init: {}", again.said());
    let said = again.json();
    assert_eq!(said["existed"], true, "{said}");
    assert_eq!(said["remotes"][0]["remote_id"], "personal", "{said}");
    assert_eq!(said["remotes"][0]["registered"], true, "{said}");
    let id = said["project_id"].as_str().unwrap_or_default().to_string();
    assert!(!id.is_empty(), "the daemon named the project: {said}");
    assert_eq!(
        project.read(".ctxloom/project-id").expect("bound").trim(),
        id,
        "the root is bound to the id the answer carries"
    );
    let remotes = project.read(".ctxloom/remotes.yaml").expect("remotes.yaml");
    assert!(remotes.contains("acceptance-remote"), "{remotes}");
    let listed = project.run_in(project.dir(), &["session", "list", "--format", "json"]);
    assert_eq!(listed.code, 0, "and it is usable: {}", listed.said());
}

// A root that HAS a `.ctxloom` of its own naming a project that another
// repository's root commit registered is a genuine identity conflict: still
// refused, exit 48, nothing changed.
#[test]
fn test_a_command_where_the_roots_ctxloom_names_another_repositorys_project_is_refused() {
    let project = Project::bare_in_git();
    let init = project.run_in(project.dir(), &["init", "--format", "json"]);
    assert_eq!(init.code, 0, "init: {}", init.said());
    let listed = project.run_in(project.dir(), &["session", "list", "--format", "json"]);
    assert_eq!(listed.code, 0, "session list: {}", listed.said());
    // The repository made again, its `.ctxloom` (project-id too) kept.
    files::remove_tree(project.dir().join(".git"));
    project.git(&["init", "-q"]);
    project.git(&["commit", "-q", "--allow-empty", "-m", "another repository"]);
    let again = project.run_in(project.dir(), &["session", "list", "--format", "json"]);
    assert_eq!(again.code, 48, "refused: {}", again.said());
    again.contains("sessions-project-identity-changed");
}
