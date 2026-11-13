//! The steps of j000700 (a team's standards, authored once in a repository of
//! their own, reach every project that depends on them), as a child of the
//! base World.
//!
//! Three places, two people, one machine's worth of temporary directories:
//!
//! - Carol's `team-standards`, a git repository of ctxloom content alone,
//!   beside her checkout of the team's project; both are hers, so both run
//!   in her home (the World's own [`Project`]: its directory is her checkout
//!   of the team's project);
//! - the team's project, pushed to a bare origin Bob clones;
//! - Bob's clone, in a home of his own (a second [`Project`]).
//!
//! `https://github.com/acme/team-standards`, the address the feature names,
//! is mapped to Carol's bare `team-standards.git` by each home's own git
//! configuration (`url.<fixture>.insteadOf`), so the commands run exactly as
//! written and the real `remote-git` adapter clones a real repository, with
//! nothing reaching the network (as j000200's).
//!
//! The assistant is a Claude Code stub first on `PATH`. It records the
//! session home it was pointed at (`CLAUDE_CONFIG_DIR`) beside itself, so a
//! "session offers" step reads what that session was handed from the home
//! itself: its context file, its commands and skills, its MCP servers in
//! `.claude.json` and its hooks in the settings file `--settings` names. The
//! setup interview (a launch whose arguments carry the interview's opening)
//! is recorded apart; a one-shot child asked to compress text (the distill
//! prompt's opening) answers with the distilled form the steps staged.
//!
//! Carol's editor is a script that writes, into the file it is handed, what
//! the steps staged under that file's name: so `fragment create` and
//! `command edit` store what she "wrote", through ctxloom's real editor
//! round trip.

use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};

use cucumber::gherkin::Step;
use cucumber::{given, then, when};
use serde_json::Value;

use super::super::deps::git_in;
use super::super::gherkin_common::{doc, split, CommonWorld};
use super::super::library::Project;
use super::World;
use crate::tests::{files, Ran};

/// The address the feature names for Carol's repository.
const STANDARDS_URL: &str = "https://github.com/acme/team-standards";
/// Where a bundle's tree lives in a project.
const TREE: &str = ".ctxloom/content/bundles/v2/standards";

/// What Carol wrote, each carrying a marker nothing else in the scenario does.
const HOUSE_STYLE: &str =
    "J000700-HOUSE-STYLE: name a thing for what it does, and keep functions short.\n";
const COMMITS_V1: &str =
    "J000700-CONVENTIONAL-COMMITS-V1: write `type(scope): subject`, imperative, under 72 characters.\n";
const COMMITS_V2: &str =
    "J000700-CONVENTIONAL-COMMITS-V2: write `type(scope)!: subject` for a breaking change, and say why in the body.\n";
const SKILL_MD: &str = "---\nname: review-checklist\ndescription: Reviews a change against the team's checklist\n---\nJ000700-REVIEW-CHECKLIST: run scripts/check.sh, then read the diff against the checklist.\n";
const SKILL_SCRIPT: &str = "#!/bin/sh\necho J000700-REVIEW-CHECKLIST-SCRIPT\n";
/// The MCP server's program and argument.
const LEDGER_COMMAND: &str = "j000700-ledger";
const LEDGER_ARG: &str = "--serve";
/// The hook's program.
const STAMP_COMMAND: &str = "j000700-stamp-edits";

/// The verbose house style, and the distilled form the distiller answers.
const VERBOSE_MARKER: &str = "J000700-VERBOSE-HOUSE-STYLE";
const DISTILLED: &str = "J000700-DISTILLED-HOUSE-STYLE: name by purpose; short functions.";

/// The session home's context file (claude's `--append-system-prompt-file`).
const CONTEXT_FILE: &str = "ctxloom-context.md";

/// The setup interview's opening carries this heading (its last phase).
const INTERVIEW_MARK: &str = "Phase 5 \u{2014} Close";

/// The assistant stub. `$here/distilled-answer` is what it answers a request
/// to compress text with.
const STUB: &str = r#"here="$(dirname "$0")"
case "$*" in
  *"Phase 5 "*" Close"*)
    rec="$here/claude-interview"
    for a in "$@"; do printf '%s\n' "$a" >> "$rec"; done
    [ -f "$here/carol-says" ] && cat "$here/carol-says" >> "$rec"
    exit 0 ;;
