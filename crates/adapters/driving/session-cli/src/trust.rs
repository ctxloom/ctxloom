//! The `trust` noun (step 8 U1, ruling 142): the user's verdict on this
//! project's own settings, previewed first.
//!
//! ```text
//! ctxloom trust [--yes]           (preview, then trust what was shown)
//! ctxloom trust --undo [--yes]
//! ctxloom trust list
//! ```
//!
//! Each is a client of the user's daemon ([`daemon_api::TrustWrites`]). The
//! preview is `GetTrust`: what trusting the project would allow, and the
//! etag of it. At a terminal the human is asked, `[y/N]`, and her yes sends
//! the grant naming that etag; off one the preview is printed and nothing
//! changes (exit 0, 17 under `--detailed-exitcode` when the apply would
//! change something); `--yes` applies at once. The grant is refused
//! `trust-plan-stale` when what the preview showed changed before the yes.

use std::io::Write;

use angzarr_presentation::{write_json, Format};
use clap::{Args, Command, Subcommand};
use daemon_api::{TrustWrites, Trusted};
use daemon_proto::v1;

use crate::error::CliFault;
use crate::run::{call_failure, fault, wrote, Done, Failure};
use crate::text::Text;

/// The `trust` noun, shaped for mounting on the command line.
pub fn trust_noun() -> Command {
    Trust::augment_args(Command::new("trust"))
        .about("Trust this project's own settings that reach past it, once you have looked at them")
        .after_help(
            "Examples:\n  ctxloom trust\n  ctxloom trust --yes\n  ctxloom trust --undo --yes\n  ctxloom trust list",
        )
}

/// The `trust` group: bare, the project's trust previewed and given.
#[derive(Args, Debug)]
#[command(args_conflicts_with_subcommands = true)]
pub struct Trust {
    #[command(subcommand)]
    pub verb: Option<TrustVerb>,
    /// Take the trust back: the project's own settings are held back again
    #[arg(long)]
    pub undo: bool,
    /// Apply what this invocation shows, without asking
    #[arg(short = 'y', long)]
    pub yes: bool,
}

#[derive(Subcommand, Debug)]
pub enum TrustVerb {
    /// List the projects you trust now
    #[command(after_help = "Examples:\n  ctxloom trust list")]
    List,
}

/// The person at the terminal, when there is one: asked yes or no.
pub trait Asking {
    /// Whether a person is there to answer.
    fn is_terminal(&self) -> bool;
    /// Ask `question`, offered as `[y/N]`: only `y` or `yes` is a yes.
    fn confirm(&self, question: &str) -> bool;
}

/// Run `trust`'s command on the user's `writes`, the grant or revoke made by
/// `user` and, at a terminal, asked of `asking` first; its answer to `out`.
pub fn trust<W: TrustWrites>(
    command: &Trust,
    writes: &W,
    user: &str,
    asking: &dyn Asking,
    format: Format,
    out: &mut dyn Write,
) -> Result<Done, Failure> {
    // Time and I/O: the daemon is called over the network.
    let runtime = tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
        .map_err(|e| {
            fault(CliFault::Runtime {
                ext_err_txt: e.to_string(),
            })
        })?;
    runtime.block_on(async {
        if let Some(TrustVerb::List) = command.verb {
            let listed = writes.list_trusts().await.map_err(call_failure)?;
            wrote(match format {
                Format::Json => write_json(out, &listed),
                Format::Text => listed_text(&listed, out),
            })?;
            return Ok(Done::default());
        }
        let shown = writes.get_trust().await.map_err(call_failure)?;
        // What the apply would change: a grant of what is not trusted, or a
        // revoke of what is.
        let would_change = shown.trusted == command.undo;
        if !command.yes {
            wrote(match format {
                Format::Json => write_json(out, &shown),
                Format::Text => preview_text(&shown, command.undo, out),
            })?;
            let question = match command.undo {
                false => Text::TrustQuestion,
                true => Text::TrustUndoQuestion,
            }
            .said();
            if !(would_change && asking.is_terminal() && asking.confirm(&question)) {
                return Ok(Done {
                    would_change: would_change && !asking.is_terminal(),
                    ..Done::default()
                });
            }
        }
        let applied = match command.undo {
            false => writes.grant_trust(&shown.etag, user).await,
            true => writes.revoke_trust(user).await,
        }
        .map_err(call_failure)?;
        wrote(match format {
            Format::Json => write_json(out, &applied.trust),
            Format::Text => applied_text(&applied, out),
        })?;
        Ok(Done::default())
    })
}

/// The preview in text: the trust, and what trusting it allows.
fn preview_text(shown: &v1::Trust, undo: bool, out: &mut dyn Write) -> std::io::Result<()> {
    let name = shown.name.clone();
    let state = match shown.trusted {
        true => Text::Trusted { name },
        false => Text::NotTrusted { name },
    };
    writeln!(out, "{}", state.said())?;
    match shown.settings.is_empty() {
        true => writeln!(out, "{}", Text::TrustNothing.said())?,
        false => {
            writeln!(out, "{}", Text::TrustAllows.said())?;
            for setting in &shown.settings {
                writeln!(out, "  {}: {}", setting.path, setting.value)?;
            }
        }
    }
    match (undo, shown.trusted) {
        (false, false) => writeln!(out, "{}", Text::TrustApply.said()),
        (true, true) => writeln!(out, "{}", Text::TrustUndoApply.said()),
        _ => Ok(()),
    }
}

/// A grant or a revoke, said in text.
fn applied_text(applied: &Trusted, out: &mut dyn Write) -> std::io::Result<()> {
    let name = applied.trust.name.clone();
    let said = match (applied.trust.trusted, applied.applied) {
        (true, true) => Text::Trusted { name },
        (true, false) => Text::AlreadyTrusted { name },
        (false, true) => Text::NoLongerTrusted { name },
        (false, false) => Text::NotTrusted { name },
    };
    writeln!(out, "{}", said.said())
}

/// `trust list` in text: each project trusted, by whom and since when.
fn listed_text(listed: &v1::ListTrustsResponse, out: &mut dyn Write) -> std::io::Result<()> {
    if listed.trusts.is_empty() {
        return writeln!(out, "{}", Text::NoneTrusted.said());
    }
    for trust in &listed.trusts {
        let (name, actor) = (trust.name.clone(), trust.actor.clone());
        let line = match trust.grant_time.as_ref() {
            Some(t) => Text::TrustedBySince {
                name,
                actor,
                seconds: t.seconds,
            },
            None => Text::TrustedBy { name, actor },
        };
        writeln!(out, "{}", line.said())?;
    }
    Ok(())
}

#[cfg(test)]
#[path = "trust.test.rs"]
mod tests;
