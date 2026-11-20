use angzarr_diagnostic::Diagnostic;
use run_ports::{Engine, Planned};

use super::{diagnostic_samples, OpencodeNotice, EN_US};
use crate::Opencode;

fn planned(agent: &str, model: &str) -> Planned {
    Planned {
        agent: agent.to_string(),
        model: model.to_string(),
        ..Planned::default()
    }
}

// Ruling 168: an opencode agent with no model is told opencode chooses its
// own, by its name, the key to set and the command that sets it.
#[test]
fn test_an_agent_without_a_model_is_told_opencode_chooses_its_own() {
    let said = Opencode::new().advise(&planned("dev", ""));
    assert_eq!(said.len(), 1);
    assert_eq!(said[0].code(), "engine-opencode-no-model");
    let args: Vec<String> = said[0]
        .args()
        .into_iter()
        .map(|(name, value)| format!("{name}={value:?}"))
        .collect();
    assert_eq!(args.len(), 1, "{args:?}");
    assert!(
        args[0].starts_with("agent=") && args[0].contains("dev"),
        "{args:?}"
    );
}

#[test]
fn test_an_agent_with_a_model_is_told_nothing() {
    assert!(Opencode::new()
        .advise(&planned("dev", "openrouter/anthropic/claude-sonnet-4"))
        .is_empty());
}

// With no agent there is no `agents.<agent>` key to name.
#[test]
fn test_a_run_with_no_agent_is_told_nothing() {
    assert!(Opencode::new().advise(&planned("", "")).is_empty());
}

#[test]
fn test_every_notice_has_a_message_using_exactly_its_args_and_a_fix() {
    let (name, catalog) = EN_US;
    assert_eq!(name, "engine-opencode");
    for sample in diagnostic_samples() {
        let code = sample.code();
        assert!(code.starts_with("engine-opencode-"), "{code}");
        for id in [code.to_string(), format!("{code}-fix")] {
            let line = catalog
                .lines()
                .find(|l| l.starts_with(&format!("{id} =")))
                .unwrap_or_else(|| panic!("{id} has no message"));
            let used: Vec<&str> = line
                .split("{ $")
                .skip(1)
                .filter_map(|rest| rest.split(' ').next())
                .collect();
            let args: Vec<&str> = sample.args().into_iter().map(|(n, _)| n).collect();
            assert!(used.iter().all(|u| args.contains(u)), "{id}: {used:?}");
        }
    }
    let notice = OpencodeNotice::NoModel {
        agent: "dev".to_string(),
    };
    assert!(catalog.contains("agents.{ $agent }.engines.opencode.model"));
    assert!(catalog
        .contains("ctxloom agent edit { $agent } --engine opencode --model <provider/model>"));
    assert_eq!(notice.code(), "engine-opencode-no-model");
}

// A notice displays as its code and args, as every diagnostic does (never
// English: the catalog renders the words).
#[test]
fn test_a_notice_displays_as_its_code_and_args() {
    let notice = OpencodeNotice::NoModel {
        agent: "dev".to_string(),
    };
    assert_eq!(notice.to_string(), "engine-opencode-no-model agent=dev");
}

// The catalog test covers every notice only if the samples hold one of each.
#[test]
fn test_the_samples_hold_every_notice() {
    let codes: Vec<String> = diagnostic_samples()
        .iter()
        .map(|sample| sample.code().to_string())
        .collect();
    assert_eq!(codes, ["engine-opencode-no-model"]);
}