esac
rec="$here/claude-launch"
printf '%s\n' "${CLAUDE_CONFIG_DIR:-}" > "$here/claude-home"
for a in "$@"; do
  printf '%s\n' "$a" >> "$rec"
  [ -f "$a" ] && cat "$a" >> "$rec"
done
input=""
[ -t 0 ] || input="$(cat)"
printf '%s\n' "$input" >> "$rec"
answer="J000700-ASSISTANT-REPLY"
case "$input" in
  *"text compression assistant"*) answer="$(cat "$here/distilled-answer")" ;;
esac
echo '{"type":"system","subtype":"init","session_id":"00000000-0000-4000-8000-0000000007aa"}'
printf '{"type":"result","subtype":"success","is_error":false,"result":"%s","session_id":"00000000-0000-4000-8000-0000000007aa"}\n' "$answer"
"#;

/// Carol's editor: the file it is handed gets what is staged under its name,
/// when anything is (`$1` is the file).
const EDITOR: &str = r#"staged="$(dirname "$0")/$(basename "$1")"
[ -f "$staged" ] && cat "$staged" > "$1"
exit 0
"#;

/// Whose place a command runs in.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
enum Place {
    /// Carol's `team-standards`.
    Standards,
    /// Carol's checkout of the team's project.
    Team,
    /// Bob's clone of it.
    Bob,
}

/// What the j000700 scenarios keep between steps.
#[derive(Default)]
pub(super) struct State {
    /// Bob's clone and home, once he works in the project.
    bob: Option<Project>,
    /// Whether Bob has cloned the project yet.
    cloned: bool,
    /// The setup Carol typed, staged until she says what she tells the
    /// interview.
    setup: String,
    /// Whether the standards bundle is authored and pushed.
    authored: bool,
    /// Whether the team's project depends on it (committed and pushed).
    depends: bool,
}

impl World {
    /// Carol's scratch directory: beside her checkout of the team's project
    /// and her home.
    fn carol_scratch(&self) -> PathBuf {
        self.adopt_scratch()
    }

    fn standards_dir(&self) -> PathBuf {
        self.carol_scratch().join("team-standards")
    }

    fn standards_origin(&self) -> PathBuf {
        self.carol_scratch().join("origin/team-standards.git")
    }

    fn team_origin(&self) -> PathBuf {
        self.carol_scratch().join("origin/team.git")
    }

    fn stub_bin(&self) -> PathBuf {
        self.carol_scratch().join("bin")
    }

    fn editor_dir(&self) -> PathBuf {
        self.carol_scratch().join("editor")
    }

    fn bob(&self) -> &Project {
        self.authoring
            .bob
            .as_ref()
            .expect("Bob works in the project")
    }

    fn dir_of(&self, place: Place) -> PathBuf {
        match place {
            Place::Standards => self.standards_dir(),
            Place::Team => self.common.project.dir().to_path_buf(),
            Place::Bob => self.bob().dir().to_path_buf(),
        }
    }

    fn home_of(&self, place: Place) -> PathBuf {
        match place {
            Place::Standards | Place::Team => self.common.project.home().to_path_buf(),
            Place::Bob => self.bob().home().to_path_buf(),
        }
    }

