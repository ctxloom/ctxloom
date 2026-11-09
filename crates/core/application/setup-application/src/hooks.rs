//! The engines' callbacks, all reached by `hook <engine> execute <native>`
//! ([`Setup::execute_hook`]); the session-start notice and the skill mates
//! (Go's `hook_session_start.go`, `hook_skill_mates.go`,
//! `skill_mates_decide.go`). A hook must never fail the host engine's
//! startup: what it can foresee going wrong is silence, and only a payload it
//! cannot read, or an engine it does not know, is an error.

use library_application::{Flags, PackageRequest};
use surface_ports::{EngineSurfaces, HookCodec, HookEvent, HookResponse};

use crate::{Setup, SetupError};

/// The nudge Go's `operations.AgentSetupNudge` hands a project that has
/// profiles and no agents.
const AGENT_SETUP_NUDGE: &str = "ctxloom: this project has profiles but no agents configured. Run `ctxloom init prompt` (or ask your agent to) to bind engines to profiles - the standard trio is an orchestrator you drive, a containerized developer, and a cheap finder, plus per-language code reviewers and any other roles you want to orchestrate.";

/// The tag that links skills into a group: `ctxloom:link_id=<id>`.
const LINK_TAG: &str = "ctxloom:link_id=";

/// What `hook <engine> execute <name>` answers: the engine's stdout, and what
/// else the caller is to do with the event (the driving edge records the bind,
/// and warns of a session with no harp).
#[derive(Clone, PartialEq, Eq, Debug, Default)]
pub struct HookAnswer {
    /// The engine's wire answer, one JSON line (empty for silence).
    pub stdout: String,
    /// A session start to bind: the harp, and the engine's session.
    pub bind: Option<SessionBind>,
    /// A session start fired with no usable harp: nothing truthful to emit.
    pub harp_missing: bool,
    /// The event the codec decoded, for the
    /// caller that records it. `None` for an answer made without one.
    pub event: Option<HookEvent>,
}

/// The binding a session start asks for.
#[derive(Clone, PartialEq, Eq, Debug, Default)]
pub struct SessionBind {
    pub harp: String,
    pub native_id: String,
    pub transcript: String,
}

