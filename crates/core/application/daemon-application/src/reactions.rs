//! A library change reacted to (step 10c U13, G6/G7; protos.md §2): the
//! pure, keyed policy.
//!
//! A session's loadout names what its run was planned to draw on: its
//! agent, its profiles, and the bundles its fragments came from (and its
//! tree's config always). Each recorded change (a `TreeObserved` or a
//! `StepApplied`, the events whose sequences move the resources' tokens:
//! one mechanism) is held against the loadout of each live session on its
//! tree by the OwedContexts handler ([`crate::owed`]): one whose loadout it
//! [`touched`] is recorded owed it, and its next hook collects every
//! change owed in one re-assembly. An owed delivery stays owed until a
//! re-assembly covering it is recorded (a hook that cannot reach the
//! daemon in time, or an event the engine cannot carry it at, leaves it
//! owed), across a daemon's restart too. Nothing here reads a file or a
//! clock.
//!
//! `daemon_application::Reaction` (distill's seam, 10b) reacts to a hosted
//! run's OUTCOME; a library change is no run's, so this policy is a seam of
//! its own beside it, in the same shape: what follows decided purely, made
//! by the daemon.

use daemon_proto::v1;
use sessions::SessionLoadout;

/// Where a bundle's directory is in its tree.
const BUNDLES: &str = "content/bundles/v2/";

/// `names` each once, in the order first named.
fn once(names: impl IntoIterator<Item = String>) -> Vec<String> {
    let mut kept: Vec<String> = Vec::new();
    for name in names {
        if !name.is_empty() && !kept.contains(&name) {
            kept.push(name);
        }
    }
    kept
}

/// The loadout of a plan (moved to run-application, where a run records
/// it too).
pub use run_application::loadout_of_plan;

/// The bundle a fragment's resource name (`.../bundles/<b>/fragments/<f>`)
/// is in.
fn bundle_of_name(name: &str) -> Option<String> {
    let (_, rest) = name.split_once("/bundles/")?;
    let (bundle, _) = rest.split_once("/fragments/")?;
    Some(bundle.to_string())
}

/// The loadout of an assembly of `agent` over `profiles`: the bundles of
/// the fragments it used and of those a premise held back.
pub fn loadout_of_assembled(
    agent: &str,
    profiles: &[String],
    assembled: &v1::AssembledContext,
) -> SessionLoadout {
    let used = assembled.fragments.iter().map(|f| f.fragment.as_str());
    let held = assembled.premised.iter().map(|p| p.fragment.as_str());
    SessionLoadout {
        agent: agent.to_string(),
        profiles: profiles.to_vec(),
        bundles: once(used.chain(held).filter_map(bundle_of_name)),
    }
}

/// The resource name of the fragment at `path` (relative to its tree,
/// `content/bundles/v2/<b>/fragments/<f>.md`) of the tree `tree`
/// (`projects/{p}/checkouts/{c}/library`, `users/me/library`):
/// `projects/{p}/bundles/<b>/fragments/<f>`, or the home's under
/// `users/me`; none for a path that is no fragment's.
pub fn fragment_name(tree: &str, path: &str) -> Option<String> {
    let rest = path.strip_prefix(BUNDLES)?;
    let (bundle, rest) = rest.split_once("/fragments/")?;
    let fragment = rest.strip_suffix(".md")?;
    if bundle.is_empty() || fragment.is_empty() {
        return None;
    }
    let parent = match tree {
        HOME_TREE => "users/me".to_string(),
        tree => {
            let (project, _) = tree.strip_prefix("projects/")?.split_once("/checkouts/")?;
            format!("projects/{project}")
        }
    };
    Some(format!("{parent}/bundles/{bundle}/fragments/{fragment}"))
}

/// The home's library tree.
const HOME_TREE: &str = "users/me/library";

#[cfg(test)]
#[path = "reactions.test.rs"]
mod tests;
