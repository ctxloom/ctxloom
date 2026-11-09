//! Why a hook did not complete, and what it warned of.

use std::fmt;

use angzarr_diagnostic::{code, Arg, Diagnostic};

#[derive(Clone, PartialEq, Eq, Debug)]
pub enum HookError {
    /// An engine the registry does not know (Go's
    /// `UnknownHookEngineError`): guessing would decode one engine's wire
    /// with another's codec.
    UnknownEngine { engine: String, known: String },
}

impl fmt::Display for HookError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        angzarr_diagnostic::display(self, f)
    }
}

impl Diagnostic for HookError {
    fn code(&self) -> &str {
        match self {
            HookError::UnknownEngine { .. } => code!("unknown-engine"),
        }
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        match self {
            HookError::UnknownEngine { engine, known } => vec![
                ("engine", engine.as_str().into()),
                ("known", known.as_str().into()),
            ],
        }
    }
}

/// What a hook says on its diagnostic channel and still succeeds: stdout is
/// the engine's input and a diagnostic printed there would corrupt it.
#[derive(Clone, PartialEq, Eq, Debug)]
pub enum HookWarning {
    /// There is no harp to name: nothing truthful to emit on stdout.
    NoUsableHarp { harp: String },
    /// An observation the hook could not
    /// record. The engine's answer is unaffected; `what` is the fact's
    /// external id and `why` the delivery's text.
    NotRecorded { what: String, why: String },
}

impl fmt::Display for HookWarning {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        angzarr_diagnostic::display(self, f)
    }
}

impl Diagnostic for HookWarning {
    fn code(&self) -> &str {
        match self {
            HookWarning::NoUsableHarp { .. } => code!("no-usable-harp"),
            HookWarning::NotRecorded { .. } => code!("not-recorded"),
        }
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        match self {
            HookWarning::NoUsableHarp { harp } => vec![("harp", harp.as_str().into())],
            HookWarning::NotRecorded { what, why } => {
                vec![("what", what.as_str().into()), ("why", why.as_str().into())]
            }
        }
    }
}

/// This crate's en-US messages, its name first.
pub const EN_US: (&str, &str) = (
    env!("CARGO_PKG_NAME"),
    include_str!("../locales/en-US/messages.ftl"),
);

/// One value of every error and warning, for the catalog test.
#[cfg(any(test, feature = "test-support"))]
pub fn diagnostic_samples() -> Vec<Box<dyn Diagnostic>> {
    vec![
        Box::new(HookError::UnknownEngine {
            engine: "nope".to_string(),
            known: "claude-code".to_string(),
        }),
        Box::new(HookWarning::NoUsableHarp {
            harp: String::new(),
        }),
        Box::new(HookWarning::NotRecorded {
            what: "hook:prompt:s:p".to_string(),
            why: "the listener is not running".to_string(),
        }),
    ]
}

#[cfg(test)]
#[path = "error.test.rs"]
mod tests;
