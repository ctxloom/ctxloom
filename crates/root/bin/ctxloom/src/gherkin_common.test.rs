//! What the three cucumber Worlds (`gherkin.test.rs`, `gherkin_surfaces.test.rs`,
//! `gherkin_isolation.test.rs`) share, and the two things that run them all:
//! the feature discovery (`every_feature_runs`) and the parity lint
//! (`gherkin_lint.test.rs` uses the header reader here).
//!
//! A World embeds a [`Common`] (the project, the environment, the last
//! command) and implements [`CommonWorld`]; `common_steps!()` then defines
//! the steps every World has, in a module of its own, against that World.
//! Steps match per World, so a feature's steps all live in one World; the
//! feature's `# world:` header line says which (the convention is at the head
//! of `gherkin_lint.test.rs`).

use std::fmt;
use std::path::{Path, PathBuf};

use cucumber::gherkin::{Feature, GherkinEnv, Rule, Scenario, Step};
use serde_json::Value;

use super::library::{contains_value, entries, holds, Project};
use super::Ran;
use crate::tests::files;

/// The state every World has: the project and its home, the environment the
/// commands get, and the last command's outcome.
pub(crate) struct Common {
    pub(crate) project: Project,
    pub(crate) env: Vec<(String, String)>,
    pub(crate) ran: Option<Ran>,
    /// The last command line, as written.
    pub(crate) line: String,
    /// Names a scenario writes that the test minted under another, each
    /// `(written, minted)`, put in a command line's words before it runs: a
    /// session's harp is minted, never chosen (owner S55), so a journey's
    /// "amber-quiet-heron" is whichever harp its session was given.
    pub(crate) aliases: Vec<(String, String)>,
    /// What a step noted from a command's output, each `(label, value)`
    /// (`I note ... as "<label>"`): `{<label>}` in a later command line or
    /// in `the output contains` is the value (a minted name, never chosen).
    pub(crate) notes: Vec<(String, String)>,
    /// What the next step's commands alone run with, over [`Common::env`]
    /// (`... of the next command alone`): taken by [`Common::run_lines_at`].
    pub(crate) next_env: Vec<(String, String)>,
    /// Where every command runs when it is not the project: a directory that
    /// is in no ctxloom project (no `CTXLOOM_ROOT`).
    pub(crate) elsewhere: Option<PathBuf>,
}

impl Common {
    pub(crate) fn new() -> Common {
        Common {
            project: Project::bare(),
            env: Vec::new(),
            ran: None,
            line: String::new(),
            aliases: Vec::new(),
            notes: Vec::new(),
            next_env: Vec::new(),
            elsewhere: None,
        }
    }

    /// The commands of a docstring (one per line) or one line, run in turn
    /// (`$PROJECT_DIR` is the project's directory; in the input, which is
    /// JSON, as JSON writes it, so a Windows path's `\` is escaped); the first
    /// to fail is the one the next step sees.
    pub(crate) fn run_lines(&mut self, lines: &str, input: &str) {
        let elsewhere = self.elsewhere.clone();
        self.run_lines_at(elsewhere.as_deref(), lines, input);
    }

    /// `text` with each noted `{<label>}` as the value noted under it
    /// ([`Common::note`]); text naming no note is as written.
    pub(crate) fn expanded(&self, text: &str) -> String {
        self.notes
            .iter()
            .fold(text.to_string(), |text, (label, value)| {
                text.replace(&format!("{{{label}}}"), value)
            })
    }

    /// Keep `value` under `label`, for `{<label>}` in a later step.
    pub(crate) fn note(&mut self, label: &str, value: &str) {
        self.notes.retain(|(noted, _)| noted != label);
        self.notes.push((label.to_string(), value.to_string()));
    }

    /// [`Common::run_lines`], each command run from `cwd` when one is given
    /// (another project, found by walking up from it: no `CTXLOOM_ROOT`),
    /// else in the project.
    pub(crate) fn run_lines_at(&mut self, cwd: Option<&Path>, lines: &str, input: &str) {
        let lines = self.expanded(lines);
        let lines = lines.as_str();
        let dir = self.project.dir().display().to_string();
        let quoted = serde_json::to_string(&dir).expect("a string is JSON");
        let in_json = &quoted[1..quoted.len() - 1];
        let next_env = std::mem::take(&mut self.next_env);
        for line in lines.lines().map(str::trim).filter(|l| !l.is_empty()) {
            let line = line.replace("$PROJECT_DIR", &dir);
            let words: Vec<String> = split(&line)
                .into_iter()
                .map(|word| {
                    self.aliases
                        .iter()
                        .find(|(written, _)| *written == word)
                        .map_or(word.clone(), |(_, minted)| minted.clone())
                })
                .collect();
            assert_eq!(words.first().map(String::as_str), Some("ctxloom"), "{line}");
            let mut args: Vec<&str> = words[1..].iter().map(String::as_str).collect();
            let env: Vec<(&str, &str)> = self
                .env
                .iter()
                .filter(|(k, _)| !next_env.iter().any(|(n, _)| n == k))
                .chain(next_env.iter())
                .map(|(k, v)| (k.as_str(), v.as_str()))
                .collect();
            // A stood-in terminal reads what a real one does: `init`'s prose
            // (the default format off a stream that is no terminal is json).
            let at_terminal = env.iter().any(|(k, _)| *k == "CTXLOOM_TEST_TERMINAL");
            if at_terminal && args.first() == Some(&"init") && !args.contains(&"--format") {
                args.extend(["--format", "text"]);
            }
            let ran = if let Some(cwd) = cwd {
                self.project.run_in_with(cwd, &env, &args)
            } else if input.is_empty() {
                self.project.run_with(&env, &args)
            } else {
                self.project
                    .run_input(&args, &input.replace("$PROJECT_DIR", in_json), &env)
            };
            let failed = ran.code != 0;
            self.line = line;
            self.ran = Some(ran);
            if failed {
                return;
            }
        }
    }

    pub(crate) fn ran(&self) -> &Ran {
        self.ran.as_ref().expect("a command ran")
    }

    pub(crate) fn json(&self) -> Value {
        super::parse_one(&self.ran().stdout)
    }

    /// The directory of the recording git ([`Common::record_git_calls_next`]),
    /// beside the scenario's home.
    fn recording_git_dir(&self) -> PathBuf {
        self.project.home().join("..").join("recording-git")
    }

