//! The drive half: a child's turn is one `opencode run --format json` with the
//! prompt on stdin (the probe: an argument with a space reaches the model
//! wrapped in literal quotes, and `run` reads stdin to its end); the human's
//! session is the TUI; both get the session home as `OPENCODE_CONFIG_DIR`,
//! every XDG base and HOME relocated into it, and a curated environment.

use std::collections::BTreeMap;

use run_ports::{
    AuthSetting, Engine, EngineError, EngineSettings, SessionHome, TurnCommand, TurnRequest,
};

use crate::{Opencode, ENV_ALLOWLIST, INTERACTIVE_ENV_ALLOWLIST, OPTIONAL_KEY};

const HOME: &str = "/p/.ctxloom/sessions/h/home";

fn request() -> TurnRequest {
    TurnRequest {
        prompt: "Reply with exactly: ok".to_string(),
        home: HOME.to_string(),
        context_path: format!("{HOME}/AGENTS.md"),
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

fn turn(request: &TurnRequest) -> TurnCommand {
    Opencode::new().drive_turn(request).unwrap()
}

fn terminal(request: &TurnRequest) -> TurnCommand {
    Opencode::new().drive_interactive(request).unwrap()
}

#[test]
fn test_a_turn_is_one_run_command_that_prints_json_with_the_prompt_on_stdin() {
    let command = turn(&request());
    assert_eq!(command.program, "opencode");
    assert_eq!(command.args, ["run", "--format", "json"]);
    assert_eq!(command.stdin, b"Reply with exactly: ok");
    assert!(!command.args.iter().any(|a| a.contains("exactly")));
    assert_eq!(command.cwd.as_deref(), Some("/p"));
}

// The first turn makes the session; a later one names it (the id the first
// turn's stream gave, bound to the session by the launcher).
#[test]
fn test_a_later_turn_names_the_native_session() {
    let mut later = request();
    later.resume = Some("ses_e3c648397ffesrvA00TD1hAwdC".to_string());
    assert_eq!(
        turn(&later).args,
        [
            "run",
            "--format",
            "json",
            "--session",
            "ses_e3c648397ffesrvA00TD1hAwdC"
        ]
    );
}

// opencode names a model `provider/model`; an effort level is not passed (no
// opencode flag for it is verified), and is not refused either.
#[test]
fn test_a_model_is_passed_and_an_effort_is_not() {
    let mut chosen = request();
    chosen.model = "opencode/big-pickle".to_string();
    chosen.effort = "high".to_string();
    assert_eq!(
        turn(&chosen).args,
        ["run", "--format", "json", "-m", "opencode/big-pickle"]
    );
    chosen.prompt = String::new();
    assert_eq!(terminal(&chosen).args, ["-m", "opencode/big-pickle"]);
}

#[test]
fn test_the_configured_command_is_run() {
    let mut configured = request();
    configured.settings.command = "/opt/opencode/bin/opencode".to_string();
    assert_eq!(turn(&configured).program, "/opt/opencode/bin/opencode");
    assert_eq!(terminal(&configured).program, "/opt/opencode/bin/opencode");
}

// The TUI is given the terminal: the prompt a flag (it has no stdin of its
// own to read), the session it resumes by name.
#[test]
fn test_the_humans_session_is_the_tui_with_the_prompt_as_a_flag() {
    let mut opening = request();
    opening.resume = Some("ses_1".to_string());
    let command = terminal(&opening);
    assert_eq!(command.program, "opencode");
    assert_eq!(
        command.args,
        ["--session", "ses_1", "--prompt", "Reply with exactly: ok"]
    );
    assert!(command.stdin.is_empty());
    assert_eq!(command.cwd.as_deref(), Some("/p"));
    let mut bare = request();
    bare.prompt = "  ".to_string();
    assert!(terminal(&bare).args.is_empty());
}

// The probe: OPENCODE_CONFIG_DIR adds the session home to opencode's config
// directories (the plugin and AGENTS.md are found there); every XDG base and
// HOME are the session's, so the user's own opencode config, data, state,
// cache and ~/.opencode are never read nor written.
#[test]
fn test_the_engine_is_relocated_into_the_session_home() {
    let caller = env(&[
        ("HOME", "/home/alice"),
        ("XDG_CONFIG_HOME", "/home/alice/.config"),
        ("XDG_DATA_HOME", "/home/alice/.local/share"),
        ("PATH", "/usr/bin"),
    ]);
    for command in [
        turn(&TurnRequest {
            env: caller.clone(),
            ..request()
        }),
        terminal(&TurnRequest {
            env: caller.clone(),
            ..request()
        }),
    ] {
        let got = |k: &str| command.env.get(k).cloned().unwrap_or_default();
        assert_eq!(got("OPENCODE_CONFIG_DIR"), HOME);
        assert_eq!(got("HOME"), HOME);
        assert_eq!(got("XDG_CONFIG_HOME"), format!("{HOME}/xdg/config"));
        assert_eq!(got("XDG_DATA_HOME"), format!("{HOME}/xdg/data"));
        assert_eq!(got("XDG_STATE_HOME"), format!("{HOME}/xdg/state"));
        assert_eq!(got("XDG_CACHE_HOME"), format!("{HOME}/xdg/cache"));
        assert_eq!(got("PATH"), "/usr/bin");
        assert!(
            !command.env.values().any(|v| v.starts_with("/home/alice")),
            "{:?}",
            command.env
        );
    }
}

// The caller's variables pass on the curated list, the engine block's `env`
// and the configured source only. OPENCODE_PURE skips external plugins, so
// capture would vanish silently: it never passes, whatever the config names,
// and neither does any other variable of opencode's own.
#[test]
fn test_only_the_curated_variables_pass_and_opencode_pure_never_does() {
    let caller = env(&[
        ("PATH", "/usr/bin"),
        (OPTIONAL_KEY, "sk-test"), // gitleaks:allow
        ("OPENCODE_PURE", "1"),
        ("OPENCODE_CONFIG", "/home/alice/oc.json"),
        ("OPENCODE_CONFIG_CONTENT", "{}"),
        ("SECRET_OF_ALICE", "x"),
        ("NAMED", "y"),
        ("CTXLOOM_ENDPOINT", "https://127.0.0.1:1"),
        ("CTXLOOM_SESSION_HARP", "amber-swift-owl"),
    ]);
    let settings = EngineSettings {
        env: vec!["NAMED".to_string(), "OPENCODE_PURE".to_string()],
        ..EngineSettings::default()
    };
    for command in [
        turn(&TurnRequest {
            env: caller.clone(),
            settings: settings.clone(),
            ..request()
        }),
        terminal(&TurnRequest {
            env: caller.clone(),
            settings: settings.clone(),
            ..request()
        }),
    ] {
        let names: Vec<&str> = command.env.keys().map(String::as_str).collect();
        for passed in [
            "PATH",
            OPTIONAL_KEY,
            "NAMED",
            "CTXLOOM_ENDPOINT",
            "CTXLOOM_SESSION_HARP",
        ] {
            assert!(names.contains(&passed), "{passed}: {names:?}");
        }
        for kept in [
            "OPENCODE_PURE",
            "OPENCODE_CONFIG",
            "OPENCODE_CONFIG_CONTENT",
            "SECRET_OF_ALICE",
        ] {
            assert!(!names.contains(&kept), "{kept}: {names:?}");
        }
    }
    for list in [ENV_ALLOWLIST, INTERACTIVE_ENV_ALLOWLIST] {
        assert!(!list.iter().any(|n| n.starts_with("OPENCODE_")), "{list:?}");
        assert!(list.contains(&OPTIONAL_KEY));
        for hook in run_ports::HOOK_VARIABLES {
            assert!(list.contains(&hook), "{hook}");
        }
        assert!(list.contains(&run_ports::HOME_VARIABLE));
        assert!(list.contains(&run_ports::HARP_VARIABLE));
    }
}

#[test]
fn test_a_configured_credential_source_passes_its_variables() {
    let settings = EngineSettings {
        auth: AuthSetting::Source {
            env: vec!["MY_PROVIDER_KEY".to_string()],
            file: None,
        },
        ..EngineSettings::default()
    };
    let command = turn(&TurnRequest {
        env: env(&[("MY_PROVIDER_KEY", "k")]),
        settings,
        ..request()
    });
    assert_eq!(
        command.env.get("MY_PROVIDER_KEY").map(String::as_str),
        Some("k")
    );
}

// opencode's key is optional (a built-in provider's free models need none): a
// child is never refused for want of it.
#[test]
fn test_no_credential_is_required_of_a_child_or_shared_with_the_primary() {
    let credentials = Opencode::new().credentials();
    assert!(credentials.child_env.is_empty());
    assert!(credentials.primary_env.is_empty());
    assert_eq!(credentials.login_file, None);
}

// `session_home: host`: the user's own opencode config, data and ~/.opencode
// are used in place; the session home is still a config directory, so the
// plugin is loaded. No HOME is refused.
#[test]
fn test_a_host_home_keeps_the_users_own_dirs_and_still_loads_the_plugin() {
    let settings = EngineSettings {
        session_home: SessionHome::Host,
        ..EngineSettings::default()
    };
    let command = turn(&TurnRequest {
        env: env(&[
            ("HOME", "/home/alice"),
            ("XDG_CONFIG_HOME", "/home/alice/.cfg"),
        ]),
        settings: settings.clone(),
        ..request()
    });
    let got = |k: &str| command.env.get(k).cloned();
    assert_eq!(got("OPENCODE_CONFIG_DIR").as_deref(), Some(HOME));
    assert_eq!(got("HOME").as_deref(), Some("/home/alice"));
    assert_eq!(got("XDG_CONFIG_HOME").as_deref(), Some("/home/alice/.cfg"));
    assert_eq!(got("XDG_DATA_HOME"), None);
    assert_eq!(
        Opencode::new().drive_turn(&TurnRequest {
            settings,
            ..request()
        }),
        Err(EngineError::NoHostHome {
            engine: "opencode".to_string()
        })
    );
}
