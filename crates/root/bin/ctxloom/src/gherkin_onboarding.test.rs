//! The steps of Go's `steps_j000800_onboarding.go` (and the Bob half of
//! `steps_j001800_guardrails.go`), as a child of the surfaces World: Carol's
//! team project and Bob's clone of it are `j000700`'s (a bare origin made in
//! the scenario's temporary directory), and every remote a scenario draws on
//! is a local bare repository too.
//!
//! "Bob starts a session:" runs the commands it gives in Bob's checkout, as
//! he types them (`ctxloom run`): his assistant is a stub `claude` that
//! records the session home it was given (its `CLAUDE_CONFIG_DIR`) and
//! exits, and what his assistant "receives" is the context file ctxloom
//! wrote there (`ctxloom-context.md`). On opencode (`run --engine
//! opencode`) it is a stub `opencode` that records its session home (its
//! `OPENCODE_CONFIG_DIR`), and what it receives is that home's `AGENTS.md`
//! context section, opencode's own native surface. His `PATH` holds no shipped
//! companion of this machine's (`files::path_without_shipped_companions`):
//! a companion "on his machine" is the stub companion, put first on it.
//! The Go-ported "Bob starts a session" (no docstring, j001800's) is still
//! `materialize default` into `out` for claude-code.

use std::path::{Path, PathBuf};

use cucumber::gherkin::Step;
use cucumber::{given, then, when};

use super::super::deps::git_in;
use super::super::gherkin_common::{doc, CommonWorld};
use super::super::Ran;
use super::{
    j000700_commit, j000700_project, j000700_pull, World, J000700_BUNDLE, J000700_PROFILE,
};
use crate::tests::companion_stub::Stub;
use crate::tests::files;

const TEAM_MARKER: &str = "J000800-TEAM-STANDARDIZED-CONTEXT-MARKER";
const PINNED_MARKER: &str = "J000800-PINNED-UPSTREAM-VERSION-MARKER";
const NEWER_MARKER: &str = "J000800-NEWER-UPSTREAM-VERSION-MARKER";
const COMPANY_MARKER: &str = "J000800-TRENT-COMPANY-CONTENT-MARKER";
const UPSTREAM_BUNDLE: &str = "upstream-bundle";
const COMPANY_BUNDLE: &str = "secure-standards";

/// What the onboarding scenarios keep between steps.
#[derive(Default)]
pub(super) struct State {
    /// The upstream's bare repository, which a later step advances.
    upstream: Option<PathBuf>,
    /// The commit the team's lockfile froze when Carol held the upstream
    /// bundle: what "the versions the team pinned" means.
    pinned_sha: String,
    /// The loadout the team's reprise guidance is, once a step says there is
    /// one.
    reprise_loadout: Option<String>,
    /// The last command Bob ran.
    last: Option<Ran>,
    /// The engine and target of the last `starts a session on`.
    session_on: Option<(String, String)>,
    /// The directory of the stub companion's program, when one is on his
    /// machine.
    companion_bin: Option<PathBuf>,
    /// Whether a session was started through `ctxloom run`.
    ran_session: bool,
}

/// Bob's assistant: records the session home it was given, and exits.
const CLAUDE_STUB: &str = r#"printf '%s\n' "${CLAUDE_CONFIG_DIR:-}" > "$(dirname "$0")/claude-home"
[ -t 0 ] || cat > /dev/null
exit 0
"#;

/// The context file ctxloom writes in a claude session home.
const CONTEXT_FILE: &str = "ctxloom-context.md";

/// Bob's assistant on opencode: records the session home it was given, and
/// exits.
const OPENCODE_STUB: &str = r#"printf '%s\n' "${OPENCODE_CONFIG_DIR:-}" > "$(dirname "$0")/opencode-home"
[ -t 0 ] || cat > /dev/null
exit 0
"#;

/// The context file opencode reads in its session home (its config dir).
const OPENCODE_CONTEXT_FILE: &str = "AGENTS.md";

impl World {
    /// `ctxloom args` in Bob's checkout (on the `PATH` he has).
    fn bob_runs(&mut self, args: &[&str]) {
        let dir = self.bob_dir().to_path_buf();
        self.onboarding.last = Some(self.common.project.run_in(&dir, args));
    }

    /// Where Bob's assistant stub is.
    fn bob_bin(&self) -> PathBuf {
        self.scratch().join("bob-bin")
    }

