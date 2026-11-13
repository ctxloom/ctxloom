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
}

impl Common {
    pub(crate) fn new() -> Common {
        Common {
            project: Project::bare(),
            env: Vec::new(),
            ran: None,
            line: String::new(),
            aliases: Vec::new(),
        }
    }

    /// The commands of a docstring (one per line) or one line, run in turn
    /// (`$PROJECT_DIR` is the project's directory; in the input, which is
    /// JSON, as JSON writes it, so a Windows path's `\` is escaped); the first
    /// to fail is the one the next step sees.
    pub(crate) fn run_lines(&mut self, lines: &str, input: &str) {
        let dir = self.project.dir().display().to_string();
        let quoted = serde_json::to_string(&dir).expect("a string is JSON");
        let in_json = &quoted[1..quoted.len() - 1];
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
            let args: Vec<&str> = words[1..].iter().map(String::as_str).collect();
            let env: Vec<(&str, &str)> = self
                .env
                .iter()
                .map(|(k, v)| (k.as_str(), v.as_str()))
                .collect();
            let ran = if input.is_empty() {
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
                    panic!("{why}: {}", ran.said());
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

            #[given("an empty project directory")]
            fn empty(w: &mut World) {
                w.common_mut().project = Project::bare();
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

            #[given(expr = "the project already has the file {string}:")]
            fn project_file(w: &mut World, step: &Step, rel: String) {
                w.common().project.write(&rel, &format!("{}\n", doc(step)));
            }

            #[given(expr = "I run {string}")]
            #[when(expr = "I run {string}")]
            fn run(w: &mut World, line: String) {
                w.before_run();
                w.common_mut().run_lines(&line, "");
            }

            #[given(expr = "I run {string} with input:")]
            #[when(expr = "I run {string} with input:")]
            fn run_with_input(w: &mut World, step: &Step, line: String) {
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
                w.ran().contains(&Common::clap_wording(&text));
            }

            #[then(expr = "the output does not contain {string}")]
            fn output_lacks(w: &mut World, text: String) {
                w.ran().lacks(&Common::clap_wording(&text));
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

            #[then(expr = "the file {string} contains {string} exactly {int} times")]
            fn file_contains_times(w: &mut World, rel: String, text: String, times: usize) {
                w.common().project.file_counts(&rel, &text, times);
            }

            #[then(expr = "the file {string} does not contain {string}")]
            fn file_lacks(w: &mut World, rel: String, text: String) {
                w.common().project.file_lacks(&rel, &text);
            }

            #[then(expr = "the file {string} exists")]
            fn file_exists(w: &mut World, rel: String) {
                assert!(w.common().project.exists(&rel), "{rel} should exist");
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
}

/// The id of what failed in a feature outside any scenario (it did not
/// parse, a hook failed, it has no World): `<feature> :: (outside a
/// scenario)`.
pub(crate) fn outside_id(feature: &str) -> String {
    format!("{feature} :: (outside a scenario)")
}

/// Run one feature file with the World `W`, its pending scenarios left out:
/// whether it passed, and each scenario's outcome. A failure no scenario
/// owns is the feature's [`outside_id`]. A scenario whose step found the host
/// cannot run a fake program (`files::put_program`'s [`files::UNSUPPORTED`]
/// panic, off unix) is skipped, said `SKIP <scenario>: <why>` on stderr, and
/// fails nothing; on unix no step panics so.
pub(crate) fn run_feature<W>(file: &Path) -> FeatureRun
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
    let writer = futures::executor::block_on(
        W::cucumber::<&Path>()
            .with_parser(SpacedPlaceholders)
            .after(move |feature, rule, scenario, finished, _| {
                let id = scenario_id(&named, rule, scenario);
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
            .max_concurrent_scenarios(4)
            .fail_on_skipped()
            .filter_run(file, |feature, rule, scenario| {
                !is_pending(feature, rule, scenario)
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
    FeatureRun { passed, scenarios }
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
