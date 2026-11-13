//! The steps of j000400 (one shared profile, two engines) and j002000 (the
//! engine switch): sessions started on stub engines, through the real binary.
//!
//! The engines are stubs on `PATH` (an interactive run, the terminal stood in
//! by `CTXLOOM_TEST_TERMINAL`): a `claude` and an `opencode` that record what
//! they were handed (their arguments, working directory and the variables that
//! name their config dirs) under `<scratch>/bin/runs/<harp>/`, and exit. The
//! stub opencode also does what opencode does with the plugins of its config
//! dir at a session's start: it loads each under node and tells it the
//! session was created (`session.created`), then disposes it. What a session
//! "starts with" and "offers" is read where the engine itself reads it: the
//! context file claude's command line names and the project's own
//! `CLAUDE.md`, and what the launch carries (`LaunchSurfaces`): claude's
//! commands in its config dir, its servers in the `--mcp-config` file and
//! its hooks in the `--settings` file; opencode's commands and `mcp` in its
//! config dir, its hooks run by ctxloom's plugin (the team's start hook
//! writes [`HOOK_OUT`] there).

use std::collections::BTreeMap;
use std::path::PathBuf;

use cucumber::gherkin::Step;
use cucumber::{given, then, when};
use serde_json::Value;

use super::World;
use crate::tests::files;
use crate::tests::gherkin_common::{doc, CommonWorld};

/// The team's fragment, the only place this text is.
const CONTEXT_MARKER: &str = "J000400-CONTEXT-MARKER-3f8a91";
/// The team command's body.
const COMMAND_MARKER: &str = "J000400-COMMAND-BODY-MARKER-7c2d64";
/// The team MCP server's program.
const MCP_COMMAND: &str = "j000400-mcp-tool-9e1b52";
/// The team's session-start hook.
const HOOK_MARKER: &str = "J000400-HOOK-COMMAND-4a6f18";
/// What the team's hook writes, in opencode's config dir (the session
/// home), when the session runs it.
const HOOK_OUT: &str = "team-hook.out";
/// Alice's own notes for Claude Code, in the project's `CLAUDE.md`.
const ALICES_NOTES: &str =
    "# Alice's notes\n\nJ000400-ALICES-NOTES-MARKER-6d2e73: tabs, never spaces\n";

/// The team's guidance in j002000.
const J002000_MARKER: &str = "J002000-TEAM-GUIDANCE-MARKER";
/// The session recorded before the switch.
const J002000_HARP: &str = "amber-quiet-heron";

/// The stub claude: records what it was handed, and exits.
const CLAUDE_STUB: &str = r#"here="$(dirname "$0")"
dir="$here/runs/$CTXLOOM_SESSION_HARP"
mkdir -p "$dir"
echo claude-code > "$dir/engine"
pwd > "$dir/cwd"
printf '%s\n' "$CLAUDE_CONFIG_DIR" > "$dir/config-dir"
: > "$dir/args"
for a in "$@"; do printf '%s\n' "$a" >> "$dir/args"; done
"#;

/// The stub opencode: records what it was handed, then runs every plugin of
/// its config dir under node over a session's start.
const OPENCODE_STUB: &str = r#"here="$(dirname "$0")"
dir="$here/runs/$CTXLOOM_SESSION_HARP"
mkdir -p "$dir"
echo opencode > "$dir/engine"
pwd > "$dir/cwd"
printf '%s\n' "$OPENCODE_CONFIG_DIR" > "$dir/config-dir"
: > "$dir/args"
for a in "$@"; do printf '%s\n' "$a" >> "$dir/args"; done
node "$here/plugins.mjs" "$OPENCODE_CONFIG_DIR/plugins" > "$dir/plugins-out" 2>&1
echo "$?" > "$dir/plugins-code"
"#;

