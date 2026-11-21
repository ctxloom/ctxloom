//! `fragment|command distill` as the daemon answers it (step 10b; owner
//! ruling 2026-11-18: the daemon owns the model call as a run): the
//! response a distill call is answered with, from what its ask came to,
//! so the CLI renders its text from the message alone (D10). The run and
//! the write that follows from it are the daemon's.

use daemon_proto::v1;
use library_application::ItemDistilled;

/// The item a resource name names (`.../bundles/{b}/{dir}/{id}`), as its
/// reference (`{b}#{dir}/{id}`).
pub fn reference_of(name: &str) -> String {
    let rest = name.split_once("/bundles/").map_or(name, |(_, rest)| rest);
    match rest.split_once('/') {
        Some((bundle, item)) => format!("{bundle}#{item}"),
        None => rest.to_string(),
    }
}

/// What the item's form is said to be: none for an item marked
/// `no_distill`, else fresh (as it is, or as the run will leave it).
fn state(done: &ItemDistilled) -> i32 {
    match done.status.as_str() {
        "no_distill" => v1::DistilledState::None as i32,
        _ => v1::DistilledState::Fresh as i32,
    }
}

/// Why nothing is asked (`fresh`, `no_distill`); nothing for a distill
/// that asks.
fn detail(done: &ItemDistilled) -> Vec<String> {
    match done.status.as_str() {
        "distilled" => Vec::new(),
        status => vec![status.to_string()],
    }
}

/// `DistillFragment`'s answer for the fragment `name`: what its ask came
/// to (`done`), the run that asks its model (empty: none is asked), and
/// what was said. Applied by no call: a run's write is applied once it
/// ends, as its end says.
pub fn fragment_response(
    name: &str,
    done: &ItemDistilled,
    run: String,
    notices: Vec<v1::Notice>,
) -> v1::DistillFragmentResponse {
    v1::DistillFragmentResponse {
        fragment: Some(v1::Fragment {
            name: name.to_string(),
            distilled: state(done),
            no_distill: (done.status == "no_distill").then_some(true),
            ..v1::Fragment::default()
        }),
        path: done.path.clone(),
        applied: Some(false),
        detail: detail(done),
        run,
        notices,
        ..v1::DistillFragmentResponse::default()
    }
}

/// `DistillCommand`'s answer, as [`fragment_response`].
pub fn command_response(
    name: &str,
    done: &ItemDistilled,
    run: String,
    notices: Vec<v1::Notice>,
) -> v1::DistillCommandResponse {
    v1::DistillCommandResponse {
        command: Some(v1::Command {
            name: name.to_string(),
            distilled: state(done),
            no_distill: (done.status == "no_distill").then_some(true),
            ..v1::Command::default()
        }),
        path: done.path.clone(),
        applied: Some(false),
        detail: detail(done),
        run,
        notices,
        ..v1::DistillCommandResponse::default()
    }
}

#[cfg(test)]
#[path = "distill.test.rs"]
mod tests;
