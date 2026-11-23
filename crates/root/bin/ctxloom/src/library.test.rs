//! The library read side's acceptance harness (S1): a project and a home on
//! disk, and the real `ctxloom` binary run in them.
//!
//! A project is built the way Go's acceptance steps build one: the files
//! `ctxloom bundle create`, `fragment create` and `profile create` write
//! (`steps_fixture.go`: "an initialized ctxloom project", `a bundle "demo"
//! exists`, `a fragment "testing" in bundle "demo" exists`, `a profile "dev"
//! with bundle "demo"`), put in place directly, because the write side is
//! S2's. Or it is a copy of a fixture under `fixtures/`: a project tree and a
//! home tree that Go ctxloom's own output was captured against.

use std::path::{Path, PathBuf};
use std::process::Command;

use serde_json::Value;

use super::{binary, files, Ran};

/// Go's `minimalConfig` (`steps_fixture.go`): the smallest project config.
pub(crate) const MINIMAL_CONFIG: &str = "schema_version: 9\n";

/// Where a project's id is kept, as `init` and a run write it.
pub(crate) const PROJECT_MARKER: &str = ".ctxloom/project-id";

/// The id an initialized fixture project carries, as `init` would mint one.
pub(crate) const FIXTURE_PROJECT_ID: &str = "initialized-fixture-project";

/// What a fixture project commits: the library's pathspec (step 10, L13),
/// each that the fixture has.
const FIXTURE_LIBRARY: [&str; 4] = [
    ".ctxloom/config.yaml",
    ".ctxloom/remotes.yaml",
    ".ctxloom/lock.yaml",
    ".ctxloom/content",
];

/// The editors Go's acceptance steps pin in the home config
/// (`steps_fixture.go`): `sh -c <script> editor <tmpfile>`, so `$1` is the
/// temporary file the content was written to.
#[derive(Clone, Copy)]
pub(crate) enum Editor {
    /// Appends `EDITED-BY-TEST` to the file ("a marker editor").
    Marker,
    /// Truncates the file ("an editor that empties the file").
    Empties,
    /// Rewrites the `description:` line ("a description-rewriting editor").
    Description,
    /// Rewrites the `command:` line ("a command-rewriting editor").
    Command,
}

impl Editor {
    pub(crate) fn home_config(self) -> &'static str {
        match self {
            Editor::Marker => "schema_version: 9\neditor:\n  command: sh\n  args:\n    - \"-c\"\n    - 'printf \"\\nEDITED-BY-TEST\\n\" >> \"$1\"'\n    - editor\n",
            Editor::Empties => "schema_version: 9\neditor:\n  command: sh\n  args:\n    - \"-c\"\n    - 'printf \"\" > \"$1\"'\n    - editor\n",
            Editor::Description => "schema_version: 9\neditor:\n  command: sh\n  args:\n    - \"-c\"\n    - 'sed \"s/^description:.*/description: EDITED-BY-TEST/\" \"$1\" > \"$1.edited\" && mv \"$1.edited\" \"$1\"'\n    - editor\n",
            Editor::Command => "schema_version: 9\neditor:\n  command: sh\n  args:\n    - \"-c\"\n    - 'sed \"s/^\\( *\\)command:.*/\\1command: EDITED-BY-TEST/\" \"$1\" > \"$1.edited\" && mv \"$1.edited\" \"$1\"'\n    - editor\n",
        }
    }
}

/// Where the fixtures are.
pub(crate) fn fixtures() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("fixtures")
}

/// A project directory and a home directory, both temporary.
pub(crate) struct Project {
    _root: file_system::Rooted,
    project: PathBuf,
    home: PathBuf,
    /// Its runs see the catalog Go had: no built-in bundles.
    gos_catalog: bool,
    /// The binary its runs run, when not the one under test at its own path
    /// (a copy under another name).
    program: Option<PathBuf>,
    /// Directories every run's `PATH` starts with (a companion's program),
    /// unless the run names a `PATH` of its own.
    path_first: std::sync::Mutex<Vec<PathBuf>>,
    /// The processes its `start_*` started, ended when it is dropped if
    /// they still run (see its `Drop`).
    started: std::sync::Mutex<Vec<u32>>,
    /// Its runs stand at a terminal (`CTXLOOM_TEST_TERMINAL`, the binary's
    /// stand-in, as init's terminal scenarios use): the CLI says
    /// `can_launch` there only, so an editor is launched at one alone (the
    /// Caller ruling, kept 2026-11-19).
    terminal: std::sync::atomic::AtomicBool,
}

impl Project {
    /// Go's "an initialized ctxloom project": a config at the current
    /// schema, in the project and (with a no-op editor) in the home, and the
    /// project id `ctxloom init` mints (owner ruling 2026-11-13: only `run`
    /// and `init` mint one, and a read in a project with none is refused).
    pub(crate) fn initialized() -> Project {
        Project::configured().named()
    }

    /// The project with the id `ctxloom init` would mint for it, written
    /// where init writes it (owner ruling 2026-11-13: a json read in a
    /// project with none is refused).
    pub(crate) fn named(self) -> Project {
        self.write(PROJECT_MARKER, &format!("{FIXTURE_PROJECT_ID}\n"));
        self
    }

    /// An initialized project no `init` or run has named: its configs, and
    /// no project id. It is a git repository (step 10, G1: a library lives
    /// in git) whose one commit holds the project's config alone; whatever
    /// a later step writes stays uncommitted, as in her project before she
    /// commits it.
    pub(crate) fn configured() -> Project {
        let project = Project::configured_outside_git();
        project.git_init_committing(&[".ctxloom/config.yaml"]);
        project
    }

    /// [`configured`](Project::configured), in no git repository (step 10,
    /// G1: such a library is refused; a project from before git was
    /// required).
    pub(crate) fn configured_outside_git() -> Project {
        let project = Project::bare();
        project.write_configs();
        project
    }

