//! A first init's questions as the daemon's preview needs (step 10c U6, D2,
//! QUESTIONS.md 2): where a session's outputs go, when conditional guidance
//! loads, then the companions. Each is OPTIONAL: off a terminal nobody
//! answers, each suggestion stands unwritten, and what only an answer would
//! write is not written. The apply reads its answers back here.

use crate::companions::{offer_question, BY_NAME_ID, COMPANIONS_BY_NAME};
use crate::{
    OUTPUT_DIR_QUESTION, OUTPUT_DIR_SUGGESTED, PREMISED_ALWAYS, PREMISED_QUESTION,
    PREMISED_SUGGESTED,
};

/// The id of the output folder's question.
pub const OUTPUT_DIR_ID: &str = "output-dir";

/// The id of the conditional guidance question.
pub const PREMISED_ID: &str = "premised";

/// How a need is answered.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Kind {
    /// A value typed, `suggestion` taken when none is.
    Ask { suggestion: String },
    /// One of `choices`.
    Choose { choices: Vec<String> },
    /// Yes or no.
    Confirm,
}

/// One need of init's preview, every one optional.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Need {
    pub id: String,
    pub text: String,
    pub kind: Kind,
}

/// An answer to one need, as the apply carries it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Given {
    Text(String),
    Chosen(String),
    Confirmed(bool),
}

/// What the answers write in the new project's config: each only where it
/// was answered.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Chosen {
    pub output_dir: Option<String>,
    pub premised: Option<String>,
}

/// init's needs, in the order they are put: the output folder, the
/// conditional guidance, an offer of each shipped companion `available` on
/// the caller's machine, then companions by name.
pub fn needs(available: &[String]) -> Vec<Need> {
    let mut asked = vec![
        Need {
            id: OUTPUT_DIR_ID.to_string(),
            text: OUTPUT_DIR_QUESTION.to_string(),
            kind: Kind::Ask {
                suggestion: OUTPUT_DIR_SUGGESTED.to_string(),
            },
        },
        Need {
            id: PREMISED_ID.to_string(),
            text: PREMISED_QUESTION.to_string(),
            kind: Kind::Choose {
                choices: vec![PREMISED_SUGGESTED.to_string(), PREMISED_ALWAYS.to_string()],
            },
        },
    ];
    asked.extend(available.iter().map(|name| Need {
        id: format!("companion:{name}"),
        text: offer_question(name),
        kind: Kind::Confirm,
    }));
    asked.push(Need {
        id: BY_NAME_ID.to_string(),
        text: COMPANIONS_BY_NAME.to_string(),
        kind: Kind::Ask {
            suggestion: String::new(),
        },
    });
    asked
}

/// What `answers` (by need id) choose for the config.
pub fn chosen(answers: &[(String, Given)]) -> Chosen {
    let mut chosen = Chosen::default();
    for (id, given) in answers {
        match (id.as_str(), given) {
            (OUTPUT_DIR_ID, Given::Text(text)) => {
                let dir = text.trim();
                chosen.output_dir = Some(if dir.is_empty() {
                    OUTPUT_DIR_SUGGESTED.to_string()
                } else {
                    dir.to_string()
                });
            }
            (PREMISED_ID, Given::Chosen(word)) => {
                let word = word.trim().to_lowercase();
                chosen.premised = Some(if word == PREMISED_ALWAYS {
                    PREMISED_ALWAYS.to_string()
                } else {
                    PREMISED_SUGGESTED.to_string()
                });
            }
            _ => {}
        }
    }
    chosen
}

#[cfg(test)]
#[path = "needs.test.rs"]
mod tests;
