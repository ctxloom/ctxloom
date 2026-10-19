---
title: "The Split View: ctxloom run's Controls"
---

On a terminal, `ctxloom run` shows the **agent panel** on the left and the selected agent on the right. The panel lists the session and every agent it delegated to, each marked with what it is doing. At first the right pane is your engine's own terminal, and everything you type goes to it.

`ui.prefix_key` (default **Ctrl+]**) moves the keyboard between the pane and the panel. `--plain-terminal` turns the split view off for one run.

## In the panel

| Key | Does |
|---|---|
| `j` / `k`, ↓ / ↑ | Select an agent. The right pane shows it: your engine's terminal, or a delegated agent's feed |
| Enter | Fold or unfold an agent's children |
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

When a delegated agent asks you to approve a call, the terminal bell rings. On Linux, a desktop notification also appears if `notify-send` is installed. The approvals view takes the pane the next time you pause typing, and it ignores keys for a moment so that one meant for your engine can't answer it. To decide, move with ←/→ (or Tab) and press Enter. Esc puts the decision off.