    /// An initialized project one level below a git repository's root
    /// (`<root>/outer/project`, the repository `<root>/outer`, whose one
    /// commit holds the project's config), the home outside it: in a work
    /// tree whose root is not the project's.
    pub(crate) fn configured_in_a_subdirectory() -> Project {
        let project = Project::bare_in_a_subdirectory();
        project.write_configs();
        project.git_in(
            project.project.parent().expect("a parent"),
            &["add", "project/.ctxloom/config.yaml"],
        );
        project.git_in(
            project.project.parent().expect("a parent"),
            &["commit", "-qm", "initial"],
        );
        project
    }

    /// An empty project directory one level below a git repository's root,
    /// which has one empty commit; the home outside it.
    pub(crate) fn bare_in_a_subdirectory() -> Project {
        let root = files::root();
        let outer = root.root().join("outer");
        let project = outer.join("project");
        let home = root.root().join("home");
        files::mkdirs(&project);
        files::mkdirs(&home);
        let project = Project::placed(root, project, home);
        project.git_in(&outer, &["init", "-q"]);
        project.git_in(&outer, &["commit", "-q", "--allow-empty", "-m", "initial"]);
        project
    }

    /// An empty project directory that is a git repository with one empty
    /// commit: her live repository, before ctxloom.
    pub(crate) fn bare_in_git() -> Project {
        let project = Project::bare();
        project.git(&["init", "-q"]);
        project.git(&["commit", "-q", "--allow-empty", "-m", "initial"]);
        project
    }

    /// The configs an initialized project has, in the project and (with a
    /// no-op editor) in the home.
    fn write_configs(&self) {
        self.write(".ctxloom/config.yaml", MINIMAL_CONFIG);
        self.write_home(
            ".ctxloom/config.yaml",
            "schema_version: 9\neditor:\n  command: \"true\"\n",
        );
    }

    /// `git init` in the project, then one commit of `paths`.
    pub(crate) fn git_init_committing(&self, paths: &[&str]) {
        self.git(&["init", "-q"]);
        let mut add = vec!["add", "--"];
        add.extend_from_slice(paths);
        if !paths.is_empty() {
            self.git(&add);
        }
        self.git(&["commit", "-q", "--allow-empty", "-m", "initial"]);
    }

    /// The harness's git in the project, which must succeed: its stdout.
    pub(crate) fn git(&self, args: &[&str]) -> String {
        self.git_in(&self.project, args)
    }

    /// The harness's git in `dir`, which must succeed: its stdout.
    pub(crate) fn git_in(&self, dir: &Path, args: &[&str]) -> String {
        let output = self.git_output(dir, args);
        assert!(
            output.status.success(),
            "git {args:?} in {}: {}",
            dir.display(),
            String::from_utf8_lossy(&output.stderr)
        );
        String::from_utf8_lossy(&output.stdout).into_owned()
    }

    /// The harness's git in `dir`, whatever it exits: a fixed identity, no
    /// system config, the temporary home as its `HOME`, and nothing else of
    /// this process's environment (no `GIT_*` of the runner's).
    pub(crate) fn git_output(&self, dir: &Path, args: &[&str]) -> std::process::Output {
        Command::new("git")
            .args([
                "-c",
                "user.name=Alice",
                "-c",
                "user.email=alice@example.invalid",
            ])
            .args([
                "-c",
                "init.defaultBranch=main",
                "-c",
                "commit.gpgsign=false",
            ])
            .args(NO_MAINTENANCE)
            .args(args)
            .current_dir(dir)
            .env_clear()
            .env("PATH", crate::tests::host_var("PATH").unwrap_or_default())
            .env("HOME", &self.home)
            .env("USERPROFILE", &self.home)
            .env("TMPDIR", crate::tests::temp_root())
            .env(crate::tests::LIFELINE, crate::tests::lifeline())
            .env("GIT_CONFIG_NOSYSTEM", "1")
            .output()
            .expect("git runs")
    }

    /// Go's "an empty project directory": nothing in it.
    pub(crate) fn bare() -> Project {
        let root = files::root();
        let project = root.root().join("project");
        let home = root.root().join("home");
        files::mkdirs(&project);
        files::mkdirs(&home);
        Project::placed(root, project, home)
    }

    /// The project at `project` and its home at `home`, both under `root`.
    fn placed(root: file_system::Rooted, project: PathBuf, home: PathBuf) -> Project {
        Project {
            _root: root,
            project,
            home,
            gos_catalog: false,
            program: None,
            path_first: std::sync::Mutex::new(Vec::new()),
            started: std::sync::Mutex::new(Vec::new()),
            terminal: std::sync::atomic::AtomicBool::new(false),
        }
    }

    /// Every later run stands at no terminal (the default).
    pub(crate) fn off_a_terminal(&self) {
        self.terminal
            .store(false, std::sync::atomic::Ordering::SeqCst);
    }

    /// Every later run stands at a terminal, as an editor is launched at
    /// one only.
    pub(crate) fn at_a_terminal(&self) {
        self.terminal
            .store(true, std::sync::atomic::Ordering::SeqCst);
    }

    /// Every later run's `PATH` starts with `dir` (one naming its own `PATH`
    /// excepted).
    pub(crate) fn put_on_path(&self, dir: &Path) {
        self.path_first
            .lock()
            .expect("not poisoned")
            .push(dir.to_path_buf());
    }

    /// A copy of `fixtures/<name>/project` and `fixtures/<name>/home`.
    pub(crate) fn from_fixture(name: &str) -> Project {
        let project = Project::bare();
        let source = fixtures().join(name);
        copy_tree(&source.join("project"), &project.project);
        if files::is_dir(source.join("home")) {
            copy_tree(&source.join("home"), &project.home);
        }
        // A fixture's ctxloom project stands for an initialized one, which
        // is in git (step 10, G1), its library committed, and carries the id
        // init mints (owner ruling 2026-11-13).
        if files::is_dir(project.project.join(".ctxloom")) {
            let library: Vec<&str> = FIXTURE_LIBRARY
                .into_iter()
                .filter(|rel| project.exists(rel))
                .collect();
            project.git_init_committing(&library);
            if !project.exists(PROJECT_MARKER) {
                return project.named();
            }
        }
        project
    }

