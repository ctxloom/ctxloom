//! The running binary (owner rulings 163, 164): the one place the process is
//! asked where its executable is. Its directory leads an engine's `PATH`, and
//! its file name is the program every callback ctxloom installs runs (the
//! hooks claude is told, opencode's plugin, the statusline), so a binary
//! installed under another name (`ctxloom-rs`) is the one its hooks call. The
//! platform's executable suffix is no part of the name: `ctxloom.exe` is
//! `ctxloom` (owner ruling 2026-11-12).

use std::path::Path;
use std::sync::OnceLock;

/// Where the running binary is.
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct Program {
    /// Its directory; empty when it could not be found.
    pub(crate) dir: String,
    /// Its file name; [`surface_ports::CALLBACK_PROGRAM`] when it could not
    /// be found.
    pub(crate) name: String,
}

impl Program {
    /// The program at `exe` (as `std::env::current_exe` answers it: on Linux
    /// `/proc/self/exe`, a symlink's target and never the link); none known,
    /// the authored name and no directory. `suffix` is the platform's
    /// executable suffix (`std::env::consts::EXE_SUFFIX`).
    pub(crate) fn at(exe: Option<&Path>, suffix: &str) -> Program {
        let dir = exe
            .and_then(Path::parent)
            .map(|dir| dir.to_string_lossy().into_owned())
            .unwrap_or_default();
        let name = exe
            .and_then(Path::file_name)
            .map(|name| without_suffix(&name.to_string_lossy(), suffix).to_string())
            .filter(|name| !name.is_empty())
            .unwrap_or_else(|| surface_ports::CALLBACK_PROGRAM.to_string());
        Program { dir, name }
    }
}

/// `name` without the platform's executable `suffix` (Windows' `.exe`, in
/// any case, as its file names are; none elsewhere), so the name a callback
/// runs is `ctxloom` on every OS (owner ruling 2026-11-12, amending 163 and
/// 164): the PATH search finds the executable by its suffix itself.
fn without_suffix<'n>(name: &'n str, suffix: &str) -> &'n str {
    let cut = name.len().saturating_sub(suffix.len());
    match (name.get(..cut), name.get(cut..)) {
        (Some(stem), Some(end)) if !suffix.is_empty() && end.eq_ignore_ascii_case(suffix) => stem,
        _ => name,
    }
}

/// The running binary, asked once per process.
pub(crate) fn running() -> &'static Program {
    static RUNNING: OnceLock<Program> = OnceLock::new();
    RUNNING.get_or_init(|| {
        Program::at(
            std::env::current_exe().ok().as_deref(),
            std::env::consts::EXE_SUFFIX,
        )
    })
}

#[cfg(test)]
#[path = "program.test.rs"]
mod tests;
