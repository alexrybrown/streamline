---
name: use-rg-not-grep
enabled: true
event: bash
pattern: \bgrep\b
action: block
---

**Blocked: use `rg` instead of `grep`.** Flags map directly: `-r` is default, `-n`, `-l`, `-i` all work. Rewrite and retry.
