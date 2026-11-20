use std::collections::BTreeMap;

use angzarr_diagnostic::Diagnostic;

use super::*;
use crate::TurnCommand;

/// The terminal session's command, `prompt` its last argument, as an engine
/// builds it.
fn session(program: &str, prompt: &str) -> TurnCommand {
    let mut command = TurnCommand::new(program);
    command.args = vec![
        "--append-system-prompt-file".to_string(),
        "/p/.ctxloom/sessions/h/home/ctxloom-context.md".to_string(),
        prompt.to_string(),
    ];
    command.env = BTreeMap::from([("PATH".to_string(), "/bin".to_string())]);
    command
}

/// The length of a command on Windows when its prompt is `n` letters long.
fn windows_length_with(n: usize) -> usize {
    windows_command_line(&session("claude", &"a".repeat(n)))
}

// Linux: one argument is at most MAX_ARG_STRLEN (32 pages, 131072 bytes) with
// its terminating NUL: 131071 bytes fit, 131072 do not.
#[test]
fn on_linux_one_argument_fits_up_to_max_arg_strlen() {
    assert_eq!(MAX_ARG_STRLEN, 131_072);
    let under = session("claude", &"a".repeat(MAX_ARG_STRLEN - 1));
    assert_eq!(check_command_line("linux", &under), Ok(()));
    let over = session("claude", &"a".repeat(MAX_ARG_STRLEN));
    assert_eq!(
        check_command_line("linux", &over),
        Err(CommandTooLong {
            os: "linux".to_string(),
            measure: Measure::Argument,
            length: MAX_ARG_STRLEN + 1,
            limit: MAX_ARG_STRLEN,
        })
    );
}

// Bytes, not characters: a prompt of multi-byte letters is measured in the
// bytes the kernel copies.
#[test]
fn on_linux_an_argument_is_measured_in_bytes() {
    let letters = MAX_ARG_STRLEN / 2;
    let over = session("claude", &"\u{e9}".repeat(letters));
    assert!(check_command_line("linux", &over).is_err());
}

// An environment string is copied as an argument is: one over the limit
// fails as one.
#[test]
fn on_linux_an_environment_string_is_held_to_the_same_limit() {
    let mut command = session("claude", "hello");
    command
        .env
        .insert("BIG".to_string(), "v".repeat(MAX_ARG_STRLEN));
    assert!(check_command_line("linux", &command).is_err());
}

// Windows: CreateProcess takes a command line of at most 32767 UTF-16 units,
// its NUL included, measured as std quotes it.
#[test]
fn on_windows_the_whole_command_line_fits_up_to_create_process_limit() {
    assert_eq!(WINDOWS_COMMAND_LINE, 32_767);
    let base = windows_length_with(0);
    // The longest prompt that fits, and one letter more.
    let fits = WINDOWS_COMMAND_LINE - 1 - (base - 2);
    let under = session("claude", &"a".repeat(fits));
    assert_eq!(windows_command_line(&under), WINDOWS_COMMAND_LINE - 1);
    assert_eq!(check_command_line("windows", &under), Ok(()));
    let over = session("claude", &"a".repeat(fits + 1));
    assert_eq!(
        check_command_line("windows", &over),
        Err(CommandTooLong {
            os: "windows".to_string(),
            measure: Measure::CommandLine,
            length: WINDOWS_COMMAND_LINE + 1,
            limit: WINDOWS_COMMAND_LINE,
        })
    );
}

