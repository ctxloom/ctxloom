//! The plugin's wire in the port's terms: what `plugins/ctxloom.js` writes to
//! a hook's stdin, decoded. Not yet built.

use surface_ports::{HookCodec, HookEvent, HookResponse, SurfaceError};

/// opencode's hook wire, as the plugin forwards it.
#[derive(Default)]
pub struct Codec;

impl HookCodec for Codec {
    fn unified_event(&self, native: &str) -> String {
        native.to_string()
    }

    fn decode(&self, _event: &str, _payload: &str) -> Result<HookEvent, SurfaceError> {
        Ok(HookEvent::default())
    }

    fn encode(&self, _event: &str, _response: &HookResponse) -> Result<String, SurfaceError> {
        Ok(String::new())
    }

    fn invoked_skills(&self, _transcript: &str) -> Vec<String> {
        Vec::new()
    }
}

#[cfg(test)]
#[path = "codec.test.rs"]
mod tests;
