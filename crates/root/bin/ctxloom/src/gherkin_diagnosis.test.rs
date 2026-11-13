//! The steps of j001900 (the day the assistant goes blind), as a child of the
//! base World: Alice walks lost deploy guidance back from the symptom, one
//! cause per scenario, and each ends with her assistant answering again.
//!
//! Hermetic: Claude Code is a stub first on `PATH` that knows only what it
//! was handed. It answers with every deploy marker in the context file ctxloom
//! appended to its system prompt (`--append-system-prompt-file`), and nothing
//! else of the project's; so "her assistant answers with the deploy process"
//! is a claim about what ctxloom handed the session. It records each launch's
//! arguments beside itself. The team's remote is a bare git repository in the
//! scenario's directory, registered under `team`.
//!
//! A docstring's commands are run as she types them: `ctxloom ... > file`
//! writes the command's standard output to `file` in the checkout it ran in,
//! and `diff a b` is the system's `diff`, whose exit 1 (they differ) is its
//! answer, not a failure. Bob's checkout is a second project of its own,
//! beside hers; what he "sends" her is copied into hers.

use std::path::{Path, PathBuf};

use cucumber::gherkin::Step;
use cucumber::{given, then, when};
use serde_json::Value;

use super::super::deps::git_in;
use super::super::gherkin_common::{doc, split, CommonWorld};
use super::super::library::Project;
use super::super::Ran;
use super::World;
use crate::tests::files;

/// The deploy process as the team first wrote it, and as Carol rewrote it.
const OLDER: &str = "J001900-DEPLOY-PROCESS-OLDER";
const NEWER: &str = "J001900-DEPLOY-PROCESS-NEWER";

/// The stub engine: records its launch beside itself and answers, as claude
/// does under `--output-format stream-json`, with the deploy markers its
/// appended system prompt carries.
const CLAUDE_STUB: &str = r#"rec="$(dirname "$0")/claude-launch"
: > "$rec"
ctx=""
next=""
for a in "$@"; do
  printf '%s\n' "$a" >> "$rec"
  [ "$next" = 1 ] && ctx="$a"
  next=""
  [ "$a" = "--append-system-prompt-file" ] && next=1
done
[ -t 0 ] || cat > /dev/null
said="I do not know how this team deploys."
if [ -n "$ctx" ] && [ -f "$ctx" ]; then
  found="$(grep -o 'J001900-DEPLOY-PROCESS-[A-Z]*' "$ctx" | sort -u | tr '\n' ' ')"
  [ -n "$found" ] && said="We deploy as the runbook says: $found"
fi
echo '{"type":"system","subtype":"init","session_id":"00000000-0000-4000-8000-0000000019aa"}'
printf '{"type":"result","subtype":"success","is_error":false,"result":"%s","session_id":"00000000-0000-4000-8000-0000000019aa"}\n' "$said"
"#;

/// The project's config as `ctxloom init` leaves it: one agent, `default`,
/// on claude-code, running the bare `default` profile.
const CONFIG: &str = "schema_version: 9\nagents:\n  default:\n    engine: claude-code\n    profiles:\n      - default\n    runtime: host\ndefault_agent: default\n";

/// What the j001900 scenarios keep between steps.
#[derive(Default)]
pub(super) struct State {
    /// The Background ran: the docstrings are this journey's.
    on: bool,
    /// Bob's checkout of the same project.
    bob: Option<Project>,
    /// The team's bare remote and the scratch directory it lives in.
    remote: Option<(file_system::Rooted, PathBuf)>,
    /// The commit the older runbook was published at.
    older_commit: String,
    /// Every command a docstring ran, as written, and what it did.
    ran: Vec<(String, Ran)>,
}

impl World {
    fn diagnosis_scratch(&self) -> PathBuf {
        self.common
            .project
            .dir()
            .parent()
            .expect("the project is in the scenario's directory")
            .to_path_buf()
    }

