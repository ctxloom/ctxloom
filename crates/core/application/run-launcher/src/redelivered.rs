//! A live session's re-assembled context, as its engine is handed it (step
//! 10c U13, G6/G7; texts.md §1): one framing for every engine, carried by
//! each engine's own `run_ports::Engine::deliver_context`.

use std::fmt;

use coded_diagnostic::{code, Arg, Diagnostic};

use crate::EN_US;

/// The framing line of a re-assembled context: what changed (its bundles,
/// and `config` for a config change; never a path or a value).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ContextRedelivered {
    pub changed: Vec<String>,
}

impl Diagnostic for ContextRedelivered {
    fn code(&self) -> &str {
        code!("context-redelivered")
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        vec![("changed", self.changed.join(", ").into())]
    }
}

impl fmt::Display for ContextRedelivered {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        coded_diagnostic::catalog::render(EN_US.1, self, f)
    }
}

/// `context` (as `context assemble` renders it, its premise index included)
/// re-delivered for what `changed` names: the framing line, a blank line,
/// then the context whole.
pub fn redelivered_context(changed: &[String], context: &str) -> String {
    let framing = ContextRedelivered {
        changed: changed.to_vec(),
    };
    format!("{framing}\n\n{context}")
}

/// The most characters one hook answer gives the engine (owner, 2026-11-18:
/// claude caps a hook's `additionalContext` at 10,000), every part of it:
/// the messages first, then the re-delivered context.
pub const HOOK_CONTEXT_BUDGET: usize = 10_000;

/// A fragment of a re-delivery that changed: its resource name and its
/// body as the library has it now.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ChangedFragment {
    pub name: String,
    pub body: String,
}

/// The framing of a re-delivery too long to give whole: only what changed
/// follows.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ContextChangedOnly {
    pub changed: Vec<String>,
}

impl Diagnostic for ContextChangedOnly {
    fn code(&self) -> &str {
        code!("context-changed-only")
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        vec![("changed", self.changed.join(", ").into())]
    }
}

impl fmt::Display for ContextChangedOnly {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        coded_diagnostic::catalog::render(EN_US.1, self, f)
    }
}

/// The pointer a re-delivery gives when even its changed fragments are too
/// long: `ctxloom context assemble`.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ContextTooLong {
    pub changed: Vec<String>,
}

impl Diagnostic for ContextTooLong {
    fn code(&self) -> &str {
        code!("context-too-long")
    }

    fn args(&self) -> Vec<(&str, Arg)> {
        vec![("changed", self.changed.join(", ").into())]
    }
}

impl fmt::Display for ContextTooLong {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        coded_diagnostic::catalog::render(EN_US.1, self, f)
    }
}

/// `context` re-delivered for what `changed` names within `budget`
/// characters (what the hook's answer has left, step 10c U13 rework): whole
/// ([`redelivered_context`]) when it fits; else a framing line and only
/// the `fragments` that changed, each whole (its name, then its body); else
/// a pointer to `ctxloom context assemble`; else, with no room even for
/// that, nothing. THE part U13 gives a hook's one budget: the wake's
/// `compose_context` (`daemon/s9-wake`) takes it as one more argument.
pub fn redelivered_within(
    changed: &[String],
    context: &str,
    fragments: &[ChangedFragment],
    budget: usize,
) -> String {
    let chars = |text: &str| text.chars().count();
    let whole = redelivered_context(changed, context);
    if chars(&whole) <= budget {
        return whole;
    }
    let mut only = ContextChangedOnly {
        changed: changed.to_vec(),
    }
    .to_string();
    for fragment in fragments {
        only.push_str(&format!("\n\n{}\n{}", fragment.name, fragment.body));
    }
    if !fragments.is_empty() && chars(&only) <= budget {
        return only;
    }
    let pointer = ContextTooLong {
        changed: changed.to_vec(),
    }
    .to_string();
    match chars(&pointer) <= budget {
        true => pointer,
        false => String::new(),
    }
}

#[cfg(test)]
#[path = "redelivered.test.rs"]
mod tests;
