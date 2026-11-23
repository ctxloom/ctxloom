//! `--root[=<dir>]`, the flag form of `CTXLOOM_ROOT`: the PROJECT root, never
//! the daemon root (`CTXLOOM_HOME`). The flag is put in the process's
//! environment snapshot as `CTXLOOM_ROOT` before anything is wired, so the
//! resolution in this process and the daemon's (which reads the caller's
//! whole environment) see one answer.

use std::ffi::OsString;
use std::path::PathBuf;
use std::sync::Arc;

use process_env::EnvSnapshot;

use crate::LibError;

const VARIABLE: &str = "CTXLOOM_ROOT";

/// `env` with `CTXLOOM_ROOT` as `--root` says it: `given` is the flag's
/// value, `Some("")` for a bare `--root` (the user's home directory), a
/// directory resolved against the working directory when relative; `None`
/// (the flag omitted) leaves `env` as it is, and the flag, given, wins over
/// a `CTXLOOM_ROOT` already set.
pub(crate) fn applied(
    env: &Arc<EnvSnapshot>,
    given: Option<&str>,
) -> Result<Arc<EnvSnapshot>, LibError> {
    let Some(given) = given else {
        return Ok(Arc::clone(env));
    };
    let root = if given.is_empty() {
        env.home()
            .map(PathBuf::from)
            .ok_or_else(|| LibError::NoHome {
                variable: env.flavor().home_var().to_string(),
            })?
    } else {
        let named = PathBuf::from(given);
        match (named.is_absolute(), env.cwd()) {
            (false, Ok(cwd)) => cwd.join(named),
            _ => named,
        }
    };
    let vars = env
        .vars()
        .filter(|(name, _)| *name != VARIABLE)
        .map(|(name, value)| (name.to_os_string(), value.to_os_string()))
        .chain([(OsString::from(VARIABLE), root.into_os_string())]);
    Ok(Arc::new(EnvSnapshot::from_parts(
        env.flavor(),
        env.cwd(),
        env.temp_dir().to_path_buf(),
        vars,
    )))
}

#[cfg(test)]
#[path = "root_flag.test.rs"]
mod tests;