    /// One `ctxloom` command line in `project`'s checkout, with the
    /// scenario's environment.
    fn diagnosis_ctxloom(&self, project: &Project, words: &[String]) -> Ran {
        let args: Vec<&str> = words.iter().map(String::as_str).collect();
        let env: Vec<(&str, &str)> = self
            .common
            .env
            .iter()
            .map(|(k, v)| (k.as_str(), v.as_str()))
            .collect();
        project.run_with(&env, &args)
    }

    /// The docstring's lines, run in `who`'s checkout: Bob's, or hers.
    fn diagnosis_lines(&mut self, text: &str, bob: bool) {
        for line in text.lines().map(str::trim).filter(|l| !l.is_empty()) {
            let mut words = split(line);
            let into = match words.iter().position(|w| w == ">") {
                Some(at) => {
                    let file = words.get(at + 1).cloned().expect("a file after >");
                    words.truncate(at);
                    Some(file)
                }
                None => None,
            };
            let dir = if bob {
                self.diagnosis
                    .bob
                    .as_ref()
                    .expect("Bob's checkout")
                    .dir()
                    .to_path_buf()
            } else {
                self.common.project.dir().to_path_buf()
            };
            let ran = match words.first().map(String::as_str) {
                Some("ctxloom") => {
                    let project = if bob {
                        self.diagnosis.bob.as_ref().expect("Bob's checkout")
                    } else {
                        &self.common.project
                    };
                    self.diagnosis_ctxloom(project, &words[1..])
                }
                Some("diff") => diff(&dir, &words[1..]),
                other => panic!("a command she would type: {other:?} in {line}"),
            };
            if let Some(file) = into {
                files::put(dir.join(&file), &ran.stdout);
            }
            let failed = ran.code != 0 && words[0] != "diff";
            self.common.line = line.to_string();
            self.common.ran = Some(ran.clone());
            self.diagnosis.ran.push((line.to_string(), ran));
            if failed {
                return;
            }
        }
    }

    /// What the command line starting `start` did, the last time it ran.
    fn diagnosis_ran(&self, start: &str) -> &Ran {
        self.diagnosis
            .ran
            .iter()
            .rev()
            .find(|(line, _)| line.starts_with(start))
            .map(|(_, ran)| ran)
            .unwrap_or_else(|| panic!("no `{start}` ran: {:?}", self.ran_lines()))
    }

    fn ran_lines(&self) -> Vec<String> {
        self.diagnosis
            .ran
            .iter()
            .map(|(line, ran)| format!("{line} -> {}", ran.said()))
            .collect()
    }

    /// The team's bare remote, made once, `main` holding the runbook as
    /// first written, and registered in her project as `team`.
    fn team_remote(&mut self) {
        let scratch = files::root();
        let root = scratch.root().to_path_buf();
        git_in(&root, &["init", "--bare", "-b", "main", "remote.git"]);
        git_in(&root, &["init", "-b", "main", "work"]);
        let work = root.join("work");
        write_runbook(&work, OLDER);
        git_in(&work, &["add", "-A"]);
        git_in(&work, &["commit", "-m", "the deploy runbook"]);
        let bare = root.join("remote.git");
        git_in(&work, &["push", bare.to_str().expect("utf-8"), "main"]);
        git_in(&bare, &["symbolic-ref", "HEAD", "refs/heads/main"]);
        self.diagnosis.older_commit = git_in(&work, &["rev-parse", "HEAD"]).trim().to_string();
        let url = files::file_url(&bare);
        self.diagnosis_must(&format!("ctxloom remote create team {url} --forge git"));
        self.diagnosis.remote = Some((scratch, bare));
    }

    /// Carol publishes the rewritten runbook to the team's remote.
    fn carol_publishes(&mut self) {
        let (scratch, bare) = self.diagnosis.remote.as_ref().expect("the team's remote");
        let work = scratch.root().join("work");
        write_runbook(&work, NEWER);
        git_in(&work, &["commit", "-am", "the deploy runbook, rewritten"]);
        git_in(&work, &["push", bare.to_str().expect("utf-8"), "main"]);
    }

    /// A setup command that must succeed.
    fn diagnosis_must(&mut self, line: &str) {
        let ran = self.diagnosis_ctxloom(&self.common.project, &split(line)[1..]);
        assert_eq!(ran.code, 0, "{line}: {}", ran.said());
    }

