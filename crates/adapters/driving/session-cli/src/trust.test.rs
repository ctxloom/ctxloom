//! The `trust` noun over a fake daemon: the preview first, the human asked
//! at a terminal, `--yes` applying what was shown, and the listing.

use std::cell::RefCell;

use angzarr_presentation::Format;
use clap::FromArgMatches;
use daemon_api::{CallError, TrustWrites, Trusted};
use daemon_proto::v1;

use super::{trust, trust_noun, Asking, Trust, TrustVerb};

const NAME: &str = "projects/calm-otter/trust";

/// A daemon holding one project's trust.
struct Fake {
    trusted: bool,
    /// Each call, in order.
    calls: RefCell<Vec<String>>,
}

impl Fake {
    fn new(trusted: bool) -> Fake {
        Fake {
            trusted,
            calls: RefCell::new(Vec::new()),
        }
    }

    fn calls(&self) -> Vec<String> {
        self.calls.borrow().clone()
    }

    fn trust(&self, trusted: bool) -> v1::Trust {
        v1::Trust {
            name: NAME.to_string(),
            trusted,
            settings: vec![v1::TrustSetting {
                path: "engines.claude-code.command".to_string(),
                value: "/opt/their-claude".to_string(),
            }],
            etag: "e-1".to_string(),
            ..v1::Trust::default()
        }
    }
}

impl TrustWrites for Fake {
    async fn get_trust(&self) -> Result<v1::Trust, CallError> {
        self.calls.borrow_mut().push("get".to_string());
        Ok(self.trust(self.trusted))
    }

    async fn grant_trust(&self, etag: &str, user: &str) -> Result<Trusted, CallError> {
        self.calls.borrow_mut().push(format!("grant {etag} {user}"));
        Ok(Trusted {
            trust: self.trust(true),
            applied: !self.trusted,
        })
    }

    async fn revoke_trust(&self, user: &str) -> Result<Trusted, CallError> {
        self.calls.borrow_mut().push(format!("revoke {user}"));
        Ok(Trusted {
            trust: self.trust(false),
            applied: self.trusted,
        })
    }

    async fn list_trusts(&self) -> Result<v1::ListTrustsResponse, CallError> {
        self.calls.borrow_mut().push("list".to_string());
        Ok(v1::ListTrustsResponse {
            trusts: vec![v1::Trust {
                name: NAME.to_string(),
                trusted: true,
                actor: "alice".to_string(),
                ..v1::Trust::default()
            }],
            ..v1::ListTrustsResponse::default()
        })
    }
}

/// A terminal, or none, answering every question `answer`.
struct Person {
    there: bool,
    answer: bool,
    asked: RefCell<Vec<String>>,
}

fn person(there: bool, answer: bool) -> Person {
    Person {
        there,
        answer,
        asked: RefCell::new(Vec::new()),
    }
}

impl Asking for Person {
    fn is_terminal(&self) -> bool {
        self.there
    }

    fn confirm(&self, question: &str) -> bool {
        self.asked.borrow_mut().push(question.to_string());
        self.answer
    }
}

fn parsed(args: &[&str]) -> Trust {
    let matches = trust_noun()
        .try_get_matches_from(std::iter::once("trust").chain(args.iter().copied()))
        .expect("the line parses");
    Trust::from_arg_matches(&matches).expect("the matches read")
}

fn ran(args: &[&str], fake: &Fake, who: &Person, format: Format) -> (Result<bool, String>, String) {
    let mut out = Vec::new();
    let done = trust(&parsed(args), fake, "alice", who, format, &mut out);
    let out = String::from_utf8(out).unwrap();
    let done = done
        .map(|d| d.would_change)
        .map_err(|f| angzarr_diagnostic::Diagnostic::code(&*f.failure.cause).to_string());
    (done, out)
}

#[test]
fn off_a_terminal_trust_previews_and_changes_nothing_but_would() {
    let fake = Fake::new(false);
    let who = person(false, true);
    let (done, out) = ran(&[], &fake, &who, Format::Json);
    assert_eq!(done, Ok(true));
    assert_eq!(fake.calls(), ["get"]);
    assert!(who.asked.borrow().is_empty());
    let json: serde_json::Value = serde_json::from_str(&out).unwrap();
    assert_eq!(json["name"], NAME);
    assert_eq!(json["settings"][0]["path"], "engines.claude-code.command");
}

