//! `ctxloom test watch-library [--home] [--count <n>]` (step 10c G7,
//! QUESTIONS.md 6; test-support only): a client of the daemon's
//! `WatchLibrary`, the RPC the CLI verb waits for. Once the stream is open
//! it prints `{"watching": "<parent>"}`, then each change the daemon sends
//! as one JSON line ([`change_line`]), until `--count` changes are printed
//! or the stream ends; a stream the daemon breaks fails the command.

use std::io::Write;

use daemon_ports::{DaemonApi, Daemons};
use daemon_proto::v1;
use serde_json::{json, Value};

use super::{block_on, called, library_parent, Reach};
use ctxloom_cli::{CliError, Failure};

use crate::Wiring;

/// One change as the watcher prints it: its tree, its cause in a word
/// (`external`, `intended`), its sequence and each path with its status
/// (and a rename's `from_path`); a write's `request_id` and `verb` when it
/// is one. Never content: the change carries none.
pub fn change_line(change: &v1::LibraryChange) -> Value {
    let cause = match v1::library_change::Cause::try_from(change.cause) {
        Ok(v1::library_change::Cause::External) => "external",
        Ok(v1::library_change::Cause::Intended) => "intended",
        _ => "",
    };
    let changes: Vec<Value> = change
        .changes
        .iter()
        .map(|summary| {
            let mut row = json!({"path": summary.path, "status": summary.status});
            if !summary.from_path.is_empty() {
                row["from_path"] = summary.from_path.clone().into();
            }
            row
        })
        .collect();
    let mut line = json!({
        "tree": change.tree,
        "cause": cause,
        "sequence": change.sequence,
        "changes": changes,
    });
    if !change.request_id.is_empty() {
        line["request_id"] = change.request_id.clone().into();
    }
    if !change.verb.is_empty() {
        line["verb"] = change.verb.clone().into();
    }
    line
}

fn output(written: std::io::Result<()>) -> Result<(), Failure> {
    written.map_err(|e| {
        let error = CliError::Output {
            detail: e.to_string(),
        };
        Failure::new(error, cli_support::Exit::FAILED)
    })
}

/// Watch the cwd's project's library (the home's under `home`) through the
/// daemon (started if none serves), printing to `out`.
pub fn watch_library(
    wiring: &Wiring,
    stamp: &str,
    home: bool,
    count: Option<u32>,
    out: &mut dyn Write,
) -> Result<(), Failure> {
    let parent = match home {
        true => "users/me".to_string(),
        false => library_parent(wiring, stamp, crate::Minting::AtRoot)?,
    };
    block_on(async {
        let reach = Reach::new(wiring, stamp);
        let api = reach
            .reach()
            .await
            .map_err(|unreached| called(daemon_api::CallError::Unreached(unreached)))?;
        let mut changes = api
            .watch_library(v1::WatchLibraryRequest {
                parent: parent.clone(),
                caller: Some(super::caller_of(
                    wiring.env(),
                    v1::client::Kind::Cli,
                    reach.stamp(),
                )),
            })
            .await
            .map_err(|call| called(call.into()))?;
        output(writeln!(out, "{}", json!({"watching": parent})).and_then(|()| out.flush()))?;
        let mut printed = 0;
        while count.is_none_or(|count| printed < count) {
            match changes.next().await {
                None => break,
                Some(Err(call)) => return Err(called(call.into())),
                Some(Ok(change)) => {
                    output(writeln!(out, "{}", change_line(&change)).and_then(|()| out.flush()))?;
                    printed += 1;
                }
            }
        }
        Ok(())
    })?
}

#[cfg(test)]
#[path = "watch.test.rs"]
mod tests;
