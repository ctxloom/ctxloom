//! The pure checks. Go: `cli/doctor.feature` and `doctor_cmd_checks_test.go`
//! (release/0.7).

use std::sync::Arc;

use auth_application::Auth;
use ctxloom_ports::ExportedVars;

use crate::checks::{self, Status};
use crate::hooks::Stale;

fn files(path: &str, text: &str) -> Vec<(String, String)> {
    vec![(path.to_string(), text.to_string())]
}

// doctor.feature: "A blanket ignore rule is reported as a warning".
#[test]
fn a_blanket_ignore_rule_warns_and_names_the_install() {
    for rule in [".ctxloom/", ".ctxloom", "/.ctxloom/", ".ctxloom/*"] {
        let check = checks::gitignore(Some(&format!("target/\n{rule}\n")), &[]);
        assert_eq!(check.status, Status::Warn, "{rule}");
        assert!(check.detail.contains("blanket"));
        assert_eq!(check.remedy, "ctxloom manage gitignore install");
    }
}

// doctor.feature: "The current granular ignore patterns keep the check quiet".
#[test]
fn the_granular_patterns_and_no_gitignore_are_quiet() {
    let granular = checks::gitignore(Some(".ctxloom/cache/\n.ctxloom/sessions/\n"), &[]);
    assert_eq!(granular.status, Status::Ok);
    assert_eq!(checks::gitignore(None, &[]).status, Status::Ok);
}

// Hand-test 2026-11-10: private state git can see is a warning naming it
// and the install that ignores it; a blanket rule is the finding first.
#[test]
fn private_state_git_can_see_warns_naming_it() {
    let check = checks::gitignore(None, &["/cache", "/sessions"]);
    assert_eq!(check.status, Status::Warn);
    assert!(
        check.detail.contains("(/cache, /sessions)"),
        "{}",
        check.detail
    );
    assert_eq!(check.remedy, "ctxloom manage gitignore install");
    let blanket = checks::gitignore(Some(".ctxloom/\n"), &["/cache"]);
    assert!(blanket.detail.contains("blanket"), "{}", blanket.detail);
}

// doctor.feature: "Doctor names a settings file whose ctxloom entry launches a
// server that does not exist" (written by hand: no record, so by hand).
#[test]
fn a_launched_ctxloom_mcp_server_is_named_with_the_by_hand_remedy() {
    let bad =
        r#"{"mcpServers":{"ctxloom":{"command":"/usr/local/bin/ctxloom","args":["mcp","serve"]}}}"#;
    let check = checks::mcp_invocation(&files(".mcp.json", bad), "ctxloom");
    assert_eq!(check.status, Status::Warn);
    assert!(check.detail.contains(".mcp.json") && check.detail.contains("entry from"));
    assert!(check.remedy.contains("by hand"));
    assert!(!check
        .remedy
        .contains("ctxloom materialize --release --surface mcp"));
}

// doctor.feature: "Doctor stays quiet about a settings file that names the
// session's endpoint".
#[test]
fn an_endpoint_url_launches_nothing() {
    let good = r#"{"mcpServers":{"ctxloom":{"type":"http","url":"http://127.0.0.1:4242/mcp"}}}"#;
    assert_eq!(
        checks::mcp_invocation(&files(".mcp.json", good), "ctxloom").status,
        Status::Ok
    );
    let other = r#"{"mcpServers":{"ctxloom":{"command":"other","args":["mcp","serve"]}}}"#;
    assert_eq!(
        checks::mcp_invocation(&files(".mcp.json", other), "ctxloom").status,
        Status::Ok
    );
    assert_eq!(
        checks::mcp_invocation(&files(".mcp.json", "not json"), "ctxloom").status,
        Status::Ok
    );
}

// doctor.feature: "The delivered-versus-ingested boundary is stated, not
// claimed as checked": info, never ok.
#[test]
fn the_ingestion_limit_is_info_and_says_whose_process_it_is() {
    let check = checks::ingestion();
    assert_eq!(check.status, Status::Info);
    assert!(check.detail.contains("a process ctxloom does not own"));
}

