//! The steps of j000300 (companions shape how a project is set up) and
//! init.feature's companion scenarios, as a child of the base World, after
//! j000100's and j000200's: the same stub assistant (it records each launch,
//! the interview's opening among its arguments), the fixture token, and a
//! terminal stood in by `CTXLOOM_TEST_TERMINAL`, its answers on standard
//! input.
//!
//! A companion is a script named as its program in a `companions` directory
//! first on the `PATH` (no shipped companion of this machine's is on it:
//! `files::path_without_shipped_companions`); it answers `loadout --format
//! yaml` with its setup guidance, a marker, as `init.setup_guidance`.
//!
//! Init's questions come in a fixed order: where outputs go, when
//! conditional guidance loads, an offer of each shipped companion on her
//! machine, the companions by name; then the interview, then the restart.
//! A table answers by question; a question it does not name is answered with
//! a bare Enter (the recommendation), and the restart is declined.

use std::path::PathBuf;

use cucumber::gherkin::Step;
use cucumber::{given, then, when};

use super::super::gherkin_common::{doc, split, CommonWorld};
use super::World;
use crate::tests::files;

/// What the scenarios keep.
#[derive(Default)]
pub(super) struct State {
    /// The shipped companions put on her machine, in the order init offers
    /// them (ltk, taskloom, reprise).
    shipped: Vec<String>,
}

const SHIPPED: [&str; 3] = ["ltk", "taskloom", "reprise"];

/// The marker a companion's setup guidance carries.
fn guidance(name: &str) -> String {
    format!("J000300-{}-SETUP-GUIDANCE-MARKER", name.to_uppercase())
}

impl World {
    fn companions_bin(&self) -> PathBuf {
        self.adopt_scratch().join("companions")
    }

    /// Her machine, once: a project that has never used ctxloom, her
    /// assistant (the stub) and her token, and a `PATH` of the stub's
    /// directory and the companions'.
    fn setup_machine(&mut self) {
        if files::exists(self.adopt_scratch().join("bin/claude")) {
            return;
        }
        self.common.project = crate::tests::library::Project::bare();
        super::adopt::claude_code(self);
        super::adopt::token(self);
        self.common.set_env("CTXLOOM_ROOT", String::new());
        files::mkdirs(self.companions_bin());
        let path = files::path_without_shipped_companions(&[
            self.adopt_scratch().join("bin"),
            self.companions_bin(),
        ]);
        self.common.set_env("PATH", path);
    }

    /// The companion `name`'s program on her machine.
    fn companion_on_machine(&mut self, name: &str) {
        self.setup_machine();
        let program = if SHIPPED.contains(&name) {
            name.to_string()
        } else {
            format!("ctxloom-companion-{name}")
        };
        files::put_program(
            self.companions_bin().join(&program),
            &format!(
                "#!/bin/sh\ncase \"$1\" in\n  loadout) printf 'init:\\n  setup_guidance: {}\\n' ;;\n  *) exit 2 ;;\nesac\n",
                guidance(name)
            ),
        );
        if SHIPPED.contains(&name) && !self.init_companions.shipped.iter().any(|n| n == name) {
            self.init_companions.shipped.push(name.to_string());
            self.init_companions
                .shipped
                .sort_by_key(|n| SHIPPED.iter().position(|s| s == n));
        }
    }

    /// What she types for init's questions, from a table of question and
    /// answer (none: every recommendation), then declining the restart.
    fn typed_for_init(&self, table: &[(String, String)]) -> String {
        let answer = |question: &str| -> String {
            table
                .iter()
                .find(|(q, _)| q == question)
                .map(|(_, a)| a.clone())
                .unwrap_or_default()
        };
        let mut typed = String::from("\n\n");
        for name in &self.init_companions.shipped {
            typed.push_str(&answer(&format!("Add the {name} companion?")));
            typed.push('\n');
        }
        typed.push_str(&answer("Add companions by name?"));
        typed.push_str("\nn\n");
        typed
    }

    /// `ctxloom init` at her terminal, `typed` its answers.
    fn init_at_terminal(&mut self, line: &str, typed: &str) {
        self.common
            .set_env("CTXLOOM_TEST_TERMINAL", "1".to_string());
        self.before_run();
        self.common.run_lines(line, typed);
        self.common
            .env
            .retain(|(name, _)| name != "CTXLOOM_TEST_TERMINAL");
    }

    /// Everything the assistant was launched with: the interview's opening
    /// among it.
    fn interview(&self) -> String {
        self.launch_record()
    }

    /// What the last command said, both streams.
    fn said(&self) -> String {
        let ran = self.common.ran();
        format!("{}{}", ran.stdout, ran.stderr)
    }
}