    /// What the stub was last launched with, one argument a line.
    fn diagnosis_launch(&self) -> String {
        files::read(self.diagnosis_scratch().join("bin/claude-launch")).unwrap_or_default()
    }
}

/// The runbook bundle as a repository (or a project) holds it, its deploy
/// fragment carrying `marker`.
fn write_runbook(dir: &Path, marker: &str) {
    let root = dir.join(".ctxloom/content/bundles/v2/runbook");
    files::put(
        root.join("bundle.yaml"),
        "schema_version: 4\nversion: 1.0.0\ndescription: The team's runbook\n",
    );
    files::put(
        root.join("fragments/deploy.md"),
        format!("---\nno_distill: true\n---\n# Deploying\n\n{marker}: tag the release, then run the deploy job.\n"),
    );
}

/// The project's `default` profile, composing `bundles`.
fn write_profile(project: &Project, bundles: &[&str]) {
    project.write(
        ".ctxloom/content/bundles/v2/project/bundle.yaml",
        "schema_version: 4\nversion: 1.0.0\n",
    );
    let list = if bundles.is_empty() {
        String::new()
    } else {
        let names: String = bundles.iter().map(|b| format!("  - {b}\n")).collect();
        format!("bundles:\n{names}")
    };
    project.write(
        ".ctxloom/content/bundles/v2/project/profiles/default.yaml",
        &format!("description: The project's default profile\ntags:\n  - default\n{list}"),
    );
}

/// The system's `diff`, run in `dir`.
fn diff(dir: &Path, args: &[String]) -> Ran {
    let output = std::process::Command::new("diff")
        .args(args)
        .current_dir(dir)
        .env_clear()
        .env("PATH", crate::tests::host_var("PATH").unwrap_or_default())
        .env("TMPDIR", crate::tests::temp_root())
        .output()
        .expect("diff runs");
    Ran::of(output)
}

/// Whether this step is one of j001900's docstrings, and if so, run it.
pub(super) fn runs(w: &mut World, step: &Step) -> bool {
    if !w.diagnosis.on {
        return false;
    }
    w.before_run();
    w.diagnosis_lines(&doc(step), false);
    true
}

#[given("Alice's team ships its deploy process as ctxloom content")]
fn team_ships(w: &mut World) {
    w.diagnosis.on = true;
    w.common.project = Project::initialized();
    w.common.project.write(".ctxloom/config.yaml", CONFIG);
    write_profile(&w.common.project, &[]);
    let bin = w.diagnosis_scratch().join("bin");
    files::put_program(bin.join("claude"), CLAUDE_STUB);
    let host = crate::tests::host_var("PATH").unwrap_or_default();
    let mut paths = vec![bin];
    paths.extend(files::search_path(&host));
    let path = std::env::join_paths(paths).expect("a PATH");
    w.common
        .set_env("PATH", path.to_string_lossy().into_owned());
    w.common.set_env(
        "CLAUDE_CODE_OAUTH_TOKEN",
        "sk-ant-oat01-acceptance-fixture-not-real".to_string(), // gitleaks:allow
    );
}

// ------------------------------------------------- the two checkouts

#[given("Bob's checkout of the same project delivers the deploy guidance and Alice's does not")]
fn bob_has_it(w: &mut World) {
    let bob = Project::initialized();
    for project in [&w.common.project, &bob] {
        project.write(".ctxloom/config.yaml", CONFIG);
        write_runbook(project.dir(), OLDER);
    }
    write_profile(&bob, &["runbook"]);
    write_profile(&w.common.project, &[]);
    w.diagnosis.bob = Some(bob);
}

