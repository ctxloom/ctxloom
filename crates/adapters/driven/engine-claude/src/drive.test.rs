use std::collections::BTreeMap;

use run_ports::{Engine, PrepareRequest, TurnRequest};

use crate::drive::{COPY_IN_ALLOWLIST, ENV_ALLOWLIST, INTERACTIVE_ENV_ALLOWLIST};
use crate::{Claude, CONTEXT_PROMPT_FILE};

fn request() -> TurnRequest {
    TurnRequest {
        prompt: "what does this project expect of me?".to_string(),
        home: "/p/.ctxloom/sessions/h/home".to_string(),
        context_path: "/p/.ctxloom/sessions/h/home/ctxloom-context.md".to_string(),
        resume: None,
        env: BTreeMap::new(),
        cwd: Some("/p".to_string()),
    }
}

fn env(pairs: &[(&str, &str)]) -> BTreeMap<String, String> {
    pairs
        .iter()
        .map(|(k, v)| (k.to_string(), v.to_string()))
        .collect()
}

#[test]
fn a_turn_is_one_print_command_that_streams_json() {
    let command = Claude::new().drive_turn(&request()).unwrap();
    assert_eq!(command.program, "claude");
    assert_eq!(
        command.args,
        vec![
            "-p",
            "--output-format",
            "stream-json",
            "--verbose",
            "--append-system-prompt-file",
            "/p/.ctxloom/sessions/h/home/ctxloom-context.md",
        ]
    );
    assert_eq!(command.cwd.as_deref(), Some("/p"));
}

#[test]
fn the_prompt_goes_on_stdin_and_never_on_the_command_line() {
    let command = Claude::new().drive_turn(&request()).unwrap();
    assert_eq!(command.stdin, b"what does this project expect of me?");
    assert!(!command.args.iter().any(|a| a.contains("expect of me")));
}

#[test]
fn a_later_turn_resumes_the_native_session() {
    let mut later = request();
    later.resume = Some("native-1".to_string());
    let command = Claude::new().drive_turn(&later).unwrap();
    let at = command.args.iter().position(|a| a == "--resume").unwrap();
    assert_eq!(command.args[at + 1], "native-1");
    assert!(!Claude::new()
        .drive_turn(&request())
        .unwrap()
        .args
        .contains(&"--resume".to_string()));
}

#[test]
fn the_config_dir_is_the_session_home() {
    let mut asked = request();
    // Even a CLAUDE_CONFIG_DIR the caller has is not the user's to give.
    asked.env = env(&[("CLAUDE_CONFIG_DIR", "/home/alice/.claude")]);
    let command = Claude::new().drive_turn(&asked).unwrap();
    assert_eq!(
        command.env.get("CLAUDE_CONFIG_DIR").map(String::as_str),
        Some("/p/.ctxloom/sessions/h/home")
    );
}

#[test]
fn only_the_allowlisted_environment_is_passed_on() {
    let mut asked = request();
    asked.env = env(&[
        ("PATH", "/usr/bin"),
        ("CLAUDE_CODE_OAUTH_TOKEN", "tok"), // gitleaks:allow
        ("HTTPS_PROXY", "http://proxy"),
        ("HOME", "/home/alice"),
        ("AWS_SECRET_ACCESS_KEY", "nope"),
        ("SSH_AUTH_SOCK", "/s"),
        ("CLAUDE_PROJECT_DIR", "/elsewhere"),
    ]);
    let command = Claude::new().drive_turn(&asked).unwrap();
    let names: Vec<&str> = command.env.keys().map(String::as_str).collect();
    assert_eq!(
        names,
        vec![
            "CLAUDE_CODE_OAUTH_TOKEN",
            "CLAUDE_CONFIG_DIR",
            "HTTPS_PROXY",
            "PATH"
        ]
    );
}

#[test]
fn preparing_a_home_writes_the_context_file_it_names() {
    let prepared = Claude::new()
        .prepare(&PrepareRequest {
            home: "/h".to_string(),
            context: "the context".to_string(),
            offered: vec!["settings.json".to_string(), ".credentials.json".to_string()],
            interactive: false,
        })
        .unwrap();
    assert_eq!(prepared.context_path, format!("/h/{CONTEXT_PROMPT_FILE}"));
    assert_eq!(prepared.files.len(), 1);
    assert_eq!(prepared.files[0].rel, CONTEXT_PROMPT_FILE);
    assert_eq!(prepared.files[0].bytes, b"the context");
    // Nothing of the user's own configuration is on the allowlist yet.
    assert!(prepared.copy_in.is_empty());
}

