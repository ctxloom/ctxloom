//! The launch value: what it starts, and what its `Debug` shows.

use std::collections::BTreeMap;

use crate::{Ignored, Launch, LaunchKind, Stdio, StopPolicy, TurnCommand, STOP_GRACE, STOP_SIGNAL};

fn command() -> TurnCommand {
    TurnCommand {
        program: "/usr/bin/claude".to_string(),
        args: vec!["-p".to_string()],
        env: BTreeMap::from([
            (
                "ANTHROPIC_API_KEY".to_string(),
                "sk-planted-value".to_string(),
            ), // gitleaks:allow
            ("PATH".to_string(), "/usr/bin".to_string()),
        ]),
        stdin: b"the planted prompt".to_vec(),
        cwd: Some("/work".to_string()),
    }
}

#[test]
fn a_launch_starts_exactly_the_command_it_was_made_of() {
    let launch = Launch::new(LaunchKind::OneShot, "claude-code", Stdio::Piped, command());
    assert_eq!(launch.command(), command());
    assert_eq!(launch.engine, "claude-code");
    assert_eq!(launch.stdio, Stdio::Piped);
}

#[test]
fn a_launchs_debug_shows_its_environments_names_and_never_a_value() {
    let mut launch = Launch::new(
        LaunchKind::Terminal,
        "claude-code",
        Stdio::Inherit,
        command(),
    );
    launch.ignored = vec![Ignored::Owned {
        permit: "CTXLOOM_PERMIT_PATH".to_string(),
    }];
    let shown = format!("{launch:?}");
    assert!(shown.contains("ANTHROPIC_API_KEY"), "{shown}");
    assert!(shown.contains("PATH"), "{shown}");
    assert!(!shown.contains("sk-planted-value"), "{shown}");
    assert!(!shown.contains("/usr/bin\""), "a value: {shown}");
    assert!(!shown.contains("planted prompt"), "the input: {shown}");
    assert!(shown.contains("stdin_bytes: 18"), "{shown}");
    assert!(shown.contains("CTXLOOM_PERMIT_PATH"), "{shown}");
}

#[test]
fn a_child_is_stopped_by_its_input_first_and_every_other_launch_by_the_signal() {
    for kind in [
        LaunchKind::Terminal,
        LaunchKind::OneShot,
        LaunchKind::Editor,
    ] {
        let stop = StopPolicy::of(kind);
        assert_eq!(stop.signal, STOP_SIGNAL);
        assert_eq!(stop.grace, STOP_GRACE);
        assert!(!stop.close_stdin_first, "{kind:?}");
    }
    assert!(StopPolicy::of(LaunchKind::Child).close_stdin_first);
    assert_eq!(STOP_SIGNAL, 15);
    assert_eq!(STOP_GRACE, std::time::Duration::from_secs(5));
}
