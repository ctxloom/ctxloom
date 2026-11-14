//! A first init's companion questions (M4): the shipped companions whose
//! programs are on her machine offered one by one, then any others she
//! names. The questions are data at this port and the answers come back as
//! data (D2): the terminal lives in the driving adapter alone, so a daemon's
//! typed Need and a continuation map onto the same two calls. Each companion
//! she accepts or names is registered exactly as `companion add` registers it
//! (the [`Companions`] port); one whose program is not on her machine is
//! refused, said, and the others are still added.

use ctxloom_ports::Failure;

/// What init needs of the companion registration.
pub trait Companions {
    /// The companions ctxloom ships whose programs are on the caller's
    /// machine, in the order they are offered: looked for, never run.
    fn shipped_available(&self) -> Vec<String>;

    /// `name` registered as `companion add <name>` registers it.
    fn add(&self, name: &str) -> Result<Added, NotAdded>;
}

/// A registration that was made, or was there already.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Added {
    New,
    Already,
}

/// Why a companion was not registered.
#[derive(Debug)]
pub enum NotAdded {
    /// Its program is not on her machine: the program looked for.
    Missing { program: String },
    /// Anything else: why.
    Failed(Failure),
}

/// The question that asks for companions by name; its answer is the names,
/// separated by spaces or commas.
pub const COMPANIONS_BY_NAME: &str = "Add companions by name?";

/// The id of the by-name question.
pub const BY_NAME_ID: &str = "companions";

/// The question that offers the shipped companion `name`.
pub fn offer_question(name: &str) -> String {
    format!("Add the {name} companion?")
}

/// How a question is answered.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Asked {
    /// Yes or no, yes suggested (`[Y/n]`).
    YesNo,
    /// Words, none suggested.
    Words,
}

/// One question, as data.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Question {
    /// Keys its answer.
    pub id: String,
    /// What she is asked.
    pub prompt: String,
    pub asked: Asked,
}

/// One answer: a yes-no question's `yes` or `no`, a words question's line
/// as typed; none where nobody answered (a closed input).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Answer {
    pub id: String,
    pub given: Option<String>,
}

/// What came of the companion answers, each name once, in the order asked.
#[derive(Debug, Default)]
pub struct CompanionsAnswered {
    /// Registered now, or already registered.
    pub added: Vec<(String, Added)>,
    /// Not registered: its program is not on her machine (name, program).
    pub missing: Vec<(String, String)>,
    /// Not registered for another reason.
    pub failed: Vec<(String, Failure)>,
}

/// The id of the offer of `name`.
fn offer_id(name: &str) -> String {
    format!("companion:{name}")
}

/// The companion questions of a first init: an offer of each shipped
/// companion on her machine, then the question by name.
pub fn questions(companions: &dyn Companions) -> Vec<Question> {
    let mut asked: Vec<Question> = companions
        .shipped_available()
        .iter()
        .map(|name| Question {
            id: offer_id(name),
            prompt: offer_question(name),
            asked: Asked::YesNo,
        })
        .collect();
    asked.push(Question {
        id: BY_NAME_ID.to_string(),
        prompt: COMPANIONS_BY_NAME.to_string(),
        asked: Asked::Words,
    });
    asked
}

/// The names `answers` accept, each once, in the order asked: an offer
/// answered `yes` (any case), then the names typed for the by-name question.
pub fn accepted(answers: &[Answer]) -> Vec<String> {
    let mut names: Vec<String> = Vec::new();
    let mut push = |name: &str| {
        if !name.is_empty() && !names.iter().any(|n| n == name) {
            names.push(name.to_string());
        }
    };
    for answer in answers {
        let given = answer.given.as_deref().map(str::trim).unwrap_or("");
        if let Some(name) = answer.id.strip_prefix("companion:") {
            if given.eq_ignore_ascii_case("yes") {
                push(name);
            }
        } else if answer.id == BY_NAME_ID {
            for name in given.split([' ', ',', '\t']) {
                push(name);
            }
        }
    }
    names
}

/// Each accepted or named companion registered through `companions`.
pub fn register(companions: &dyn Companions, answers: &[Answer]) -> CompanionsAnswered {
    let mut done = CompanionsAnswered::default();
    for name in accepted(answers) {
        match companions.add(&name) {
            Ok(added) => done.added.push((name, added)),
            Err(NotAdded::Missing { program }) => done.missing.push((name, program)),
            Err(NotAdded::Failed(why)) => done.failed.push((name, why)),
        }
    }
    done
}

#[cfg(test)]
#[path = "companions.test.rs"]
mod tests;