#[test]
fn the_interactive_run_is_the_terminal_command_with_the_context_and_no_print_mode() {
    let mut asked = request();
    asked.prompt = String::new();
    asked.env = env(&[("PATH", "/usr/bin"), ("HOME", "/home/alice")]);
    let command = Claude::new().drive_interactive(&asked).unwrap();
    assert_eq!(command.program, "claude");
    assert_eq!(
        command.args,
        vec![
            "--append-system-prompt-file",
            "/p/.ctxloom/sessions/h/home/ctxloom-context.md"
        ]
    );
    assert!(command.stdin.is_empty());
    assert_eq!(command.cwd.as_deref(), Some("/p"));
    let names: Vec<&str> = command.env.keys().map(String::as_str).collect();
    assert_eq!(names, vec!["CLAUDE_CONFIG_DIR", "PATH"]);
    assert_eq!(
        command.env.get("CLAUDE_CONFIG_DIR").map(String::as_str),
        Some("/p/.ctxloom/sessions/h/home")
    );
}

#[test]
fn an_interactive_run_given_a_prompt_opens_on_it() {
    let command = Claude::new().drive_interactive(&request()).unwrap();
    assert_eq!(
        command.args.last().map(String::as_str),
        Some("what does this project expect of me?")
    );
}

// The launcher picks an engine by this name.
#[test]
fn the_run_engine_is_named_claude_code() {
    assert_eq!(Engine::name(&Claude::new()), "claude-code");
}

// Owner ruling 2026-11-08: the terminal session's hook processes run without
// HOME, so it is given ctxloom's home by name; a child's turn is not, and
// neither is given HOME.
#[test]
fn the_interactive_run_is_given_ctxloom_home_and_still_no_home() {
    let mut asked = request();
    asked.env = env(&[
        ("PATH", "/usr/bin"),
        ("HOME", "/home/alice"),
        ("CTXLOOM_HOME", "/home/alice"),
    ]);
    let command = Claude::new().drive_interactive(&asked).unwrap();
    let names: Vec<&str> = command.env.keys().map(String::as_str).collect();
    assert_eq!(names, vec!["CLAUDE_CONFIG_DIR", "CTXLOOM_HOME", "PATH"]);
    assert_eq!(
        command.env.get("CTXLOOM_HOME").map(String::as_str),
        Some("/home/alice")
    );
    let turn = Claude::new().drive_turn(&asked).unwrap();
    assert!(!turn.env.contains_key("CTXLOOM_HOME"), "{:?}", turn.env);
    assert!(!turn.env.contains_key("HOME"));
}

fn prepared(interactive: bool) -> run_ports::Prepared {
    Claude::new()
        .prepare(&PrepareRequest {
            home: "/h".to_string(),
            context: "the context".to_string(),
            offered: Vec::new(),
            interactive,
        })
        .unwrap()
}

// The terminal session's hooks are installed in its home's settings through
// the engine's own spelling: `ctxloom hook claude-code execute <Event>`.
#[test]
fn an_interactive_home_gets_settings_whose_hooks_call_ctxloom_execute() {
    let prepared = prepared(true);
    let settings = prepared
        .files
        .iter()
        .find(|f| f.rel == "settings.json")
        .expect("settings.json in the home");
    let doc = surface_ports::Json::parse(std::str::from_utf8(&settings.bytes).unwrap()).unwrap();
    assert!(crate::hooks::has_callback(&doc), "{doc:?}");
    let hooks = doc.get("hooks").unwrap();
    for event in [
        "SessionStart",
        "UserPromptSubmit",
        "MessageDisplay",
        "PostToolBatch",
        "Stop",
        "SessionEnd",
        "PreCompact",
        "PostCompact",
    ] {
        let group = hooks
            .get(event)
            .and_then(|g| g.as_array())
            .unwrap_or_else(|| panic!("{event}"));
        let hook = group[0].get("hooks").and_then(|h| h.as_array()).unwrap()[0].clone();
        assert_eq!(
            hook.get("command").and_then(|c| c.as_str()),
            Some("ctxloom")
        );
        let args: Vec<&str> = hook
            .get("args")
            .and_then(|a| a.as_array())
            .unwrap()
            .iter()
            .filter_map(|a| a.as_str())
            .collect();
        assert_eq!(args, ["hook", "claude-code", "execute", event]);
    }
}

