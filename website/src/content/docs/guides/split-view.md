---
title: "The Split View: ctxloom run's Controls"
---

On a terminal, `ctxloom run` shows the **agent panel** on the left and the selected agent on the right. The panel lists the session and every agent it delegated to, each marked with what it is doing. At first the right pane is your engine's own terminal, and everything you type goes to it.

`ui.prefix_key` (default **Ctrl+]**) moves the keyboard between the pane and the panel. `--plain-terminal` turns the split view off for one run.

When the run ends, your engine's last screen stays in your terminal, followed by a line naming the session's full transcript: only that last screen reaches your shell's scrollback, and the transcript has the rest.

## In the panel

Your session is always the top row, with the agents it delegated to under it. Each level of the list is ordered by activity, most recent first. An agent that is waiting for you to approve a call is marked with a red **!** in place of its activity glyph (and !2 at the row's right edge when it waits on two), and it moves to the top of its level. The approvals line at the panel's foot is red too. Unread mail shows there as ✉ and a count. A name too long for the panel is cut short to keep them.

The list shows only agents and sessions active within `ui.list_window` (default **7d**; `all` shows everything). Anything running, and anything waiting on an approval or holding mail, shows whatever its age. `w` widens the window for this session (1h, 1d, 7d, any age, then round again), and `f` filters by status: all, active (not ended), waiting (on your approval), or ended. Your own session and the selected agent always show, and so does the parent of any agent that shows; one shown only for that is dim. A dim line at the foot of the panel says what is hidden: `+416 older than 7d`, or, under a status filter, the filter, the window and the count, for example `active · 7d · +4 hidden`.

When you move the keyboard to the panel, the selection starts on the agent the pane is showing.

| Key | Does |
|---|---|
| `j` / `k`, ↓ / ↑ | Select an agent. The right pane shows it: your engine's terminal, or a delegated agent's feed |
| Enter | Return the keyboard to the pane, on the selected agent |
| `f` | Cycle the status filter: all, active, waiting, ended |
| `w` | Cycle the age window: 1h, 1d, 7d, any age |
| `[`, PgUp | Scroll back through your engine's terminal |
| `a` | Open the approvals waiting for you |
| `i` | Send the selected agent a message |
| `?` | Ask the selected agent a question |
| `s` | Ask the selected agent for a summary |
| `p` / `r` | Pause or resume the selected agent |
| Esc, `q`, Ctrl+] | Return the keyboard to the pane |

## A delegated agent's pane

The pane shows the agent's feed above an input line. Enter sends what you typed, and Alt+Enter sends it as an interruption. Esc clears the line. ↑ / ↓, PgUp / PgDn and End scroll the feed.

## Scrolling back

`[` or PgUp in the panel shows the history of your engine's terminal. A marker in the top-right corner shows how far back you are. PgUp / PgDn, ↑ / ↓, `g` and `G` move, and the mouse wheel scrolls. `q` or Esc returns to the live screen. Your keys don't reach the engine while you scroll back, but its output keeps arriving.

## Approvals

When a delegated agent asks you to approve a call, the terminal bell rings. On Linux, a desktop notification also appears if `notify-send` is installed. The approvals view takes the pane the next time you pause typing, and it ignores keys for a moment so that one meant for your engine can't answer it. Once it shows, it has the keyboard: there is nothing to switch to. To decide, move with ←/→ (or Tab) and press Enter. Esc puts the decision off; Ctrl+] there leaves the view open and says so.