/// Bob's one step, named whole: a `^Bob .*:$` catch-all would match the
/// Bob steps of j000400 and j000700 too, which run in this World.
#[when("Bob shows what his next session would be handed, and sends it to Alice:")]
fn bob_runs(w: &mut World, step: &Step) {
    let start = w.diagnosis.ran.len();
    w.diagnosis_lines(&doc(step), true);
    // What he sends her lands in her checkout.
    let bob = w
        .diagnosis
        .bob
        .as_ref()
        .expect("Bob's checkout")
        .dir()
        .to_path_buf();
    let his: Vec<String> = w.diagnosis.ran[start..]
        .iter()
        .map(|(l, _)| l.clone())
        .collect();
    for line in his {
        if let Some((_, file)) = line.split_once('>') {
            let file = file.trim();
            files::copy(bob.join(file), w.common.project.dir().join(file));
        }
    }
}

#[then("the comparison shows the deploy guidance present for Bob and absent for her")]
fn comparison(w: &mut World) {
    let diff = w.diagnosis_ran("diff");
    assert_eq!(
        diff.code,
        1,
        "the two dry runs do not differ: {}",
        diff.said()
    );
    let lines: Vec<&str> = diff.stdout.lines().collect();
    assert!(
        lines
            .iter()
            .any(|l| l.starts_with('<') && l.contains(OLDER)),
        "Bob's side of the comparison lacks the deploy guidance: {}",
        diff.stdout
    );
    assert!(
        !lines
            .iter()
            .any(|l| l.starts_with('>') && l.contains(OLDER)),
        "her side of the comparison carries the deploy guidance: {}",
        diff.stdout
    );
}

// ------------------------------------------------- the profile

#[given("the runbook is pulled, but composed into no profile of Alice's")]
fn pulled_not_composed(w: &mut World) {
    w.team_remote();
    w.diagnosis_must("ctxloom profile edit default --add-bundle team/runbook");
    w.diagnosis_must("ctxloom deps pull");
    w.diagnosis_must("ctxloom profile edit default --remove-bundle team/runbook");
}

#[then("she sees that her profile does not compose the runbook")]
fn profile_lacks_runbook(w: &mut World) {
    let agent = w.diagnosis_ran("ctxloom agent show").succeeds().json();
    // OD-40: `agent show` is the `Agent` resource itself.
    let profiles = &agent["profiles"];
    assert!(
        profiles
            .as_array()
            .is_some_and(|p| p.iter().any(|v| v == "default")),
        "agent show does not name the profile her agent runs: {agent}"
    );
    let profile = w.diagnosis_ran("ctxloom profile show").succeeds().json();
    assert_eq!(profile["name"], "default", "{profile}");
    let bundles: Vec<&str> = profile["bundles"]
        .as_array()
        .map(|b| b.iter().filter_map(Value::as_str).collect())
        .unwrap_or_default();
    assert!(
        !bundles.iter().any(|b| b.contains("runbook")),
        "her profile composes the runbook: {profile}"
    );
}

#[then("her assistant answers with the team's deploy process")]
fn answers_deploy(w: &mut World) {
    w.diagnosis_ran("ctxloom run --one-shot")
        .succeeds()
        .contains(OLDER);
}

// ------------------------------------------------- the hold

#[given("Carol published a newer runbook while Alice's copy is held at the older one")]
fn held_at_older(w: &mut World) {
    w.team_remote();
    w.diagnosis_must("ctxloom profile edit default --add-bundle team/runbook");
    w.diagnosis_must("ctxloom deps pull");
    w.diagnosis_must("ctxloom deps hold team/runbook");
    w.carol_publishes();
}

#[then("she sees the runbook named as held at the older version")]
fn listed_held(w: &mut World) {
    let listing = w.diagnosis_ran("ctxloom deps list").succeeds().json();
    let runbook = listing["deps"]
        .as_array()
        .and_then(|deps| deps.iter().find(|d| d["name"] == "runbook"))
        .unwrap_or_else(|| panic!("the listing does not name the runbook: {listing}"));
    assert_eq!(runbook["held"], true, "not named as held: {runbook}");
    assert_eq!(
        runbook["sha"].as_str(),
        Some(w.diagnosis.older_commit.as_str()),
        "not held at the older version: {runbook}"
    );
}

#[then("her assistant answers with the newer deploy process, not the older")]
fn answers_newer(w: &mut World) {
    w.diagnosis_ran("ctxloom run --one-shot")
        .succeeds()
        .contains(NEWER)
        .lacks(OLDER);
}

