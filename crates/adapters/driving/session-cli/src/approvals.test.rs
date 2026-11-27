//! `session approvals` over a fake daemon and a fake config writer: the
//! listing, an answer, and allow-always's order (waiting, written,
//! answered), previewed off a terminal.

use std::cell::RefCell;

use clap::FromArgMatches;
use daemon_api_client::{Answered, ApiRejection, ApprovalAnswers, CallError, Rejection};
use daemon_proto::v1;
use presentation::Format;

use super::{approvals, is_approvals, AlwaysPlan, AlwaysRules};
use crate::{noun, Asking, Failure, Session};

struct Fake {
    approval: v1::Approval,
    /// Settle the ask before the answer reaches it.
    settled_meanwhile: bool,
    calls: RefCell<Vec<String>>,
}

fn waiting(option: (&str, &str, &str)) -> v1::Approval {
    v1::Approval {
        name: "projects/calm-otter/approvals/ask-tidy-wren".to_string(),
        child: "projects/calm-otter/sessions/brisk-copper-moth".to_string(),
        root: "projects/calm-otter/sessions/amber-quiet-heron".to_string(),
        engine: "claude-code".to_string(),
        agent: "default".to_string(),
        tool_name: "Bash".to_string(),
        input_excerpt: "{\"command\":\"rm -rf build/\"}".to_string(),
        state: "waiting".to_string(),
        options: vec![
            v1::ApprovalOption {
                id: "allow-once".to_string(),
                kind: "allow_once".to_string(),
                ..v1::ApprovalOption::default()
            },
            v1::ApprovalOption {
                id: option.0.to_string(),
                kind: option.1.to_string(),
                rule: option.2.to_string(),
                ..v1::ApprovalOption::default()
            },
        ],
        ..v1::Approval::default()
    }
}

fn fake(approval: v1::Approval) -> Fake {
    Fake {
        approval,
        settled_meanwhile: false,
        calls: RefCell::new(Vec::new()),
    }
}

impl ApprovalAnswers for Fake {
    async fn list_approvals(
        &self,
        root: Option<&str>,
    ) -> Result<v1::ListApprovalsResponse, CallError> {
        self.calls
            .borrow_mut()
            .push(format!("list {}", root.unwrap_or("-")));
        Ok(v1::ListApprovalsResponse {
            approvals: vec![self.approval.clone()],
            ..v1::ListApprovalsResponse::default()
        })
    }

    async fn approval(&self, word: &str) -> Result<v1::Approval, CallError> {
        self.calls.borrow_mut().push(format!("approval {word}"));
        Ok(self.approval.clone())
    }

    async fn answer(
        &self,
        approval: &v1::Approval,
        option: &str,
        message: &str,
    ) -> Result<Answered, CallError> {
        self.calls
            .borrow_mut()
            .push(format!("answer {option} {message}"));
        if self.settled_meanwhile {
            let mut settled = approval.clone();
            settled.state = "expired".to_string();
            return Err(daemon_api_client::settled_refusal(&settled).unwrap());
        }
        let mut answered = approval.clone();
        answered.state = "answered".to_string();
        answered.option = option.to_string();
        Ok(Answered {
            approval: answered,
            applied: true,
        })
    }
}

struct Rules<'a>(&'a RefCell<Vec<String>>);

impl AlwaysRules for Rules<'_> {
    fn allow_always(
        &self,
        agent: &str,
        engine: &str,
        rule: &str,
        apply: bool,
    ) -> Result<AlwaysPlan, Failure> {
        self.0
            .borrow_mut()
            .push(format!("rule {agent} {engine} {rule} {apply}"));
        Ok(AlwaysPlan {
            path: "/work/.ctxloom/config.yaml".to_string(),
            key: format!("agents.{agent}.engines.{engine}.permissions"),
            rule: rule.to_string(),
            document: format!("{{\"defaultMode\":\"acceptEdits\",\"allow\":[\"{rule}\"]}}"),
        })
    }
}

struct Person {
    there: bool,
    answer: bool,
}

impl Asking for Person {
    fn is_terminal(&self) -> bool {
        self.there
    }

    fn confirm(&self, _: &str) -> bool {
        self.answer
    }
}

fn parsed(args: &[&str]) -> Session {
    let matches = noun()
        .try_get_matches_from(
            ["session", "approvals"]
                .into_iter()
                .chain(args.iter().copied()),
        )
        .expect("the line parses");
    Session::from_arg_matches(&matches).expect("the matches read")
}

