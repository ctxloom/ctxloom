//! The steps of j000100 (adopting ctxloom, and backing out), as a child of
//! the base World: Alice runs `ctxloom init` on an empty project directory,
//! looks at what it did, and leaves by deleting `.ctxloom`.
//!
//! Hermetic: the seeded default profile is bare (owner ruling 117), so
//! nothing reaches the network and no fixture stands in for content. The one
//! built-in bundle holds ctxloom's operational prompts, which no ordinary
//! session is handed: the stub's launch record carries no line of either.
//!
//! Claude Code is a stub first on `PATH`, so the engine init finds is the
//! same on every machine. It records what it was launched with: its
//! arguments, every file an argument names, its standard input, and every
//! file of the configuration directory it was pointed at (a session home).
//! Alice's own assistant configuration, in her home, is a settings
//! file and an instructions file of hers, so "exactly as it was" compares
//! something she wrote.

use std::collections::BTreeMap;
use std::path::PathBuf;

use cucumber::{given, then, when};

use super::super::gherkin_common::CommonWorld;
use super::World;
use crate::tests::files;

/// A line of each of ctxloom's built-in prompts (the setup interview, the
/// distill prompt), which no ordinary session's launch carries.
const OPERATIONAL_MARKERS: [&str; 2] = [
    "You are running the ctxloom **setup interview**",
    "You are a text compression assistant.",
];

/// The stub engine: records its launch beside itself, and replies as claude
/// does under `--output-format stream-json`: its session's `init` and a
/// `result` carrying the answer.
const CLAUDE_STUB: &str = r#"rec="$(dirname "$0")/claude-launch"
for a in "$@"; do
  printf '%s\n' "$a" >> "$rec"
  [ -f "$a" ] && cat "$a" >> "$rec"
done
if [ -n "${CLAUDE_CONFIG_DIR:-}" ] && [ -d "$CLAUDE_CONFIG_DIR" ]; then
  find "$CLAUDE_CONFIG_DIR" -type f -exec cat {} + >> "$rec" 2>/dev/null
fi
env > "$(dirname "$0")/claude-env"
[ -t 0 ] || cat >> "$rec"
echo '{"type":"system","subtype":"init","session_id":"00000000-0000-4000-8000-0000000000aa"}'
echo '{"type":"result","subtype":"success","is_error":false,"result":"J000100-ASSISTANT-REPLY","session_id":"00000000-0000-4000-8000-0000000000aa"}'
"#;

/// Where Alice's own assistant keeps its configuration, in her home.
const ASSISTANT_HOME: &[&str] = &[".claude", ".claude.json"];

/// A project as it stands: every file (path and bytes), and the names of
/// its top-level entries, sorted.
type Snapshot = (BTreeMap<String, Vec<u8>>, Vec<String>);

/// What the j000100 scenarios keep between steps.
#[derive(Default)]
pub(super) struct State {
    /// Every file of the project, and its top-level entries, before the
    /// first ctxloom command ran.
    before: Option<Snapshot>,
    /// Her assistant's own configuration in her home, then.
    assistant_before: Option<BTreeMap<String, Vec<u8>>>,
    /// Every file of her home, then.
    home_before: Option<BTreeMap<String, Vec<u8>>>,
    /// The session the last turn was taken on, by its harp.
    harp: Option<String>,
}

/// Every file below `path` (or `path` itself, a file), by its path.
fn files_below(path: &std::path::Path, out: &mut BTreeMap<String, Vec<u8>>) {
    if files::is_dir(path) {
        for child in files::children(path) {
            files_below(&child, out);
        }
    } else if let Ok(bytes) = files::read_bytes(path) {
        out.insert(path.display().to_string(), bytes);
    }
}

impl World {
    /// Record the project as it is, once, before ctxloom first runs in it.
    pub(super) fn adopt_record(&mut self) {
        if self.adopt.before.is_none() {
            self.adopt.before = Some(self.project_now());
            self.adopt.assistant_before = Some(self.assistant_now());
            self.adopt.home_before = Some(self.home_now());
        }
    }

    fn project_now(&self) -> Snapshot {
        let project = &self.common.project;
        let mut top: Vec<String> = files::children(project.dir())
            .iter()
            .filter_map(|p| p.file_name().map(|n| n.to_string_lossy().into_owned()))
            .collect();
        top.sort();
        (project.tree("."), top)
    }