/// What opencode does with its config dir's plugins at a session's start:
/// each loaded once, told the session was created, then disposed.
const PLUGINS_DRIVER: &str = r#"import { readdirSync } from "node:fs"
import { join } from "node:path"
import { pathToFileURL } from "node:url"
const [dir] = process.argv.slice(2)
const event = { id: "evt_1", type: "session.created", properties: { info: { id: "ses_j000400", title: "j000400" } } }
for (const name of readdirSync(dir).filter((n) => n.endsWith(".js")).sort()) {
  const module = await import(pathToFileURL(join(dir, name)).href)
  for (const plugin of Object.values(module)) {
    if (typeof plugin !== "function") continue
    const hooks = await plugin({})
    if (hooks.event) await hooks.event({ event })
    if (hooks.dispose) await hooks.dispose()
  }
}
"#;

/// What the scenarios keep.
#[derive(Default)]
pub(super) struct State {
    /// Whether the stub engines are on the commands' `PATH`.
    staged: bool,
    /// What a session wrote into the project outside its own directory, by
    /// path, over every session started.
    wrote: Vec<String>,
    /// The sessions started, in order: harp and engine.
    started: Vec<(String, String)>,
    /// What the commands of a step that runs several said, in order.
    said: Vec<crate::tests::Ran>,
    /// The guidance the old engine's session started with (j002000).
    old_guidance: Option<String>,
}

impl World {
    fn stub_dir(&self) -> PathBuf {
        self.adopt_scratch().join("bin")
    }

    /// The stub engines on the commands' `PATH`, once, and the terminal
    /// stood in.
    fn stage_engines(&mut self) {
        if self.engines.staged {
            return;
        }
        let bin = self.stub_dir();
        files::put_program(bin.join("claude"), CLAUDE_STUB);
        files::put_program(bin.join("opencode"), OPENCODE_STUB);
        files::put(bin.join("plugins.mjs"), PLUGINS_DRIVER);
        let host = crate::tests::host_var("PATH").unwrap_or_default();
        self.common.set_env(
            "PATH",
            format!("{}:{}", bin.display(), host.to_string_lossy()),
        );
        self.common
            .set_env("CTXLOOM_TEST_TERMINAL", "1".to_string());
        self.engines.staged = true;
    }

    /// The project's files outside the sessions' own directories, and
    /// outside the project's identity (`.ctxloom/project-id`, git-ignored,
    /// which the first command to open the project's store writes, whatever
    /// the command).
    fn outside_sessions(&self) -> BTreeMap<String, Vec<u8>> {
        self.common
            .project
            .tree("")
            .into_iter()
            .filter(|(path, _)| {
                !path.starts_with(".ctxloom/sessions/") && path != ".ctxloom/project-id"
            })
            .collect()
    }

    /// The harps of the sessions the project has a directory for.
    fn harps(&self) -> Vec<String> {
        let sessions = self.common.project.dir().join(".ctxloom/sessions");
        files::children(&sessions)
            .iter()
            .filter(|p| files::is_dir(p))
            .filter_map(|p| p.file_name().map(|n| n.to_string_lossy().into_owned()))
            .collect()
    }

    /// Each command of `lines` in turn, a run's effect on the project kept;
    /// the first to fail stops the rest.
    fn run_each(&mut self, lines: &str) {
        self.stage_engines();
        self.before_run();
        self.engines.said.clear();
        for line in lines.lines().map(str::trim).filter(|l| !l.is_empty()) {
            let session = line.starts_with("ctxloom run");
            let (before, harps) = if session {
                (self.outside_sessions(), self.harps())
            } else {
                (BTreeMap::new(), Vec::new())
            };
            self.common.run_lines(line, "");
            let ran = self.common.ran();
            let failed = ran.code != 0;
            let copy = crate::tests::Ran {
                code: ran.code,
                stdout: ran.stdout.clone(),
                stderr: ran.stderr.clone(),
            };
            self.engines.said.push(copy);
            if session {
                let after = self.outside_sessions();
                for (path, bytes) in &after {
                    if before.get(path) != Some(bytes) {
                        self.engines.wrote.push(path.clone());
                    }
                }
                for path in before.keys().filter(|p| !after.contains_key(*p)) {
                    self.engines.wrote.push(format!("{path} (removed)"));
                }
                for harp in self.harps().into_iter().filter(|h| !harps.contains(h)) {
                    let engine =
                        files::read(self.stub_dir().join("runs").join(&harp).join("engine"))
                            .unwrap_or_default()
                            .trim()
                            .to_string();
                    self.engines.started.push((harp, engine));
                }
            }
            if failed {
                return;
            }
        }
    }

