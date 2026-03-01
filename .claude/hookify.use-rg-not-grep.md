---
name: use-rg-not-grep
enabled: true
event: bash
pattern: \bgrep\b
action: block
---

**BLOCKED: `grep` is not allowed.** You MUST rewrite the SAME command replacing `grep` with `rg`. Do NOT use `tail`, `head`, `awk`, `sed`, or any other workaround — the goal is to use `rg` specifically. Flags map directly: `-r` is default, `-n`, `-l`, `-i`, `-E` all work. For example: `go test ./... 2>&1 | grep -E "PASS|FAIL"` becomes `go test ./... 2>&1 | rg "PASS|FAIL"`. Rewrite and retry.