    /// Each `ctxloom ...` line of `lines` in Bob's checkout, at his
    /// terminal (stood in), with his assistant and his token, on a `PATH`
    /// of his own; the last is the one the next step sees.
    fn bob_types(&mut self, lines: &str) {
        let bin = self.bob_bin();
        if !files::exists(bin.join("claude")) {
            files::put_program(bin.join("claude"), CLAUDE_STUB);
        }
        if !files::exists(bin.join("opencode")) {
            files::put_program(bin.join("opencode"), OPENCODE_STUB);
        }
        let mut first = vec![bin];
        first.extend(self.onboarding.companion_bin.clone());
        let path = files::path_without_shipped_companions(&first);
        let env = [
            ("PATH", path.as_str()),
            ("CTXLOOM_TEST_TERMINAL", "1"),
            (
                "CLAUDE_CODE_OAUTH_TOKEN",
                "sk-ant-oat01-acceptance-fixture-not-real",
            ), // gitleaks:allow
        ];
        let dir = self.bob_dir().to_path_buf();
        for line in lines.lines().map(str::trim).filter(|l| !l.is_empty()) {
            let words = super::super::gherkin_common::split(line);
            assert_eq!(words.first().map(String::as_str), Some("ctxloom"), "{line}");
            let args: Vec<&str> = words[1..].iter().map(String::as_str).collect();
            if args.first() == Some(&"run") {
                for recorded in ["claude-home", "opencode-home"] {
                    let home = self.bob_bin().join(recorded);
                    if files::exists(&home) {
                        files::remove(&home);
                    }
                }
                self.onboarding.ran_session = true;
            }
            let ran = self.common.project.run_in_with(&dir, &env, &args);
            let failed = ran.code != 0;
            self.onboarding.last = Some(ran);
            if failed {
                break;
            }
        }
    }

    /// The context Bob's last session was handed: the context file in the
    /// session home his assistant (on claude) was given.
    fn bob_session_context(&self) -> String {
        self.bob_session_context_in("claude-home", CONTEXT_FILE)
    }

    /// The context file `file` in the session home Bob's assistant recorded
    /// in `recorded` (beside it on his `PATH`).
    fn bob_session_context_in(&self, recorded: &str, file: &str) -> String {
        let said = self
            .onboarding
            .last
            .as_ref()
            .map(Ran::said)
            .unwrap_or_default();
        let home = files::read(self.bob_bin().join(recorded))
            .unwrap_or_else(|_| panic!("Bob's assistant was never launched: {said}"));
        let home = PathBuf::from(home.trim());
        files::read(home.join(file)).unwrap_or_else(|_| {
            panic!(
                "Bob's session home {} holds no {file}: {said}",
                home.display()
            )
        })
    }

    /// Go's `bobMaterialized`: his default profile into `out`, again, and the
    /// `CLAUDE.md` it wrote.
    fn bob_materialized(&mut self) -> String {
        self.bob_runs(&[
            "materialize",
            J000700_PROFILE,
            "--target",
            "out",
            "--backend",
            "claude-code",
        ]);
        let said = self.onboarding.last.as_ref().expect("ran").said();
        read_in(self.bob_dir(), "out/CLAUDE.md")
            .unwrap_or_else(|| panic!("Bob's materialize wrote no out/CLAUDE.md: {said}"))
    }

    fn bob_has(&mut self, marker: &str) {
        let body = if self.onboarding.ran_session {
            self.bob_session_context()
        } else {
            self.bob_materialized()
        };
        assert!(
            body.contains(marker),
            "the materialized context for Bob lacks {marker:?}: {body}"
        );
    }

    fn bob_lacks(&mut self, marker: &str) {
        let body = if self.onboarding.ran_session {
            self.bob_session_context()
        } else {
            self.bob_materialized()
        };
        assert!(
            !body.contains(marker),
            "the materialized context for Bob has {marker:?} and should not: {body}"
        );
    }

    /// Go's `SeedTreeRemote`: a bare repository whose `main` holds the
    /// bundle `bundle`, with one fragment, `guidance`, carrying `marker`
    /// (and no description: that is a fragment's premise, and would withhold
    /// it from an ordinary assembly).
    fn seed_remote(&self, dir: &str, bundle: &str, marker: &str) -> PathBuf {
        let root = self.scratch().join(dir);
        let (bare, work) = (root.join("remote.git"), root.join("work"));
        files::mkdirs(&root);
        git_in(&root, &["init", "-q", "--bare", "-b", "main", "remote.git"]);
        git_in(&root, &["init", "-q", "-b", "main", "work"]);
        let tree = format!(".ctxloom/content/bundles/v2/{bundle}");
        files::put(
            work.join(format!("{tree}/bundle.yaml")),
            "schema_version: 4\nversion: 1.0.0\n",
        );
        files::put(
            work.join(format!("{tree}/fragments/guidance.md")),
            format!("{marker}\n"),
        );
        git_in(&work, &["add", "-A"]);
        git_in(&work, &["commit", "-q", "-m", "seed"]);
        git_in(
            &work,
            &["remote", "add", "origin", bare.to_str().expect("utf-8")],
        );
        git_in(&work, &["push", "-q", "origin", "main"]);
        bare
    }