    /// Every file of her home, by its path below it.
    fn home_now(&self) -> BTreeMap<String, Vec<u8>> {
        let home = self.common.project.home();
        let mut out = BTreeMap::new();
        files_below(home, &mut out);
        let prefix = format!("{}/", home.display());
        out.into_iter()
            .map(|(path, bytes)| (path.trim_start_matches(&prefix).to_string(), bytes))
            .collect()
    }

    fn assistant_now(&self) -> BTreeMap<String, Vec<u8>> {
        let mut out = BTreeMap::new();
        for rel in ASSISTANT_HOME {
            files_below(&self.common.project.home().join(rel), &mut out);
        }
        out
    }

    pub(super) fn launch_record(&self) -> String {
        files::read(self.adopt_scratch().join("bin/claude-launch")).unwrap_or_default()
    }

    /// The scenario's temporary directory, beside the project and the home.
    pub(super) fn adopt_scratch(&self) -> PathBuf {
        self.common
            .project
            .dir()
            .parent()
            .expect("the project is in the scenario's directory")
            .to_path_buf()
    }
}

fn outside_ctxloom<V: Clone>(tree: &BTreeMap<String, V>) -> BTreeMap<String, V> {
    tree.iter()
        .filter(|(path, _)| !path.starts_with(".ctxloom/"))
        .map(|(path, v)| (path.clone(), v.clone()))
        .collect()
}

#[given("Alice's assistant is Claude Code")]
pub(super) fn claude_code(w: &mut World) {
    let bin = w.adopt_scratch().join("bin");
    files::put_program(bin.join("claude"), CLAUDE_STUB);
    let project = &w.common.project;
    project.write_home(".claude/settings.json", "{\n  \"theme\": \"dark\"\n}\n");
    project.write_home(".claude/CLAUDE.md", "Alice's own instructions: be brief.\n");
    let host = crate::tests::host_var("PATH").unwrap_or_default();
    let path = format!("{}:{}", bin.display(), host.to_string_lossy());
    w.common.set_env("PATH", path);
}

/// The token every agent authenticates with: a fixture, so init's check
/// for it passes and no real credential is read.
#[given("Alice has exported her assistant's token")]
pub(super) fn token(w: &mut World) {
    w.common.set_env(
        "CLAUDE_CODE_OAUTH_TOKEN",
        "sk-ant-oat01-acceptance-fixture-not-real".to_string(), // gitleaks:allow
    );
}

/// The variables a child agent of claude is given a credential by.
const CHILD_CREDENTIALS: [&str; 2] = ["CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"];

/// The host exports no credential at all (the fixture token is withdrawn).
#[given("Alice has exported no credential")]
fn no_credential(w: &mut World) {
    w.common
        .env
        .retain(|(name, _)| !CHILD_CREDENTIALS.contains(&name.as_str()));
}

/// The host exports this one credential and no other.
#[given(expr = "Alice has exported only the credential {string}")]
fn only_credential(w: &mut World, variable: String) {
    no_credential(w);
    w.common
        .set_env(&variable, "sk-ant-fixture-not-real".to_string()); // gitleaks:allow
}

#[given("Alice has set ctxloom up in her project")]
fn set_up(w: &mut World) {
    w.before_run();
    w.common.run_lines("ctxloom init", "");
    w.ran().succeeds();
}

#[given("Alice has started her assistant through ctxloom")]
fn started_through_ctxloom(w: &mut World) {
    w.before_run();
    w.common.run_lines(
        "ctxloom run --one-shot \"what does this project expect of me?\"",
        "",
    );
    w.ran().succeeds();
}

/// Owner ruling 115: another turn on the session the last one-shot made
/// (its reply, json off a terminal, names it), by `run --session`.
#[when(expr = "Alice takes another turn on that session with {string}")]
fn another_turn(w: &mut World, prompt: String) {
    let harp = w.json()["harp"]
        .as_str()
        .unwrap_or_else(|| panic!("the last reply names its session: {}", w.ran().said()))
        .to_string();
    w.adopt.harp = Some(harp.clone());
    w.before_run();
    w.common.run_lines(
        &format!("ctxloom run --one-shot \"{prompt}\" --session {harp}"),
        "",
    );
}