    /// Put a `git` that logs each call, then execs the real one, first on
    /// the `PATH` of the next step's commands alone (step 10 U4: the daemon
    /// runs git in the asking command's environment). Its log is one line
    /// per call, `<parent pid> <args>`: the parent is the process that ran
    /// it (the shim execs `sh`, so `$PPID` is the caller).
    pub(crate) fn record_git_calls_next(&mut self) {
        let path = self
            .env
            .iter()
            .find(|(name, _)| name == "PATH")
            .map(|(_, value)| std::ffi::OsString::from(value))
            .or_else(|| super::host_var("PATH"))
            .unwrap_or_default();
        let dirs = files::search_path(&path);
        let real = real_git(&dirs, |git| files::exists(git)).expect("a git on the PATH");
        let dir = self.recording_git_dir();
        let log = dir.join("calls.log");
        files::put_program(
            dir.join("git"),
            &format!(
                "printf '%s %s\\n' \"$PPID\" \"$*\" >> '{}'\nexec '{}' \"$@\"\n",
                log.display(),
                real.display()
            ),
        );
        let joined = std::env::join_paths(std::iter::once(dir).chain(dirs)).expect("a PATH");
        self.next_env.retain(|(name, _)| name != "PATH");
        self.next_env.push((
            "PATH".to_string(),
            joined.to_str().expect("utf-8").to_string(),
        ));
    }

    /// The recording git's calls, each `(parent pid, args)`; none when it
    /// was never run.
    pub(crate) fn recorded_git_calls(&self) -> Vec<(u32, String)> {
        git_calls(&files::read(self.recording_git_dir().join("calls.log")).unwrap_or_default())
    }

    pub(crate) fn set_env(&mut self, name: &str, value: String) {
        self.env.retain(|(n, _)| n != name);
        self.env.push((name.to_string(), value));
    }

    /// Whether the last command asked for the text form: an explicit
    /// `--format text`. Off a terminal the derived default is json.
    pub(crate) fn text_asked(&self) -> bool {
        let words = split(&self.line);
        words
            .windows(2)
            .any(|pair| pair[0] == "--format" && pair[1] == "text")
    }

    /// Go: "the output contains", with the two words clap says differently
    /// from cobra (plan 8.2: the command line is clap's): its usage error
    /// says "unexpected argument" where cobra said "unknown flag", and its
    /// heading is "Commands:" where cobra's was "Available Commands:".
    pub(crate) fn clap_wording(text: &str) -> String {
        if let Some(flag) = text.strip_prefix("unknown flag: ") {
            return format!("unexpected argument '{flag}'");
        }
        match text {
            "unknown flag" => "unexpected argument",
            "Available Commands:" => "Commands:",
            other => other,
        }
        .to_string()
    }

    /// `the output reports "<path>" as empty, saying "<words>"`: asked for
    /// text, the words are said; otherwise the json holds nothing at `path`,
    /// null or an empty list there.
    pub(crate) fn output_reports_empty(&self, path: &str, saying: &str) {
        if self.text_asked() {
            self.ran().contains(saying);
            return;
        }
        let doc = self.ran().json();
        match super::library::at(&doc, path) {
            None | Some(Value::Null) => {}
            Some(Value::Array(a)) => assert!(a.is_empty(), "{path}: {a:?}"),
            Some(other) => panic!("{path} is {other}, so the command did find something"),
        }
    }

    /// `the output reports no "<path>"`: the json holds nothing at `path`.
    pub(crate) fn output_reports_no(&self, path: &str) {
        let ran = self.ran();
        let doc = serde_json::from_str::<Value>(&ran.stdout)
            .unwrap_or_else(|e| panic!("not json ({e}): {}", ran.said()));
        assert!(
            super::library::at(&doc, path).is_none(),
            "{path} should be absent: {}",
            ran.said()
        );
    }

    /// `the output reports "<path>" containing "<value>"`: the list at `path`
    /// has `value` among its entries; an output that is not json (a text
    /// row) says `value`, as [`Common::output_reports`] reads one.
    pub(crate) fn output_reports_containing(&self, path: &str, value: &str) {
        let ran = self.ran();
        let Ok(doc) = serde_json::from_str::<Value>(&ran.stdout) else {
            // A text row names a reference by its short form, harp-free
            // (owner ruling 2026-11-17), so no `{project}` is substituted.
            ran.contains(value);
            return;
        };
        let (path, value) = (&self.named(path), &self.named(value));
        assert!(
            contains_value(&doc, path, value),
            "{path} should contain {value}: {}",
            ran.said()
        );
    }

    /// `the output reports "<path>" not containing "<value>"`: the negation
    /// of [`Common::output_reports_containing`]: the list at `path` does not
    /// hold `value` (nothing there holds nothing); an output that is not json
    /// (a text row) does not say `value`.
    pub(crate) fn output_reports_not_containing(&self, path: &str, value: &str) {
        let ran = self.ran();
        let Ok(doc) = serde_json::from_str::<Value>(&ran.stdout) else {
            ran.lacks(value);
            return;
        };
        let (path, value) = (&self.named(path), &self.named(value));
        assert!(
            !contains_value(&doc, path, value),
            "{path} should not contain {value}: {}",
            ran.said()
        );
    }

    /// `the output reports "<path>" having "<n>" entries`: the list (or
    /// object) at `path` has `n` entries; an output that is not json (a text
    /// row) says `n`'s words, as [`Common::output_reports`] reads one.
    pub(crate) fn output_reports_having(&self, path: &str, n: &str) {
        let ran = self.ran();
        let Ok(doc) = serde_json::from_str::<Value>(&ran.stdout) else {
            ran.contains(n);
            return;
        };
        let path = &self.named(path);
        assert_eq!(entries(&doc, path).to_string(), n, "{path}: {}", ran.said());
    }

    pub(crate) fn output_reports(&self, path: &str, value: &str) {
        let ran = self.ran();
        match serde_json::from_str::<Value>(&ran.stdout) {
            Ok(doc) => {
                let (path, value) = (self.named(path), self.named(value));
                if let Err(why) = holds(&doc, &path, &value) {
                    // A path the spec writes with `/` is reported in the
                    // host's spelling (`\` on Windows), as a person reads it
                    // there (CI on Windows, 2026-11-19).
                    let hosts = host_spelled(&value);
                    if hosts == value || holds(&doc, &path, &hosts).is_err() {
                        panic!("{why}: {}", ran.said());
                    }
                }
            }
            Err(_) => {
                ran.contains(value);
            }
        }
    }

    /// `text` with `{project}` as the project's harp (OD-40, design §8.1):
    /// what the daemon's `ResolveProject` names this project (OQ-2), asked
    /// through the hidden `ctxloom test project-name`, which answers
    /// `projects/<harp>`. Text without it is as written, and nothing is asked.
    /// `text` with every project resource name (`projects/<harp>`, a harp
    /// being lowercase words joined by `-`) cut to `projects/`.
    pub(crate) fn without_project_names(text: &str) -> String {
        let mut out = String::with_capacity(text.len());
        let mut rest = text;
        while let Some(at) = rest.find("projects/") {
            let (before, after) = rest.split_at(at + "projects/".len());
            out.push_str(before);
            let end = after
                .find(|c: char| !(c.is_ascii_lowercase() || c == '-'))
                .unwrap_or(after.len());
            rest = &after[end..];
        }
        out.push_str(rest);
        out
    }

