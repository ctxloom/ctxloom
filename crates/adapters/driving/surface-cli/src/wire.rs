//! The setup writes as the API says them (step 10c U10): a materialize's
//! and an uninstall's reports as their messages, and a materialize's
//! request as the use case's. The daemon says its answers in these; the
//! CLI reads them back ([`super::materialize_report`]); a test serves them
//! in process ([`InProcess`]).

use daemon_proto::v1;
use setup_application::{
    BackendStatus, CheckReport, GitignoreReport, MaterializeRequest, MaterializeResult, Setup,
    SetupError, SurfaceCurrency, SurfaceSpec, UninstallReport,
};

use cli_support::{ExitClass, Failure};

/// A diagnostic said as the API says a notice: its code, flattened args and
/// en-US text (the caller's flattening: run-application's, in the daemon).
pub type NoticeOf<'a> = &'a dyn Fn(&dyn angzarr_diagnostic::Diagnostic) -> v1::Notice;

/// A materialize's report as the API says it, each failure said by
/// `notice_of` and its text among the errors too.
pub fn materialized(
    result: &MaterializeResult,
    notice_of: NoticeOf<'_>,
) -> v1::MaterializeResponse {
    let failures: Vec<v1::Notice> = result
        .failures
        .iter()
        .map(|failure| notice_of(failure))
        .collect();
    v1::MaterializeResponse {
        target: result.target.clone(),
        created: result.created,
        profiles: result.profiles.clone(),
        engines: result
            .engines
            .iter()
            .map(|engine| v1::MaterializedEngine {
                engine: engine.engine.clone(),
                wrote: engine.wrote.clone(),
                released: engine.released.clone(),
                context_file: engine.context_file.clone(),
                not_carried: engine
                    .not_carried
                    .iter()
                    .map(|lost| v1::NotCarried {
                        surface: lost.surface.clone(),
                        reason: lost.reason.clone(),
                        ..v1::NotCarried::default()
                    })
                    .collect(),
                skipped: engine.skipped.clone(),
                warnings: engine.warnings.clone(),
            })
            .collect(),
        status: result.status.clone(),
        errors: result
            .errors
            .iter()
            .cloned()
            .chain(failures.iter().map(|failure| failure.text.clone()))
            .collect(),
        warnings: result.warnings.clone(),
        failures,
        ..v1::MaterializeResponse::default()
    }
}

/// An uninstall's report as the API says it.
pub fn uninstalled(report: &UninstallReport) -> v1::UninstallManageResponse {
    v1::UninstallManageResponse {
        status: report.status.clone(),
        backends: report.backends.clone(),
        errors: report.errors.clone(),
        note: report.note.clone(),
        ..v1::UninstallManageResponse::default()
    }
}

/// `manage check`'s report as the API says it (step 10c U5).
pub fn checked(report: &CheckReport) -> v1::CheckManageResponse {
    v1::CheckManageResponse {
        work_dir: report.work_dir.clone(),
        manage_statusline: report.manage_statusline,
        backends: report
            .backends
            .iter()
            .map(|b| v1::ManagedBackend {
                backend: b.backend.clone(),
                settings_exists: b.settings_exists,
                hooks_present: b.hooks_present,
                hooks_at_launch: b.hooks_at_launch,
                statusline: b.status_line,
                mcp_present: b.mcp_present,
            })
            .collect(),
        surfaces: report
            .surfaces
            .iter()
            .map(|s| v1::ManagedSurface {
                route: s.route.clone(),
                backend: s.backend.clone(),
                status: s.status.clone(),
            })
            .collect(),
        ..v1::CheckManageResponse::default()
    }
}

/// The report the API's `manage check` answer says, as it has always
/// been printed.
pub fn check_report(answered: &v1::CheckManageResponse) -> CheckReport {
    CheckReport {
        work_dir: answered.work_dir.clone(),
        manage_statusline: answered.manage_statusline,
        backends: answered
            .backends
            .iter()
            .map(|b| BackendStatus {
                backend: b.backend.clone(),
                settings_exists: b.settings_exists,
                hooks_present: b.hooks_present,
                hooks_at_launch: b.hooks_at_launch,
                status_line: b.statusline,
                mcp_present: b.mcp_present,
            })
            .collect(),
        surfaces: answered
            .surfaces
            .iter()
            .map(|s| SurfaceCurrency {
                route: s.route.clone(),
                backend: s.backend.clone(),
                status: s.status.clone(),
            })
            .collect(),
    }
}

