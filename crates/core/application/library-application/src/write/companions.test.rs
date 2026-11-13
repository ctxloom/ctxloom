//! `companion.feature`'s write scenarios at the application port: a
//! registration is a name (OD-10), its state a word (OD-9), each write one
//! section replace and a write that changes nothing writes nothing (D3).

use std::sync::Arc;

use ctxloom_ports::{CompanionLoadout, Scope};

use crate::fakes::{plain, Mem, Setup, CONFIG};
use crate::write::views::Removal;
use crate::{CompanionState, Library};

const HOME: &str = "schema_version: 9\neditor:\n  command: 'true'\n";

fn loaded() -> CompanionLoadout {
    CompanionLoadout::Loaded("run:\n  version: 1.0.0\n".to_string())
}

fn world() -> (Library, Arc<Mem>) {
    let (library, mem) = Setup {
        layers: &[HOME, CONFIG],
        ..Setup::default()
    }
    .fixture();
    for name in ["acme", "ltk"] {
        mem.set_companion(name, loaded());
    }
    (library, mem)
}

fn err<T>(out: crate::Outcome<T>) -> String {
    format!("{:?}", out.result.err().expect("an error"))
}

// companion.feature: A registered companion runs, and removing it stops it (the registration)
#[test]
fn test_add_records_the_name_alone_in_the_home_config() {
    let (library, mem) = world();
    let done = library.companion_add(&plain(), "acme").result.unwrap();
    assert_eq!(
        (done.name.as_str(), done.program.as_str(), done.added),
        ("acme", "ctxloom-companion-acme", true)
    );
    assert_eq!(
        mem.config(Scope::Home).unwrap(),
        format!("{HOME}companions:\n  acme:\n    state: enabled\n")
    );
    assert_eq!(mem.config(Scope::Project).unwrap(), CONFIG);
}

// A shipped companion is its own program; any other name is prefixed.
#[test]
fn test_add_probes_the_program_the_name_stands_for_once_before_it_records() {
    let (library, mem) = world();
    library.companion_add(&plain(), "acme").result.unwrap();
    library.companion_add(&plain(), "ltk").result.unwrap();
    assert_eq!(mem.asked_companions(), ["ctxloom-companion-acme", "ltk"]);
}

// D1: the probe is asked at the caller's site: its environment, and the
// project's root as the directory the program runs in.
#[test]
fn test_add_probes_at_the_callers_environment_and_the_projects_root() {
    let (library, mem) = Setup {
        layers: &[HOME, CONFIG],
        environ: &[("PATH", "/caller/bin"), ("MARK", "seen")],
        ..Setup::default()
    }
    .fixture();
    mem.set_companion("acme", loaded());
    library.companion_add(&plain(), "acme").result.unwrap();
    let sites = mem.probe_sites();
    assert_eq!(sites.len(), 1);
    assert_eq!(
        sites[0].env,
        [
            ("PATH".to_string(), "/caller/bin".to_string()),
            ("MARK".to_string(), "seen".to_string())
        ]
    );
    assert_eq!(sites[0].cwd, "/p");
}

// D3: a second add of a registered name writes nothing, and leaves its
// state as it was.
#[test]
fn test_a_second_add_writes_nothing_and_keeps_the_state() {
    let (library, mem) = world();
    library.companion_add(&plain(), "acme").result.unwrap();
    library
        .companion_set_state(&plain(), "acme", CompanionState::Disabled)
        .result
        .unwrap();
    let writes = mem.writes();
    let again = library.companion_add(&plain(), "acme").result.unwrap();
    assert!(!again.added);
    assert_eq!(mem.writes(), writes);
    library.companion_add(&plain(), "ltk").result.unwrap();
    let home = mem.config(Scope::Home).unwrap();
    assert!(
        home.ends_with("companions:\n  acme:\n    state: disabled\n  ltk:\n    state: enabled\n"),
        "{home}"
    );
}

