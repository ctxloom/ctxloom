---
tags:
  - testing
  - tdd
  - workflow
content_hash: sha256:7a9cfe52e20269405608c1b455c436144764461d07cf45b59ca73de96201a1b7
---
# Test-Driven Development (TDD)

Red-green-refactor is mandatory. Verify the red test actually runs and fails for the right reason before implementing.

## Integration/Acceptance Tests

- Build and run actual binaries, don't define hooks
- Mark slow tests with a slow tag (framework-specific)
- Ensure good test isolation, clean up after yourself

## Test Naming

Format: `test_<action>_<condition>_<expected_result>`

- Python: `test_divide_by_zero_raises_value_error` (snake_case)
- C#/Java: `testDivideByZeroRaisesValueError` (camelCase)
- JavaScript: `testDivideByZeroThrowsError` (camelCase)

Prioritize readability over strict format compliance.

## Test Organization

Order by complexity: simple usage-demonstrating tests at the top (examples for developers), comprehensive tests and edge cases at the bottom. Tests serve as documentation.