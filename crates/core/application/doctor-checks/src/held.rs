//! `HELD-FACTS-h1` (s4-Q14, unit H4): the capture facts this home's holds
//! keep, which no daemon has recorded yet, read through the holds' port
//! (never the store, never the daemon).
//!
//! - `ok`: nothing is held;
//! - `info`: facts a daemon of their version (or newer) takes, held for
//!   less than an hour: the next ctxloom command that reaches one delivers
//!   them;
//! - `warn`: a hold doctor cannot read, or one with facts held more than an
//!   hour, facts of a NEWER ctxloom than this one (no daemon of this
//!   version takes them: a downgrade, owner H-Q7 (a), kept until a
//!   reinstall or the session's destroy), facts dropped past its bound
//!   (gaps), set-aside parts (what a flush could never record), a format
//!   this ctxloom cannot read, or a torn or corrupt tail.
//!
//! Each hold is named by its session's harp (its first fact's session),
//! else its root, with its count, size, stamps and oldest instant. Doctor
//! removes no hold: `--fix` leaves them (owner H-Q7 (a)).

use run_ports::{up_to_date, HoldError, HoldSummary};

use crate::checks::{Check, Status};

/// The row's marker.
pub const MARKER: &str = "DOCTOR-CHECK-HELD-FACTS-h1";

/// The row's name.
const NAME: &str = "held facts";

/// How long a fact may be held before doctor warns of it: an hour.
pub const STALE_AFTER_SECONDS: i64 = 3_600;

/// What to do about a hold doctor warns of. Doctor removes none.
const REMEDY: &str = "run a ctxloom command of the version that held them (or newer) to deliver \
                      them; a hold of a session you no longer want goes with `ctxloom session \
                      remove`; doctor removes no hold";

/// The row for `holds` (the holds' survey, or why it could not be read),
/// judged by this ctxloom's stamp `own` at `now` (seconds since the epoch).
pub fn held_facts(holds: &Result<Vec<HoldSummary>, HoldError>, own: &str, now: i64) -> Check {
    let holds = match holds {
        Ok(holds) => holds,
        Err(error) => {
            return row(
                Status::Warn,
                format!("the holds cannot be read: {error}"),
                REMEDY,
            )
        }
    };
    if holds.is_empty() {
        return row(Status::Ok, "no fact is held".to_string(), "");
    }
    let mut warns = false;
    let lines: Vec<String> = holds
        .iter()
        .map(|hold| {
            let (line, warned) = described(hold, own, now);
            warns |= warned;
            line
        })
        .collect();
    let status = if warns { Status::Warn } else { Status::Info };
    let remedy = if warns { REMEDY } else { "" };
    row(status, lines.join("; "), remedy)
}

/// `hold` in a few words, and whether it is to be warned of.
fn described(hold: &HoldSummary, own: &str, now: i64) -> (String, bool) {
    let name = hold
        .session
        .as_deref()
        .and_then(|session| session.rsplit('/').next())
        .filter(|harp| !harp.is_empty())
        .map_or_else(|| hold.root.to_string(), str::to_string);
    let mut said = vec![format!(
        "{name}: {} fact(s), {} byte(s)",
        hold.facts, hold.bytes
    )];
    if !hold.stamps.is_empty() {
        said.push(format!("held by {}", hold.stamps.join(", ")));
    }
    if let Some(oldest) = hold.oldest {
        said.push(format!(
            "the oldest at {}",
            session_index::stamp::rfc3339(oldest.into())
        ));
    }
    let mut warnings = Vec::new();
    if hold
        .oldest
        .is_some_and(|oldest| now - oldest.seconds > STALE_AFTER_SECONDS)
    {
        warnings.push("held more than an hour".to_string());
    }
    let newer: Vec<&str> = hold
        .stamps
        .iter()
        .filter(|stamp| !up_to_date(own, stamp))
        .map(String::as_str)
        .collect();
    if !newer.is_empty() {
        warnings.push(format!(
            "held by a newer ctxloom ({}) than this one ({own}), which no daemon of this version takes",
            newer.join(", ")
        ));
    }
    if hold.gaps > 0 {
        warnings.push(format!("{} fact(s) dropped past its bound", hold.gaps));
    }
    if hold.set_aside > 0 {
        warnings.push(format!(
            "{} set-aside part(s) a flush could never record",
            hold.set_aside
        ));
    }
    if hold.unreadable {
        warnings.push("in a format this ctxloom cannot read".to_string());
    }
    if hold.bad_tail {
        warnings.push("a bad tail (a torn append or corruption)".to_string());
    }
    let warned = !warnings.is_empty();
    said.extend(warnings);
    (said.join(", "), warned)
}

/// The row: `status`, `detail` and `remedy`.
fn row(status: Status, detail: String, remedy: &str) -> Check {
    Check {
        marker: MARKER.to_string(),
        name: NAME.to_string(),
        status,
        detail,
        remedy: remedy.to_string(),
    }
}