// companion.feature: A name with nothing on PATH is refused, naming the
// program it looked for; nothing is recorded.
#[test]
fn test_add_refuses_a_missing_program_a_bad_name_and_a_program_that_does_not_answer() {
    let (library, mem) = world();
    mem.set_companion("gone", CompanionLoadout::Absent);
    mem.set_companion(
        "sick",
        CompanionLoadout::NoLoadout("exit status: 3".to_string()),
    );
    mem.set_companion(
        "broken",
        CompanionLoadout::ProbeFailed("did not answer within 3000ms".to_string()),
    );
    mem.set_companion("mute", CompanionLoadout::Silent);
    let missing = err(library.companion_add(&plain(), "gone"));
    assert!(missing.contains("CompanionProgramMissing"), "{missing}");
    assert!(missing.contains("ctxloom-companion-gone"), "{missing}");
    for bad in ["", "-x", "a b", "../x", "a/b", ".x"] {
        assert!(
            err(library.companion_add(&plain(), bad)).contains("CompanionName"),
            "{bad:?}"
        );
    }
    assert!(err(library.companion_add(&plain(), "sick")).contains("NotACompanion"));
    assert!(err(library.companion_add(&plain(), "broken")).contains("did not answer within"));
    assert!(err(library.companion_add(&plain(), "mute")).contains("CompanionSilent"));
    assert_eq!(mem.writes(), 0);
    assert_eq!(mem.config(Scope::Home).unwrap(), HOME);
}

#[test]
fn test_add_makes_the_home_config_where_there_is_none() {
    let (library, mem) = Setup {
        layers: &[CONFIG],
        ..Setup::default()
    }
    .fixture();
    mem.set_companion("acme", loaded());
    library.companion_add(&plain(), "acme").result.unwrap();
    assert_eq!(
        mem.config(Scope::Home).unwrap(),
        "schema_version: 9\ncompanions:\n  acme:\n    state: enabled\n"
    );
}

// A home config Go wrote (generation 8, a list of names) is rewritten as
// the mapping (the section sorted), the names it held kept; its stamp is
// the file's own.
#[test]
fn test_add_over_a_generation_8_list_keeps_its_names() {
    let (library, mem) = Setup {
        layers: &["schema_version: 8\ncompanions: [beta]\n", CONFIG],
        ..Setup::default()
    }
    .fixture();
    mem.set_companion("acme", loaded());
    library.companion_add(&plain(), "acme").result.unwrap();
    assert_eq!(
        mem.config(Scope::Home).unwrap(),
        "schema_version: 8\ncompanions:\n  acme:\n    state: enabled\n  beta: {}\n"
    );
}

// companion.feature: disable keeps a companion registered; enable restores it
#[test]
fn test_disable_and_enable_write_the_state_word_and_a_repeat_writes_nothing() {
    let (library, mem) = world();
    library.companion_add(&plain(), "acme").result.unwrap();
    let off = library
        .companion_set_state(&plain(), "acme", CompanionState::Disabled)
        .result
        .unwrap();
    assert!(off.changed);
    assert_eq!(off.state, CompanionState::Disabled);
    assert_eq!(
        mem.config(Scope::Home).unwrap(),
        format!("{HOME}companions:\n  acme:\n    state: disabled\n")
    );
    let writes = mem.writes();
    let again = library
        .companion_set_state(&plain(), "acme", CompanionState::Disabled)
        .result
        .unwrap();
    assert!(!again.changed);
    assert_eq!(mem.writes(), writes, "a repeat writes nothing (D3)");
    let on = library
        .companion_set_state(&plain(), "acme", CompanionState::Enabled)
        .result
        .unwrap();
    assert!(on.changed);
    assert_eq!(mem.writes(), writes + 1, "one write");
    assert_eq!(
        mem.config(Scope::Home).unwrap(),
        format!("{HOME}companions:\n  acme:\n    state: enabled\n")
    );
}

