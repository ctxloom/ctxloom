use std::collections::BTreeMap;

use run_ports::{Engine, PrepareRequest, TurnRequest};

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
