// ctxloom's session hooks for opencode. WRITTEN by ctxloom into this
// session's home (plugins/ctxloom-session-hooks.js, found through
// OPENCODE_CONFIG_DIR) at every launch: do not edit.
//
// It runs the hooks the session's profiles carry for the session's start
// (opencode's `session.created`, once per top-level session: a sub-agent's,
// one with a parent, is not a session's start), one after another, each with
// opencode's environment, its output appended to ctxloom-hooks.log in this
// session home (never the terminal opencode's TUI draws on). A hook with
// arguments runs its program with them; one without is a shell's command
// line, as claude runs it. A hook that cannot run, fails or outlives its
// timeout (killed) never stops the session. It imports node's builtins and
// nothing else.

import { spawn } from "node:child_process"
import { closeSync, openSync } from "node:fs"
import { join } from "node:path"

const HOOKS = []
// A hook's timeout when it names none, in seconds.
const DEFAULT_TIMEOUT_S = 60
// How long dispose waits for the hooks still running.
const DRAIN_MS = 5000

const state = (globalThis.__ctxloomOpencodeSessionHooks ??= {
  queue: Promise.resolve(),
  started: new Set(), // the sessions whose start hooks ran, by id
})

function sink() {
  const dir = process.env.OPENCODE_CONFIG_DIR
  if (!dir) return "ignore"
  try {
    return openSync(join(dir, "ctxloom-hooks.log"), "a")
  } catch {
    return "ignore"
  }
}

function run(hook) {
  return new Promise((resolve) => {
    const out = sink()
    let over = false
    let timer
    const done = () => {
      if (over) return
      over = true
      clearTimeout(timer)
      if (typeof out === "number") {
        try {
          closeSync(out)
        } catch {}
      }
      resolve()
    }
    let child
    try {
      const stdio = ["ignore", out, out]
      child =
        hook.args.length > 0
          ? spawn(hook.command, hook.args, { stdio })
          : spawn(hook.command, { stdio, shell: true })
    } catch {
      return done()
    }
    const seconds = hook.timeout > 0 ? hook.timeout : DEFAULT_TIMEOUT_S
    timer = setTimeout(() => child.kill("SIGKILL"), seconds * 1000)
    child.on("error", done)
    child.on("close", done)
  })
}

function onEvent(event) {
  if (!event || event.type !== "session.created") return
  const info = (event.properties || {}).info || {}
  if (!info.id || info.parentID || state.started.has(info.id)) return
  state.started.add(info.id)
  for (const hook of HOOKS) state.queue = state.queue.then(() => run(hook))
}

async function drain() {
  let timer
  const limit = new Promise((resolve) => {
    timer = setTimeout(resolve, DRAIN_MS)
  })
  await Promise.race([state.queue, limit])
  clearTimeout(timer)
}

export const CtxloomSessionHooks = async () => ({
  event: async ({ event }) => {
    try {
      onEvent(event)
    } catch {}
  },
  dispose: drain,
})
