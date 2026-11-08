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
fn a_started_command_that_exits_reports_started_then_exited() {
    assert_eq!(
        over(Some(3), None, false).events(),
        vec![
            LaunchEvent::RunnerStarted,
            LaunchEvent::RunExited {
                code: Some(3),
                signal: None
            }
        ]
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
    assert_eq!(events.len(), 2);
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
