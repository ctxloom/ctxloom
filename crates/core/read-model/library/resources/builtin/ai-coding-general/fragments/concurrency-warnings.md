---
tags:
  - ai
  - coding
  - best-practices
  - workflow
  - concurrency
  - safety
content_hash: sha256:fce2fef1c1846f8f2d337cb7c209620d510a465777e5c5b8038077e1097e5058
---
# Concurrency Warnings

AI-authored code is approximately 2x more likely to make concurrency and dependency-correctness mistakes than human-only code (CodeRabbit, *State of AI vs Human Code Generation*, 2025; 470 PRs analyzed, ~1.7x more issues overall, concurrency a leading subcategory).

Do not "optimize" sequential awaits into parallel ones; ordering may be load-bearing. Mark intentional ordering with `// SYNC-REQUIRED: [reason]`:

```javascript
// SYNC-REQUIRED: email needs user.id from save
await saveUser(user);
await sendEmail(user);

// AI BUG: await Promise.all([saveUser, sendEmail])
```