/// `manage gitignore install`'s report as the API says it (step 10c U5).
pub fn gitignore_installed(report: &GitignoreReport) -> v1::InstallGitignoreResponse {
    v1::InstallGitignoreResponse {
        status: report.status.clone(),
        path: report.path.clone(),
        nested_path: report.nested_path.clone(),
        nested_written: report.nested_written,
        ..v1::InstallGitignoreResponse::default()
    }
}

/// The report the API's `manage gitignore install` answer says, as it has
/// always been printed.
pub fn gitignore_report(answered: &v1::InstallGitignoreResponse) -> GitignoreReport {
    GitignoreReport {
        status: answered.status.clone(),
        path: answered.path.clone(),
        nested_path: answered.nested_path.clone(),
        nested_written: answered.nested_written,
    }
}

/// The use case's request a materialize's message asks for, under `flags`.
///
/// # Errors
/// A surface that does not parse.
pub fn materialize_request(
    asked: &v1::MaterializeRequest,
    flags: &library_application::Flags,
) -> Result<MaterializeRequest, SetupError> {
    Ok(MaterializeRequest {
        profiles: asked.profiles.clone(),
        target: (!asked.target.is_empty()).then(|| asked.target.clone()),
        yes: asked.yes,
        engines: asked.backends.clone(),
        surfaces: asked
            .surfaces
            .iter()
            .map(|spec| SurfaceSpec::parse(spec))
            .collect::<Result<_, _>>()?,
        release: asked.release,
        dry_run: asked.dry_run || asked.validate_only,
        leave_launch_context: false,
        flags: flags.clone(),
    })
}

/// The setup writes served by the use cases in this process, writing
/// through their own store: what the CLI is handed in a test.
pub struct InProcess<'a> {
    pub setup: &'a Setup,
    pub notice_of: NoticeOf<'a>,
}

impl super::Foreign for InProcess<'_> {
    fn materialize(
        &self,
        request: v1::MaterializeRequest,
    ) -> Result<v1::MaterializeResponse, Failure> {
        let flags = library_application::Flags::default();
        let done =
            materialize_request(&request, &flags).and_then(|asked| self.setup.materialize(&asked));
        // A use case's error is the call's, of its own class.
        done.map(|done| materialized(&done.value, self.notice_of))
            .map_err(|error| {
                let exit = error.exit();
                Failure::new(error, exit)
            })
    }

    fn check(&self, _: v1::CheckManageRequest) -> Result<v1::CheckManageResponse, Failure> {
        let flags = library_application::Flags::default();
        match self.setup.check(&flags) {
            Ok(done) => Ok(v1::CheckManageResponse {
                notices: done.notices.iter().map(|n| (self.notice_of)(n)).collect(),
                ..checked(&done.value)
            }),
            Err(error) => {
                let exit = error.exit();
                Err(Failure::new(error, exit))
            }
        }
    }

    fn install_gitignore(
        &self,
        _: v1::InstallGitignoreRequest,
    ) -> Result<v1::InstallGitignoreResponse, Failure> {
        match self.setup.gitignore_install() {
            Ok(report) => Ok(gitignore_installed(&report)),
            Err(error) => {
                let exit = error.exit();
                Err(Failure::new(error, exit))
            }
        }
    }

    /// As the daemon answers it, from the plan: the project there or not
    /// (the creation itself is the daemon's, step 10c U6).
    fn install_manage(
        &self,
        request: v1::InstallManageRequest,
    ) -> Result<v1::InstallManageResponse, Failure> {
        let engine = (!request.engine.is_empty()).then_some(request.engine.as_str());
        match self.setup.install_plan(engine) {
            Ok(plan) => Ok(v1::InstallManageResponse {
                app_dir: plan.ctxloom_dir,
                initialized: !plan.ctxloom_dir_exists,
                gitignore: plan.gitignore,
                gitignore_status: "unchanged".to_string(),
                ..v1::InstallManageResponse::default()
            }),
            Err(error) => {
                let exit = error.exit();
                Err(Failure::new(error, exit))
            }
        }
    }

    fn bind_project(&self, _: &str) -> Result<(), Failure> {
        Ok(())
    }

    fn uninstall(
        &self,
        _: v1::UninstallManageRequest,
    ) -> Result<v1::UninstallManageResponse, Failure> {
        match self.setup.uninstall() {
            Ok(report) => Ok(uninstalled(&report)),
            Err(error) => {
                let exit = error.exit();
                Err(Failure::new(error, exit))
            }
        }
    }
}
