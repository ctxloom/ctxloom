//! The ports' value types: what their Debug withholds, and what a launch's
//! record gives back.

use std::collections::BTreeMap;

use run_ports::{Sealing, TestSealing};

use super::{child_env_field, ChildInputs, Program};

// Review 4: a launcher's inputs hold its environment and its user's
// settings, either of which may hold a credential; the Debug names each
// variable and shows no value.
#[test]
fn test_a_childs_inputs_debug_names_its_variables_and_shows_no_value() {
    let handed = ChildInputs {
        env: BTreeMap::from([("TOKEN".to_string(), "sk-not-shown".to_string())]), // gitleaks:allow
        launcher: BTreeMap::from([("SETTINGS".to_string(), "hidden-too".to_string())]),
        agent: "reviewer".to_string(),
        project: String::new(),
        program: None,
    };
    let shown = format!("{handed:?}");
    assert!(
        shown.contains("TOKEN") && shown.contains("SETTINGS"),
        "{shown}"
    );
    assert!(!shown.contains("sk-not-shown"), "{shown}");
    assert!(!shown.contains("hidden-too"), "{shown}");
    assert!(shown.contains("reviewer"), "{shown}");
}

fn entry(name: &str, sealed: &[u8], plain: &str) -> sessions::ChildEnvEntry {
    sessions::ChildEnvEntry {
        name: name.to_string(),
        sealed: sealed.to_vec(),
        plain: plain.to_string(),
    }
}

// What a launch recorded is what a start reads: a sealed value opened for its
// variable, a plain one (a path that places her settings) as it is, the
// program and the agent.
#[test]
fn a_launch_recorded_reads_back_as_the_childs_inputs() {
    let launched = sessions::ChildLaunched {
        agent: "reviewer".to_string(),
        client_program: sessions::ChildProgram {
            name: "ctxloom".to_string(),
            directory: "/opt/ctxloom".to_string(),
        },
        settings_places: vec![entry("CLAUDE_CONFIG_DIR", &[], "/home/a/.claude")],
        env: vec![entry(
            "TOKEN",
            &TestSealing
                .seal("", &child_env_field("TOKEN"), b"sk-secret")
                .unwrap(),
            "",
        )],
        ..Default::default()
    };
    let read = ChildInputs::of_launched(&launched, &TestSealing).unwrap();
    assert_eq!(read.env["TOKEN"], "sk-secret");
    assert_eq!(read.launcher["CLAUDE_CONFIG_DIR"], "/home/a/.claude");
    assert_eq!(read.agent, "reviewer");
    assert_eq!(
        read.program,
        Some(Program {
            name: "ctxloom".to_string(),
            dir: "/opt/ctxloom".to_string()
        })
    );
}

// A value sealed for another variable does not open: a child is never given
// a secret moved from one name to another.
#[test]
fn a_value_sealed_for_another_variable_is_refused() {
    let moved = TestSealing
        .seal("", &child_env_field("OTHER"), b"sk-secret")
        .unwrap();
    let launched = sessions::ChildLaunched {
        env: vec![entry("TOKEN", &moved, "")],
        ..Default::default()
    };
    assert!(ChildInputs::of_launched(&launched, &TestSealing).is_err());
}

// A launch that named no program and no places gives none.
#[test]
fn a_launch_recording_no_program_gives_none() {
    let read = ChildInputs::of_launched(&sessions::ChildLaunched::default(), &TestSealing).unwrap();
    assert_eq!(read, ChildInputs::default());
}
