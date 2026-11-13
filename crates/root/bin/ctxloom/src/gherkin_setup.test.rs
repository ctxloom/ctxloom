//! The steps of j000200 (setting up ctxloom and the context she trusts), as a
//! child of the base World, after j000100's: the same stub assistant on
//! `PATH`, the same fixture token, and ctxloom's default content built in.
//!
//! The two repositories Alice names are bare git repositories in the
//! scenario's temporary directory. The addresses the feature text names
//! (`https://github.com/alice/ctxloom-content`, ...) are mapped to them by the
//! home's own git configuration (`url.<fixture>.insteadOf`), so the commands
//! run exactly as written, the real `remote-git` adapter clones real
//! repositories, and nothing reaches the network.
//!
//! A session "was handed" a repository's content when the assistant stub's
//! launch record carries the marker of its fragment.
//!
//! Scenario 2 (setup at a terminal) is told as the user's flow, but it is one
//! process: `ctxloom init` run with a terminal stood in by the test-only
//! variable `CTXLOOM_TEST_TERMINAL` (the binary's own seam, compiled only with
//! `test-support`; no pty), the restart answered on its standard input. The
//! steps before the answer stage what the interview stub will do; the step
//! that accepts the restart runs the command and says what the process did.
//! The stub acts out the interview by running, as the assistant would, the
//! ctxloom commands the steps staged; it records the interview and the
//! restarted session apart.

use cucumber::{given, then, when};

use super::super::deps::git_in;
use super::super::gherkin_common::CommonWorld;
use super::World;
use crate::tests::files;

/// The address of Alice's own repository, as the feature names it.
const PERSONAL_URL: &str = "https://github.com/alice/ctxloom-content";
/// The address of her company's.
const COMPANY_URL: &str = "https://github.com/acme/ctxloom-content";

/// The bundle of her own repository, and the marker its fragment carries.
const PERSONAL_BUNDLE: &str = "house-rules";
const PERSONAL_MARKER: &str = "J000200-PERSONAL-REPO-CONTEXT-MARKER";
/// The marker of the fragment with a premise her own repository may carry.
const PREMISED_MARKER: &str = "J000200-PERSONAL-REPO-PREMISED-MARKER";
/// The bundle of her company's, and its marker.
const COMPANY_BUNDLE: &str = "standards";
const COMPANY_MARKER: &str = "J000200-COMPANY-REPO-CONTEXT-MARKER";

impl World {
    /// A bare repository whose `main` holds `bundle`, of one fragment
    /// carrying `marker` (no description: that is a fragment's premise and
    /// would withhold it from an ordinary assembly), served at `url`.
    fn serve_repository(&mut self, name: &str, url: &str, bundle: &str, marker: &str) {
        let root = self.adopt_scratch().join(format!("repository-{name}"));
        let (bare, work) = (root.join("repository.git"), root.join("work"));
        files::mkdirs(&root);
        git_in(
            &root,
            &["init", "-q", "--bare", "-b", "main", "repository.git"],
        );
        git_in(&root, &["init", "-q", "-b", "main", "work"]);
        let tree = format!(".ctxloom/content/bundles/v2/{bundle}");
        files::put(
            work.join(format!("{tree}/bundle.yaml")),
            "schema_version: 4\nversion: 1.0.0\n",
        );
        files::put(
            work.join(format!("{tree}/fragments/marker.md")),
            format!("{marker}\n"),
        );
        git_in(&work, &["add", "-A"]);
        git_in(&work, &["commit", "-q", "-m", "seed"]);
        git_in(
            &work,
            &["push", "-q", bare.to_str().expect("utf-8"), "main"],
        );
        // Longest match wins, so the `.git` spelling maps to the same place.
        let home = self.common.project.home();
        let held = files::read(home.join(".gitconfig")).unwrap_or_default();
        self.common.project.write_home(
            ".gitconfig",
            &format!(
                "{held}[url \"{}\"]\n\tinsteadOf = {url}.git\n\tinsteadOf = {url}\n",
                files::file_url(&bare)
            ),
        );
    }
}

/// A project that has never seen ctxloom, with her assistant and her token
/// (ctxloom's default content, which `init` seeds, is built in). Her
/// commands find the project as hers do, from the directory she types them
/// in: no `CTXLOOM_ROOT` (an empty one is none), so a first `init` must find
/// the project it has just created.
#[given("Alice has a project that has never used ctxloom")]
fn never_used(w: &mut World) {
    w.common_mut().project = crate::tests::library::Project::bare();
    super::adopt::claude_code(w);
    super::adopt::token(w);
    w.common.set_env("CTXLOOM_ROOT", String::new());
}

