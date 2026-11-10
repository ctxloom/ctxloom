//! The seam through which another crate's commands join the command line:
//! the library commands (S1) are one, the sessions' another. This crate parses
//! the whole line (so help, usage errors and the exit ladder are one
//! behaviour) and hands the extension's noun its matches; the extension
//! answers, and its errors are reported like any other.

use std::io::Write;

use angzarr_diagnostic::Diagnostic;
use angzarr_presentation::Catalog;
use clap::Command;
use library_application::Flags;

/// Why a command did not complete: its own diagnostic and exit code, and the
/// refusals an aggregate refusal holds, in the order they were checked (owner
/// ruling 73b: the JSON envelope's `refusals`, the text's lines after the
/// summary). An error that is no aggregate holds none.
#[derive(Debug)]
pub struct Failure {
    pub cause: Box<dyn Diagnostic>,
    pub exit: u8,
    pub refusals: Vec<Box<dyn Diagnostic>>,
}

impl Failure {
    /// `cause`, ending the command with `exit`.
    pub fn new(cause: impl Diagnostic + 'static, exit: u8) -> Failure {
        Failure {
            cause: Box::new(cause),
            exit,
            refusals: Vec::new(),
        }
    }
}

/// A shared shim's failure holds no refusals.
impl From<cli_support::Failure> for Failure {
    fn from(failure: cli_support::Failure) -> Failure {
        Failure {
            cause: failure.cause,
            exit: failure.exit,
            refusals: Vec::new(),
        }
    }
}

/// One command, with the global flags the library's use cases take.
pub type Call<'a> = cli_support::Call<'a, Flags>;

/// A group of nouns another crate owns.
pub trait Extension {
    /// The nouns it adds to the command, each already shaped (its verbs, its
    /// default verb, no `help` command).
    fn nouns(&self) -> Vec<Command>;

    /// Run `noun`. The exit code on success (0, or 3 when fatal findings
    /// followed an answer that was still printed).
    fn run(
        &mut self,
        noun: &str,
        call: &Call<'_>,
        catalog: &Catalog,
        out: &mut dyn Write,
        err: &mut dyn Write,
    ) -> Result<u8, Failure>;
}

/// No extension: the command line holds only this crate's own commands.
pub struct NoExtension;

impl Extension for NoExtension {
    fn nouns(&self) -> Vec<Command> {
        Vec::new()
    }

    fn run(
        &mut self,
        _noun: &str,
        _call: &Call<'_>,
        _catalog: &Catalog,
        _out: &mut dyn Write,
        _err: &mut dyn Write,
    ) -> Result<u8, Failure> {
        Ok(0)
    }
}
