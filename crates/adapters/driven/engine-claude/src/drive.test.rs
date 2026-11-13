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
        user_home: None,
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
            "/p/.ctxloom/sessions/h/home/ctxloom-hooks.json",
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
    // HOME is the session home (owner ruling 112), never the caller's.
    assert_eq!(
        names,
        vec![
            "CLAUDE_CODE_OAUTH_TOKEN",
            "CLAUDE_CONFIG_DIR",
            "HOME",
            "HTTPS_PROXY",
            "PATH"
        ]
    );
    assert_eq!(command.env["HOME"], "/p/.ctxloom/sessions/h/home");
}

#[test]
fn preparing_a_home_writes_the_context_file_it_names() {
    let prepared = Claude::new()
        .prepare(&PrepareRequest {
            home: "/h".to_string(),
            context: "the context".to_string(),
            offered: vec!["settings.json".to_string(), ".credentials.json".to_string()],
            interactive: false,
            cwd: None,
            existing: Default::default(),
            permissions: None,
            session_home: run_ports::SessionHome::Session,
            surfaces: Default::default(),
        })
        .unwrap();
    assert_eq!(prepared.context_path, format!("/h/{CONTEXT_PROMPT_FILE}"));
    // The context, ctxloom's hooks (the `--settings` file), the user-scope
    // settings that hold the agent's permissions (ruling 145), and claude's
    // own state (ruling 113).
    assert_eq!(prepared.files.len(), 4);
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
            "/p/.ctxloom/sessions/h/home/ctxloom-hooks.json"
        ]
    );
    assert!(command.stdin.is_empty());
    assert_eq!(command.cwd.as_deref(), Some("/p"));
    let names: Vec<&str> = command.env.keys().map(String::as_str).collect();
    assert_eq!(names, vec!["CLAUDE_CONFIG_DIR", "HOME", "PATH"]);
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

// Owner ruling 112 (2026-11-10): the engine's HOME is its session home, the
// terminal's and a child's turn alike (parity with opencode, ruling 58), so
// what its tools write under `~` lands in the session's own directory and
// never in the project (the hand test found gh's `.local/state` there, made
// relative to the working directory with no HOME). ctxloom's own home still
// reaches the hook processes by name, as CTXLOOM_HOME.
#[test]
fn the_engines_home_is_the_session_home_and_ctxlooms_home_is_named() {
    let mut asked = request();
    asked.env = env(&[
        ("PATH", "/usr/bin"),
        ("HOME", "/home/alice"),
        ("CTXLOOM_HOME", "/home/alice"),
    ]);
    for command in [
        Claude::new().drive_interactive(&asked).unwrap(),
        Claude::new().drive_turn(&asked).unwrap(),
    ] {
        let names: Vec<&str> = command.env.keys().map(String::as_str).collect();
        assert_eq!(
            names,
            vec!["CLAUDE_CONFIG_DIR", "CTXLOOM_HOME", "HOME", "PATH"]
        );
        assert_eq!(command.env["HOME"], "/p/.ctxloom/sessions/h/home");
        assert_eq!(command.env["CTXLOOM_HOME"], "/home/alice");
    }
}

