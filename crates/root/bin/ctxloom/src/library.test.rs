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
    /// no project id.
    pub(crate) fn configured() -> Project {
        let project = Project::bare();
        project.write(".ctxloom/config.yaml", MINIMAL_CONFIG);
        project.write_home(
            ".ctxloom/config.yaml",
            "schema_version: 9\neditor:\n  command: \"true\"\n",
        );
        project
    }

    /// Go's "an empty project directory": nothing in it.
    pub(crate) fn bare() -> Project {
        let root = files::root();
        let project = root.root().join("project");
        let home = root.root().join("home");
        files::mkdirs(&project);
        files::mkdirs(&home);
        Project {
            _root: root,
            project,
            home,
            gos_catalog: false,
            program: None,
            path_first: std::sync::Mutex::new(Vec::new()),
        }
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
        // carries the id init mints (owner ruling 2026-11-13).
        if files::is_dir(project.project.join(".ctxloom")) && !project.exists(PROJECT_MARKER) {
            return project.named();
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

    /// The project as run by `program` (a copy of the binary under test
    /// under another name) instead of the binary at its own path.
    pub(crate) fn run_by(mut self, program: PathBuf) -> Project {
        self.program = Some(program);
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
        fn walk(top: &Path, dir: &Path, out: &mut std::collections::BTreeMap<String, Vec<u8>>) {
            for path in files::children(dir) {
                if files::is_dir(&path) {
                    walk(top, &path, out);
                } else {
                    let below = path.strip_prefix(top).expect("below the top");
                    out.insert(
                        below.to_string_lossy().replace('\\', "/"),
                        files::read_bytes(&path).expect("a file"),
                    );
                }
            }
        }
        let mut out = std::collections::BTreeMap::new();
        walk(&self.project.join(rel), &self.project.join(rel), &mut out);
        out
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
            .expect("ctxloom starts")
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
            .current_dir(cwd);
        for name in ["PATH", "SystemRoot"] {
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
    match at(doc, path) {
        Some(Value::Array(a)) => a.len(),
        Some(Value::Object(o)) => o.len(),
        None => 0,
        other => panic!("no list at {path}: {other:?} in {doc}"),
    }
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