    /// Go's `AdvanceTreeRemote`: a new commit that rewrites the fragment.
    fn advance_remote(&self, bare: &Path, bundle: &str, marker: &str) {
        let work = self.scratch().join("advance-work");
        git_in(
            &self.scratch(),
            &[
                "clone",
                "-q",
                bare.to_str().expect("utf-8"),
                work.to_str().expect("utf-8"),
            ],
        );
        files::put(
            work.join(format!(
                ".ctxloom/content/bundles/v2/{bundle}/fragments/guidance.md"
            )),
            format!("{marker}\n"),
        );
        git_in(&work, &["add", "-A"]);
        git_in(&work, &["commit", "-q", "-m", "advance"]);
        git_in(&work, &["push", "-q", "origin", "main"]);
    }

    /// Go's `remote create`, then the profile naming the bundle.
    fn draw_on(&mut self, remote: &str, bare: &Path, bundle: &str) {
        let url = files::file_url(bare);
        self.common.run_lines(
            &format!(
                "ctxloom remote create {remote} {url} --forge git\nctxloom profile edit {J000700_PROFILE} --add-bundle {remote}/{bundle}"
            ),
            "",
        );
        self.ran().succeeds();
    }
}

fn read_in(dir: &Path, rel: &str) -> Option<String> {
    files::read(dir.join(rel)).ok()
}

/// Go's `j000800LockedSHA`: the commit the lockfile records for `bundle`.
/// The lockfile's entries are keyed by reference, one level in.
fn locked_sha(raw: &str, bundle: &str) -> String {
    let mut inside = false;
    for line in raw.lines() {
        let indent = line.len() - line.trim_start().len();
        if indent == 2 {
            inside = line.trim_end().ends_with(':') && line.contains(bundle);
        } else if inside && indent > 2 {
            if let Some(sha) = line.trim().strip_prefix("sha:") {
                return sha.trim().to_string();
            }
        }
    }
    panic!("the lockfile has no sha for {bundle:?}:\n{raw}");
}

// ------------------------------------------------------------ Background

/// Carol's project, with the team's own first-party fragment in its bundle,
/// committed and pushed.
#[given("the team's project carries the context Carol has standardized on")]
fn team_context(w: &mut World) {
    j000700_project(w);
    w.common.project.write(
        &format!(".ctxloom/content/bundles/v2/{J000700_BUNDLE}/fragments/guidance.md"),
        &format!("{TEAM_MARKER}\n"),
    );
    // The team's default agent, on claude-code with the team's profile: what
    // `ctxloom run` starts for Bob.
    w.common.run_lines(
        &format!(
            "ctxloom agent create default --engine claude-code --profiles {J000700_PROFILE}\nctxloom agent default default"
        ),
        "",
    );
    w.ran().succeeds();
    // A later `deps pull` populates a git cache under .ctxloom/cache/: kept
    // out of the team's history.
    let ignore = w.common.project.read(".gitignore").unwrap_or_default();
    w.common
        .project
        .write(".gitignore", &format!("{ignore}.ctxloom/cache/\n"));
    j000700_commit(w, "author team standards");
}

#[given("Bob has just joined and has never used ctxloom before")]
fn joined(_w: &mut World) {
    // Narrative only: Bob's clone does not exist until he clones.
}

// ---------------------------------------------------------- the shared steps

#[given("Bob clones the project")]
#[when("Bob clones the project")]
fn clones(w: &mut World) {
    j000700_pull(w);
}

#[when("Bob fetches the context the project draws on")]
fn fetches(w: &mut World) {
    w.bob_runs(&["deps", "pull"]);
    w.onboarding.last.as_ref().expect("ran").succeeds();
}

#[when("Bob fetches the context the project draws on, reinstalling every reference")]
fn fetches_forced(w: &mut World) {
    w.bob_runs(&["deps", "pull", "--force"]);
    w.onboarding.last.as_ref().expect("ran").succeeds();
}

