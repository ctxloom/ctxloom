//! The launcher runs real fake programs (`fixture_program::install_program`),
//! never a script it wrote and exec'd itself.

use std::time::{Duration, Instant};

use file_system::{Fs, Rooted};
use run_ports::{Exit, Launch, LaunchEvent, Limits, RunnerLauncher, TurnCommand};

use crate::HostLauncher;

/// A program running `body`, in a directory of its own.
fn program(body: &str) -> (Rooted, TurnCommand) {
    let dir = Rooted::temp().unwrap();
    let path = fixture_program::install_program(&dir, &os::fixture("/turn"), body).unwrap();
    (dir, TurnCommand::new(path.to_str().unwrap()))
}

fn limits() -> Limits {
    Limits::new(Duration::from_secs(30))
}

fn over(launch: Launch) -> run_ports::TurnOutput {
    let Launch::Over(output) = launch else {
        panic!("it should have started: {launch:?}")
    };
    output
}

#[test]
fn a_command_that_succeeds_leaves_its_stdout() {
    let (_dir, command) = program("#!/bin/sh\necho J000100-REPLY\necho warned >&2\n");
    let launch = HostLauncher.launch(&command, &limits());
    assert_eq!(
        launch.events(),
        vec![
            LaunchEvent::RunnerStarted,
            LaunchEvent::RunExited {
                code: Some(0),
                signal: None
            }
        ]
    );
    let output = over(launch);
    assert_eq!(output.stdout, b"J000100-REPLY\n");
    assert_eq!(output.stderr, b"warned\n");
    assert!(!output.timed_out && !output.truncated && !output.drain_incomplete);
}

#[test]
fn a_command_that_exits_nonzero_is_an_exit_not_a_failure_to_launch() {
    let (_dir, command) = program("#!/bin/sh\necho no >&2\nexit 3\n");
    let output = over(HostLauncher.launch(&command, &limits()));
    assert_eq!(
        output.exit,
        Exit {
            code: Some(3),
            signal: None
        }
    );
    assert_eq!(output.stderr, b"no\n");
}

#[test]
fn a_command_ended_by_a_signal_reports_the_signal() {
    let (_dir, command) = program("#!/bin/sh\nkill -9 $$\n");
    let output = over(HostLauncher.launch(&command, &limits()));
    assert_eq!(
        output.exit,
        Exit {
            code: None,
            signal: Some(9)
        }
    );
    assert!(!output.timed_out);
}

#[test]
fn a_command_is_fed_its_stdin_and_runs_where_it_is_told() {
    let (dir, mut command) = program("#!/bin/sh\npwd\ncat\n");
    command.stdin = b"the prompt".to_vec();
    command.cwd = Some(dir.root().to_str().unwrap().to_string());
    let output = over(HostLauncher.launch(&command, &limits()));
    let text = String::from_utf8(output.stdout).unwrap();
    let (cwd, input) = text.split_once('\n').unwrap();
    assert!(
        cwd.ends_with(dir.root().file_name().unwrap().to_str().unwrap()),
        "{cwd}"
    );
    assert_eq!(input, "the prompt");
}

#[test]
fn a_command_that_outlives_its_deadline_is_killed() {
    let (_dir, command) = program("#!/bin/sh\nsleep 20\n");
    let started = Instant::now();
    let output = over(HostLauncher.launch(&command, &Limits::new(Duration::from_millis(200))));
    assert!(output.timed_out);
    assert_eq!(
        output.exit,
        Exit {
            code: None,
            signal: None
        }
    );
    assert!(
        started.elapsed() < Duration::from_secs(10),
        "it waited for the program"
    );
}

#[test]
fn a_helper_the_command_started_is_killed_with_it() {
    let (dir, command) =
        program("#!/bin/sh\nsleep 30 &\necho $! > \"$(dirname \"$0\")/helper\"\nwait\n");
    // A loaded machine may not have started the helper by the first deadline:
    // ask again with a longer one until it has.
    for secs in [1, 5, 25] {
        let output = over(HostLauncher.launch(&command, &Limits::new(Duration::from_secs(secs))));
        assert!(output.timed_out);
        if dir.exists(&os::fixture("/helper")).unwrap() {
            break;
        }
    }
    let pid = dir.read_to_string(&os::fixture("/helper")).unwrap();
    assert!(
        run_within::process_gone(pid.trim()),
        "the helper {pid} outlived the command"
    );
}

