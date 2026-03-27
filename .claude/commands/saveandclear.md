---
description: Compact current session to memory and clear context
---

Save the current session context and clear for a fresh start.

IMPORTANT: Execute these steps in order:

1. First, use the SCM MCP tool "compact_session" to save the current session:
   - This distills the conversation into key decisions, context, and learnings

2. Then, use the SCM MCP tool "index_session" to add the distilled content to the vector database:
   - This enables semantic search across your session history

3. After indexing completes successfully, execute /clear to reset the context

4. IMPORTANT: After clearing, inform the user:
   "Session saved to memory. If you want to continue where we left off, just ask and I'll search my memory for relevant context using the query_memory tool."

The user can then ask questions like "What were we working on?" or "Continue from before" and you should use the "query_memory" MCP tool to retrieve relevant context from previous sessions.