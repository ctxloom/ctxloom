//! THE one launch (T10): [`launch_of`] makes every engine process ctxloom
//! starts into a [`Launch`], in one order, for every kind alike (a one-shot's
//! turn, the terminal session, a hosted child):
//!
//! 1. the launch is set up ([`set_up_launch`]: its workspace, its session
//!    home, `CTXLOOM_ROOT`);
//! 2. the engine builds its command: the terminal's
//!    ([`Engine::drive_interactive`]), a turn's ([`Engine::drive_turn`]; a
//!    one-shot, owner ruling s4-Q8), or a child's ([`ChildEngine::command`]);
//! 3. the caller's permits are applied over it (rulings 78, 78b), never over
//!    a name ctxloom set;
//! 4. its program is found on its own `PATH` (PATHEXT's on Windows);
//! 5. a batch file's arguments are checked (std will not hand cmd.exe a line
//!    break);
//! 6. the complete command is measured against the platform's command line.
//!
//! A step that refuses ends the launch there: nothing after it is done, and
//! nothing is started. The launch's caller starts what it answers, exactly
//! as it is: the launcher a turn or the terminal (`RunnerLauncher::start`),
//! the daemon a child (`ChildEngine::start`).

use std::fmt;

use isolation_ports::Workspaces;
use run_ports::{
    ChildEngine, ChildPermissions, ChildSpec, Engine, EngineSettings, Ignored, Launch, LaunchAt,
    LaunchKind, LeftOut, ProgramNotFound, SessionRecords, Stdio, TurnCommand, TurnRequest,
};

use crate::home::{set_up_launch, LaunchSetUp, LaunchSetup};
use crate::workspace::Placement;

/// What a launch is made from: which run it starts and how, its setup, and
/// what its engine's command is built with.
#[derive(Clone, PartialEq, Eq)]
pub struct LaunchRequest {
    pub at: LaunchAt,
    pub kind: LaunchKind,
    pub setup: LaunchSetup,
    /// A one-shot's prompt (on its turn's stdin), or the terminal's opening
    /// one (on its command line); a child's is none (it is sent messages).
    pub prompt: String,
    /// The engine's own session the launch resumes; none, it starts one.
    pub resume: Option<String>,
    /// A child's new conversation's native id (D17a); never with `resume`.
    pub session_id: Option<String>,
    /// The user's own home, as the composition root resolved it once.
    pub user_home: Option<String>,
    /// The model; empty names none.
    pub model: String,
    /// The effort level; empty names none.
    pub effort: String,
    /// How the config says the engine is run.
    pub settings: EngineSettings,
    /// How a child's permission requests are answered.
    pub child_permissions: ChildPermissions,
    /// The session's lock its starter takes; empty names none.
    pub session_lock: String,
}

impl fmt::Debug for LaunchRequest {
    /// The environment's names alone (the setup's own `Debug`).
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("LaunchRequest")
            .field("at", &self.at)
            .field("kind", &self.kind)
            .field("setup", &self.setup)
            .field("prompt", &self.prompt)
            .field("resume", &self.resume)
            .field("session_id", &self.session_id)
            .field("user_home", &self.user_home)
            .field("model", &self.model)
            .field("effort", &self.effort)
            .field("settings", &self.settings)
            .field("child_permissions", &self.child_permissions)
            .field("session_lock", &self.session_lock)
            .finish()
    }
}

/// The engine a launch is made for: its `Engine` (the home it prepares, a
/// turn's and the terminal's command) and, for a child, its child half.
#[derive(Clone, Copy)]
pub struct LaunchEngines<'a> {
    pub engine: &'a dyn Engine,
    pub child: Option<&'a dyn ChildEngine>,
}

/// Where a launch's program is looked for (`RunnerLauncher::find_program`):
/// the file it is spawned as, or why it is on no directory of its `PATH`.
pub type FindProgram<'a> = &'a dyn Fn(&TurnCommand) -> Result<String, ProgramNotFound>;

/// A launch made: the launch, where its engine was placed (the checkout its
/// run releases), and, for a child, the spec its engine converses by.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct LaunchMade {
    pub launch: Launch,
    pub placed: Option<Placement>,
    pub child: Option<ChildSpec>,
}

