---
distilled_by: claude-code
---
AI code makes ~2x more concurrency mistakes than human code (CodeRabbit 2025, 470 PRs). Never parallelize sequential awaits; mark load-bearing ordering with `// SYNC-REQUIRED: [reason]`.