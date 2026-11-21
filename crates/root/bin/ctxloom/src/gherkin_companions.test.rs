//! The steps of `cli/companion.feature`, as a child of the base World.
//!
//! A fake companion is a script (`files::put_program`, unix only) named as
//! its registration's program, `ctxloom-companion-<name>`, in a directory put
//! first on every run's `PATH`. Each time it is invoked it appends its name
//! and its arguments to one witness file in the scenario's directory, so
//! "was never executed" is read off the filesystem, not inferred from a
//! missing line of output. It answers `loadout --format yaml` with a loadout
//! whose one fragment is the companion's guidance marker.

use std::path::PathBuf;

use cucumber::{given, then};

use super::World;
use crate::tests::files;

/// The marker a companion's guidance carries.
fn marker(name: &str) -> String {
    format!("COMPANION-{}-GUIDANCE-MARKER", name.to_uppercase())
}

impl World {
    /// The scenario's directory, beside the project and the home.
    fn companions_scratch(&self) -> PathBuf {
        self.adopt_scratch()
    }

    fn witness(&self) -> PathBuf {
        self.companions_scratch().join("companions-ran.log")
    }

    /// `program` in `dir`, which every later run finds first on its `PATH`.
    fn put_companion(&mut self, dir: PathBuf, program: &str) {
        let name = program
            .strip_prefix("ctxloom-companion-")
            .unwrap_or(program);
        let body = format!(
            "#!/bin/sh\nprintf '%s %s\\n' '{program}' \"$*\" >> '{witness}'\ncase \"$1\" in\n  loadout) printf 'run:\\n  version: 1.0.0\\n  fragments:\\n    guidance:\\n      content: {marker}\\n' ;;\n  version) printf '{{\"name\":\"{program}\",\"version\":\"0.0.0-fixture\"}}\\n' ;;\n  *) exit 2 ;;\nesac\n",
            witness = self.witness().display(),
            marker = marker(name),
        );
        files::put_program(dir.join(program), &body);
        self.common.project.put_on_path(&dir);
    }

    /// The witness lines of `program`'s executions.
    fn executions(&self, program: &str) -> usize {
        files::read(self.witness())
            .unwrap_or_default()
            .lines()
            .filter(|line| line.split(' ').next() == Some(program))
            .count()
    }

    /// The home config's `companions:` section, as written.
    fn home_companions(&self) -> String {
        let home = self
            .common
            .project
            .read_home(".ctxloom/config.yaml")
            .unwrap_or_default();
        let mut lines = home.lines().skip_while(|l| *l != "companions:");
        let Some(head) = lines.next() else {
            return String::new();
        };
        std::iter::once(head)
            .chain(lines.take_while(|l| l.starts_with(' ')))
            .collect::<Vec<_>>()
            .join("\n")
    }

    /// A session's context: the project's own fragment and whatever the
    /// registered companions contribute, as `context assemble` composes it
    /// for a profile of the project's.
    fn companion_context(&mut self) -> String {
        let project = &self.common.project;
        if !project.exists(".ctxloom/content/bundles/v2/own/bundle.yaml") {
            project.bundle("own");
            project.fragment("own", "own");
            project.profile("dev", "own");
        }
        let ran = project.run(&["context", "assemble", "-p", "dev", "--format", "json"]);
        ran.succeeds();
        ran.json()["context"]
            .as_str()
            .unwrap_or_default()
            .to_string()
    }
}

#[given(
    expr = "a dependency drops a companion {string} into an absolute node_modules\\/.bin on PATH"
)]
fn npm_drops(w: &mut World, program: String) {
    let dir = w.common.project.dir().join("node_modules/.bin");
    assert!(dir.is_absolute());
    w.put_companion(dir, &program);
}

#[given(expr = "a companion {string} is on PATH, not registered")]
fn on_path(w: &mut World, program: String) {
    let dir = w.companions_scratch().join("companions-bin");
    w.put_companion(dir, &program);
}

#[given("the companion executions so far are forgotten")]
fn forget(w: &mut World) {
    if files::exists(w.witness()) {
        files::remove(w.witness());
    }
}

#[given(expr = "Alice has added the companion {string}")]
fn has_added(w: &mut World, name: String) {
    w.common
        .run_lines(&format!("ctxloom companion add {name}"), "");
    w.common.ran().succeeds();
}

#[given(expr = "Alice has added the companion {string} and disabled it")]
fn has_added_and_disabled(w: &mut World, name: String) {
    w.common.run_lines(
        &format!("ctxloom companion add {name}\nctxloom companion disable {name}"),
        "",
    );
    w.common.ran().succeeds();
}

#[then(expr = "the companion {string} was never executed")]
fn never_executed(w: &mut World, program: String) {
    assert_eq!(
        w.executions(&program),
        0,
        "{program} ran: {}",
        files::read(w.witness()).unwrap_or_default()
    );
}

#[then(expr = "the companion {string} was executed")]
fn executed(w: &mut World, program: String) {
    assert!(
        w.executions(&program) > 0,
        "{program} never ran: {:?}",
        files::read(w.witness()).ok()
    );
}

/// The registration holds the name and no path or command (OD-10).
#[then(expr = "the home config registers the companion {string} by name only")]
fn by_name_only(w: &mut World, name: String) {
    let section = w.home_companions();
    assert!(
        section.contains(&format!("\n  {name}:")),
        "{name} is not registered: {section:?}"
    );
    assert!(!section.contains("command"), "{section}");
    assert!(!section.contains('/'), "a path is registered: {section}");
    assert!(
        !section.contains(&w.companions_scratch().display().to_string()),
        "{section}"
    );
}

#[then(expr = "the companion {string} is still registered in her home, disabled")]
fn still_registered_disabled(w: &mut World, name: String) {
    let section = w.home_companions();
    assert!(
        section.contains(&format!("\n  {name}:\n    state: disabled")),
        "{section:?}"
    );
    let ran = w
        .common
        .project
        .run(&["companion", "list", "--format", "json"]);
    ran.succeeds();
    let listed = ran.json();
    let found = listed["companions"]
        .as_array()
        .into_iter()
        .flatten()
        .any(|c| {
            c["name"]
                .as_str()
                .is_some_and(|n| n.ends_with(&format!("/companions/{name}")))
                && c["state"] == "DISABLED"
        });
    assert!(found, "not listed as disabled: {listed}");
}

#[then(expr = "the companion {string} is not registered")]
fn not_registered(w: &mut World, name: String) {
    let section = w.home_companions();
    assert!(
        !section.contains(&format!("\n  {name}:")),
        "{name} is still registered: {section}"
    );
    let ran = w
        .common
        .project
        .run(&["companion", "list", "--format", "text"]);
    ran.succeeds();
    assert!(!ran.stdout.contains(&name), "{}", ran.stdout);
}

#[then(expr = "a session's context carries none of {string}'s guidance")]
fn carries_none(w: &mut World, name: String) {
    let before = w.executions(&format!("ctxloom-companion-{name}"));
    let context = w.companion_context();
    assert!(!context.contains(&marker(&name)), "{context}");
    assert_eq!(
        w.executions(&format!("ctxloom-companion-{name}")),
        before,
        "a disabled companion was run"
    );
}

#[then(expr = "a session's context carries {string}'s guidance")]
fn carries(w: &mut World, name: String) {
    let context = w.companion_context();
    assert!(context.contains(&marker(&name)), "{context}");
}