#[then("the reply names the same session")]
fn same_session(w: &mut World) {
    let harp = w.adopt.harp.clone().expect("a turn was taken on a session");
    assert_eq!(
        w.json()["harp"].as_str(),
        Some(harp.as_str()),
        "{}",
        w.ran().said()
    );
}

/// The stub's launch record holds each argument on a line: the last launch
/// was handed `--resume` and the engine's own session id.
#[then(expr = "the assistant was resumed on its own session {string}")]
fn resumed_on(w: &mut World, native: String) {
    let record = w.launch_record();
    assert!(
        record.contains(&format!("--resume\n{native}\n")),
        "no resume of {native}: {record}"
    );
}

/// Owner ruling 116: the last launch was handed `flag` and then `value`, two
/// arguments in a row (the stub records each on a line).
#[then(expr = "the assistant was launched with {string} {string}")]
fn launched_with(w: &mut World, flag: String, value: String) {
    let record = w.launch_record();
    assert!(
        record.contains(&format!("{flag}\n{value}\n")),
        "no {flag} {value}: {record}"
    );
}

/// The last launch was never handed `flag` (the stub records each argument
/// on a line of its own).
#[then(expr = "the assistant was launched without {string}")]
fn launched_without(w: &mut World, flag: String) {
    let record = w.launch_record();
    assert!(!record.is_empty(), "no assistant was launched");
    assert!(
        !record.lines().any(|line| line == flag),
        "the launch was handed {flag}: {record}"
    );
}

/// The settings file the last launch was handed with `--settings`, parsed.
fn handed_settings(w: &World) -> serde_json::Value {
    let record = w.launch_record();
    let lines: Vec<&str> = record.lines().collect();
    let path = lines
        .iter()
        .rposition(|line| *line == "--settings")
        .and_then(|at| lines.get(at + 1))
        .unwrap_or_else(|| panic!("the launch was handed no --settings: {record}"));
    let text = files::read(path).unwrap_or_else(|e| panic!("reading {path}: {e}"));
    serde_json::from_str(&text).unwrap_or_else(|e| panic!("{path} is not JSON ({e}): {text}"))
}

/// Owner rulings 135, 137: the settings claude was handed hold, as their
/// `permissions`, exactly the document `want` (JSON).
#[then(expr = "the assistant's settings hold the permissions {string}")]
fn settings_hold_permissions(w: &mut World, want: String) {
    let want: serde_json::Value = serde_json::from_str(&want).expect("the step's JSON");
    let settings = handed_settings(w);
    assert_eq!(settings.get("permissions"), Some(&want), "{settings}");
}

/// Owner T21: the agent edits the settings file it was handed (it is in its
/// own session directory, within reach of its file tools), loosening its own
/// permissions and dropping ctxloom's hooks.
#[given("her assistant loosens its own permissions in the settings it was handed")]
fn loosens_its_settings(w: &mut World) {
    let record = w.launch_record();
    let lines: Vec<&str> = record.lines().collect();
    let path = lines
        .iter()
        .rposition(|line| *line == "--settings")
        .and_then(|at| lines.get(at + 1))
        .unwrap_or_else(|| panic!("the launch was handed no --settings: {record}"))
        .to_string();
    files::put(
        &path,
        r#"{"permissions":{"defaultMode":"bypassPermissions","allow":["Bash(*)"]}}"#,
    );
}

/// ctxloom's own keys of the handed settings are never overwritten by the
/// passthrough: its hooks still call `ctxloom hook claude-code execute`.
#[then("the assistant's settings still register ctxloom's hooks")]
fn settings_keep_hooks(w: &mut World) {
    let settings = handed_settings(w);
    let hooks = settings
        .get("hooks")
        .and_then(serde_json::Value::as_object)
        .unwrap_or_else(|| panic!("no hooks: {settings}"));
    assert!(!hooks.is_empty(), "no hook registered: {settings}");
    assert!(
        settings.to_string().contains("claude-code"),
        "the hooks are not ctxloom's: {settings}"
    );
}