    /// The last session started on `engine`: its harp.
    fn session_on(&self, engine: &str) -> String {
        self.engines
            .started
            .iter()
            .rev()
            .find(|(_, e)| e == engine)
            .map(|(h, _)| h.clone())
            .unwrap_or_else(|| {
                panic!(
                    "no session ran on {engine} (started: {:?}): {}",
                    self.engines.started,
                    self.ran().said()
                )
            })
    }

    /// What the stub recorded of the session `harp`, by file name.
    fn recorded(&self, harp: &str, what: &str) -> String {
        files::read(self.stub_dir().join("runs").join(harp).join(what))
            .unwrap_or_else(|e| panic!("the engine recorded no {what} for {harp}: {e}"))
    }

    /// The session's engine config dir, as the engine was told it.
    fn config_dir(&self, harp: &str) -> PathBuf {
        PathBuf::from(self.recorded(harp, "config-dir").trim())
    }

    /// The argument after `flag` on the engine's command line.
    fn arg_after(&self, harp: &str, flag: &str) -> Option<String> {
        let args = self.recorded(harp, "args");
        let args: Vec<&str> = args.lines().collect();
        args.windows(2)
            .find(|w| w[0] == flag)
            .map(|w| w[1].to_string())
    }

    /// The context claude's session was handed: the file its command line
    /// names for the system prompt.
    fn claude_context(&self, harp: &str) -> String {
        let file = self
            .arg_after(harp, "--append-system-prompt-file")
            .unwrap_or_else(|| {
                panic!(
                    "claude was handed no context file: {}",
                    self.recorded(harp, "args")
                )
            });
        files::read(&file).unwrap_or_else(|e| panic!("{file}: {e}"))
    }

    /// The context opencode's session reads: its config dir's `AGENTS.md`.
    fn opencode_context(&self, harp: &str) -> String {
        let file = self.config_dir(harp).join("AGENTS.md");
        files::read(&file).unwrap_or_else(|e| panic!("{}: {e}", file.display()))
    }

    /// Every file below `dir` whose path ends `.md`, by text.
    fn markdown_below(dir: &PathBuf) -> Vec<String> {
        let mut found = Vec::new();
        for path in files::children(dir) {
            if files::is_dir(&path) {
                found.extend(World::markdown_below(&path));
            } else if path.extension().is_some_and(|e| e == "md") {
                found.push(files::read(&path).unwrap_or_default());
            }
        }
        found
    }
}

fn json_file(path: &PathBuf) -> Value {
    let text = files::read(path).unwrap_or_else(|e| panic!("{}: {e}", path.display()));
    serde_json::from_str(&text)
        .unwrap_or_else(|e| panic!("{} is not JSON ({e}): {text}", path.display()))
}

/// The `Alice ...:` steps that start a session (the generic `Alice ...:` step
/// is the World's `alice_runs`, which hands them here): whether it is one.
pub(super) fn starts(w: &mut World, step: &Step) -> bool {
    let ours = step.value.starts_with("Alice starts a session on ")
        || step.value.contains("and starts a session with it");
    if !ours || !w.engines_journey() {
        return false;
    }
    w.run_each(&doc(step));
    true
}

impl World {
    /// Whether the scenario is one of these journeys': its stubs staged by
    /// its Background.
    fn engines_journey(&self) -> bool {
        self.engines.staged
    }
}

// ------------------------------------------------------------- j000400

#[given(
    expr = "Carol's team profile {string} carries a shared fragment, command, MCP server, and hook"
)]
fn team_profile(w: &mut World, name: String) {
    let project = crate::tests::library::Project::initialized();
    let root = ".ctxloom/content/bundles/v2/team";
    project.write(
        &format!("{root}/bundle.yaml"),
        "schema_version: 4\nversion: 1.0.0\ndescription: the team's shared bundle\n",
    );
    project.write(
        &format!("{root}/fragments/onboarding-context.md"),
        CONTEXT_MARKER,
    );
    project.write(
        &format!("{root}/prompts/onboarding.md"),
        &format!("---\ndescription: the team's onboarding command\n---\n{COMMAND_MARKER}"),
    );
    project.write(
        &format!("{root}/mcp/toolserver.yaml"),
        &format!("command: {MCP_COMMAND}\nargs:\n  - \"--flag\"\n"),
    );
    project.write(
        &format!("{root}/hooks/session_start/team-hook.yaml"),
        &format!("type: command\ncommand: 'echo {HOOK_MARKER} > \"${{OPENCODE_CONFIG_DIR}}/{HOOK_OUT}\"'\n"),
    );
    project.profile(&name, "team");
    w.common.project = project;
    w.stage_engines();
}

