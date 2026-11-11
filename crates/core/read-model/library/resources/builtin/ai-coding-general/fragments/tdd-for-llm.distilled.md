---
distilled_by: claude-3-opus
---
# TDD for LLM-Written Code

LLMs non-deterministic; tests are deterministic gate.

## Rule
Demand TDD: tests first, implementation second. Non-negotiable.

## Workflow
1. Describe requirement
2. LLM writes tests against requirement (not implementation)
3. Review tests: requirement, edge cases, failure modes captured?
4. LLM implements to pass tests
5. Run tests

## Why
Test suite = contract. Without tests-first, model fills contract with its own output — no separate ground truth.

## Anti-Patterns
- Implementation-first, then tests → tests confirm bug, not requirement
- Vacuous assertions (`assert!(true)`)
- Implementation-coupled tests (`assert_eq!(hash("x"), 0x7a3f...)`) — brittle, blind to behavior
- Skipping step 3: test review is load-bearing

## What Tests Document
Document **problem**, not solution. Name requirement in test name; assert observable behavior.

## Verification
```
Show me the tests before the implementation. I will review the tests, approve them, then you write the implementation.
```