// doctor.feature: "A config that will not parse does not stop the diagnosis".
#[test]
fn a_broken_config_is_a_warning_not_a_failure() {
    let check = checks::setup_marker(true, Some("schema_version: [unclosed\n  - : :\n"), None);
    assert_eq!(check.status, Status::Warn);
    assert_eq!(
        checks::setup_marker(true, Some("schema_version: 8\n"), None).status,
        Status::Ok
    );
    assert_eq!(checks::setup_marker(false, None, None).status, Status::Warn);
    assert_eq!(checks::setup_marker(true, None, None).status, Status::Warn);
}

// Hand-test 2026-11-10: a config every run refuses is a fail row, in the
// refusal's words and with its fix, project or not.
#[test]
fn a_config_every_run_refuses_is_a_fail_row_with_its_fix() {
    for has_project in [true, false] {
        let check = checks::setup_marker(
            has_project,
            Some("schema_version: 9\n"),
            Some(("failed to load config: no", "edit it")),
        );
        assert_eq!(check.status, Status::Fail);
        assert_eq!(check.detail, "failed to load config: no");
        assert_eq!(check.remedy, "edit it");
    }
}

// hand-test 2 2026-11-11: an unknown config key was filed under "setup
// marker"; the check reads the config too, and is named for both (its marker
// stays `SETUP-MARKER-e5`, which the doctor skill and doctor.feature name).
#[test]
fn the_setup_check_is_named_for_the_config_it_reads() {
    let refused = checks::setup_marker(true, None, Some(("failed to load config: no", "")));
    assert_eq!(refused.name, "setup and config");
    assert_eq!(
        checks::setup_marker(false, None, None).name,
        "setup and config"
    );
}

// doctor.feature: --deps "binaries on PATH and git identity".
#[test]
fn deps_want_git_and_an_identity() {
    assert_eq!(checks::deps(false, false).status, Status::Warn);
    assert_eq!(checks::deps(false, true).status, Status::Warn);
    assert!(checks::deps(false, true).detail.contains("not on the PATH"));
    assert_eq!(checks::deps(true, false).status, Status::Warn);
    assert!(checks::deps(true, false).detail.contains("has no identity"));
    assert_eq!(checks::deps(true, true).status, Status::Ok);
}

#[test]
fn an_agent_with_neither_an_engine_nor_profiles_resolves_to_nothing() {
    let bad = "schema_version: 8\nagents:\n  coder:\n    runtime: host\n";
    assert_eq!(checks::agents(Some(bad)).status, Status::Warn);
    let good = "schema_version: 8\nagents:\n  coder:\n    engine: claude-code\n";
    assert_eq!(checks::agents(Some(good)).status, Status::Ok);
    assert_eq!(
        checks::agents(Some("schema_version: 8\n")).status,
        Status::Ok
    );
}

#[test]
fn the_stale_hooks_row_names_the_event_the_subcommand_and_the_fix() {
    let stale = vec![(
        ".claude/settings.json".to_string(),
        Stale {
            event: "SessionStart".to_string(),
            sub: "inject-context".to_string(),
        },
    )];
    let check = checks::stale_hooks(&stale);
    assert_eq!(check.status, Status::Warn);
    assert!(check
        .detail
        .contains("SessionStart runs `ctxloom hook inject-context`"));
    assert_eq!(check.remedy, "ctxloom doctor --fix");
    assert_eq!(checks::stale_hooks(&[]).status, Status::Ok);
    assert!(!check.detail.contains("entries in all"), "{}", check.detail);
    for count in [2, 3] {
        let many: Vec<_> = (0..count).map(|_| stale[0].clone()).collect();
        assert!(checks::stale_hooks(&many)
            .detail
            .ends_with(&format!("; {count} entries in all")),);
    }
}

