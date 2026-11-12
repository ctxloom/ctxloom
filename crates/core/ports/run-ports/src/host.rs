//! What an engine's environment needs of the host it runs on, as data: the
//! platform is the operating system's name the caller gives
//! (`std::env::consts::OS`), so every rule here is tested for Windows on any
//! host.
//!
//! The variables' NAMES are the caller's already canonical: the composition
//! root keys the run's environment as the platform compares names
//! (`os::Flavor::env_key`: uppercase on Windows, where `Path` and `PATH` are
//! one variable), so a case-sensitive lookup of `PATH` here, an engine's
//! allowlist and a permit find the variable whatever its spelling was.

use std::collections::BTreeMap;

/// The operating system name (`std::env::consts::OS`) of Windows.
pub const WINDOWS: &str = "windows";

/// The variables a Windows program needs to start and run at all, passed on
/// from the caller as they are: the system's directories (without which it
/// finds neither its system libraries nor its sockets), the temporary
/// directory (`TMPDIR`'s counterpart), how a command without an extension is
/// found (`PATHEXT`) and the command interpreter (`COMSPEC`, which node's
/// `child_process` runs a shell command with). The platform crate's own
/// `Flavor::startup_vars` holds the first five, but for its tests only; an
/// engine's allowlist holds every one of these (its tests say so). None is
/// set on unix, so passing them changes nothing there.
pub const WINDOWS_STARTUP_VARIABLES: &[&str] = &[
    "SYSTEMROOT",
    "SYSTEMDRIVE",
    "WINDIR",
    "TEMP",
    "TMP",
    "PATHEXT",
    "COMSPEC",
];

/// The variables that are the user's home on Windows, and where each is
/// under a session home (owner ruling 112: the engine's home is its session
/// home): `USERPROFILE` is the home itself (what node's `os.homedir()`, and
/// so claude and opencode, read on Windows, as `HOME` on unix), and
/// `APPDATA` and `LOCALAPPDATA` sit where Windows puts them under a profile,
/// `AppData/Roaming` and `AppData/Local` (as the XDG bases are relocated
/// under it on unix). Under `session_home: host` they are the user's own,
/// passed on as they are: an engine's allowlist holds them.
pub const WINDOWS_PROFILE_VARIABLES: &[(&str, &str)] = &[
    ("USERPROFILE", ""),
    ("APPDATA", "AppData/Roaming"),
    ("LOCALAPPDATA", "AppData/Local"),
];

/// The Windows profile variables relocated into the session home `home`:
/// those of [`WINDOWS_PROFILE_VARIABLES`] the caller has (every one on
/// Windows, none on unix, where nothing is added).
pub fn session_profile(home: &str, caller: &BTreeMap<String, String>) -> Vec<(String, String)> {
    WINDOWS_PROFILE_VARIABLES
        .iter()
        .filter(|(name, _)| caller.contains_key(*name))
        .map(|(name, dir)| {
            let value = match *dir {
                "" => home.to_string(),
                dir => format!("{home}/{dir}"),
            };
            ((*name).to_string(), value)
        })
        .collect()
}

/// The separator of `PATH`'s directories on `os`: `;` on Windows, `:`
/// elsewhere (`os::Flavor::split_search_path` splits at the same).
pub fn search_path_separator(os: &str) -> char {
    if os == WINDOWS {
        ';'
    } else {
        ':'
    }
}

/// `PATH` with `first` leading the `inherited` one (none or empty: `first`
/// alone), joined as `os` joins it. On Windows a directory holding the
/// separator is quoted, as the platform's split reads a quoted entry whole.
pub fn search_path_led_by(os: &str, first: &str, inherited: Option<&str>) -> String {
    let separator = search_path_separator(os);
    let first = if os == WINDOWS && first.contains(separator) {
        format!("\"{first}\"")
    } else {
        first.to_string()
    };
    match inherited.filter(|path| !path.is_empty()) {
        Some(inherited) => format!("{first}{separator}{inherited}"),
        None => first,
    }
}

#[cfg(test)]
#[path = "host.test.rs"]
mod tests;
