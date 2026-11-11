//! `ctxloom init` (S12).
//!
//! BUILT: `init` on a directory that already has a `.ctxloom` (what every
//! re-run does): it says so, changes none of the configuration, and
//! registers the `--remote` repositories as `personal`, `personal-2`, ...
//! (a re-run once read and threw them away); and `init prompt`, the setup
//! interview body.
//!
//! `init` of a directory with no `.ctxloom`: it scaffolds the project (the
//! `Create` port, as `config create` does): the config, a remotes registry
//! that seeds no remote, and a BARE default profile that inherits nothing
//! (owner rulings 110, 111, 117). Nothing is pulled, and a fresh project
//! works offline.
//! It writes nothing outside `.ctxloom`: the engine files wait for a session
//! home.
//!
//! On a terminal, the setup interview and the offer to restart into the
//! configured session ([`interview`]; the mechanism, with the interview's
//! text as it is).
//!
//! NOT BUILT: the interview's own questions (the engine asks them), minting the project
//! identity (taskloom's registry owns it; a re-run only reads it). The
//! agent-token guide (a notice that init prints when no token is exported)
//! is the driving adapter's, from `auth-application`.

mod error;
mod interview;

use std::rc::Rc;
use std::sync::Arc;

pub use ctxloom_ports::{Failure, Scaffold, UserFiles};
use deps_application::{CreateRequest, Deps, Notice};
use serde::Serialize;

pub use error::InitError;
pub use interview::{
    interview, Ended, Interviewed, LaunchFailure, Outcome, Sessions, Terminal, RESTART_COMMAND,
    RESTART_QUESTION,
};

/// What the setup interview is read through: the project's catalog, as it is
/// when it is read (owner ruling 128). The interview is ctxloom's built-in
/// bundle's `ctxloom#prompts/setup-interview` (the owner's words, once Go
/// ctxloom's `resources/commands/ctxloom-init.md`), unless the project has a
/// bundle of its own named `ctxloom`, which overrides it whole.
pub trait Prompts {
    /// The setup interview's body, or why there is none to read (a project
    /// `ctxloom` bundle without one, or that cannot be read).
    fn setup_interview(&self) -> Result<String, PromptFailure>;
}

/// A prompt that could not be read: why, and the exit code that reason
/// carries (a refusal 2, any other failure 1).
#[derive(Debug)]
pub struct PromptFailure {
    pub cause: Failure,
    pub exit: u8,
}

/// Scaffolds a project's `.ctxloom`: the config, the remotes registry and the
/// seeded default profile; it refuses a directory that has a config.
pub trait Create {
    fn create(&self) -> Result<(), Failure>;

    /// Write `dir` as the new project's `output.dir`.
    fn set_output_dir(&self, dir: &str) -> Result<(), Failure>;

    /// The dependency use cases over the project as it is now: once
    /// [`create`](Create::create) has written it, the new project's (those
    /// built before saw no project, and would write the home's files).
    fn deps(&self) -> Result<Rc<Deps>, Failure>;
}

/// The output directory a first init suggests: `output.dir`'s default (owner
/// ruling 106), inside the project.
pub const OUTPUT_DIR_SUGGESTED: &str = "ctxloom/";

/// What a first init asks at the terminal (rulings 89, 106), and says while
/// asking: the directory is not gitignored, so outputs can be committed.
pub const OUTPUT_DIR_QUESTION: &str = "Where should your sessions' outputs (plans, reports) go? A path relative to the project, or absolute. It is not gitignored, so what they leave there can be committed.";

/// What `init` is asked.
#[derive(Clone, Debug, Default)]
pub struct InitRequest {
    /// `--remote`, repeatable.
    pub remotes: Vec<String>,
    /// `--forge`: the forge every `--remote` is bound to.
    pub forge: String,
}

/// A remote `init` registered.
#[derive(Clone, PartialEq, Eq, Debug, Serialize)]
pub struct RemoteAdded {
    pub name: String,
    pub url: String,
}

/// What an `init` came to.
#[derive(Debug, Serialize)]
pub struct Initialized {
    pub app_dir: String,
    /// The directory was there already.
    pub existed: bool,
    pub remotes: Vec<RemoteAdded>,
    /// What went wrong without stopping init (a remote that would not
    /// register), for the error stream.
    #[serde(skip)]
    pub notices: Vec<Notice>,
}

/// The names the `--remote` repositories are registered under.
fn personal_name(index: usize) -> String {
    match index {
        0 => "personal".to_string(),
        n => format!("personal-{}", n + 1),
    }
}

/// The init use case.
pub struct Init {
    scaffold: Arc<dyn Scaffold>,
    files: Arc<dyn UserFiles>,
    deps: Rc<Deps>,
    create: Box<dyn Create>,
}

impl Init {
    pub fn new(
        scaffold: Arc<dyn Scaffold>,
        files: Arc<dyn UserFiles>,
        deps: Rc<Deps>,
        create: Box<dyn Create>,
    ) -> Init {
        Init {
            scaffold,
            files,
            deps,
            create,
        }
    }

    pub fn init(&self, request: &InitRequest) -> Result<Initialized, InitError> {
        let app_dir = self.scaffold.app_dir();
        let existed = self.files.is_dir(&app_dir).map_err(InitError::Files)?;
        let mut done = Initialized {
            app_dir,
            existed,
            remotes: Vec::new(),
            notices: Vec::new(),
        };
        // The use cases over the project as it is: a new one's once it is
        // written (those built before saw no project and found the home).
        let mut deps = Rc::clone(&self.deps);
        if !existed {
            self.create.create().map_err(InitError::Create)?;
            deps = self.create.deps().map_err(InitError::Create)?;
        }
        for (index, url) in request.remotes.iter().enumerate() {
            let outcome = deps.remote_create(&CreateRequest {
                name: personal_name(index),
                url: url.clone(),
                forge: request.forge.clone(),
            });
            done.notices.extend(outcome.notices);
            match outcome.result {
                Ok(created) => done.remotes.push(RemoteAdded {
                    name: created.name,
                    url: created.url,
                }),
                Err(error) => done.notices.push(Notice::text(format!(
                    "failed to add remote \"{}\" ({url}): {error}",
                    personal_name(index)
                ))),
            }
        }
        Ok(done)
    }
}

impl Init {
    /// A first init's question at the terminal: where a session's outputs
    /// go (rulings 89, 106). The answer, trimmed, is written as the project's
    /// `output.dir`; a bare Enter or a closed input takes
    /// [`OUTPUT_DIR_SUGGESTED`]. Off a terminal nothing is asked and nothing
    /// written: the directory chosen, if one was.
    pub fn choose_output_dir(&self, terminal: &dyn Terminal) -> Result<Option<String>, InitError> {
        if !terminal.is_terminal() {
            return Ok(None);
        }
        let typed = terminal.ask(OUTPUT_DIR_QUESTION, OUTPUT_DIR_SUGGESTED);
        let dir = match typed.as_deref().map(str::trim) {
            Some(answer) if !answer.is_empty() => answer.to_string(),
            _ => OUTPUT_DIR_SUGGESTED.to_string(),
        };
        self.create
            .set_output_dir(&dir)
            .map_err(InitError::Create)?;
        Ok(Some(dir))
    }
}

/// `init prompt`: the interview body, as Go prints it (a line after it).
pub fn prompt(prompts: &dyn Prompts) -> Result<String, PromptFailure> {
    // RED: the port is not read.
    let _ = prompts;
    Ok(String::new())
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
