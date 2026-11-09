//! The session's own `ctxloom`: a link in the session home's `bin` to the
//! running binary, which leads the interactive engine's `PATH`.
//!
//! A hook's command is the bare `ctxloom`, and a different `ctxloom` (the Go
//! one) can come first on the user's `PATH`. The directory is the session's,
//! so the engine, its hooks and the session's own shell all find this one
//! first: shadowing a user's other `ctxloom` there is intended.

use library_application::Notice;
use run_ports::{LinkState, LoginLinks};

/// The link, relative to the session's directory.
pub(crate) const SHIM_REL: &str = "home/bin/ctxloom";

/// The directory the link is in, relative to the session's directory.
pub(crate) const SHIM_DIR_REL: &str = "home/bin";

/// Make the session's `ctxloom` a link to `program`: nothing to do where it
/// already is one to it, a stale link replaced, anything else left as it is.
/// The notice says why the link is not there.
pub(crate) fn ensure_shim(
    links: &mut dyn LoginLinks,
    harp: &str,
    program: &str,
) -> Result<(), Box<Notice>> {
    let unlinked = |text: String| {
        Box::new(Notice::Finding {
        text: format!(
            "the session's {SHIM_REL} could not be made ({text}), so its hooks run the ctxloom first on PATH"
        ),
        remedy: "put this ctxloom first on PATH".to_string(),
    })
    };
    if program.is_empty() {
        return Err(unlinked("the running binary was not found".to_string()));
    }
    match links.state(harp, SHIM_REL) {
        LinkState::LinkTo(held) if held == program => Ok(()),
        LinkState::LinkTo(_) => {
            links
                .unlink(harp, SHIM_REL)
                .map_err(|e| unlinked(e.to_string()))?;
            links
                .link(harp, SHIM_REL, program)
                .map_err(|e| unlinked(e.to_string()))
        }
        LinkState::Absent => links
            .link(harp, SHIM_REL, program)
            .map_err(|e| unlinked(e.to_string())),
        LinkState::Other => Err(unlinked("something else is there".to_string())),
    }
}

/// `PATH` with `dir` first and the inherited one after it.
pub(crate) fn path_with(dir: &str, inherited: Option<&str>) -> String {
    match inherited.filter(|path| !path.is_empty()) {
        Some(path) => format!("{dir}:{path}"),
        None => dir.to_string(),
    }
}
