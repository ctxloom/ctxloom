---
description: Compact current session to memory
---

Save the current session context to memory.

Execute these steps in order:

1. Use the SCM MCP tool "compact_session" to save the current session:
   - This distills the conversation into key decisions, context, and learnings

2. Use the SCM MCP tool "index_session" to add the distilled content to the vector database:
   - This enables semantic search across your session history

After indexing completes, inform the user:
"Session saved to memory. Run /clear if you want to start fresh. To continue where you left off in a new session, just ask and I'll search my memory for relevant context using the query_memory tool."

The user can then ask questions like "What were we working on?" or "Continue from before" and you should use the "query_memory" MCP tool to retrieve relevant context from previous sessions.