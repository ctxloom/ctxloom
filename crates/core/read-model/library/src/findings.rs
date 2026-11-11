//! What a read noticed on the way and did not stop for: a profile's parent
//! that is not there, a fragment that did not load, a variable nobody
//! defined. The caller says them (as warnings, with the remedy) and, for the
//! ones that fail a launch, refuses the run unless `--degraded`.

/// Whether a finding fails a launch.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Class {
    /// Fatal to a launch (and to its preview) unless `--degraded`; Go's
    /// `strictness.Fail`.
    Fail,
    /// A warning only (Go's `clidiag.Warn`).
    Warn,
}

/// One finding.
#[derive(Clone, PartialEq, Eq, Debug)]
pub struct Finding {
    /// What kind of thing was wrong: `bundle`, `ref`, ...
    pub kind: String,
    pub text: String,
    /// What to do about it; empty for none.
    pub remedy: String,
    pub class: Class,
    /// What the finding is about (a bundle's reference), empty for nothing
    /// named: a warning about a thing a failing finding of its kind is also
    /// about says it again, and [`said`] leaves it out.
    pub about: String,
}

impl Finding {
    /// A finding that fails a launch.
    pub fn fail(kind: &str, text: impl Into<String>, remedy: impl Into<String>) -> Finding {
        Finding {
            kind: kind.to_string(),
            text: text.into(),
            remedy: remedy.into(),
            class: Class::Fail,
            about: String::new(),
        }
    }

    /// A finding that only warns.
    pub fn warn(kind: &str, text: impl Into<String>) -> Finding {
        Finding {
            kind: kind.to_string(),
            text: text.into(),
            remedy: String::new(),
            class: Class::Warn,
            about: String::new(),
        }
    }

    /// The finding, about `subject`.
    #[must_use]
    pub fn about(mut self, subject: impl Into<String>) -> Finding {
        self.about = subject.into();
        self
    }
}

/// The findings worth saying: each but a warning about what a failing
/// finding of its kind is also about (the failure says it).
pub fn said(findings: &[Finding]) -> Vec<&Finding> {
    findings.iter().collect()
}

#[cfg(test)]
#[path = "findings.test.rs"]
mod tests;