// The quoting std's CreateProcess line is built with: the program quoted, an
// argument with a space or tab (or none at all) quoted, a quote escaped, and
// the backslashes before a quote (or a closing one) doubled.
#[test]
fn a_windows_command_line_is_measured_as_std_quotes_it() {
    let line = |args: &[&str]| {
        let mut command = TurnCommand::new("p");
        command.args = args.iter().map(ToString::to_string).collect();
        windows_command_line(&command)
    };
    // `"p"`
    assert_eq!(line(&[]), 3);
    // `"p" ab`
    assert_eq!(line(&["ab"]), 6);
    // `"p" ""`
    assert_eq!(line(&[""]), 6);
    // `"p" "a b"`
    assert_eq!(line(&["a b"]), 9);
    // `"p" a\"b`
    assert_eq!(line(&["a\"b"]), 8);
    // `"p" a\\\"b`
    assert_eq!(line(&["a\\\"b"]), 10);
    // `"p" "a b\\"`
    assert_eq!(line(&["a b\\"]), 11);
    // `"p" a\b` (a backslash before no quote is itself)
    assert_eq!(line(&["a\\b"]), 7);
    // UTF-16 units: one for é, two for an emoji.
    assert_eq!(line(&["\u{e9}"]), 5);
    assert_eq!(line(&["\u{1F600}"]), 6);
}

// A batch file is run through cmd.exe, whose escaping can double a
// character: such a command is measured as if every character were escaped.
#[test]
fn a_windows_batch_command_is_measured_as_if_every_character_were_escaped() {
    let prompt = "a".repeat(20_000);
    assert_eq!(
        check_command_line("windows", &session("claude", &prompt)),
        Ok(())
    );
    for program in ["claude.cmd", "C:/bin/claude.BAT"] {
        assert!(
            check_command_line("windows", &session(program, &prompt)).is_err(),
            "{program}"
        );
    }
}

// macOS's limit is a sysctl (`kern.argmax`) and Linux's total a quarter of the
// stack rlimit: neither is a constant, so neither total is checked (the spawn
// fails as before); macOS has no per-argument limit of its own.
#[test]
fn where_no_constant_limit_is_known_nothing_is_refused() {
    let huge = session("claude", &"a".repeat(4 * 1024 * 1024));
    for os in ["macos", "freebsd", ""] {
        assert_eq!(check_command_line(os, &huge), Ok(()), "{os}");
    }
}

// A child's turn has its prompt on stdin: stdin is not the command line.
#[test]
fn stdin_is_not_the_command_line() {
    let mut turn = TurnCommand::new("claude");
    turn.stdin = vec![b'a'; 4 * MAX_ARG_STRLEN];
    for os in ["linux", "windows", "macos"] {
        assert_eq!(check_command_line(os, &turn), Ok(()), "{os}");
    }
}

#[test]
fn a_command_too_long_is_said_with_its_code_and_args() {
    let error = CommandTooLong {
        os: "windows".to_string(),
        measure: Measure::CommandLine,
        length: 40_000,
        limit: 32_767,
    };
    assert_eq!(error.code(), "run-ports-command-too-long");
    assert_eq!(
        error.to_string(),
        "run-ports-command-too-long os=windows measure=\"command line\" length=40000 limit=32767"
    );
    let argument = CommandTooLong {
        measure: Measure::Argument,
        ..error
    };
    assert!(
        argument.to_string().contains("measure=argument"),
        "{argument}"
    );
}

// An environment string is `NAME=value` with its NUL: `BIG=` and a value of
// MAX_ARG_STRLEN - 5 bytes is the longest that fits, one byte more does not.
#[test]
fn on_linux_an_environment_string_fits_exactly_up_to_the_limit() {
    let with_value = |n: usize| {
        let mut command = session("claude", "hello");
        command.env.insert("BIG".to_string(), "v".repeat(n));
        command
    };
    assert_eq!(
        check_command_line("linux", &with_value(MAX_ARG_STRLEN - 5)),
        Ok(())
    );
    assert!(check_command_line("linux", &with_value(MAX_ARG_STRLEN - 4)).is_err());
}

// A batch command's argument counts as every unit doubled plus its two
// quotes: the program quoted, then each argument after a space.
#[test]
fn a_windows_batch_argument_counts_twice_its_units_and_two_quotes() {
    let mut command = TurnCommand::new("claude.cmd");
    command.args = vec!["abc".to_string()];
    assert_eq!(
        windows_command_line(&command),
        ("claude.cmd".len() + 2) + 1 + (2 * 3 + 2)
    );
}