    pub(crate) fn dir(&self) -> &Path {
        &self.project
    }

    /// The path `rel` (`/`-separated) in the project, as ctxloom shows it: a
    /// step at a time, so the host's separator joins each step (`C:\p\a\b`
    /// on Windows, never `C:\p\a/b`).
    pub(crate) fn shown(&self, rel: &str) -> String {
        rel.split('/')
            .fold(self.project.clone(), |path, step| path.join(step))
            .display()
            .to_string()
    }

    /// The project as a Go-ported test holds it: its runs see the catalog Go
    /// had, with no built-in bundle (owner rulings 110, 111 added them; their
    /// own contract is `cli/init.feature`'s), so a listing or a search is
    /// held to the project's own content, whatever a ctxloom release ships.
    /// The binary's `test-support` variable `CTXLOOM_TEST_NO_BUILTINS` does it.
    pub(crate) fn on_gos_catalog(mut self) -> Project {
        self.gos_catalog = true;
        self
    }

    /// Run the binary as `program` from now on (as [`Project::run_by`]).
    pub(crate) fn set_program(&mut self, program: PathBuf) {
        self.program = Some(program);
    }

    /// The project as run by `program` (a copy of the binary under test
    /// under another name) instead of the binary at its own path.
    pub(crate) fn run_by(mut self, program: PathBuf) -> Project {
        self.set_program(program);
        self
    }

    pub(crate) fn home(&self) -> &Path {
        &self.home
    }

    /// Put `content` at `rel` in the project.
    pub(crate) fn write(&self, rel: &str, content: &str) {
        put(&self.project.join(rel), content);
    }

    /// Put `bytes` at `rel` in the project.
    pub(crate) fn write_bytes(&self, rel: &str, bytes: &[u8]) {
        files::put(self.project.join(rel), bytes);
    }

    /// Put `content` at `rel` in the home.
    pub(crate) fn write_home(&self, rel: &str, content: &str) {
        put(&self.home.join(rel), content);
    }

    /// Go's `ctxloom bundle create <name> -d "acceptance fixture bundle"`:
    /// the manifest, an example fragment and an example command.
    pub(crate) fn bundle(&self, name: &str) {
        let root = format!(".ctxloom/content/bundles/v2/{name}");
        self.write(
            &format!("{root}/bundle.yaml"),
            "schema_version: 4\nversion: 1.0.0\ndescription: acceptance fixture bundle\n",
        );
        self.write(
            &format!("{root}/fragments/example.md"),
            "---\ntags:\n  - example\nno_distill: true\n---\n# Example Fragment\n\nAdd your content here.",
        );
        self.write(
            &format!("{root}/prompts/example.md"),
            "---\ndescription: Example prompt\ntags:\n  - example\nno_distill: true\n---\nExample prompt content. Describe what this prompt does.",
        );
    }

    /// Go's `a fragment "<name>" in bundle "<bundle>" exists`: the body is
    /// the marker `FRAGMENT-BODY-<name>` (`fixtureFragmentBody`).
    pub(crate) fn fragment(&self, bundle: &str, name: &str) {
        self.write(
            &format!(".ctxloom/content/bundles/v2/{bundle}/fragments/{name}.md"),
            &format!("FRAGMENT-BODY-{name}"),
        );
    }

    /// Go's `a command "<name>" in bundle "<bundle>" exists`: the body is
    /// the marker `COMMAND-BODY-<name>`.
    pub(crate) fn command(&self, bundle: &str, name: &str) {
        self.write(
            &format!(".ctxloom/content/bundles/v2/{bundle}/prompts/{name}.md"),
            &format!("COMMAND-BODY-{name}"),
        );
    }

    /// Go's `a profile "<name>" with bundle "<bundle>"`: the file
    /// `profile create <name> --include <bundle> -d "acceptance fixture
    /// profile"` writes, in the project bundle.
    pub(crate) fn profile(&self, name: &str, bundle: &str) {
        self.write(
            ".ctxloom/content/bundles/v2/project/bundle.yaml",
            "schema_version: 4\nversion: 1.0.0\n",
        );
        self.write(
            &format!(".ctxloom/content/bundles/v2/project/profiles/{name}.yaml"),
            &format!("description: acceptance fixture profile\nbundles:\n  - {bundle}\n"),
        );
    }

    /// The text of the file at `rel` in the project; none where it is not there.
    pub(crate) fn read(&self, rel: &str) -> Option<String> {
        files::read(self.project.join(rel)).ok()
    }

    /// The bytes of the file at `rel` in the project; none where it is not there.
    pub(crate) fn read_bytes(&self, rel: &str) -> Option<Vec<u8>> {
        files::read_bytes(self.project.join(rel)).ok()
    }

    /// The text of the file at `rel` in the home.
    pub(crate) fn read_home(&self, rel: &str) -> Option<String> {
        files::read(self.home.join(rel)).ok()
    }

    /// Whether anything is at `rel` in the project.
    pub(crate) fn exists(&self, rel: &str) -> bool {
        files::exists(self.project.join(rel))
    }

    /// Go's `the file "<rel>" contains "<text>"`.
    pub(crate) fn file_contains(&self, rel: &str, text: &str) -> &Project {
        let held = self.read(rel).unwrap_or_else(|| panic!("no file {rel}"));
        assert!(
            held.contains(text),
            "{rel} should contain {text:?}: {held:?}"
        );
        self
    }

