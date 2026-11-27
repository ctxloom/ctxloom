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
pub struct DaemonTop<'a> {
    wiring: &'a Wiring,
    stamp: String,
    runtime: Arc<tokio::runtime::Runtime>,
    /// The signals heard for the command's whole life (ruling 114): none
    /// ends it by its default, and the first stops it.
    stops: crate::signals::CommandStops,
}

impl<'a> DaemonTop<'a> {
    pub fn new(
        wiring: &'a Wiring,
        stamp: &str,
    ) -> Result<DaemonTop<'a>, Box<dyn coded_diagnostic::Diagnostic>> {
        let runtime = tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .build()
            .map_err(|e| -> Box<dyn coded_diagnostic::Diagnostic> {
                Box::new(ctxloom_cli::CliError::Output {
                    detail: e.to_string(),
                })
            })?;
        let fs = wiring.fs();
        let stops =
            crate::signals::CommandStops::hearing(move || fs.os().signals(&crate::signals::HEARD));
        Ok(DaemonTop {
            wiring,
            stamp: stamp.to_string(),
            runtime: Arc::new(runtime),
            stops,
        })
    }
}

/// A draining daemon refuses every new call: it is none to watch through,
/// and `top` waits for the next.
fn is_draining(called: &daemon_ports::Called) -> bool {
    called.refusal::<v1::DaemonDraining>().is_some()
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
        // A session named by its harp alone is a session of the project the
        // command is in: where there is none, it is refused (as `session
        // show` refuses it), before any daemon is looked for.
        if !all && !matches!(self.wiring.registration(), Ok(Some(_))) {
            let request = make("")?;
            if !request.session.is_empty() && !request.session.starts_with("projects/") {
                let cwd = self
                    .wiring
                    .env()
                    .cwd()
                    .map(|cwd| cwd.display().to_string())
                    .unwrap_or_default();
                return Err(session_cli::Failure::of(daemon_api::CallError::Refused(
                    daemon_api::Rejection::Api(daemon_api::ApiRejection::ProjectUnknown { cwd }),
                )));
            }
        }
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
                    Err(draining) if is_draining(&draining) => return Ok(None),
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
            Err(draining) if is_draining(&draining) => Ok(None),
            Err(refused) => Err(failure(refused)),
        }
    }

    fn stopped(&self) -> Option<session_cli::Failure> {
        self.stops.signal().map(|signal| {
            session_cli::Failure::of(run_application::RunError::Stopped {
                signal: signal.name().to_string(),
                status: signal.exit_status(),
            })
        })
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

    // The screen's tick: a wait for the push, not a look at the daemon.
    #[cfg_attr(test, mutants::skip)]
    fn within(&mut self, wait: Duration) -> session_cli::Within {
        let waited = self
            .runtime
            .block_on(async { tokio::time::timeout(wait, self.events.next()).await });
        match waited {
            Err(_) => session_cli::Within::Idle,
            Ok(Some(Ok(event))) => session_cli::Within::Event(Box::new(event)),
            Ok(Some(Err(called))) => session_cli::Within::Broke(Box::new(failure(called))),
            Ok(None) => session_cli::Within::Ended,
        }
    }
}

/// What a project that is none has: an empty snapshot, and then silence.
struct Nothing {
    said: bool,
}

impl session_cli::TopFeed for Nothing {
    // A project made here later is found by opening the watch again.
    fn reopened_when_idle(&self) -> bool {
        true
    }

    // A watch with nothing to push is idle for the wait.
    #[cfg_attr(test, mutants::skip)]
    fn within(&mut self, wait: Duration) -> session_cli::Within {
        if !self.said {
            return match self.next() {
                Some(Ok(event)) => session_cli::Within::Event(Box::new(event)),
                _ => session_cli::Within::Ended,
            };
        }
        std::thread::sleep(wait);
        session_cli::Within::Idle
    }

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