    /// `ctxloom args` in `place`, as its owner: her or his home, the
    /// scenario's environment, the place's directory as the project root,
    /// and `input` on its standard input.
    fn ctxloom_in(&self, place: Place, args: &[String], input: Option<&str>) -> Ran {
        let dir = self.dir_of(place);
        let home = self.home_of(place);
        let mut command = Command::new(crate::tests::binary());
        command
            .args(args)
            .env_clear()
            .env("HOME", &home)
            .env("USERPROFILE", &home)
            .env("TMPDIR", crate::tests::temp_root())
            .env("CTXLOOM_ROOT", &dir)
            .current_dir(&dir)
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped());
        if let Some(path) = crate::tests::host_var("SystemRoot") {
            command.env("SystemRoot", path);
        }
        for (name, value) in &self.common.env {
            command.env(name, value);
        }
        let mut child = command.spawn().expect("ctxloom runs");
        crate::tests::feed(
            child.stdin.take().expect("stdin"),
            input.unwrap_or_default(),
        );
        Ran::of(child.wait_with_output().expect("ctxloom finishes"))
    }

    /// The docstring's commands, run in turn in `place`; the first to fail is
    /// the one the next step sees.
    fn run_in_place(&mut self, place: Place, lines: &str) {
        for line in lines.lines().map(str::trim).filter(|l| !l.is_empty()) {
            let words = split(line);
            assert_eq!(words.first().map(String::as_str), Some("ctxloom"), "{line}");
            let ran = self.ctxloom_in(place, &words[1..], None);
            let failed = ran.code != 0;
            self.common.line = line.to_string();
            self.common.ran = Some(ran);
            if failed {
                return;
            }
        }
    }

    /// As [`World::run_in_place`], each command required to succeed.
    fn run_ok(&mut self, place: Place, lines: &str) {
        self.run_in_place(place, lines);
        let ran = self.common.ran();
        assert_eq!(
            ran.code,
            0,
            "{} in {place:?}: {}",
            self.common.line,
            ran.said()
        );
    }

    /// The machine both of them work on: the stub assistant first on `PATH`,
    /// a fixture token, Carol's editor, and each home mapping the feature's
    /// address to Carol's bare repository.
    fn authoring_machine(&mut self) {
        if files::exists(self.stub_bin().join("claude")) {
            return;
        }
        files::put_program(self.stub_bin().join("claude"), STUB);
        files::put(self.stub_bin().join("distilled-answer"), DISTILLED);
        let host = crate::tests::host_var("PATH").unwrap_or_default();
        let path = format!("{}:{}", self.stub_bin().display(), host.to_string_lossy());
        self.common.set_env("PATH", path);
        super::adopt::token(self);
        files::put(self.editor_dir().join("editor.sh"), EDITOR);
        let editor = self.editor_dir().join("editor.sh");
        self.common.project.write_home(
            ".ctxloom/config.yaml",
            &format!(
                "schema_version: 9\neditor:\n  command: sh\n  args:\n    - '{}'\n",
                editor.display()
            ),
        );
        self.map_standards(self.common.project.home());
    }

    /// The home's git maps the feature's address to Carol's bare repository.
    fn map_standards(&self, home: &Path) {
        let held = files::read(home.join(".gitconfig")).unwrap_or_default();
        files::put(
            home.join(".gitconfig"),
            format!(
                "{held}[url \"{}\"]\n\tinsteadOf = {STANDARDS_URL}.git\n\tinsteadOf = {STANDARDS_URL}\n",
                files::file_url(&self.standards_origin())
            ),
        );
    }

    /// Stage what Carol's editor writes into a file named `name`.
    fn stage_edit(&self, name: &str, content: &str) {
        files::put(self.editor_dir().join(name), content);
    }

    /// Commit and push a place's work tree to its origin.
    fn push(&self, dir: &Path, message: &str) {
        git_in(dir, &["add", "-A"]);
        git_in(dir, &["commit", "-q", "--allow-empty", "-m", message]);
        git_in(dir, &["push", "-q", "origin", "main"]);
    }

    /// Carol's `team-standards`: a git repository with a bare origin.
    fn standards_repository(&mut self) {
        self.authoring_machine();
        let dir = self.standards_dir();
        if files::exists(dir.join(".git")) {
            return;
        }
        let origin = self.standards_origin();
        files::mkdirs(origin.parent().expect("a parent"));
        git_in(
            origin.parent().expect("a parent"),
            &["init", "-q", "--bare", "-b", "main", "team-standards.git"],
        );
        files::mkdirs(&dir);
        git_in(&dir, &["init", "-q", "-b", "main"]);
        git_in(
            &dir,
            &["remote", "add", "origin", origin.to_str().expect("utf-8")],
        );
    }

    /// The team's project: Carol's checkout, set up with ctxloom, pushed to a
    /// bare origin; and Bob, in a home of his own, about to clone it.
    fn team_project(&mut self) {
        self.authoring_machine();
        let dir = self.common.project.dir().to_path_buf();
        if !files::exists(dir.join(".git")) {
            git_in(&dir, &["init", "-q", "-b", "main"]);
            self.run_ok(Place::Team, "ctxloom init");
            let origin = self.team_origin();
            files::mkdirs(origin.parent().expect("a parent"));
            git_in(
                origin.parent().expect("a parent"),
                &["init", "-q", "--bare", "-b", "main", "team.git"],
            );
            git_in(
                &dir,
                &["remote", "add", "origin", origin.to_str().expect("utf-8")],
            );
            self.push(&dir, "the team's project, set up with ctxloom");
        }
        if self.authoring.bob.is_none() {
            let bob = Project::bare();
            self.map_standards(bob.home());
            self.authoring.bob = Some(bob);
        }
    }

    /// Carol's whole bundle, authored with ctxloom where it has a verb for it
    /// and as files where it has none (the skill's script, the MCP server,
    /// the hook), and pushed. `profile` adds her "reviewing" profile.
    fn author_standards(&mut self, profile: bool) {
        self.standards_repository();
        let dir = self.standards_dir();
        if !files::exists(dir.join(".ctxloom/config.yaml")) {
            self.run_ok(Place::Standards, "ctxloom init");
        }
        if !files::exists(dir.join(TREE).join("bundle.yaml")) {
            self.stage_edit("house-style.md", HOUSE_STYLE);
            self.stage_edit("conventional-commits.md", COMMITS_V1);
            self.run_ok(
                Place::Standards,
                "ctxloom bundle create standards -d \"How the team works\"\n\
                 ctxloom fragment create standards#fragments/house-style\n\
                 ctxloom command create standards#commands/conventional-commits\n\
                 ctxloom skill create standards#skills/review-checklist -d \"Reviews a change against the team's checklist\"",
            );
            let tree = dir.join(TREE);
            files::put(tree.join("skills/review-checklist/SKILL.md"), SKILL_MD);
            files::put(
                tree.join("skills/review-checklist/scripts/check.sh"),
                SKILL_SCRIPT,
            );
            files::put(
                tree.join("skills/.review-checklist.meta.yaml"),
                "executable:\n  - scripts/check.sh\n",
            );
            files::put(
                tree.join("mcp/ledger.yaml"),
                format!("command: {LEDGER_COMMAND}\nargs:\n  - {LEDGER_ARG}\n"),
            );
            files::put(
                tree.join("hooks/post_file_edit/stamp-edits.yaml"),
                format!("command: {STAMP_COMMAND}\n"),
            );
        }
        if profile {
            files::put(
                dir.join(TREE).join("profiles/reviewing.yaml"),
                "description: Reviewing a change\nskills:\n  - standards#skills/review-checklist\n",
            );
        }
        self.push(&dir, "the team's standards");
        self.authoring.authored = true;
    }

    /// The team's project depends on Carol's standards, committed and pushed,
    /// and Bob has the project.
    fn depend(&mut self) {
        if !self.authoring.authored {
            self.author_standards(false);
        }
        self.team_project();
        if !self.authoring.depends {
            self.run_ok(
                Place::Team,
                &format!(
                    "ctxloom remote create team {STANDARDS_URL}\n\
                     ctxloom profile edit default --add-bundle team/standards\n\
                     ctxloom deps pull"
                ),
            );
            self.push(self.common.project.dir(), "depend on the team's standards");
            self.authoring.depends = true;
        }
        self.bob_pulls();
    }

    /// Bob's first pull clones the project; each later one pulls.
    fn bob_pulls(&mut self) {
        let origin = self.team_origin();
        let dir = self.bob().dir().to_path_buf();
        if self.authoring.cloned {
            git_in(&dir, &["pull", "-q", "origin", "main"]);
        } else {
            git_in(&dir, &["clone", "-q", origin.to_str().expect("utf-8"), "."]);
            self.authoring.cloned = true;
        }
    }

    /// Bob fetches and starts a session: the record of the last one cleared
    /// first, so what is read is this session's.
    fn bob_starts(&mut self, lines: &str) {
        for record in ["claude-home", "claude-launch"] {
            let path = self.stub_bin().join(record);
            if files::exists(&path) {
                files::remove(&path);
            }
        }
        self.run_ok(Place::Bob, lines);
    }

    /// The session home the assistant was last launched in.
    fn session_home(&self) -> PathBuf {
        let recorded = files::read(self.stub_bin().join("claude-home"))
            .unwrap_or_else(|_| panic!("no assistant was launched"));
        PathBuf::from(recorded.trim())
    }

    fn session_file(&self, rel: &str) -> String {
        let path = self.session_home().join(rel);
        files::read(&path).unwrap_or_else(|_| {
            panic!(
                "the session home holds no {rel}; it holds {:?}",
                listing(&self.session_home())
            )
        })
    }

    /// The command file of the session home whose name carries `name`.
    fn session_command(&self, name: &str) -> String {
        let dir = self.session_home().join("commands");
        let found = files::children(&dir)
            .into_iter()
            .find(|p| {
                let file = p.file_name().expect("a name").to_string_lossy();
                file.contains(name) && file.ends_with(".md")
            })
            .unwrap_or_else(|| {
                panic!(
                    "the session offers no command {name}; its home holds {:?}",
                    listing(&self.session_home())
                )
            });
        files::read(found).expect("a readable command file")
    }

    /// Whether the session was offered the skill, as Carol wrote it.
    fn offers_skill(&self) {
        assert_eq!(
            self.session_file("skills/review-checklist/SKILL.md"),
            SKILL_MD,
            "the session's skill is not Carol's"
        );
        assert_eq!(
            self.session_file("skills/review-checklist/scripts/check.sh"),
            SKILL_SCRIPT,
            "the session's skill script is not Carol's"
        );
    }

    /// Everything the session was handed, as one text: the context file and
    /// every file of its home.
    fn everything_handed(&self) -> String {
        let mut out = String::new();
        for path in walk(&self.session_home()) {
            if let Ok(text) = files::read(&path) {
                out.push_str(&text);
            }
        }
        out
    }
}