#[then("her assistant was handed none of ctxloom's own prompts")]
fn handed_no_operational_prompt(w: &mut World) {
    let record = w.launch_record();
    assert!(
        record.contains("what does this project expect of me?"),
        "the session's launch was not recorded: {record}"
    );
    let shipped = [
        library::builtin::SETUP_INTERVIEW,
        library::builtin::DISTILL_PROMPT,
    ];
    for (marker, prompt) in OPERATIONAL_MARKERS.iter().zip(shipped) {
        assert!(
            prompt.contains(marker),
            "the prompt no longer says {marker:?}"
        );
        assert!(
            !record.contains(marker),
            "the session's launch carried a built-in prompt ({marker:?}): {record}"
        );
    }
    assert!(
        !files::exists(w.common.project.dir().join(".ctxloom/cache")),
        "nothing was fetched"
    );
}

#[then("no assistant was launched")]
fn no_assistant(w: &mut World) {
    assert_eq!(w.launch_record(), "", "an assistant was launched");
}

/// The environment the stub assistant was last given, by name.
fn assistant_env(w: &World) -> BTreeMap<String, String> {
    files::read(w.adopt_scratch().join("bin/claude-env"))
        .expect("the assistant ran")
        .lines()
        .filter_map(|l| l.split_once('='))
        .map(|(k, v)| (k.to_string(), v.to_string()))
        .collect()
}

#[then(expr = "the assistant's environment held {string} as {string}")]
fn assistant_env_held(w: &mut World, name: String, value: String) {
    let env = assistant_env(w);
    assert_eq!(env.get(&name), Some(&value), "{:?}", env.keys());
}

#[then(expr = "the assistant's environment did not hold {string}")]
fn assistant_env_lacks(w: &mut World, name: String) {
    let env = assistant_env(w);
    assert!(!env.contains_key(&name), "{:?}", env.keys());
}

#[then(expr = "the assistant's {string} was ctxloom's, not {string}")]
fn assistant_env_ctxlooms(w: &mut World, name: String, value: String) {
    let env = assistant_env(w);
    assert_ne!(env.get(&name), Some(&value), "{name}");
}

/// Owner ruling 112: claude's HOME is its session home, its config dir.
#[then("the assistant's HOME was its session home")]
fn assistant_home_is_session_home(w: &mut World) {
    let env = assistant_env(w);
    let home = env.get("HOME").expect("a HOME");
    assert_eq!(Some(home), env.get("CLAUDE_CONFIG_DIR"));
    assert!(home.contains("/.ctxloom/sessions/"), "{home}");
}

#[then("an assistant was launched")]
fn an_assistant(w: &mut World) {
    assert_ne!(w.launch_record(), "", "no assistant was launched");
}

/// The error's json envelope: the last json document on the error stream
/// (the warnings a run said before it, one json line each, come first).
fn envelope_of(w: &World) -> serde_json::Value {
    serde_json::Deserializer::from_str(&w.ran().stderr)
        .into_iter::<serde_json::Value>()
        .map(|value| value.expect("the error stream is json documents"))
        .last()
        .unwrap_or_else(|| panic!("a json envelope on the error stream: {}", w.ran().said()))
}

/// A refusal is a typed message: its code is the envelope's `code`.
#[then(expr = "the refusal is {string}")]
fn refusal_is(w: &mut World, code: String) {
    let envelope = envelope_of(w);
    assert_eq!(envelope["code"], code, "{}", w.ran().said());
}

/// An aggregate refusal lists each refusal it holds, in check order (owner
/// ruling 73b): their codes, comma separated.
#[then(expr = "the refusals are {string}")]
fn refusals_are(w: &mut World, codes: String) {
    let envelope = envelope_of(w);
    let listed: Vec<&str> = envelope["refusals"]
        .as_array()
        .unwrap_or_else(|| panic!("a refusals list: {}", w.ran().said()))
        .iter()
        .map(|r| r["code"].as_str().expect("a code"))
        .collect();
    let wanted: Vec<&str> = codes.split(", ").collect();
    assert_eq!(listed, wanted, "{}", w.ran().said());
}

