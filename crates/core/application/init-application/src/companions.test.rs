//! init.feature's companion scenarios at the application port (D2: the
//! questions out, the answers in, as data).

use super::*;

fn answer(id: &str, given: Option<&str>) -> Answer {
    Answer {
        id: id.to_string(),
        given: given.map(ToString::to_string),
    }
}

#[test]
fn an_offer_answered_yes_and_the_names_typed_are_accepted_each_once() {
    let answers = [
        answer("companion:ltk", Some("yes")),
        answer("companion:reprise", Some("no")),
        answer("companion:taskloom", Some("YES")),
        answer("companions", Some(" acme, ltk  beta ")),
        answer("output", Some("anything")),
    ];
    assert_eq!(accepted(&answers), ["ltk", "taskloom", "acme", "beta"]);
    assert!(accepted(&[answer("companions", None), answer("companion:ltk", None)]).is_empty());
}
