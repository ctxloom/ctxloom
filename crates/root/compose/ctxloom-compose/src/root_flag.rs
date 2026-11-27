//! `--root[=<dir>]`, the flag form of `CTXLOOM_ROOT`: the PROJECT root, never
//! the daemon root (`CTXLOOM_DAEMON_ROOT`). The flag is put in the process's
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
pub fn applied(env: &Arc<EnvSnapshot>, given: Option<&str>) -> Result<Arc<EnvSnapshot>, LibError> {
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
    Ok(with_variable(env, VARIABLE, root))
}

/// `env` with `CTXLOOM_DAEMON_ROOT` as `--daemon-root` says it: the DAEMON
/// root (the store, the daemon's directory), never the project's. `given`
/// is the flag's value, resolved against the working directory when
/// relative; `None` leaves `env` as it is, and the flag, given, wins over a
/// variable already set.
pub fn daemon_root_applied(env: &Arc<EnvSnapshot>, given: Option<&str>) -> Arc<EnvSnapshot> {
    let Some(given) = given else {
        return Arc::clone(env);
    };
    let named = PathBuf::from(given);
    let root = match (named.is_absolute(), env.cwd()) {
        (false, Ok(cwd)) => cwd.join(named),
        _ => named,
    };
    with_variable(env, run_ports::DAEMON_ROOT_VARIABLE, root)
}

fn with_variable(env: &Arc<EnvSnapshot>, variable: &str, value: PathBuf) -> Arc<EnvSnapshot> {
    let vars = env
        .vars()
        .filter(|(name, _)| *name != variable)
        .map(|(name, value)| (name.to_os_string(), value.to_os_string()))
        .chain([(OsString::from(variable), value.into_os_string())]);
    Arc::new(EnvSnapshot::from_parts(
        env.flavor(),
        env.cwd(),
        env.temp_dir().to_path_buf(),
        vars,
    ))
}

#[cfg(test)]
#[path = "root_flag.test.rs"]
mod tests;