#[when("Bob starts a session")]
fn starts(w: &mut World) {
    w.bob_runs(&[
        "materialize",
        J000700_PROFILE,
        "--target",
        "out",
        "--backend",
        "claude-code",
    ]);
}

#[when("Bob fetches the context the project draws on:")]
fn fetches_typed(w: &mut World, step: &Step) {
    w.bob_types(&doc(step));
    w.onboarding.last.as_ref().expect("ran").succeeds();
}

#[when("Bob starts a session:")]
fn starts_typed(w: &mut World, step: &Step) {
    w.bob_types(&doc(step));
}

#[when(regex = r"^Bob starts a session on (\S+):$")]
fn starts_on_typed(w: &mut World, engine: String, step: &Step) {
    w.bob_types(&doc(step));
    w.onboarding.session_on = Some((engine, String::new()));
}

#[then(
    regex = r"^Bob (is warned|is not warned) that the reprise-dependent guidance was left out because reprise is not on his machine$"
)]
fn bob_warned(w: &mut World, told: String) {
    let ran = w.onboarding.last.as_ref().expect("Bob ran a command");
    let said = ran.said();
    // The notice, as a run off a terminal says it (a json line) or on one.
    let code = "library-application-companion-not-on-path";
    let words = "its program reprise is not on PATH here, so the guidance it brings";
    if told == "is warned" {
        assert!(
            said.contains(code) && said.contains(words) && said.contains("is left out"),
            "Bob was not warned: {said}"
        );
        assert!(said.contains("reprise"), "{said}");
    } else {
        assert!(!said.contains(code), "Bob was warned: {said}");
    }
}

#[then("his assistant receives the team's standardized context")]
fn receives_team(w: &mut World) {
    w.bob_has(TEAM_MARKER);
}

#[then("his assistant still receives the team's own context, because the project is first-party")]
fn still_receives_team(w: &mut World) {
    w.bob_has(TEAM_MARKER);
}

#[then("nothing fails because of the companion's presence or absence")]
fn nothing_fails(w: &mut World) {
    let ran = w.onboarding.last.as_ref().expect("Bob ran a command");
    assert_eq!(
        ran.code,
        0,
        "Bob's session start should succeed whether or not a companion is installed: {}",
        ran.said()
    );
}

// ------------------------------------------------ the engine outline

#[when(regex = r"^Bob starts a session on ([^\s:]+)$")]
fn starts_on(w: &mut World, engine: String) {
    let target = format!("out-{engine}");
    w.bob_runs(&[
        "materialize",
        J000700_PROFILE,
        "--target",
        &target,
        "--backend",
        &engine,
    ]);
    w.onboarding.session_on = Some((engine, target));
}

#[then(
    regex = r"^his assistant receives the team's standardized context in (\S+)'s own native surface$"
)]
fn receives_native(w: &mut World, engine: String) {
    let (_, target) = w
        .onboarding
        .session_on
        .clone()
        .expect("Bob started a session on an engine");
    if w.onboarding.ran_session {
        // Where each engine reads its context in a session home: claude
        // the context file its command line names, opencode its config
        // dir's `AGENTS.md`.
        let body = match engine.as_str() {
            "claude-code" => w.bob_session_context(),
            "opencode" => w.bob_session_context_in("opencode-home", OPENCODE_CONTEXT_FILE),
            other => panic!("no session home context known for {other}"),
        };
        assert!(
            body.contains(TEAM_MARKER),
            "{engine}'s session home context lacks the team's marker: {body}"
        );
        return;
    }
    let file = match engine.as_str() {
        "claude-code" => "CLAUDE.md",
        "mock" => "MOCK_CONTEXT.md",
        other => panic!("no native context file known for {other}"),
    };
    let said = w.onboarding.last.as_ref().expect("ran").said();
    let body = read_in(w.bob_dir(), &format!("{target}/{file}"))
        .unwrap_or_else(|| panic!("{engine}: Bob's {target}/{file} was not written: {said}"));
    assert!(
        body.contains(TEAM_MARKER),
        "the materialized {engine} context for Bob lacks the team's marker: {body}"
    );
}

// ------------------------------------------------------ pinned, not latest