/// Every file below `dir`.
fn walk(dir: &Path) -> Vec<PathBuf> {
    let mut out = Vec::new();
    if files::is_dir(dir) {
        for child in files::children(dir) {
            if files::is_dir(&child) {
                out.extend(walk(&child));
            } else {
                out.push(child);
            }
        }
    }
    out
}

fn listing(dir: &Path) -> Vec<String> {
    let prefix = format!("{}/", dir.display());
    walk(dir)
        .iter()
        .map(|p| {
            p.display()
                .to_string()
                .trim_start_matches(&prefix)
                .to_string()
        })
        .collect()
}

// ------------------------------------------------------------ background

#[given(expr = "Carol keeps the team's standards in a repository of their own, {string}")]
fn keeps_standards(w: &mut World, _name: String) {
    w.standards_repository();
}

#[given("Bob works in the team's project")]
fn bob_works(w: &mut World) {
    w.team_project();
}

// ------------------------------------------------- scenario 1: authoring

/// Staged: the setup runs once she has said what she tells the interview.
#[when(expr = "Carol sets ctxloom up in {string}:")]
fn sets_up(w: &mut World, _name: String, step: &Step) {
    w.authoring.setup = doc(step);
}

/// Her setup, run at a terminal (the binary's test-only stand-in for one):
/// init's own questions answered with their suggestions, then the
/// interview, to which she says what the step says (the stub records it as
/// what it was told), then the restart declined by a closed input.
#[when("she tells her assistant in the setup interview:")]
fn tells_interview(w: &mut World, step: &Step) {
    files::put(w.stub_bin().join("carol-says"), doc(step));
    let lines = std::mem::take(&mut w.authoring.setup);
    w.common.set_env("CTXLOOM_TEST_TERMINAL", "1".to_string());
    for line in lines.lines().map(str::trim).filter(|l| !l.is_empty()) {
        let words = split(line);
        let ran = w.ctxloom_in(Place::Standards, &words[1..], Some("\n\n"));
        w.common.line = line.to_string();
        w.common.ran = Some(ran);
    }
    w.common
        .env
        .retain(|(name, _)| name != "CTXLOOM_TEST_TERMINAL");
}