    pub(crate) fn named(&self, text: &str) -> String {
        if !text.contains("{project}") {
            return text.to_string();
        }
        let env: Vec<(&str, &str)> = self
            .env
            .iter()
            .map(|(k, v)| (k.as_str(), v.as_str()))
            .collect();
        let ran = self.project.run_with(&env, &["test", "project-name"]);
        assert_eq!(ran.code, 0, "the project has no name: {}", ran.said());
        let name = ran.stdout.trim();
        let harp = name
            .strip_prefix("projects/")
            .unwrap_or_else(|| panic!("{name:?} names no project"));
        text.replace("{project}", harp)
    }
}

/// What a World gives the shared steps.
pub(crate) trait CommonWorld {
    fn common(&self) -> &Common;
    fn common_mut(&mut self) -> &mut Common;

    /// What a World does before each command (plants its sessions).
    fn before_run(&mut self) {}

    /// What a World does before the command `line` a step names runs, ahead
    /// of [`CommonWorld::before_run`] (a harness the command needs: a stub
    /// engine, a daemon the scenario owns).
    fn before_line(&mut self, line: &str) {
        let _ = line;
    }

    /// An `Alice ...:` step: its docstring's commands, run. A World whose
    /// step is told in stages (it needs the rest of the scenario before it
    /// can run) takes the step over.
    fn alice_runs(&mut self, step: &Step) {
        self.before_run();
        self.common_mut().run_lines(&doc(step), "");
    }

    fn ran(&self) -> &Ran {
        self.common().ran()
    }

    fn json(&self) -> Value {
        self.common().json()
    }
}

/// A command line's words: whitespace separates, double quotes group, and a
/// single quote that opens a word groups to its twin (so `''` is the empty
/// word a flag names to clear its axis).
pub(crate) fn split(line: &str) -> Vec<String> {
    let mut words = Vec::new();
    let mut word = String::new();
    let (mut quoted, mut any, mut single) = (false, false, false);
    for c in line.chars() {
        match c {
            '\'' if single => single = false,
            '\'' if !any && !quoted => {
                single = true;
                any = true;
            }
            c if single => word.push(c),
            '"' => {
                quoted = !quoted;
                any = true;
            }
            c if c.is_whitespace() && !quoted => {
                if any {
                    words.push(std::mem::take(&mut word));
                    any = false;
                }
            }
            c => {
                word.push(c);
                any = true;
            }
        }
    }
    if any {
        words.push(word);
    }
    words
}

/// A step's docstring, trimmed.
pub(crate) fn doc(step: &Step) -> String {
    step.docstring
        .clone()
        .expect("a docstring")
        .trim()
        .to_string()
}

/// The first `git` (or Windows' `git.exe`) of `dirs` that `exists`.
fn real_git(dirs: &[PathBuf], exists: impl Fn(&Path) -> bool) -> Option<PathBuf> {
    dirs.iter()
        .flat_map(|dir| ["git", "git.exe"].map(|name| dir.join(name)))
        .find(|git| exists(git))
}

/// The recording git's log, `<parent pid> <args>` a line, as `(pid, args)`;
/// a line whose first word is no pid is not a call.
pub(crate) fn git_calls(log: &str) -> Vec<(u32, String)> {
    log.lines()
        .filter_map(|line| {
            let (pid, args) = line.split_once(' ').unwrap_or((line, ""));
            pid.parse().ok().map(|pid| (pid, args.to_string()))
        })
        .collect()
}