// Owner ruling 162: under `legacy.go: present` each Go-era entry is an info
// row of its own, naming its event, its subcommand and its file.
#[test]
fn each_go_era_entry_is_an_info_row_left_alone() {
    let entry = |event: &str, sub: &str| Stale {
        event: event.to_string(),
        sub: sub.to_string(),
    };
    let entries = vec![
        (
            "~/.claude/settings.json".to_string(),
            entry("PostToolUse", "stamp-plan"),
        ),
        (
            ".claude/settings.json".to_string(),
            entry("SessionStart", "session-bind"),
        ),
    ];
    let rows = checks::go_era_hooks(&entries);
    assert_eq!(rows.len(), 2);
    for row in &rows {
        assert_eq!(row.marker, "DOCTOR-CHECK-GO-ERA-HOOK-n6");
        assert_eq!(row.status, Status::Info);
        assert!(
            row.detail.starts_with("Go-era hook, left alone: "),
            "{}",
            row.detail
        );
        assert!(row.remedy.contains("legacy.go: retired"), "{}", row.remedy);
    }
    assert!(rows[0]
        .detail
        .contains("PostToolUse runs `ctxloom hook stamp-plan` (~/.claude/settings.json)"));
    assert!(rows[1]
        .detail
        .contains("SessionStart runs `ctxloom hook session-bind` (.claude/settings.json)"));
    assert!(checks::go_era_hooks(&[]).is_empty());
}

#[test]
fn the_version_row_says_the_version() {
    assert!(checks::version("1.2.3").detail.contains("1.2.3"));
}

#[test]
fn registered_hooks_are_ok_and_none_is_info() {
    let on = files(
        ".claude/settings.json",
        r#"{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"ctxloom hook claude-code execute SessionStart"}]}]}}"#,
    );
    assert_eq!(checks::hooks(&on, "ctxloom").status, Status::Ok);
    assert_eq!(checks::hooks(&[], "ctxloom").status, Status::Info);
}

/// The variables a launch exports.
struct Exports(&'static [&'static str]);

impl ExportedVars for Exports {
    fn is_exported(&self, name: &str) -> bool {
        self.0.contains(&name)
    }
}

const TOKEN: &[&str] = &["CLAUDE_CODE_OAUTH_TOKEN"];

fn credentials(layers: &[&str], exported: &'static [&'static str]) -> checks::Check {
    let layers: Vec<String> = layers.iter().map(ToString::to_string).collect();
    checks::credentials(&layers, &Auth::new(Arc::new(Exports(exported))))
}

// doctor.feature: "A missing agent credential is a fail row with run's fix".
#[test]
fn a_missing_claude_token_is_a_fail_row_with_runs_fix() {
    let check = credentials(&["schema_version: 8\n"], &[]);
    assert_eq!(check.marker, "DOCTOR-CHECK-CREDENTIALS-m5");
    assert_eq!(check.status, Status::Fail);
    assert!(check.detail.contains("claude-code"), "{}", check.detail);
    assert!(check
        .detail
        .contains("none of CLAUDE_CODE_OAUTH_TOKEN, ANTHROPIC_API_KEY is exported"));
    assert!(check.remedy.contains("claude setup-token"));
}

#[test]
fn an_exported_token_is_ok_and_the_row_says_it_does_not_test_acceptance() {
    let check = credentials(&["schema_version: 8\n"], TOKEN);
    assert_eq!(check.status, Status::Ok);
    assert!(
        check.detail.contains("claude-code (auth token)"),
        "{}",
        check.detail
    );
    assert!(check
        .detail
        .contains("does not test whether the engine accepts it"));
    assert_eq!(check.remedy, "");
}

// No project and no config at all is the shipped default: claude-code.
#[test]
fn with_no_config_at_all_the_shipped_default_engine_is_checked() {
    assert_eq!(credentials(&[], &[]).status, Status::Fail);
    assert_eq!(credentials(&[], TOKEN).status, Status::Ok);
}

// owner amendment (2026-11-09): `engines.<engine>.auth` is the one auth key; a
// login shared in place reads nothing from the environment.
#[test]
fn login_needs_no_credential_from_the_environment() {
    let check = credentials(
        &["schema_version: 9\nengines:\n  claude-code:\n    auth: login\n"],
        &[],
    );
    assert_eq!(check.status, Status::Info);
    assert!(check
        .detail
        .contains("no configured engine needs a credential from you"));
}

#[test]
fn the_auth_of_the_highest_layer_wins() {
    let home = "schema_version: 9\nengines:\n  claude-code:\n    auth: login\n";
    let project = "schema_version: 9\nengines:\n  claude-code:\n    auth: token\n";
    assert_eq!(credentials(&[home, project], &[]).status, Status::Fail);
    assert_eq!(credentials(&[project, home], &[]).status, Status::Info);
}

