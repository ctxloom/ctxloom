//! Go's acceptance scenarios for the companion noun's write side (S2),
//! `tests/acceptance/features/cli/companion.feature` at release/0.7
//! ca14d6046, ported: each test names its scenario. A companion is registered
//! by its name alone (OD-10): its program, `ctxloom-companion-<name>` (or a
//! shipped companion's own name), is found on the PATH and answers
//! `<program> loadout --format yaml`; the fakes here are shell scripts in a
//! directory of their own, first on the PATH.
//!
//! Not here: what `ctxloom doctor` runs (S12): the witness-file scenarios'
//! registration and removal halves are, and the executions are read off the
//! fake's own witness file where the command itself runs it.

use std::path::PathBuf;

use super::library::{reports_as, Project};
use crate::tests::files;

const HOME_CONFIG: &str = ".ctxloom/config.yaml";

/// A directory of fake companions, and the `PATH` that finds them first.
struct Bin {
    dir: file_system::Rooted,
}

impl Bin {
    fn new() -> Bin {
        Bin { dir: files::root() }
    }

    /// `ctxloom-companion-<name>`, answering the loadout probe and counting
    /// its runs in `ran.log`.
    fn companion(&self, name: &str) -> PathBuf {
        self.script(
            &format!("ctxloom-companion-{name}"),
            &format!(
                "#!/bin/sh\necho \"$@\" >> \"{}/ran.log\"\ncase \"$1\" in\n  loadout) printf 'run:\\n  version: 1.0.0\\n';;\n  version) printf '{{\"name\":\"{name}\",\"version\":\"1\"}}\\n';;\nesac\n",
                self.dir.root().display()
            ),
        )
    }

    /// A binary that is no companion: it prints nothing and fails.
    fn broken(&self, name: &str) -> PathBuf {
        self.script(&format!("ctxloom-companion-{name}"), "#!/bin/sh\nexit 3\n")
    }

    /// A binary that answers with nothing.
    fn silent(&self, name: &str) -> PathBuf {
        self.script(&format!("ctxloom-companion-{name}"), "#!/bin/sh\nexit 0\n")
    }

    fn script(&self, file: &str, body: &str) -> PathBuf {
        let path = self.dir.root().join(file);
        files::put_program(&path, body);
        path
    }

    fn path(&self) -> String {
        let mut paths = vec![self.dir.root().to_path_buf()];
        paths.extend(files::search_path(
            &crate::tests::host_var("PATH").unwrap_or_default(),
        ));
        std::env::join_paths(paths)
            .expect("a PATH")
            .to_string_lossy()
            .into_owned()
    }

    fn ran(&self) -> String {
        files::read(self.dir.root().join("ran.log")).unwrap_or_default()
    }
}

fn run(project: &Project, bin: &Bin, args: &[&str]) -> super::Ran {
    project.run_with(&[("PATH", &bin.path())], args)
}

fn home_companions(project: &Project) -> String {
    project.read_home(HOME_CONFIG).unwrap_or_default()
}

// companion.feature: Scenario: A registered companion runs, and removing it
// stops it (the registration and the removal: the home config holds the name
// alone; doctor's execution is S12's)
#[test]
fn test_a_registered_companion_is_recorded_by_name_and_removing_it_unrecords_it() {
    if !files::runs_scripts() {
        return;
    }
    let project = Project::initialized();
    let bin = Bin::new();
    bin.companion("acme");
    run(&project, &bin, &["companion", "add", "acme"]).succeeds();
    let home = home_companions(&project);
    assert!(
        home.contains("companions:\n  acme:\n    state: enabled\n"),
        "{home}"
    );
    assert!(
        !home.contains(&bin.dir.root().display().to_string()),
        "no path: {home}"
    );
    assert!(
        home.contains("editor:"),
        "the rest of the home config was kept: {home}"
    );
    assert_eq!(
        project.read(".ctxloom/config.yaml").as_deref(),
        Some("schema_version: 9\n"),
        "the project's config was not touched"
    );
    run(&project, &bin, &["companion", "remove", "acme", "--yes"]).succeeds();
    assert!(!home_companions(&project).contains("acme"));
    assert_eq!(
        project.read_home(HOME_CONFIG).as_deref(),
        Some("schema_version: 9\neditor:\n  command: \"true\"\n"),
        "the empty map is not left behind"
    );
}

// companion.feature: Scenario: A project's companions are added to the home
// registrations
#[test]
fn test_a_projects_companions_are_added_to_the_home_registrations_by_add() {
    if !files::runs_scripts() {
        return;
    }
    let project = Project::initialized();
    let bin = Bin::new();
    bin.companion("acme");
    run(&project, &bin, &["companion", "add", "acme"]).succeeds();
    project.write(
        ".ctxloom/config.yaml",
        "schema_version: 9\ncompanions:\n  beta: {}\n",
    );
    let ran = run(&project, &bin, &["companion", "list", "--format", "json"]);
    ran.succeeds();
    let doc = ran.json();
    assert!(reports_as(&doc, "companions.0.name").ends_with("/companions/acme"));
    assert!(reports_as(&doc, "companions.1.name").ends_with("/companions/beta"));
}