    /// Go's `the file "<rel>" does not contain "<text>"`.
    pub(crate) fn file_lacks(&self, rel: &str, text: &str) -> &Project {
        let held = self.read(rel).unwrap_or_else(|| panic!("no file {rel}"));
        assert!(
            !held.contains(text),
            "{rel} should not contain {text:?}: {held:?}"
        );
        self
    }

    /// Go's `the file "<rel>" does not exist`.
    pub(crate) fn file_is_absent(&self, rel: &str) -> &Project {
        assert!(!self.exists(rel), "{rel} should not exist");
        self
    }

    /// Every file below `rel` in the project, by its path below `rel` (slash
    /// separated) and its bytes.
    pub(crate) fn tree(&self, rel: &str) -> std::collections::BTreeMap<String, Vec<u8>> {
        tree_of(&self.project.join(rel), |_| {})
    }

    /// Give the file at `rel` the execute bits (unix).
    pub(crate) fn make_executable(&self, rel: &str) {
        files::set_mode(self.project.join(rel), 0o755);
    }

    /// Whether the file at `rel` has an execute bit (unix; false elsewhere).
    pub(crate) fn is_executable(&self, rel: &str) -> bool {
        files::mode_of(self.project.join(rel)).is_some_and(|mode| mode & 0o111 != 0)
    }

    /// Go's "a ctxloom project with a <kind> editor": the project's config
    /// minimal, the home's pinning the editor (`steps_fixture.go`), and the
    /// id init mints.
    pub(crate) fn with_editor(editor: Editor) -> Project {
        let project = Project::bare();
        project.write(".ctxloom/config.yaml", MINIMAL_CONFIG);
        project.write_home(".ctxloom/config.yaml", editor.home_config());
        project.git_init_committing(&[".ctxloom/config.yaml"]);
        project.at_a_terminal();
        project.named()
    }

    /// Run `ctxloom args` in the project, with the project root named by
    /// `CTXLOOM_ROOT`.
    pub(crate) fn run(&self, args: &[&str]) -> Ran {
        self.run_with(&[], args)
    }

    /// As [`run`](Project::run), with these environment variables too.
    pub(crate) fn run_with(&self, env: &[(&str, &str)], args: &[&str]) -> Ran {
        let mut all = vec![("CTXLOOM_ROOT", self.project.to_str().expect("utf-8"))];
        all.extend_from_slice(env);
        self.spawn(&self.project, &all, args)
    }

    /// Run from `dir` with no `CTXLOOM_ROOT`: the project is found by
    /// walking up from the working directory.
    pub(crate) fn run_in(&self, dir: &Path, args: &[&str]) -> Ran {
        self.spawn(dir, &[], args)
    }

    /// As [`run_in`](Project::run_in), with these environment variables too.
    pub(crate) fn run_in_with(&self, dir: &Path, env: &[(&str, &str)], args: &[&str]) -> Ran {
        self.spawn(dir, env, args)
    }

    /// `ctxloom args` on a pseudo-terminal: util-linux's `script` runs it
    /// with a pty for its standard input, output and error (`-e` returns the
    /// child's status), no stand-in set.
    pub(crate) fn run_on_a_pty(&self, args: &[&str]) -> Ran {
        let mut line = vec![binary().to_str().expect("utf-8").to_string()];
        line.extend(args.iter().map(|a| (*a).to_string()));
        let quoted: Vec<String> = line
            .iter()
            .map(|word| format!("'{}'", word.replace('\'', "'\\''")))
            .collect();
        let root = self.project.to_str().expect("utf-8");
        let mut all = vec![("CTXLOOM_ROOT", root)];
        all.extend_from_slice(&[]);
        let command = self.process(&self.project, &all, &[]);
        let mut script = Command::new("script");
        script
            .args(["-qefc", &quoted.join(" "), "/dev/null"])
            .env_clear()
            .current_dir(&self.project)
            .stdin(std::process::Stdio::null());
        for (name, value) in command.get_envs() {
            if let Some(value) = value {
                script.env(name, value);
            }
        }
        Ran::of(script.output().expect("script runs"))
    }

    /// As [`run_with`](Project::run_with), started and not waited for: its
    /// output piped, for the caller to wait for.
    pub(crate) fn start_with(&self, env: &[(&str, &str)], args: &[&str]) -> std::process::Child {
        let mut all = vec![("CTXLOOM_ROOT", self.project.to_str().expect("utf-8"))];
        all.extend_from_slice(env);
        self.process(&self.project, &all, args)
            .stdin(std::process::Stdio::null())
            .stdout(std::process::Stdio::piped())
            .stderr(std::process::Stdio::piped())
            .spawn()
            .map(|child| self.started(child))
            .expect("ctxloom starts")
    }

    /// As [`start_with`](Project::start_with), its stdin a pipe the test
    /// writes keys to.
    pub(crate) fn start_with_input(
        &self,
        env: &[(&str, &str)],
        args: &[&str],
    ) -> std::process::Child {
        let mut all = vec![("CTXLOOM_ROOT", self.project.to_str().expect("utf-8"))];
        all.extend_from_slice(env);
        self.process(&self.project, &all, args)
            .stdin(std::process::Stdio::piped())
            .stdout(std::process::Stdio::piped())
            .stderr(std::process::Stdio::piped())
            .spawn()
            .map(|child| self.started(child))
            .expect("ctxloom starts")
    }

