## Context

### Test Organization

**File Structure:**

- Unit tests: `*_test.go`, `test_*.py`, `*.test.ts` (co-located)
- Integration tests: `tests/integration/` or build tags
- Acceptance tests: `tests/acceptance/features/*.feature`

**Test Naming:**

Format: `test_<action>_<condition>_<expected_result>`

Language-specific casing:
- Python: `test_divide_by_zero_raises_value_error`
- Go/Java: `TestDivideByZeroRaisesValueError`
- JavaScript: `testDivideByZeroThrowsError`

Prioritize readability over strict format compliance.

**Test Ordering:**

- **Top of file:** Usage-demonstrating tests
- **Bottom of file:** Comprehensive tests, edge cases, exhaustive coverage

Tests serve as documentation and examples for developers.

**Test Isolation:**

- Unit tests shouldn't modify the environment
- Clean up after yourself
- No shared mutable state between tests