// the credential is any ONE variable of the source: an API key alone passes
#[test]
fn an_api_key_alone_passes_and_a_configured_source_replaces_the_built_in_one() {
    const KEY: &[&str] = &["ANTHROPIC_API_KEY"];
    assert_eq!(
        credentials(&["schema_version: 9\n"], KEY).status,
        Status::Ok
    );
    let team = "schema_version: 9\nengines:\n  claude-code:\n    auth:\n      env: [TEAM_TOKEN]\n";
    let check = credentials(&[team], KEY);
    assert_eq!(check.status, Status::Fail);
    assert!(
        check.detail.contains("TEAM_TOKEN is not exported"),
        "{}",
        check.detail
    );
    assert_eq!(check.remedy, "export TEAM_TOKEN");
    assert_eq!(credentials(&[team], &["TEAM_TOKEN"]).status, Status::Ok);
}

// Ruling 53: a source that is a file alone is a warn row: the primary session
// can run on it, a child agent cannot, and the fix names what a child needs.
#[test]
fn a_file_only_source_is_a_warn_row_usable_by_the_primary_not_by_children() {
    let file = "schema_version: 9\nengines:\n  claude-code:\n    auth:\n      file: ~/.claude/.credentials.json\n";
    let check = credentials(&[file], TOKEN);
    assert_eq!(check.status, Status::Warn);
    // Ruling 81: in the words a child's run of it is refused with.
    assert_eq!(
        check.detail,
        "claude-code (auth file): a child agent run of claude-code needs a credential in the environment (any one of: CLAUDE_CODE_OAUTH_TOKEN, ANTHROPIC_API_KEY), and engines.claude-code.auth names only a file, which serves the primary interactive session alone"
    );
    assert_eq!(
        check.remedy,
        "name one of those variables in engines.claude-code.auth.env and export it (for claude-code, `claude setup-token` makes CLAUDE_CODE_OAUTH_TOKEN); the file keeps serving the primary session"
    );
}

#[test]
fn several_variables_missing_are_said_as_none_of_them() {
    let check = credentials(&["schema_version: 9\n"], &[]);
    assert!(
        check
            .detail
            .contains("none of CLAUDE_CODE_OAUTH_TOKEN, ANTHROPIC_API_KEY is exported"),
        "{}",
        check.detail
    );
}

// the engines checked are the ones the agents are bound to
#[test]
fn opencode_is_keyless_without_its_optional_key_and_never_fails() {
    let config = "schema_version: 9\nagents:\n  o:\n    engine: opencode\n";
    let check = credentials(&[config], &[]);
    assert_eq!(check.status, Status::Ok);
    // hand-test 2026-11-10: no key is exported, so the row does not call it
    // present; it says what `auth status` says (rulings 69, 79).
    assert_eq!(
        check.detail,
        "opencode (auth token): not needed: none of ANTHROPIC_API_KEY, OPENAI_API_KEY is exported, so opencode runs keyless on its free models; export one for a provider's models"
    );
    assert_eq!(check.remedy, "");
    let both =
        "schema_version: 9\nagents:\n  c:\n    engine: claude-code\n  o:\n    engine: opencode\n";
    let check = credentials(&[both], TOKEN);
    assert_eq!(check.status, Status::Ok);
    assert!(
        check.detail.starts_with("every configured engine's credential is present: claude-code (auth token) (doctor does not test"),
        "{}",
        check.detail
    );
    assert!(
        check.detail.ends_with("; opencode (auth token): not needed: none of ANTHROPIC_API_KEY, OPENAI_API_KEY is exported, so opencode runs keyless on its free models; export one for a provider's models"),
        "{}",
        check.detail
    );
    let keyed = credentials(&[config], &["OPENAI_API_KEY"]);
    assert_eq!(keyed.status, Status::Ok);
    assert!(
        keyed
            .detail
            .starts_with("every configured engine's credential is present: opencode (auth token)"),
        "{}",
        keyed.detail
    );
}