/// Define the steps every World has, in a module of its own, against the
/// `World` in scope (which implements [`CommonWorld`]).
macro_rules! common_steps {
    () => {
        mod common {
            use cucumber::gherkin::Step;
            use cucumber::{given, then, when};

            use super::World;
            use $crate::tests::gherkin_common::{doc, Common, CommonWorld};
            use $crate::tests::library::Project;

            #[given("an initialized ctxloom project")]
            fn initialized(w: &mut World) {
                w.common_mut().project = Project::initialized();
            }

            /// A directory, not yet a project: her commands find the
            /// project from where she types them, as hers do, with no
            /// `CTXLOOM_ROOT` (an empty one is none), so outside any project
            /// is outside any project.
            #[given("an empty project directory")]
            fn empty(w: &mut World) {
                w.common_mut().project = Project::bare();
                w.common_mut().set_env("CTXLOOM_ROOT", String::new());
            }

            /// A project from before git was required (step 10, G1).
            #[given("an initialized ctxloom project outside git")]
            fn initialized_outside_git(w: &mut World) {
                w.common_mut().project = Project::configured_outside_git().named();
            }

            /// The repository is the project's parent; the home is outside it.
            #[given("an initialized ctxloom project in a subdirectory of a git repository")]
            fn initialized_in_a_subdirectory(w: &mut World) {
                w.common_mut().project = Project::configured_in_a_subdirectory().named();
            }

            #[given("an empty project directory in a subdirectory of a git repository")]
            fn empty_in_a_subdirectory(w: &mut World) {
                w.common_mut().project = Project::bare_in_a_subdirectory();
            }

            /// Her live repository: `git init` and one empty commit.
            #[given("an empty project directory that is a git repository")]
            fn empty_in_git(w: &mut World) {
                w.common_mut().project = Project::bare_in_git();
            }

            /// Go's fixture bundle: a manifest, an example fragment and an
            /// example command (`Project::bundle`).
            #[given(expr = "a bundle {string} exists")]
            fn bundle(w: &mut World, name: String) {
                w.common().project.bundle(&name);
            }

            #[given(expr = "a fragment {string} in bundle {string} exists")]
            fn fragment(w: &mut World, name: String, bundle: String) {
                w.common().project.fragment(&bundle, &name);
            }

            #[given(expr = "a command {string} in bundle {string} exists")]
            fn command(w: &mut World, name: String, bundle: String) {
                w.common().project.command(&bundle, &name);
            }

            /// Go's fixture: a dedicated `<name>-base` bundle, so the profile
            /// stands alone.
            #[given(expr = "a profile {string} exists")]
            fn profile_exists(w: &mut World, name: String) {
                let base = format!("{name}-base");
                w.common().project.bundle(&base);
                w.common().project.profile(&name, &base);
            }

            #[given(expr = "a profile {string} with bundle {string}")]
            fn profile(w: &mut World, name: String, bundle: String) {
                w.common().project.profile(&name, &bundle);
            }

            /// A file of the project's own bundle lands in that bundle as an
            /// initialized project has it, its manifest there (as `init` and
            /// `profile create` write it; `Project::profile` does the same).
            #[given(expr = "the project already has the file {string}:")]
            fn project_file(w: &mut World, step: &Step, rel: String) {
                let manifest = ".ctxloom/content/bundles/v2/project/bundle.yaml";
                let project = &w.common().project;
                if rel.starts_with(".ctxloom/content/bundles/v2/project/")
                    && !project.exists(manifest)
                {
                    project.write(manifest, "schema_version: 4\nversion: 1.0.0\n");
                }
                project.write(&rel, &format!("{}\n", doc(step)));
            }

            #[given(expr = "I run {string}")]
            #[when(expr = "I run {string}")]
            fn run(w: &mut World, line: String) {
                w.before_line(&line);
                w.before_run();
                w.common_mut().run_lines(&line, "");
            }

            #[given(expr = "I run {string} with input:")]
            #[when(expr = "I run {string} with input:")]
            fn run_with_input(w: &mut World, step: &Step, line: String) {
                w.before_line(&line);
                w.before_run();
                w.common_mut().run_lines(&line, &doc(step));
            }

            /// Every `Alice ...:` step carries its commands in a docstring.
            #[when(regex = r"^Alice .*:$")]
            fn alice_runs(w: &mut World, step: &Step) {
                w.alice_runs(step);
            }

            #[then("the command succeeds")]
            fn succeeds(w: &mut World) {
                w.ran().succeeds();
            }

            #[then("the command fails")]
            fn fails(w: &mut World) {
                w.ran().fails();
            }

            #[then(expr = "the command exits with code {int}")]
            fn exits(w: &mut World, code: i32) {
                assert_eq!(w.ran().code, code, "{}", w.ran().said());
            }

            #[then(expr = "the output contains {string}")]
            fn output_contains(w: &mut World, text: String) {
                let text = w.common().expanded(&text);
                w.ran().contains_as_platform(&Common::clap_wording(&text));
            }

            #[then(expr = "the output does not contain {string}")]
            fn output_lacks(w: &mut World, text: String) {
                let text = Common::clap_wording(&w.common().expanded(&text));
                // A project's harp is minted at random from harp's word
                // lists, so it may hold any word a scenario says is absent
                // ("projects/unholy-unrelated-petticoat"): the output is
                // judged with every project name taken out, never by the
                // luck of the draw. Taken out by its shape, so judging asks
                // nothing (asking the name could mint one or start a daemon).
                let ran = w.ran();
                let output = Common::without_project_names(&ran.output());
                assert!(
                    !output.contains(&text),
                    "expected the output not to contain {text:?}: {}",
                    ran.said()
                );
            }

            #[then("the output is valid JSON")]
            fn valid_json(w: &mut World) {
                let _ = w.json();
            }

            #[then(expr = "the output reports {string} as {string}")]
            fn output_reports(w: &mut World, path: String, value: String) {
                w.common().output_reports(&path, &value);
            }

            #[then(expr = "the output reports no {string}")]
            fn output_reports_no(w: &mut World, path: String) {
                w.common().output_reports_no(&path);
            }

            #[then(expr = "the output reports {string} as empty, saying {string}")]
            fn output_reports_empty(w: &mut World, path: String, saying: String) {
                w.common().output_reports_empty(&path, &saying);
            }

            #[then(expr = "the output reports {string} containing {string}")]
            fn output_reports_containing(w: &mut World, path: String, value: String) {
                w.common().output_reports_containing(&path, &value);
            }

            #[then(expr = "the output reports {string} not containing {string}")]
            fn output_reports_not_containing(w: &mut World, path: String, value: String) {
                w.common().output_reports_not_containing(&path, &value);
            }

            #[then(expr = "the output reports {string} having {string} entries")]
            fn output_reports_having(w: &mut World, path: String, n: String) {
                w.common().output_reports_having(&path, &n);
            }

            #[given(expr = "the file {string} contains {string}")]
            #[then(expr = "the file {string} contains {string}")]
            fn file_contains(w: &mut World, rel: String, text: String) {
                w.common().project.file_contains(&rel, &text);
            }

            #[given("a git that records each call is first on the PATH of the next command alone")]
            fn recording_git(w: &mut World) {
                w.common_mut().record_git_calls_next();
            }

            /// The serving daemon ran the recording git: a call whose
            /// parent is the daemon `daemon status` names, never the command
            /// that asked (whose PATH alone held it).
            #[then("the recording git was called for the pull")]
            fn recorded_for_pull(w: &mut World) {
                let common = w.common();
                let calls = common.recorded_git_calls();
                let env: Vec<(&str, &str)> = common
                    .env
                    .iter()
                    .map(|(k, v)| (k.as_str(), v.as_str()))
                    .collect();
                let status = common
                    .project
                    .run_with(&env, &["daemon", "status", "--format", "json"]);
                assert_eq!(status.code, 0, "{}", status.said());
                let pid = status.json()["daemons"][0]["pid"].as_u64();
                assert!(
                    calls
                        .iter()
                        .any(|(parent, _)| Some(u64::from(*parent)) == pid),
                    "no git call of the serving daemon ({pid:?}): {calls:?}"
                );
            }

            #[then(expr = "the file {string} contains {string} exactly {int} times")]
            fn file_contains_times(w: &mut World, rel: String, text: String, times: usize) {
                w.common().project.file_counts(&rel, &text, times);
            }

            /// Go's: a file that is not there contains nothing.
            #[then(expr = "the file {string} does not contain {string}")]
            fn file_lacks(w: &mut World, rel: String, text: String) {
                if w.common().project.exists(&rel) {
                    w.common().project.file_lacks(&rel, &text);
                }
            }

            #[then(expr = "the file {string} exists")]
            fn file_exists(w: &mut World, rel: String) {
                assert!(w.common().project.exists(&rel), "{rel} should exist");
            }

            /// The file parses as YAML (angzarr-yaml, the reader ctxloom's
            /// own documents go through); a parse error fails the step with it.
            #[then(expr = "the file {string} is valid YAML")]
            fn file_valid_yaml(w: &mut World, rel: String) {
                let held = w
                    .common()
                    .project
                    .read(&rel)
                    .unwrap_or_else(|| panic!("{rel} should exist"));
                angzarr_yaml::parse(&held).unwrap_or_else(|e| panic!("{rel}: {e}"));
            }

            #[then(expr = "the file {string} does not exist")]
            fn file_absent(w: &mut World, rel: String) {
                w.common().project.file_is_absent(&rel);
            }
        }
    };
}
pub(crate) use common_steps;

