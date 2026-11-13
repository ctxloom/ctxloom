//! Where an engine's program is: the one search a child's turn and the
//! terminal session are both found by, before either is spawned.
//!
//! std's spawn searches PATH for a bare name with `.exe` appended on Windows
//! and nothing else (its `resolve_exe`), so claude installed by npm, the
//! `claude.cmd` shim a terminal runs through PATHEXT, was "program not found"
//! to it. Here the program is looked for on the command's OWN `PATH` (the
//! engine's, which the run leads with the running binary's directory), by
//! the file names the host looks a command up by ([`os::Flavor::candidates`]:
//! the name itself on unix; on Windows PATHEXT's, the command's own or
//! cmd.exe's default), and the file found is what is spawned.
//!
//! On Windows an executable is preferred to a batch file wherever each is on
//! the PATH: a batch file runs through cmd.exe, which std will not hand an
//! argument of more than one line (`run_ports::check_batch_arguments`), so
//! claude's native `claude.exe` is taken over an npm `claude.cmd` earlier on
//! the PATH. Among executables, and among batch files, PATH's order and then
//! PATHEXT's decide, as the shell's search does.

use std::path::Path;

use file_system::{Fs, Kind};
use os::Flavor;
use run_ports::{is_batch_file, ProgramNotFound, TurnCommand};

/// The file `command`'s program is spawned as, on `flavor`'s rules, looked
/// up through `fs`; or what was looked for and where, when it is nowhere.
/// A program named with a directory is that file (with PATHEXT's extensions
/// tried on Windows when it has none of them), never searched for; one that
/// is not there is spawned as named, for the spawn to say so.
pub(crate) fn find(
    fs: &dyn Fs,
    flavor: Flavor,
    command: &TurnCommand,
) -> Result<String, ProgramNotFound> {
    let program = &command.program;
    let var = |name: &str| {
        command
            .env
            .iter()
            .find(|(key, _)| flavor.env_key(key) == name)
            .map(|(_, value)| value.as_str())
    };
    let names = flavor.candidates(program, var("PATHEXT"));
    let runnable = |path: &Path| {
        fs.metadata(path)
            .is_ok_and(|meta| meta.kind == Kind::File && flavor.is_executable(meta.mode))
    };
    let named_with_a_directory = match flavor {
        Flavor::Unix => program.contains('/'),
        Flavor::Windows => program.contains(['/', '\\']),
    };
    if named_with_a_directory {
        return Ok(names
            .iter()
            .find(|name| runnable(Path::new(name)))
            .cloned()
            .unwrap_or_else(|| program.clone()));
    }
    let dirs = flavor.split_search_path(var("PATH").unwrap_or_default());
    let (executables, batch_files): (Vec<&String>, Vec<&String>) =
        names.iter().partition(|name| !is_batch_file(name));
    for group in [executables, batch_files] {
        for dir in &dirs {
            for name in &group {
                let path = Path::new(dir).join(name.as_str());
                if runnable(&path) {
                    return Ok(path.display().to_string());
                }
            }
        }
    }
    Err(ProgramNotFound {
        program: program.clone(),
        names,
        dirs,
    })
}

#[cfg(test)]
#[path = "find.test.rs"]
mod tests;