fn ran(args: &[&str], fake: &Fake, who: &Person, format: Format) -> (Result<bool, String>, String) {
    let mut out = Vec::new();
    let done = approvals(
        &parsed(args),
        fake,
        &Rules(&fake.calls),
        who,
        format,
        &mut out,
    );
    let out = String::from_utf8(out).unwrap();
    let done = done
        .map(|d| d.would_change)
        .map_err(|f| coded_diagnostic::Diagnostic::code(&*f.failure.cause).to_string());
    (done, out)
}

const ALWAYS: (&str, &str, &str) = ("allow-always", "allow_always", "Bash(rm -rf build/)");

#[test]
fn approvals_is_its_own_verb() {
    assert!(is_approvals(&parsed(&[])));
    assert!(is_approvals(&parsed(&["answer", "ask-a-b", "deny"])));
}

#[test]
fn bare_it_lists_the_waiting_asks_under_a_root_when_named() {
    let fake = fake(waiting(ALWAYS));
    let (done, out) = ran(
        &["amber-quiet-heron"],
        &fake,
        &Person {
            there: false,
            answer: false,
        },
        Format::Json,
    );
    assert_eq!(done, Ok(false));
    assert_eq!(fake.calls.borrow()[0], "list amber-quiet-heron");
    let json: serde_json::Value = serde_json::from_str(&out).unwrap();
    assert_eq!(json["approvals"][0]["tool_name"], "Bash");
    let (_, text) = ran(
        &[],
        &fake,
        &Person {
            there: false,
            answer: false,
        },
        Format::Text,
    );
    assert!(
        text.contains("brisk-copper-moth asks to run Bash"),
        "{text}"
    );
    assert!(
        text.contains("ask ask-tidy-wren, under amber-quiet-heron"),
        "{text}"
    );
    assert!(text.contains("allow-once      Allow once"), "{text}");
    assert!(
        text.contains("Always allow Bash(rm -rf build/): writes it into agents.default.engines.claude-code.permissions"),
        "{text}"
    );
    assert!(
        text.contains("answer: ctxloom session approvals answer ask-tidy-wren <option>"),
        "{text}"
    );
    assert_eq!(fake.calls.borrow()[1], "list -");
}

#[test]
fn an_empty_listing_says_nothing_waits() {
    let fake = Fake {
        approval: v1::Approval::default(),
        settled_meanwhile: false,
        calls: RefCell::new(Vec::new()),
    };
    struct Empty;
    impl ApprovalAnswers for Empty {
        async fn list_approvals(
            &self,
            _: Option<&str>,
        ) -> Result<v1::ListApprovalsResponse, CallError> {
            Ok(v1::ListApprovalsResponse::default())
        }
        async fn approval(&self, _: &str) -> Result<v1::Approval, CallError> {
            unreachable!()
        }
        async fn answer(&self, _: &v1::Approval, _: &str, _: &str) -> Result<Answered, CallError> {
            unreachable!()
        }
    }
    let mut out = Vec::new();
    approvals(
        &parsed(&[]),
        &Empty,
        &Rules(&fake.calls),
        &Person {
            there: false,
            answer: false,
        },
        Format::Text,
        &mut out,
    )
    .unwrap();
    assert_eq!(String::from_utf8(out).unwrap(), "no ask is waiting\n");
}

#[test]
fn an_answer_names_its_option_and_message_and_writes_nothing() {
    let fake = fake(waiting(ALWAYS));
    let (done, out) = ran(
        &["answer", "brisk-copper-moth", "deny", "not yet"],
        &fake,
        &Person {
            there: false,
            answer: false,
        },
        Format::Text,
    );
    assert_eq!(done, Ok(false));
    assert_eq!(
        *fake.calls.borrow(),
        ["approval brisk-copper-moth", "answer deny not yet"]
    );
    assert_eq!(out, "ask-tidy-wren: answered deny\n");
}

