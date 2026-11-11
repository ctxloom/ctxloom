---
tags:
  - ai
  - coding
  - best-practices
  - configuration
content_hash: sha256:f1a9e9a382bd92330f654cbf53c3262496ddeac56084e77e5e83a419fcba10fb
---
# Warning Suppression
Do not suppress lint or compiler warnings without explicit user approval.  Warnings are signals of potential issues. Suppressing them without review risks hiding real problems.
Ensure that, when the user does approve a warning suppression, the comment includes the justification and details of the warning being suppressed. This creates a record for future reviewers to understand the context and reasoning behind the suppression.