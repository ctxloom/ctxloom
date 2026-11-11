//! The launcher runs real fake programs (`fixture_program::install_program`),
//! never a script it wrote and exec'd itself.

use std::time::{Duration, Instant};

use file_system::{Fs, Rooted};
use run_ports::{Exit, Launch, LaunchEvent, Limits, RunnerLauncher, TurnCommand};

use crate::{HostLauncher, Stop};

/// A turn run to its end, as one answer.
fn launch(command: &TurnCommand, limits: &Limits) -> Launch {
    answer(HostLauncher::default().launch(command, limits)).1
}

/// A terminal session run to its end, as one answer.
fn terminal(command: &TurnCommand) -> Launch {
    answer(HostLauncher::default().launch_terminal(command)).1
}

/// The pid the spawn gave (none when nothing spawned), and the end.
fn answer(
    started: Result<Box<dyn run_ports::Running>, run_ports::LaunchFailure>,
) -> (Option<u32>, Launch) {
    match started {
        Err(failure) => (None, Launch::NotStarted(failure)),
        Ok(running) => {
            let pid = running.pid();
            match running.finish() {
                Ok(output) => (Some(pid), Launch::Over(output)),
                Err(failure) => (Some(pid), Launch::NotStarted(failure)),
            }
        }
    }
}

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
    let launch = launch(&command, &limits());
    assert_eq!(
        launch.events(),
        vec![LaunchEvent::RunExited {
            code: Some(0),
            signal: None
        }]
    );
    let output = over(launch);
    assert_eq!(output.stdout, b"J000100-REPLY\n");
    assert_eq!(output.stderr, b"warned\n");
    assert!(!output.timed_out && !output.truncated && !output.drain_incomplete);
}

#[test]
fn a_command_that_exits_nonzero_is_an_exit_not_a_failure_to_launch() {
    let (_dir, command) = program("#!/bin/sh\necho no >&2\nexit 3\n");
    let output = over(launch(&command, &limits()));
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
    let output = over(launch(&command, &limits()));
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
    let output = over(launch(&command, &limits()));
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
    let output = over(launch(&command, &Limits::new(Duration::from_millis(200))));
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
        let output = over(launch(&command, &Limits::new(Duration::from_secs(secs))));
        assert!(output.timed_out);
        if dir.exists(&os::fixture("/helper")).unwrap() {
            break;
        }
    }
    let pid = dir.read_to_string(&os::fixture("/helper")).unwrap();
    assert!(
        gone_soon(pid.trim()),
        "the helper {pid} outlived the command"
    );
}

/// Whether the killed process `pid` is gone within ten seconds. A run past
/// its limit sends SIGKILL to the program's whole process group and reaps the
/// program, not its descendants: a helper dies when the kernel next runs it,
/// which can be just after the run returned (it was caught in state `R`), so
/// one look at that instant can see a helper that is already doomed. The
/// bounded look is forced in taskloom-cli-client's
/// `a_helper_seen_alive_before_its_kill_lands_is_gone_within_the_bound`.
fn gone_soon(pid: &str) -> bool {
    let until = std::time::Instant::now() + std::time::Duration::from_secs(10);
    while !run_within::process_gone(pid) {
        if std::time::Instant::now() >= until {
            return false;
        }
        std::thread::sleep(std::time::Duration::from_millis(1));
    }
    true
}

#[test]
fn output_beyond_the_bound_is_discarded_and_said_so() {
    let (_dir, command) = program("#!/bin/sh\nyes | head -c 5000000\n");
    let mut bounded = limits();
    bounded.max_bytes = 1000;
    let output = over(launch(&command, &bounded));
    assert!(output.truncated);
    assert!(output.stdout.len() <= 1000, "{}", output.stdout.len());
    assert_eq!(output.exit.code, Some(0));
}

/// A helper that holds the command's pipes after it exits, until the file
/// `release` appears beside the script (or 30 seconds, its own bound), its
/// pid in `helper`. A program that exits normally is not killed with what it
/// started (run-within: the descendant may be meant to stay), so the test
/// lets it go itself.
const HELD: &str = "d=$(dirname \"$0\")\n\
    ( i=0; while [ ! -e \"$d/release\" ] && [ \"$i\" -lt 3000 ]; do sleep 0.01; i=$((i+1)); done ) &\n\
    echo $! > \"$d/helper\"\n";

/// Lets a [`HELD`] helper go when dropped, whether the test passed or not.
struct Release<'a>(&'a Rooted);

impl Drop for Release<'_> {
    fn drop(&mut self) {
        let _ = self.0.write_atomic(
            &os::fixture("/release"),
            b"",
            file_system::Perms::Exact(os::Mode::FILE),
        );
    }
}