/// The harp self-id marker, `<ctxloom name="..." kind="harp" />`: the only
/// index-independent statement of which harp owns a transcript. `None` for
/// nothing it can say truthfully: an empty harp, or one the attribute syntax
/// cannot carry (a `"` or `>` would make it read back as a different harp).
pub fn harp_marker(harp: &str) -> Option<String> {
    if harp.is_empty() || harp.contains(['"', '>']) {
        return None;
    }
    Some(format!(r#"<ctxloom name="{harp}" kind="harp" />"#))
}

impl Setup {
    /// `hook <engine> execute <native>`: the one entry every engine callback
    /// reaches (the owner's ruling, 2026-11-08). The engine is resolved through
    /// the registry (an unknown one is `UnknownHookEngine`, naming the known
    /// ones), the native name is mapped to the unified event by the engine's
    /// codec, and the payload decoded by it. A session start answers the
    /// harp's marker as context plus the session-start notice, and asks for the
    /// bind; a post-tool call answers what `skill_mates` does; every other
    /// event (turn_end, message_display, post_tool_batch, ...) answers
    /// silence. `harp` is the session the hook runs in (may be empty).
    pub fn execute_hook(
        &self,
        engine: &str,
        native: &str,
        payload: &str,
        harp: &str,
    ) -> Result<HookAnswer, SetupError> {
        let codec = self.firing(engine)?.codec();
        let unified = codec.unified_event(native);
        let event = codec.decode(&unified, payload).map_err(SetupError::cause)?;
        let encode =
            |response: &HookResponse| codec.encode(&unified, response).map_err(SetupError::cause);
        match unified.as_str() {
            "session_start" => {
                let marker = harp_marker(harp);
                let usable = marker.is_some();
                let stdout = encode(&HookResponse {
                    context: marker.unwrap_or_default(),
                    notice: self.agent_nudge(),
                    ..HookResponse::default()
                })?;
                Ok(HookAnswer {
                    stdout,
                    bind: usable.then(|| SessionBind {
                        harp: harp.to_string(),
                        native_id: event.native_session.clone(),
                        transcript: event.transcript.clone(),
                    }),
                    harp_missing: !usable,
                    event: Some(event),
                })
            }
            "post_tool" => Ok(HookAnswer {
                stdout: encode(&self.mates_response(codec, &event))?,
                event: Some(event),
                ..HookAnswer::default()
            }),
            _ => Ok(HookAnswer {
                stdout: encode(&HookResponse::default())?,
                event: Some(event),
                ..HookAnswer::default()
            }),
        }
    }

    fn firing(&self, engine: &str) -> Result<&dyn EngineSurfaces, SetupError> {
        self.engines
            .get(engine)
            .ok_or_else(|| SetupError::UnknownHookEngine {
                engine: engine.to_string(),
                known: self.engines.names().join(", "),
            })
    }

    /// The engine's answer to a SessionStart: the session-start notice and
    /// never the project's context, which the session already has as its
    /// system prompt.
    pub fn session_start(&self, engine: &str, payload: &str) -> Result<String, SetupError> {
        let engine = self.firing(engine)?;
        let codec = engine.codec();
        codec
            .decode("session_start", payload)
            .map_err(SetupError::cause)?;
        codec
            .encode(
                "session_start",
                &HookResponse {
                    notice: self.agent_nudge(),
                    ..HookResponse::default()
                },
            )
            .map_err(SetupError::cause)
    }

    /// The session-start notice: the nudge to set up agents when the project
    /// has profiles and none; empty otherwise.
    fn agent_nudge(&self) -> String {
        let flags = Flags {
            degraded: true,
            ..Flags::default()
        };
        let package = self.library.package(&flags, &PackageRequest::default());
        match package.result {
            Ok(p) if !p.has_agents && p.has_profiles => AGENT_SETUP_NUDGE.to_string(),
            _ => String::new(),
        }
    }

    /// The engine's answer to a PostToolUse: when the tool call completed a
    /// skill of a link group, the group's mates the session has not invoked
    /// yet; silence after every other call.
    pub fn skill_mates(&self, engine: &str, payload: &str) -> Result<String, SetupError> {
        let engine = self.firing(engine)?;
        let codec = engine.codec();
        let event = codec
            .decode("post_tool", payload)
            .map_err(SetupError::cause)?;
        let response = self.mates_response(codec, &event);
        codec
            .encode("post_tool", &response)
            .map_err(SetupError::cause)
    }

    /// What a decoded PostToolUse is answered with: the mates line when it
    /// completed a skill, else nothing.
    fn mates_response(&self, codec: &dyn HookCodec, event: &HookEvent) -> HookResponse {
        let mut response = HookResponse::default();
        if !event.skill.is_empty() {
            let transcript = if event.transcript.is_empty() {
                None
            } else {
                self.store.read_absolute(&event.transcript).ok().flatten()
            };
            let invoked = transcript
                .and_then(|b| String::from_utf8(b).ok())
                .map(|t| codec.invoked_skills(&t))
                .unwrap_or_default();
            response.context = self.mates_line(&event.skill, &invoked);
        }
        response
    }

    /// The one line the hook injects: the completed skill and the mates it
    /// leaves uninvoked, names only. Empty when there are none.
    fn mates_line(&self, completed: &str, invoked: &[String]) -> String {
        let flags = Flags {
            degraded: true,
            ..Flags::default()
        };
        let Ok(package) = self
            .library
            .package(&flags, &PackageRequest::default())
            .result
        else {
            return String::new();
        };
        let links = |tags: &[String]| -> Vec<String> {
            tags.iter()
                .filter_map(|t| t.strip_prefix(LINK_TAG).map(str::to_string))
                .collect()
        };
        let mut mates: Vec<String> = Vec::new();
        for skill in package.skills.iter().filter(|s| s.name == completed) {
            let ids = links(&skill.tags);
            for other in &package.skills {
                let shares = links(&other.tags).iter().any(|id| ids.contains(id));
                if other.bundle == skill.bundle
                    && other.name != completed
                    && shares
                    && !invoked.contains(&other.name)
                    && !mates.contains(&other.name)
                {
                    mates.push(other.name.clone());
                }
            }
        }
        mates.sort();
        if mates.is_empty() {
            return String::new();
        }
        format!(
            "Skill {completed} completed; linked skills not yet invoked this session: {}",
            mates.join(", ")
        )
    }
}

#[cfg(test)]
#[path = "hooks.test.rs"]
mod tests;