// companion.feature: Scenario: An empty project map does not unregister a
// home companion
#[test]
fn test_an_empty_project_map_does_not_unregister_a_home_companion_after_add() {
    if !files::runs_scripts() {
        return;
    }
    let project = Project::initialized();
    let bin = Bin::new();
    bin.companion("acme");
    run(&project, &bin, &["companion", "add", "acme"]).succeeds();
    project.write(
        ".ctxloom/config.yaml",
        "schema_version: 9\ncompanions: {}\n",
    );
    let ran = run(&project, &bin, &["companion", "list", "--format", "json"]);
    ran.succeeds();
    assert!(reports_as(&ran.json(), "companions.0.name").ends_with("/companions/acme"));
}

// companion.feature: Scenario: A name with nothing on PATH is refused, naming
// the program it looked for, and nothing is recorded
#[test]
fn test_a_name_whose_program_is_not_on_path_is_refused_naming_it() {
    let project = Project::initialized();
    let bin = Bin::new();
    let before = project.read_home(HOME_CONFIG);
    run(&project, &bin, &["companion", "add", "nowhere"])
        .fails()
        .contains("ctxloom-companion-nowhere")
        .contains("is not on PATH");
    assert_eq!(project.read_home(HOME_CONFIG), before);
    run(&project, &bin, &["companion", "list", "--format", "text"])
        .succeeds()
        .contains("No companions registered");
}

// operations.AddCompanion (documented conditions): a name that is no name, a
// program that does not answer the probe, one that answers nothing: each is
// refused and records nothing. A shipped companion's program is its own name.
#[test]
fn test_add_checks_the_companion_before_it_records_anything() {
    if !files::runs_scripts() {
        return;
    }
    let project = Project::initialized();
    let bin = Bin::new();
    bin.broken("sick");
    bin.silent("mute");
    let before = project.read_home(HOME_CONFIG);
    run(&project, &bin, &["companion", "add", "sick"])
        .fails()
        .contains("does not answer the companion loadout probe");
    run(&project, &bin, &["companion", "add", "mute"])
        .fails()
        .contains("printed no loadout");
    for bad in ["-x", "a b", "../x", ""] {
        run(&project, &bin, &["companion", "add", bad])
            .fails()
            .contains("invalid companion name");
    }
    assert_eq!(project.read_home(HOME_CONFIG), before);
    bin.script("ltk", "#!/bin/sh\nprintf 'run:\\n  version: 1.0.0\\n'\n");
    let ran = run(
        &project,
        &bin,
        &["companion", "add", "ltk", "--format", "json"],
    );
    ran.succeeds();
    assert!(reports_as(&ran.json(), "companion.name").ends_with("/companions/ltk"));
    assert_eq!(reports_as(&ran.json(), "companion.bin"), "ltk");
}

// operations.AddCompanion (documented condition): the answer, in each format,
// a second add of the same name is not a second registration, and the probe
// is run once.
#[test]
fn test_add_answers_and_a_second_add_registers_nothing_twice() {
    if !files::runs_scripts() {
        return;
    }
    let project = Project::initialized();
    let bin = Bin::new();
    bin.companion("acme");
    let first = run(
        &project,
        &bin,
        &["companion", "add", "acme", "--format", "json"],
    );
    first.succeeds();
    let doc = first.json();
    // OD-40: `CreateCompanionResponse`, applied when the registration is new.
    assert!(reports_as(&doc, "companion.name").ends_with("/companions/acme"));
    assert_eq!(reports_as(&doc, "companion.bin"), "ctxloom-companion-acme");
    assert_eq!(reports_as(&doc, "applied"), "true");
    assert_eq!(bin.ran().lines().count(), 1, "one probe: {}", bin.ran());
    let again = run(
        &project,
        &bin,
        &["companion", "add", "acme", "--format", "text"],
    );
    assert_eq!(
        again.stdout,
        "Companion 'acme' is already registered (ctxloom-companion-acme)\n"
    );
    assert_eq!(home_companions(&project).matches("acme:").count(), 1);
    let project2 = Project::initialized();
    let text = run(
        &project2,
        &bin,
        &["companion", "add", "acme", "--format", "text"],
    );
    assert_eq!(
        text.stdout,
        "Registered companion 'acme' (its program ctxloom-companion-acme answers)\n"
    );
}