#[test]
fn a_helper_that_outlives_the_command_does_not_hold_the_launch() {
    let (dir, command) = program(&format!("#!/bin/sh\n{HELD}echo done\n"));
    let release = Release(&dir);
    let mut quick = limits();
    quick.drain_grace = Duration::from_millis(50);
    let started = Instant::now();
    let output = over(launch(&command, &quick));
    assert_eq!(output.stdout, b"done\n");
    assert!(output.drain_incomplete);
    assert!(started.elapsed() < Duration::from_secs(4));
    let pid = dir.read_to_string(&os::fixture("/helper")).unwrap();
    assert!(
        !run_within::process_gone(pid.trim()),
        "the helper held the pipes"
    );
    drop(release);
    assert!(gone_soon(pid.trim()), "the helper {pid} outlived the test");
}

#[test]
fn the_command_runs_in_exactly_the_environment_it_is_given() {
    // The shell adds PWD, SHLVL and `_` of its own; nothing else may be there.
    let (_dir, mut command) =
        program("#!/bin/sh\nenv | grep -v -e '^PWD=' -e '^SHLVL=' -e '^_=' -e '^OLDPWD='\n");
    command.env.insert("MARK".to_string(), "seen".to_string());
    let output = over(launch(&command, &limits()));
    assert_eq!(String::from_utf8_lossy(&output.stdout), "MARK=seen\n");
}

#[test]
fn a_program_that_is_not_there_never_started_and_will_not_next_time() {
    let launch = launch(&TurnCommand::new("/no/such/program"), &limits());
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
    let launch = launch(&TurnCommand::new(dir.root().to_str().unwrap()), &limits());
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
    let output = over(terminal(&command));
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
    let Launch::NotStarted(failure) = terminal(&command) else {
        panic!("it cannot have started")
    };
    assert!(failure.deterministic);
}

#[test]
fn a_terminal_session_ended_by_a_signal_reports_the_signal() {
    let (_dir, command) = program("#!/bin/sh\nkill -9 $$\n");
    let output = over(terminal(&command));
    assert_eq!(
        output.exit,
        Exit {
            code: None,
            signal: Some(9)
        }
    );
}

/// A file the programs of a test wrote in its directory.
fn text(dir: &Rooted, name: &str) -> String {
    dir.read_to_string(&os::fixture(&format!("/{name}")))
        .unwrap()
}

// --- the pid is the spawn's, heard while the command still runs

#[test]
fn the_pid_is_the_commands_own_and_is_heard_before_the_command_is_over() {
    let dir = Rooted::temp().unwrap();
    let note = dir.real_path(&os::fixture("/")).unwrap().join("pid");
    // The command writes its pid, then waits for a file the test makes only
    // after it has the pid in hand: it cannot be over before launch returns.
    let release = note.with_file_name("release");
    let body = format!(
        "#!/bin/sh\necho $$ > {}\nwhile [ ! -e {} ]; do sleep 0.01; done\n",
        note.display(),
        release.display()
    );
    let path = fixture_program::install_program(&dir, &os::fixture("/turn"), &body).unwrap();
    let command = TurnCommand::new(path.to_str().unwrap());
    let running = HostLauncher::default().launch(&command, &limits()).unwrap();
    let pid = running.pid();
    dir.write_atomic(
        &os::fixture("/release"),
        b"go",
        file_system::Perms::Exact(os::Mode::FILE),
    )
    .unwrap();
    assert!(running.finish().is_ok());
    let written = text(&dir, "pid");
    assert_eq!(written.trim(), pid.to_string());
}

#[test]
fn a_program_that_is_not_there_gives_no_pid() {
    let (pid, launch) =
        answer(HostLauncher::default().launch(&TurnCommand::new("/no/such/program"), &limits()));
    assert_eq!(pid, None);
    assert!(matches!(launch, Launch::NotStarted(_)));
    let (pid, launch) =
        answer(HostLauncher::default().launch_terminal(&TurnCommand::new("/no/such/program")));
    assert_eq!(pid, None);
    assert!(matches!(launch, Launch::NotStarted(_)));
}

// --- the terminal session

#[test]
fn a_terminal_sessions_pid_is_the_commands_own() {
    let dir = Rooted::temp().unwrap();
    let note = dir.real_path(&os::fixture("/")).unwrap().join("pid");
    let body = format!("#!/bin/sh\necho $$ > {}\n", note.display());
    let path = fixture_program::install_program(&dir, &os::fixture("/turn"), &body).unwrap();
    let command = TurnCommand::new(path.to_str().unwrap());
    let (pid, launch) = answer(HostLauncher::default().launch_terminal(&command));
    over(launch);
    let written = text(&dir, "pid");
    assert_eq!(Some(written.trim().to_string()), pid.map(|p| p.to_string()));
}

