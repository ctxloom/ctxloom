//! `ctxloom top`'s source (DRAFT): the user's daemon, reached if it serves,
//! never started, and its `WatchTop` followed.

use std::sync::Arc;
use std::time::Duration;

use daemon_ports::{DaemonApi as _, Daemons as _, TopEvents};
use daemon_proto::v1;

use crate::daemon::Reach;
use crate::Wiring;

/// How long `top` waits between looks for a daemon to serve. Nothing can be
/// pushed before one exists, so this is the one thing looked for on a timer.
const DAEMON_LOOK: Duration = Duration::from_millis(500);

/// The daemon `top` watches through.
pub(crate) struct DaemonTop<'a> {
    wiring: &'a Wiring,
    stamp: String,
    runtime: Arc<tokio::runtime::Runtime>,
}

impl<'a> DaemonTop<'a> {
    pub(crate) fn new(
        wiring: &'a Wiring,
        stamp: &str,
    ) -> Result<DaemonTop<'a>, Box<dyn angzarr_diagnostic::Diagnostic>> {
        let runtime = tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .build()
            .map_err(|e| -> Box<dyn angzarr_diagnostic::Diagnostic> {
                Box::new(ctxloom_cli::CliError::Output {
                    detail: e.to_string(),
                })
            })?;
        Ok(DaemonTop {
            wiring,
            stamp: stamp.to_string(),
            runtime: Arc::new(runtime),
        })
    }
}

/// A call that came to nothing as the command's failure.
fn failure(called: daemon_ports::Called) -> session_cli::Failure {
    // A session the project does not have is `top`'s own refusal, said so.
    if let Some(missing) = called.refusal::<v1::SessionNotFound>() {
        return session_cli::Failure::of(session_cli::CliFault::TopSessionUnknown {
            name: missing.name,
        });
    }
    session_cli::Failure::of(daemon_api::CallError::from(called))
}

impl session_cli::TopSource for DaemonTop<'_> {
    fn open(
        &self,
        all: bool,
        make: &dyn Fn(&str) -> Result<v1::WatchTopRequest, session_cli::Failure>,
    ) -> Result<Option<Box<dyn session_cli::TopFeed>>, session_cli::Failure> {
        let reach = Reach::new(self.wiring, &self.stamp);
        let Ok(Some(api)) = self.runtime.block_on(reach.serving()) else {
            return Ok(None);
        };
        let parent = match (all, self.wiring.registration()) {
            (true, _) => "projects/-".to_string(),
            (false, Ok(Some(registration))) => {
                match self.runtime.block_on(api.resolve_project(registration)) {
                    Ok(project) => project.name,
                    Err(daemon_ports::Called::Unreachable { .. }) => return Ok(None),
                    Err(refused) => return Err(failure(refused)),
                }
            }
            // Outside a project it lists nothing, and stays open.
            (false, _) => return Ok(Some(Box::new(Nothing { said: false }))),
        };
        let request = make(&parent)?;
        match self.runtime.block_on(api.watch_top(request)) {
            Ok(events) => Ok(Some(Box::new(Watching {
                events,
                runtime: Arc::clone(&self.runtime),
            }))),
            Err(daemon_ports::Called::Unreachable { .. }) => Ok(None),
            Err(refused) => Err(failure(refused)),
        }
    }

    // A timer: the only thing `top` looks for rather than is told.
    #[cfg_attr(test, mutants::skip)]
    fn wait(&self) {
        std::thread::sleep(DAEMON_LOOK);
    }
}

/// A watch the daemon pushes.
struct Watching {
    events: Box<dyn TopEvents>,
    runtime: Arc<tokio::runtime::Runtime>,
}

impl session_cli::TopFeed for Watching {
    fn next(&mut self) -> Option<Result<v1::TopEvent, session_cli::Failure>> {
        self.runtime
            .block_on(self.events.next())
            .map(|event| event.map_err(failure))
    }
}

/// What a project that is none has: an empty snapshot, and then silence.
struct Nothing {
    said: bool,
}

impl session_cli::TopFeed for Nothing {
    // Blocks for the life of the command, as a watch with nothing to push does.
    #[cfg_attr(test, mutants::skip)]
    fn next(&mut self) -> Option<Result<v1::TopEvent, session_cli::Failure>> {
        if !self.said {
            self.said = true;
            return Some(Ok(v1::TopEvent {
                event: Some(v1::top_event::Event::Snapshot(v1::TopSnapshot::default())),
            }));
        }
        loop {
            std::thread::park();
        }
    }
}