/// The interview ran in her repository and was told what it is; what it
/// left there is ctxloom's directory and nothing for any language: no file
/// outside `.ctxloom`, no companion, no bundle or profile but the bare
/// default init seeds.
#[then(expr = "the setup leaves {string} configured as content only, for no language or toolchain")]
fn content_only(w: &mut World, _name: String) {
    w.ran().succeeds();
    let interview = files::read(w.stub_bin().join("claude-interview")).unwrap_or_default();
    assert!(
        interview.contains(INTERVIEW_MARK),
        "no setup interview was launched: {interview}"
    );
    assert!(
        interview.contains("standalone ctxloom content"),
        "the interview was not told what the repository is: {interview}"
    );
    let dir = w.standards_dir();
    let mut top: Vec<String> = files::children(&dir)
        .iter()
        .filter_map(|p| p.file_name().map(|n| n.to_string_lossy().into_owned()))
        .filter(|n| n != ".git")
        .collect();
    top.sort();
    assert_eq!(
        top,
        vec![".ctxloom".to_string()],
        "setup wrote outside .ctxloom"
    );
    let bundles: Vec<String> = files::children(dir.join(".ctxloom/content/bundles/v2"))
        .iter()
        .filter_map(|p| p.file_name().map(|n| n.to_string_lossy().into_owned()))
        .collect();
    assert_eq!(bundles, vec!["project".to_string()], "setup added content");
    let profile =
        files::read(dir.join(".ctxloom/content/bundles/v2/project/profiles/default.yaml"))
            .expect("the default profile");
    assert!(
        !profile.contains("\nbundles:") && !profile.contains("\nparents:"),
        "the default profile selects something: {profile}"
    );
    let ran = w.ctxloom_in(
        Place::Standards,
        &["companion".to_string(), "list".to_string()],
        None,
    );
    ran.succeeds();
    let none = ran.json().as_array().is_none_or(Vec::is_empty);
    assert!(none, "setup enabled a companion: {}", ran.said());
}

