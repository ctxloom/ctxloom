//! Where an engine's program is, and whether it can be handed its arguments.
//!
//! On Windows `std::process::Command` searches PATH for a bare name with
//! `.exe` appended and nothing else (std's `resolve_exe`: "If the file name
//! does not contain an extension, .exe is appended"), so an npm shim
//! (`claude.cmd`) a terminal runs through PATHEXT is "program not found" to
//! it. The launch therefore finds the program itself
//! ([`RunnerLauncher::find_program`](crate::RunnerLauncher::find_program)) and
//! spawns the file found. A program found nowhere is [`ProgramNotFound`],
//! naming the names and the directories searched.
//!
//! A batch file (`.cmd`, `.bat`) is run by std through `cmd.exe`, and since
//! Rust 1.77 std refuses (`ErrorKind::InvalidInput`, "batch file arguments
//! are invalid") an argument holding a carriage return or a line feed, which
//! cmd.exe would cut, and any argument holding a NUL (std's
//! `make_bat_command_line` and `append_bat_arg`). Its quoting is otherwise
//! complete: the script path quoted (spaces and all), `cmd.exe /e:ON /v:OFF
//! /d /c`, each `%` guarded, every ASCII symbol outside `#$*+-./:?@\_`
//! quoted, which are the rules Go ctxloom built by hand for a batch engine
//! (`batchcmdline.go`, task usable-epileptic), so none is ported.
//! [`check_batch_arguments`] refuses such a command before the spawn, in
//! words: [`BatchArguments`]. Pure: nothing here reads the system.

use std::fmt;

use angzarr_diagnostic::{code, Arg, Diagnostic};

use crate::TurnCommand;

/// The program of an engine's command was found on none of the directories
/// of its PATH, by any of the file names the platform looks it up by.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ProgramNotFound {
    /// The program as the engine named it (`claude`).
    pub program: String,
    /// The file names looked for in each directory, in order.
    pub names: Vec<String>,
    /// The directories searched, in order (none: the PATH was empty).
    pub dirs: Vec<String>,
}

impl fmt::Display for ProgramNotFound {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        angzarr_diagnostic::display(self, f)
    }
}

impl std::error::Error for ProgramNotFound {}

impl Diagnostic for ProgramNotFound {
    fn code(&self) -> &str {
        if self.dirs.is_empty() {
            code!("program-not-found-no-path")
        } else {
            code!("program-not-found")
        }
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        let mut args = vec![
            ("program", self.program.as_str().into()),
            ("names", self.names.join(", ").into()),
        ];
        if !self.dirs.is_empty() {
            args.push(("dirs", self.dirs.join("; ").into()));
        }
        args
    }
}

/// A batch file's command with an argument std will not hand cmd.exe: one
/// holding a carriage return, a line feed (a terminal session's opening
/// prompt of more than one line) or a NUL. Refused before the spawn.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct BatchArguments {
    /// The batch file the program was found as (`...\claude.cmd`).
    pub program: String,
}

impl fmt::Display for BatchArguments {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        angzarr_diagnostic::display(self, f)
    }
}

impl std::error::Error for BatchArguments {}

impl Diagnostic for BatchArguments {
    fn code(&self) -> &str {
        code!("batch-arguments")
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        vec![("program", self.program.as_str().into())]
    }
}

/// Whether `program` is a batch file, as std judges it on Windows: its name
/// ends in `.bat` or `.cmd`, in any case.
pub fn is_batch_file(program: &str) -> bool {
    let lower = program.to_ascii_lowercase();
    lower.ends_with(".bat") || lower.ends_with(".cmd")
}

/// Whether `command` can be started on `os` as std starts it: on Windows a
/// batch file's arguments may hold no carriage return, line feed or NUL; anything
/// else, anywhere else, is not judged.
pub fn check_batch_arguments(os: &str, command: &TurnCommand) -> Result<(), BatchArguments> {
    if os != crate::WINDOWS || !is_batch_file(&command.program) {
        return Ok(());
    }
    if command
        .args
        .iter()
        .any(|arg| arg.contains(['\r', '\n', '\0']))
    {
        return Err(BatchArguments {
            program: command.program.clone(),
        });
    }
    Ok(())
}

#[cfg(test)]
#[path = "program.test.rs"]
mod tests;