#[test]
fn a_childs_home_has_no_settings() {
    let prepared = prepared(false);
    assert!(prepared.files.iter().all(|f| f.rel != "settings.json"));
    assert_eq!(prepared.files.len(), 1);
}

#[test]
fn a_child_is_given_only_an_env_credential_and_never_the_login_file() {
    let mut asked = request();
    asked.env = env(&[
        ("CLAUDE_CODE_OAUTH_TOKEN", "tok"),       // gitleaks:allow
        ("ANTHROPIC_AUTH_TOKEN", "primary-only"), // gitleaks:allow
    ]);
    let command = Claude::new().drive_turn(&asked).unwrap();
    assert!(command.env.contains_key("CLAUDE_CODE_OAUTH_TOKEN"));
    assert!(!command.env.contains_key("ANTHROPIC_AUTH_TOKEN"));
    // The login file is none of a child's home: nothing is offered to copy.
    assert!(COPY_IN_ALLOWLIST.is_empty());
    let credentials = Claude::new().credentials();
    assert_eq!(
        credentials.child_env,
        ["CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"]
    );
    for var in credentials.child_env {
        assert!(ENV_ALLOWLIST.contains(var), "{var} reaches a child");
    }
}

#[test]
fn the_primary_may_be_authenticated_by_any_of_three_variables_and_is_passed_each() {
    let credentials = Claude::new().credentials();
    assert_eq!(
        credentials.primary_env,
        [
            "CLAUDE_CODE_OAUTH_TOKEN",
            "ANTHROPIC_API_KEY",
            "ANTHROPIC_AUTH_TOKEN"
        ]
    );
    assert_eq!(credentials.user_dir, ".claude");
    assert_eq!(credentials.login_file, Some(".credentials.json"));
    for var in credentials.primary_env {
        assert!(INTERACTIVE_ENV_ALLOWLIST.contains(var), "{var} reaches it");
    }
}

// Owner rulings 2026-11-08 item 9 and 2026-11-09 item 16: the terminal session's
// hook processes find the listener through the engine's environment, so the
// interactive command is passed the endpoint, the bearer and the run; a child's
// turn is passed none of them, whatever the caller's environment holds.
#[test]
fn only_the_interactive_run_is_passed_the_listener_variables() {
    let mut asked = request();
    asked.env = env(&[
        ("PATH", "/usr/bin"),
        ("CTXLOOM_ENDPOINT", "/run/user/1/ctxloom/a.sock"),
        ("CTXLOOM_BEARER", "run.secret"),
        ("CTXLOOM_RUN", "run"),
        ("CTXLOOM_SESSION_HARP", "brisk-copper-moth"),
    ]);
    let interactive = Claude::new().drive_interactive(&asked).unwrap();
    for name in run_ports::HOOK_VARIABLES {
        assert!(interactive.env.contains_key(name), "{name}");
        assert!(INTERACTIVE_ENV_ALLOWLIST.contains(&name));
        assert!(
            !ENV_ALLOWLIST.contains(&name),
            "{name} must not reach a turn"
        );
    }
    assert_eq!(interactive.env["CTXLOOM_SESSION_HARP"], "brisk-copper-moth");
    let turn = Claude::new().drive_turn(&asked).unwrap();
    for name in run_ports::HOOK_VARIABLES {
        assert!(!turn.env.contains_key(name), "{name} in {:?}", turn.env);
    }
    assert!(!turn.env.contains_key("CTXLOOM_SESSION_HARP"));
}

// The command carrying the bearer renders without it.
#[test]
fn the_interactive_command_never_renders_the_bearer() {
    let mut asked = request();
    asked.env = env(&[("CTXLOOM_BEARER", "run.TOPSECRET")]);
    let command = Claude::new().drive_interactive(&asked).unwrap();
    assert!(!format!("{command:?} {asked:?}").contains("TOPSECRET"));
}