#[test]
fn test_enable_of_a_name_registered_without_a_state_writes_it_and_changes_nothing() {
    let (library, mem) = Setup {
        layers: &["schema_version: 9\ncompanions:\n  acme: {}\n", CONFIG],
        ..Setup::default()
    }
    .fixture();
    let on = library
        .companion_set_state(&plain(), "acme", CompanionState::Enabled)
        .result
        .unwrap();
    assert!(!on.changed, "it was enabled by default");
    let writes = mem.writes();
    library
        .companion_set_state(&plain(), "acme", CompanionState::Enabled)
        .result
        .unwrap();
    assert_eq!(mem.writes(), writes);
}

#[test]
fn test_enable_or_disable_of_a_name_the_home_does_not_register_is_refused() {
    let (library, mem) = Setup {
        layers: &[HOME, "schema_version: 9\ncompanions:\n  beta: {}\n"],
        ..Setup::default()
    }
    .fixture();
    for state in [CompanionState::Enabled, CompanionState::Disabled] {
        for name in ["acme", "beta"] {
            assert!(
                err(library.companion_set_state(&plain(), name, state))
                    .contains("CompanionNotRegistered"),
                "{name}"
            );
        }
    }
    assert_eq!(mem.writes(), 0);
}

// companion.feature: Remove without --yes names the apply command and changes nothing
#[test]
fn test_remove_previews_then_unregisters_exactly_that_name() {
    let (library, mem) = world();
    library.companion_add(&plain(), "acme").result.unwrap();
    library.companion_add(&plain(), "ltk").result.unwrap();
    let before = mem.config(Scope::Home);
    let Removal::Preview(p) = library
        .companion_remove(&plain(), "acme", false)
        .result
        .unwrap()
    else {
        panic!("a preview")
    };
    assert_eq!(
        (p.target.as_str(), p.apply.as_str()),
        ("companion \"acme\"", "ctxloom companion remove acme --yes")
    );
    assert_eq!(mem.config(Scope::Home), before);
    let Removal::Done(done) = library
        .companion_remove(&plain(), "acme", true)
        .result
        .unwrap()
    else {
        panic!("done")
    };
    assert_eq!((done.name.as_str(), done.removed), ("acme", true));
    assert!(mem
        .config(Scope::Home)
        .unwrap()
        .ends_with("companions:\n  ltk:\n    state: enabled\n"));
    library
        .companion_remove(&plain(), "ltk", true)
        .result
        .unwrap();
    assert_eq!(
        mem.config(Scope::Home).unwrap(),
        HOME,
        "the empty map goes with its last name"
    );
}

#[test]
fn test_remove_of_a_name_not_registered_is_refused_with_and_without_yes() {
    let (library, mem) = world();
    for yes in [false, true] {
        assert!(
            err(library.companion_remove(&plain(), "acme", yes)).contains("CompanionNotRegistered")
        );
    }
    assert_eq!(mem.writes(), 0);
}

// companion.feature: a project may add companions, never remove home's
#[test]
fn test_a_name_only_the_project_registers_cannot_be_removed_from_home() {
    let (library, mem) = Setup {
        layers: &[HOME, "schema_version: 9\ncompanions:\n  beta: {}\n"],
        ..Setup::default()
    }
    .fixture();
    assert!(
        err(library.companion_remove(&plain(), "beta", true)).contains("CompanionNotRegistered")
    );
    assert_eq!(mem.writes(), 0);
}

#[test]
fn test_remove_of_another_name_than_the_registered_one_is_refused() {
    let (library, mem) = world();
    library.companion_add(&plain(), "acme").result.unwrap();
    let writes = mem.writes();
    for yes in [false, true] {
        assert!(
            err(library.companion_remove(&plain(), "ltk", yes)).contains("CompanionNotRegistered")
        );
    }
    assert_eq!(mem.writes(), writes);
}