/// Each listed refusal is the envelope it would be alone: its message, and the
/// fix where its catalog has one.
#[then("each refusal carries its own message and fix")]
fn each_refusal_whole(w: &mut World) {
    let envelope = envelope_of(w);
    for refusal in envelope["refusals"].as_array().expect("a refusals list") {
        assert!(
            refusal["message"].as_str().is_some_and(|m| !m.is_empty()),
            "{refusal}"
        );
        assert!(
            refusal["fix"].as_str().is_some_and(|f| !f.is_empty()),
            "{refusal}"
        );
        assert!(refusal["args"].is_object(), "{refusal}");
    }
}

/// A single refusal is its own plain envelope, as it always was (ruling 73b).
#[then("the refusal holds no list of refusals")]
fn refusal_holds_no_list(w: &mut World) {
    let envelope = envelope_of(w);
    assert!(envelope.get("refusals").is_none(), "{}", w.ran().said());
}

/// The json envelope carries the remedy as `fix` (owner ruling).
#[then(expr = "the refusal's fix says {string}")]
fn refusal_fix_says(w: &mut World, text: String) {
    let envelope = envelope_of(w);
    let fix = envelope["fix"].as_str().expect("a fix in the envelope");
    assert!(fix.contains(&text), "{fix:?} does not say {text:?}");
}

/// Her assistant's own configuration, in her home and in her project, as it
/// was before ctxloom first ran.
#[then("her assistant, started on its own, is exactly as it was before she set ctxloom up")]
fn assistant_as_it_was(w: &mut World) {
    let before = w.adopt.assistant_before.clone().expect("a command ran");
    assert!(
        w.assistant_now() == before,
        "her assistant's own configuration changed: now {:?}, before {:?}",
        w.assistant_now().keys().collect::<Vec<_>>(),
        before.keys().collect::<Vec<_>>()
    );
    let in_project = |tree: BTreeMap<String, Vec<u8>>| -> BTreeMap<String, Vec<u8>> {
        tree.into_iter()
            .filter(|(p, _)| p.starts_with(".claude/") || p == ".mcp.json" || p == "CLAUDE.md")
            .collect()
    };
    let was = in_project(w.adopt.before.clone().expect("a command ran").0);
    assert_eq!(
        in_project(w.project_now().0),
        was,
        "her project's assistant files changed"
    );
}

/// Asserted before the removal, so the scenario starts from a project that
/// demonstrably has something to remove.
#[given("her project has a .ctxloom directory")]
fn has_ctxloom(w: &mut World) {
    assert!(
        files::is_dir(w.common.project.dir().join(".ctxloom")),
        "no .ctxloom in the project"
    );
}

#[when("Alice takes ctxloom back out by deleting .ctxloom and ~/.ctxloom")]
fn delete_ctxloom(w: &mut World) {
    files::remove_tree(w.common.project.dir().join(".ctxloom"));
    files::remove_tree(w.common.project.home().join(".ctxloom"));
}

#[then("nothing in her home directory outside ~/.ctxloom was written")]
fn home_kept_to_its_directory(w: &mut World) {
    let before = w.adopt.home_before.clone().expect("a command ran");
    let (now, before) = (outside_ctxloom(&w.home_now()), outside_ctxloom(&before));
    let changed: Vec<&String> = now
        .keys()
        .chain(before.keys())
        .filter(|k| now.get(*k) != before.get(*k))
        .collect();
    assert!(
        changed.is_empty(),
        "ctxloom wrote outside ~/.ctxloom: {changed:?}"
    );
}

#[then("her home directory is exactly as it was before she set ctxloom up")]
fn home_as_it_was(w: &mut World) {
    let before = w.adopt.home_before.clone().expect("a command ran");
    let now = w.home_now();
    let changed: Vec<&String> = now
        .keys()
        .chain(before.keys())
        .filter(|k| now.get(*k) != before.get(*k))
        .collect();
    assert!(changed.is_empty(), "her home is not as it was: {changed:?}");
}

