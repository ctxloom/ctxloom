//! Which credentials are exported, and what is said of each. Ported from Go's
//! `auth status` (internal/adapters/cli/auth.go, release/0.7) and
//! auth.feature; the doctor's rules are Go's `ConfiguredCredentialChecks`.

use std::sync::{Arc, Mutex};

use ctxloom_ports::ExportedVars;
use serde_json::json;

use super::*;

/// An environment that exports exactly the names it is given, and records
/// every name asked: the only thing ever asked of it.
struct Env {
    exported: Vec<&'static str>,
    asked: Mutex<Vec<String>>,
}

impl ExportedVars for Env {
    fn is_exported(&self, name: &str) -> bool {
        self.asked.lock().unwrap().push(name.to_string());
        self.exported.contains(&name)
    }
}

/// The engine block that shares the human's own login in place.
fn login() -> String {
    "engines:\n  claude-code:\n    auth: login\n".to_string()
}

fn layer(text: &str) -> Vec<String> {
    vec![text.to_string()]
}

fn auth(exported: &[&'static str]) -> Auth {
    Auth::new(Arc::new(Env {
        exported: exported.to_vec(),
        asked: Mutex::new(Vec::new()),
    }))
}

// auth.feature: "An exported token is reported present without revealing it".
#[test]
fn an_exported_claude_token_is_reported_present_by_the_variable_it_is_read_from() {
    let rows = auth(&["CLAUDE_CODE_OAUTH_TOKEN"]).status(&[]);
    assert_eq!(rows.len(), 1);
    assert_eq!(rows[0].engine, "claude-code");
    assert_eq!(rows[0].mode, "token");
    assert_eq!(rows[0].present, Some(true));
    assert_eq!(rows[0].vars, ["CLAUDE_CODE_OAUTH_TOKEN"]);
    assert_eq!(rows[0].remedy, "");
}

// auth.feature: "With nothing exported, status names how to mint and export
// the token".
#[test]
fn a_missing_claude_token_is_reported_missing_with_how_to_mint_and_export_it() {
    let rows = auth(&[]).status(&[]);
    assert_eq!(rows.len(), 1);
    assert_eq!(rows[0].present, Some(false));
    assert!(rows[0].vars.is_empty());
    assert_eq!(
        rows[0].remedy,
        "run `claude setup-token` and export CLAUDE_CODE_OAUTH_TOKEN (or store it in your secret manager)"
    );
}

// Go: opencode's endpoint key is optional, so it has a row only when exported.
#[test]
fn opencodes_optional_key_has_a_row_only_when_it_is_exported() {
    let rows = auth(&["CLAUDE_CODE_OAUTH_TOKEN", "CTXLOOM_OPENCODE_API_KEY"]).status(&[]);
    let names: Vec<&str> = rows.iter().map(|r| r.engine.as_str()).collect();
    assert_eq!(names, ["claude-code", "opencode"]);
    assert_eq!(rows[1].present, Some(true));
    assert_eq!(rows[1].vars, ["CTXLOOM_OPENCODE_API_KEY"]);
    let rows = auth(&[]).status(&[]);
    assert!(rows.iter().all(|r| r.engine != "opencode"));
}

// Go's emitted shape: engine, mode, present always; vars and remedy only
// when there are some.
#[test]
fn a_row_serializes_to_gos_shape() {
    let rows = auth(&["CLAUDE_CODE_OAUTH_TOKEN"]).status(&[]);
    assert_eq!(
        serde_json::to_value(&rows).unwrap(),
        json!([{
            "engine": "claude-code",
            "mode": "token",
            "present": true,
            "vars": ["CLAUDE_CODE_OAUTH_TOKEN"],
            "via": "CLAUDE_CODE_OAUTH_TOKEN",
        }])
    );
    let rows = auth(&[]).status(&[]);
    let doc = serde_json::to_value(&rows).unwrap();
    assert_eq!(doc[0]["present"], false);
    assert!(doc[0].get("vars").is_none());
    assert!(doc[0]["remedy"]
        .as_str()
        .unwrap()
        .contains("claude setup-token"));
}

// The secret appears in no output: the port can only say whether a name is
// exported, so there is no value to leak, and only credential names are asked.
#[test]
fn only_the_credential_variables_are_asked_about_and_only_whether_they_are_exported() {
    let env = Arc::new(Env {
        exported: vec!["CLAUDE_CODE_OAUTH_TOKEN"],
        asked: Mutex::new(Vec::new()),
    });
    let _ = Auth::new(env.clone()).status(&[]);
    let mut asked = env.asked.lock().unwrap().clone();
    asked.sort();
    // opencode's built-in source is its provider keys and its endpoint key
    // (ruling 69), none exported, so each is asked.
    assert_eq!(
        asked,
        [
            "ANTHROPIC_API_KEY",
            "CLAUDE_CODE_OAUTH_TOKEN",
            "CTXLOOM_OPENCODE_API_KEY",
            "OPENAI_API_KEY"
        ]
    );
}

// doctor.feature: "A missing agent credential is a fail row".
#[test]
fn a_configured_claude_without_its_token_is_not_ready_and_says_the_fix() {
    let verdicts = auth(&[]).needed(&["claude-code".to_string()], &[]);
    assert_eq!(verdicts.len(), 1);
    let v = &verdicts[0];
    assert_eq!(
        (v.engine.as_str(), v.mode, v.vars.as_slice()),
        (
            "claude-code",
            "token",
            [
                "CLAUDE_CODE_OAUTH_TOKEN".to_string(),
                "ANTHROPIC_API_KEY".to_string()
            ]
            .as_slice()
        )
    );
    assert!(!v.ready);
    assert!(v.remedy.contains("claude setup-token"));
}

#[test]
fn a_configured_claude_with_its_token_is_ready() {
    let verdicts = auth(&["CLAUDE_CODE_OAUTH_TOKEN"]).needed(&["claude-code".to_string()], &[]);
    assert_eq!(verdicts.len(), 1);
    assert!(verdicts[0].ready);
}

// Go: opencode is keyless without its key, which is not a refusal.
#[test]
fn opencode_is_ready_without_its_optional_key() {
    let verdicts = auth(&[]).needed(&["opencode".to_string()], &[]);
    assert_eq!(verdicts.len(), 1);
    assert!(verdicts[0].ready);
    assert_eq!(verdicts[0].engine, "opencode");
}

// Go: a login shared in place reads nothing from the environment.
#[test]
fn login_mode_needs_nothing_from_the_environment() {
    assert!(auth(&[])
        .needed(&["claude-code".to_string()], &[login()])
        .is_empty());
    assert!(auth(&[])
        .status(&[login()])
        .iter()
        .all(|r| r.engine != "claude-code"));
}

#[test]
fn an_engine_with_no_credential_rule_is_not_asked_about() {
    assert!(auth(&[]).needed(&["mock".to_string()], &[]).is_empty());
}

#[test]
fn each_engine_is_reported_once_whatever_its_labels() {
    let engines = ["claude-code".to_string(), "claude-code".to_string()];
    assert_eq!(auth(&[]).needed(&engines, &[]).len(), 1);
}

#[test]
fn a_token_or_an_api_key_is_an_agent_token_and_nothing_else_is() {
    assert!(auth(&["CLAUDE_CODE_OAUTH_TOKEN"]).agent_token_exported(&[]));
    assert!(auth(&["ANTHROPIC_API_KEY"]).agent_token_exported(&[]));
    assert!(!auth(&["ANTHROPIC_AUTH_TOKEN", "CTXLOOM_OPENCODE_API_KEY"]).agent_token_exported(&[]));
    assert!(!auth(&[]).agent_token_exported(&[]));
}

// init's guide asks the configured source, not the two built-in variables.
#[test]
fn a_configured_source_is_what_the_agent_token_is() {
    let acme = layer("engines:\n  claude-code:\n    auth:\n      env: [ACME_TOKEN]\n");
    assert!(auth(&["ACME_TOKEN"]).agent_token_exported(&acme));
    assert!(!auth(&["CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"]).agent_token_exported(&acme));
}

#[test]
fn an_engine_that_reads_no_variable_has_nothing_to_guide() {
    for text in [
        "engines:\n  claude-code:\n    auth: none\n",
        "engines:\n  claude-code:\n    auth: login\n",
        "agents:\n  a:\n    engine: mock\n",
        // opencode's endpoint key is optional
        "agents:\n  a:\n    engine: opencode\n",
    ] {
        assert!(auth(&[]).agent_token_exported(&layer(text)), "{text}");
    }
}

#[test]
fn every_engine_in_use_must_be_satisfied() {
    let two = layer("agents:\n  a:\n    engine: claude-code\n  b:\n    engine: claude-code\nengines:\n  claude-code:\n    auth:\n      env: [ACME_TOKEN]\n");
    assert!(auth(&["ACME_TOKEN"]).agent_token_exported(&two));
    assert!(!auth(&[]).agent_token_exported(&two));
}

// engines.<engine>.auth is the source: any ONE of its variables authenticates
// (the API key alone is enough, which status once denied), and what is read
// is a name, never a value.
#[test]
fn an_api_key_alone_authenticates_claude_and_the_row_names_the_variable() {
    let rows = auth(&["ANTHROPIC_API_KEY"]).status(&[]);
    assert_eq!(rows[0].engine, "claude-code");
    assert_eq!(rows[0].present, Some(true));
    assert_eq!(rows[0].via, "ANTHROPIC_API_KEY");
    let verdicts = auth(&["ANTHROPIC_API_KEY"]).needed(&["claude-code".to_string()], &[]);
    assert!(verdicts[0].ready);
}

#[test]
fn a_configured_source_replaces_the_built_in_one_and_the_fix_names_it() {
    let layers =
        layer("engines:\n  claude-code:\n    auth:\n      env: [TEAM_TOKEN, OTHER_TOKEN]\n");
    // the built-in variable no longer counts
    let rows = auth(&["CLAUDE_CODE_OAUTH_TOKEN"]).status(&layers);
    assert_eq!(rows[0].present, Some(false));
    assert_eq!(rows[0].remedy, "export TEAM_TOKEN or OTHER_TOKEN");
    let rows = auth(&["OTHER_TOKEN"]).status(&layers);
    assert_eq!(rows[0].present, Some(true));
    assert_eq!(rows[0].via, "OTHER_TOKEN");
    let verdicts = auth(&[]).needed(&["claude-code".to_string()], &layers);
    assert!(!verdicts[0].ready);
    assert_eq!(verdicts[0].vars, ["TEAM_TOKEN", "OTHER_TOKEN"]);
}

#[test]
fn auth_none_and_a_file_alone_need_nothing_from_the_environment_of_a_child() {
    let none = layer("engines:\n  claude-code:\n    auth: none\n");
    assert!(auth(&[]).status(&none).is_empty());
    assert!(auth(&[])
        .needed(&["claude-code".to_string()], &none)
        .is_empty());
}

/// A source that is a file alone (owner ruling 53).
fn file_only() -> Vec<String> {
    layer("engines:\n  claude-code:\n    auth:\n      file: /h/.claude/.credentials.json\n")
}

// Ruling 53: a file alone has a row of its own, usable by the primary and not
// by a child, whatever is exported (the source names no variable), and the
// fix names the variables to put in the source and export.
#[test]
fn a_file_only_source_has_a_row_usable_by_the_primary_not_by_children() {
    for exported in [&[][..], &["CLAUDE_CODE_OAUTH_TOKEN"][..]] {
        let rows = auth(exported).status(&file_only());
        assert_eq!(rows.len(), 1, "{rows:?}");
        let row = &rows[0];
        assert_eq!(row.engine, "claude-code");
        assert_eq!(row.mode, "file");
        // Ruling 65: nothing looks at the file, so the row says nothing of
        // whether it is there.
        assert_eq!(row.present, None);
        assert_eq!(row.usable_by, "primary");
        assert_eq!(row.via, "/h/.claude/.credentials.json");
        assert!(row.vars.is_empty());
        assert_eq!(
            row.remedy,
            "a child agent needs an environment credential: name one of CLAUDE_CODE_OAUTH_TOKEN, ANTHROPIC_API_KEY in engines.claude-code.auth.env and export it"
        );
    }
    let doc = serde_json::to_value(auth(&[]).status(&file_only())).unwrap();
    assert_eq!(doc[0]["usable_by"], "primary");
    assert!(doc[0].get("present").is_none(), "{doc}");
    // a token row says nothing of who may use it
    let doc = serde_json::to_value(auth(&["CLAUDE_CODE_OAUTH_TOKEN"]).status(&[])).unwrap();
    assert!(doc[0].get("usable_by").is_none());
}

// The doctor's verdict of a file alone: the primary can run, a child cannot.
#[test]
fn a_file_only_source_is_a_verdict_for_the_primary_only() {
    let verdicts = auth(&[]).needed(&["claude-code".to_string()], &file_only());
    assert_eq!(verdicts.len(), 1, "{verdicts:?}");
    let v = &verdicts[0];
    assert_eq!(v.mode, "file");
    assert!(v.primary_only);
    assert!(v.ready, "the primary may run on it");
    assert!(
        v.remedy.contains("engines.claude-code.auth.env"),
        "{}",
        v.remedy
    );
    // a variable source is no primary-only one
    let verdicts = auth(&[]).needed(&["claude-code".to_string()], &[]);
    assert!(!verdicts[0].primary_only);
}

#[test]
fn token_spelled_out_is_the_built_in_variables() {
    let layers = layer("engines:\n  claude-code:\n    auth: token\n");
    let rows = auth(&["ANTHROPIC_API_KEY"]).status(&layers);
    assert_eq!(rows[0].via, "ANTHROPIC_API_KEY");
}

// a source the schema refuses is the loader's to report: nothing here panics
// or echoes it
#[test]
fn a_refused_source_is_skipped_and_never_repeated() {
    let layers =
        layer("engines:\n  claude-code:\n    auth:\n      env: [sk-ant-oat01-NOT-A-REAL-KEY]\n");
    let rows = auth(&[]).status(&layers);
    assert!(rows.iter().all(|r| r.engine != "claude-code"));
    assert!(!format!("{rows:?}").contains("sk-ant"));
}

// An engine with no built-in variables (a test engine) is told to name one.
#[test]
fn a_file_only_source_of_an_engine_with_no_builtin_variable_is_told_to_name_one() {
    let file = layer("engines:\n  mock:\n    auth:\n      file: /h/creds\n");
    let verdicts = auth(&[]).needed(&["mock".to_string()], &file);
    assert_eq!(
        verdicts[0].remedy,
        "a child agent needs an environment credential: name a variable in engines.mock.auth.env and export it"
    );
}

// Ruling 69: opencode's built-in source is the provider keys opencode reads
// and its endpoint key; any one exported is a row, present by its name.
#[test]
fn an_opencode_provider_key_is_its_credential_by_name() {
    for key in ["ANTHROPIC_API_KEY", "OPENAI_API_KEY"] {
        let rows = auth(&[key]).status(&[]);
        let row = rows
            .iter()
            .find(|r| r.engine == "opencode")
            .unwrap_or_else(|| panic!("{key}: {rows:?}"));
        assert_eq!(row.present, Some(true));
        assert_eq!(row.via, key);
    }
}

/// The agents' engine is `engine`, whose source is a file alone.
fn file_only_in_use(engine: &str) -> Vec<String> {
    layer(&format!(
        "agents:\n  a:\n    engine: {engine}\nengines:\n  {engine}:\n    auth:\n      file: /h/login.json\n"
    ))
}

// Ruling 64: init warns of an engine in use whose source is a file alone, in
// the words a child's run of it is refused with, naming the variables of its
// built-in source; whatever is exported (the source names none of it).
#[test]
fn an_engine_in_use_on_a_file_alone_is_warned_of_as_its_children_are_refused() {
    for exported in [&[][..], &["CLAUDE_CODE_OAUTH_TOKEN"][..]] {
        let warned = auth(exported).file_only(&file_only_in_use("claude-code"));
        assert_eq!(warned.len(), 1, "{warned:?}");
        assert_eq!(warned[0].code(), "runs-file-only-credential");
        let args: Vec<(String, String)> = warned[0]
            .args()
            .into_iter()
            .map(|(name, arg)| (name.to_string(), format!("{arg:?}")))
            .collect();
        assert_eq!(args.len(), 2);
        assert_eq!(args[0].0, "engine");
        assert!(args[0].1.contains("claude-code"), "{args:?}");
        assert_eq!(args[1].0, "variables");
        assert!(
            args[1]
                .1
                .contains("CLAUDE_CODE_OAUTH_TOKEN, ANTHROPIC_API_KEY"),
            "{args:?}"
        );
    }
    let opencode = auth(&[]).file_only(&file_only_in_use("opencode"));
    assert_eq!(opencode.len(), 1, "{opencode:?}");
    assert!(
        format!("{:?}", opencode[0].args())
            .contains("ANTHROPIC_API_KEY, OPENAI_API_KEY, CTXLOOM_OPENCODE_API_KEY"),
        "{opencode:?}"
    );
}

// Nothing to warn of: an engine no agent uses, a source with a variable, the
// built-in source, `login` and `none`.
#[test]
fn only_an_engine_in_use_on_a_file_alone_is_warned_of() {
    let unused = layer("agents:\n  a:\n    engine: opencode\nengines:\n  claude-code:\n    auth:\n      file: /h/login.json\n");
    assert!(auth(&[]).file_only(&unused).is_empty());
    for source in [
        "auth:\n      env: [TEAM_TOKEN]\n      file: /h/login.json",
        "auth: login",
        "auth: none",
        "auth: token",
    ] {
        let text = layer(&format!(
            "agents:\n  a:\n    engine: claude-code\nengines:\n  claude-code:\n    {source}\n"
        ));
        assert!(auth(&[]).file_only(&text).is_empty(), "{source}");
    }
    assert!(auth(&[]).file_only(&[]).is_empty());
}
