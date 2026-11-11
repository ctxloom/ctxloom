---
tags:
  - testing
  - coverage
  - quality
content_hash: sha256:90e6507bae0e3815717306e88a6b30d319fc28ab3ffa844d779b515bac00f822
---
# Test Coverage

Target 90%+ coverage. Unit + integration tests exercising the full application.

## Acceptable Gaps

- Main entrypoints (tested via E2E)
- Generated code
- Panic/fatal paths that can't occur
- Default factory functions (exclude via tooling)