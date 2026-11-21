//! What trusting a project would allow, read from its own layer and its own
//! bundles (step 8 U1).

use crate::fakes::{Setup, CONFIG, ENVELOPE};
use crate::{HeldBack, CTXLOOM_BUNDLE_PATH};

fn held(path: &str, value: &str) -> HeldBack {
    HeldBack {
        path: path.to_string(),
        value: value.to_string(),
    }
}

#[test]
fn a_project_layers_project_only_settings_are_what_trust_allows() {
    let library = Setup {
        layers: &[
            CONFIG,
            "schema_version: 9\nagents:\n  default:\n    session_home: host\nengines:\n  claude-code:\n    command: /opt/their-claude\n    auth:\n      file: ~/their-login\n",
        ],
        ..Setup::default()
    }
    .library();
    assert_eq!(
        library.trust_settings().unwrap(),
        [
            held("agents.default.session_home", "host"),
            held("engines.claude-code.command", "/opt/their-claude"),
            held("engines.claude-code.auth.file", "~/their-login"),
        ]
    );
}

#[test]
fn the_home_layer_says_nothing_trust_allows() {
    let library = Setup {
        layers: &[
            "schema_version: 9\nengines:\n  claude-code:\n    command: /opt/mine\n",
            CONFIG,
        ],
        ..Setup::default()
    }
    .library();
    assert_eq!(library.trust_settings().unwrap(), []);
}

#[test]
fn a_project_ctxloom_bundle_is_allowed_by_trust_too() {
    let library = Setup {
        layers: &[CONFIG, CONFIG],
        bundles: &[("ctxloom", &[ENVELOPE]), ("lore", &[ENVELOPE])],
        ..Setup::default()
    }
    .library();
    let settings = library.trust_settings().unwrap();
    assert_eq!(settings.len(), 1, "{settings:?}");
    assert_eq!(settings[0].path, CTXLOOM_BUNDLE_PATH);
    assert!(settings[0].value.contains("ctxloom"), "{settings:?}");
}

#[test]
fn a_project_with_no_bundles_directory_has_no_ctxloom_bundle() {
    let library = Setup {
        layers: &[CONFIG, CONFIG],
        bundles: &[("ctxloom", &[ENVELOPE])],
        no_root: true,
        ..Setup::default()
    }
    .library();
    assert_eq!(library.trust_settings().unwrap(), []);
}

#[test]
fn outside_a_project_trust_allows_nothing() {
    let library = Setup {
        layers: &["schema_version: 9\nengines:\n  claude-code:\n    command: /opt/x\n"],
        home_only: true,
        ..Setup::default()
    }
    .library();
    assert_eq!(library.trust_settings().unwrap(), []);
}

#[test]
fn a_project_layer_that_does_not_parse_is_refused() {
    let library = Setup {
        layers: &[CONFIG, "schema_version: 9\nengines: [unclosed\n"],
        ..Setup::default()
    }
    .library();
    assert!(library.trust_settings().is_err());
}

/// A project layer saying every project-only key, for its default agent's
/// engine and for another agent's.
const SAYS_ALL: &str = "schema_version: 9\ndefault_agent: default\nagents:\n  default:\n    engine: claude-code\n    session_home: host\n  other:\n    engine: opencode\n    session_home: host\nengines:\n  claude-code:\n    command: /opt/their-claude\n    auth:\n      file: ~/their-login\n  opencode:\n    command: /opt/their-opencode\n";

fn says_all() -> crate::Library {
    Setup {
        layers: &[CONFIG, SAYS_ALL],
        ..Setup::default()
    }
    .library()
}

fn planned(library: &crate::Library, flags: &crate::Flags) -> crate::DryRun {
    library
        .dry_run(flags, &crate::RunRequest::default())
        .result
        .unwrap()
        .plan
}

