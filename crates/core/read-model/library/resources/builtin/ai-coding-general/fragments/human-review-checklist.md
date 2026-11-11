---
tags:
  - ai
  - coding
  - best-practices
  - workflow
  - review
  - security
  - quality
content_hash: sha256:b09ab85a2fbe7a45f34f7e626d812d508c02d8af9cdc4a1b1150e6ff2a14046a
---
# Human Review Checklist

AI code: correct happy path, dangerous elsewhere.

## Hallucination Check

LLMs reference nonexistent packages at 5.2% (commercial models) to 21.7% (open-source models) rates (Spracklen et al., *We Have a Package for You!*, arXiv:2406.10279, 2024; 16 LLMs, 576k samples). Verify before trusting.

- Verify imports: `npm info`, `pip show`, `cargo search`
- Check API signatures
- Confirm endpoints exist

## Test Integrity (AI "fixes" by)
- Deleting tests
- Removing assertions
- Mocking away behavior
- (See `mutation-as-test-validator` for the deterministic check that catches this.)

## Red Flags
- Deleted code unexplained
- Catch-all replacing specific handlers
- Removed validation
- async/sync changes without reason
- Unjustified new deps

## Reject Immediately
- Security vulns in sensitive code
- Deleted tests w/o justification
- Hallucinated deps
- Race conditions
- Missing error handling in critical paths