#[when(expr = "Carol authors the team's standards in {string}:")]
fn authors(w: &mut World, _name: String, step: &Step) {
    w.stage_edit("house-style.md", HOUSE_STYLE);
    w.stage_edit("conventional-commits.md", COMMITS_V1);
    w.run_in_place(Place::Standards, &doc(step));
}

#[when(expr = "she pushes {string}")]
fn pushes(w: &mut World, _name: String) {
    let dir = w.standards_dir();
    w.push(&dir, "the team's standards");
}

#[then(
    expr = "Carol sees the house-style fragment, the conventional-commits command and the review-checklist skill in the {string} bundle"
)]
fn sees_bundle(w: &mut World, bundle: String) {
    w.ran().succeeds();
    let ran = w.ctxloom_in(
        Place::Standards,
        &["bundle".to_string(), "show".to_string(), bundle],
        None,
    );
    ran.succeeds();
    let shown = ran.json();
    for (kind, name) in [
        ("fragments", "house-style"),
        ("commands", "conventional-commits"),
        ("skills", "review-checklist"),
    ] {
        assert!(
            shown.get(kind).and_then(|k| k.get(name)).is_some(),
            "the bundle shows no {kind} {name}: {}",
            ran.said()
        );
    }
    let fragment = files::read(
        w.standards_dir()
            .join(TREE)
            .join("fragments/house-style.md"),
    )
    .expect("the fragment's file");
    assert!(
        fragment.contains(HOUSE_STYLE.trim()),
        "the fragment is not what she wrote: {fragment}"
    );
    let command = files::read(
        w.standards_dir()
            .join(TREE)
            .join("prompts/conventional-commits.md"),
    )
    .expect("the command's file");
    assert!(
        command.contains(COMMITS_V1.trim()),
        "the command is not what she wrote: {command}"
    );
}

/// What she pushed, cloned afresh: ctxloom's directory and nothing else.
#[then(expr = "{string} holds nothing but its .ctxloom directory")]
fn holds_only_ctxloom(w: &mut World, _name: String) {
    let clone = w.carol_scratch().join("standards-clone");
    let origin = w.standards_origin();
    git_in(
        &w.carol_scratch(),
        &[
            "clone",
            "-q",
            origin.to_str().expect("utf-8"),
            "standards-clone",
        ],
    );
    let mut top: Vec<String> = files::children(&clone)
        .iter()
        .filter_map(|p| p.file_name().map(|n| n.to_string_lossy().into_owned()))
        .filter(|n| n != ".git")
        .collect();
    top.sort();
    assert_eq!(top, vec![".ctxloom".to_string()]);
}