    /// `ctxloom args` on a pseudo-terminal of `(columns, rows)`, run by the
    /// python `helper` (which writes the terminal's output to `out` and its
    /// end to `status`); the child's stdin is the terminal's keys.
    pub(crate) fn start_on_pty(
        &self,
        env: &[(&str, &str)],
        args: &[&str],
        [helper, out, status]: [&Path; 3],
        (columns, rows): (&str, &str),
    ) -> std::process::Child {
        let mut all = vec![("CTXLOOM_ROOT", self.project.to_str().expect("utf-8"))];
        all.extend_from_slice(env);
        let inner = self.process(&self.project, &all, args);
        let mut command = Command::new("python3");
        command
            .args(["-I"])
            .arg(helper)
            .arg(out)
            .arg(status)
            .args([columns, rows])
            .arg(inner.get_program())
            .args(inner.get_args())
            .env_clear()
            .current_dir(&self.project);
        for (name, value) in inner.get_envs() {
            if let Some(value) = value {
                command.env(name, value);
            }
        }
        command
            .stdin(std::process::Stdio::piped())
            .stdout(std::process::Stdio::null())
            .stderr(std::process::Stdio::null())
            .spawn()
            .map(|child| self.started(child))
            .expect("the pty helper starts")
    }

    /// As [`start_with`](Project::start_with), from `dir` with no
    /// `CTXLOOM_ROOT` (the project found by walking up from it).
    pub(crate) fn start_in_with(
        &self,
        dir: &Path,
        env: &[(&str, &str)],
        args: &[&str],
    ) -> std::process::Child {
        self.process(dir, env, args)
            .stdin(std::process::Stdio::null())
            .stdout(std::process::Stdio::piped())
            .stderr(std::process::Stdio::piped())
            .spawn()
            .map(|child| self.started(child))
            .expect("ctxloom starts")
    }

    /// As [`start_with`](Project::start_with), its stdin piped too: a
    /// process the test talks to (ctxloom's MCP server, line by line).
    pub(crate) fn start_talking(&self, env: &[(&str, &str)], args: &[&str]) -> std::process::Child {
        let mut all = vec![("CTXLOOM_ROOT", self.project.to_str().expect("utf-8"))];
        all.extend_from_slice(env);
        self.process(&self.project, &all, args)
            .stdin(std::process::Stdio::piped())
            .stdout(std::process::Stdio::piped())
            .stderr(std::process::Stdio::piped())
            .spawn()
            .map(|child| self.started(child))
            .expect("ctxloom starts")
    }

    /// `child`, its pid kept to be ended when the project is dropped.
    fn started(&self, child: std::process::Child) -> std::process::Child {
        self.started
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner)
            .push(child.id());
        child
    }

    fn spawn(&self, cwd: &Path, env: &[(&str, &str)], args: &[&str]) -> Ran {
        Ran::of(self.process(cwd, env, args).output().expect("ctxloom runs"))
    }

    /// `ctxloom args` from `cwd`, in the project's home, with `env`.
    fn process(&self, cwd: &Path, env: &[(&str, &str)], args: &[&str]) -> Command {
        let mut command = Command::new(self.program.as_deref().unwrap_or(binary()));
        command
            .args(args)
            .env_clear()
            .env("HOME", &self.home)
            .env("USERPROFILE", &self.home)
            .env("TMPDIR", crate::tests::temp_root())
            .env(crate::tests::LIFELINE, crate::tests::lifeline())
            .current_dir(cwd);
        // The user's login passes as it is, so what ctxloom records as her
        // (a trust's actor, step 8 U1) is the account running the test.
        for name in ["PATH", "SystemRoot", "USER", "LOGNAME", "USERNAME"] {
            if let Some(value) = crate::tests::host_var(name) {
                command.env(name, value);
            }
        }
        let first = self.path_first.lock().expect("not poisoned").clone();
        if !first.is_empty() {
            let mut paths = first;
            paths.extend(files::search_path(
                &crate::tests::host_var("PATH").unwrap_or_default(),
            ));
            command.env("PATH", std::env::join_paths(paths).expect("a PATH"));
        }
        if self.gos_catalog {
            command.env("CTXLOOM_TEST_NO_BUILTINS", "1");
        }
        if self.terminal.load(std::sync::atomic::Ordering::SeqCst) {
            command.env("CTXLOOM_TEST_TERMINAL", "1");
        }
        for (name, value) in env {
            command.env(name, value);
        }
        command
    }

    /// What a run printed with the project's and the home's paths written
    /// as `{PROJECT}` and `{HOME}`: the form the goldens are in. A path is
    /// shown in the host's spelling, so on Windows it is found as JSON writes
    /// it too (each `\` doubled), and the rest of a path below either is
    /// spelled with `/`, as Go's goldens (made on unix) spell it.
    pub(crate) fn normalized(&self, text: &str) -> String {
        let mut out = text.to_string();
        for (path, token) in [(&self.project, "{PROJECT}"), (&self.home, "{HOME}")] {
            let path = path.to_str().expect("utf-8");
            out = out
                .replace(&path.replace('\\', "\\\\"), token)
                .replace(path, token);
        }
        if os::host().flavor() == os::Flavor::Windows {
            out = slashed_below(&out, &["{PROJECT}", "{HOME}"]);
        }
        out
    }
}

/// How long a process a test started, still running when its project is
/// dropped, is given to stop when asked, before it is killed.
const STOP_GRACE: std::time::Duration = std::time::Duration::from_secs(10);

impl Drop for Project {
    /// No process a test started outlives it (2026-11-15, U8r): a test that
    /// panics drops its children without ending them, and a frontend left so
    /// keeps its daemon serving, or starts another. Each one still running,
    /// and still this process's child (a pid given up and reused is another
    /// process's), is asked to stop (SIGTERM, on which a run ends its engine
    /// and records its end, ruling 114), given [`STOP_GRACE`], then killed;
    /// all before the home is removed. Where `ps` and `kill` are not
    /// (Windows) nothing is asked.
    fn drop(&mut self) {
        let pids = std::mem::take(
            &mut *self
                .started
                .lock()
                .unwrap_or_else(std::sync::PoisonError::into_inner),
        );
        let running: Vec<u32> = pids.into_iter().filter(|pid| running_child(*pid)).collect();
        if running.is_empty() {
            return;
        }
        for pid in &running {
            signal(*pid, "-TERM");
        }
        let deadline = std::time::Instant::now() + STOP_GRACE;
        while std::time::Instant::now() < deadline && running.iter().any(|pid| running_child(*pid))
        {
            std::thread::sleep(std::time::Duration::from_millis(50));
        }
        for pid in running.iter().filter(|pid| running_child(**pid)) {
            signal(*pid, "-KILL");
        }
    }
}

