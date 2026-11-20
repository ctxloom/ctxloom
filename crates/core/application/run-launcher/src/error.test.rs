use crate::{diagnostic_samples, EN_US};

#[test]
fn every_diagnostic_has_a_message_using_exactly_its_args() {
    let (name, catalog) = EN_US;
    assert_eq!(name, "run-launcher");
    for sample in diagnostic_samples() {
        let code = sample.code();
        assert!(code.starts_with("run-launcher-"), "{code} is prefixed");
        let line = catalog
            .lines()
            .find(|l| l.starts_with(&format!("{code} =")))
            .unwrap_or_else(|| panic!("{code} has no message"));
        let mut args: Vec<&str> = sample.args().into_iter().map(|(n, _)| n).collect();
        args.sort_unstable();
        let mut used: Vec<&str> = line
            .split('$')
            .skip(1)
            .map(|rest| {
                rest.split(|c: char| !(c.is_ascii_alphanumeric() || c == '_' || c == '-'))
                    .next()
                    .unwrap()
            })
            .collect();
        used.sort_unstable();
        used.dedup();
        assert_eq!(args, used, "{code}");
    }
}

// Every code of the catalog has a sample, and a displayed error is its code
// and args.
#[test]
fn the_samples_hold_a_value_of_every_code_and_each_displays_as_its_code() {
    let samples = diagnostic_samples();
    let codes: Vec<&str> = samples.iter().map(|s| s.code()).collect();
    for want in ["catch-up", "record", "fact", "bad-session"] {
        assert!(
            codes.contains(&format!("run-launcher-{want}").as_str()),
            "{want}"
        );
    }
    assert_eq!(
        crate::LaunchError::Fact { reason: "r".into() }.to_string(),
        "run-launcher-fact reason=r"
    );
}