#[given("Alice keeps her own notes for Claude Code in the project")]
fn alices_notes(w: &mut World) {
    w.common.project.write("CLAUDE.md", ALICES_NOTES);
}

#[when(regex = r"^Bob starts a session on (\S+) with the team profile:$")]
fn bob_starts(w: &mut World, step: &Step, _engine: String) {
    w.run_each(&doc(step));
}

#[then("Alice's session starts with the team's guidance and her own notes")]
fn alice_guided(w: &mut World) {
    w.ran().succeeds();
    let harp = w.session_on("claude-code");
    let context = w.claude_context(&harp);
    assert!(
        context.contains(CONTEXT_MARKER),
        "claude's session was not handed the team's guidance: {context:?}"
    );
    // Claude reads the project's own CLAUDE.md where it runs.
    let cwd = PathBuf::from(w.recorded(&harp, "cwd").trim());
    let notes = files::read(cwd.join("CLAUDE.md")).unwrap_or_default();
    assert_eq!(
        notes,
        ALICES_NOTES,
        "claude did not run where Alice's notes are ({})",
        cwd.display()
    );
}

#[then("Alice's session offers the team's command, MCP server and hook")]
fn alice_offered(w: &mut World) {
    let harp = w.session_on("claude-code");
    let home = w.config_dir(&harp);
    // Commands: the config dir's user commands.
    let commands = World::markdown_below(&home.join("commands"));
    assert!(
        commands.iter().any(|c| c.contains(COMMAND_MARKER)),
        "claude's config dir holds no command with the team's body: {commands:?}"
    );
    // MCP: the servers of the file claude's command line names.
    let mcp = w
        .arg_after(&harp, "--mcp-config")
        .expect("claude was handed an MCP config");
    let state = json_file(&PathBuf::from(mcp));
    let servers = state["mcpServers"].as_object().cloned().unwrap_or_default();
    assert!(
        servers
            .values()
            .any(|s| s["command"].as_str() == Some(MCP_COMMAND)),
        "claude's config dir registers no team MCP server: {state}"
    );
    // Hooks: the settings file claude's command line names.
    let settings = w
        .arg_after(&harp, "--settings")
        .expect("claude was handed a settings file");
    let settings = json_file(&PathBuf::from(settings));
    let starts = settings["hooks"]["SessionStart"]
        .as_array()
        .cloned()
        .unwrap_or_default();
    let commands: Vec<String> = starts
        .iter()
        .flat_map(|g| g["hooks"].as_array().cloned().unwrap_or_default())
        .map(|h| {
            let mut words = vec![h["command"].as_str().unwrap_or_default().to_string()];
            words.extend(
                h["args"]
                    .as_array()
                    .cloned()
                    .unwrap_or_default()
                    .iter()
                    .filter_map(|a| a.as_str().map(str::to_string)),
            );
            words.join(" ")
        })
        .collect();
    assert!(
        commands.iter().any(|c| c.contains(HOOK_MARKER)),
        "claude's session runs no team hook at its start: {commands:?}"
    );
}

#[then("Bob's session starts with the same team guidance")]
fn bob_guided(w: &mut World) {
    w.ran().succeeds();
    let harp = w.session_on("opencode");
    let context = w.opencode_context(&harp);
    assert!(
        context.contains(CONTEXT_MARKER),
        "opencode's session was not handed the team's guidance: {context:?}"
    );
    let alice = w.claude_context(&w.session_on("claude-code"));
    assert!(
        context.contains(alice.trim()),
        "opencode's guidance is not claude's: {context:?} vs {alice:?}"
    );
}