#[test]
fn a_terminal_session_stays_in_the_callers_process_group_and_a_turn_does_not() {
    // Each program in a directory of its own (kept alive by the caller).
    let groups = |dir: &Rooted| {
        let note = dir.real_path(&os::fixture("/")).unwrap().join("groups");
        let body = format!(
            "#!/bin/sh\necho $(ps -o pgid= -p $$) $(ps -o pgid= -p $PPID) > {}\n",
            note.display()
        );
        let path = fixture_program::install_program(dir, &os::fixture("/prog"), &body).unwrap();
        let mut command = TurnCommand::new(path.to_str().unwrap());
        command
            .env
            .insert("PATH".to_string(), "/usr/bin:/bin".to_string());
        (command, note)
    };
    let read = |dir: &Rooted| {
        let held = text(dir, "groups");
        held.split_whitespace()
            .map(String::from)
            .collect::<Vec<_>>()
    };
    let (own, loose) = (Rooted::temp().unwrap(), Rooted::temp().unwrap());
    let (command, _) = groups(&own);
    over(terminal(&command));
    let held = read(&own);
    assert_eq!(held.len(), 2, "{held:?}");
    assert_eq!(held[0], held[1], "the child is in the caller's group");
    // The contrast that gives the test its meaning.
    let (command, _) = groups(&loose);
    over(launch(&command, &limits()));
    let held = read(&loose);
    assert_ne!(held[0], held[1], "a turn is in a group of its own");
}

// A stop requested from outside ends a terminal session (the direct child);
// forced, not waited for: the stop is requested once the pid is heard, which
// is when the child is known to run.
#[test]
fn a_requested_stop_kills_the_terminal_sessions_direct_child() {
    let (_dir, mut command) = program("#!/bin/sh\nexec sleep 30\n");
    command
        .env
        .insert("PATH".to_string(), "/usr/bin:/bin".to_string());
    let stop = Stop::new();
    let launcher = HostLauncher::stopped_by(stop.clone());
    let running = launcher.launch_terminal(&command).unwrap();
    let began = Instant::now();
    stop.request();
    let output = running.finish().unwrap();
    assert_eq!(
        output.exit,
        Exit {
            code: None,
            signal: Some(9)
        }
    );
    assert!(began.elapsed() < Duration::from_secs(20));
}

// A stop requested from outside ends a turn too (ruling 114): its tree is
// asked to stop (SIGTERM), not killed outright, and the turn is over long
// before its deadline. Forced, not waited for: the stop is requested once the
// spawn is heard; a SIGTERM before the script's `exec` ends the shell the same
// way.
#[test]
fn a_requested_stop_asks_a_turns_tree_to_stop() {
    let (_dir, mut command) = program("#!/bin/sh\nexec sleep 30\n");
    command
        .env
        .insert("PATH".to_string(), "/usr/bin:/bin".to_string());
    let stop = Stop::new();
    let launcher = HostLauncher::stopped_by(stop.clone());
    let running = launcher.launch(&command, &limits()).unwrap();
    let began = Instant::now();
    stop.request();
    let output = running.finish().unwrap();
    assert_eq!(
        output.exit,
        Exit {
            code: None,
            signal: Some(15)
        }
    );
    assert!(!output.timed_out);
    assert!(began.elapsed() < Duration::from_secs(20));
}

// --- inherited stdio, proved in a process whose stdio the test owns (a test
// process has no terminal to give)

/// The driver: ignored, so only the parent test runs it, by re-running this
/// binary with `--ignored`; it runs a terminal session whose stdin, stdout and
/// stderr are the pipes the parent gave.
#[test]
#[ignore = "run by the parent test, which owns its stdio"]
fn stdio_driver() {
    let (_dir, command) = program("#!/bin/sh\necho out\necho err >&2\nread line\necho got:$line\n");
    let output = over(terminal(&command));
    assert_eq!(output.exit.code, Some(0));
}

#[test]
fn a_terminal_session_reads_the_callers_stdin_and_writes_the_callers_stdout_and_stderr() {
    use std::io::Write;
    use std::process::Stdio;
    let exe = std::env::current_exe().unwrap();
    let mut driver = std::process::Command::new(exe)
        .args(["--ignored", "--exact", "tests::stdio_driver", "--nocapture"])
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .unwrap();
    driver.stdin.take().unwrap().write_all(b"hello\n").unwrap();
    let out = driver.wait_with_output().unwrap();
    let stdout = String::from_utf8_lossy(&out.stdout);
    let stderr = String::from_utf8_lossy(&out.stderr);
    assert!(out.status.success(), "{stdout}{stderr}");
    assert!(stdout.contains("out\n"), "{stdout}");
    assert!(stdout.contains("got:hello\n"), "{stdout}");
    assert!(stderr.contains("err\n"), "{stderr}");
}