/// Why a launch's making started nothing.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum NotLaunched {
    /// Its words: a home that could not be written, a command the engine
    /// would not build.
    Failed(String),
    /// The program is on no directory of the command's PATH.
    ProgramNotFound(ProgramNotFound),
    /// A batch file's argument std will not pass.
    BatchArguments(run_ports::BatchArguments),
    /// The command is too long for the platform.
    TooLong(run_ports::CommandTooLong),
}

impl fmt::Display for NotLaunched {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            NotLaunched::Failed(detail) => f.write_str(detail),
            NotLaunched::ProgramNotFound(missing) => missing.fmt(f),
            NotLaunched::BatchArguments(refusal) => refusal.fmt(f),
            NotLaunched::TooLong(refusal) => refusal.fmt(f),
        }
    }
}

/// A launch that started nothing: why, and what its steps before the
/// refusal said (the placement its run still releases, the settings left
/// out, the permits ignored).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Unprepared {
    pub why: NotLaunched,
    pub placed: Option<Placement>,
    pub left_out: Vec<LeftOut>,
    pub ignored: Vec<Ignored>,
}

impl Unprepared {
    fn at_setup(detail: String) -> Box<Unprepared> {
        Box::new(Unprepared {
            why: NotLaunched::Failed(detail),
            placed: None,
            left_out: Vec::new(),
            ignored: Vec::new(),
        })
    }
}

/// Make `request` into its launch on `engines`, in the one order (the
/// module's): set up, the engine's command, the permits, the program found
/// by `find`, the batch check and the length check for `os` (an empty `os`
/// measures nothing). Its error is why nothing can start.
pub fn launch_of(
    engines: LaunchEngines<'_>,
    find: FindProgram<'_>,
    records: &mut dyn SessionRecords,
    workspaces: Option<&dyn Workspaces>,
    request: &LaunchRequest,
    os: &str,
) -> Result<LaunchMade, Box<Unprepared>> {
    // 1. Set up: the one session-setup path (owner, 2026-11-12).
    let mut set_up = set_up_launch(engines.engine, records, workspaces, &request.setup)
        .map_err(Unprepared::at_setup)?;
    give_tool_homes(&mut set_up, request);
    let mut said = Unprepared {
        why: NotLaunched::Failed(String::new()),
        placed: set_up.placed.clone(),
        left_out: set_up.home.left_out.clone(),
        ignored: Vec::new(),
    };
    let refused = |mut said: Unprepared, why| {
        said.why = why;
        Box::new(said)
    };
    // 2. The engine's command, as the kind is driven.
    let (mut command, stdio, child) = match drive(engines, request, &set_up) {
        Ok(driven) => driven,
        Err(detail) => return Err(refused(said, NotLaunched::Failed(detail))),
    };
    // 3. The caller's CTXLOOM_PERMIT_<NAME> reaches every engine's command as
    // <NAME>, over what the engine chose (ruling 78), never over a name
    // ctxloom set for it (ruling 78b): the adapter's computed names, derived
    // from the command, and the run's own.
    said.ignored = run_ports::apply_permits(&request.setup.env, &mut command.env, &set_up.owned);
    // 3b. The human's terminal session offers its child-credential variables
    // to what it starts as permits (after the permits above, which strip
    // them): its MCP server, which claude hands no token of its own, then
    // holds them, and a child the session launches is judged and given the
    // credential. Never a child's turn, a one-shot's or the primary-only token.
    if request.kind == LaunchKind::Terminal {
        let policy = request.settings.credentials(engines.engine.credentials());
        let names: Vec<&str> = policy.child_env.iter().map(String::as_str).collect();
        for (permit, value) in run_ports::permits_for(&request.setup.env, &names) {
            command.env.entry(permit).or_insert(value);
        }
    }
    // 4. The program is found on the command's own PATH, by the names the
    // host looks it up by (PATHEXT's on Windows, which std's spawn does not
    // try), and the file found is what is spawned.
    command.program = match find(&command) {
        Ok(program) => program,
        Err(missing) => return Err(refused(said, NotLaunched::ProgramNotFound(missing))),
    };
    // 5. A batch file (an npm `claude.cmd`) is run through cmd.exe, which std
    // will not hand an argument of more than one line.
    if let Err(refusal) = run_ports::check_batch_arguments(os, &command) {
        return Err(refused(said, NotLaunched::BatchArguments(refusal)));
    }
    // 6. The command is complete: measured against the platform's command
    // line (the terminal's opening prompt is on it).
    if let Err(refusal) = run_ports::check_command_line(os, &command) {
        return Err(refused(said, NotLaunched::TooLong(refusal)));
    }
    let Unprepared {
        left_out, ignored, ..
    } = said;
    let launch = Launch {
        at: request.at.clone(),
        session_home: set_up.home.home.clone(),
        session_lock: request.session_lock.clone(),
        ignored,
        left_out,
        ..Launch::new(request.kind, engines.engine.name(), stdio, command)
    };
    Ok(LaunchMade {
        launch,
        placed: set_up.placed,
        child,
    })
}

