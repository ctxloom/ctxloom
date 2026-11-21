//! The engines' callbacks, all reached by `hook <engine> execute <native>`
//! ([`Setup::execute_hook`]); the session-start notice and the skill mates
//! (Go's `hook_session_start.go`, `hook_skill_mates.go`,
//! `skill_mates_decide.go`). A hook must never fail the host engine's
//! startup: what it can foresee going wrong is silence, and only a payload it
//! cannot read, or an engine it does not know, is an error.

use surface_ports::{EngineSurfaces, HookCodec, HookEvent, HookResponse};

use crate::delivery::{compose_context, remaining};
use crate::{Setup, SetupError};

/// The nudge Go's `operations.AgentSetupNudge` hands a project that has
/// profiles and no agents.
const AGENT_SETUP_NUDGE: &str = "ctxloom: this project has profiles but no agents configured. Run `ctxloom init prompt` (or ask your agent to) to bind engines to profiles - the standard trio is an orchestrator you drive, a containerized developer, and a cheap finder, plus per-language code reviewers and any other roles you want to orchestrate.";

/// The tag that links skills into a group: `ctxloom:link_id=<id>`.
const LINK_TAG: &str = "ctxloom:link_id=";

/// A skill as a hook judges its mates by (O3): its name, the bundle it is
/// in (any spelling that is the same for two skills of one bundle), and its
/// tags.
#[derive(Clone, PartialEq, Eq, Debug, Default)]
pub struct HookSkill {
    pub name: String,
    pub bundle: String,
    pub tags: Vec<String>,
}

/// The library as an engine's hooks read it (step 10a U2, O3, owner ruling
/// 2026-11-17): THROUGH THE DAEMON, within the hook's bound, degraded (a
/// config a strict load refuses must not silence a hook). None, when no
/// daemon answered in time (or the read failed): the hook then says
/// nothing of the library, and never reads it in process.
pub trait HookLibrary {
    /// Whether the project has agents, and whether it has profiles
    /// (`ListAgents`, `ListProfiles`).
    fn agents_and_profiles(&self) -> Option<(bool, bool)>;

    /// The project's skills (`ListSkills`).
    fn skills(&self) -> Option<Vec<HookSkill>>;
}

/// The skills a `ListSkills` answer lists, as a hook judges them: each by
/// its `display_name` (its SKILL.md frontmatter name, which the engine
/// reports it by; owner ruling 2026-11-17), else its resource name's last
/// segment, its bundle the name before
/// `/skills/` (`projects/{p}/bundles/{b}`, a remote's or a companion's
/// alike), its tags as listed.
pub fn hook_skills(listed: &daemon_proto::v1::ListSkillsResponse) -> Vec<HookSkill> {
    listed
        .skills
        .iter()
        .filter_map(|skill| {
            let (bundle, id) = skill.name.rsplit_once("/skills/")?;
            let name = match skill.display_name.as_str() {
                "" => id,
                display => display,
            };
            Some(HookSkill {
                name: name.to_string(),
                bundle: bundle.to_string(),
                tags: skill.tags.clone(),
            })
        })
        .collect()
}

/// What `hook <engine> execute <name>` answers: the engine's stdout, and what
/// else the caller is to do with the event (the driving edge records what it
/// observed, a session start's bind among it, and warns of a session with no
/// harp).
#[derive(Clone, PartialEq, Eq, Debug, Default)]
pub struct HookAnswer {
    /// The engine's wire answer, one JSON line (empty for silence).
    pub stdout: String,
    /// A session start fired with no usable harp: nothing truthful to emit.
    pub harp_missing: bool,
    /// The event the codec decoded, for the
    /// caller that records it. `None` for an answer made without one.
    pub event: Option<HookEvent>,
    /// What to acknowledge to the daemon once `stdout` is printed and
    /// flushed (message delivery, 2026-11-19); empty: nothing.
    pub offered: crate::Offered,
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
    /// harp's marker as context plus the session-start notice (its bind is an
    /// observation of its event, recorded like the rest); a post-tool call answers what `skill_mates` does; every other
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
                    harp_missing: !usable,
                    event: Some(event),
                    ..HookAnswer::default()
                })
            }
            // The terminal wake: a prompt delivers the informs and wakes
            // (a stale wake line blocked) and what the session is owed (step
            // 10c U13), in ONE daemon call (the lean hook RPC), which joins
            // them by `compose_context` within the one budget.
            "turn_start" => {
                let (carried, offered, block) =
                    self.prompt_delivery(&event.prompt, native, remaining(&[]));
                let response = HookResponse {
                    context: compose_context(&[&carried]),
                    block,
                    reason: match block {
                        true => crate::hooks_wake::STALE_WAKE.to_string(),
                        false => String::new(),
                    },
                    ..HookResponse::default()
                };
                Ok(HookAnswer {
                    stdout: encode(&response)?,
                    event: Some(event),
                    offered,
                    ..HookAnswer::default()
                })
            }
            // A tool call delivers no message: it would be a steer, and a
            // primary is offered none (owner ruling 2026-11-18). It carries
            // what the session is owed (U13), after the skill mates line,
            // in the same budget, in its one daemon call.
            "post_tool" => {
                let mut response = self.mates_response(codec, &event);
                let owed = self
                    .carried(native, &[], "", remaining(&[&response.context]))
                    .context;
                response.context = compose_context(&[&response.context, &owed]);
                Ok(HookAnswer {
                    stdout: encode(&response)?,
                    event: Some(event),
                    ..HookAnswer::default()
                })
            }
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
    /// has profiles and none; empty otherwise, and when the daemon did not
    /// answer (O3).
    fn agent_nudge(&self) -> String {
        match self
            .hook_library()
            .and_then(|library| library.agents_and_profiles())
        {
            Some((false, true)) => AGENT_SETUP_NUDGE.to_string(),
            _ => String::new(),
        }
    }

    fn hook_library(&self) -> Option<&dyn HookLibrary> {
        self.hook_library.as_deref()
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
        let Some(skills) = self.hook_library().and_then(|library| library.skills()) else {
            return String::new();
        };
        let links = |tags: &[String]| -> Vec<String> {
            tags.iter()
                .filter_map(|t| t.strip_prefix(LINK_TAG).map(str::to_string))
                .collect()
        };
        let mut mates: Vec<String> = Vec::new();
        for skill in skills.iter().filter(|s| s.name == completed) {
            let ids = links(&skill.tags);
            for other in &skills {
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
