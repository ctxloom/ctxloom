//! Which urgencies a session takes.

use sessions::Urgency;

/// The urgencies `engine` takes for a session that is (`child`) or is not
/// a child the daemon hosts, and whose ctxloom MCP server does or does not
/// watch it (`wakeable`, the terminal wake): what a send gives as its
/// capability evidence.
pub fn urgencies_of(child: bool, engine: &str, wakeable: bool) -> Vec<Urgency> {
    let state = sessions::SessionState {
        owner: child.then(|| sessions::SessionRef {
            project_id: String::new(),
            session: String::new(),
        }),
        engine: engine.to_string(),
        ..sessions::SessionState::default()
    };
    session_policies::supported(&state, wakeable)
}
