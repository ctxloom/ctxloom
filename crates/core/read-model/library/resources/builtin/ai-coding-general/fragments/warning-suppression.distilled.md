---
distilled_by: claude-code
---
# Warning Suppression
Do not suppress lint or compiler warnings without explicit user approval.  Warnings are signals of potential issues. Suppressing them without review risks hiding real problems.
Ensure that, when the user does approve a warning suppression, the comment includes the justification and details of the warning being suppressed. This creates a record for future reviewers to understand the context and reasoning behind the suppression.