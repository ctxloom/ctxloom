//! `engine list|show <engine>`: the read-only noun of the programs ctxloom
//! drives, each with the settings its agents will use and the layer that said
//! each.

/// The engines a person is told of: the public set, in name order.
#[cfg(not(feature = "test-support"))]
pub(crate) fn listed() -> Vec<&'static str> {
    crate::agents::ENGINES
        .iter()
        .copied()
        .filter(|engine| !engine.starts_with("mock-"))
        .collect()
}

/// The engines a person is told of: every one, the test engines
/// (`mock-launch`, `mock-lossy`, `mock-noskills`) included under
/// `test-support`.
#[cfg(feature = "test-support")]
pub(crate) fn listed() -> Vec<&'static str> {
    crate::agents::ENGINES.to_vec()
}
