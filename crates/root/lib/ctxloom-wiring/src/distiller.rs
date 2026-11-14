//! Distill's model (plan step 5), wired: `library_application::Distiller`
//! over a one-shot run on no context (`run --no-context`, owner OD-38), the
//! way init's interview runs its session over [`OneShots`]. The run is built
//! when it is asked, over the project as it is then; its session is an
//! ordinary one-shot's (no provenance, owner 2026-11-13), recorded by its
//! hooks like any other.

use std::sync::Arc;

use angzarr_diagnostic::Diagnostic;
use library_application::{Distilled, Distiller, Flags, RunRequest};
use process_env::EnvSnapshot;
use run_application::{OneShot, RunService};

use crate::oneshot::OneShots;
use crate::Wiring;

/// The distiller: a one-shot with no context, of the default agent (else
/// the default engine).
pub(crate) struct OneShotDistiller {
    pub(crate) env: Arc<EnvSnapshot>,
}

/// What failed before the run could be asked.
fn failed(cause: impl Diagnostic + 'static) -> Distilled {
    Distilled {
        answer: Err(Box::new(cause)),
        notices: Vec::new(),
    }
}

impl Distiller for OneShotDistiller {
    // Builds and drives a real run (an engine process, the store, the
    // listener); the acceptance tests drive it (content_distill.feature,
    // j000700) over a stub model.
    #[cfg_attr(test, mutants::skip)]
    fn distill(&self, flags: &Flags, request: &str) -> Distilled {
        let wiring = match Wiring::new(Arc::clone(&self.env)) {
            Ok(wiring) => wiring,
            Err(e) => return failed(e),
        };
        let library = match crate::library::build(&wiring, None) {
            Ok(library) => library,
            Err(e) => return failed(e),
        };
        let mut runs = OneShots::new(&wiring, library);
        let asked = OneShot {
            run: RunRequest {
                prompt: request.to_string(),
                no_context: true,
                ..RunRequest::default()
            },
            engine: String::new(),
            session: String::new(),
        };
        let done = runs.one_shot(flags, &asked);
        Distilled {
            answer: done
                .result
                .map(|answer| answer.answer)
                .map_err(|e| Box::new(e) as Box<dyn Diagnostic>),
            notices: done.notices,
        }
    }
}