// ---------------------------------------- scenario 2: every kind reaches Bob

#[given(
    expr = "Carol has authored and pushed the {string} bundle, carrying a fragment, a command, a skill, an MCP server and a hook"
)]
fn authored_every_kind(w: &mut World, _bundle: String) {
    w.author_standards(false);
}

#[when("Carol has the team's project depend on her standards:")]
fn has_project_depend(w: &mut World, step: &Step) {
    w.team_project();
    w.run_in_place(Place::Team, &doc(step));
    w.authoring.depends = true;
}

#[when("she commits it to the project")]
fn commits_to_project(w: &mut World) {
    w.ran().succeeds();
    let dir = w.common.project.dir().to_path_buf();
    w.push(&dir, "the team's project moves");
}

#[when("Bob pulls the project")]
fn bob_pulls_project(w: &mut World) {
    w.bob_pulls();
}

#[when("Bob fetches what the project depends on and starts a session:")]
fn bob_fetches_and_starts(w: &mut World, step: &Step) {
    w.bob_starts(&doc(step));
}

#[then(expr = "Bob's session offers the team's {word} {string}, as Carol wrote it")]
fn offers_kind(w: &mut World, kind: String, name: String) {
    match kind.as_str() {
        "fragment" => {
            assert_eq!(name, "house-style");
            let context = w.session_file(CONTEXT_FILE);
            assert!(
                context.contains(HOUSE_STYLE.trim()),
                "the session's context lacks the house style: {context}"
            );
        }
        "command" => {
            let body = w.session_command(&name);
            assert!(
                body.contains(COMMITS_V1.trim()),
                "the session's command is not Carol's: {body}"
            );
        }
        "skill" => {
            assert_eq!(name, "review-checklist");
            w.offers_skill();
        }
        "mcp-server" => {
            let state: Value = serde_json::from_str(&w.session_file(".claude.json")).expect("JSON");
            let server = state
                .get("mcpServers")
                .and_then(|s| s.get(&name))
                .unwrap_or_else(|| panic!("the session registers no MCP server {name}: {state}"));
            assert_eq!(
                server.get("command").and_then(Value::as_str),
                Some(LEDGER_COMMAND)
            );
            assert_eq!(
                server.get("args"),
                Some(&Value::Array(vec![Value::String(LEDGER_ARG.to_string())]))
            );
        }
        "hook" => {
            assert_eq!(name, "stamp-edits");
            let hooks: Value =
                serde_json::from_str(&w.session_file("ctxloom-hooks.json")).expect("JSON");
            let found = hooks
                .get("hooks")
                .and_then(|h| h.get("PostToolUse"))
                .and_then(Value::as_array)
                .is_some_and(|groups| {
                    groups.iter().any(|group| {
                        group
                            .get("hooks")
                            .and_then(Value::as_array)
                            .is_some_and(|hooks| {
                                hooks.iter().any(|h| {
                                    h.get("command").and_then(Value::as_str) == Some(STAMP_COMMAND)
                                })
                            })
                    })
                });
            assert!(found, "the session registers no stamp-edits hook: {hooks}");
        }
        other => panic!("no such kind: {other}"),
    }
}

// --------------------------------------------- scenario 3: Carol's profile

#[given(
    expr = "Carol has authored and pushed the {string} bundle, carrying a {string} profile that selects the review-checklist skill"
)]
fn authored_with_profile(w: &mut World, _bundle: String, _profile: String) {
    w.author_standards(true);
}

#[given("the team's project depends on Carol's standards")]
fn project_depends(w: &mut World) {
    w.depend();
}

#[when("Bob fetches what the project depends on and starts a session with Carol's profile:")]
fn bob_starts_with_profile(w: &mut World, step: &Step) {
    w.bob_starts(&doc(step));
}

