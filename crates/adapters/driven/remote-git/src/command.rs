//! Running git for the remotes: git-cli's runner (the environment scrubbed,
//! non-interactive, bounded in time with its whole process tree), a
//! credential set after the scrub, and a failure said in the remotes' words,
//! the credential blotted out.

use std::ffi::OsString;
use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::Duration;

use deps_ports::Why;
use git_cli::{Invocation, Runner};
use process_env::EnvSnapshot;
use run_within::RunError;

use crate::auth::Credential;

/// How long one git invocation may run. Go bounds its remote sync at sixty
/// seconds (`adapters/cli/run.go`, `syncCtx`); here each invocation (a clone,
/// a fetch, a push) has that long on its own.
pub const DEFAULT_TIMEOUT: Duration = Duration::from_secs(60);

/// The git the remotes run: which program, and how long each invocation has.
/// The seam tests narrow to a stub program and a tiny limit.
#[derive(Clone, Debug)]
pub struct Git {
    runner: Runner,
}

/// Why a git invocation did not succeed, with git's words.
pub(crate) enum Failed {
    /// It did not finish within its limit.
    TimedOut(Why),
    /// It failed, or could not be run.
    Said(Why),
}

impl Failed {
    pub(crate) fn into_why(self) -> Why {
        match self {
            Failed::TimedOut(why) | Failed::Said(why) => why,
        }
    }
}

impl Git {
    /// `program` (a path, or a name looked up on PATH), each invocation
    /// bounded by `timeout`, run in `env`'s environment.
    pub fn new(program: impl Into<PathBuf>, timeout: Duration, env: Arc<EnvSnapshot>) -> Git {
        Git {
            runner: Runner::new(program, timeout, env),
        }
    }

    /// Run `git args` in `dir` (the process's working directory where none),
    /// feeding `stdin`; its output. A failure is git's own words.
    pub(crate) fn run(
        &self,
        dir: Option<&Path>,
        args: &[&str],
        stdin: Option<&[u8]>,
    ) -> Result<Vec<u8>, Why> {
        self.run_with(dir, args, stdin, &Credential::none())
    }

    /// `run` with `credential` in git's environment, set after the scrub so
    /// the caller's own `GIT_CONFIG_*` never mixes with it; git's words in a
    /// failure have it blotted out.
    pub(crate) fn run_with(
        &self,
        dir: Option<&Path>,
        args: &[&str],
        stdin: Option<&[u8]>,
        credential: &Credential,
    ) -> Result<Vec<u8>, Why> {
        self.attempt(dir, args, stdin, credential)
            .map_err(Failed::into_why)
    }

    /// `run_with`, saying whether a failure was the limit.
    pub(crate) fn attempt(
        &self,
        dir: Option<&Path>,
        args: &[&str],
        stdin: Option<&[u8]>,
        credential: &Credential,
    ) -> Result<Vec<u8>, Failed> {
        let label = args.first().copied().unwrap_or("");
        let pairs: Vec<(OsString, OsString)> = credential
            .pairs()
            .iter()
            .map(|(k, v)| (k.into(), v.into()))
            .collect();
        let invocation = Invocation {
            dir,
            args,
            stdin,
            extra_env: &pairs,
        };
        match self.runner.run(&invocation) {
            Ok(ran) => Ok(ran.stdout),
            Err(git_cli::Failure::Spawn(RunError::NotFound { .. })) => Err(Failed::Said(
                "the git binary was not found on PATH (it is required to reach a remote)"
                    .to_string(),
            )),
            Err(git_cli::Failure::Spawn(other)) => {
                Err(Failed::Said(format!("git {label}: {}", other.io())))
            }
            Err(git_cli::Failure::TimedOut(timeout)) => Err(Failed::TimedOut(format!(
                "git {label}: did not finish within {timeout:?} and was killed"
            ))),
            Err(git_cli::Failure::Exited { status, stderr, .. }) => {
                let said = credential.redact(String::from_utf8_lossy(&stderr).trim());
                Err(Failed::Said(if said.is_empty() {
                    format!("git {label}: {status}")
                } else {
                    format!("git {label}: {said}")
                }))
            }
        }
    }

    /// `run`, its output as trimmed text.
    pub(crate) fn text(&self, dir: &Path, args: &[&str]) -> Result<String, Why> {
        self.run(Some(dir), args, None)
            .map(|out| String::from_utf8_lossy(&out).trim().to_string())
    }
}

#[cfg(test)]
#[path = "command.test.rs"]
pub(crate) mod tests;
