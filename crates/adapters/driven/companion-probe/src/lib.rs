//! The `CompanionProbe` port as a process (Go's `companions.Verify` and
//! `Prober.ProbeCompanionLoadouts`): a companion's program found on the
//! `PATH` of the site it is asked at, and run there as `<program> loadout
//! --format yaml` with a time limit (Go's is three seconds), its output read,
//! a program that outlives the limit killed with everything it started
//! (`run-within`).
//!
//! The one probe every caller uses (OD-8): it holds no environment and no
//! directory of its own. Each call names its site (D1): the environment the
//! program runs in, exactly, whose `PATH` is searched, and the directory it
//! runs in, the caller's project root.
//!
//! The outcomes are Go's: a program not on the `PATH` is absent; one that ran
//! and exited with a status offers no loadout; one that succeeded and printed
//! nothing is silent; one that was killed, timed out or could not be started
//! failed.

use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::Duration;

use ctxloom_ports::{CompanionLoadout, CompanionProbe, ProbeSite};
use file_system::{Fs, Kind};
use os::Flavor;
use process_env::EnvSnapshot;
use run_within::{run_within, Spec, Within};

/// How long, once the companion is over, its output is waited for: a helper
/// it started that outlives it can hold the pipe open.
const DRAIN_GRACE: Duration = Duration::from_secs(2);

/// Go's bound on a companion's answer.
pub const STANDARD_LIMIT: Duration = Duration::from_secs(3);

/// Probes a companion's program by running it.
pub struct ProcessProbe {
    limit: Duration,
    drain_grace: Duration,
    /// Where a program is looked for: the filesystem seam.
    fs: Arc<dyn Fs>,
    /// How the host spells a `PATH` and a program's file names.
    flavor: Flavor,
}

impl ProcessProbe {
    /// A probe that gives a program `limit` to answer, finding it through
    /// `fs` as a `flavor` host spells it.
    pub fn new(limit: Duration, fs: Arc<dyn Fs>, flavor: Flavor) -> ProcessProbe {
        ProcessProbe {
            limit,
            drain_grace: DRAIN_GRACE,
            fs,
            flavor,
        }
    }

    /// The same, waiting `grace` for a finished program's output (two
    /// seconds by default).
    pub fn drain_grace(mut self, grace: Duration) -> ProcessProbe {
        self.drain_grace = grace;
        self
    }

    /// Go's three seconds.
    pub fn standard(fs: Arc<dyn Fs>, flavor: Flavor) -> ProcessProbe {
        ProcessProbe::new(STANDARD_LIMIT, fs, flavor)
    }

    fn runnable(&self, candidate: &Path) -> Option<String> {
        let meta = self.fs.metadata(candidate).ok()?;
        (meta.kind == Kind::File && meta.executable).then(|| candidate.display().to_string())
    }

    /// The site's environment as a snapshot, for its `PATH` and `PATHEXT`.
    fn snapshot(&self, site: &ProbeSite) -> EnvSnapshot {
        EnvSnapshot::from_parts(
            self.flavor,
            Ok(PathBuf::from(&site.cwd)),
            PathBuf::from(&site.cwd),
            site.env.iter().map(|(k, v)| (k.into(), v.into())),
        )
    }
}

impl CompanionProbe for ProcessProbe {
    fn find(&self, program: &str, site: &ProbeSite) -> Option<String> {
        // A name, never a path (OD-10): one with a separator is no program
        // of a companion's.
        if program.is_empty() || program.contains(['/', '\\']) {
            return None;
        }
        let env = self.snapshot(site);
        let names = self.flavor.candidates(program, env.utf8("PATHEXT"));
        env.search_path()
            .iter()
            .find_map(|dir| names.iter().find_map(|file| self.runnable(&dir.join(file))))
    }

    fn loadout(&self, program: &str, site: &ProbeSite) -> CompanionLoadout {
        let Some(path) = self.find(program, site) else {
            return CompanionLoadout::Absent;
        };
        let mut spec = Spec::new(&path)
            .args(["loadout", "--format", "yaml"])
            .envs(site.env.iter().map(|(k, v)| (k.as_str(), v.as_str())));
        if !site.cwd.is_empty() {
            spec = spec.current_dir(&site.cwd);
        }
        let outcome = match run_within(
            &spec,
            &Within::new(self.limit).drain_grace(self.drain_grace),
        ) {
            Ok(outcome) => outcome,
            Err(e) => return CompanionLoadout::ProbeFailed(e.io().to_string()),
        };
        let Some(status) = outcome.status else {
            return CompanionLoadout::ProbeFailed(format!(
                "did not answer within {}ms",
                self.limit.as_millis()
            ));
        };
        if !status.success() {
            // An exit with a status answered; a signal did not.
            return if status.code().is_some() {
                CompanionLoadout::NoLoadout(status.to_string())
            } else {
                CompanionLoadout::ProbeFailed(status.to_string())
            };
        }
        if outcome.stdout.is_empty() {
            return CompanionLoadout::Silent;
        }
        CompanionLoadout::Loaded(String::from_utf8_lossy(&outcome.stdout).into_owned())
    }
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