#[test]
fn an_agent_naming_an_engine_is_checked_and_one_naming_none_is_the_default() {
    let named = "schema_version: 9\nagents:\n  dev:\n    engine: claude-code\n";
    assert_eq!(credentials(&[named], &[]).status, Status::Fail);
    let mocked = "schema_version: 9\nagents:\n  dev:\n    engine: mock\n";
    assert_eq!(credentials(&[mocked], &[]).status, Status::Info);
    let silent = "schema_version: 9\nagents:\n  dev:\n    profiles: [p]\n";
    assert_eq!(credentials(&[silent], &[]).status, Status::Fail);
}

#[test]
fn a_config_that_will_not_parse_is_the_setup_markers_to_report_and_checks_the_default() {
    let check = credentials(&["schema_version: [unclosed\n"], &[]);
    assert_eq!(check.status, Status::Fail);
}

// An agent is bound by a non-empty engine OR a non-empty profile list: each
// alone binds it, and an empty list binds nothing.
#[test]
fn an_agent_bound_by_profiles_alone_or_engine_alone_is_bound_and_an_empty_list_is_not() {
    let profiles = "schema_version: 8\nagents:\n  a:\n    profiles: [dev]\n";
    assert_eq!(checks::agents(Some(profiles)).status, Status::Ok);
    let engine = "schema_version: 8\nagents:\n  a:\n    engine: claude-code\n";
    assert_eq!(checks::agents(Some(engine)).status, Status::Ok);
    let empty = "schema_version: 8\nagents:\n  a:\n    profiles: []\n";
    assert_eq!(checks::agents(Some(empty)).status, Status::Warn);
}

#[test]
fn test_an_engine_that_cannot_carry_an_agent_s_hook_is_a_warning_naming_each() {
    use library_application::{AgentLosses, NotCarried};
    let ok = checks::not_carried(&[]);
    assert_eq!(ok.marker, "DOCTOR-CHECK-NOT-CARRIED-r8");
    assert_eq!(ok.status, Status::Ok);
    let lost = |event: &str| NotCarried {
        engine: "opencode".to_string(),
        surface: "hooks".to_string(),
        event: event.to_string(),
        reason: format!("no {event}"),
    };
    let warned = checks::not_carried(&[
        AgentLosses {
            agent: "dev".to_string(),
            engine: "opencode".to_string(),
            not_carried: vec![lost("session_end"), lost("pre_tool")],
        },
        AgentLosses {
            agent: "ops".to_string(),
            engine: "opencode".to_string(),
            not_carried: vec![lost("turn_end")],
        },
    ]);
    assert_eq!(warned.status, Status::Warn);
    assert_eq!(
        warned.detail,
        "agent dev on opencode cannot carry hooks: session_end (no session_end), hooks: pre_tool (no pre_tool); agent ops on opencode cannot carry hooks: turn_end (no turn_end)"
    );
    assert!(warned
        .remedy
        .contains("ctxloom agent edit <name> --engine <engine>"));
}

// j001300 "Worktrees ctxloom did not create are reported, never touched":
// each foreign worktree is named with its measured states and the exact
// commands that remove it (Go: doctorCheckForeignWorktrees), the forced
// forms where the safe ones would refuse.
#[test]
fn a_foreign_worktree_is_named_with_its_states_and_the_commands_that_remove_it() {
    use ctxloom_ports::{ForeignListing, ForeignWorktree};
    let listing = ForeignListing::Listed(vec![
        ForeignWorktree {
            path: "/w/proj--stale-feature".to_string(),
            branch: "stale-feature".to_string(),
            merged: Some(false),
            dirty: Some(true),
        },
        ForeignWorktree {
            path: "/w/proj--done".to_string(),
            branch: "done".to_string(),
            merged: Some(true),
            dirty: Some(false),
        },
    ]);
    let check = checks::foreign_worktrees(Some("/p"), &listing);
    assert_eq!(check.marker, "DOCTOR-CHECK-FOREIGN-WORKTREES-s9");
    assert_eq!(check.status, Status::Warn);
    let said = format!("{}\n{}", check.detail, check.remedy);
    assert!(
        said.contains("2 worktree(s) ctxloom did not create"),
        "{said}"
    );
    assert!(
        said.contains("/w/proj--stale-feature (branch stale-feature, unmerged, dirty)"),
        "{said}"
    );
    assert!(
        said.contains("/w/proj--done (branch done, merged, clean)"),
        "{said}"
    );
    assert!(
        said.contains("git -C /p worktree remove --force /w/proj--stale-feature"),
        "{said}"
    );
    assert!(said.contains("git -C /p branch -D stale-feature"), "{said}");
    assert!(
        said.contains("git -C /p worktree remove /w/proj--done"),
        "{said}"
    );
    assert!(said.contains("git -C /p branch -d done"), "{said}");
    assert!(!said.contains("--force /w/proj--done"), "{said}");
}