/// What one feature's run showed: whether it passed, and each scenario it ran
/// by its stable id ([`scenario_id`]) with whether that scenario passed.
pub(crate) struct FeatureRun {
    pub(crate) passed: bool,
    pub(crate) scenarios: Vec<(String, bool)>,
    /// Each step that failed, in the order they failed: what the ratchet's
    /// `<list>.failures` says of a red scenario.
    pub(crate) failures: Vec<Failure>,
}

/// A step that failed: its scenario's id ([`scenario_id`]), the step as the
/// feature writes it (its keyword and text), and what it said.
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct Failure {
    pub(crate) scenario: String,
    pub(crate) step: String,
    pub(crate) message: String,
}

/// A cucumber writer beside the console's that keeps each failed step
/// ([`Failure`]) of one feature, whatever the console shows.
struct StepFailures {
    feature: String,
    kept: std::sync::Arc<std::sync::Mutex<Vec<Failure>>>,
}

impl<W: cucumber::World> cucumber::Writer<W> for StepFailures {
    type Cli = cucumber::cli::Empty;

    async fn handle_event(
        &mut self,
        event: cucumber::parser::Result<cucumber::Event<cucumber::event::Cucumber<W>>>,
        _: &Self::Cli,
    ) {
        use cucumber::event::{Cucumber, Feature as F, Rule as R, Scenario as S, Step};
        let Ok(event) = event else { return };
        let Cucumber::Feature(_, feature) = event.value else {
            return;
        };
        let (rule, scenario, event) = match feature {
            F::Scenario(scenario, event) => (None, scenario, event),
            F::Rule(rule, R::Scenario(scenario, event)) => (Some(rule), scenario, event),
            _ => return,
        };
        let (S::Step(step, Step::Failed(_, _, _, error))
        | S::Background(step, Step::Failed(_, _, _, error))) = event.event
        else {
            return;
        };
        self.kept.lock().expect("not poisoned").push(Failure {
            scenario: scenario_id(&self.feature, rule.as_deref(), &scenario),
            step: format!("{} {}", step.keyword.trim(), step.value),
            message: error.to_string(),
        });
    }
}

impl cucumber::writer::Normalized for StepFailures {}

/// The id of what failed in a feature outside any scenario (it did not
/// parse, a hook failed, it has no World): `<feature> :: (outside a
/// scenario)`.
pub(crate) fn outside_id(feature: &str) -> String {
    format!("{feature} :: (outside a scenario)")
}

/// `GHERKIN_TIMINGS=<absolute file>`'s profile: each scenario's wall time,
/// from its start to its end, appended to the file as `<seconds>\t<id>`.
#[derive(Clone, Default)]
struct Timings {
    file: Option<PathBuf>,
    started:
        std::sync::Arc<std::sync::Mutex<std::collections::BTreeMap<String, std::time::Instant>>>,
}

impl Timings {
    fn asked() -> Timings {
        Timings {
            file: super::host_var("GHERKIN_TIMINGS").map(PathBuf::from),
            started: std::sync::Arc::default(),
        }
    }

    fn on(&self) -> bool {
        self.file.is_some()
    }

    fn start(&self, id: String) {
        if self.on() {
            let now = std::time::Instant::now();
            self.started.lock().expect("not poisoned").insert(id, now);
        }
    }

    fn finish(&self, id: &str) {
        let Some(file) = &self.file else { return };
        let Some(start) = self.started.lock().expect("not poisoned").remove(id) else {
            return;
        };
        use std::io::Write as _;
        let line = format!("{:.2}\t{id}\n", start.elapsed().as_secs_f64());
        // A profile, best effort: a line that cannot be written is lost.
        let _ = files::open_append(file).and_then(|mut f| f.write_all(line.as_bytes()));
    }
}

/// Run one feature file with the World `W`, its pending scenarios left out
/// (and, when `only` names a scenario id, every other scenario too):
/// whether it passed, and each scenario's outcome. A failure no scenario
/// owns is the feature's [`outside_id`]. A scenario whose step found the host
/// cannot run a fake program (`files::put_program`'s [`files::UNSUPPORTED`]
/// panic, off unix) is skipped, said `SKIP <scenario>: <why>` on stderr, and
/// fails nothing; on unix no step panics so.
///
/// A whole feature's scenarios run at once, [`SCENARIOS_AT_ONCE`] at a
/// time, each a cucumber run of its own on a thread of its own
/// ([`scenario_ids`], then this with `only` naming it): within one cucumber
/// run they take turns, because every step blocks its one executor thread.
/// Each scenario has a temporary root of its own, so nothing one runs is
/// another's. Under `GHERKIN_TIMINGS` (and for a feature whose scenarios
/// cannot be listed, so its parse error is reported as before) the feature
/// is one run, its scenarios one at a time.
pub(crate) fn run_feature<W>(file: &Path, only: Option<&str>) -> FeatureRun
where
    W: cucumber::World + cucumber::codegen::WorldInventory + fmt::Debug,
{
    if only.is_none() && !Timings::asked().on() {
        if let Some(ids) = scenario_ids(file).filter(|ids| !ids.is_empty()) {
            return run_scenarios_at_once(&ids, scenarios_at_once(), |id| {
                run_feature_whole::<W>(file, Some(id))
            });
        }
    }
    run_feature_whole::<W>(file, only)
}

/// How many scenarios of one feature run at once: nextest already runs as
/// many features at once as it runs tests, so a few more each.
// 2, not 3: measured 2026-11-18 with nextest's 12 threads, as fast on a quiet host and faster under load, at ~60% of 20x3's load average.
pub(crate) const SCENARIOS_AT_ONCE: usize = 2;

/// How many scenarios of one feature run at once in this test process:
/// `GHERKIN_SCENARIOS_AT_ONCE`, read once, else [`SCENARIOS_AT_ONCE`] (a
/// knob for measuring the run, as `NEXTEST_TEST_THREADS` is nextest's).
fn scenarios_at_once() -> usize {
    static AT_ONCE: std::sync::OnceLock<usize> = std::sync::OnceLock::new();
    *AT_ONCE.get_or_init(|| {
        scenarios_at_once_from(super::host_var("GHERKIN_SCENARIOS_AT_ONCE").as_deref())
    })
}

/// [`scenarios_at_once`] of the variable's value: unset is the default, a
/// positive count is that count; anything else panics, so a mistyped knob
/// never measures the default.
pub(crate) fn scenarios_at_once_from(value: Option<&std::ffi::OsStr>) -> usize {
    let Some(value) = value else {
        return SCENARIOS_AT_ONCE;
    };
    value
        .to_str()
        .and_then(|text| text.parse::<usize>().ok())
        .filter(|&count| count > 0)
        .unwrap_or_else(|| {
            panic!("GHERKIN_SCENARIOS_AT_ONCE is {value:?}, not a count of 1 or more")
        })
}