/// Whether `pid` is a child of this process that has not exited (a zombie,
/// not yet waited for, has).
fn running_child(pid: u32) -> bool {
    let Ok(out) = Command::new("ps")
        .args(["-o", "ppid=,stat=", "-p", &pid.to_string()])
        .output()
    else {
        return false;
    };
    let text = String::from_utf8_lossy(&out.stdout);
    let mut words = text.split_whitespace();
    words.next().and_then(|ppid| ppid.parse::<u32>().ok()) == Some(std::process::id())
        && words.next().is_some_and(|stat| !stat.starts_with('Z'))
}

/// Send `signal` (`-TERM`, `-KILL`) to `pid`, whatever comes of it.
fn signal(pid: u32, signal: &str) {
    let _ = Command::new("kill")
        .args([signal, &pid.to_string()])
        .output();
}

/// `text` with each path that starts at one of `tokens` spelled with `/`: the
/// `\` (or JSON's `\\`) up to the path's end (a quote, a space, a line's end,
/// or `,`, `:`, `;`, `)`, `` ` ``) made `/`.
fn slashed_below(text: &str, tokens: &[&str]) -> String {
    let ends = |c: char| c.is_whitespace() || "\"'`,:;)".contains(c);
    let mut out = String::with_capacity(text.len());
    let mut rest = text;
    while let Some((at, token)) = tokens
        .iter()
        .filter_map(|t| rest.find(t).map(|at| (at, *t)))
        .min()
    {
        let after = at + token.len();
        out.push_str(&rest[..after]);
        let tail = &rest[after..];
        let span = tail.find(ends).unwrap_or(tail.len());
        out.push_str(&tail[..span].replace("\\\\", "/").replace('\\', "/"));
        rest = &tail[span..];
    }
    out.push_str(rest);
    out
}

fn put(path: &Path, content: &str) {
    files::put(path, content);
}

fn copy_tree(from: &Path, to: &Path) {
    files::mkdirs(to);
    for entry in files::children(from) {
        let target = to.join(entry.file_name().expect("a name"));
        if files::is_dir(&entry) {
            copy_tree(&entry, &target);
        } else {
            files::copy(&entry, &target);
        }
    }
}

impl Ran {
    /// Both streams, as Go's "the output contains" reads them.
    ///
    /// Off a terminal an error is a json envelope on the error stream; Go's
    /// text is its `message` (and `fix`), decoded, so the quotes read as the
    /// person reads them.
    pub(crate) fn output(&self) -> String {
        let said = serde_json::from_str::<Value>(&self.stderr)
            .ok()
            .and_then(|doc| {
                let message = doc.get("message")?.as_str()?.to_string();
                let fix = doc.get("fix").and_then(Value::as_str).unwrap_or_default();
                Some(format!("{message}\n{fix}\n"))
            })
            .unwrap_or_else(|| self.stderr.clone());
        format!("{}{}{}", self.stdout, self.stderr, said)
    }

    /// Go's "the command succeeds".
    pub(crate) fn succeeds(&self) -> &Ran {
        assert_eq!(self.code, 0, "expected success: {}", self.said());
        self
    }

    /// Go's "the command fails": a non-zero exit (any of the ladder's).
    pub(crate) fn fails(&self) -> &Ran {
        assert_ne!(self.code, 0, "expected failure: {}", self.said());
        self
    }

    /// Go's `the output contains "<text>"`.
    pub(crate) fn contains(&self, text: &str) -> &Ran {
        assert!(
            self.output().contains(text),
            "expected the output to contain {text:?}: {}",
            self.said()
        );
        self
    }

    /// The feature step `the output contains "<text>"`: [`contains`](Self::contains),
    /// a path in `text` found however the platform separates it. On Windows
    /// `/` and `\` are both the separator, so a spec's `~/.ctxloom/daemon/daemon.log`
    /// is the same path as `~/.ctxloom/daemon\daemon.log` and either is found;
    /// elsewhere `\` is a file name's character and `text` is found exactly
    /// as written.
    pub(crate) fn contains_as_platform(&self, text: &str) -> &Ran {
        let output = self.output();
        let found = output.contains(text)
            || (os::host().flavor() == os::Flavor::Windows
                && output.replace('\\', "/").contains(&text.replace('\\', "/")));
        assert!(
            found,
            "expected the output to contain {text:?}: {}",
            self.said()
        );
        self
    }

    /// Go's `the output does not contain "<text>"`.
    pub(crate) fn lacks(&self, text: &str) -> &Ran {
        assert!(
            !self.output().contains(text),
            "expected the output not to contain {text:?}: {}",
            self.said()
        );
        self
    }
}

/// The value at a path in a JSON document, in the notation of Go's
/// `the output reports "<path>" as ...` (`steps_cli.go`): `$` is the root, a
/// segment is a key, an index (`runs.0` or `runs[0]`), or a selector
/// `[key=value]` that picks the element of an array whose `key` is `value`,
/// or `[key$=suffix]` whose `key` ends with `suffix` (a resource name read
/// without its project, OD-40: `engines[name$=/engines/claude-code]`).
pub(crate) fn at<'a>(doc: &'a Value, path: &str) -> Option<&'a Value> {
    let mut here = doc;
    if path == "$" {
        return Some(here);
    }
    let path = path.strip_prefix("$.").unwrap_or(path);
    for segment in split_path(path) {
        here = match segment.strip_prefix('[').and_then(|s| s.strip_suffix(']')) {
            Some(selector) => match selector.split_once('=') {
                Some((key, suffix)) if key.ends_with('$') => {
                    let key = &key[..key.len() - 1];
                    here.as_array()?.iter().find(|element| {
                        element[key]
                            .as_str()
                            .is_some_and(|text| text.ends_with(suffix))
                    })?
                }
                Some((key, value)) => here
                    .as_array()?
                    .iter()
                    .find(|element| element[key].as_str() == Some(value))?,
                // `runs[0]`: the element at an index.
                None => here.as_array()?.get(selector.parse::<usize>().ok()?)?,
            },
            None => match segment.parse::<usize>() {
                Ok(index) if here.is_array() => here.get(index)?,
                _ => here.get(segment)?,
            },
        };
    }
    Some(here)
}

