---
tags:
  - ai
  - coding
  - best-practices
  - workflow
  - testing
  - mutation-testing
  - validation
content_hash: sha256:b7d3dba5beb3c0e90d17b38f9fb424577a829a0a2e7a6dfffb373f26ac020d75
---
# Mutation Testing as a Test Validator

Passing tests are not proof that tests are good. Mutation testing is the deterministic check.

LLMs produce plausible-looking tests at scale; "plausible" is not "meaningful." Mutation testing catches the failure modes: tautological assertions (`assert_eq!(result, calculate(5, 3))` always passes), missing edge cases at boundaries, and implementation-coupled tests that are brittle to refactor but blind to real bugs.

## Workflow Integration

After the TDD round-trip:

1. LLM writes tests
2. You review tests
3. LLM implements
4. **Run mutation testing**
5. Analyze survivors: which behaviors are not actually tested?
6. Add tests that kill survivors, or accept the gap explicitly

## Tools

- Rust: `cargo-mutants`
- Java: `pitest`
- Python: `mutmut`, `cosmic-ray`
- JS/TS: `stryker`
- Go: `gomutation`, `go-mutesting`

## Target Kill Rates

| Code shape | Target |
|---|---|
| Pure utility functions | 80-90%+ |
| Business logic | 80-90% |
| Framework glue delegating to tested core | 60-70% |
| Logging-only paths | survivors acceptable |

Teams with 80-90% code coverage routinely discover 30% mutation kill rates on first adoption. Coverage measures execution; mutation testing measures verification.

## Anti-Patterns

- Treating high code coverage as test quality.
- Killing a surviving mutant by adding `assert!(true)` adjacent to the line. Score goes up; signal goes to zero.