// ------------------------------------------------- the loose notes

/// Where her loose notes are, in her repository.
const NOTES: &str = "docs/deploy-notes.md";

#[given("the deploy process exists only as loose notes in Alice's repo, in no bundle")]
fn loose_notes(w: &mut World) {
    let project = &w.common.project;
    project.write(
        NOTES,
        &format!("How we deploy\n\n{OLDER}: tag the release, then run the deploy job.\n"),
    );
    // Her editor: she writes the notes up into whatever file it opens.
    let notes = project.dir().join(NOTES);
    project.write_home(
        ".ctxloom/config.yaml",
        &format!(
            "schema_version: 9\neditor:\n  command: sh\n  args:\n    - \"-c\"\n    - 'cat \"{}\" > \"$1\"'\n    - editor\n",
            notes.display()
        ),
    );
}

#[then("she sees that nothing packaged carries the deploy process")]
fn nothing_packaged(w: &mut World) {
    let search = w.diagnosis_ran("ctxloom search").succeeds().json();
    assert_eq!(search["count"], 0, "the search found something: {search}");
    assert!(
        w.common
            .project
            .read(NOTES)
            .is_some_and(|n| n.contains("deploy")),
        "her loose notes are gone, so the search proves nothing"
    );
}

#[then("her assistant answers with the deploy process she packaged")]
fn answers_packaged(w: &mut World) {
    w.diagnosis_ran("ctxloom run --one-shot")
        .succeeds()
        .contains(OLDER);
}

// ------------------------------------------------- the export

#[given("Alice exported the team's context into the project for Dave last month")]
fn exported(w: &mut World) {
    write_runbook(w.common.project.dir(), OLDER);
    write_profile(&w.common.project, &["runbook"]);
    w.diagnosis_must("ctxloom materialize --yes");
    let exported = w.common.project.read("CLAUDE.md").unwrap_or_default();
    assert!(
        exported.contains(OLDER),
        "the export carries no deploy process: {exported}"
    );
}

#[given("the team's deploy process has changed since")]
fn changed_since(w: &mut World) {
    write_runbook(w.common.project.dir(), NEWER);
}

#[then("she sees the exported context named as stale")]
fn named_stale(w: &mut World) {
    let check = w.diagnosis_ran("ctxloom manage check").succeeds().json();
    let surfaces = check["surfaces"].as_array().cloned().unwrap_or_default();
    assert!(
        surfaces.iter().any(|s| s["route"] == "context"
            && s["backend"] == "claude-code"
            && s["status"] == "stale"),
        "the exported context is not named as stale: {check}"
    );
}

#[then("Dave's assistant, opened on the project without ctxloom, has the current deploy process")]
fn dave_current(w: &mut World) {
    w.diagnosis_ran("ctxloom materialize").succeeds();
    let exported = w.common.project.read("CLAUDE.md").unwrap_or_default();
    assert!(
        exported.contains(NEWER),
        "the export is not current: {exported}"
    );
    assert!(
        !exported.contains(OLDER),
        "the export is still stale: {exported}"
    );
}

#[then("Alice's own sessions are handed the team's context as before, not the exported files")]
fn handed_not_exported(w: &mut World) {
    w.before_run();
    w.diagnosis_lines("ctxloom run --one-shot \"how do we deploy?\"", false);
    w.diagnosis_ran("ctxloom run --one-shot")
        .succeeds()
        .contains(NEWER);
    let record = w.diagnosis_launch();
    let args: Vec<&str> = record.lines().collect();
    let context = args
        .iter()
        .position(|a| *a == "--append-system-prompt-file")
        .and_then(|at| args.get(at + 1))
        .unwrap_or_else(|| panic!("her session was handed no context file: {record}"));
    let sessions = w.common.project.dir().join(".ctxloom/sessions");
    assert!(
        Path::new(context).starts_with(&sessions),
        "her session's context is not in its own session home: {context}"
    );
    let exported = w.common.project.dir().join("CLAUDE.md");
    assert!(
        !args.iter().any(|a| Path::new(a) == exported),
        "her session was handed the exported file: {record}"
    );
}