/// The engine's HOME is its session home (owner ruling 112), so a tool that
/// finds its data through HOME (rustup, cargo, npm) would start from nothing
/// in every session. The launch names their real locations to the engine
/// (`run_ports::tool_homes`: the user's own variable, else the tool's default
/// under her home) and owns the names, so no permit replaces them. Under
/// `session_home: host` the engine's HOME is hers and nothing is added.
fn give_tool_homes(set_up: &mut LaunchSetUp, request: &LaunchRequest) {
    if request.setup.home.session_home == run_ports::SessionHome::Host {
        return;
    }
    let homes = run_ports::tool_homes(&request.setup.env, request.user_home.as_deref());
    for (name, value) in homes {
        set_up.owned.push(name.clone());
        set_up.env.insert(name, value);
    }
}

/// Step 2: the engine's command for `request`'s kind, the protocol it
/// speaks, and a child's spec; or why the engine would not build it.
fn drive(
    engines: LaunchEngines<'_>,
    request: &LaunchRequest,
    set_up: &LaunchSetUp,
) -> Result<(TurnCommand, Stdio, Option<ChildSpec>), String> {
    let turn = || TurnRequest {
        prompt: request.prompt.clone(),
        home: set_up.home.home.clone(),
        context_path: set_up.home.context_path.clone(),
        resume: request.resume.clone(),
        env: set_up.env.clone(),
        user_home: request.user_home.clone(),
        cwd: set_up.cwd.clone(),
        model: request.model.clone(),
        effort: request.effort.clone(),
        settings: request.settings.clone(),
        // ctxloom's server rides every launch with context (OD-38).
        approval_prompt: !request.setup.home.surfaces.0.no_context,
    };
    let engine = engines.engine;
    match request.kind {
        LaunchKind::Terminal => engine
            .drive_interactive(&turn())
            .map(|command| (command, Stdio::Inherit, None))
            .map_err(|e| e.to_string()),
        LaunchKind::OneShot => engine
            .drive_turn(&turn())
            .map(|command| (command, Stdio::Piped, None))
            .map_err(|e| e.to_string()),
        LaunchKind::Child => {
            let child = engines
                .child
                .ok_or_else(|| format!("{} hosts no child", engine.name()))?;
            let spec = ChildSpec {
                home: set_up.home.home.clone(),
                context_path: set_up.home.context_path.clone(),
                env: set_up.env.clone(),
                user_home: request.user_home.clone(),
                cwd: set_up.cwd.clone(),
                model: request.model.clone(),
                effort: request.effort.clone(),
                settings: request.settings.clone(),
                resume: request.resume.clone(),
                session_id: request.session_id.clone(),
                permissions: request.child_permissions,
            };
            let command = child.command(&spec).map_err(|e| e.to_string())?;
            Ok((command, child.stdio(), Some(spec)))
        }
        LaunchKind::Editor => Err(format!("{} launches no editor", engine.name())),
    }
}

#[cfg(test)]
#[path = "launch.test.rs"]
mod tests;