#[then("Bob's session offers the review-checklist skill the profile selects")]
fn offers_profile_skill(w: &mut World) {
    w.offers_skill();
}

// -------------------------------------------- scenario 4: a changed command

#[given("Bob's session already offers Carol's conventional-commits command")]
fn already_offers(w: &mut World) {
    w.bob_starts("ctxloom deps pull\nctxloom run");
    let body = w.session_command("conventional-commits");
    assert!(body.contains(COMMITS_V1.trim()), "{body}");
}

#[when(expr = "Carol edits the command in {string}:")]
fn edits_command(w: &mut World, _name: String, step: &Step) {
    w.stage_edit("conventional-commits.md", COMMITS_V2);
    w.run_in_place(Place::Standards, &doc(step));
    let held = files::read(
        w.standards_dir()
            .join(TREE)
            .join("prompts/conventional-commits.md"),
    )
    .expect("the command's file");
    assert!(
        held.contains(COMMITS_V2.trim()),
        "her edit did not land: {held}; {}",
        w.ran().said()
    );
}

#[when("Carol moves the team's project onto the new version:")]
fn moves_project(w: &mut World, step: &Step) {
    w.run_in_place(Place::Team, &doc(step));
}

#[then("Bob's session offers the updated conventional-commits command")]
fn offers_updated(w: &mut World) {
    let body = w.session_command("conventional-commits");
    assert!(
        body.contains(COMMITS_V2.trim()),
        "the session's command is not Carol's new version: {body}"
    );
}

#[then("it no longer offers the previous version")]
fn not_previous(w: &mut World) {
    let handed = w.everything_handed();
    assert!(
        !handed.contains("J000700-CONVENTIONAL-COMMITS-V1"),
        "the session still carries the previous version"
    );
}

// ---------------------------------------------------- scenario 5: distill

#[given(expr = "Carol has authored a verbose house-style fragment in {string}")]
fn verbose_fragment(w: &mut World, _name: String) {
    let sentence = format!(
        "{VERBOSE_MARKER}: when you name a thing, it is very important that you take the time to name it for what it actually does, rather than for how it happens to do it, and you should also try to keep every function you write as short as you reasonably can.\n"
    );
    files::put(
        w.standards_dir()
            .join(TREE)
            .join("fragments/house-style.md"),
        sentence.repeat(8),
    );
}

#[when("Carol distills the fragment:")]
fn distills(w: &mut World, step: &Step) {
    w.run_in_place(Place::Standards, &doc(step));
}

#[when("she pushes both forms")]
fn pushes_both(w: &mut World) {
    w.ran().succeeds();
    let distilled = w
        .standards_dir()
        .join(TREE)
        .join("fragments/house-style.distilled.md");
    assert!(
        files::exists(&distilled),
        "no distilled form was stored beside the fragment"
    );
    let dir = w.standards_dir();
    w.push(&dir, "the house style, and its distilled form");
}

/// The team moves the project onto the new version (Carol's upgrade,
/// committed), Bob pulls it, fetches what it depends on and starts a session
/// with distilled content preferred (`config.content_form: distilled`, in his
/// own config).
#[when("Bob pulls the project with distilled context enabled")]
fn bob_pulls_distilled(w: &mut World) {
    w.run_ok(Place::Team, "ctxloom deps upgrade --yes");
    let dir = w.common.project.dir().to_path_buf();
    w.push(&dir, "move onto the distilled house style");
    w.bob_pulls();
    files::put(
        w.bob().home().join(".ctxloom/config.yaml"),
        "schema_version: 9\nconfig:\n  content_form: distilled\n",
    );
    w.bob_starts("ctxloom deps pull\nctxloom run");
}

#[then("Bob's assistant receives the distilled guidance")]
fn receives_distilled(w: &mut World) {
    let context = w.session_file(CONTEXT_FILE);
    assert!(
        context.contains(DISTILLED),
        "the session's context lacks the distilled form: {context}"
    );
}

#[then("it does not receive the verbose original")]
fn not_verbose(w: &mut World) {
    let context = w.session_file(CONTEXT_FILE);
    assert!(
        !context.contains(VERBOSE_MARKER),
        "the session's context carries the verbose original: {context}"
    );
}
