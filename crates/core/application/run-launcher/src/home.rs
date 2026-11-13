//! THE one session-setup path (owner, 2026-11-12, "One session-setup
//! path"): everything that sets up a launch, the interactive terminal's, a
//! one-shot child's and the daemon's hosted children alike
//! ([`set_up_launch`]): the workspace its engine is placed in, its session
//! home, what it reads only from where it works, and the live project named
//! to it. The engine prepares the home from what it holds of the
//! files it merges into (ruling 113); its files are written whole, its seeds
//! once (one already there is left as it is, ruling 167), and, in a session
//! home, the user's git identity is its `.gitconfig` (ruling 112). The
//! user's own engine settings (ruling 169) and the program its callbacks run
//! (ruling 163) are the engine's own, as the composition root built it.

use isolation_ports::Workspaces;
use run_ports::{
    Engine, GitUser, HomeFile, LaunchSurfaces, LeftOut, PrepareRequest, SessionHome, SessionRecords,
};

use crate::workspace::{place_workspace, Axis, Placement, Workspace};

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

/// What a launch is set up from: its home's setup, whose `cwd` is the live
/// project (none: no project), the workspace axis it asks for, and the
/// environment it hands the engine with the names it owns there.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct LaunchSetup {
    pub home: HomeSetup,
    pub workspace: Workspace,
    pub env: std::collections::BTreeMap<String, String>,
    pub owned: Vec<String>,
}

/// A launch set up: its home, where its engine works (`cwd`, and the
/// placement that put it there), and the environment and owned names the
/// engine is handed, the live project named in them.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct LaunchSetUp {
    pub home: HomeSetUp,
    pub placed: Option<Placement>,
    pub cwd: Option<String>,
    pub env: std::collections::BTreeMap<String, String>,
    pub owned: Vec<String>,
}

/// Set up a launch: THE one session-setup path (owner, 2026-11-12), the
/// launcher's `prepare` and the daemon's hosted children alike. The engine is
/// placed in its workspace (`place_workspace`: none for no project; the
/// project where no worktree is asked, or `workspaces` makes none, with a
/// warning), its home set up there ([`set_up_home`]), what it reads only from
/// where it works written into its checkout, and the live project named to
/// it as `CTXLOOM_ROOT`, whatever its workspace, so its hook processes and
/// the agent's own `ctxloom` calls resolve it from a checkout (one the caller
/// had is replaced, and owned, so no permit replaces it). Its error is why
/// nothing can start.
pub fn set_up_launch(
    engine: &dyn Engine,
    records: &mut dyn SessionRecords,
    workspaces: Option<&dyn Workspaces>,
    setup: &LaunchSetup,
) -> Result<LaunchSetUp, String> {
    let mut placed = place(
        setup.home.cwd.as_deref(),
        &setup.workspace,
        &engine.workspace_patterns(setup.home.session_home),
        workspaces,
    )?;
    let cwd = placed
        .as_ref()
        .map(|p| p.cwd.clone())
        .or_else(|| setup.home.cwd.clone());
    let home = set_up_home(
        engine,
        records,
        &HomeSetup {
            cwd: cwd.clone(),
            ..setup.home.clone()
        },
    )?;
    place_files(placed.as_mut(), &home.workspace_files, workspaces)?;
    let mut env = setup.env.clone();
    let mut owned = setup.owned.clone();
    env.remove(run_ports::ROOT_VARIABLE);
    if let Some(project) = &setup.home.cwd {
        env.insert(run_ports::ROOT_VARIABLE.to_string(), project.clone());
        owned.push(run_ports::ROOT_VARIABLE.to_string());
    }
    Ok(LaunchSetUp {
        home,
        placed,
        cwd,
        env,
        owned,
    })
}

/// Where the engine works (the workspace axis): none with no project; the
/// project where no worktree is asked, or `workspaces` makes none (with a
/// warning); else what [`place_workspace`] says. A worktree refused is why
/// nothing started.
fn place(
    project: Option<&str>,
    asked: &Workspace,
    patterns: &[String],
    workspaces: Option<&dyn Workspaces>,
) -> Result<Option<Placement>, String> {
    let Some(project) = project else {
        return Ok(None);
    };
    let Some(workspaces) = workspaces else {
        return Ok(Some(Placement {
            cwd: project.to_string(),
            checkout: None,
            warning: (asked.axis == Axis::Worktree)
                .then(|| crate::workspace::NO_WORKSPACES.to_string()),
            files: Vec::new(),
        }));
    };
    place_workspace(
        asked.axis,
        project,
        &asked.work_dir,
        &asked.agent,
        patterns,
        workspaces,
    )
    .map(Some)
    .map_err(|e| e.to_string())
}

/// Write what the launch carries that its engine reads only from where it
/// works into the checkout it was placed in (owner ruling 2026-11-12, Q3),
/// and keep their paths for the release; in the live project none is
/// written.
fn place_files(
    placed: Option<&mut Placement>,
    files: &[HomeFile],
    workspaces: Option<&dyn Workspaces>,
) -> Result<(), String> {
    let (Some(placed), Some(workspaces)) = (placed, workspaces) else {
        return Ok(());
    };
    let Some(checkout) = &placed.checkout else {
        return Ok(());
    };
    if files.is_empty() {
        return Ok(());
    }
    let pairs: Vec<(String, Vec<u8>)> = files
        .iter()
        .map(|f| (f.rel.clone(), f.bytes.clone()))
        .collect();
    workspaces
        .place_files(&checkout.path, &pairs)
        .map_err(|e| e.to_string())?;
    placed.files = files.iter().map(|f| f.rel.clone()).collect();
    Ok(())
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