#[test]
fn off_a_terminal_a_trusted_project_would_change_nothing() {
    let fake = Fake::new(true);
    let (done, _) = ran(&[], &fake, &person(false, true), Format::Json);
    assert_eq!(done, Ok(false));
}

#[test]
fn with_yes_the_grant_names_the_etag_the_preview_showed() {
    let fake = Fake::new(false);
    let (done, out) = ran(&["--yes"], &fake, &person(false, false), Format::Text);
    assert_eq!(done, Ok(false));
    assert_eq!(fake.calls(), ["get", "grant e-1 alice"]);
    assert_eq!(out, format!("{NAME}: trusted\n"));
}

#[test]
fn at_a_terminal_a_yes_grants_and_a_no_changes_nothing() {
    let fake = Fake::new(false);
    let who = person(true, true);
    let (done, out) = ran(&[], &fake, &who, Format::Text);
    assert_eq!(done, Ok(false));
    assert_eq!(fake.calls(), ["get", "grant e-1 alice"]);
    assert_eq!(who.asked.borrow().len(), 1);
    assert!(
        out.contains("engines.claude-code.command: /opt/their-claude"),
        "{out}"
    );
    assert!(out.ends_with(&format!("{NAME}: trusted\n")), "{out}");

    let fake = Fake::new(false);
    let (done, _) = ran(&[], &fake, &person(true, false), Format::Text);
    assert_eq!(done, Ok(false));
    assert_eq!(fake.calls(), ["get"]);
}

#[test]
fn at_a_terminal_nothing_is_asked_where_nothing_would_change() {
    let fake = Fake::new(true);
    let who = person(true, true);
    let (done, _) = ran(&[], &fake, &who, Format::Text);
    assert_eq!(done, Ok(false));
    assert!(who.asked.borrow().is_empty());
    assert_eq!(fake.calls(), ["get"]);
}

#[test]
fn undo_takes_a_trust_back_with_yes_and_previews_without() {
    let fake = Fake::new(true);
    let (done, out) = ran(
        &["--undo", "--yes"],
        &fake,
        &person(false, false),
        Format::Text,
    );
    assert_eq!(done, Ok(false));
    assert_eq!(fake.calls(), ["get", "revoke alice"]);
    assert_eq!(out, format!("{NAME}: no longer trusted\n"));

    let fake = Fake::new(true);
    let (done, out) = ran(&["--undo"], &fake, &person(false, false), Format::Text);
    assert_eq!(done, Ok(true));
    assert!(out.contains("ctxloom trust --undo --yes"), "{out}");

    let fake = Fake::new(false);
    let (done, _) = ran(&["--undo"], &fake, &person(false, false), Format::Text);
    assert_eq!(done, Ok(false));
}

#[test]
fn the_text_preview_says_what_trusting_allows_and_how_to_give_it() {
    let fake = Fake::new(false);
    let (_, out) = ran(&[], &fake, &person(false, false), Format::Text);
    assert_eq!(
        out,
        format!(
            "{NAME}: not trusted\nTrusting it allows this project's own:\n  engines.claude-code.command: /opt/their-claude\nTo trust it: ctxloom trust --yes\n"
        )
    );
}

#[test]
fn trust_list_lists_the_trusted_projects() {
    let fake = Fake::new(true);
    let (done, out) = ran(&["list"], &fake, &person(false, false), Format::Json);
    assert_eq!(done, Ok(false));
    assert_eq!(fake.calls(), ["list"]);
    let json: serde_json::Value = serde_json::from_str(&out).unwrap();
    assert_eq!(json["trusts"][0]["name"], NAME);
    let (_, out) = ran(&["list"], &fake, &person(false, false), Format::Text);
    assert_eq!(out, format!("{NAME}  by alice\n"));
    assert!(matches!(parsed(&["list"]).verb, Some(TrustVerb::List)));
}

#[test]
fn a_flag_with_list_is_refused_by_the_line() {
    let matches = trust_noun().try_get_matches_from(["trust", "--yes", "list"]);
    assert!(matches.is_err());
}