#[test]
fn output_beyond_the_bound_is_discarded_and_said_so() {
    let (_dir, command) = program("#!/bin/sh\nyes | head -c 5000000\n");
    let mut bounded = limits();
    bounded.max_bytes = 1000;
    let output = over(HostLauncher.launch(&command, &bounded));
    assert!(output.truncated);
    assert!(output.stdout.len() <= 1000, "{}", output.stdout.len());
    assert_eq!(output.exit.code, Some(0));
}

#[test]
fn a_helper_that_outlives_the_command_does_not_hold_the_launch() {
    let (_dir, command) = program("#!/bin/sh\nsleep 5 &\necho done\n");
    let mut quick = limits();
    quick.drain_grace = Duration::from_millis(50);
    let started = Instant::now();
    let output = over(HostLauncher.launch(&command, &quick));
    assert_eq!(output.stdout, b"done\n");
    assert!(output.drain_incomplete);
    assert!(started.elapsed() < Duration::from_secs(4));
}

#[test]
fn the_command_runs_in_exactly_the_environment_it_is_given() {
    // The shell adds PWD, SHLVL and `_` of its own; nothing else may be there.
    let (_dir, mut command) =
        program("#!/bin/sh\nenv | grep -v -e '^PWD=' -e '^SHLVL=' -e '^_=' -e '^OLDPWD='\n");
    command.env.insert("MARK".to_string(), "seen".to_string());
    let output = over(HostLauncher.launch(&command, &limits()));
    assert_eq!(String::from_utf8_lossy(&output.stdout), "MARK=seen\n");
}

#[test]
fn a_program_that_is_not_there_never_started_and_will_not_next_time() {
    let launch = HostLauncher.launch(&TurnCommand::new("/no/such/program"), &limits());
    let Launch::NotStarted(failure) = &launch else {
        panic!("{launch:?}")
    };
    assert!(failure.deterministic);
    assert!(
        failure.detail.contains("No such file"),
        "{}",
        failure.detail
    );
    assert!(matches!(
        launch.events().as_slice(),
        [LaunchEvent::LaunchFailed {
            deterministic: true,
            ..
        }]
    ));
}

#[test]
fn a_program_the_system_refuses_to_start_may_start_next_time() {
    // A directory is not executable: the refusal is the system's, not the
    // program's absence.
    let dir = Rooted::temp().unwrap();
    let launch = HostLauncher.launch(&TurnCommand::new(dir.root().to_str().unwrap()), &limits());
    let Launch::NotStarted(failure) = launch else {
        panic!()
    };
    assert!(!failure.deterministic);
}

#[test]
fn a_terminal_session_runs_on_exactly_the_commands_environment_and_reports_its_exit() {
    let (_dir, mut command) =
        program("#!/bin/sh\n[ \"$ONLY\" = yes ] && [ -z \"$HOME\" ] && exit 7\nexit 5\n");
    command.env.insert("ONLY".to_string(), "yes".to_string());
    let output = over(HostLauncher.launch_terminal(&command));
    assert_eq!(
        output.exit,
        Exit {
            code: Some(7),
            signal: None
        }
    );
    assert!(output.stdout.is_empty() && output.stderr.is_empty());
}

#[test]
fn a_terminal_session_that_cannot_start_is_not_started() {
    let command = TurnCommand::new("/no/such/program");
    let Launch::NotStarted(failure) = HostLauncher.launch_terminal(&command) else {
        panic!("it cannot have started")
    };
    assert!(failure.deterministic);
}

#[test]
fn a_terminal_session_ended_by_a_signal_reports_the_signal() {
    let (_dir, command) = program("#!/bin/sh\nkill -9 $$\n");
    let output = over(HostLauncher.launch_terminal(&command));
    assert_eq!(
        output.exit,
        Exit {
            code: None,
            signal: Some(9)
        }
    );
}
