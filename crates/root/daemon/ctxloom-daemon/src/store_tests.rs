//! The hidden `ctxloom test` commands that read or write the store
//! (`test-support` only): the daemon crate runs them, so the CLI's wiring
//! opens no store. The command line parses them ([`StoreVerb`]) and hands
//! each here through [`Hosting::store_tests`](ctxloom_wiring::daemon::Hosting).

use std::io::Write;
use std::ops::Deref;

use cli_support::Exit;
use ctxloom_cli::{CliError, Failure};
use ctxloom_wiring::{StoreVerb, TestError, Wiring};

use crate::seed::Seed;

/// A [`Wiring`] the test commands add their store methods to (an inherent
/// impl on another crate's type is not allowed).
pub(crate) struct Harness<'a>(pub(crate) &'a Wiring);

impl Deref for Harness<'_> {
    type Target = Wiring;

    fn deref(&self) -> &Wiring {
        self.0
    }
}

fn failed(cause: impl coded_diagnostic::Diagnostic + 'static) -> Failure {
    Failure::new(cause, Exit::FAILED)
}

fn output(written: std::io::Result<()>) -> Result<(), Failure> {
    written.map_err(|e| {
        failed(CliError::Output {
            detail: e.to_string(),
        })
    })
}

/// Run `verb` where `wiring` is, writing its answer to `out`.
pub(crate) fn run(
    wiring: &Wiring,
    version: &str,
    verb: StoreVerb,
    out: &mut dyn Write,
) -> Result<(), Failure> {
    let harness = Harness(wiring);
    match verb {
        StoreVerb::OpenStore { project } => {
            let path = harness.open_store(&project).map_err(failed)?;
            output(writeln!(out, "{}", path.display()))
        }
        StoreVerb::SeedSession { args, loadout } => {
            let seed = Seed {
                loadout,
                ..(*args).into()
            };
            let id = harness.seed_session(&seed).map_err(failed)?;
            output(writeln!(out, "{id}"))
        }
        StoreVerb::SessionName { harp } => {
            let name = harness.session_resource(&harp).map_err(failed)?;
            output(writeln!(out, "{name}"))
        }
        StoreVerb::SessionEvents { harp, run } => {
            let events = harness.session_events(&harp, run).map_err(failed)?;
            // As `test library-events` prints its tree's (step 10c U13: a
            // scenario reads `events[type=...]`).
            output(writeln!(out, "{}", serde_json::json!({ "events": events })))
        }
        StoreVerb::LibraryEvents { home } => {
            let events = harness.library_events(home, version).map_err(failed)?;
            output(writeln!(out, "{events}"))
        }
        StoreVerb::Record => {
            let record = harness.record().map_err(failed)?;
            output(writeln!(out, "{record}"))
        }
        StoreVerb::SeedRehosts { harp, count } => {
            harness.seed_rehosts(&harp, count).map_err(failed)
        }
    }
}

impl Harness<'_> {
    /// The one store opened (made, if need be) under the home for `project`
    /// (T14), the path of its database; refused where the project still has
    /// a store of its own (T17).
    #[expect(
        clippy::disallowed_methods,
        reason = "the daemon's own composition: `test open-store` opens the store as the daemon's start does"
    )]
    fn open_store(&self, project: &str) -> Result<std::path::PathBuf, TestError> {
        let stores = ctxloom_store_sqlite::Stores::new(self.fs(), self.home().to_path_buf());
        let runtime = tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .build()
            .map_err(|e| TestError::Seed {
                detail: e.to_string(),
            })?;
        // Closed, not dropped (ruling 131): the file holds the open's
        // migrations, with no log beside it, when this returns.
        runtime
            .block_on(async {
                let store = stores.open_for(project).await?;
                store.close().await.map_err(|e| {
                    ctxloom_store_sqlite::StoreOpenError::Open(
                        ctxloom_store_sqlite::OpenError::Store(e),
                    )
                })
            })
            .map_err(|e| TestError::Seed {
                detail: e.to_string(),
            })?;
        Ok(stores.path())
    }
}

#[cfg(test)]
#[path = "store_tests.test.rs"]
mod tests;
