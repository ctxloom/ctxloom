//! Running git, for every adapter that runs it (step 10, G9: one runner, not
//! a copy per adapter): `remote-git` and `worktree-git`.
//!
//! A git is run in the environment its caller gives, less the variables git
//! itself calls local to a repository (`GIT_DIR` and its siblings, and the
//! numbered config overrides `git -c` exports), so nothing of the caller's
//! can steer which repository it reads; it never prompts; and it is bounded
//! in time (`run-within`): a git that does not finish within its limit is
//! killed with everything it started, and the wait for its output is bounded
//! too, so a hook or a helper that outlives it cannot hang the caller.
//!
//! What a failure means is the caller's: this crate says only what happened
//! (it could not be started, it ran out its time, or it exited unsuccessfully
//! with these words), and each adapter says it in its own terms.

use std::ffi::OsString;
use std::path::{Path, PathBuf};
use std::process::ExitStatus;
use std::sync::{Arc, OnceLock};
use std::time::Duration;

use process_env::EnvSnapshot;
use run_within::{run_within, RunError, Spec, Within};

/// How long, once a git is over, its output is waited for by default: a hook
/// or a helper it started that outlives it can hold the pipes open.
pub const DRAIN_GRACE: Duration = Duration::from_secs(2);

/// How long git is given to name its repository-local variables.
const ASK_TIMEOUT: Duration = Duration::from_secs(30);

/// The variables git itself says are local to a repository, asked of git
/// once, from the filesystem root with an empty environment, so nothing of
/// the caller's can steer the question. None when git cannot be asked (then
/// every `GIT_` variable counts: the scrub errs towards stripping).
pub fn local_variables() -> &'static Option<Vec<String>> {
    static LOCAL: OnceLock<Option<Vec<String>>> = OnceLock::new();
    LOCAL.get_or_init(|| {
        let top = os::host().flavor().top();
        let spec = Spec::new("git")
            .args(["rev-parse", "--local-env-vars"])
            .current_dir(top);
        let output = run_within(&spec, &Within::new(ASK_TIMEOUT).drain_grace(DRAIN_GRACE)).ok()?;
        listed(output.status.is_some_and(|s| s.success()), &output.stdout)
    })
}

/// git's answer to `rev-parse --local-env-vars`, `stdout` once it exited
/// (`succeeded`): its names, or none when it failed or named none.
fn listed(succeeded: bool, stdout: &[u8]) -> Option<Vec<String>> {
    let names: Vec<String> = String::from_utf8_lossy(stdout)
        .split_whitespace()
        .map(str::to_string)
        .collect();
    (succeeded && !names.is_empty()).then_some(names)
}

/// Whether `key` changes which repository git operates on, or how it reads
/// it, by `local` (git's own list; none: every `GIT_` variable).
pub fn is_local(key: &str, local: &Option<Vec<String>>) -> bool {
    let Some(names) = local else {
        return key.starts_with("GIT_");
    };
    if names.iter().any(|n| n == key) {
        return true;
    }
    // The numbered pairs a counter counts are matched by prefix: no fixed
    // list can enumerate them, and git refuses a count whose pairs are gone.
    names.iter().any(|n| n == "GIT_CONFIG_COUNT")
        && (key.starts_with("GIT_CONFIG_KEY_") || key.starts_with("GIT_CONFIG_VALUE_"))
}

/// The environment git is run in: `inherited`, less the repository-local
/// variables, and never interactive (no terminal prompt, no askpass helper).
pub fn environment(inherited: &EnvSnapshot) -> Vec<(OsString, OsString)> {
    let local = local_variables();
    let mut env: Vec<(OsString, OsString)> = inherited
        .vars()
        .filter(|(key, _)| !is_local(&key.to_string_lossy(), local))
        .map(|(key, value)| (key.to_os_string(), value.to_os_string()))
        .collect();
    env.extend([
        ("GIT_TERMINAL_PROMPT".into(), "0".into()),
        ("GIT_ASKPASS".into(), "".into()),
        ("SSH_ASKPASS".into(), "".into()),
    ]);
    env
}