#[given("the project pins the versions of the context it draws from elsewhere")]
fn pins(w: &mut World) {
    let bare = w.seed_remote("upstream", UPSTREAM_BUNDLE, PINNED_MARKER);
    w.draw_on("upstream", &bare, UPSTREAM_BUNDLE);
    w.onboarding.upstream = Some(bare);
    w.common.run_lines(
        &format!("ctxloom deps pull\nctxloom deps hold upstream/{UPSTREAM_BUNDLE}"),
        "",
    );
    w.ran().succeeds();
    // The commit the hold froze: the concrete claim Bob's lockfile keeps or
    // breaks, so the scenario cannot pass by a pull that never resolved
    // anything.
    let raw = w
        .common
        .project
        .read(".ctxloom/lock.yaml")
        .expect("the team's lockfile");
    w.onboarding.pinned_sha = locked_sha(&raw, UPSTREAM_BUNDLE);
    j000700_commit(w, "pin upstream dependency");
}

#[given("an upstream has since published a newer version")]
fn newer(w: &mut World) {
    let bare = w.onboarding.upstream.clone().expect("an upstream");
    w.advance_remote(&bare, UPSTREAM_BUNDLE, NEWER_MARKER);
}

#[then("he receives the pinned versions, the same ones the rest of the team has")]
fn receives_pinned(w: &mut World) {
    assert!(
        !w.onboarding.pinned_sha.is_empty(),
        "the pinning step must run first"
    );
    let raw = read_in(w.bob_dir(), ".ctxloom/lock.yaml").expect("Bob's lockfile");
    let got = locked_sha(&raw, UPSTREAM_BUNDLE);
    assert_eq!(
        got, w.onboarding.pinned_sha,
        "the pull moved Bob's upstream bundle off the team's pin; his lockfile:\n{raw}"
    );
    w.bob_has(PINNED_MARKER);
}

#[then("he does not receive the newer upstream version")]
fn lacks_newer(w: &mut World) {
    w.bob_lacks(NEWER_MARKER);
}

// ------------------------------------------- referenced content survives

#[given("the project references a bundle published by Trent's company")]
fn references(w: &mut World) {
    let bare = w.seed_remote("company", COMPANY_BUNDLE, COMPANY_MARKER);
    w.draw_on("trent-company", &bare, COMPANY_BUNDLE);
    // Not pulled: the reference is committed unresolved, and Bob's own fetch
    // is the first pull, as a reference genuinely arrives at a team.
    j000700_commit(w, "reference trent's company bundle");
}

#[then("his assistant receives the company's content")]
fn receives_company(w: &mut World) {
    w.bob_has(COMPANY_MARKER);
}

// ------------------------------------------------ a companion Bob may not have

const REPRISE_MARKER: &str = "J000800-REPRISE-DEPENDENT-GUIDANCE-MARKER";

#[given(r#"the team's context includes guidance for the "reprise" companion"#)]
fn team_has_reprise_guidance(w: &mut World) {
    w.onboarding.reprise_loadout = Some(format!(
        "run:\n  version: 1.0.0\n  fragments:\n    guidance:\n      content: {REPRISE_MARKER}\n"
    ));
}

/// A companion is registered in the home with the command that runs it.
/// Installed is the stub companion (the compiled `ctxloom-companion-stub`,
/// asked for the team's guidance); not installed is a command whose program is
/// not there, registered all the same, so "nothing fails" is held with a
/// registration that cannot be run.
#[given(regex = r#"^the "(reprise|ltk)" companion is (installed|not installed) on Bob's machine$"#)]
fn companion_presence(w: &mut World, name: String, presence: String) {
    let scratch = w.scratch();
    if presence == "installed" {
        let loadout = w
            .onboarding
            .reprise_loadout
            .clone()
            .unwrap_or_else(|| panic!("no loadout was given for the {name:?} companion"));
        let stub = Stub::install(&scratch.join("companions"), &name, &loadout);
        w.common.project.put_on_path(stub.bin());
        w.onboarding.companion_bin = Some(stub.bin().to_path_buf());
        let args = stub.add_args(&name);
        let args: Vec<&str> = args.iter().map(String::as_str).collect();
        let ran = w.common.project.run_with(&[], &args);
        ran.succeeds();
        return;
    }
    let held = w
        .common
        .project
        .read_home(".ctxloom/config.yaml")
        .unwrap_or_else(|| "schema_version: 9\n".to_string());
    let entry = format!("  {name}: {{}}\n");
    let config = if held.contains("\ncompanions:\n") {
        held.replacen("\ncompanions:\n", &format!("\ncompanions:\n{entry}"), 1)
    } else {
        format!("{held}companions:\n{entry}")
    };
    w.common.project.write_home(".ctxloom/config.yaml", &config);
}

#[then("his assistant receives the team's context that does not depend on reprise")]
fn receives_independent_context(w: &mut World) {
    w.bob_has(TEAM_MARKER);
}

