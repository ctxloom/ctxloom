//! What a runtime enforces of a run's posture (owner rulings 59, 109): the
//! restrictive `permissions.network` and `permissions.sandbox` words a launch
//! on it really holds the agent to. A run that claims more than this is
//! refused, so the table says only what the launch does, never what it will.
//!
//! Today every run is launched on the host, whatever runtime it names (the
//! container rows of [`crate::bind_for`] are not wired to a launch), and the
//! host fences nothing: no runtime enforces any restrictive word. A runtime
//! that starts passing, say, `--network none` adds its word here, with the
//! launch that enforces it.

/// The restrictive words a runtime enforces.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Enforced {
    /// The `network` words it enforces (`none`).
    pub network: Vec<String>,
    /// The `sandbox` words it enforces (`read-only`, `workspace-write`).
    pub sandbox: Vec<String>,
}

/// What `runtime` enforces: nothing, for every runtime today.
// Equivalent to `Default::default()` while no runtime enforces a word (the
// test pins that); the first enforcing runtime gives it a row to mutate.
#[cfg_attr(test, mutants::skip)]
pub fn enforced(runtime: &str) -> Enforced {
    let _ = runtime;
    Enforced::default()
}

#[cfg(test)]
#[path = "posture.test.rs"]
mod tests;