/// A path's segments: dotted, with `[k=v]` kept whole (the value of a
/// selector may hold a dot).
fn split_path(path: &str) -> Vec<&str> {
    let mut segments = Vec::new();
    let (mut start, mut depth) = (0, 0usize);
    for (i, c) in path.char_indices() {
        match c {
            '[' => depth += 1,
            ']' => depth = depth.saturating_sub(1),
            '.' if depth == 0 => {
                if i > start {
                    segments.push(&path[start..i]);
                }
                start = i + 1;
            }
            _ => {}
        }
    }
    if start < path.len() {
        segments.push(&path[start..]);
    }
    // `servers[name=x].command` is `servers` then `[name=x]` then `command`.
    segments
        .into_iter()
        .flat_map(|segment| match segment.find('[') {
            Some(at) if at > 0 => vec![&segment[..at], &segment[at..]],
            _ => vec![segment],
        })
        .collect()
}

/// Go's `the output reports "<path>" as "<value>"`: the scalar at `path`,
/// as text.
pub(crate) fn reports_as(doc: &Value, path: &str) -> String {
    match at(doc, path) {
        Some(Value::String(s)) => s.clone(),
        Some(other) => other.to_string(),
        None => panic!("no {path} in {doc}"),
    }
}

/// Go's `the output reports "<path>" having "<n>" entries`. A list nobody
/// printed is empty: proto3 JSON leaves an empty list out (OD-40, design
/// §8.1), so an absent one has none.
pub(crate) fn entries(doc: &Value, path: &str) -> usize {
    // A path ending in a `[key=value]` selector counts every element it
    // matches (step 10c U10: `events[type=TreeObserved]` having 1 entries),
    // where reading it picks the first.
    if let Some((list, key, value)) = selected(path) {
        return at(doc, list).and_then(Value::as_array).map_or(0, |all| {
            all.iter()
                .filter(|element| element[key].as_str() == Some(value))
                .count()
        });
    }
    match at(doc, path) {
        Some(Value::Array(a)) => a.len(),
        Some(Value::Object(o)) => o.len(),
        None => 0,
        other => panic!("no list at {path}: {other:?} in {doc}"),
    }
}

/// A path whose last segment is a `[key=value]` selector: the list before
/// it, the key and the value; none for any other path.
fn selected(path: &str) -> Option<(&str, &str, &str)> {
    let inner = path.strip_suffix(']')?;
    let open = inner.rfind('[')?;
    let (key, value) = inner[open + 1..].split_once('=')?;
    if key.ends_with('$') || inner[open + 1..].contains('[') {
        return None;
    }
    Some((&path[..open], key, value))
}

#[test]
fn a_path_ending_in_a_selector_has_as_many_entries_as_it_matches() {
    let doc = serde_json::json!({"events": [
        {"type": "TreeObserved", "a": 1, "b": 2},
        {"type": "StepApplied"},
        {"type": "TreeObserved"}
    ]});
    assert_eq!(entries(&doc, "events[type=TreeObserved]"), 2);
    assert_eq!(entries(&doc, "events[type=StepApplied]"), 1);
    assert_eq!(entries(&doc, "events[type=Nothing]"), 0);
    assert_eq!(entries(&doc, "events"), 3);
    // An index or an ends-with selector reads as ever.
    assert_eq!(entries(&doc, "events[0]"), 3);
    assert_eq!(
        entries(
            &serde_json::json!({"x": [{"k": "ab", "y": [1]}]}),
            "x[k$=b].y"
        ),
        1
    );
}

/// What proto3 JSON leaves out, read back as text: a false, a zero, an
/// empty text. An absent field reads as its default (OD-40, design §8.1).
const DEFAULTS: [&str; 3] = ["false", "0", ""];

/// The step `the output reports "<path>" as "<value>"`: the scalar at
/// `path` is `value`, or, absent, `value` is a default proto3 JSON leaves
/// out (a fact that must print `false` has presence, so it is never absent).
pub(crate) fn holds(doc: &Value, path: &str, value: &str) -> Result<(), String> {
    match at(doc, path) {
        None if DEFAULTS.contains(&value) => Ok(()),
        None => Err(format!("no {path} in {doc}")),
        Some(_) => {
            let found = reports_as(doc, path);
            if found == value {
                Ok(())
            } else {
                Err(format!("{path} is {found:?}, not {value:?}"))
            }
        }
    }
}

/// Go's `the output reports "<path>" containing "<value>"`: a list holding
/// it, or a text holding it (a resource name, slice 2).
pub(crate) fn contains_value(doc: &Value, path: &str, value: &str) -> bool {
    match at(doc, path) {
        Some(Value::Array(a)) => a.iter().any(|e| e.as_str() == Some(value)),
        Some(Value::String(text)) => text.contains(value),
        _ => false,
    }
}

/// A list at `path` holding a resource name ending `suffix` (design §2: a
/// reference is a resource name, read without the project's harp).
pub(crate) fn names_ending(doc: &Value, path: &str, suffix: &str) -> bool {
    match at(doc, path) {
        Some(Value::Array(a)) => a
            .iter()
            .any(|e| e.as_str().is_some_and(|name| name.ends_with(suffix))),
        _ => false,
    }
}