#[then("Bob's session offers the team's command, MCP server and hook")]
fn bob_offered(w: &mut World) {
    let harp = w.session_on("opencode");
    let home = w.config_dir(&harp);
    let commands = World::markdown_below(&home.join("commands"));
    assert!(
        commands.iter().any(|c| c.contains(COMMAND_MARKER)),
        "opencode's config dir holds no command with the team's body: {commands:?}"
    );
    let config = json_file(&home.join("opencode.json"));
    let servers = config["mcp"].as_object().cloned().unwrap_or_default();
    assert!(
        servers.values().any(|s| {
            s["command"]
                .as_array()
                .and_then(|c| c.first())
                .and_then(Value::as_str)
                == Some(MCP_COMMAND)
        }),
        "opencode's config registers no team MCP server: {config}"
    );
    assert_eq!(
        w.recorded(&harp, "plugins-code").trim(),
        "0",
        "opencode's plugins failed: {}",
        w.recorded(&harp, "plugins-out")
    );
    let log = files::read(home.join(HOOK_OUT)).unwrap_or_default();
    assert!(
        log.contains(HOOK_MARKER),
        "opencode's session ran no team hook at its start: {log:?} ({})",
        w.recorded(&harp, "plugins-out")
    );
}

#[then("the project holds nothing either session wrote, and Alice's notes are as she wrote them")]
fn project_untouched(w: &mut World) {
    assert!(
        w.engines.wrote.is_empty(),
        "the sessions wrote into the project: {:?}",
        w.engines.wrote
    );
    assert_eq!(
        w.common.project.read("CLAUDE.md").as_deref(),
        Some(ALICES_NOTES)
    );
}

// ------------------------------------------------------------- j002000

#[given("the team's agent \"dev\" runs on claude-code, and the team is moving to opencode")]
fn dev_on_claude(w: &mut World) {
    let project = crate::tests::library::Project::initialized();
    let root = ".ctxloom/content/bundles/v2/house";
    project.write(
        &format!("{root}/bundle.yaml"),
        "schema_version: 4\nversion: 1.0.0\n",
    );
    project.write(
        &format!("{root}/fragments/house-guidance.md"),
        J002000_MARKER,
    );
    project.write(
        &format!("{root}/hooks/session_end/h0.yaml"),
        "type: command\ncommand: echo j002000-session-end\n",
    );
    project.profile("default", "house");
    project.write(
        ".ctxloom/config.yaml",
        "schema_version: 9\nagents:\n  dev:\n    engine: claude-code\n    profiles:\n      - default\ndefault_agent: dev\n",
    );
    w.common.project = project;
    w.stage_engines();
}

/// The engine the project's config binds `dev` to.
fn dev_engine(w: &World) -> String {
    let config = w
        .common
        .project
        .read(".ctxloom/config.yaml")
        .expect("config.yaml");
    config
        .lines()
        .skip_while(|l| l.trim() != "dev:")
        .skip(1)
        .find_map(|l| {
            l.trim()
                .strip_prefix("engine:")
                .map(|v| v.trim().to_string())
        })
        .unwrap_or_else(|| panic!("config.yaml binds dev to no engine: {config}"))
}

#[then("ctxloom shows what the new engine cannot carry that the old one did")]
fn shows_losses(w: &mut World) {
    let ran = w.ran();
    ran.succeeds();
    let doc = w.json();
    assert_eq!(doc["applied"], Value::Bool(false), "{}", ran.said());
    let lost = doc["not_carried"].as_array().cloned().unwrap_or_default();
    assert!(
        lost.iter()
            .any(|l| l["surface"] == "hooks" && l["event"] == "session_end"),
        "the preview names no session-end hook lost: {}",
        ran.said()
    );
    assert!(
        doc["apply_command"]
            .as_str()
            .is_some_and(|a| a.contains("--engine opencode") && a.contains("--yes")),
        "the preview names no command that applies it: {}",
        ran.said()
    );
}

#[then("the team's agent is still on the old engine")]
fn still_old(w: &mut World) {
    assert_eq!(dev_engine(w), "claude-code");
    w.common
        .run_lines("ctxloom agent show dev --format json", "");
    w.ran().succeeds();
    assert_eq!(
        // OD-40: `agent show` is the `Agent` resource itself.
        w.json()["engine"],
        "claude-code",
        "{}",
        w.ran().said()
    );
}

