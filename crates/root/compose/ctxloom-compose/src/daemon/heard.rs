//! What the serving daemon says of itself that a command tells the person
//! (owner rulings OD-52, OD-54, OD-55): a daemon whose start-up catch-up
//! (or its sweep) failed serves anyway, and one draining hands its runs
//! over; every answer it gives carries those standing warnings (response
//! metadata, [`daemon_proto::WARNING_METADATA`]), and the command's client
//! hears them from the answers it already has ([`hear`], told by
//! `daemon_client::DaemonClient::hearing`): no call is made to ask. Each is
//! said once a process, however many answers carried it, on stderr as the
//! command ends ([`take_heard`]); what the daemon answered still stands.
//! `daemon status` hears nothing: it shows the warnings itself.

use std::sync::Mutex;

use daemon_proto::v1;

/// What this process heard: every warning, once, and those not said yet.
struct Heard {
    /// Every warning heard, said or not: a warning heard again is not
    /// heard twice.
    ever: Vec<v1::Notice>,
    /// Those heard and not said yet.
    unsaid: Vec<v1::Notice>,
}

static HEARD: Mutex<Heard> = Mutex::new(Heard {
    ever: Vec::new(),
    unsaid: Vec::new(),
});

/// Keep `notices`, those this process has not heard before, to be said as
/// the command ends: every answer of the daemon's carries its standing
/// warnings, so most are heard many times.
pub fn hear(notices: Vec<v1::Notice>) {
    if let Ok(mut heard) = HEARD.lock() {
        for notice in notices {
            if !heard.ever.contains(&notice) {
                heard.ever.push(notice.clone());
                heard.unsaid.push(notice);
            }
        }
    }
}

/// What this process heard and has not said yet, taken: the command says
/// each once, on stderr, as it ends, in its own code and args (rendered by
/// the command's catalog).
pub fn take_heard() -> Vec<coded_diagnostic::Report> {
    HEARD
        .lock()
        .map(|mut heard| std::mem::take(&mut heard.unsaid))
        .unwrap_or_default()
        .iter()
        .map(|notice| run_application::client::report_of(&notice.code, &notice.args))
        .collect()
}

#[cfg(test)]
#[path = "heard.test.rs"]
mod tests;
