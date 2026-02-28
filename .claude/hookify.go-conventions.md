---
name: go-conventions
enabled: true
event: file
action: warn
conditions:
  - field: file_path
    operator: regex_match
    pattern: \.go$
---

**Verify this code follows the conventions in CLAUDE.md before proceeding.**