/// The `Alice ...:` steps of these scenarios: whether `step` is one, run.
pub(super) fn runs(w: &mut World, step: &Step) -> bool {
    let text = step.value.as_str();
    if text.ends_with("at a terminal, answering:") {
        let table: Vec<(String, String)> = step
            .table
            .as_ref()
            .map(|t| {
                t.rows
                    .iter()
                    .skip(1)
                    .map(|row| (row[0].clone(), row[1].clone()))
                    .collect()
            })
            .unwrap_or_default();
        w.setup_machine();
        let line = text.split('"').nth(1).unwrap_or("ctxloom init").to_string();
        let typed = w.typed_for_init(&table);
        w.init_at_terminal(&line, &typed);
        return true;
    }
    if text == "Alice runs init at a terminal, taking every recommendation:" {
        w.setup_machine();
        let typed = w.typed_for_init(&[]);
        w.init_at_terminal(&doc(step), &typed);
        return true;
    }
    if text == "Alice adds both companions, disables the company's, and takes the setup interview:"
    {
        w.setup_machine();
        for line in doc(step).lines().map(str::trim).filter(|l| !l.is_empty()) {
            if split(line).get(1).map(String::as_str) == Some("init") {
                let typed = w.typed_for_init(&[]);
                w.init_at_terminal(line, &typed);
            } else {
                w.before_run();
                w.common.run_lines(line, "");
                w.common.ran().succeeds();
            }
        }
        return true;
    }
    false
}

#[given("her company ships a companion \"acme\" carrying the company's onboarding steps")]
fn company_ships(w: &mut World) {
    w.companion_on_machine("acme");
}

#[given("her own tooling ships a companion \"alice-tools\" carrying her setup preferences")]
fn tooling_ships(w: &mut World) {
    w.companion_on_machine("alice-tools");
}

#[given("claude-code is the only engine installed, and nothing reaches the network")]
fn only_claude(w: &mut World) {
    w.setup_machine();
}

#[given(regex = r#"^the "(\S+)" program is on Alice's machine$"#)]
fn program_on(w: &mut World, name: String) {
    w.companion_on_machine(&name);
}

#[given(regex = r#"^the (?:shipped companion )?"(\S+)" program is not on Alice's machine$"#)]
fn program_not_on(w: &mut World, _name: String) {
    w.setup_machine();
}

#[given(regex = r#"^the shipped companions "(\S+)" and "(\S+)" are on Alice's machine$"#)]
fn shipped_on(w: &mut World, one: String, two: String) {
    w.companion_on_machine(&one);
    w.companion_on_machine(&two);
}

#[given(regex = r#"^the shipped companion "(\S+)" is on Alice's machine$"#)]
fn one_shipped_on(w: &mut World, name: String) {
    w.companion_on_machine(&name);
}

#[given(regex = r#"^the shipped companion "(\S+)" is not on Alice's machine$"#)]
fn one_shipped_not_on(w: &mut World, _name: String) {
    w.setup_machine();
}

#[when("she takes the setup interview it starts")]
fn takes_interview(w: &mut World) {
    let interview = w.interview();
    assert!(
        !interview.is_empty(),
        "no interview was launched: {}",
        w.said()
    );
}

#[then("the setup interview includes ctxloom's own setup guidance")]
fn includes_own(w: &mut World) {
    let interview = w.interview();
    assert!(
        interview.contains("Phase 5 \u{2014} Close"),
        "the interview lacks ctxloom's own: {interview}"
    );
}

#[then("it includes the company's onboarding steps")]
fn includes_company(w: &mut World) {
    let interview = w.interview();
    assert!(interview.contains(&guidance("acme")), "{interview}");
}

#[then("it does not include the company's onboarding steps")]
fn lacks_company(w: &mut World) {
    let interview = w.interview();
    assert!(!interview.contains(&guidance("acme")), "{interview}");
}

#[then("it includes her personal setup preferences")]
fn includes_personal(w: &mut World) {
    let interview = w.interview();
    assert!(interview.contains(&guidance("alice-tools")), "{interview}");
}

#[then(expr = "init told her it added the companion {string}")]
fn told_added(w: &mut World, name: String) {
    let said = w.said();
    assert!(
        said.contains(&format!("Added companion \"{name}\"")),
        "{said}"
    );
}

#[then(expr = "init told her it could not add {string} because its program is not on her machine")]
fn told_missing(w: &mut World, name: String) {
    let said = w.said();
    assert!(
        said.contains(&format!("could not add companion \"{name}\""))
            && said.contains("is not on this machine"),
        "{said}"
    );
}

#[then(expr = "the companion {string} is registered in her home")]
fn registered_in_home(w: &mut World, name: String) {
    let home = w
        .common
        .project
        .read_home(".ctxloom/config.yaml")
        .unwrap_or_default();
    assert!(
        home.contains(&format!("\n  {name}:\n    state: enabled")),
        "{home}"
    );
}

#[then(regex = r#"^init offered her the companions "(\S+)" and "(\S+)"$"#)]
fn offered_two(w: &mut World, one: String, two: String) {
    let said = w.said();
    for name in [one, two] {
        assert!(
            said.contains(&format!("Add the {name} companion?")),
            "{said}"
        );
    }
}

#[then(regex = r#"^init offered her the companion "(\S+)"$"#)]
fn offered_one(w: &mut World, name: String) {
    let said = w.said();
    assert!(
        said.contains(&format!("Add the {name} companion?")),
        "{said}"
    );
}

#[then(regex = r#"^init did not offer her the companion "(\S+)"$"#)]
fn not_offered(w: &mut World, name: String) {
    let said = w.said();
    assert!(
        !said.contains(&format!("Add the {name} companion?")),
        "{said}"
    );
}
