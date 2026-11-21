//! The isolation commands wired (S11): the worktree commands over the
//! sessions' locks and git, and `container` over the machine's runtimes, the
//! engines, the registered companions and the project's files.

use cli_support::Exit;
use std::rc::Rc;
use std::sync::Arc;

use angzarr_presentation::Catalog;
use container_runtime::HostRuntimes;
use content_fs::UserFilesFs;
use ctxloom_cli::{Call, Failure};
use isolation_application::Containers;
use isolation_ports::{Backends, Companions, Failure as PortFailure, Tooling};
use library_application::{Flags, Library};
use process_env::EnvSnapshot;

use crate::{LibError, Wiring};

/// The engines `container` may be asked about, and the one a bare command
/// means: the configured default when it is an engine, else the first.
struct Engines {
    names: Vec<String>,
    library: Rc<Library>,
    flags: Flags,
}

impl Backends for Engines {
    fn names(&self) -> Vec<String> {
        self.names.clone()
    }

    fn default_backend(&self) -> Option<String> {
        let configured = self
            .library
            .default_engine(&self.flags)
            .result
            .ok()
            .filter(|engine| self.names.contains(engine));
        configured.or_else(|| self.names.first().cloned())
    }
}

/// The registered companions' declarations: only a REGISTERED, enabled
/// companion is ever run, through the library's one probe (OD-8); one on the
/// PATH nobody registered declares nothing.
struct Registered {
    library: Rc<Library>,
    flags: Flags,
}

impl Companions for Registered {
    fn tooling(&self) -> Result<Vec<Tooling>, PortFailure> {
        let Ok(inits) = self.library.companion_inits(&self.flags).result else {
            return Ok(Vec::new());
        };
        Ok(inits
            .into_iter()
            .filter(|c| !c.init.tooling.is_empty())
            .map(|c| Tooling {
                source: c.name,
                content: c.init.tooling,
            })
            .collect())
    }
}

/// Run `ctxloom container ...`.
pub(crate) fn container(
    env: &Arc<EnvSnapshot>,
    call: &Call<'_>,
    _catalog: &Catalog,
    out: &mut dyn std::io::Write,
) -> Result<Exit, Failure> {
    let wired = |e: LibError| Failure::boxed(Box::new(e), Exit::FAILED);
    let wiring = Wiring::new(Arc::clone(env)).map_err(wired)?;
    let library = Rc::new(crate::library::build(&wiring, None).map_err(wired)?);
    let flags = call.globals.clone();
    let containers = Containers::new(
        HostRuntimes::from_env(wiring.fs(), Arc::clone(env)),
        Engines {
            names: crate::commands::engines().names(),
            library: Rc::clone(&library),
            flags: flags.clone(),
        },
        Registered {
            library: Rc::clone(&library),
            flags,
        },
        UserFilesFs::new(wiring.fs()),
    );
    container_cli::run(&containers, call, out)
}

#[cfg(test)]
#[path = "isolation.test.rs"]
mod tests;