// companion.feature: Scenario: Remove without --yes names the apply command
// and changes nothing
#[test]
fn test_remove_without_yes_names_the_apply_command_and_changes_nothing() {
    if !files::runs_scripts() {
        return;
    }
    let project = Project::initialized();
    let bin = Bin::new();
    bin.companion("acme");
    let add = |project: &Project| {
        run(project, &bin, &["companion", "add", "acme"]).succeeds();
    };
    add(&project);
    let before = project.read_home(HOME_CONFIG);
    let ran = run(
        &project,
        &bin,
        &["companion", "remove", "acme", "--format", "text"],
    );
    ran.succeeds()
        .contains("ctxloom companion remove acme --yes");
    assert_eq!(
        ran.stdout,
        "Would remove companion \"acme\".\n\nNothing was removed. Re-run with --yes to apply:\n  ctxloom companion remove acme --yes\n"
    );
    assert_eq!(project.read_home(HOME_CONFIG), before);
    let json = run(&project, &bin, &["companion", "remove", "acme"]);
    assert_eq!(reports_as(&json.json(), "applied"), "false");
    assert_eq!(project.read_home(HOME_CONFIG), before);
    let applied = run(
        &project,
        &bin,
        &["companion", "remove", "acme", "--yes", "--format", "json"],
    );
    // OD-40: `DeleteCompanionResponse`, applied, naming what went.
    assert_eq!(reports_as(&applied.json(), "applied"), "true");
    assert!(reports_as(&applied.json(), "companion.name").ends_with("/companions/acme"));
    let text = {
        add(&project);
        run(
            &project,
            &bin,
            &["companion", "remove", "acme", "--yes", "--format", "text"],
        )
    };
    assert_eq!(text.stdout, "Removed companion 'acme'\n");
    for yes in [vec![], vec!["--yes"]] {
        let mut args = vec!["companion", "remove", "acme"];
        args.extend(yes);
        run(&project, &bin, &args)
            .fails()
            .contains("companion not registered: \"acme\"");
    }
}

// companion.feature: Scenario: List answers which program each registered
// name resolves to (after add); the program is looked for, never run.
#[test]
fn test_list_answers_each_names_program_and_whether_it_resolves_after_add() {
    if !files::runs_scripts() {
        return;
    }
    let project = Project::initialized();
    let bin = Bin::new();
    bin.companion("acme");
    run(&project, &bin, &["companion", "add", "acme"]).succeeds();
    let probes = bin.ran().lines().count();
    let ran = run(&project, &bin, &["companion", "list", "--format", "json"]);
    ran.succeeds();
    let doc = ran.json();
    assert!(reports_as(&doc, "companions.0.name").ends_with("/companions/acme"));
    assert_eq!(
        reports_as(&doc, "companions.0.bin"),
        "ctxloom-companion-acme"
    );
    assert_eq!(reports_as(&doc, "companions.0.resolves"), "true");
    assert_eq!(bin.ran().lines().count(), probes, "listing ran nothing");
    // Off the PATH it was found on, it no longer resolves (OD-10: the name
    // is found where it is asked).
    let elsewhere = project.run_with(
        &[("PATH", "/usr/bin:/bin")],
        &["companion", "list", "--format", "json"],
    );
    assert_eq!(
        reports_as(&elsewhere.json(), "companions.0.resolves"),
        "false"
    );
}

// companion.feature: Scenario: Bare companion lists the registered companions
// (after add)
#[test]
fn test_bare_companion_lists_the_registered_companions_after_add() {
    if !files::runs_scripts() {
        return;
    }
    let project = Project::initialized();
    let bin = Bin::new();
    bin.companion("acme");
    run(&project, &bin, &["companion", "add", "acme"]).succeeds();
    run(&project, &bin, &["companion", "--format", "text"])
        .succeeds()
        .contains("acme")
        .contains("ctxloom-companion-acme")
        .contains("found")
        .lacks("Available Commands:");
}

// companion.feature: Scenario: A companion an npm dependency drops on PATH is
// never run (adding or removing a name runs only that name's program)
#[test]
fn test_only_the_registered_name_is_ever_run_by_add() {
    if !files::runs_scripts() {
        return;
    }
    let project = Project::initialized();
    let bin = Bin::new();
    bin.companion("acme");
    bin.companion("evil");
    run(&project, &bin, &["companion", "add", "acme"]).succeeds();
    let ran = bin.ran();
    assert!(ran.contains("loadout"), "{ran}");
    run(&project, &bin, &["companion", "list"]).succeeds();
    run(&project, &bin, &["companion", "remove", "acme", "--yes"]).succeeds();
    let log = bin.ran();
    assert_eq!(log.lines().count(), 1, "something else ran: {log}");
}

// companion.feature: disable keeps a companion registered and listed, its
// state written as a word (OD-9); enable restores it.
#[test]
fn test_disable_and_enable_write_the_state_word_in_the_home() {
    if !files::runs_scripts() {
        return;
    }
    let project = Project::initialized();
    let bin = Bin::new();
    bin.companion("acme");
    run(&project, &bin, &["companion", "add", "acme"]).succeeds();
    run(
        &project,
        &bin,
        &["companion", "disable", "acme", "--format", "text"],
    )
    .succeeds()
    .contains("Companion 'acme' is disabled");
    assert!(home_companions(&project).contains("  acme:\n    state: disabled\n"));
    let listed = run(&project, &bin, &["companion", "list", "--format", "json"]);
    assert_eq!(reports_as(&listed.json(), "companions.0.state"), "DISABLED");
    run(
        &project,
        &bin,
        &["companion", "enable", "acme", "--format", "json"],
    )
    .succeeds();
    assert!(home_companions(&project).contains("  acme:\n    state: enabled\n"));
}
