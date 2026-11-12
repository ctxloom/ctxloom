//! Whether an engine's command fits the platform's command line (owner
//! ruling, 2026-11-12): measured once the command is complete, before it is
//! spawned, so a terminal session's opening prompt (the one piece of content
//! on an engine's command line: claude's positional prompt, opencode's
//! `--prompt`) too long to start is refused in words, never a spawn's bare
//! `E2BIG`. A child's turn has its prompt on stdin, which is not measured.
//!
//! One table, constants only, keyed by the OS name the caller gives
//! (`std::env::consts::OS`):
//!
//! | os      | limit                                                     |
//! |---------|-----------------------------------------------------------|
//! | windows | 32767 UTF-16 units for the whole command line, its NUL included, as CreateProcess takes it |
//! | linux   | 131072 bytes per argument or environment string, its NUL included (MAX_ARG_STRLEN) |
//! | other   | none                                                      |
//!
//! Only these two are constants. Linux's total for argv and the environment
//! (ARG_MAX) is a quarter of the stack rlimit, and macOS's (`kern.argmax`) a
//! sysctl; the platform crates read neither, so neither total is checked and
//! a command over it fails at the spawn as it did before. Pure: nothing here
//! reads the system.

use std::fmt;

use angzarr_diagnostic::{code, Arg, Diagnostic};

use crate::TurnCommand;

/// Linux's MAX_ARG_STRLEN: one argument or environment string, its NUL
/// included, is at most 32 pages of 4096 bytes.
pub const MAX_ARG_STRLEN: usize = 131_072;

/// CreateProcess's `lpCommandLine`: at most 32767 UTF-16 units, its NUL
/// included.
pub const WINDOWS_COMMAND_LINE: usize = 32_767;

/// What was measured against a limit.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Measure {
    /// One argument (or environment string).
    Argument,
    /// The whole command line.
    CommandLine,
}

impl Measure {
    fn text(self) -> &'static str {
        match self {
            Measure::Argument => "argument",
            Measure::CommandLine => "command line",
        }
    }
}

/// A command too long for the platform to start: on `os`, the `measure`
/// came to `length` where at most `limit` fit (units as the table says).
/// Refused before the spawn.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct CommandTooLong {
    pub os: String,
    pub measure: Measure,
    pub length: usize,
    pub limit: usize,
}

impl fmt::Display for CommandTooLong {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        angzarr_diagnostic::display(self, f)
    }
}

impl std::error::Error for CommandTooLong {}

impl Diagnostic for CommandTooLong {
    fn code(&self) -> &str {
        code!("command-too-long")
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        vec![
            ("os", self.os.as_str().into()),
            ("measure", self.measure.text().into()),
            ("length", self.length.to_string().into()),
            ("limit", self.limit.to_string().into()),
        ]
    }
}

/// Whether `command` fits the command line of `os` ([the table](self)).
pub fn check_command_line(os: &str, command: &TurnCommand) -> Result<(), CommandTooLong> {
    let too_long = |measure, length, limit| CommandTooLong {
        os: os.to_string(),
        measure,
        length,
        limit,
    };
    match os {
        crate::WINDOWS => {
            let length = windows_command_line(command) + 1;
            if length > WINDOWS_COMMAND_LINE {
                return Err(too_long(Measure::CommandLine, length, WINDOWS_COMMAND_LINE));
            }
        }
        "linux" => {
            let env = command.env.iter().map(|(k, v)| k.len() + 1 + v.len());
            let strings = std::iter::once(command.program.len())
                .chain(command.args.iter().map(String::len))
                .chain(env);
            if let Some(longest) = strings.max() {
                if longest + 1 > MAX_ARG_STRLEN {
                    return Err(too_long(Measure::Argument, longest + 1, MAX_ARG_STRLEN));
                }
            }
        }
        _ => {}
    }
    Ok(())
}

/// The UTF-16 length, without its NUL, of the command line std builds for
/// `command` on Windows: the program quoted, then each argument after a
/// space, quoted when it is empty or holds a space or tab, a quote escaped and
/// the backslashes before a quote or the closing quote doubled. A batch file
/// (`.cmd`, `.bat`) is run through cmd.exe, whose escaping can double a
/// character, so its arguments are counted as if every unit were doubled.
pub(crate) fn windows_command_line(command: &TurnCommand) -> usize {
    let units = |text: &str| text.encode_utf16().count();
    let program = command.program.to_ascii_lowercase();
    let batch = program.ends_with(".cmd") || program.ends_with(".bat");
    let mut length = units(&command.program) + 2;
    for arg in &command.args {
        length += 1 + if batch {
            2 * units(arg) + 2
        } else {
            quoted_units(arg)
        };
    }
    length
}

/// The UTF-16 length of `arg` as std quotes it for a command line.
fn quoted_units(arg: &str) -> usize {
    let quote = arg.is_empty() || arg.contains([' ', '\t']);
    let mut length = if quote { 2 } else { 0 };
    let mut backslashes = 0;
    for unit in arg.encode_utf16() {
        if unit == u16::from(b'\\') {
            backslashes += 1;
        } else {
            if unit == u16::from(b'"') {
                // Each backslash before it doubled, and the quote escaped.
                length += backslashes + 1;
            }
            backslashes = 0;
        }
        length += 1;
    }
    if quote {
        // The backslashes before the closing quote, doubled.
        length += backslashes;
    }
    length
}

#[cfg(test)]
#[path = "argv.test.rs"]
mod tests;
