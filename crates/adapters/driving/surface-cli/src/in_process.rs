//! The setup writes served by the use cases in this process, writing through
//! their own store: what the CLI is handed in a test.

use cli_support::{ExitClass, Failure};
use daemon_proto::v1;
use setup_application::Setup;
use surface_api::wire::{
    checked, gitignore_installed, materialize_request, materialized, uninstalled, NoticeOf,
};

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
