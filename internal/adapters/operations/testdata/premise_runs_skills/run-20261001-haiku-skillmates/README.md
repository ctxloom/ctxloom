# run-20261001-haiku-skillmates -- the skill-mates hook in the loop

The ruling (moving-simple, 2026-10-01): measure the PostToolUse `ctxloom hook
skill-mates` against the 15 two-skill situations, and keep it only if both-found
beats the 4/15 listing-condition ceiling.

Method. Step 1 is FIXED to the listing-condition control
(`../run-20260918-sonnet-linkpointer/answers_control.txt`, the 4/15 run). Step 2
runs wherever a step-1 skill has a link-group mate step 1 did not pick: the
model is shown the same listing, the moment, and the body of each skill it
invoked (the corpus `content`), and decides what comes next. Within each pair of
arms the ONLY difference is the hook's line, generated from the same text as
`claude.SkillMatesContext`. Final answer = step 1 plus step 2.

Link groups are an ORACLE: every consequent pair the corpus expects is a group
(so prompt-human has four mates). That is the hook's best case. No shipped
bundle declares a link group for any of these skills.

Selector: `claude --print --model claude-haiku-4-5-20251001`, from an empty cwd
under a throwaway HOME/CLAUDE_CONFIG_DIR, no tools, `--strict-mcp-config`
(`call.sh`). An earlier pass run from inside this worktree loaded the project's
CLAUDE.md and .mcp.json; it was discarded.

    arm      step-2 question                        both-found  recall  precision  single-exact  fp
    control  (step 1 only; the ceiling)             4/15        0.759   0.957      48/57         --
    nbody    "what is your next action?"            5/15, 5/15  0.770   0.870      42-43/57      10
    nhook    same, plus the hook's line             8/15, 7/15  0.79-0.81 0.77-0.79 32-34/57     19-21
    body     "which further skills do you invoke?"  11/15       0.839   0.768      32/57         22
    hook     same, plus the hook's line             10/15       0.828   0.735      29/57         26

The n-arms are the hook's real shape: after a skill completes, a session gets
the hook's line and nothing that asks it to reconsider. Two passes each
(`out/`, `out2/`). The forced-question arms show the second decision point is
what recovers pairs; with an explicit question the hook's line adds nothing.

Cost: the hook's extra false positives are almost all prompt-human (10-11 per
pass against 2 without the hook), the skill the oracle grouped with four others.
Narrow groups would avoid most of it.

Reproduce:  python3 loop.py build   # writes prompts/ (not committed)
            ls prompts | sed 's/.txt$//' | xargs -P 8 -I{} zsh -ic './call.sh prompts/{}.txt out/{}.txt'
            python3 loop.py score body hook nbody nhook
            OUT=out2 SUFFIX=_rep2 python3 loop.py score nbody nhook