fn prepared(interactive: bool) -> run_ports::Prepared {
    Claude::new()
        .prepare(&PrepareRequest {
            home: "/h".to_string(),
            context: "the context".to_string(),
            offered: Vec::new(),
            interactive,
            cwd: None,
            existing: Default::default(),
            permissions: None,
            session_home: run_ports::SessionHome::Session,
            surfaces: Default::default(),
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
        .find(|f| f.rel == HOOKS_FILE)
        .expect("ctxloom's hooks file in the home");
    let doc = surface_ports::Json::parse(std::str::from_utf8(&settings.bytes).unwrap()).unwrap();
    assert!(crate::hooks::has_callback(&doc, "ctxloom"), "{doc:?}");
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
        .find(|f| f.rel == HOOKS_FILE)
        .expect("ctxloom's hooks file in the home");
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
    assert_eq!(
        prepared.files.len(),
        4,
        "the context, the hooks, the permissions settings and claude's state (ruling 113)"
    );
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

// The login is the config dir's own file (the session home is the config
// dir), `HOME` alone places the user's, and a missing one is said with
// claude's own ways to sign in.
#[test]
fn the_login_is_the_homes_own_file_and_a_missing_one_says_how_to_sign_in() {
    let credentials = Claude::new().credentials();
    assert_eq!(credentials.home_dir, "");
    assert_eq!(credentials.user_dir_variable, None);
    assert!(!credentials.login_optional);
    assert!(credentials.sign_in.contains("/login"));
    assert!(credentials.sign_in.contains("claude setup-token"));
    assert!(credentials.sign_in.contains("CLAUDE_CODE_OAUTH_TOKEN"));
    // Ruling 80: claude's macOS keychain and Windows' symlink privilege are
    // claude's no-link platforms; nothing of its login is ever copied.
    assert_eq!(credentials.no_link_on, ["macos", "windows"]);
    assert!(credentials.copy_on.is_empty());
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
    for name in run_ports::HOOK_VARIABLES.into_iter().chain([
        run_ports::HOME_VARIABLE,
        run_ports::HARP_VARIABLE,
        run_ports::ROOT_VARIABLE,
    ]) {
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

/// ctxloom's own settings file in the session home: the hooks, which the
/// command names with `--settings` (owner ruling 145).
const HOOKS_FILE: &str = "ctxloom-hooks.json";

/// The file of a prepared home at `rel`, as text.
fn file_of(prepared: &run_ports::Prepared, rel: &str) -> Option<String> {
    prepared
        .files
        .iter()
        .find(|f| f.rel == rel)
        .map(|f| String::from_utf8(f.bytes.clone()).unwrap())
}

/// A session-home `prepare`, given `permissions` and what the home holds.
fn prepare_home(
    interactive: bool,
    permissions: Option<&str>,
    existing: BTreeMap<String, Vec<u8>>,
) -> run_ports::Prepared {
    Claude::new()
        .prepare(&PrepareRequest {
            home: "/h".to_string(),
            context: "c".to_string(),
            offered: Vec::new(),
            interactive,
            cwd: None,
            existing,
            permissions: permissions.map(String::from),
            session_home: run_ports::SessionHome::Session,
            surfaces: Default::default(),
        })
        .unwrap()
}

// Owner rulings 135, 137, 145: the agent's permissions document is the
// `permissions` object of the user-scope `settings.json` of the session home
// (the session's CLAUDE_CONFIG_DIR), exactly as handed (its order and its
// words, the retired `mode` among them: nothing is checked), and NOT in the
// `--settings` file that holds ctxloom's hooks: claude refuses a settings file
// whole for one invalid value, hooks included, so a bad value in the block
// must disable the block alone. Probe (claude 2.1.322, no credentials, no
// model call; `env -i PATH HOME=<tmp> CLAUDE_CONFIG_DIR=<tmp>/cfg claude -p
// --settings <tmp>/hooks.json hi`), with `{"permissions":{"defaultMode":
// "bogusMode"}}` in <tmp>/cfg/settings.json:
//   These settings files did not load, so nothing in them is in effect,
//   including hooks and permission rules. [...]
//     <tmp>/cfg/settings.json (permissions.defaultMode): Invalid value.
//     Expected one of: "acceptEdits", "auto", "bypassPermissions", "default",
//     "dontAsk", "plan"
// (only that file is named: the hooks file loads); the same block inside the
// hooks file names the hooks file, so its hooks are off.
#[test]
fn the_permissions_document_is_the_user_settings_and_never_the_hooks_file() {
    const DOCUMENT: &str = r#"{"mode":"nonsense","defaultMode":"plan","deny":["WebFetch","Bash(rm *)"],"allow":["Read"],"hooks":{"Stop":[]},"blockWritesOutsideWorkingDirectories":true,"n":7}"#;
    for interactive in [false, true] {
        let prepared = prepare_home(interactive, Some(DOCUMENT), BTreeMap::new());
        let text = file_of(&prepared, "settings.json").expect("settings.json in the home");
        let doc = surface_ports::Json::parse(&text).unwrap();
        // The file is the document as its `permissions`, and nothing else.
        assert_eq!(
            doc,
            surface_ports::Json::object([(
                "permissions",
                surface_ports::Json::parse(DOCUMENT).unwrap()
            )]),
            "{text}"
        );
        let keys: Vec<&str> = match doc.get("permissions") {
            Some(surface_ports::Json::Object(members)) => {
                members.iter().map(|(k, _)| k.as_str()).collect()
            }
            other => panic!("{other:?}"),
        };
        assert_eq!(
            keys,
            [
                "mode",
                "defaultMode",
                "deny",
                "allow",
                "hooks",
                "blockWritesOutsideWorkingDirectories",
                "n"
            ]
        );
        // The hooks file holds ctxloom's hooks alone, as with no document.
        let hooks = file_of(&prepared, HOOKS_FILE).expect("the hooks file");
        assert_eq!(
            Some(hooks.clone()),
            file_of(
                &prepare_home(interactive, None, BTreeMap::new()),
                HOOKS_FILE
            )
        );
        let hooks = surface_ports::Json::parse(&hooks).unwrap();
        assert!(hooks.get("permissions").is_none(), "{hooks:?}");
        assert!(crate::hooks::has_callback(&hooks, "ctxloom"), "{hooks:?}");
    }
}

// Owner T21: both files are written whole from what the launch is handed,
// never merged into what the home holds (neither is a file claude merges
// into), so an agent that loosened its own settings finds them regenerated at
// its next launch, a terminal's, a turn's and a resume's.
#[test]
fn the_settings_are_regenerated_whatever_the_home_holds() {
    for rel in ["settings.json", HOOKS_FILE] {
        assert!(!Claude::new().merges().contains(&rel));
    }
    let loosened = br#"{"permissions":{"defaultMode":"bypassPermissions"}}"#.to_vec();
    let edited = BTreeMap::from([
        ("settings.json".to_string(), loosened.clone()),
        (HOOKS_FILE.to_string(), loosened),
    ]);
    for interactive in [false, true] {
        let fresh = prepare_home(
            interactive,
            Some(r#"{"defaultMode":"acceptEdits"}"#),
            BTreeMap::new(),
        );
        let again = prepare_home(
            interactive,
            Some(r#"{"defaultMode":"acceptEdits"}"#),
            edited.clone(),
        );
        for rel in ["settings.json", HOOKS_FILE] {
            assert_eq!(
                file_of(&again, rel),
                file_of(&fresh, rel),
                "{rel} interactive={interactive}"
            );
        }
    }
}

// No document: the user-scope settings are empty (so nothing an agent wrote
// there lingers), and the hooks file holds no permissions.
#[test]
fn with_no_document_the_settings_hold_no_permissions() {
    for interactive in [false, true] {
        let prepared = prepare_home(interactive, None, BTreeMap::new());
        assert_eq!(file_of(&prepared, "settings.json").as_deref(), Some("{}\n"));
        let hooks = file_of(&prepared, HOOKS_FILE).expect("the hooks file");
        let doc = surface_ports::Json::parse(&hooks).unwrap();
        assert!(doc.get("permissions").is_none(), "{hooks}");
        assert!(crate::hooks::has_callback(&doc, "ctxloom"), "{hooks}");
    }
}

// A document that is not JSON (ctxloom's own rendering, so a caller's bug) is
// refused before anything is written, in words.
#[test]
fn a_document_that_is_not_json_is_refused() {
    let refused = Claude::new().prepare(&PrepareRequest {
        home: "/h".to_string(),
        context: "c".to_string(),
        offered: Vec::new(),
        interactive: false,
        cwd: None,
        existing: Default::default(),
        permissions: Some("{not json".to_string()),
        session_home: run_ports::SessionHome::Session,
        surfaces: Default::default(),
    });
    assert!(
        matches!(&refused, Err(EngineError::BadPermissions { engine, .. }) if engine == "claude-code"),
        "{refused:?}"
    );
}

// Ruling 137 retires ruling 116's flag: no `--permission-mode` is passed, for
// a turn or for the terminal, whatever the document says.
#[test]
fn no_permission_mode_flag_is_ever_passed() {
    let asked = request();
    for command in [
        Claude::new().drive_turn(&asked).unwrap(),
        Claude::new().drive_interactive(&asked).unwrap(),
    ] {
        assert!(
            !command.args.iter().any(|a| a.contains("permission")),
            "{command:?}"
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
        assert_eq!(
            names,
            vec!["ALPHA_TOKEN", "CLAUDE_CONFIG_DIR", "HOME", "PATH"]
        );
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
    asked.user_home = Some("/home/alice".to_string());
    for command in [
        Claude::new().drive_turn(&asked).unwrap(),
        Claude::new().drive_interactive(&asked).unwrap(),
    ] {
        assert_eq!(command.env["CLAUDE_CONFIG_DIR"], "/home/alice/.claude");
        // The user's own home is the engine's, as opencode's is (ruling 112).
        assert_eq!(command.env["HOME"], "/home/alice");
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
        asked.user_home = Some("/home/alice".to_string());
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
            "/p/.ctxloom/sessions/h/home/ctxloom-hooks.json",
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
        asked.user_home = Some("/home/alice".to_string());
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
                "/p/.ctxloom/sessions/h/home/ctxloom-hooks.json",
                "host={host}"
            );
        }
    }
}

// Ruling 58: claude's SessionEnd hook says a session ended; its exit does not.
#[test]
fn test_its_session_end_is_its_hooks_not_its_exit() {
    assert!(!Claude::new().session_ends_with_exit());
}

/// claude's own state file, `prepare`d for a home whose `.claude.json` holds
/// `existing` (none: not there), for a session run in `cwd`.
fn claude_state(existing: Option<&str>, cwd: Option<&str>) -> Option<String> {
    let prepared = Claude::new()
        .prepare(&PrepareRequest {
            home: "/h".to_string(),
            context: "the context".to_string(),
            offered: Vec::new(),
            interactive: true,
            cwd: cwd.map(String::from),
            existing: existing
                .map(|text| {
                    BTreeMap::from([(".claude.json".to_string(), text.as_bytes().to_vec())])
                })
                .unwrap_or_default(),
            permissions: None,
            session_home: run_ports::SessionHome::Session,
            surfaces: Default::default(),
        })
        .unwrap();
    prepared
        .files
        .iter()
        .find(|file| file.rel == ".claude.json")
        .map(|file| String::from_utf8(file.bytes.clone()).unwrap())
}

fn json(text: &str) -> serde_json::Value {
    serde_json::from_str(text).unwrap()
}

// Owner ruling 113: a session home's `.claude.json` (claude's state, in its
// config dir) is seeded with the minimal keys claude 2.1.322 reads: the
// onboarding done (no theme picker, no security notes) and the session's
// project directory trusted (no "is this a project you trust?" prompt, whose
// default is "No, exit"). Verified live in a fresh config dir.
#[test]
fn a_fresh_home_is_seeded_with_onboarding_done_and_the_project_trusted() {
    assert_eq!(Claude::new().merges(), [".claude.json"]);
    let state = claude_state(None, Some("/p")).expect("seeded");
    assert_eq!(
        json(&state),
        serde_json::json!({
            "hasCompletedOnboarding": true,
            "projects": {"/p": {"hasTrustDialogAccepted": true}}
        })
    );
    // No project directory named: the onboarding alone.
    assert_eq!(
        json(&claude_state(None, None).unwrap()),
        serde_json::json!({"hasCompletedOnboarding": true})
    );
}

// What claude keeps there itself (a resumed session's home has it) is kept:
// the two keys are merged in, never the file replaced.
#[test]
fn the_seed_merges_into_what_claude_keeps_there() {
    let existing = r#"{
  "numStartups": 3,
  "hasCompletedOnboarding": false,
  "projects": {
    "/elsewhere": {"hasTrustDialogAccepted": false},
    "/p": {"allowedTools": ["Bash"], "hasTrustDialogAccepted": false}
  },
  "tipsHistory": {"x": 1}
}"#;
    let state = claude_state(Some(existing), Some("/p")).unwrap();
    assert_eq!(
        json(&state),
        serde_json::json!({
            "numStartups": 3,
            "hasCompletedOnboarding": true,
            "projects": {
                "/elsewhere": {"hasTrustDialogAccepted": false},
                "/p": {"allowedTools": ["Bash"], "hasTrustDialogAccepted": true}
            },
            "tipsHistory": {"x": 1}
        })
    );
}

// A state file that is not a JSON object is claude's to deal with: it is left
// exactly as it is (the home gets no `.claude.json` to write).
#[test]
fn a_state_file_that_is_not_an_object_is_left_as_it_is() {
    assert_eq!(claude_state(Some("{not json"), Some("/p")), None);
    assert_eq!(claude_state(Some("[1, 2]"), Some("/p")), None);
}

// Owner ruling 145, under `session_home: host`: the config dir is the user's
// own `~/.claude`, whose `settings.json` ctxloom never writes, so no
// user-scope file is written at all and the agent's document stays the
// `permissions` of ctxloom's hooks file (the cost, said in the docs: one
// invalid value there disables the hooks too). A document that is not JSON
// is refused in both modes.
#[test]
fn under_a_host_home_the_block_stays_in_the_hooks_file_and_no_user_settings_are_written() {
    const DOCUMENT: &str = r#"{"defaultMode":"plan","allow":["Read"]}"#;
    for interactive in [false, true] {
        let prepared = Claude::new()
            .prepare(&PrepareRequest {
                home: "/h".to_string(),
                context: "c".to_string(),
                offered: Vec::new(),
                interactive,
                cwd: None,
                existing: Default::default(),
                permissions: Some(DOCUMENT.to_string()),
                session_home: SessionHome::Host,
                surfaces: Default::default(),
            })
            .unwrap();
        assert_eq!(file_of(&prepared, "settings.json"), None);
        let hooks = file_of(&prepared, HOOKS_FILE).expect("the hooks file");
        let doc = surface_ports::Json::parse(&hooks).unwrap();
        assert_eq!(
            doc.get("permissions"),
            Some(&surface_ports::Json::parse(DOCUMENT).unwrap())
        );
        assert!(crate::hooks::has_callback(&doc, "ctxloom"), "{hooks}");
    }
    for home in [SessionHome::Session, SessionHome::Host] {
        let refused = Claude::new().prepare(&PrepareRequest {
            home: "/h".to_string(),
            context: "c".to_string(),
            offered: Vec::new(),
            interactive: false,
            cwd: None,
            existing: Default::default(),
            permissions: Some("{nope".to_string()),
            session_home: home,
            surfaces: Default::default(),
        });
        assert!(
            matches!(&refused, Err(EngineError::BadPermissions { .. })),
            "{home:?}: {refused:?}"
        );
    }
}

// Owner ruling 163: a session home's hooks call the running binary by its file
// name, the terminal's and a child's turn's alike.
#[test]
fn a_home_prepared_by_a_claude_calling_another_program_calls_that_program() {
    for interactive in [true, false] {
        let prepared = Claude::calling("ctxloom-other")
            .prepare(&PrepareRequest {
                home: "/h".to_string(),
                context: "the context".to_string(),
                offered: Vec::new(),
                interactive,
                cwd: None,
                existing: Default::default(),
                permissions: None,
                session_home: run_ports::SessionHome::Session,
                surfaces: Default::default(),
            })
            .unwrap();
        let settings = prepared
            .files
            .iter()
            .find(|f| f.rel == HOOKS_FILE)
            .expect("ctxloom's hooks file in the home");
        let text = std::str::from_utf8(&settings.bytes).unwrap();
        let doc = surface_ports::Json::parse(text).unwrap();
        assert!(
            crate::hooks::has_callback(&doc, "ctxloom-other"),
            "{interactive}: {text}"
        );
        assert!(!text.contains(r#""command": "ctxloom""#), "{text}");
    }
}

// The terminal session is given the terminal's type and colour depth, so
// claude draws in colour; a child's turn writes to a pipe and needs neither.
#[test]
fn the_terminal_session_is_told_the_terminal_and_its_colours() {
    let mut asked = request();
    asked.env = env(&[("TERM", "xterm-256color"), ("COLORTERM", "truecolor")]);
    let command = Claude::new().drive_interactive(&asked).unwrap();
    assert_eq!(
        command.env.get("TERM").map(String::as_str),
        Some("xterm-256color")
    );
    assert_eq!(
        command.env.get("COLORTERM").map(String::as_str),
        Some("truecolor")
    );
    let turn = Claude::new().drive_turn(&asked).unwrap();
    assert!(!turn.env.contains_key("TERM"), "{:?}", turn.env.keys());
    assert!(!turn.env.contains_key("COLORTERM"), "{:?}", turn.env.keys());
}

/// The user's own settings file, read, at a path of hers.
fn users(text: &str) -> run_ports::UserSettings {
    run_ports::UserSettings {
        path: "/home/a/.claude/settings.json".to_string(),
        bytes: Some(text.as_bytes().to_vec()),
    }
}

/// A `prepare` by a Claude told the user's own settings (`user`), the agent's
/// `permissions` given.
fn prepare_over(
    user: Option<run_ports::UserSettings>,
    permissions: Option<&str>,
    session_home: run_ports::SessionHome,
) -> run_ports::Prepared {
    Claude::new()
        .over_user_settings(user)
        .prepare(&PrepareRequest {
            home: "/h".to_string(),
            context: "c".to_string(),
            offered: Vec::new(),
            interactive: true,
            cwd: None,
            existing: BTreeMap::new(),
            permissions: permissions.map(str::to_string),
            session_home,
            surfaces: Default::default(),
        })
        .unwrap()
}

/// The session home's `settings.json`, parsed.
fn home_settings(prepared: &run_ports::Prepared) -> surface_ports::Json {
    surface_ports::Json::parse(&file_of(prepared, "settings.json").expect("settings.json")).unwrap()
}

fn doc(text: &str) -> surface_ports::Json {
    surface_ports::Json::parse(text).unwrap()
}

// Owner ruling 169: the session home's user-scope settings are the user's
// own, ctxloom's values laid over them: her attribution (and model, env,
// plugins, hooks, statusline, ...) reach the session as she wrote them.
#[test]
fn the_users_own_settings_are_the_session_homes_base() {
    let prepared = prepare_over(
        Some(users(
            r#"{"attribution":{"commit":"","pr":""},"model":"opus","env":{"K":"v"}}"#,
        )),
        Some(r#"{"defaultMode":"acceptEdits"}"#),
        run_ports::SessionHome::Session,
    );
    assert_eq!(
        home_settings(&prepared),
        doc(
            r#"{"attribution":{"commit":"","pr":""},"model":"opus","env":{"K":"v"},"permissions":{"defaultMode":"acceptEdits"}}"#
        )
    );
    assert!(prepared.left_out.is_empty());
}

// Ruling 169: `permissions` is the agent's document WHOLE, never a merge with
// hers; with no document, none of hers is left either.
#[test]
fn the_agents_permissions_replace_the_users_whole() {
    let hers = r#"{"permissions":{"allow":["Bash(*)"],"defaultMode":"bypassPermissions"},"x":1}"#;
    let prepared = prepare_over(
        Some(users(hers)),
        Some(r#"{"deny":["WebFetch"]}"#),
        run_ports::SessionHome::Session,
    );
    assert_eq!(
        home_settings(&prepared),
        doc(r#"{"permissions":{"deny":["WebFetch"]},"x":1}"#)
    );
    let none = prepare_over(Some(users(hers)), None, run_ports::SessionHome::Session);
    assert_eq!(home_settings(&none), doc(r#"{"x":1}"#));
}

// Ruling 169 (and 166, now a case of it): her acceptance of claude's
// bypass-permissions warning is in her settings, so it crosses with them,
// and a session she already accepted it for is not asked again.
#[test]
fn the_users_acceptance_of_the_bypass_warning_crosses_with_her_settings() {
    let prepared = prepare_over(
        Some(users(r#"{"skipDangerousModePermissionPrompt":true}"#)),
        Some(r#"{"defaultMode":"bypassPermissions"}"#),
        run_ports::SessionHome::Session,
    );
    assert_eq!(
        home_settings(&prepared),
        doc(
            r#"{"skipDangerousModePermissionPrompt":true,"permissions":{"defaultMode":"bypassPermissions"}}"#
        )
    );
}

// Ruling 169: a file that is not a JSON object (or that could not be read) is
// left out, named, never a refusal: the home starts from nothing of hers.
#[test]
fn a_users_file_that_is_no_object_is_left_out_and_named() {
    let unread = run_ports::UserSettings {
        path: "/home/a/.claude/settings.json".to_string(),
        bytes: None,
    };
    for user in [users("[1]"), users("not json"), users(""), unread] {
        let prepared = prepare_over(
            Some(user.clone()),
            Some(r#"{"defaultMode":"acceptEdits"}"#),
            run_ports::SessionHome::Session,
        );
        assert_eq!(
            home_settings(&prepared),
            doc(r#"{"permissions":{"defaultMode":"acceptEdits"}}"#),
            "{user:?}"
        );
        assert_eq!(
            prepared.left_out,
            vec![run_ports::LeftOut {
                engine: "claude-code".to_string(),
                path: "/home/a/.claude/settings.json".to_string(),
            }],
            "{user:?}"
        );
    }
}

// Ruling 169: no file of hers is a start from `{}`, and nothing to say.
#[test]
fn a_missing_users_file_is_a_start_from_nothing() {
    let prepared = prepare_over(
        None,
        Some(r#"{"defaultMode":"acceptEdits"}"#),
        run_ports::SessionHome::Session,
    );
    assert_eq!(
        home_settings(&prepared),
        doc(r#"{"permissions":{"defaultMode":"acceptEdits"}}"#)
    );
    assert!(prepared.left_out.is_empty());
    let bare = prepare_over(None, None, run_ports::SessionHome::Session);
    assert_eq!(file_of(&bare, "settings.json").as_deref(), Some("{}\n"));
}

// T21 and ruling 169: the home's settings are rewritten whole from hers at
// every launch (a file `prepare` writes, never one it merges into), so an
// edit of hers reaches the next launch and the session's own edit does not
// survive it.
#[test]
fn the_users_settings_are_copied_afresh_at_every_launch() {
    assert!(!Claude::new().merges().contains(&"settings.json"));
    let first = prepare_over(
        Some(users(r#"{"model":"opus"}"#)),
        None,
        run_ports::SessionHome::Session,
    );
    let second = prepare_over(
        Some(users(r#"{"model":"sonnet"}"#)),
        None,
        run_ports::SessionHome::Session,
    );
    assert_eq!(home_settings(&first), doc(r#"{"model":"opus"}"#));
    assert_eq!(home_settings(&second), doc(r#"{"model":"sonnet"}"#));
}

// Ruling 169: under `session_home: host` the config dir IS hers, never
// written: nothing of her settings is copied anywhere, and nothing is said.
#[test]
fn under_a_host_home_nothing_of_her_settings_is_copied() {
    for user in [users(r#"{"attribution":{"commit":"MARK"}}"#), users("[1]")] {
        let prepared = prepare_over(
            Some(user),
            Some(r#"{"defaultMode":"acceptEdits"}"#),
            run_ports::SessionHome::Host,
        );
        assert_eq!(file_of(&prepared, "settings.json"), None);
        assert!(prepared.left_out.is_empty());
        for file in &prepared.files {
            assert!(
                !String::from_utf8_lossy(&file.bytes).contains("MARK"),
                "{}",
                file.rel
            );
        }
    }
}

/// A Windows caller's environment, its names as the composition root keys
/// them (uppercase).
fn windows_caller() -> BTreeMap<String, String> {
    env(&[
        ("PATH", r"C:\ctxloom;C:\Windows\system32"),
        ("SYSTEMROOT", r"C:\Windows"),
        ("SYSTEMDRIVE", "C:"),
        ("WINDIR", r"C:\Windows"),
        ("TEMP", r"C:\Users\her\AppData\Local\Temp"),
        ("TMP", r"C:\Users\her\AppData\Local\Temp"),
        ("PATHEXT", ".COM;.EXE;.BAT;.CMD"),
        ("COMSPEC", r"C:\Windows\system32\cmd.exe"),
        ("USERPROFILE", r"C:\Users\her"),
        ("APPDATA", r"C:\Users\her\AppData\Roaming"),
        ("LOCALAPPDATA", r"C:\Users\her\AppData\Local"),
    ])
}

// On Windows a program cannot start without the system's directories, and
// claude finds its home through USERPROFILE (node's `os.homedir()`): both
// allowlists hold every startup and profile variable.
#[test]
fn every_windows_startup_and_profile_variable_is_on_both_allowlists() {
    let profile = run_ports::WINDOWS_PROFILE_VARIABLES.iter().map(|(n, _)| n);
    for name in run_ports::WINDOWS_STARTUP_VARIABLES.iter().chain(profile) {
        assert!(ENV_ALLOWLIST.contains(name), "{name} reaches a turn");
        assert!(
            INTERACTIVE_ENV_ALLOWLIST.contains(name),
            "{name} reaches the terminal"
        );
    }
}

// Ruling 112 on Windows: the session home is the engine's USERPROFILE as it is
// its HOME, and APPDATA and LOCALAPPDATA are where Windows puts them under it;
// the system's variables pass on as they are.
#[test]
fn on_windows_the_profile_is_the_session_home_and_the_system_passes_on() {
    let mut asked = request();
    asked.env = windows_caller();
    for command in [
        Claude::new().drive_turn(&asked).unwrap(),
        Claude::new().drive_interactive(&asked).unwrap(),
    ] {
        let home = asked.home.as_str();
        assert_eq!(command.env["USERPROFILE"], home);
        assert_eq!(command.env["APPDATA"], format!("{home}/AppData/Roaming"));
        assert_eq!(command.env["LOCALAPPDATA"], format!("{home}/AppData/Local"));
        for name in run_ports::WINDOWS_STARTUP_VARIABLES {
            assert_eq!(command.env[*name], asked.env[*name], "{name}");
        }
        assert_eq!(command.env["PATH"], asked.env["PATH"]);
    }
}

// Under `session_home: host` her home is the one the composition root
// resolved (USERPROFILE on Windows), never the environment's HOME (Git Bash
// sets one of its own), and her profile variables pass on as they are.
#[test]
fn a_host_session_home_is_the_resolved_home_and_her_profile_passes_on() {
    let mut asked = request();
    asked.settings = settings(|s| s.session_home = SessionHome::Host);
    asked.env = windows_caller();
    asked
        .env
        .insert("HOME".to_string(), "/c/Users/her".to_string());
    asked.user_home = Some(r"C:\Users\her".to_string());
    for command in [
        Claude::new().drive_turn(&asked).unwrap(),
        Claude::new().drive_interactive(&asked).unwrap(),
    ] {
        assert_eq!(command.env["HOME"], r"C:\Users\her");
        assert_eq!(command.env["CLAUDE_CONFIG_DIR"], r"C:\Users\her/.claude");
        for (name, _) in run_ports::WINDOWS_PROFILE_VARIABLES {
            assert_eq!(command.env[*name], asked.env[*name], "{name}");
        }
    }
}

// Ruling 168: claude-code has nothing to say of a plan, a model or none.
#[test]
fn claude_advises_nothing_of_a_plan() {
    use run_ports::Engine;
    let planned = run_ports::Planned {
        agent: "dev".to_string(),
        ..run_ports::Planned::default()
    };
    assert!(Claude::new().advise(&planned).is_empty());
}

use crate::drive::MCP_CONFIG_FILE;

// ------------------------------------------- what a launch carries (claude)
//
// Owner ruling 2026-11-12: everything ctxloom delivered at rest rides the
// launch, from the run agent's selection, in every working directory. Probed
// on claude 2.1.322 (no model call: the stream's `init` event, from a
// temporary project and a temporary CLAUDE_CONFIG_DIR):
// - `--mcp-config <file>` loads its servers (`source: dynamic`, connected),
//   and a server of the same name as a project `.mcp.json` one replaces it;
// - `commands/<name>.md` and `skills/<name>/SKILL.md` under CLAUDE_CONFIG_DIR
//   are offered (`slash_commands`, `skills`);
// - `--disallowedTools` is variadic, so it is not used: the deny list is
//   `permissions.deny` where the agent's document lives.

/// What a launch with `surfaces` prepares for a session home (`host`: the
/// user's own config dir).
fn prepare_carrying(
    interactive: bool,
    session_home: SessionHome,
    permissions: Option<&str>,
    surfaces: surface_ports::Inputs,
) -> run_ports::Prepared {
    Claude::new()
        .prepare(&PrepareRequest {
            home: "/h".to_string(),
            context: "c".to_string(),
            offered: Vec::new(),
            interactive,
            cwd: Some("/p".to_string()),
            existing: Default::default(),
            permissions: permissions.map(String::from),
            session_home,
            surfaces: run_ports::LaunchSurfaces(surfaces),
        })
        .unwrap()
}

fn hook(command: &str, args: &[&str]) -> surface_ports::Hook {
    surface_ports::Hook {
        command: command.to_string(),
        args: args.iter().map(ToString::to_string).collect(),
        ..Default::default()
    }
}

fn carried() -> surface_ports::Inputs {
    let mut hooks = std::collections::BTreeMap::new();
    hooks.insert("pre_shell".to_string(), vec![hook("echo", &["AUTHORED"])]);
    hooks.insert(
        "session_start".to_string(),
        vec![hook("ctxloom", &["hook", "session-start"])],
    );
    hooks.insert(
        "post_tool".to_string(),
        vec![surface_ports::Hook {
            tool: "skill".to_string(),
            ..hook("ctxloom", &["hook", "skill-mates"])
        }],
    );
    surface_ports::Inputs {
        hooks,
        statusline: true,
        deny_tools: vec!["Bash".to_string(), "WebFetch".to_string()],
        mcp: vec![surface_ports::McpServer {
            name: "tool".to_string(),
            command: "tool-cmd".to_string(),
            args: vec!["--flag".to_string()],
            ..Default::default()
        }],
        commands: vec![surface_ports::Command {
            name: "demo/example".to_string(),
            description: "Example".to_string(),
            body: "EXAMPLE-BODY".to_string(),
        }],
        skills: vec![surface_ports::Skill {
            name: "reviewer".to_string(),
            description: "Reviews".to_string(),
            files: vec![surface_ports::SkillFile {
                path: "SKILL.md".to_string(),
                bytes: b"---\nname: reviewer\n---\n".to_vec(),
                executable: false,
            }],
        }],
        ..Default::default()
    }
}

fn hooks_doc(prepared: &run_ports::Prepared) -> surface_ports::Json {
    surface_ports::Json::parse(&file_of(prepared, HOOKS_FILE).expect("the hooks file")).unwrap()
}

/// Every hook command line of `event` in a claude settings document.
fn commands_of(doc: &surface_ports::Json, event: &str) -> Vec<String> {
    doc.get("hooks")
        .and_then(|h| h.get(event))
        .and_then(surface_ports::Json::as_array)
        .into_iter()
        .flatten()
        .flat_map(|group| {
            group
                .get("hooks")
                .and_then(surface_ports::Json::as_array)
                .cloned()
                .unwrap_or_default()
        })
        .map(|h| {
            let mut line = vec![h
                .get("command")
                .and_then(surface_ports::Json::as_str)
                .unwrap_or_default()
                .to_string()];
            for arg in h
                .get("args")
                .and_then(surface_ports::Json::as_array)
                .cloned()
                .unwrap_or_default()
            {
                line.push(arg.as_str().unwrap_or_default().to_string());
            }
            line.join(" ")
        })
        .collect()
}

// The authored hooks and ctxloom's own callbacks are the hooks file's,
// beside the capture events, each once.
#[test]
fn the_authored_hooks_and_ctxlooms_callbacks_ride_the_hooks_file() {
    let prepared = prepare_carrying(true, SessionHome::Session, None, carried());
    let doc = hooks_doc(&prepared);
    let pre = commands_of(&doc, "PreToolUse");
    assert!(pre.contains(&"echo AUTHORED".to_string()), "{pre:?}");
    let start = commands_of(&doc, "SessionStart");
    assert_eq!(
        start,
        ["ctxloom hook claude-code execute SessionStart"],
        "the capture callback and the session-start one are one entry"
    );
    let post = commands_of(&doc, "PostToolUse");
    assert!(
        post.contains(&"ctxloom hook claude-code execute PostToolUse".to_string()),
        "skill-mates: {post:?}"
    );
    // The capture events are still there.
    assert!(!commands_of(&doc, "Stop").is_empty());
}

// A child's turn registers no SessionStart or SessionEnd (owner ruling 51:
// `claude -p` fires them once per process), whatever the selection carries.
#[test]
fn a_childs_turn_carries_no_session_start_hook() {
    let prepared = prepare_carrying(false, SessionHome::Session, None, carried());
    let doc = hooks_doc(&prepared);
    assert!(commands_of(&doc, "SessionStart").is_empty());
    assert!(commands_of(&doc, "PreToolUse").contains(&"echo AUTHORED".to_string()));
}

// The statusline rides the hooks file (the `--settings` file outranks user
// scope); none when the project manages none.
#[test]
fn the_statusline_rides_the_hooks_file() {
    let prepared = prepare_carrying(true, SessionHome::Session, None, carried());
    let doc = hooks_doc(&prepared);
    assert_eq!(
        doc.get("statusLine")
            .and_then(|s| s.get("command"))
            .and_then(surface_ports::Json::as_str),
        Some("ctxloom hook claude-code execute statusline")
    );
    let none = prepare_carrying(
        true,
        SessionHome::Session,
        None,
        surface_ports::Inputs::default(),
    );
    assert!(hooks_doc(&none).get("statusLine").is_none());
}

// deny_tools is the session home's `permissions.deny`, laid over the agent's
// document (its own deny kept, each once); with no document, the deny alone.
#[test]
fn deny_tools_ride_the_permissions_where_the_document_lives() {
    let doc = |prepared: &run_ports::Prepared| {
        surface_ports::Json::parse(&file_of(prepared, "settings.json").unwrap()).unwrap()
    };
    let deny = |doc: &surface_ports::Json| -> Vec<String> {
        doc.get("permissions")
            .and_then(|p| p.get("deny"))
            .and_then(surface_ports::Json::as_array)
            .into_iter()
            .flatten()
            .filter_map(|v| v.as_str().map(String::from))
            .collect()
    };
    let with = prepare_carrying(
        false,
        SessionHome::Session,
        Some(r#"{"defaultMode":"plan","deny":["WebFetch"]}"#),
        carried(),
    );
    assert_eq!(deny(&doc(&with)), ["WebFetch", "Bash"]);
    assert_eq!(
        doc(&with)
            .get("permissions")
            .and_then(|p| p.get("defaultMode"))
            .and_then(surface_ports::Json::as_str),
        Some("plan")
    );
    let alone = prepare_carrying(false, SessionHome::Session, None, carried());
    assert_eq!(deny(&doc(&alone)), ["Bash", "WebFetch"]);
    // Under `host` the user's settings are never written: the deny list is
    // the hooks file's permissions, beside the document.
    let host = prepare_carrying(false, SessionHome::Host, None, carried());
    assert!(file_of(&host, "settings.json").is_none());
    assert_eq!(deny(&hooks_doc(&host)), ["Bash", "WebFetch"]);
}

// The servers ride `ctxloom-mcp.json`, written at every launch (`{}` of
// servers with none), which the command names with `--mcp-config`.
#[test]
fn the_servers_ride_an_mcp_config_the_command_names() {
    let prepared = prepare_carrying(false, SessionHome::Session, None, carried());
    let mcp = surface_ports::Json::parse(&file_of(&prepared, MCP_CONFIG_FILE).unwrap()).unwrap();
    assert_eq!(
        mcp.get("mcpServers")
            .and_then(|s| s.get("tool"))
            .and_then(|t| t.get("command"))
            .and_then(surface_ports::Json::as_str),
        Some("tool-cmd")
    );
    let none = prepare_carrying(
        false,
        SessionHome::Session,
        None,
        surface_ports::Inputs::default(),
    );
    assert_eq!(
        file_of(&none, MCP_CONFIG_FILE).unwrap().trim(),
        "{\n  \"mcpServers\": {}\n}"
    );
    let mut asked = request();
    asked.prompt = "the opening".to_string();
    for command in [
        Claude::new().drive_turn(&asked).unwrap(),
        Claude::new().drive_interactive(&asked).unwrap(),
    ] {
        let at = command
            .args
            .iter()
            .position(|a| a == "--mcp-config")
            .expect("--mcp-config");
        assert_eq!(
            command.args[at + 1],
            format!("{}/{MCP_CONFIG_FILE}", asked.home)
        );
        // It takes many files: an option follows it, never the prompt.
        assert!(command.args[at + 2].starts_with("--"), "{:?}", command.args);
    }
}

// The commands and skills are the session home's (the session's
// CLAUDE_CONFIG_DIR): claude's user-scope `commands/` and `skills/`.
#[test]
fn the_commands_and_skills_ride_the_session_home() {
    let prepared = prepare_carrying(true, SessionHome::Session, None, carried());
    assert_eq!(
        file_of(&prepared, "commands/demo-example.md").unwrap(),
        "---\ndescription: Example\n---\n\nEXAMPLE-BODY"
    );
    assert!(file_of(&prepared, "skills/reviewer/SKILL.md").is_some());
}
