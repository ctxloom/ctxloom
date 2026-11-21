//! The setup reads, served (step 10c U5 and U6, D1): `doctor`, `auth
//! status`, `manage check` and `init prompt`. Each is answered by the setup
//! use cases run where the caller stands (its directory, its environment,
//! its flags under the project's trust), as a process there would run
//! them: the wiring's [`SetupReads`]. No CLI answers one in process, so a
//! command that can reach no daemon diagnoses nothing.

use daemon_proto::v1;
use run_ports::Edge;

use crate::library_writes::LibraryWriteRequest;
use crate::refusal::ApiError;
use crate::server::Api;

/// A setup read.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum SetupRead {
    /// `doctor`, `--deps` its own machine's checks alone.
    Doctor {
        deps: bool,
    },
    AuthStatus,
    CheckManage,
    InitPrompt,
}

/// A setup read's answer, as the API says it.
#[derive(Clone, Debug, PartialEq)]
pub enum SetupAnswer {
    Doctor(v1::RunDoctorResponse),
    AuthStatus(v1::GetAuthStatusResponse),
    CheckManage(v1::CheckManageResponse),
    InitPrompt(v1::GetInitPromptResponse),
}

/// The setup use cases that read, each run where a process at `dir`, in
/// `env`, under `flags`, would run it: the wiring's. A use case's own
/// refusal or failure is the call's ([`crate::setup_error`]).
pub trait SetupReads: Send + Sync {
    fn read(
        &self,
        dir: &str,
        env: &[(String, String)],
        flags: &library_application::Flags,
        ask: SetupRead,
    ) -> Result<SetupAnswer, ApiError>;
}

impl<E: Edge> Api<E> {
    /// The setup read `ask` of `request` answered where its caller stands.
    pub(crate) async fn setup_read<Q: LibraryWriteRequest>(
        &self,
        request: &Q,
        ask: SetupRead,
    ) -> Result<SetupAnswer, ApiError> {
        self.open()?;
        let reads = self.setup_reads.as_ref().ok_or_else(|| ApiError::Failed {
            detail: "this daemon answers no setup read".to_string(),
        })?;
        let (site, project) = self
            .resolve(
                request.target(),
                request.caller_cwd(),
                request.caller_env(),
                request.caller_globals(),
            )
            .await?;
        // Its trees observed before the root reads them (step 10 U11).
        self.observed(
            project
                .as_ref()
                .map(|(name, root)| (name.clone(), root.as_str())),
        )
        .await?;
        reads.read(&site.at, &site.env, &site.flags, ask)
    }
}

/// `manage gitignore install`'s answer (step 10c U5) from its write: the
/// `.ctxloom` the use case wrote in, else (a retry of a write applied
/// before) the one the file written is in.
pub(crate) fn gitignore_installed(
    wrote: &library_api::served_writes::Wrote<library_application::GitignoreInstalled>,
) -> v1::InstallGitignoreResponse {
    let (app_dir, written) = match &wrote.answer {
        Some(installed) => (installed.app_dir.clone(), installed.written),
        None => (
            wrote
                .at
                .strip_suffix("/.gitignore")
                .unwrap_or(&wrote.at)
                .to_string(),
            wrote.applied,
        ),
    };
    let root = app_dir
        .strip_suffix("/.ctxloom")
        .or_else(|| app_dir.strip_suffix("\\.ctxloom"))
        .unwrap_or(&app_dir);
    v1::InstallGitignoreResponse {
        status: if written { "updated" } else { "unchanged" }.to_string(),
        path: format!("{root}/.gitignore"),
        nested_path: format!("{app_dir}/.gitignore"),
        nested_written: written,
        etag: wrote.etag.clone(),
        notices: wrote.notices.clone(),
    }
}

#[cfg(test)]
#[path = "setup_reads.test.rs"]
mod tests;