#[then(regex = r"^the reprise-dependent guidance (reaches|does not reach) his assistant$")]
fn reprise_guidance(w: &mut World, reaches: String) {
    if reaches == "reaches" {
        w.bob_has(REPRISE_MARKER);
    } else {
        w.bob_lacks(REPRISE_MARKER);
    }
}

// ------------------------------------------------ j001800: Alice's guardrails
//
// Alice's companions are the stub companion, on her `PATH`; she adds them
// and starts her session as she types it (`ctxloom run`, the terminal stood
// in). Her assistant is a stub `claude` ([`ALICE_CLAUDE`]) that records the
// session home it was given and its arguments, then does what an assistant
// told "use the task runner" does out of habit: it reaches for a raw shell
// command, and, as claude does, first runs every `PreToolUse` hook of the
// settings file its command line names (`--settings`, the session's own
// `ctxloom-hooks.json`) whose matcher takes its `Bash` tool, with the call
// on stdin ([`FIRE`], under node), running the command only when no hook
// blocked it (exit 2). The guard ltk's loadout declares is [`GUARD_COMMAND`],
// a program on her `PATH` that records what it was asked. Both write one
// log, so its order is the order things happened.

const TASK_RUNNER_MARKER: &str = "J001800-TASK-RUNNER-GUIDANCE-MARKER";
const REUSE_MARKER: &str = "J001800-REUSE-BEFORE-YOU-WRITE-MARKER";
/// What the stub's `pre_tool` hooks run, in place of `ltk evaluate`.
const GUARD_COMMAND: &str = "stub-guard evaluate";
/// The raw shell command her assistant reaches for.
const RAW_COMMAND: &str = "make test";

/// Alice's assistant: records its session home and arguments, then
/// reaches for a raw shell command through its hooks.
const ALICE_CLAUDE: &str = r#"here="$(dirname "$0")"
printf '%s\n' "${CLAUDE_CONFIG_DIR:-}" > "$here/claude-home"
: > "$here/claude-args"
settings=""
prev=""
for a in "$@"; do
  printf '%s\n' "$a" >> "$here/claude-args"
  [ "$prev" = "--settings" ] && settings="$a"
  prev="$a"
done
if [ -n "$settings" ]; then
  node "$here/fire.mjs" "$settings" "$here/guard.log" "make test" > "$here/fire.out" 2>&1
fi
[ -t 0 ] || cat > /dev/null
exit 0
"#;

/// What claude does before a tool call: each `PreToolUse` hook whose
/// matcher takes the tool, run with the call on stdin; the call runs only
/// when none exited 2.
const FIRE: &str = r#"import { readFileSync, appendFileSync } from "node:fs"
import { spawnSync } from "node:child_process"
const [settingsFile, log, command] = process.argv.slice(2)
const settings = JSON.parse(readFileSync(settingsFile, "utf8"))
const payload = JSON.stringify({ session_id: "j001800", hook_event_name: "PreToolUse", cwd: process.cwd(), tool_name: "Bash", tool_input: { command } })
let blocked = false
for (const group of (settings.hooks && settings.hooks.PreToolUse) || []) {
  const matcher = group.matcher || ""
  if (matcher && !new RegExp("^(?:" + matcher + ")$").test("Bash")) continue
  for (const hook of group.hooks || []) {
    if (hook.type !== "command") continue
    const ran = spawnSync(hook.command, { shell: true, input: payload, env: process.env })
    if (ran.status === 2) blocked = true
  }
}
if (!blocked) appendFileSync(log, "ran " + command + "\n")
"#;

/// The guard: records the call it was asked about, and lets it run.
const GUARD: &str = r#"printf 'asked %s\n' "$(cat)" >> "$(dirname "$0")/guard.log"
exit 0
"#;

/// The tools of the shell and file-edit classes in claude, written out here
/// rather than derived from the loadout (Go's `j001800LtkGuardedTools`): a
/// superset check, so a narrowed matcher fails it though the loadout and the
/// delivery agree.
const GUARDED_TOOLS: [&str; 6] = [
    "Bash",
    "PowerShell",
    "Edit",
    "Write",
    "MultiEdit",
    "NotebookEdit",
];

/// The task-runner companion's loadout: guidance, and a guard on each tool
/// class it declares.
fn guard_loadout() -> String {
    format!(
        "run:\n  version: 1.0.0\n  hooks:\n    pre_tool:\n      - command: {GUARD_COMMAND}\n        tool: shell\n        type: command\n      - command: {GUARD_COMMAND}\n        tool: file_edit\n        type: command\n  fragments:\n    task-runner:\n      content: {TASK_RUNNER_MARKER}\n"
    )
}