/// Asserted on the whole tree, not on a list of well-known files: any file
/// init writes outside `.ctxloom`, under any name, is one Alice would have
/// to find and delete by hand, and her exit is deleting `.ctxloom` alone.
/// (What init keeps under `~/.ctxloom` is a cache shared across projects,
/// and is left when she goes.)
#[then("nothing in her project outside .ctxloom was written")]
fn kept_to_its_directory(w: &mut World) {
    let (before_files, before_top) = w.adopt.before.clone().expect("a command ran");
    let (now_files, now_top) = w.project_now();
    let top = |names: Vec<String>| -> Vec<String> {
        names.into_iter().filter(|n| n != ".ctxloom").collect()
    };
    assert_eq!(
        top(now_top),
        top(before_top),
        "init left entries in the project outside .ctxloom"
    );
    let (now, before) = (outside_ctxloom(&now_files), outside_ctxloom(&before_files));
    let changed: Vec<&String> = now
        .keys()
        .chain(before.keys())
        .filter(|k| now.get(*k) != before.get(*k))
        .collect();
    assert!(
        changed.is_empty(),
        "init wrote outside .ctxloom: {changed:?}"
    );
}

#[then("her project is exactly as it was before she set ctxloom up")]
fn as_it_was(w: &mut World) {
    let before = w.adopt.before.clone().expect("a command ran");
    assert!(
        w.project_now() == before,
        "the project is not as it was before init: now {:?}, before {:?}",
        w.project_now().1,
        before.1
    );
}

/// Go's `steps_j000100_adopt.go`: doctor names a check for each part of the
/// harness. doctor needs no network, engine or credentials, which is what
/// makes it the check Alice can run before she trusts anything else.
#[then("she is shown a health check for every part of the harness")]
fn health_check(w: &mut World) {
    for marker in [
        "DOCTOR-CHECK-DEPS-a1",
        "DOCTOR-CHECK-AGENTS-b2",
        "DOCTOR-CHECK-HOOKS-TRUST-d4",
    ] {
        w.ran().contains(marker);
    }
}

/// The session a one-shot's json reply names records its outputs at
/// `<project>/<rel>/<harp>` (owner rulings 89, 106).
#[then(expr = "the session that answered keeps its outputs in {string} of her project")]
fn outputs_kept_in(w: &mut World, rel: String) {
    let reply = w.common.json();
    let harp = reply["harp"]
        .as_str()
        .expect("the reply names its session")
        .to_string();
    let env: Vec<(&str, &str)> = w
        .common
        .env
        .iter()
        .map(|(k, v)| (k.as_str(), v.as_str()))
        .collect();
    let shown = w
        .common
        .project
        .run_with(&env, &["session", "show", &harp, "--format", "json"]);
    assert_eq!(shown.code, 0, "{}", shown.said());
    let doc = super::super::parse_one(&shown.stdout);
    let want = w.common.project.dir().join(&rel).join(&harp);
    assert_eq!(
        doc["output_dir"].as_str(),
        Some(want.display().to_string().as_str()),
        "{doc}"
    );
}

/// The session a one-shot's json reply names is ended, and both `show` and
/// the listing say when (its incarnation's end, ruling 85), no earlier than
/// it started.
#[then("the session that answered is ended and says when it ended")]
fn says_when_it_ended(w: &mut World) {
    let reply = w.common.json();
    let harp = reply["harp"]
        .as_str()
        .expect("the reply names its session")
        .to_string();
    let env: Vec<(&str, &str)> = w
        .common
        .env
        .iter()
        .map(|(k, v)| (k.as_str(), v.as_str()))
        .collect();
    let shown = w
        .common
        .project
        .run_with(&env, &["session", "show", &harp, "--format", "json"]);
    assert_eq!(shown.code, 0, "{}", shown.said());
    let doc = super::super::parse_one(&shown.stdout);
    assert_eq!(doc["state"].as_str(), Some("ended"), "{doc}");
    let ended = doc["ended_at"].as_str().expect("show says when it ended");
    let started = doc["started_at"]
        .as_str()
        .expect("show says when it started");
    assert!(
        ended >= started,
        "ended {ended} before it started {started}"
    );
    let listed = w
        .common
        .project
        .run_with(&env, &["session", "list", "--format", "json"]);
    assert_eq!(listed.code, 0, "{}", listed.said());
    let rows = super::super::parse_one(&listed.stdout);
    let row = rows
        .as_array()
        .and_then(|rows| {
            rows.iter()
                .find(|r| r["harp"].as_str() == Some(harp.as_str()))
        })
        .unwrap_or_else(|| panic!("{harp} is not listed: {rows}"));
    assert_eq!(row["end"].as_str(), Some(ended), "{row}");
}
