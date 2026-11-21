//! The words `session approvals` and `trust` say in text (step 8, the owner's
//! ruling after U4): each a message of this crate's en-US catalog, its id the
//! full code, filled from its args. The layout around them (an option's
//! padded id, a setting's `path: value` line) stays the code's; every word a
//! person reads is the catalog's. Step 8's texts (D14, texts.md §6), accepted verbatim (owner, 2026-11-16; reviewed before release).

use std::fmt;

use angzarr_diagnostic::{code, Arg, Diagnostic};

use crate::error::EN_US;

/// One thing said in text, by its message.
#[derive(Clone, PartialEq, Eq, Debug)]
pub enum Text {
    /// A claude ask's `allow-once`.
    LabelAllowOnce,
    /// A claude ask's `allow-session`, for `rule`.
    LabelAllowSession { rule: String },
    /// A claude ask's `allow-always`, written into the agent's permissions.
    LabelAllowAlways {
        rule: String,
        agent: String,
        engine: String,
    },
    /// A claude ask's `deny`.
    LabelDeny,
    /// `session approvals` with nothing waiting.
    NoneWaiting,
    /// An ask's first line: who asks to run what.
    Asks {
        child: String,
        tool: String,
        input: String,
    },
    /// An ask's name and root, with no expiry.
    AskUnder { ask: String, root: String },
    /// An ask's name and root, and when it is denied.
    AskUnderUntil {
        ask: String,
        root: String,
        seconds: i64,
    },
    /// How an ask is answered.
    AnswerWith { ask: String },
    /// An `allow-always`'s preview, its first line.
    AlwaysWould {
        ask: String,
        rule: String,
        path: String,
    },
    /// An `allow-always`'s preview, how to apply it.
    AlwaysApply { apply: String },
    /// An `allow-always`'s question at a terminal.
    AlwaysQuestion { rule: String, path: String },
    /// An answer sent.
    Answered { ask: String, option: String },
    /// An `allow-always` answer sent, its rule written.
    AnsweredWritten {
        ask: String,
        option: String,
        rule: String,
        path: String,
    },
    /// A project trusted now.
    Trusted { name: String },
    /// A project not trusted.
    NotTrusted { name: String },
    /// A grant of a project trusted already.
    AlreadyTrusted { name: String },
    /// A revoke applied.
    NoLongerTrusted { name: String },
    /// A preview of a project whose config reaches past it in nothing.
    TrustNothing,
    /// A preview's heading over what trusting allows.
    TrustAllows,
    /// How to trust it.
    TrustApply,
    /// How to take the trust back.
    TrustUndoApply,
    /// The question before a grant.
    TrustQuestion,
    /// The question before a revoke.
    TrustUndoQuestion,
    /// `trust list` with nothing trusted.
    NoneTrusted,
    /// A trusted project, by whom.
    TrustedBy { name: String, actor: String },
    /// A trusted project, by whom and since when.
    TrustedBySince {
        name: String,
        actor: String,
        seconds: i64,
    },
}

impl Text {
    /// The en-US text.
    pub fn said(&self) -> String {
        English(self).to_string()
    }
}

/// `text` in this crate's en-US catalog.
struct English<'a>(&'a Text);

impl fmt::Display for English<'_> {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        angzarr_diagnostic::catalog::render(EN_US.1, self.0, f)
    }
}

impl fmt::Display for Text {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        angzarr_diagnostic::display(self, f)
    }
}