/// The ids of a feature file's scenarios that a run would run (its pending
/// ones left out), each once, in the file's order; `None` when it does not
/// parse.
pub(crate) fn scenario_ids(file: &Path) -> Option<Vec<String>> {
    use cucumber::feature::Ext as _;
    let feature = Feature::parse_path(file, GherkinEnv::default()).ok()?;
    let feature = join_spaced_placeholders(feature).expand_examples().ok()?;
    let name = file
        .strip_prefix(features_dir())
        .unwrap_or(file)
        .display()
        .to_string();
    let mut ids: Vec<String> = Vec::new();
    let plain = feature.scenarios.iter().map(|s| (None, s));
    let ruled = feature
        .rules
        .iter()
        .flat_map(|r| r.scenarios.iter().map(move |s| (Some(r), s)));
    for (rule, scenario) in plain.chain(ruled) {
        if is_pending(&feature, rule, scenario) {
            continue;
        }
        let id = scenario_id(&name, rule, scenario);
        if !ids.contains(&id) {
            ids.push(id);
        }
    }
    Some(ids)
}

/// Run each of `ids` by `run`, `at_once` at a time, each on a thread of
/// its own, and put their outcomes together, in the order of `ids`: the
/// feature passed when every run did.
pub(crate) fn run_scenarios_at_once(
    ids: &[String],
    at_once: usize,
    run: impl Fn(&str) -> FeatureRun + Sync,
) -> FeatureRun {
    let next = std::sync::atomic::AtomicUsize::new(0);
    let runs: Vec<std::sync::Mutex<Option<FeatureRun>>> =
        ids.iter().map(|_| std::sync::Mutex::new(None)).collect();
    std::thread::scope(|scope| {
        for _ in 0..at_once.clamp(1, ids.len().max(1)) {
            scope.spawn(|| loop {
                let i = next.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
                let Some(id) = ids.get(i) else { break };
                *runs[i].lock().expect("not poisoned") = Some(run(id));
            });
        }
    });
    let mut all = FeatureRun {
        passed: true,
        scenarios: Vec::new(),
        failures: Vec::new(),
    };
    for (id, one) in ids.iter().zip(runs) {
        let mut one = one
            .into_inner()
            .expect("not poisoned")
            .expect("every id ran");
        // An id its own run did not run is red, never left out of the list.
        if one.scenarios.is_empty() {
            one.passed = false;
            one.scenarios.push((id.clone(), false));
            one.failures.push(Failure {
                scenario: id.clone(),
                step: "(none)".to_string(),
                message: "its own run ran no scenario of this id".to_string(),
            });
        }
        all.passed &= one.passed;
        all.scenarios.extend(one.scenarios);
        all.failures.extend(one.failures);
    }
    all
}

/// [`run_feature`]'s one cucumber run.
fn run_feature_whole<W>(file: &Path, only: Option<&str>) -> FeatureRun
where
    W: cucumber::World + cucumber::codegen::WorldInventory + fmt::Debug,
{
    use cucumber::event::ScenarioFinished;
    use futures::FutureExt as _;
    let feature = file
        .strip_prefix(features_dir())
        .unwrap_or(file)
        .display()
        .to_string();
    let seen = std::sync::Arc::new(std::sync::Mutex::new(Vec::new()));
    let record = std::sync::Arc::clone(&seen);
    let unsupported = std::sync::Arc::new(std::sync::atomic::AtomicUsize::new(0));
    let skips = std::sync::Arc::clone(&unsupported);
    let named = feature.clone();
    let chosen = feature.clone();
    let only = only.map(str::to_string);
    let failures = std::sync::Arc::new(std::sync::Mutex::new(Vec::new()));
    let recorder = StepFailures {
        feature: feature.clone(),
        kept: std::sync::Arc::clone(&failures),
    };
    let timings = Timings::asked();
    let at_once = if timings.on() { 1 } else { 4 };
    let stamp = timings.clone();
    let stamped = feature.clone();
    let writer = futures::executor::block_on(
        W::cucumber::<&Path>()
            .with_writer({
                use cucumber::WriterExt as _;
                cucumber::writer::Basic::stdout()
                    .summarized()
                    .tee::<W, _>(recorder.discard_arbitrary_writes().discard_stats_writes())
            })
            .with_parser(SpacedPlaceholders)
            .before(move |_, rule, scenario, _| {
                stamp.start(scenario_id(&stamped, rule, scenario));
                async {}.boxed_local()
            })
            .after(move |feature, rule, scenario, finished, _| {
                let id = scenario_id(&named, rule, scenario);
                timings.finish(&id);
                if let Some(why) = unsupported_here(finished) {
                    eprintln!("SKIP {id}: {why}");
                    skips.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
                    record.lock().expect("not poisoned").push((id, true));
                    return async {}.boxed_local();
                }
                let passed = match finished {
                    ScenarioFinished::StepPassed => true,
                    // `fail_on_skipped`: a skipped step fails the scenario
                    // unless it allows one.
                    ScenarioFinished::StepSkipped => [&feature.tags, &scenario.tags]
                        .into_iter()
                        .chain(rule.map(|r| &r.tags))
                        .any(|tags| tags.iter().any(|t| t == "allow.skipped")),
                    ScenarioFinished::BeforeHookFailed(_) | ScenarioFinished::StepFailed(..) => {
                        false
                    }
                };
                record.lock().expect("not poisoned").push((id, passed));
                async {}.boxed_local()
            })
            // The test harness's own arguments (`--exact`) are not ours.
            .with_cli(cucumber::cli::Opts::<_, _, _, cucumber::cli::Empty>::default())
            .max_concurrent_scenarios(at_once)
            .fail_on_skipped()
            .filter_run(file, move |feature, rule, scenario| {
                !is_pending(feature, rule, scenario)
                    && only
                        .as_deref()
                        .is_none_or(|id| scenario_id(&chosen, rule, scenario) == id)
            }),
    );
    // Each skipped scenario failed the one step that found the host unable.
    let passed = {
        use cucumber::writer::Stats as _;
        let skipped = unsupported.load(std::sync::atomic::Ordering::SeqCst);
        writer.failed_steps() == skipped
            && writer.parsing_errors() == 0
            && writer.hook_errors() == 0
    };
    let mut scenarios = std::mem::take(&mut *seen.lock().expect("not poisoned"));
    if !passed && scenarios.iter().all(|(_, ok)| *ok) {
        scenarios.push((outside_id(&feature), false));
    }
    let failures = std::mem::take(&mut *failures.lock().expect("not poisoned"));
    FeatureRun {
        passed,
        scenarios,
        failures,
    }
}