#[test]
fn off_a_terminal_allow_always_previews_and_changes_nothing() {
    let fake = fake(waiting(ALWAYS));
    let (done, out) = ran(
        &["answer", "brisk-copper-moth", "allow-always"],
        &fake,
        &Person {
            there: false,
            answer: true,
        },
        Format::Json,
    );
    assert_eq!(done, Ok(true));
    assert_eq!(
        *fake.calls.borrow(),
        [
            "approval brisk-copper-moth",
            "rule default claude-code Bash(rm -rf build/) false"
        ]
    );
    let json: serde_json::Value = serde_json::from_str(&out).unwrap();
    assert_eq!(json["applied"], false);
    assert_eq!(json["status"], "preview");
    assert_eq!(
        json["apply"],
        "ctxloom session approvals answer ask-tidy-wren allow-always --yes"
    );
    assert_eq!(
        json["key"],
        "agents.default.engines.claude-code.permissions"
    );
    assert!(json["document"].as_str().unwrap().contains("acceptEdits"));
    let (_, text) = ran(
        &["answer", "brisk-copper-moth", "allow-always"],
        &fake,
        &Person {
            there: false,
            answer: true,
        },
        Format::Text,
    );
    assert!(
        text.contains("would write Bash(rm -rf build/) into /work/.ctxloom/config.yaml"),
        "{text}"
    );
}

#[test]
fn allow_always_is_written_then_answered_with_yes_or_a_yes_at_a_terminal() {
    for (args, who) in [
        (
            vec!["answer", "ask-tidy-wren", "allow-always", "--yes"],
            Person {
                there: false,
                answer: false,
            },
        ),
        (
            vec!["answer", "ask-tidy-wren", "allow-always"],
            Person {
                there: true,
                answer: true,
            },
        ),
    ] {
        let fake = fake(waiting(ALWAYS));
        let (done, out) = ran(&args, &fake, &who, Format::Text);
        assert_eq!(done, Ok(false));
        let calls = fake.calls.borrow();
        assert_eq!(calls.last().unwrap(), "answer allow-always ");
        assert!(calls.contains(&"rule default claude-code Bash(rm -rf build/) true".to_string()));
        assert!(
            out.contains("answered allow-always, and Bash(rm -rf build/) written"),
            "{out}"
        );
    }
}

#[test]
fn a_no_at_the_terminal_writes_and_answers_nothing() {
    let fake = fake(waiting(ALWAYS));
    let (done, _) = ran(
        &["answer", "ask-tidy-wren", "allow-always"],
        &fake,
        &Person {
            there: true,
            answer: false,
        },
        Format::Text,
    );
    assert_eq!(done, Ok(false));
    assert!(!fake
        .calls
        .borrow()
        .iter()
        .any(|c| c.starts_with("answer") || c.ends_with("true")));
}

#[test]
fn allow_always_of_a_settled_ask_is_refused_before_anything_is_written() {
    let mut settled = waiting(ALWAYS);
    settled.state = "answered".to_string();
    settled.option = "deny".to_string();
    let fake = fake(settled);
    let (done, _) = ran(
        &["answer", "ask-tidy-wren", "allow-always", "--yes"],
        &fake,
        &Person {
            there: false,
            answer: false,
        },
        Format::Text,
    );
    assert_eq!(done, Err("approvals-approval-settled".to_string()));
    assert_eq!(*fake.calls.borrow(), ["approval ask-tidy-wren"]);
}

#[test]
fn a_rule_written_for_an_ask_settled_meanwhile_stays_and_is_said() {
    let mut fake = fake(waiting(ALWAYS));
    fake.settled_meanwhile = true;
    let (done, _) = ran(
        &["answer", "ask-tidy-wren", "allow-always", "--yes"],
        &fake,
        &Person {
            there: false,
            answer: false,
        },
        Format::Text,
    );
    assert_eq!(
        done,
        Err("daemon-api-contract-approval-rule-kept".to_string())
    );
    let _ = (
        ApiRejection::ApprovalNoneWaiting {
            harp: String::new(),
        },
        Rejection::Api,
    );
}

#[test]
fn an_always_without_a_rule_is_relayed_and_writes_nothing() {
    // ACP's allow_always (Q4): no rule, so nothing written.
    let fake = fake(waiting(("allow-always", "allow_always", "")));
    let (done, _) = ran(
        &["answer", "ask-tidy-wren", "allow-always"],
        &fake,
        &Person {
            there: false,
            answer: false,
        },
        Format::Text,
    );
    assert_eq!(done, Ok(false));
    assert_eq!(
        *fake.calls.borrow(),
        ["approval ask-tidy-wren", "answer allow-always "]
    );
}

#[test]
fn a_settled_answer_without_a_written_rule_is_the_settled_refusal() {
    let mut fake = fake(waiting(ALWAYS));
    fake.settled_meanwhile = true;
    let (done, _) = ran(
        &["answer", "ask-tidy-wren", "allow-once"],
        &fake,
        &Person {
            there: false,
            answer: false,
        },
        Format::Text,
    );
    assert_eq!(done, Err("approvals-approval-settled".to_string()));
}