#[given("Alice has a personal ctxloom repository")]
fn personal(w: &mut World) {
    w.machine();
    w.serve_repository("personal", PERSONAL_URL, PERSONAL_BUNDLE, PERSONAL_MARKER);
}

#[given("her company has a ctxloom repository")]
fn company(w: &mut World) {
    w.machine();
    w.serve_repository("company", COMPANY_URL, COMPANY_BUNDLE, COMPANY_MARKER);
}

/// A second fragment in her own repository's bundle, this one with a premise
/// (its description), committed and pushed over the first.
#[given("her personal repository carries a fragment with a premise")]
fn personal_premised(w: &mut World) {
    let root = w.adopt_scratch().join("repository-personal");
    let work = root.join("work");
    files::put(
        work.join(format!(
            ".ctxloom/content/bundles/v2/{PERSONAL_BUNDLE}/fragments/premised.md"
        )),
        format!("---\ndescription: you are about to cut a release\n---\n{PREMISED_MARKER}\n"),
    );
    git_in(&work, &["add", "-A"]);
    git_in(&work, &["commit", "-q", "-m", "premised"]);
    let bare = root.join("repository.git");
    git_in(
        &work,
        &["push", "-q", bare.to_str().expect("utf-8"), "main"],
    );
}

/// The second half of her setup: the commands in the docstring, run in turn.
#[when(regex = r"^she puts .*:$")]
fn puts_and_starts(w: &mut World, step: &cucumber::gherkin::Step) {
    w.before_run();
    let lines = crate::tests::gherkin_common::doc(step);
    w.common_mut().run_lines(&lines, "");
}

fn handed(w: &World, marker: &str) {
    let record = w.launch_record();
    assert!(
        record.contains(marker),
        "her assistant was launched without {marker}: {record}"
    );
}

#[then("her assistant receives her personal repository's content")]
fn receives_personal(w: &mut World) {
    w.ran().succeeds();
    handed(w, PERSONAL_MARKER);
}

#[then("her assistant receives her company repository's content")]
fn receives_company(w: &mut World) {
    handed(w, COMPANY_MARKER);
}

/// The marker the interview's opening prompt carries, and so the stub knows
/// it is the interview: a heading from the end of the setup interview body.
const INTERVIEW_MARK: &str = "Phase 5 \u{2014} Close";

/// What scenario 2 keeps between steps.
#[derive(Default)]
pub(super) struct State {
    /// The command Alice typed.
    init: String,
    /// The commands the interview does (the assistant's part), in order.
    interview: Vec<String>,
    /// What she types before the restart's answer: her answers to init's
    /// own questions, in order.
    answers: String,
}

/// The assistant stub of scenario 2: as j000100's, but the interview (a
/// launch whose arguments carry the interview's opening) is recorded apart,
/// reads no standard input (it is Alice's answer) and runs `script` first.
fn assistant(script: &str) -> String {
    format!(
        r#"here="$(dirname "$0")"
case "$*" in
  *"{INTERVIEW_MARK}"*) rec="$here/claude-interview"; interview=1 ;;
  *) rec="$here/claude-launch"; interview= ;;
esac
for a in "$@"; do
  printf '%s\n' "$a" >> "$rec"
  [ -f "$a" ] && cat "$a" >> "$rec"
done
if [ -n "${{CLAUDE_CONFIG_DIR:-}}" ] && [ -d "$CLAUDE_CONFIG_DIR" ]; then
  find "$CLAUDE_CONFIG_DIR" -type f -exec cat {{}} + >> "$rec" 2>/dev/null
fi
if [ -n "$interview" ]; then
{script}
else
  [ -t 0 ] || cat >> "$rec"
fi
"#
    )
}

impl World {
    /// Alice's machine as j000200 scenario 1 begins it, unless a Given has
    /// already made it so.
    fn machine(&mut self) {
        if !files::exists(self.adopt_scratch().join("bin/claude")) {
            never_used(self);
        }
    }

    fn interview_record(&self) -> String {
        files::read(self.adopt_scratch().join("bin/claude-interview")).unwrap_or_default()
    }
}