/// cucumber 0.21's parser for one feature file, except that an outline's
/// placeholder whose name holds a space (Go's `<result count>`) is filled from
/// its column like any other. cucumber expands only `<[^>\s]+>` and leaves
/// such a placeholder as written, so before it expands the examples each such
/// column is renamed ([`join_spaced_placeholders`]); the feature's text is
/// never changed.
#[derive(Clone, Copy, Debug, Default)]
pub(crate) struct SpacedPlaceholders;

impl<I: AsRef<Path>> cucumber::Parser<I> for SpacedPlaceholders {
    type Cli = cucumber::cli::Empty;
    type Output = futures::stream::Iter<std::vec::IntoIter<cucumber::parser::Result<Feature>>>;

    fn parse(self, input: I, _: Self::Cli) -> Self::Output {
        use cucumber::feature::Ext as _;
        let parsed = Feature::parse_path(input.as_ref(), GherkinEnv::default())
            .map_err(cucumber::parser::Error::from)
            .and_then(|feature| {
                join_spaced_placeholders(feature)
                    .expand_examples()
                    .map_err(cucumber::parser::Error::from)
            });
        futures::stream::iter(vec![parsed])
    }
}

/// The feature with every outline's spaced column names joined: a header
/// cell `result count` of its Examples becomes `result_count` (underscores
/// added until no other column of the outline has the name), and each
/// `<result count>` in the outline's name, its steps' text, docstrings and
/// table cells becomes `<result_count>`, so cucumber fills it from the
/// column. A `<...>` that names no column is left as it is, as cucumber
/// leaves it. The data rows are untouched, so a scenario's id is too.
pub(crate) fn join_spaced_placeholders(mut feature: Feature) -> Feature {
    for scenario in feature.scenarios.iter_mut().chain(
        feature
            .rules
            .iter_mut()
            .flat_map(|r| r.scenarios.iter_mut()),
    ) {
        join_in_scenario(scenario);
    }
    feature
}

fn join_in_scenario(scenario: &mut Scenario) {
    let headers: Vec<String> = scenario
        .examples
        .iter()
        .filter_map(|ex| ex.table.as_ref()?.rows.first())
        .flatten()
        .cloned()
        .collect();
    let mut taken: std::collections::BTreeSet<String> = headers.iter().cloned().collect();
    let mut renames: Vec<(String, String)> = Vec::new();
    for header in &headers {
        if !header.chars().any(char::is_whitespace) || renames.iter().any(|(old, _)| old == header)
        {
            continue;
        }
        let mut joined = header.split_whitespace().collect::<Vec<_>>().join("_");
        while taken.contains(&joined) {
            joined.push('_');
        }
        taken.insert(joined.clone());
        renames.push((header.clone(), joined));
    }
    if renames.is_empty() {
        return;
    }
    let rename = |text: &mut String| {
        for (old, new) in &renames {
            let from = format!("<{old}>");
            if text.contains(&from) {
                *text = text.replace(&from, &format!("<{new}>"));
            }
        }
    };
    rename(&mut scenario.name);
    for step in &mut scenario.steps {
        rename(&mut step.value);
        if let Some(doc) = step.docstring.as_mut() {
            rename(doc);
        }
        for cell in step
            .table
            .iter_mut()
            .flat_map(|t| t.rows.iter_mut().flatten())
        {
            rename(cell);
        }
    }
    for header in scenario
        .examples
        .iter_mut()
        .filter_map(|ex| ex.table.as_mut()?.rows.first_mut())
        .flatten()
    {
        if let Some((_, new)) = renames.iter().find(|(old, _)| old == header) {
            *header = new.clone();
        }
    }
}

/// The reason, when a scenario failed only because its step found the host
/// cannot run a fake program (`files::put_program` panicked with
/// [`files::UNSUPPORTED`]).
fn unsupported_here(finished: &cucumber::event::ScenarioFinished) -> Option<String> {
    let cucumber::event::ScenarioFinished::StepFailed(
        _,
        _,
        cucumber::event::StepError::Panic(payload),
    ) = finished
    else {
        return None;
    };
    let text = payload
        .downcast_ref::<String>()
        .map(String::as_str)
        .or_else(|| payload.downcast_ref::<&str>().copied())?;
    text.strip_prefix(files::UNSUPPORTED).map(str::to_string)
}

/// A scenario's stable id: `<feature> :: [<rule> :: ]<scenario>`, the
/// feature's path under `features/`, and for an outline's example the row it
/// ran, `:: | v1 | v2 |` (cucumber names each example by the outline, its
/// `<placeholders>` filled, and puts it at its Examples' line plus the row's
/// index plus 2).
pub(crate) fn scenario_id(feature: &str, rule: Option<&Rule>, scenario: &Scenario) -> String {
    let mut id = feature.to_string();
    if let Some(rule) = rule {
        id.push_str(" :: ");
        id.push_str(&rule.name);
    }
    id.push_str(" :: ");
    id.push_str(&scenario.name);
    let line = scenario.position.line;
    let row = scenario
        .examples
        .iter()
        .filter(|ex| ex.position.line + 2 <= line)
        .max_by_key(|ex| ex.position.line)
        .and_then(|ex| ex.table.as_ref()?.rows.get(line - ex.position.line - 1));
    if let Some(row) = row {
        id.push_str(&format!(" :: | {} |", row.join(" | ")));
    }
    id
}

/// The features directory.
pub(crate) fn features_dir() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("features")
}

/// Every `.feature` file under `features/`, as a path relative to it with
/// `/` separators, sorted.
pub(crate) fn feature_files() -> Vec<String> {
    fn walk(dir: &Path, rel: &str, found: &mut Vec<String>) {
        let mut entries = files::children(dir);
        entries.sort();
        for path in entries {
            let name = path
                .file_name()
                .expect("a name")
                .to_string_lossy()
                .into_owned();
            let rel = if rel.is_empty() {
                name.clone()
            } else {
                format!("{rel}/{name}")
            };
            if files::is_dir(&path) {
                walk(&path, &rel, found);
            } else if name.ends_with(".feature") {
                found.push(rel);
            }
        }
    }
    let mut found = Vec::new();
    walk(&features_dir(), "", &mut found);
    found
}

/// The value of each header line that starts with `prefix` (`# world:`,
/// `# NOT ported:`, `# Ported from`) among the comment lines at the head of
/// a feature file, in order.
pub(crate) fn header_values<'a>(text: &'a str, prefix: &str) -> Vec<&'a str> {
    header_lines(text)
        .filter_map(|line| line.strip_prefix(prefix))
        .map(str::trim)
        .collect()
}

/// A feature file's header: the leading run of comment lines, blank lines
/// allowed.
pub(crate) fn header_lines(text: &str) -> impl Iterator<Item = &str> {
    text.lines()
        .map(str::trim_end)
        .take_while(|l| l.is_empty() || l.starts_with('#'))
}