// OD-40 (design §8.1): a resource name is selected by its suffix, so a step
// needs no harp; an empty list proto3 JSON left out has no entries; an
// absent field reads as its default, and only a default.
#[test]
fn test_the_steps_read_proto3_json_a_suffix_selects_and_absent_is_the_default() {
    let doc = serde_json::json!({"engines": [
        {"name": "projects/calm-otter/engines/claude-code", "runtime": {"value": "host"}},
        {"name": "projects/calm-otter/engines/opencode"},
    ]});
    assert_eq!(
        reports_as(&doc, "engines[name$=/engines/opencode].name"),
        "projects/calm-otter/engines/opencode"
    );
    assert_eq!(
        reports_as(&doc, "engines[name$=/claude-code].runtime.value"),
        "host"
    );
    assert!(at(&doc, "engines[name$=/engines/mock]").is_none());
    assert_eq!(entries(&doc, "engines"), 2);
    assert_eq!(entries(&doc, "engines[name$=/opencode].agents"), 0);
    assert!(holds(&doc, "engines[name$=/opencode].held", "false").is_ok());
    assert!(holds(&doc, "engines[name$=/opencode].count", "0").is_ok());
    assert!(holds(&doc, "engines[name$=/opencode].via", "").is_ok());
    assert!(holds(&doc, "engines[name$=/opencode].via", "x").is_err());
    assert!(holds(&doc, "engines[name$=/claude-code].runtime.value", "host").is_ok());
    assert!(holds(&doc, "engines[name$=/claude-code].runtime.value", "false").is_err());
}

/// Every file below `top`, by its path below it (slash separated) and its
/// bytes, `listed` told each file's path after its directory is listed and
/// before it is read. A file gone in between (a daemon's write renames its
/// temporary file into place) was not there to read, and is left out, as is
/// one another process holds a lock on where the platform refuses its read
/// for it (Windows' byte-range lock, `ERROR_LOCK_VIOLATION`; a daemon's lock
/// file); any other failure to read one is the test's.
pub(crate) fn tree_of(
    top: &Path,
    mut listed: impl FnMut(&Path),
) -> std::collections::BTreeMap<String, Vec<u8>> {
    fn walk(
        top: &Path,
        dir: &Path,
        listed: &mut dyn FnMut(&Path),
        out: &mut std::collections::BTreeMap<String, Vec<u8>>,
    ) {
        for path in files::children(dir) {
            if files::is_dir(&path) {
                walk(top, &path, listed, out);
                continue;
            }
            listed(&path);
            let bytes = match files::read_bytes(&path) {
                Ok(bytes) => bytes,
                Err(gone) if gone.kind() == std::io::ErrorKind::NotFound => continue,
                Err(locked) if on_windows() && locked.raw_os_error() == Some(LOCK_VIOLATION) => {
                    continue
                }
                Err(other) => panic!("{} does not read: {other}", path.display()),
            };
            let below = path.strip_prefix(top).expect("below the top");
            out.insert(below.to_string_lossy().replace('\\', "/"), bytes);
        }
    }
    let mut out = std::collections::BTreeMap::new();
    walk(top, top, &mut listed, &mut out);
    out
}

/// Windows' `ERROR_LOCK_VIOLATION`: a read of a range another process locked.
const LOCK_VIOLATION: i32 = 33;

/// Whether the host is Windows, as the platform crate says.
fn on_windows() -> bool {
    os::host().flavor() == os::Flavor::Windows
}

/// The harness's git runs no automatic maintenance: newer git starts a
/// DETACHED `git maintenance run --auto` after a commit, whose
/// `.git/objects/maintenance.lock` comes and goes under a test comparing the
/// project's tree before and after a command (CI on macOS, 2026-11-19).
pub(crate) const NO_MAINTENANCE: [&str; 4] = ["-c", "maintenance.auto=false", "-c", "gc.auto=0"];

// The same: the harness's git, the one its fixtures commit with, runs with
// automatic maintenance and gc off.
#[test]
fn the_harness_git_runs_no_automatic_maintenance() {
    let project = Project::bare();
    project.git(&["init", "-q"]);
    assert_eq!(
        project.git(&["config", "--get", "maintenance.auto"]).trim(),
        "false"
    );
    assert_eq!(project.git(&["config", "--get", "gc.auto"]).trim(), "0");
}

// CI on Windows (2026-11-19): a tree read a lock file a daemon held, and
// Windows refuses that read (`os error 33`). The lock is held while the tree
// is read: on Windows the file is left out; elsewhere (an advisory lock) it
// reads.
#[test]
fn a_tree_leaves_out_a_file_whose_read_another_lock_refuses() {
    let project = Project::bare();
    project.write("t/a.txt", "a");
    let held = project.dir().join("t/held.lock");
    let lock = file_lock::lock_within(&*files::fs(), &held, std::time::Duration::ZERO)
        .expect("the lock is free");
    let tree = tree_of(&project.dir().join("t"), |_| {});
    drop(lock);
    assert_eq!(tree["a.txt"], b"a");
    assert_eq!(
        tree.contains_key("held.lock"),
        !on_windows(),
        "{:?}",
        tree.keys()
    );
}

// CI on macOS (2026-11-19): a file renamed away between a tree's listing
// and its read (a daemon's write) failed the test reading the tree with
// `NotFound`. The race forced: the file is removed once it is listed.
#[test]
fn a_tree_leaves_out_a_file_gone_between_its_listing_and_its_read() {
    let project = Project::bare();
    project.write("t/a.txt", "a");
    project.write("t/gone.txt", "gone");
    project.write("t/sub/b.txt", "b");
    let tree = tree_of(&project.dir().join("t"), |path| {
        if path.ends_with("gone.txt") {
            files::remove(path);
        }
    });
    let names: Vec<&str> = tree.keys().map(String::as_str).collect();
    assert_eq!(names, ["a.txt", "sub/b.txt"]);
    assert_eq!(tree["sub/b.txt"], b"b");
}