/// The `Alice ...:` step that stages scenario 2 rather than running at once:
/// whether it is that step.
pub(super) fn stages(w: &mut World, step: &cucumber::gherkin::Step) -> bool {
    if !step.value.contains("sets ctxloom up at a terminal") {
        return false;
    }
    w.machine();
    w.setup.init = crate::tests::gherkin_common::doc(step);
    true
}

#[when("in the setup interview she adds her company's repository as a remote")]
fn interview_adds_remote(w: &mut World) {
    w.setup
        .interview
        .push(format!("remote create acme {COMPANY_URL}"));
}

#[when("the setup interview composes her agents' profiles from the sources' fragments")]
fn interview_composes(w: &mut World) {
    w.setup.interview.push(format!(
        "profile edit default --add-bundle personal/{PERSONAL_BUNDLE} --add-bundle acme/{COMPANY_BUNDLE}"
    ));
    w.setup.interview.push("deps pull".to_string());
}

/// Init's question before the interview (owner rulings 89, 106): a bare
/// Enter takes the suggested `ctxloom/`.
#[when("she keeps the output directory init suggests")]
fn keeps_output_dir(w: &mut World) {
    w.setup.answers.push('\n');
}

/// Init's second question (owner ruling of 2026-11-12): a bare Enter takes
/// the suggested `on-demand`.
#[when("she keeps init's suggestion that conditional guidance loads only when it applies")]
fn keeps_premised(w: &mut World) {
    w.setup.answers.push('\n');
}

/// Init's second question, answered `always`.
#[when("she answers that conditional guidance always loads")]
fn premised_always(w: &mut World) {
    w.setup.answers.push_str("always\n");
}

#[when("the setup interview composes her agent's profile from her personal repository")]
fn interview_composes_personal(w: &mut World) {
    w.setup.interview.push(format!(
        "profile edit default --add-bundle personal/{PERSONAL_BUNDLE}"
    ));
    w.setup.interview.push("deps pull".to_string());
}

/// Said by the run that follows; nothing to stage.
#[when("ctxloom offers to restart into her newly configured session")]
fn offers_restart(_: &mut World) {}

#[when("Alice accepts the restart")]
fn accepts_restart(w: &mut World) {
    let bin = w.adopt_scratch().join("bin");
    let ctxloom = crate::tests::binary().display().to_string();
    let home = w.common.project.home().display().to_string();
    let project = w.common.project.dir().display().to_string();
    // The assistant runs ctxloom as her shell would: in the project, which
    // it finds from there, no `CTXLOOM_ROOT` naming it.
    let script: String = w
        .setup
        .interview
        .iter()
        .map(|args| {
            format!(
                "  (cd '{project}' && env -u CTXLOOM_ROOT HOME='{home}' '{ctxloom}' {args}) >> \"$rec\" 2>&1 || exit 1\n"
            )
        })
        .collect();
    files::put_program(bin.join("claude"), &assistant(&script));
    w.common.set_env("CTXLOOM_TEST_TERMINAL", "1".to_string());
    w.before_run();
    let init = w.setup.init.clone();
    let typed = format!("{}y\n", w.setup.answers);
    w.common_mut().run_lines(&init, &typed);
}

#[then("her restarted assistant receives her personal repository's content")]
fn restarted_receives_personal(w: &mut World) {
    w.ran().succeeds();
    interviewed_then_restarted(w);
    handed(w, PERSONAL_MARKER);
}

/// `always`: the premised fragment itself was handed, and no index to ask
/// from.
#[then("her restarted assistant receives her personal repository's premised fragment, and no premise index")]
fn restarted_receives_premised(w: &mut World) {
    handed(w, PREMISED_MARKER);
    let record = w.launch_record();
    assert!(
        !record.contains("# Conditional guidance"),
        "no premise index under always: {record}"
    );
}

#[then("her restarted assistant receives her company repository's content")]
fn restarted_receives_company(w: &mut World) {
    handed(w, COMPANY_MARKER);
}

/// The interview was launched with its opening and could not see what it
/// configured; ctxloom offered the restart; a second assistant followed.
fn interviewed_then_restarted(w: &World) {
    let interview = w.interview_record();
    assert!(
        interview.contains(INTERVIEW_MARK),
        "no interview was launched: {interview}"
    );
    for marker in [PERSONAL_MARKER, COMPANY_MARKER] {
        assert!(
            !interview.contains(marker),
            "the interview could see {marker}, which it had not yet configured"
        );
    }
    w.ran().contains("estart");
    assert!(
        !w.launch_record().contains(INTERVIEW_MARK),
        "the restarted session was given the interview's opening"
    );
}
