Feature: Distillation is steered by the next step the session captured, and only by that

  A transcript compressed without knowing what the resuming session means to
  do discards the material that next step needs as readily as anything else.
  So `session distill` reads the next step the TurnEnd hook captured while
  the session was live (next_step_capture.feature) and hands it to the
  distiller as a task hint.

  The claim has two arms and the second is the one that rots quietly. With a
  captured next step, the hint must reach the prompt the distiller ACTUALLY
  RECEIVED — not the command's report of what it sent. With none, the prompt
  must be byte-for-byte what it was before hints existed: a fresh harp has no
  next step, and an essence produced from a prompt that rendered an empty
  hint section would be attributable to a prompt no evaluation ever measured.
  Both are proven against the mock distiller's own record of its input, by
  distilling the same session twice — once bare, once with a next step the
  real hook captured — and comparing the two prompts.

  Background:
    Given a project whose mock engine is both its primary and its distillation backend
    And an earlier session "quiet-ember-forge" left a real, non-empty transcript on disk
    And the mock distiller is configured to respond "DISTILLED: the decision was to cache by ETag"

  # The next step is CAPTURED by the real hook, not written into place by the
  # fixture: the hook writes and the distiller reads, and a scenario that
  # seeded the file itself would stay green if the two ever disagreed about
  # where it lives.
  Scenario: The captured next step reaches the distiller, and without one the prompt is unchanged
    When I run "ctxloom session distill quiet-ember-forge"
    Then the command succeeds
    And the distiller's recorded prompt is kept as the hint-free baseline
    Given the session harp is "quiet-ember-forge"
    And a "mock" transcript at "turn.jsonl" whose turn ends with:
      """
      NEXT-STEP-HINT-REACHES-DISTILLER: re-run the ETag revalidation test before merging.
      """
    When I run "ctxloom hook next-step" with input:
      """
      {"session_id":"seeded-quiet-ember-forge","hook_event_name":"Stop","transcript_path":"$PROJECT_DIR/turn.jsonl"}
      """
    Then the captured next step for harp "quiet-ember-forge" is:
      """
      NEXT-STEP-HINT-REACHES-DISTILLER: re-run the ETag revalidation test before merging.
      """
    When I run "ctxloom session distill quiet-ember-forge"
    Then the command succeeds
    And the mock recorded input contains "NEXT-STEP-HINT-REACHES-DISTILLER: re-run the ETag revalidation test before merging."
    And the hinted distill sent the hint-free prompt unchanged, with the next step added after the instructions
