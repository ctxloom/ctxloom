@live @probe-p13-untrusted-repo-hooks
Feature: P13 — claude -p runs an untrusted repo's committed hooks, and --setting-sources user suppresses them

  A repository can commit a .claude/settings.json whose hooks run arbitrary
  commands. claude's own trust does not stop them under `-p`: a repo with no
  projects entry in CLAUDE_CONFIG_DIR/.claude.json — one nobody ever trusted —
  still has its PreToolUse and SessionStart hooks executed. ctxloom's repo
  trust therefore cannot lean on claude's trust flag; it launches an untrusted
  repo's child with `--setting-sources user --strict-mcp-config`, which is what
  keeps the repo's settings, and the hooks in them, from loading. Both halves
  are vendor behaviour, re-run on every claude pin bump:

    just capability-probe p13-untrusted-repo-hooks probes/capability_untrusted_repo_hooks.feature claude-code host none

  HOW A CELL WORKS. The vendor binary is run directly — not through ctxloom, so
  a red names claude and nothing else — in a throwaway CLAUDE_CONFIG_DIR (no
  .claude.json, so the repo is untrusted) and HOME, with cwd a git repo whose
  COMMITTED .claude/settings.json registers a PreToolUse hook on Bash and a
  SessionStart hook, each touching a marker outside the repo. A flag-scope
  --settings allows `echo hi`, and the prompt is "run echo hi". Every
  assertion reads stream-json frames and the marker files — never the model's
  prose.

  Each cell self-skips LOUDLY when claude is absent, and when no token was
  captured at launch: a throwaway config dir cannot use the subscription login.
  Two paid haiku turns for the pair.
  Scenario Outline: An untrusted repo's committed hooks under the <variant> posture
    Given the untrusted-repo-hooks probe targets "<engine>" under runtime "<runtime>" and workspace "<workspace>" in the "<variant>" posture
    When it asks the engine to run echo hi in one turn
    Then the repo's committed hooks behave as the "<variant>" posture requires

    # UNTRUSTED-FIRES exists because it is the premise: if claude's trust ever
    # stops repo hooks under -p, ctxloom's suppression is no longer the only
    # line and the design should be re-examined. Asserts: both markers written,
    # and the echo's tool_result printed "hi".
    @claude-code @host @ws-none @var-untrusted-fires
    Examples:
      | engine      | runtime | workspace | variant         |
      | claude-code | host    | none      | untrusted-fires |

    # SETTING-SOURCES-SUPPRESSES exists because it is the defence: if these
    # flags stop keeping project settings out, an untrusted repo's hooks run in
    # ctxloom's children. Asserts: NO marker written, while the echo still ran
    # and printed "hi" — the hooks were suppressed, not untriggered.
    @claude-code @host @ws-none @var-setting-sources-suppresses
    Examples:
      | engine      | runtime | workspace | variant                    |
      | claude-code | host    | none      | setting-sources-suppresses |
