//! The setup reads, answered in the daemon where the caller stands (step
//! 10c U5 and U6, D1): `doctor`, `auth status`, `manage check` and `init
//! prompt`, each its use case run as a process at the caller's directory,
//! in the caller's environment, would run it, with ctxloom's `home`. The
//! project's trust is the daemon's (the read's flags), never asked back.

use std::path::PathBuf;
use std::rc::Rc;

use daemon_api::{ApiError, SetupAnswer, SetupRead};
use daemon_proto::v1;

/// The setup reads of the binary stamped `stamp` (doctor reports it).
pub(crate) struct SetupReaders {
    pub(crate) home: PathBuf,
    pub(crate) temp: PathBuf,
    pub(crate) stamp: String,
}

/// `cause`, a use case's error of the class `exit`, as the call's.
fn refused(cause: &dyn angzarr_diagnostic::Diagnostic, exit: cli_support::Exit) -> ApiError {
    daemon_api::setup_error(cause, exit)
}

impl daemon_api::SetupReads for SetupReaders {
    fn read(
        &self,
        dir: &str,
        env: &[(String, String)],
        flags: &library_application::Flags,
        ask: SetupRead,
    ) -> Result<SetupAnswer, ApiError> {
        use cli_support::ExitClass;
        let at = if dir.is_empty() {
            self.home.display().to_string()
        } else {
            dir.to_string()
        };
        let pairs = env
            .iter()
            .map(|(name, value)| (name.as_str(), value.as_str()));
        let wiring = super::caller_wiring(&self.home, &self.temp, (&at, None), pairs);
        let failed = |e: &dyn angzarr_diagnostic::Diagnostic| ApiError::diagnosed(e);
        if ask == SetupRead::InitPrompt {
            return init_prompt(&wiring, flags);
        }
        let deps = Rc::new(crate::commands::build_deps(&wiring).map_err(|e| failed(&e))?);
        Ok(match ask {
            SetupRead::Doctor { deps: only } => {
                let uses = crate::setup::SetupUseCases::build(&wiring, &self.stamp)
                    .map_err(|e| failed(&e))?;
                SetupAnswer::Doctor(v1::RunDoctorResponse {
                    checks: setup_cli::wire::checks(&uses.doctor.report(only)),
                    ..v1::RunDoctorResponse::default()
                })
            }
            SetupRead::AuthStatus => {
                let uses = crate::setup::SetupUseCases::build(&wiring, &self.stamp)
                    .map_err(|e| failed(&e))?;
                let rows = uses.auth.status(&uses.doctor.layer_texts());
                SetupAnswer::AuthStatus(v1::GetAuthStatusResponse {
                    rows: setup_cli::wire::auth_rows(&rows),
                    ..v1::GetAuthStatusResponse::default()
                })
            }
            SetupRead::CheckManage => {
                let setup = crate::commands::build_setup(&wiring, deps).map_err(|e| failed(&e))?;
                let done = setup.check(flags).map_err(|e| {
                    let exit = e.exit();
                    refused(&e, exit)
                })?;
                SetupAnswer::CheckManage(v1::CheckManageResponse {
                    notices: done
                        .notices
                        .iter()
                        .map(run_application::client::notice_of)
                        .collect(),
                    ..surface_cli::wire::checked(&done.value)
                })
            }
            SetupRead::InitPrompt => unreachable!("answered above"),
        })
    }
}

/// `init prompt`: the setup interview through the caller's catalog (a
/// project bundle `ctxloom` overrides the built-in one whole once the
/// project is trusted, `flags.trusted`, D4), with what reading it noticed.
fn init_prompt(
    wiring: &crate::Wiring,
    flags: &library_application::Flags,
) -> Result<SetupAnswer, ApiError> {
    use cli_support::ExitClass;
    let library = crate::library::build(wiring).map_err(|e| ApiError::diagnosed(&e))?;
    let read = library.operational_prompt(flags, library::builtin::SETUP_INTERVIEW_NAME);
    let text = read.result.map_err(|e| {
        let exit = e.exit();
        refused(&e, exit)
    })?;
    // Each enabled registered companion's guidance, asked through the
    // library's one probe at the caller's site (OD-8, D1).
    let companion_guidance = library
        .companion_inits(flags)
        .result
        .unwrap_or_default()
        .into_iter()
        .map(|c| c.init.setup_guidance)
        .filter(|g| !g.is_empty())
        .collect();
    Ok(SetupAnswer::InitPrompt(v1::GetInitPromptResponse {
        // As `init prompt` has always printed it: a line after it.
        text: format!("{text}\n"),
        companion_guidance,
        notices: read
            .notices
            .iter()
            .filter(|notice| matches!(notice, library_application::Notice::PromptUntrusted { .. }))
            .map(run_application::client::notice_of)
            .collect(),
    }))
}

#[cfg(test)]
#[path = "setup_reads.test.rs"]
mod tests;
