---
name: verify-changes
description: Use when work is complete and ready to commit, or when asked to verify, validate, or check changes. Runs build, tests, and linting for the Streamline project.
---

# Verify Changes

Run all verification steps from the worktree root. Every step must pass before claiming completion.

## Steps

Run in this order. Stop on first failure.

```
1. goimports -local github.com/alexrybrown/streamline -w .   # fix formatting and import grouping
2. go build ./cmd/...
3. go test ./... -race -count=1 -timeout=60s -coverprofile=coverage.out
4. go tool cover -func=coverage.out   # verify ≥90% total coverage
5. golangci-lint run ./...
```

## Failure Response

- **Build failure:** Fix compilation errors first.
- **Test failure:** Investigate and fix. Do not skip or mark as known-flaky.
- **Test timeout:** Likely a deadlock or hung goroutine. Check channel sends/receives and context cancellation.
- **Goroutine leak (goleak):** A goroutine outlived its test. Ensure all goroutines respect context cancellation and channels are drained/closed.
- **Coverage below 90%:** Identify uncovered functions and add tests. Exclude `main()` and wire-up code from coverage expectations.
- **Lint failure:** Fix the violation. Do not add nolint directives without justification.

## Output

Report pass/fail for each step with relevant output. Only claim completion when all three pass.