/// The tool classes the guard loadout declares, one hook each.
const DECLARED_CLASSES: usize = 2;

/// Paths ctxloom's engines read in a project, none of which a session
/// started by ctxloom may leave there.
const ENGINE_FILES: [&str; 7] = [
    ".claude",
    "CLAUDE.md",
    ".mcp.json",
    "AGENTS.md",
    "opencode.json",
    ".opencode",
    "ctxloom-hooks.json",
];

impl World {
    /// Where Alice's assistant, its hook driver and her guard are.
    fn alice_bin(&self) -> PathBuf {
        self.scratch().join("alice-bin")
    }

    /// What Alice's assistant recorded, by file name.
    fn alice_recorded(&self, name: &str) -> String {
        let said = self
            .onboarding
            .last
            .as_ref()
            .map(Ran::said)
            .unwrap_or_default();
        files::read(self.alice_bin().join(name))
            .unwrap_or_else(|_| panic!("Alice's assistant recorded no {name}: {said}"))
    }

    /// The session home her assistant was given.
    fn alice_home(&self) -> PathBuf {
        PathBuf::from(self.alice_recorded("claude-home").trim())
    }

    /// The settings file her assistant's command line named.
    fn alice_settings(&self) -> serde_json::Value {
        let args = self.alice_recorded("claude-args");
        let lines: Vec<&str> = args.lines().collect();
        let file = lines
            .windows(2)
            .find(|w| w[0] == "--settings")
            .map(|w| w[1])
            .unwrap_or_else(|| panic!("her assistant was named no --settings: {args}"));
        let raw = files::read(file).unwrap_or_else(|e| panic!("{file}: {e}"));
        serde_json::from_str(&raw).unwrap_or_else(|e| panic!("{file}: {e}: {raw}"))
    }

    /// The engine files of ctxloom's in Alice's project.
    fn alice_engine_files(&self) -> Vec<String> {
        ENGINE_FILES
            .iter()
            .filter(|rel| self.common.project.exists(rel))
            .map(ToString::to_string)
            .collect()
    }
}

#[given("Alice's project exists")]
fn alice_project(w: &mut World) {
    w.common.project = super::Project::initialized();
    super::j000200_scaffold(w, "claude-code");
}

/// Both companions are the stub, its program on her `PATH` under each
/// one's name, each answering a generic loadout (a fragment with a marker;
/// for "ltk" also a `pre_tool` hook on the `shell` and the `file_edit` tool
/// class). The stub stands in for ltk and reprise, so neither's own wording
/// or command is asserted: only ctxloom's half, the delivery. Her
/// assistant and her guard are on her `PATH` too. Nothing is registered:
/// she adds them herself.
#[given("ltk and reprise are installed on Alice's machine")]
fn alice_companions(w: &mut World) {
    let scratch = w.scratch();
    let loadouts = [
        ("ltk", guard_loadout()),
        (
            "reprise",
            format!(
                "run:\n  version: 1.0.0\n  fragments:\n    reuse:\n      content: {REUSE_MARKER}\n"
            ),
        ),
    ];
    for (name, loadout) in loadouts {
        let stub = Stub::install(&scratch.join("companions"), name, &loadout);
        w.common.project.put_on_path(stub.bin());
    }
    let bin = w.alice_bin();
    files::put_program(bin.join("claude"), ALICE_CLAUDE);
    files::put_program(bin.join("stub-guard"), GUARD);
    files::put(bin.join("fire.mjs"), FIRE);
    w.common.project.put_on_path(&bin);
}

/// "Alice adds both companions and starts a session:" (the surfaces
/// World's `Alice ...:` step hands it here): each command as she types it,
/// in her project, at her terminal (stood in), with a token; the engine
/// files of her project are looked at before her session starts.
pub(super) fn alice_starts(w: &mut World, step: &Step) -> bool {
    if step.value != "Alice adds both companions and starts a session:" {
        return false;
    }
    let env = [
        ("CTXLOOM_TEST_TERMINAL", "1"),
        (
            "CLAUDE_CODE_OAUTH_TOKEN",
            "sk-ant-oat01-acceptance-fixture-not-real",
        ), // gitleaks:allow
    ];
    let dir = w.common.project.dir().to_path_buf();
    for line in doc(step).lines().map(str::trim).filter(|l| !l.is_empty()) {
        let words = super::super::gherkin_common::split(line);
        assert_eq!(words.first().map(String::as_str), Some("ctxloom"), "{line}");
        let args: Vec<&str> = words[1..].iter().map(String::as_str).collect();
        if args.first() == Some(&"run") {
            let before = w.alice_engine_files();
            assert!(
                before.is_empty(),
                "her project held engine files of ctxloom's before her session: {before:?}"
            );
        }
        let ran = w.common.project.run_in_with(&dir, &env, &args);
        let failed = ran.code != 0;
        w.onboarding.last = Some(ran);
        assert!(
            !failed,
            "{line}: {}",
            w.onboarding.last.as_ref().expect("ran").said()
        );
    }
    true
}

