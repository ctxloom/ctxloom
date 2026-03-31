---
description: Recover context from previous session after /clear
---

Recover context from the session before the last `/clear`.

## Steps

1. Use the ctxloom MCP tool "get_previous_session" to retrieve the previous session:
   - SCM tracks sessions by process ID across `/clear`
   - If the session hasn't been distilled, it will be distilled on-the-fly
   - Returns the distilled essence of the previous session

2. If successful (content is returned), review the restored context and summarize:
   - What was being worked on
   - Key decisions made
   - Progress achieved
   - Any planned next steps

3. Ask: "I've recovered context from before the clear. Ready to continue?"

If no previous session is found:
"No previous session found for this process. Use /loadctx to browse session history."
