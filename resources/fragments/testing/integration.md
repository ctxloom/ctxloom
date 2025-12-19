## Context

### Integration & Acceptance Testing

**Integration Tests:**

- Build and run actual binaries (don't mock at boundaries)
- Test real interactions between components
- Use test databases, not mocks for data layer
- Mark slow tests with appropriate tags

**Acceptance Tests (Gherkin/BDD):**

- Located in `tests/acceptance/features/`
- Written in business language
- Use godog (Go), behave (Python), cucumber (JS)
- Each scenario should be independent

**Slow Test Handling:**

- Tag slow tests: `//go:build slow` or `@pytest.mark.slow`
- Run fast tests by default, slow tests in CI
- Document why a test is slow

**Test Environment:**

- Use containers for external dependencies
- Reset state between test runs
- Document environment requirements

