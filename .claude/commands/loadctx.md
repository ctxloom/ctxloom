---
description: Browse and load context from a session
---

Browse recent sessions and load context from one, distilling it on-the-fly if needed.

## Steps

1. Use the ctxloom MCP tool "browse_session_history" to show sessions from the last 3 days:
   - Each session shows its ID, start time, and a brief AI-generated essence
   - Essences are cached for fast subsequent lookups

2. Present the sessions to the user in a readable format, showing:
   - Session date/time
   - Brief essence of what was worked on
   - Ask which session they want to load (or if they want to start fresh)

3. When the user chooses a session, use "load_session" with the session_id:
   - Uses cached distilled content if available (fast)
   - Otherwise distills on-the-fly using the configured LLM (may take several seconds)

4. After loading successfully (loaded: true), review the restored context and summarize:
   - What was being worked on
   - Key decisions that were made
   - Any open items or next steps

5. Ask the user: "I've loaded context from this session. Would you like to continue where you left off, or start something new?"

If no sessions are found, inform the user:
"No recent sessions found. This may be the first session for this project."
