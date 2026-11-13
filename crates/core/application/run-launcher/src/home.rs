//! THE one session-setup path (owner, 2026-11-12, "One session-setup
//! path"): everything that prepares a session's home for a launch, the
//! interactive terminal's, a one-shot child's and the daemon's hosted
//! children alike. The engine prepares the home from what it holds of the
//! files it merges into (ruling 113); its files are written whole, its seeds
//! once (one already there is left as it is, ruling 167), and, in a session
//! home, the user's git identity is its `.gitconfig` (ruling 112). The
//! user's own engine settings (ruling 169) and the program its callbacks run
//! (ruling 163) are the engine's own, as the composition root built it.

use run_ports::{
    Engine, GitUser, HomeFile, LaunchSurfaces, LeftOut, PrepareRequest, SessionHome, SessionRecords,
};

/// What a session's home is set up from.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct HomeSetup {
    /// The session, by its harp: its directory is the records'.
    pub harp: String,
    /// The assembled context the engine is handed.
    pub context: String,
    /// The home is the human's terminal session's (the engine installs its
    /// hooks in it).
    pub interactive: bool,
    /// The directory the engine runs in.
    pub cwd: Option<String>,
    /// The agent's permissions document for the engine (rulings 135, 137).
    pub permissions: Option<String>,
    /// Whose engine configuration directory the run gets.
    pub session_home: SessionHome,
    /// The user's git identity in the project; none, none is seeded.
    pub git_user: Option<GitUser>,
    /// What the run agent's selection carries (its commands, skills, hooks
    /// and MCP servers), each engine carrying it its own way.
    pub surfaces: LaunchSurfaces,
}

/// A home set up: where it is, the context file the engine named, and the
/// user's own settings it was not started from (the run says each).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct HomeSetUp {
    pub home: String,
    pub context_path: String,
    pub left_out: Vec<LeftOut>,
    /// What the engine reads only from where it works, for the checkout it
    /// is placed in (none is written in the live project).
    pub workspace_files: Vec<HomeFile>,
}

/// Set up `setup`'s session home in `records` for `engine`: the one
/// function every launch's home is prepared by. Its error is why nothing
/// can start.
pub fn set_up_home(
    engine: &dyn Engine,
    records: &mut dyn SessionRecords,
    setup: &HomeSetup,
) -> Result<HomeSetUp, String> {
    let harp = setup.harp.as_str();
    let home = format!("{}/home", records.dir(harp));
    // What the home holds of the files the engine merges into (ruling
    // 113): a resumed session's home has its engine's own state there.
    let mut existing = std::collections::BTreeMap::new();
    for rel in engine.merges() {
        if let Some(bytes) = records
            .read(harp, &format!("home/{rel}"))
            .map_err(|e| e.to_string())?
        {
            existing.insert((*rel).to_string(), bytes);
        }
    }
    let prepared = engine
        .prepare(&PrepareRequest {
            home: home.clone(),
            context: setup.context.clone(),
            // Nothing of the user's own engine configuration is offered.
            offered: Vec::new(),
            interactive: setup.interactive,
            cwd: setup.cwd.clone(),
            existing,
            permissions: setup.permissions.clone(),
            session_home: setup.session_home,
            surfaces: setup.surfaces.clone(),
        })
        .map_err(|e| e.to_string())?;
    for file in &prepared.files {
        records
            .write(harp, &format!("home/{}", file.rel), &file.bytes)
            .map_err(|e| e.to_string())?;
    }
    for seed in &prepared.seeds {
        seed_once(records, harp, &format!("home/{}", seed.rel), &seed.bytes)?;
    }
    seed_git_user(records, setup)?;
    Ok(HomeSetUp {
        home,
        context_path: prepared.context_path,
        left_out: prepared.left_out,
        workspace_files: prepared.workspace_files,
    })
}

/// Seed the session home with the user's git identity (owner ruling 112:
/// the engine's HOME is the session home, so git reads its global
/// configuration there): once, when the home has no `.gitconfig`; one
/// already there, which a tool of an earlier run may have written, is left
/// as it is. Under `session_home: host` the engine's HOME is the user's own,
/// which holds her git identity already, so nothing is seeded.
fn seed_git_user(records: &mut dyn SessionRecords, setup: &HomeSetup) -> Result<(), String> {
    if setup.session_home == SessionHome::Host {
        return Ok(());
    }
    let Some(text) = setup.git_user.as_ref().and_then(run_ports::git_config) else {
        return Ok(());
    };
    let rel = format!("home/{}", run_ports::GIT_CONFIG_FILE);
    seed_once(records, &setup.harp, &rel, text.as_bytes())
}

/// Write `bytes` at `rel` of the session's directory once: when nothing is
/// there. One already there is left as it is: the `.gitconfig` a tool of an
/// earlier run wrote, the engine's seed (opencode's recent models, owner
/// ruling 167) the engine has rewritten since.
fn seed_once(
    records: &mut dyn SessionRecords,
    harp: &str,
    rel: &str,
    bytes: &[u8],
) -> Result<(), String> {
    let there = records.read(harp, rel).map_err(|e| e.to_string())?;
    if there.is_none() {
        records.write(harp, rel, bytes).map_err(|e| e.to_string())?;
    }
    Ok(())
}
