//! A first init's companion answers (M4): the shipped companions whose
//! programs are on her machine are offered one by one, then any others she
//! names, as the daemon's preview needs ([`crate::needs`], step 10c U6).
//! The answers come back as data (D2): the terminal lives in the driving
//! adapter alone. Registering each accepted name is the daemon's
//! (`daemon-api`'s init), from [`accepted`].

/// The question that asks for companions by name; its answer is the names,
/// separated by spaces or commas.
pub const COMPANIONS_BY_NAME: &str = "Add companions by name?";

/// The id of the by-name question.
pub const BY_NAME_ID: &str = "companions";

/// The question that offers the shipped companion `name`.
pub fn offer_question(name: &str) -> String {
    format!("Add the {name} companion?")
}

/// One answer: a yes-no question's `yes` or `no`, a words question's line
/// as typed; none where nobody answered (a closed input).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Answer {
    pub id: String,
    pub given: Option<String>,
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

#[cfg(test)]
#[path = "companions.test.rs"]
mod tests;
