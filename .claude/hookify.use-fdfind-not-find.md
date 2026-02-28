---
name: use-fdfind-not-find
enabled: true
event: bash
pattern: \bfind\s
action: block
---

**Blocked: use `fdfind` instead of `find`.** Example: `fdfind -e go`, `fdfind "pattern" dir/`. Rewrite and retry.
