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

use super::library::{contains_value, reports_as, Project};
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
    /// has `value` among its entries.
    pub(crate) fn output_reports_containing(&self, path: &str, value: &str) {
        let ran = self.ran();
        let doc = serde_json::from_str::<Value>(&ran.stdout)
            .unwrap_or_else(|e| panic!("not json ({e}): {}", ran.said()));
        assert!(
            contains_value(&doc, path, value),
            "{path} should contain {value}: {}",
            ran.said()
        );
    }

    pub(crate) fn output_reports(&self, path: &str, value: &str) {
        let ran = self.ran();
        match serde_json::from_str::<Value>(&ran.stdout) {
            Ok(doc) => assert_eq!(reports_as(&doc, path), value, "{}", ran.said()),
            Err(_) => {
                ran.contains(value);
            }
        }
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

            #[then(expr = "the output reports {string} containing {string}")]
            fn output_reports_containing(w: &mut World, path: String, value: String) {
                w.common().output_reports_containing(&path, &value);
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
        W::cucumber()
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
