//! The setup reads as the API says them (step 10c U5): doctor's report and
//! `auth status`'s rows as their messages, and back. The daemon says its
//! answers in these; the CLI reads them back into the reports it has
//! always printed, so the JSON and the text are unchanged.

use auth_application::Row;
use daemon_proto::v1;
use doctor_checks::{Check, Report, Status};

/// A row's verdict as the API says it: its JSON word.
pub fn status_word(status: Status) -> &'static str {
    match status {
        Status::Ok => "ok",
        Status::Warn => "warn",
        Status::Fail => "fail",
        Status::Info => "info",
    }
}

/// The verdict the API's `word` says; a word no verdict has is a stated
/// limit (`info`), never a pass.
pub fn status_of(word: &str) -> Status {
    match word {
        "ok" => Status::Ok,
        "warn" => Status::Warn,
        "fail" => Status::Fail,
        _ => Status::Info,
    }
}

/// Doctor's report as the API says it.
pub fn checks(report: &Report) -> Vec<v1::DoctorCheck> {
    report
        .checks
        .iter()
        .map(|check| v1::DoctorCheck {
            marker: check.marker.clone(),
            name: check.name.clone(),
            status: status_word(check.status).to_string(),
            detail: check.detail.clone(),
            remedy: check.remedy.clone(),
        })
        .collect()
}

/// The report the API's `checks` say.
pub fn report(checks: &[v1::DoctorCheck]) -> Report {
    Report {
        checks: checks
            .iter()
            .map(|check| Check {
                marker: check.marker.clone(),
                name: check.name.clone(),
                status: status_of(&check.status),
                detail: check.detail.clone(),
                remedy: check.remedy.clone(),
            })
            .collect(),
    }
}

/// `auth status`'s rows as the API says them.
pub fn auth_rows(rows: &[Row]) -> Vec<v1::AuthStatusRow> {
    rows.iter()
        .map(|row| v1::AuthStatusRow {
            engine: row.engine.clone(),
            mode: row.mode.clone(),
            present: row.present,
            vars: row.vars.clone(),
            via: row.via.clone(),
            message: row.message.clone(),
            remedy: row.remedy.clone(),
            usable_by: row.usable_by.clone(),
        })
        .collect()
}

/// The rows the API's `rows` say.
pub fn rows_of(rows: &[v1::AuthStatusRow]) -> Vec<Row> {
    rows.iter()
        .map(|row| Row {
            engine: row.engine.clone(),
            mode: row.mode.clone(),
            present: row.present,
            vars: row.vars.clone(),
            via: row.via.clone(),
            message: row.message.clone(),
            remedy: row.remedy.clone(),
            usable_by: row.usable_by.clone(),
        })
        .collect()
}

#[cfg(test)]
#[path = "wire.test.rs"]
mod tests;