/// The context her session was handed: the context file in its home.
fn alice_context(w: &mut World) -> String {
    let home = w.alice_home();
    files::read(home.join(CONTEXT_FILE)).unwrap_or_else(|_| {
        panic!(
            "her session home {} holds no {CONTEXT_FILE}",
            home.display()
        )
    })
}

#[then("her assistant receives ltk's task-runner guidance")]
fn alice_has_task_runner(w: &mut World) {
    let body = alice_context(w);
    assert!(body.contains(TASK_RUNNER_MARKER), "{body}");
}

#[then("her assistant receives reprise's reuse-before-you-write guidance")]
fn alice_has_reuse(w: &mut World) {
    let body = alice_context(w);
    assert!(body.contains(REUSE_MARKER), "{body}");
}

/// Each declared tool class reaches the session's settings (the launch
/// carries it: `LaunchSurfaces`) as its own PreToolUse entry, whose matcher
/// covers the engine's tool names for it.
#[then("her assistant's engine is configured to run ltk on every shell command and file edit")]
fn alice_engine_guards(w: &mut World) {
    let settings = w.alice_settings();
    let entries = settings["hooks"]["PreToolUse"]
        .as_array()
        .unwrap_or_else(|| panic!("no PreToolUse hooks: {settings}"));
    let matchers: Vec<&str> = entries
        .iter()
        .filter(|entry| {
            entry["hooks"].as_array().is_some_and(|hooks| {
                hooks
                    .iter()
                    .any(|h| h["command"].as_str() == Some(GUARD_COMMAND))
            })
        })
        .map(|entry| entry["matcher"].as_str().unwrap_or_default())
        .collect();
    assert!(
        !matchers.is_empty(),
        "no PreToolUse hook runs {GUARD_COMMAND:?}: {settings}"
    );
    let covered: Vec<&str> = matchers.iter().flat_map(|m| m.split('|')).collect();
    let missing: Vec<&str> = GUARDED_TOOLS
        .iter()
        .copied()
        .filter(|tool| !covered.contains(tool))
        .collect();
    assert!(
        missing.is_empty(),
        "the matchers {matchers:?} do not cover {missing:?}: {settings}"
    );
    assert_eq!(
        matchers.len(),
        DECLARED_CLASSES,
        "one registration per declared tool class, neither adding nor dropping one: {matchers:?}"
    );
}

/// Her assistant reached for it in her session (its stub does, through its
/// hooks, as its first act): the session ran, and its hooks were fired.
#[when("her assistant runs a raw shell command")]
fn alice_raw_command(w: &mut World) {
    let fired = w.alice_recorded("fire.out");
    assert!(
        fired.trim().is_empty(),
        "her assistant's hooks could not be fired: {fired}"
    );
}

/// The guard was asked about that very call, and was asked before the
/// command ran.
#[then("ltk was asked about the command before it ran")]
fn alice_guard_asked(w: &mut World) {
    let log = w.alice_recorded("guard.log");
    let lines: Vec<&str> = log.lines().collect();
    let asked = lines
        .iter()
        .position(|l| l.starts_with("asked ") && l.contains(RAW_COMMAND) && l.contains("\"Bash\""))
        .unwrap_or_else(|| panic!("ltk was never asked about {RAW_COMMAND:?}: {log}"));
    let ran = lines
        .iter()
        .position(|l| *l == format!("ran {RAW_COMMAND}"))
        .unwrap_or_else(|| panic!("{RAW_COMMAND:?} never ran: {log}"));
    assert!(
        asked < ran,
        "ltk was asked only after the command ran: {log}"
    );
}

#[then("her project holds no engine files of ctxloom's")]
fn alice_project_clean(w: &mut World) {
    let after = w.alice_engine_files();
    assert!(
        after.is_empty(),
        "her session left engine files of ctxloom's in her project: {after:?}"
    );
}
