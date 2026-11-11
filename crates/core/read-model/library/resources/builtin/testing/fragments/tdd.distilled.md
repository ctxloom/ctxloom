---
distilled_by: claude-code
---
# TDD

Red-green-refactor is mandatory; verify the red test runs and fails for the right reason before implementing.

Integration/acceptance: build and run actual binaries, don't define hooks; tag slow tests; isolate and clean up after yourself.

Naming: `test_<action>_<condition>_<expected_result>` in the language's casing; readability over strict format. Order test files by complexity — usage-demonstrating examples at top, edge cases at bottom; tests are documentation.