impl Diagnostic for Text {
    fn code(&self) -> &str {
        match self {
            Text::LabelAllowOnce => code!("approvals-label-allow-once"),
            Text::LabelAllowSession { .. } => code!("approvals-label-allow-session"),
            Text::LabelAllowAlways { .. } => code!("approvals-label-allow-always"),
            Text::LabelDeny => code!("approvals-label-deny"),
            Text::NoneWaiting => code!("approvals-none-waiting"),
            Text::Asks { .. } => code!("approvals-asks"),
            Text::AskUnder { .. } => code!("approvals-ask-under"),
            Text::AskUnderUntil { .. } => code!("approvals-ask-under-until"),
            Text::AnswerWith { .. } => code!("approvals-answer-with"),
            Text::AlwaysWould { .. } => code!("approvals-always-would"),
            Text::AlwaysApply { .. } => code!("approvals-always-apply"),
            Text::AlwaysQuestion { .. } => code!("approvals-always-question"),
            Text::Answered { .. } => code!("approvals-answered"),
            Text::AnsweredWritten { .. } => code!("approvals-answered-written"),
            Text::Trusted { .. } => code!("trust-trusted"),
            Text::NotTrusted { .. } => code!("trust-not-trusted"),
            Text::AlreadyTrusted { .. } => code!("trust-already-trusted"),
            Text::NoLongerTrusted { .. } => code!("trust-no-longer-trusted"),
            Text::TrustNothing => code!("trust-nothing"),
            Text::TrustAllows => code!("trust-allows"),
            Text::TrustApply => code!("trust-apply"),
            Text::TrustUndoApply => code!("trust-undo-apply"),
            Text::TrustQuestion => code!("trust-question"),
            Text::TrustUndoQuestion => code!("trust-undo-question"),
            Text::NoneTrusted => code!("trust-none"),
            Text::TrustedBy { .. } => code!("trust-by"),
            Text::TrustedBySince { .. } => code!("trust-by-since"),
        }
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        let s = |v: &String| -> Arg { v.as_str().into() };
        match self {
            Text::LabelAllowOnce
            | Text::LabelDeny
            | Text::NoneWaiting
            | Text::TrustNothing
            | Text::TrustAllows
            | Text::TrustApply
            | Text::TrustUndoApply
            | Text::TrustQuestion
            | Text::TrustUndoQuestion
            | Text::NoneTrusted => Vec::new(),
            Text::LabelAllowSession { rule } => vec![("rule", s(rule))],
            Text::LabelAllowAlways {
                rule,
                agent,
                engine,
            } => vec![
                ("rule", s(rule)),
                ("agent", s(agent)),
                ("engine", s(engine)),
            ],
            Text::Asks { child, tool, input } => {
                vec![("child", s(child)), ("tool", s(tool)), ("input", s(input))]
            }
            Text::AskUnder { ask, root } => vec![("ask", s(ask)), ("root", s(root))],
            Text::AskUnderUntil { ask, root, seconds } => vec![
                ("ask", s(ask)),
                ("root", s(root)),
                ("seconds", (*seconds).into()),
            ],
            Text::AnswerWith { ask } => vec![("ask", s(ask))],
            Text::AlwaysWould { ask, rule, path } => {
                vec![("ask", s(ask)), ("rule", s(rule)), ("path", s(path))]
            }
            Text::AlwaysApply { apply } => vec![("apply", s(apply))],
            Text::AlwaysQuestion { rule, path } => vec![("rule", s(rule)), ("path", s(path))],
            Text::Answered { ask, option } => vec![("ask", s(ask)), ("option", s(option))],
            Text::AnsweredWritten {
                ask,
                option,
                rule,
                path,
            } => vec![
                ("ask", s(ask)),
                ("option", s(option)),
                ("rule", s(rule)),
                ("path", s(path)),
            ],
            Text::Trusted { name }
            | Text::NotTrusted { name }
            | Text::AlreadyTrusted { name }
            | Text::NoLongerTrusted { name } => vec![("name", s(name))],
            Text::TrustedBy { name, actor } => vec![("name", s(name)), ("actor", s(actor))],
            Text::TrustedBySince {
                name,
                actor,
                seconds,
            } => vec![
                ("name", s(name)),
                ("actor", s(actor)),
                ("seconds", (*seconds).into()),
            ],
        }
    }
}

/// One value of every text, for the catalog test.
#[cfg(any(test, feature = "test-support"))]
pub fn samples() -> Vec<Box<dyn Diagnostic>> {
    texts()
        .into_iter()
        .map(|text| Box::new(text) as Box<dyn Diagnostic>)
        .collect()
}

/// One of every text.
#[cfg(any(test, feature = "test-support"))]
pub(crate) fn texts() -> Vec<Text> {
    let t = |v: &str| v.to_string();
    vec![
        Text::LabelAllowOnce,
        Text::LabelAllowSession {
            rule: t("Bash(touch hi.txt)"),
        },
        Text::LabelAllowAlways {
            rule: t("Bash(touch hi.txt)"),
            agent: t("default"),
            engine: t("claude-code"),
        },
        Text::LabelDeny,
        Text::NoneWaiting,
        Text::Asks {
            child: t("brisk-copper-moth"),
            tool: t("Bash"),
            input: t("touch hi.txt"),
        },
        Text::AskUnder {
            ask: t("ask-tidy-wren"),
            root: t("amber-quiet-heron"),
        },
        Text::AskUnderUntil {
            ask: t("ask-tidy-wren"),
            root: t("amber-quiet-heron"),
            seconds: 1_800_000_000,
        },
        Text::AnswerWith {
            ask: t("ask-tidy-wren"),
        },
        Text::AlwaysWould {
            ask: t("ask-tidy-wren"),
            rule: t("Bash(touch hi.txt)"),
            path: t(".ctxloom/config.yaml"),
        },
        Text::AlwaysApply {
            apply: t("ctxloom session approvals answer ask-tidy-wren allow-always --yes"),
        },
        Text::AlwaysQuestion {
            rule: t("Bash(touch hi.txt)"),
            path: t(".ctxloom/config.yaml"),
        },
        Text::Answered {
            ask: t("ask-tidy-wren"),
            option: t("allow-once"),
        },
        Text::AnsweredWritten {
            ask: t("ask-tidy-wren"),
            option: t("allow-always"),
            rule: t("Bash(touch hi.txt)"),
            path: t(".ctxloom/config.yaml"),
        },
        Text::Trusted {
            name: t("projects/x/trust"),
        },
        Text::NotTrusted {
            name: t("projects/x/trust"),
        },
        Text::AlreadyTrusted {
            name: t("projects/x/trust"),
        },
        Text::NoLongerTrusted {
            name: t("projects/x/trust"),
        },
        Text::TrustNothing,
        Text::TrustAllows,
        Text::TrustApply,
        Text::TrustUndoApply,
        Text::TrustQuestion,
        Text::TrustUndoQuestion,
        Text::NoneTrusted,
        Text::TrustedBy {
            name: t("projects/x/trust"),
            actor: t("ben"),
        },
        Text::TrustedBySince {
            name: t("projects/x/trust"),
            actor: t("ben"),
            seconds: 1_800_000_000,
        },
    ]
}

#[cfg(test)]
#[path = "text.test.rs"]
mod tests;