/// Whether a whole feature is pending: its `Feature:` is tagged `@pending`,
/// so none of its scenarios run and it needs no World. The runner skips the
/// file; the lint still holds its text to Go's (convention:
/// `gherkin_lint.test.rs`).
pub(crate) fn is_pending_feature(text: &str) -> bool {
    Feature::parse(text, GherkinEnv::default()).is_ok_and(|f| tagged_pending(&f.tags))
}

/// Whether a scenario is pending: Go's, copied but not yet ported, tagged
/// `@pending` itself or through its Rule or its Feature. The runner leaves
/// it out; the lint still holds its text to Go's.
pub(crate) fn is_pending(feature: &Feature, rule: Option<&Rule>, scenario: &Scenario) -> bool {
    tagged_pending(&feature.tags)
        || rule.is_some_and(|r| tagged_pending(&r.tags))
        || tagged_pending(&scenario.tags)
}

/// Whether a tag list carries `@pending`.
pub(crate) fn tagged_pending(tags: &[String]) -> bool {
    tags.iter().any(|t| t == "pending")
}

/// Which World runs a feature, from its `# world:` header line.
pub(crate) fn world_of(text: &str) -> Result<&str, String> {
    match header_values(text, "# world:").as_slice() {
        [world @ ("base" | "surfaces" | "isolation")] => Ok(world),
        [] => Err("no `# world: base|surfaces|isolation` header line".to_string()),
        other => Err(format!(
            "`# world:` must be one of base, surfaces, isolation, once: {other:?}"
        )),
    }
}

/// The real git is found by either spelling, the first directory holding
/// one: Windows' is `git.exe` (found there, the recording git step reaches
/// `put_program`, whose unsupported-host panic skips the scenario).
#[test]
fn the_real_git_is_found_as_git_or_as_git_exe() {
    let dirs = [
        PathBuf::from("/a"),
        PathBuf::from("/b"),
        PathBuf::from("/c"),
    ];
    // Paths are built as the platform joins them (Windows' separator).
    let at = |dir: &str, name: &str| PathBuf::from(dir).join(name);
    let found = |holding: &[PathBuf]| real_git(&dirs, |path| holding.iter().any(|h| h == path));
    assert_eq!(found(&[at("/b", "git")]), Some(at("/b", "git")));
    assert_eq!(found(&[at("/b", "git.exe")]), Some(at("/b", "git.exe")));
    assert_eq!(
        found(&[at("/c", "git"), at("/b", "git.exe")]),
        Some(at("/b", "git.exe"))
    );
    assert_eq!(found(&[]), None);
}

/// Only `put_program`'s unsupported-host panic skips a scenario: any other
/// panic, and any other ending, is the scenario's own outcome.
#[test]
fn only_the_unsupported_host_panic_skips_a_scenario() {
    use cucumber::event::{ScenarioFinished, StepError};
    let panicked = |payload: std::sync::Arc<dyn std::any::Any + Send>| {
        ScenarioFinished::StepFailed(None, None, StepError::Panic(payload))
    };
    let why = format!("{}a fixture program needs sh", files::UNSUPPORTED);
    assert_eq!(
        unsupported_here(&panicked(std::sync::Arc::new(why))),
        Some("a fixture program needs sh".to_string())
    );
    assert_eq!(
        unsupported_here(&panicked(std::sync::Arc::new("a program: NotFound"))),
        None
    );
    assert_eq!(unsupported_here(&panicked(std::sync::Arc::new(7_u8))), None);
    assert_eq!(unsupported_here(&ScenarioFinished::StepPassed), None);
    assert_eq!(unsupported_here(&ScenarioFinished::StepSkipped), None);
}

/// Each spaced column is joined in its header and wherever the outline names
/// it (its name, a step's text, docstring and table), a joined name that
/// another column has takes another underscore, and what names no column, or
/// a data row, is left as written.
#[test]
fn spaced_columns_are_joined_in_the_header_and_wherever_the_outline_names_them() {
    let text = "Feature: f\n  Rule: r\n    Scenario Outline: o <the count>\n      Then it says \"<the count>\" and <the_count> and <no such>\n        \"\"\"\n        <the count> <the  kind>\n        \"\"\"\n      And a table:\n        | <the count> | <the  kind> |\n\n      Examples:\n        | the count | the_count | the  kind |\n        | a b       | 2         | x y       |\n";
    let feature =
        join_spaced_placeholders(Feature::parse(text, GherkinEnv::default()).expect("it parses"));
    let scenario = &feature.rules[0].scenarios[0];
    assert_eq!(scenario.name, "o <the_count_>");
    assert_eq!(
        scenario.steps[0].value,
        "it says \"<the_count_>\" and <the_count> and <no such>"
    );
    assert_eq!(
        scenario.steps[0].docstring.as_deref().map(str::trim),
        Some("<the_count_> <the_kind>")
    );
    assert_eq!(
        scenario.steps[1].table.as_ref().expect("a table").rows,
        vec![vec!["<the_count_>".to_string(), "<the_kind>".to_string()]]
    );
    let rows = &scenario.examples[0].table.as_ref().expect("a table").rows;
    assert_eq!(rows[0], vec!["the_count_", "the_count", "the_kind"]);
    assert_eq!(rows[1], vec!["a b", "2", "x y"]);
    use cucumber::feature::Ext as _;
    let expanded = feature.expand_examples().expect("it expands");
    assert_eq!(expanded.rules[0].scenarios[0].name, "o a b");
}

/// The recording git's log: each line its caller's pid, then the call's
/// words; a call with no words is the pid alone, and a line that starts
/// with no pid is not a call.
#[test]
fn the_recording_gits_log_reads_as_each_call_and_its_caller() {
    assert_eq!(
        git_calls("41 fetch --quiet origin\n42\nnot-a-pid clone\n\n7 ls-remote"),
        vec![
            (41, "fetch --quiet origin".to_string()),
            (42, String::new()),
            (7, "ls-remote".to_string()),
        ]
    );
}

/// `value` with each `/` the host's separator: itself off Windows.
fn host_spelled(value: &str) -> String {
    match os::host().flavor() {
        os::Flavor::Windows => value.replace('/', "\\"),
        os::Flavor::Unix => value.to_string(),
    }
}

#[test]
fn a_project_name_holding_the_word_is_not_taken_for_it() {
    let output =
        r#"{"name": "projects/unholy-unrelated-petticoat/bundles/demo", "x": "unrelated"}"#;
    let judged = Common::without_project_names(output);
    assert_eq!(
        judged,
        r#"{"name": "projects//bundles/demo", "x": "unrelated"}"#
    );
    assert!(
        !Common::without_project_names(r#"projects/unholy-unrelated-petticoat"#)
            .contains("unrelated")
    );
}
