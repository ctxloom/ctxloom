//! The steps of j000200 (setting up ctxloom and the context she trusts), as a
//! child of the base World, after j000100's: the same stub assistant on
//! `PATH`, the same published default within reach, the same fixture token.
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
                "{held}[url \"file://{}\"]\n\tinsteadOf = {url}.git\n\tinsteadOf = {url}\n",
                bare.display()
            ),
        );
    }
}

/// A project that has never seen ctxloom, with her assistant, her token and
/// ctxloom's published default (which `init` seeds) within reach.
#[given("Alice has a project that has never used ctxloom")]
fn never_used(w: &mut World) {
    w.common_mut().project = crate::tests::library::Project::bare();
    super::adopt::claude_code(w);
    super::adopt::token(w);
    super::adopt::published_default(w);
}

#[given("Alice has a personal ctxloom repository")]
fn personal(w: &mut World) {
    w.serve_repository("personal", PERSONAL_URL, PERSONAL_BUNDLE, PERSONAL_MARKER);
}

#[given("her company has a ctxloom repository")]
fn company(w: &mut World) {
    w.serve_repository("company", COMPANY_URL, COMPANY_BUNDLE, COMPANY_MARKER);
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
