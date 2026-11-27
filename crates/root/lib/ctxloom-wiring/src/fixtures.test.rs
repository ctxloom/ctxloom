//! The command line's tests' world: the shared one (`ctxloom-compose`'s
//! fixtures) with the program's commands over it.

pub use ctxloom_compose::fixtures::*;

use ctxloom_cli::Extension as _;

/// A [`World`]'s commands: the program's, over its environment as of now.
pub trait WorldCommands {
    fn commands(&self) -> crate::Commands;
}

impl WorldCommands for World {
    fn commands(&self) -> crate::Commands {
        crate::Commands::new(self.env(), VERSION)
    }
}

/// Run `line` through the extension; its exit, stdout and stderr.
pub fn run_line(commands: &mut crate::Commands, line: &[&str]) -> (u8, String, String) {
    try_line(commands, presentation::Format::Json, line)
        .unwrap_or_else(|f| panic!("failed: {}", f.cause))
}

/// Run `line` through the extension, answering in `format`: its exit, stdout
/// and stderr, or the failure it ended in.
pub fn try_line(
    commands: &mut crate::Commands,
    format: presentation::Format,
    line: &[&str],
) -> Result<(u8, String, String), ctxloom_cli::Failure> {
    let program = clap::Command::new("ctxloom")
        .subcommand_required(true)
        .subcommands(commands.nouns());
    let argv = std::iter::once("ctxloom").chain(line.iter().copied());
    let matches = program.try_get_matches_from(argv).unwrap();
    let noun = matches.subcommand_name().unwrap().to_string();
    let call = ctxloom_cli::Call::with_globals(
        &matches,
        Some(format),
        format,
        library_application::Flags::default(),
    );
    let catalog =
        presentation::Catalog::en_us(&[ctxloom_compose::EN_US, crate::EN_US, ctxloom_cli::EN_US])
            .unwrap();
    let (mut out, mut err) = (Vec::new(), Vec::new());
    let code = commands.run(&noun, &call, &catalog, &mut out, &mut err)?;
    Ok((
        code.code(true),
        String::from_utf8(out).unwrap(),
        String::from_utf8(err).unwrap(),
    ))
}