#[given("the team's last session on the old engine started with the team's guidance")]
fn old_session(w: &mut World) {
    w.run_each("ctxloom run --agent dev");
    w.ran().succeeds();
    let harp = w.session_on("claude-code");
    let context = w.claude_context(&harp);
    assert!(context.contains(J002000_MARKER), "{context:?}");
    w.engines.old_guidance = Some(context);
}

#[then("the session runs on the new engine")]
fn runs_on_new(w: &mut World) {
    w.ran().succeeds();
    assert_eq!(dev_engine(w), "opencode");
    let harp = w.session_on("opencode");
    w.common
        .run_lines(&format!("ctxloom session show {harp} --format json"), "");
    w.ran().succeeds();
    assert!(
        w.ran().stdout.contains("opencode"),
        "the session is not recorded on opencode: {}",
        w.ran().said()
    );
}

#[then("it starts with the same guidance the old engine's session had, unchanged")]
fn same_guidance(w: &mut World) {
    let old = w
        .engines
        .old_guidance
        .clone()
        .expect("the old session's guidance");
    let new = w.opencode_context(&w.session_on("opencode"));
    assert_eq!(
        new.trim(),
        old.trim(),
        "the guidance changed across the switch"
    );
}

#[then("the project holds nothing the session wrote")]
fn nothing_written(w: &mut World) {
    assert!(
        w.engines.wrote.is_empty(),
        "the session wrote into the project: {:?}",
        w.engines.wrote
    );
}

#[given("a session was recorded under the old engine before the switch")]
fn recorded_before(w: &mut World) {
    let ran = w.common.project.run(&[
        "test",
        "seed-session",
        J002000_HARP,
        "--transcript",
        "--lock",
        "free",
        "--started-ago",
        "7200",
        "--ended-ago",
        "3600",
    ]);
    assert_eq!(ran.code, 0, "seeding {J002000_HARP}: {}", ran.said());
}

#[then("the sessions recorded under the old engine are still listed after the switch")]
fn still_listed(w: &mut World) {
    w.ran().succeeds();
    assert_eq!(dev_engine(w), "opencode");
    w.common
        .run_lines("ctxloom session list --all --format json", "");
    w.ran().succeeds();
    // The daemon's listing: each session its resource, named
    // `projects/{project}/sessions/{harp}`.
    let doc = w.json();
    let rows = doc["sessions"].as_array().cloned().unwrap_or_default();
    let named = format!("/sessions/{J002000_HARP}");
    assert!(
        rows.iter()
            .any(|r| r["name"].as_str().is_some_and(|n| n.ends_with(&named))),
        "the session recorded before the switch is no longer listed: {}",
        w.ran().said()
    );
}

#[when("a teammate later checks the team's agent and the project's health:")]
fn teammate_checks(w: &mut World, step: &Step) {
    w.ran().succeeds();
    w.run_each(&doc(step));
}

#[then("each of them names the session-end hook the new engine cannot carry")]
fn each_names_loss(w: &mut World) {
    let said = &w.engines.said;
    assert_eq!(said.len(), 2, "the agent and the doctor both answered");
    let agent = crate::tests::parse_one(&said[0].stdout);
    assert_eq!(said[0].code, 0, "{}", said[0].said());
    let lost = agent["not_carried"].as_array().cloned().unwrap_or_default();
    assert!(
        lost.iter().any(|l| l["surface"] == "hooks"
            && l["event"] == "session_end"
            && l["engine"] == "opencode"),
        "`agent show` names no session-end hook opencode cannot carry: {}",
        said[0].said()
    );
    let doctor = crate::tests::parse_one(&said[1].stdout);
    let checks = doctor["checks"].as_array().cloned().unwrap_or_default();
    assert!(
        checks.iter().any(|c| {
            let detail = c["detail"].as_str().unwrap_or_default();
            c["status"] == "warn" && detail.contains("session_end") && detail.contains("opencode")
        }),
        "doctor names no session-end hook opencode cannot carry: {}",
        said[1].said()
    );
}