// A state git did not answer is said as unknown, never assumed, and the
// commands are the safe ones, which git itself refuses if they would lose
// work.
#[test]
fn a_foreign_worktree_state_not_measured_is_unknown_and_its_commands_safe() {
    use ctxloom_ports::{ForeignListing, ForeignWorktree};
    let listing = ForeignListing::Listed(vec![ForeignWorktree {
        path: "/w/x".to_string(),
        branch: "x".to_string(),
        merged: None,
        dirty: None,
    }]);
    let check = checks::foreign_worktrees(Some("/p"), &listing);
    let said = format!("{}\n{}", check.detail, check.remedy);
    assert!(
        said.contains("/w/x (branch x, merge state unknown, dirty state unknown)"),
        "{said}"
    );
    assert!(said.contains("git -C /p worktree remove /w/x"), "{said}");
    assert!(said.contains("git -C /p branch -d x"), "{said}");
    assert!(!said.contains("--force") && !said.contains("-D"), "{said}");
}

// A detached worktree has no branch to delete.
#[test]
fn a_detached_foreign_worktree_names_no_branch_command() {
    use ctxloom_ports::{ForeignListing, ForeignWorktree};
    let listing = ForeignListing::Listed(vec![ForeignWorktree {
        path: "/w/x".to_string(),
        branch: String::new(),
        merged: None,
        dirty: Some(false),
    }]);
    let check = checks::foreign_worktrees(Some("/p"), &listing);
    let said = format!("{}\n{}", check.detail, check.remedy);
    assert!(said.contains("/w/x (detached, clean)"), "{said}");
    assert!(!said.contains("branch -"), "{said}");
}

#[test]
fn no_foreign_worktree_is_ok_and_no_repository_or_project_is_info() {
    use ctxloom_ports::ForeignListing;
    let ok = checks::foreign_worktrees(Some("/p"), &ForeignListing::Listed(Vec::new()));
    assert_eq!(ok.status, Status::Ok);
    assert!(ok.remedy.is_empty());
    let not_git = checks::foreign_worktrees(Some("/p"), &ForeignListing::NotARepository);
    assert_eq!(not_git.status, Status::Info);
    assert!(
        not_git.detail.contains("not a git repository"),
        "{}",
        not_git.detail
    );
    let none = checks::foreign_worktrees(None, &ForeignListing::NotARepository);
    assert_eq!(none.status, Status::Info);
    assert!(none.detail.contains("no project"), "{}", none.detail);
    let failed =
        checks::foreign_worktrees(Some("/p"), &ForeignListing::Unlisted("boom".to_string()));
    assert_eq!(failed.status, Status::Warn);
    assert!(failed.detail.contains("could not list") && failed.detail.contains("boom"));
}

// S51: a stale distilled form is a warning naming each; none is ok.
#[test]
fn test_a_stale_distilled_form_is_a_warning_naming_each() {
    let ok = checks::stale_distilled(&[]);
    assert_eq!(ok.marker, "DOCTOR-CHECK-STALE-DISTILLED-t1");
    assert_eq!(ok.status, Status::Ok);
    let warned = checks::stale_distilled(&[
        "demo#fragments/rules".to_string(),
        "demo#commands/review".to_string(),
    ]);
    assert_eq!(warned.status, Status::Warn);
    assert_eq!(
        warned.detail,
        "stale distilled forms (their originals are used instead): demo#fragments/rules, demo#commands/review"
    );
    assert!(warned.remedy.contains("ctxloom fragment distill <ref>"));
}