// Step 8 U2, owner Q8: an untrusted project's held-back settings are named
// by the plan only where THIS launch reads them, its engine's block and its
// agent's; the plan runs without them (so the credential is read without a
// held-back auth.file, Q9).
#[test]
fn an_untrusted_plan_names_the_held_back_settings_its_launch_reads() {
    let plan = planned(&says_all(), &crate::fakes::plain());
    assert_eq!(
        plan.untrusted,
        [
            "agents.default.session_home",
            "engines.claude-code.auth.file",
            "engines.claude-code.command",
        ]
    );
    assert_ne!(plan.drive.command, "/opt/their-claude");
    assert_eq!(plan.drive.session_home, "session");
    assert!(
        !format!("{:?}", plan.drive.auth).contains("their-login"),
        "{:?}",
        plan.drive.auth
    );
}

// Owner Q8: a launch on the other engine reads that engine's block alone.
#[test]
fn a_launch_on_another_engine_reads_that_engines_block() {
    let request = crate::RunRequest {
        engine: "opencode".to_string(),
        ..crate::RunRequest::default()
    };
    let plan = says_all()
        .dry_run(&crate::fakes::plain(), &request)
        .result
        .unwrap()
        .plan;
    assert_eq!(
        plan.untrusted,
        ["agents.default.session_home", "engines.opencode.command"]
    );
}

// S23: a trusted project's own settings apply, and nothing is held back or
// said.
#[test]
fn a_trusted_project_keeps_its_own_settings() {
    let library = says_all();
    let flags = crate::Flags {
        trusted: true,
        ..crate::fakes::plain()
    };
    let previewed = library.dry_run(&flags, &crate::RunRequest::default());
    assert!(
        previewed
            .notices
            .iter()
            .all(|n| !matches!(n, crate::Notice::UntrustedSetting { .. })),
        "{:?}",
        previewed.notices
    );
    let plan = previewed.result.unwrap().plan;
    assert!(plan.untrusted.is_empty(), "{:?}", plan.untrusted);
    assert_eq!(plan.drive.command, "/opt/their-claude");
    assert_eq!(plan.drive.session_home, "host");
}

// The home layer is the user's own: nothing of it is ever held back.
#[test]
fn the_home_layer_holds_nothing_back() {
    let library = Setup {
        layers: &[SAYS_ALL, CONFIG],
        ..Setup::default()
    }
    .library();
    let plan = planned(&library, &crate::fakes::plain());
    assert!(plan.untrusted.is_empty(), "{:?}", plan.untrusted);
    assert_eq!(plan.drive.command, "/opt/their-claude");
}

/// What `notices` held back, each as its typed notice says it: the
/// setting's path, the file that said it, and the home config to set it in.
fn untrusted(notices: &[crate::Notice]) -> Vec<(String, String, String)> {
    notices
        .iter()
        .filter_map(|notice| match notice {
            crate::Notice::UntrustedSetting {
                path,
                file,
                home_file,
            } => Some((path.clone(), file.clone(), home_file.clone())),
            _ => None,
        })
        .collect()
}

// Step 10a U7 (D7): an untrusted project's held-back setting is said by its
// typed notice, one per setting, wherever the configuration is read; ruling
// 46's untyped "is ignored" warning is gone.
#[test]
fn an_untrusted_read_says_each_held_back_setting_as_its_typed_notice() {
    let read = says_all().engine_show(&crate::fakes::plain(), "claude-code");
    let said = untrusted(&read.notices);
    let at = |path: &str| {
        (
            path.to_string(),
            "/p/.ctxloom/layer-1.yaml".to_string(),
            "/h/.ctxloom/config.yaml".to_string(),
        )
    };
    assert_eq!(
        said,
        [
            at("agents.default.session_home"),
            at("agents.other.session_home"),
            at("engines.claude-code.command"),
            at("engines.claude-code.auth.file"),
            at("engines.opencode.command"),
        ]
    );
    let notice = read
        .notices
        .iter()
        .find(|n| matches!(n, crate::Notice::UntrustedSetting { .. }))
        .unwrap();
    assert_eq!(
        angzarr_diagnostic::Diagnostic::code(notice),
        "library-application-notice-untrusted-setting"
    );
}

// Trusted, nothing is held back, so nothing is said.
#[test]
fn a_trusted_read_says_no_setting_held_back() {
    let flags = crate::Flags {
        trusted: true,
        ..crate::fakes::plain()
    };
    let read = says_all().engine_show(&flags, "claude-code");
    assert!(untrusted(&read.notices).is_empty(), "{:?}", read.notices);
}
