//! `ctxloom init` (S12).
//!
//! `init` is the daemon's (step 10c U6, D9): its questions are its
//! preview's needs ([`needs`]), every one optional, and its apply creates
//! the project's `.ctxloom` whole (the daemon's `InitProject`). What is
//! here is what both ends share: the questions as data and how the apply
//! reads their answers ([`needs`]), a first init's companion registration
//! from her answers ([`companions`], D2), the setup interview's mechanism
//! ([`interview`]: on a terminal, the interview and the offer to restart
//! into the configured session) and `init prompt`'s body ([`prompt`]).
//! No git is asked or run for init, nor for `config create`: the library
//! is git-free (owner, 2026-11-18).
//!
//! NOT BUILT: the interview's own questions (the engine asks them). The
//! agent-token guide is the driving adapter's, from `auth-application`.

mod companions;
mod interview;
pub mod needs;

pub use ctxloom_ports::{Failure, Scaffold, UserFiles};
use exit_code::Exit;

pub use companions::{
    accepted as accepted_companions, offer_question, Answer, BY_NAME_ID, COMPANIONS_BY_NAME,
};
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
    /// The setup interview's body and what reading it noticed (a project
    /// `ctxloom` bundle passed over for the built-in one, untrusted: step 8
    /// U2, D4), or why there is none to read (a project `ctxloom` bundle
    /// without one, or that cannot be read).
    fn setup_interview(&self) -> Result<PromptRead, PromptFailure>;

    /// Each enabled registered companion's `init.setup_guidance`, in order
    /// (j000300): what the interview carries after ctxloom's own. A
    /// disabled companion's is none (OD-9). None by default.
    // `vec![]` is the same value as `Vec::new()`: the mutant is equivalent.
    #[cfg_attr(test, mutants::skip)]
    fn companion_guidance(&self) -> Vec<String> {
        Vec::new()
    }
}

/// A prompt as read: its body, and what reading it noticed, for the error
/// stream.
#[derive(Debug, Default)]
pub struct PromptRead {
    pub text: String,
    pub notices: Vec<Failure>,
}

impl From<String> for PromptRead {
    /// A body read with nothing noticed.
    fn from(text: String) -> PromptRead {
        PromptRead {
            text,
            notices: Vec::new(),
        }
    }
}

/// A prompt that could not be read: why, and the exit code that reason
/// carries (refused 20, any other failure 10).
#[derive(Debug)]
pub struct PromptFailure {
    pub cause: Failure,
    pub exit: Exit,
}

/// The output directory a first init suggests: `output.dir`'s default (owner
/// ruling 106), inside the project.
pub const OUTPUT_DIR_SUGGESTED: &str = "ctxloom/";

/// What a first init asks at the terminal (rulings 89, 106), and says while
/// asking: the directory is not gitignored, so outputs can be committed.
pub const OUTPUT_DIR_QUESTION: &str = "Where should your sessions' outputs (plans, reports) go? A path relative to the project, or absolute. It is not gitignored, so what they leave there can be committed.";

/// What a first init suggests for `config.premised` (owner ruling of
/// 2026-11-12): its default, conditional guidance loaded when it applies.
pub const PREMISED_SUGGESTED: &str = "on-demand";

/// The other answer: every fragment loads at launch, premised ones too.
pub const PREMISED_ALWAYS: &str = "always";

/// What a first init asks at the terminal after the output directory, and
/// says while asking: on demand keeps the context smaller.
pub const PREMISED_QUESTION: &str = "Should conditional guidance (a fragment with a premise) load only when it applies, which keeps the context smaller, or always? Answer on-demand or always.";

/// How often the question is put before an answer that is neither word
/// leaves the suggestion standing.
pub const PREMISED_TRIES: usize = 3;

/// `init prompt`: the interview body, as Go prints it (a line after it),
/// and what reading it noticed.
pub fn prompt(prompts: &dyn Prompts) -> Result<PromptRead, PromptFailure> {
    let read = prompts.setup_interview()?;
    Ok(PromptRead {
        text: format!("{}\n", read.text),
        notices: read.notices,
    })
}

#[cfg(test)]
#[path = "lib.test.rs"]
mod tests;
