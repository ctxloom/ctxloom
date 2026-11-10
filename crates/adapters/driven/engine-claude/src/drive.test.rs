use std::collections::BTreeMap;

use run_ports::{
    AuthSetting, Engine, EngineError, EngineSettings, PrepareRequest, SessionHome, TurnRequest,
};

use crate::drive::{COPY_IN_ALLOWLIST, EFFORT_LEVELS, ENV_ALLOWLIST, INTERACTIVE_ENV_ALLOWLIST};
use crate::{Claude, CONTEXT_PROMPT_FILE};

fn request() -> TurnRequest {
    TurnRequest {
        prompt: "what does this project expect of me?".to_string(),
        home: "/p/.ctxloom/sessions/h/home".to_string(),
        context_path: "/p/.ctxloom/sessions/h/home/ctxloom-context.md".to_string(),
        resume: None,
        env: BTreeMap::new(),
        cwd: Some("/p".to_string()),
        model: String::new(),
        effort: String::new(),
        settings: EngineSettings::default(),
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
            "--settings",
            "/p/.ctxloom/sessions/h/home/settings.json",
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
    // The context, and the settings that register the turn's hooks.
    assert_eq!(prepared.files.len(), 2);
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
            "/p/.ctxloom/sessions/h/home/ctxloom-context.md",
            "--settings",
            "/p/.ctxloom/sessions/h/home/settings.json"
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
    // Ruling 51: a child's hooks run without HOME too, so its turn is given
    // ctxloom's home by name as well; neither is given HOME.
    let turn = Claude::new().drive_turn(&asked).unwrap();
    assert_eq!(
        turn.env.get("CTXLOOM_HOME").map(String::as_str),
        Some("/home/alice")
    );
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

/// The native events a prepared home's `settings.json` registers, each checked
/// to call `ctxloom hook claude-code execute <Event>`.
fn registered_events(prepared: &run_ports::Prepared) -> Vec<String> {
    let settings = prepared
        .files
        .iter()
        .find(|f| f.rel == "settings.json")
        .expect("settings.json in the home");
    let doc = surface_ports::Json::parse(std::str::from_utf8(&settings.bytes).unwrap()).unwrap();
    let Some(surface_ports::Json::Object(events)) = doc.get("hooks").cloned() else {
        panic!("no hooks in {doc:?}");
    };
    let mut names = Vec::new();
    for (event, groups) in events {
        let groups = groups.as_array().unwrap().to_vec();
        assert_eq!(groups.len(), 1, "{event}");
        let hooks = groups[0]
            .get("hooks")
            .and_then(|h| h.as_array())
            .unwrap()
            .to_vec();
        assert_eq!(hooks.len(), 1, "{event}");
        assert_eq!(
            hooks[0].get("command").and_then(|c| c.as_str()),
            Some("ctxloom")
        );
        let args: Vec<String> = hooks[0]
            .get("args")
            .and_then(|a| a.as_array())
            .unwrap()
            .iter()
            .filter_map(|a| a.as_str().map(String::from))
            .collect();
        assert_eq!(args, ["hook", "claude-code", "execute", event.as_str()]);
        names.push(event);
    }
    names.sort();
    names
}

// Owner ruling 51 (after rulings 24, 26, 29): a one-shot child's turn is heard
// through hooks too, so its home gets settings; but SessionStart and
// SessionEnd fire once per `claude -p` PROCESS (every turn, child-probe), so a
// child registers only the per-turn events: the prompt, the batch, each
// message, the turn's end and a compaction.
#[test]
fn a_childs_home_registers_the_per_turn_events_and_not_the_per_process_ones() {
    let prepared = prepared(false);
    assert_eq!(prepared.files.len(), 2, "the context and the settings");
    assert_eq!(
        registered_events(&prepared),
        [
            "MessageDisplay",
            "PostCompact",
            "PostToolBatch",
            "PreCompact",
            "Stop",
            "UserPromptSubmit"
        ]
    );
}

// The terminal session's set is unchanged: it also hears the session begin
// and end.
#[test]
fn an_interactive_home_registers_the_session_events_too() {
    assert_eq!(
        registered_events(&prepared(true)),
        [
            "MessageDisplay",
            "PostCompact",
            "PostToolBatch",
            "PreCompact",
            "SessionEnd",
            "SessionStart",
            "Stop",
            "UserPromptSubmit"
        ]
    );
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

// Owner rulings 2026-11-08 item 9, 2026-11-09 item 16 and 51 (with 27): a
// session's hook processes find the listener through the engine's
// environment, so the terminal command AND a one-shot child's turn are passed
// the endpoint, the bearer, the run, the listener's certificate (public) and
// the session's harp. (Which values they are is the run's: an inherited one is
// replaced before the engine is asked.)
#[test]
fn the_terminal_and_a_childs_turn_are_both_passed_the_listener_variables() {
    let mut asked = request();
    asked.env = env(&[
        ("PATH", "/usr/bin"),
        ("CTXLOOM_ENDPOINT", "https://127.0.0.1:9"),
        ("CTXLOOM_BEARER", "run.secret"),
        ("CTXLOOM_RUN", "run"),
        ("CTXLOOM_TLS_CERT", "MAMBAgM="),
        ("CTXLOOM_SESSION_HARP", "brisk-copper-moth"),
    ]);
    for command in [
        Claude::new().drive_interactive(&asked).unwrap(),
        Claude::new().drive_turn(&asked).unwrap(),
    ] {
        for name in run_ports::HOOK_VARIABLES {
            assert_eq!(command.env.get(name), asked.env.get(name), "{name}");
        }
        assert_eq!(command.env["CTXLOOM_SESSION_HARP"], "brisk-copper-moth");
    }
    for name in run_ports::HOOK_VARIABLES
        .into_iter()
        .chain([run_ports::HOME_VARIABLE, run_ports::HARP_VARIABLE])
    {
        assert!(INTERACTIVE_ENV_ALLOWLIST.contains(&name), "{name}");
        assert!(ENV_ALLOWLIST.contains(&name), "{name} reaches a turn");
    }
}

// The turn carrying the bearer renders without it, as the terminal's does.
#[test]
fn a_childs_turn_never_renders_the_bearer() {
    let mut asked = request();
    asked.env = env(&[("CTXLOOM_BEARER", "run.TOPSECRET")]);
    let command = Claude::new().drive_turn(&asked).unwrap();
    assert!(command.env.contains_key("CTXLOOM_BEARER"));
    assert!(!format!("{command:?} {asked:?}").contains("TOPSECRET"));
}

// The command carrying the bearer renders without it.
#[test]
fn the_interactive_command_never_renders_the_bearer() {
    let mut asked = request();
    asked.env = env(&[("CTXLOOM_BEARER", "run.TOPSECRET")]);
    let command = Claude::new().drive_interactive(&asked).unwrap();
    assert!(!format!("{command:?} {asked:?}").contains("TOPSECRET"));
}

// A live probe (2026-11-09): `llm.configs.claude-code.model` never reached
// claude. A configured model is `--model <m>`, for a turn and for the terminal
// session, and the prompt stays the interactive command's last word; none
// configured is no flag at all.
#[test]
fn a_configured_model_is_passed_to_claude_for_a_turn_and_for_the_terminal() {
    let mut asked = request();
    asked.model = "haiku".to_string();
    let turn = Claude::new().drive_turn(&asked).unwrap();
    let at = turn.args.iter().position(|a| a == "--model").unwrap();
    assert_eq!(turn.args[at + 1], "haiku");
    let terminal = Claude::new().drive_interactive(&asked).unwrap();
    let at = terminal.args.iter().position(|a| a == "--model").unwrap();
    assert_eq!(terminal.args[at + 1], "haiku");
    assert_eq!(
        terminal.args.last().map(String::as_str),
        Some("what does this project expect of me?")
    );
}

#[test]
fn no_configured_model_is_no_model_flag() {
    for command in [
        Claude::new().drive_turn(&request()).unwrap(),
        Claude::new().drive_interactive(&request()).unwrap(),
    ] {
        assert!(!command.args.iter().any(|a| a == "--model"), "{command:?}");
    }
}

// `claude --effort <level>` (low, medium, high, xhigh, max; `claude --help`,
// 2.1.321): passed for a turn and for the terminal session, none named is no
// flag, and a level claude does not take is refused before it is launched.
#[test]
fn a_configured_effort_is_passed_to_claude_for_a_turn_and_for_the_terminal() {
    for level in EFFORT_LEVELS {
        let mut asked = request();
        asked.effort = level.to_string();
        for command in [
            Claude::new().drive_turn(&asked).unwrap(),
            Claude::new().drive_interactive(&asked).unwrap(),
        ] {
            let at = command.args.iter().position(|a| a == "--effort").unwrap();
            assert_eq!(command.args[at + 1], level);
        }
    }
}

#[test]
fn no_configured_effort_is_no_effort_flag() {
    for command in [
        Claude::new().drive_turn(&request()).unwrap(),
        Claude::new().drive_interactive(&request()).unwrap(),
    ] {
        assert!(!command.args.iter().any(|a| a == "--effort"), "{command:?}");
    }
}

#[test]
fn an_effort_claude_does_not_take_is_refused_before_launch() {
    let mut asked = request();
    asked.effort = "extreme".to_string();
    for refused in [
        Claude::new().drive_turn(&asked),
        Claude::new().drive_interactive(&asked),
    ] {
        assert_eq!(
            refused,
            Err(EngineError::UnknownEffort {
                engine: "claude-code".to_string(),
                level: "extreme".to_string(),
                known: "low, medium, high, xhigh, max".to_string(),
            })
        );
    }
}

// ---- engines.<claude-code>.* (owner rulings 44, 46, 48) ----

fn settings(of: impl FnOnce(&mut EngineSettings)) -> EngineSettings {
    let mut settings = EngineSettings::default();
    of(&mut settings);
    settings
}

fn names(of: &[&str]) -> Vec<String> {
    of.iter().map(ToString::to_string).collect()
}

#[test]
fn the_configured_command_is_the_program_for_a_turn_and_for_the_terminal() {
    let mut asked = request();
    asked.settings = settings(|s| s.command = "/opt/bin/claude-nightly".to_string());
    assert_eq!(
        Claude::new().drive_turn(&asked).unwrap().program,
        "/opt/bin/claude-nightly"
    );
    assert_eq!(
        Claude::new().drive_interactive(&asked).unwrap().program,
        "/opt/bin/claude-nightly"
    );
    // none configured is claude on the PATH, as before
    assert_eq!(
        Claude::new().drive_turn(&request()).unwrap().program,
        "claude"
    );
    assert_eq!(
        Claude::new().drive_interactive(&request()).unwrap().program,
        "claude"
    );
}

#[test]
fn configured_env_names_are_passed_on_besides_the_curated_ones() {
    let mut asked = request();
    asked.settings = settings(|s| s.env = names(&["ALPHA_TOKEN"]));
    asked.env = env(&[
        ("PATH", "/usr/bin"),
        ("ALPHA_TOKEN", "a"), // gitleaks:allow
        ("BETA", "b"),
    ]);
    for command in [
        Claude::new().drive_turn(&asked).unwrap(),
        Claude::new().drive_interactive(&asked).unwrap(),
    ] {
        assert_eq!(command.env["ALPHA_TOKEN"], "a");
        assert!(!command.env.contains_key("BETA"));
    }
}

// Owner ruling 15 (2026-11-09): ANTHROPIC_AUTH_TOKEN is the primary's alone,
// whatever the config lists; the hook variables are a child's too now
// (ruling 51), so they are not "interactive only" any more.
#[test]
fn the_primarys_token_is_never_a_childs_whatever_the_config_says() {
    let mut asked = request();
    asked.settings = settings(|s| s.env = names(&["ANTHROPIC_AUTH_TOKEN"]));
    asked.env = env(&[
        ("CTXLOOM_BEARER", "run.secret"),
        ("CTXLOOM_ENDPOINT", "e"),
        ("CTXLOOM_RUN", "r"),
        ("CTXLOOM_HOME", "/h"),
        ("CTXLOOM_SESSION_HARP", "brisk-copper-moth"),
        ("ANTHROPIC_AUTH_TOKEN", "primary-only"), // gitleaks:allow
    ]);
    let turn = Claude::new().drive_turn(&asked).unwrap();
    assert!(
        !turn.env.contains_key("ANTHROPIC_AUTH_TOKEN"),
        "{:?}",
        turn.env
    );
    assert!(!ENV_ALLOWLIST.contains(&"ANTHROPIC_AUTH_TOKEN"));
    for name in asked.env.keys().filter(|n| *n != "ANTHROPIC_AUTH_TOKEN") {
        assert!(turn.env.contains_key(name), "{name} missing from a child");
    }
    // The terminal session is given every one, as before.
    let terminal = Claude::new().drive_interactive(&asked).unwrap();
    for name in asked.env.keys() {
        assert!(terminal.env.contains_key(name), "{name} missing");
    }
}

// Owner ruling: there is no env_host. What an engine gets is a bounded,
// enumerable set (the container runtime will read it): the curated allowlist
// and the names the config lists, never "everything".
#[test]
fn only_the_allowlist_and_the_listed_names_are_passed() {
    let mut asked = request();
    asked.settings = settings(|s| s.env = names(&["ALPHA_TOKEN"]));
    asked.env = env(&[
        ("PATH", "/usr/bin"),
        ("ALPHA_TOKEN", "a"), // gitleaks:allow
        ("AWS_PROFILE", "dev"),
        ("HOME", "/home/alice"),
    ]);
    for command in [
        Claude::new().drive_turn(&asked).unwrap(),
        Claude::new().drive_interactive(&asked).unwrap(),
    ] {
        let names: Vec<&str> = command.env.keys().map(String::as_str).collect();
        assert_eq!(names, vec!["ALPHA_TOKEN", "CLAUDE_CONFIG_DIR", "PATH"]);
        assert_eq!(
            command.env["CLAUDE_CONFIG_DIR"],
            "/p/.ctxloom/sessions/h/home"
        );
    }
}

#[test]
fn the_configured_auth_variables_are_passed_to_a_child_and_to_the_primary() {
    let mut asked = request();
    asked.settings = settings(|s| {
        s.auth = AuthSetting::Source {
            env: names(&["ACME_TOKEN"]),
            file: None,
        }
    });
    asked.env = env(&[("ACME_TOKEN", "t"), ("ANTHROPIC_API_KEY", "k")]); // gitleaks:allow
    for command in [
        Claude::new().drive_turn(&asked).unwrap(),
        Claude::new().drive_interactive(&asked).unwrap(),
    ] {
        assert_eq!(command.env["ACME_TOKEN"], "t");
    }
}

#[test]
fn a_host_session_home_is_the_users_own_claude_directory() {
    let mut asked = request();
    asked.settings = settings(|s| s.session_home = SessionHome::Host);
    asked.env = env(&[("HOME", "/home/alice"), ("PATH", "/usr/bin")]);
    for command in [
        Claude::new().drive_turn(&asked).unwrap(),
        Claude::new().drive_interactive(&asked).unwrap(),
    ] {
        assert_eq!(command.env["CLAUDE_CONFIG_DIR"], "/home/alice/.claude");
        // HOME itself is still not handed over.
        assert!(!command.env.contains_key("HOME"));
    }
}

#[test]
fn a_host_session_home_with_no_home_in_the_environment_is_refused() {
    let mut asked = request();
    asked.settings = settings(|s| s.session_home = SessionHome::Host);
    asked.env = env(&[("PATH", "/usr/bin")]);
    let expected = EngineError::NoHostHome {
        engine: "claude-code".to_string(),
    };
    assert_eq!(Claude::new().drive_turn(&asked).unwrap_err(), expected);
    assert_eq!(
        Claude::new().drive_interactive(&asked).unwrap_err(),
        expected
    );
}

// Owner: hooks work under session_home: host too. claude reads ONE --settings
// file (the last wins) and ADDS its hooks to the config dir's own settings, so
// the terminal session is always handed the ctxloom-owned file `prepare`
// writes in the session dir, never a file in the user's ~/.claude. A child's
// turn is handed the same file (ruling 51, below).
#[test]
fn the_terminal_session_reads_the_session_dirs_settings_in_both_home_modes() {
    for host in [false, true] {
        let mut asked = request();
        asked.env = env(&[("HOME", "/home/alice"), ("PATH", "/usr/bin")]);
        asked.settings = settings(|s| {
            if host {
                s.session_home = SessionHome::Host
            }
        });
        let command = Claude::new().drive_interactive(&asked).unwrap();
        let flags: Vec<usize> = command
            .args
            .iter()
            .enumerate()
            .filter(|(_, a)| *a == "--settings")
            .map(|(i, _)| i)
            .collect();
        assert_eq!(flags.len(), 1, "host={host}: {:?}", command.args);
        assert_eq!(
            command.args[flags[0] + 1],
            "/p/.ctxloom/sessions/h/home/settings.json",
            "host={host}"
        );
        // the prompt stays the last word
        assert_eq!(
            command.args.last().map(String::as_str),
            Some(asked.prompt.as_str())
        );
        if host {
            assert_eq!(command.env["CLAUDE_CONFIG_DIR"], "/home/alice/.claude");
        }
    }
}

// Ruling 51: a child's turn reads the session dir's settings exactly as the
// terminal session does: one `--settings`, the ctxloom-owned file, in both home
// modes, and never a file in the user's ~/.claude.
#[test]
fn a_childs_turn_reads_the_session_dirs_settings_in_both_home_modes() {
    for host in [false, true] {
        let mut asked = request();
        asked.env = env(&[("HOME", "/home/alice")]);
        asked.settings = settings(|s| {
            if host {
                s.session_home = SessionHome::Host
            }
        });
        for resume in [None, Some("native-1".to_string())] {
            asked.resume = resume;
            let command = Claude::new().drive_turn(&asked).unwrap();
            let flags: Vec<usize> = command
                .args
                .iter()
                .enumerate()
                .filter(|(_, a)| *a == "--settings")
                .map(|(i, _)| i)
                .collect();
            assert_eq!(flags.len(), 1, "host={host}: {:?}", command.args);
            assert_eq!(
                command.args[flags[0] + 1],
                "/p/.ctxloom/sessions/h/home/settings.json",
                "host={host}"
            );
        }
    }
}
