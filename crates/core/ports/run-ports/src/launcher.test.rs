use super::*;

fn over(code: Option<i32>, signal: Option<i32>, timed_out: bool) -> Launch {
    Launch::Over(TurnOutput {
        exit: Exit { code, signal },
        stdout: b"out".to_vec(),
        stderr: Vec::new(),
        timed_out,
        truncated: false,
        drain_incomplete: false,
    })
}

#[test]
fn a_command_starts_with_no_environment_and_no_input() {
    let command = TurnCommand::new("claude");
    assert_eq!(command.program, "claude");
    assert!(command.args.is_empty());
    assert!(command.env.is_empty());
    assert!(command.stdin.is_empty());
    assert_eq!(command.cwd, None);
}

#[test]
fn limits_default_to_a_deadline_and_a_bound() {
    let limits = Limits::new(Duration::from_secs(9));
    assert_eq!(limits.deadline, Duration::from_secs(9));
    assert_eq!(limits.drain_grace, Duration::from_secs(2));
    assert_eq!(limits.max_bytes, 64 * 1024 * 1024);
}

#[test]
fn a_started_command_that_exits_reports_its_exit() {
    assert_eq!(
        over(Some(3), None, false).events(),
        vec![LaunchEvent::RunExited {
            code: Some(3),
            signal: None
        }]
    );
}

#[test]
fn a_command_killed_at_its_deadline_exits_by_the_kill() {
    let events = over(None, None, true).events();
    assert_eq!(
        events.last(),
        Some(&LaunchEvent::RunExited {
            code: None,
            signal: None
        })
    );
    assert_eq!(events.len(), 1);
}

#[test]
fn a_command_that_never_started_reports_only_the_failure() {
    let launch = Launch::NotStarted(LaunchFailure {
        deterministic: true,
        detail: "no such program".to_string(),
    });
    assert_eq!(
        launch.events(),
        vec![LaunchEvent::LaunchFailed {
            deterministic: true,
            detail: "no such program".to_string()
        }]
    );
}

// A command is logged without the bearer's value: the exact rendering.
#[test]
fn a_command_renders_its_env_keys_and_never_the_bearer() {
    let mut command = TurnCommand::new("claude");
    command.args = vec!["-p".to_string()];
    command.env = BTreeMap::from([
        ("CTXLOOM_BEARER".to_string(), "run.TOPSECRET".to_string()),
        ("PATH".to_string(), "/bin".to_string()),
    ]);
    command.stdin = vec![104, 105];
    command.cwd = Some("/w".to_string());
    assert_eq!(
        format!("{command:?}"),
        r#"TurnCommand { program: "claude", args: ["-p"], env: {"CTXLOOM_BEARER": "<redacted>", "PATH": "<redacted>"}, stdin: [104, 105], cwd: Some("/w") }"#
    );
}

// Credential values never render: a command's Debug names every variable and
// shows no value, whatever the name (no list of sensitive names to keep).
#[test]
fn a_commands_debug_shows_no_credential_value() {
    let mut command = TurnCommand::new("claude");
    command.env = BTreeMap::from([
        ("ANTHROPIC_API_KEY".to_string(), "secret".to_string()),
        ("CLAUDE_CODE_OAUTH_TOKEN".to_string(), "secret".to_string()),
        ("SOME_PROVIDER_KEY".to_string(), "secret".to_string()),
    ]);
    let shown = format!("{command:?}");
    assert!(!shown.contains("secret"), "{shown}");
    for name in [
        "ANTHROPIC_API_KEY",
        "CLAUDE_CODE_OAUTH_TOKEN",
        "SOME_PROVIDER_KEY",
    ] {
        assert!(shown.contains(name), "{name}: {shown}");
    }
}

/// A launcher that searches nothing: the port's default.
struct Searchless;

impl RunnerLauncher for Searchless {
    fn launch(&self, _: &TurnCommand, _: &Limits) -> Result<Box<dyn Running>, LaunchFailure> {
        unreachable!("never launched")
    }

    fn launch_terminal(&self, _: &TurnCommand) -> Result<Box<dyn Running>, LaunchFailure> {
        unreachable!("never launched")
    }
}

#[test]
fn a_launcher_that_searches_nothing_spawns_the_program_as_it_is_named() {
    let command = TurnCommand::new("claude");
    assert_eq!(Searchless.find_program(&command), Ok("claude".to_string()));
}
