//! What a session's run was planned to draw on (step 10c U13): its
//! loadout, recorded on its session once it is planned, so a change to a
//! bundle it names is told the session.

use sessions::SessionLoadout;

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

/// The bundle a loadout names for `bundle`, a plan's reference to it: a
/// local bundle (`ctxloom+local:<b>`) as its tree holds it, the directory
/// a change's path is under (`content/bundles/v2/<b>/`), which is how a
/// change is judged against the loadout; any other as it is referenced (a
/// companion's or a remote's bundle is in no tree).
fn loadout_bundle(bundle: &str) -> String {
    match library::refs::Parts::parse(bundle) {
        Ok(parts) if parts.class == library::refs::SourceClass::Local => parts.bundle,
        _ => bundle.to_string(),
    }
}

/// The loadout of a plan of `agent` over `profiles` whose fragments are
/// `fragments` (references, `<bundle>#fragments/<name>`), each bundle
/// named as [`loadout_bundle`] names it.
pub fn loadout_of_plan(agent: &str, profiles: &[String], fragments: &[String]) -> SessionLoadout {
    let bundles = fragments.iter().filter_map(|reference| {
        reference
            .split_once('#')
            .map(|(bundle, _)| loadout_bundle(bundle))
    });
    SessionLoadout {
        agent: agent.to_string(),
        profiles: profiles.to_vec(),
        bundles: once(bundles),
    }
}

#[cfg(test)]
#[path = "loadout.test.rs"]
mod tests;