/// The first argument that is not a flag: what a git invocation is named by
/// (`status` of `--no-pager status`), or empty when there is none.
pub fn subcommand<'a>(args: &[&'a str]) -> &'a str {
    args.iter()
        .find(|a| !a.starts_with('-'))
        .copied()
        .unwrap_or("")
}

/// Which git, how long each invocation has, and the environment it is run
/// in. The seam tests narrow to a stub program and a tiny limit.
#[derive(Clone, Debug)]
pub struct Runner {
    program: PathBuf,
    timeout: Duration,
    drain_grace: Duration,
    env: Arc<EnvSnapshot>,
}

/// One invocation: its working directory (the process's where none), its
/// arguments, what it is fed, and variables set after the scrub (a
/// credential), so the caller's own never mix with them.
#[derive(Clone, Copy, Debug, Default)]
pub struct Invocation<'a> {
    pub dir: Option<&'a Path>,
    pub args: &'a [&'a str],
    pub stdin: Option<&'a [u8]>,
    pub extra_env: &'a [(OsString, OsString)],
}

impl<'a> Invocation<'a> {
    /// `git args` in `dir`.
    pub fn new(dir: Option<&'a Path>, args: &'a [&'a str]) -> Invocation<'a> {
        Invocation {
            dir,
            args,
            stdin: None,
            extra_env: &[],
        }
    }
}

/// A git that exited successfully: what it wrote.
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct Ran {
    pub stdout: Vec<u8>,
    pub stderr: Vec<u8>,
}

/// Why an invocation did not succeed.
#[derive(Debug)]
pub enum Failure {
    /// It could not be started (the program is not there, or cannot be run).
    Spawn(RunError),
    /// It did not finish within its limit; it was killed with what it
    /// started, and reaped.
    TimedOut(Duration),
    /// It exited unsuccessfully: its status and what it wrote.
    Exited {
        status: ExitStatus,
        stdout: Vec<u8>,
        stderr: Vec<u8>,
    },
}

impl Failure {
    /// Whether it failed because the program was not found.
    pub fn is_not_found(&self) -> bool {
        matches!(self, Failure::Spawn(RunError::NotFound { .. }))
    }

    /// git's own words on stderr, trimmed (empty for a failure that is not
    /// an exit).
    pub fn said(&self) -> String {
        match self {
            Failure::Exited { stderr, .. } => String::from_utf8_lossy(stderr).trim().to_string(),
            _ => String::new(),
        }
    }
}

impl Runner {
    /// `program` (a path, or a name looked up on `env`'s PATH), each
    /// invocation bounded by `timeout`, run in `env`'s environment.
    pub fn new(program: impl Into<PathBuf>, timeout: Duration, env: Arc<EnvSnapshot>) -> Runner {
        Runner {
            program: program.into(),
            timeout,
            drain_grace: DRAIN_GRACE,
            env,
        }
    }

    /// The same, waiting `grace` for a finished git's output.
    pub fn drain_grace(mut self, grace: Duration) -> Runner {
        self.drain_grace = grace;
        self
    }

    /// Run `invocation`: what it wrote, or why it did not succeed.
    pub fn run(&self, invocation: &Invocation<'_>) -> Result<Ran, Failure> {
        let mut spec = Spec::new(self.program.as_os_str())
            .args(invocation.args)
            .envs(environment(&self.env))
            .envs(
                invocation
                    .extra_env
                    .iter()
                    .map(|(k, v)| (k.as_os_str(), v.as_os_str())),
            );
        if let Some(dir) = invocation.dir {
            spec = spec.current_dir(dir);
        }
        if let Some(bytes) = invocation.stdin {
            spec = spec.stdin(bytes);
        }
        let within = Within::new(self.timeout).drain_grace(self.drain_grace);
        let output = run_within(&spec, &within).map_err(Failure::Spawn)?;
        let Some(status) = output.status else {
            return Err(Failure::TimedOut(self.timeout));
        };
        if status.success() {
            Ok(Ran {
                stdout: output.stdout,
                stderr: output.stderr,
            })
        } else {
            Err(Failure::Exited {
                status,
                stdout: output.stdout,
                stderr: output.stderr,
            })
        }
    }
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;

#[cfg(test)]
#[path = "fixtures.test.rs"]
mod fixtures;
