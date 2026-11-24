//! Why a command to an ask, or to an ask name's claim, was refused.

use std::fmt;

use coded_diagnostic::{code, Arg, Diagnostic};

#[derive(Clone, PartialEq, Eq, Debug)]
pub enum ApprovalsRejection {
    /// No ask was asked on the stream.
    ApprovalUnknown { ask_id: String },
    /// The ask was answered, expired or withdrawn already; `how` says which
    /// (`answered (<option>)`, `expired`, `withdrawn`).
    ApprovalSettled { ask_id: String, how: String },
    /// The answer names an option the ask does not offer; `offered` is the
    /// ask's option ids, comma separated, in order.
    OptionNotOffered {
        ask_id: String,
        option: String,
        offered: String,
    },
    /// A message given with the answer to an ask whose engine's answer
    /// carries none (ACP, Q6).
    MessageNotRelayed { ask_id: String, engine: String },
    /// Another ask holds the name.
    NameTaken { ask_id: String },
}

impl fmt::Display for ApprovalsRejection {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        coded_diagnostic::display(self, f)
    }
}

impl Diagnostic for ApprovalsRejection {
    fn code(&self) -> &str {
        match self {
            ApprovalsRejection::ApprovalUnknown { .. } => code!("approval-unknown"),
            ApprovalsRejection::ApprovalSettled { .. } => code!("approval-settled"),
            ApprovalsRejection::OptionNotOffered { .. } => code!("option-not-offered"),
            ApprovalsRejection::MessageNotRelayed { .. } => code!("message-not-relayed"),
            ApprovalsRejection::NameTaken { .. } => code!("name-taken"),
        }
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        match self {
            ApprovalsRejection::ApprovalUnknown { ask_id }
            | ApprovalsRejection::NameTaken { ask_id } => vec![("ask_id", ask_id.as_str().into())],
            ApprovalsRejection::ApprovalSettled { ask_id, how } => vec![
                ("ask_id", ask_id.as_str().into()),
                ("how", how.as_str().into()),
            ],
            ApprovalsRejection::OptionNotOffered {
                ask_id,
                option,
                offered,
            } => vec![
                ("ask_id", ask_id.as_str().into()),
                ("option", option.as_str().into()),
                ("offered", offered.as_str().into()),
            ],
            ApprovalsRejection::MessageNotRelayed { ask_id, engine } => vec![
                ("ask_id", ask_id.as_str().into()),
                ("engine", engine.as_str().into()),
            ],
        }
    }
}

/// This crate's en-US messages, its name first.
pub const EN_US: (&str, &str) = (
    env!("CARGO_PKG_NAME"),
    include_str!("../locales/en-US/messages.ftl"),
);

/// One value of every rejection, for the catalog test.
#[cfg(any(test, feature = "test-support"))]
pub fn diagnostic_samples() -> Vec<Box<dyn Diagnostic>> {
    let ask = || "ask-tidy-wren".to_string();
    vec![
        Box::new(ApprovalsRejection::ApprovalUnknown { ask_id: ask() }),
        Box::new(ApprovalsRejection::ApprovalSettled {
            ask_id: ask(),
            how: "answered (allow-once)".to_string(),
        }),
        Box::new(ApprovalsRejection::OptionNotOffered {
            ask_id: ask(),
            option: "deny-always".to_string(),
            offered: "allow-once, deny".to_string(),
        }),
        Box::new(ApprovalsRejection::MessageNotRelayed {
            ask_id: ask(),
            engine: "opencode".to_string(),
        }),
        Box::new(ApprovalsRejection::NameTaken { ask_id: ask() }),
    ]
}
