// ctxloom's opencode plugin. WRITTEN by ctxloom into this session's home
// (plugins/ctxloom.js, found through OPENCODE_CONFIG_DIR): do not edit.
//
// It forwards what opencode's bus says of the session to ctxloom, one event at
// a time and in the order opencode said it, by running
// `ctxloom hook opencode execute <Event>` with the event on stdin. ctxloom
// decides what each one records; the plugin forwards only the events that can
// record something, and adds only what it alone knows from the events before
// (under `ctxloom`): the prompt a step message answers, a compaction's trigger
// and its summary's text. It imports node's builtins and nothing else.
//
// Its state is the module's (on globalThis), never an instance's: opencode can
// instantiate a plugin twice in one process, and the second instance must
// neither deliver an event again nor start a queue of its own. Sub-agent
// sessions (those with a parent) are not forwarded. Reasoning is never
// forwarded.

import { spawn } from "node:child_process"
import { closeSync, openSync } from "node:fs"
import { join } from "node:path"

const COMMAND = "ctxloom"
const ARGS = ["hook", "opencode", "execute"]
// How long one delivery may run before it is killed.
const DELIVERY_MS = 10000
// How long dispose waits for the queue to drain (opencode sets no limit).
const DRAIN_MS = 5000

const state = (globalThis.__ctxloomOpencode ??= {
  queue: Promise.resolve(),
  sent: new Set(), // what was forwarded, by key: each once
  children: new Set(), // sub-agent sessions
  parents: new Map(), // assistant message -> the user message it answers
  prompts: new Map(), // session -> its last prompt (user message)
  triggers: new Map(), // compaction message -> "auto" | "manual"
  summaries: new Map(), // compaction summary message -> its text parts
})

// The hook's environment: the engine's, without the HOME opencode runs under
// (the session's own), so ctxloom finds its home through CTXLOOM_HOME.
function hookEnv() {
  const env = { ...process.env }
  delete env.HOME
  return env
}

// Where a hook's stderr goes: a log in the session home, never the terminal
// opencode's TUI draws on.
function stderrSink() {
  const dir = process.env.OPENCODE_CONFIG_DIR
  if (!dir) return "ignore"
  try {
    return openSync(join(dir, "ctxloom-hooks.log"), "a")
  } catch {
    return "ignore"
  }
}

// One delivery: resolves when the hook exits, fails or is killed; never
// rejects, so a hook that cannot run never stops the session.
function run(native, payload) {
  return new Promise((resolve) => {
    const err = stderrSink()
    let over = false
    let timer
    const done = () => {
      if (over) return
      over = true
      clearTimeout(timer)
      if (typeof err === "number") {
        try {
          closeSync(err)
        } catch {}
      }
      resolve()
    }
    let child
    try {
      child = spawn(COMMAND, [...ARGS, native], { stdio: ["pipe", "ignore", err], env: hookEnv() })
    } catch {
      return done()
    }
    timer = setTimeout(() => child.kill("SIGKILL"), DELIVERY_MS)
    child.on("error", done)
    child.on("close", done)
    child.stdin.on("error", () => {})
    child.stdin.end(JSON.stringify(payload))
  })
}

// Queue one delivery after every earlier one, once per key.
function forward(key, native, payload) {
  if (state.sent.has(key)) return
  state.sent.add(key)
  state.queue = state.queue.then(() => run(native, payload))
}

function shown(part) {
  return part && part.type === "text" && !part.synthetic && !part.ignored
}

function promptOf(part) {
  return state.parents.get(part.messageID) || state.prompts.get(part.sessionID) || ""
}

function onEvent(event) {
  if (!event || typeof event.type !== "string") return
  const p = event.properties || {}
  const envelope = (ctxloom) => ({ id: event.id, type: event.type, properties: p, ctxloom })
  switch (event.type) {
    case "session.created":
    case "session.updated": {
      const info = p.info || {}
      if (info.id && info.parentID) state.children.add(info.id)
      return
    }
    case "message.updated": {
      const info = p.info || {}
      if (info.role !== "assistant" || state.children.has(info.sessionID)) return
      if (info.parentID) state.parents.set(info.id, info.parentID)
      if (info.summary === true && !state.summaries.has(info.id)) state.summaries.set(info.id, [])
      if (!info.time || !info.time.completed) return
      if (info.summary === true) {
        const trigger = state.triggers.get(info.parentID) || ""
        const summary = state.summaries.get(info.id).join("\n")
        return forward("summary:" + info.id, event.type, envelope({ trigger, summary }))
      }
      if ((info.finish && info.finish !== "tool-calls") || info.error) {
        return forward("end:" + info.id, event.type, envelope({}))
      }
      return
    }
    case "message.part.updated": {
      const part = p.part || {}
      if (state.children.has(part.sessionID)) return
      if (part.type === "compaction") {
        state.triggers.set(part.messageID, part.auto ? "auto" : "manual")
        return forward("compaction:" + part.id, event.type, envelope({}))
      }
      if (part.type === "tool") {
        const status = (part.state || {}).status
        if (status !== "completed" && status !== "error") return
        return forward("tool:" + part.id, event.type, envelope({ prompt_id: promptOf(part) }))
      }
      if (!shown(part) || !part.time || !part.time.end) return
      const summary = state.summaries.get(part.messageID)
      if (summary) {
        // A compaction's summary: its text is the compaction's, not words said.
        if (!state.sent.has("summary-part:" + part.id)) {
          state.sent.add("summary-part:" + part.id)
          summary.push(part.text || "")
        }
        return
      }
      return forward("text:" + part.id, event.type, envelope({ prompt_id: promptOf(part) }))
    }
    case "session.idle": {
      const prompt = state.prompts.get(p.sessionID)
      if (!prompt || state.children.has(p.sessionID)) return
      return forward("idle:" + p.sessionID + ":" + prompt, event.type, envelope({ prompt_id: prompt }))
    }
  }
}

function onPrompt(input, output) {
  const message = (output && output.message) || {}
  const parts = (output && output.parts) || []
  const session = (input && input.sessionID) || message.sessionID
  if (!session || !message.id || state.children.has(session)) return
  if (!parts.some((part) => shown(part) && (part.text || "").trim())) return
  state.prompts.set(session, message.id)
  forward("prompt:" + message.id, "chat.message", {
    type: "chat.message",
    properties: { sessionID: session, message, parts },
    ctxloom: {},
  })
}

// Wait for the queue (and for what joins it meanwhile), but no longer than
// DRAIN_MS in all.
async function drain() {
  const deadline = Date.now() + DRAIN_MS
  let queue
  do {
    queue = state.queue
    const left = deadline - Date.now()
    if (left <= 0) return
    let timer
    const limit = new Promise((resolve) => {
      timer = setTimeout(resolve, left)
    })
    await Promise.race([queue, limit])
    clearTimeout(timer)
  } while (queue !== state.queue)
}

export const CtxloomPlugin = async () => ({
  event: async ({ event }) => {
    try {
      onEvent(event)
    } catch {}
  },
  "chat.message": async (input, output) => {
    try {
      onPrompt(input, output)
    } catch {}
  },
  dispose: drain,
})
