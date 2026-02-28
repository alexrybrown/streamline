# Go Conventions

## Constructors
- Functions with >3 parameters MUST accept a config struct.
- Validate required config fields in the constructor, not in methods. Fail early.

## Logging
- Constructors scope with `cfg.Log.Named("component")` and `.With()` for static fields.
- Methods add `zap.String("method", "name")` as a field for attribution.

## Testing
- Test-only config fields MUST be unexported. Expose them via `export_test.go` helpers (e.g., `WorkerConfigWithProcessFactory`).
- Prefer hand-written test doubles (stubs, spies, fakes) over generated mocks. Mockery is configured (`.mockery.yaml`) but generated mocks are not used — hand-written doubles are simpler, more readable, and avoid cross-package import issues.
- Internal tests (`package foo`) go in `*_internal_test.go` for testing unexported functions.
- External tests (`package foo_test`) are the default for testing public API.

## Concurrency
- All blocking operations (channel sends, external calls, subprocess waits) MUST use `context.WithTimeout` or respect a parent context.
- Tests that spawn goroutines MUST use `goleak.VerifyNone(t)` to catch leaks.

## Naming
- Variables and parameters MUST be human-readable words. No abbreviations except universally understood ones (`ctx`, `err`, `ok`, `i`/`j`/`k` for loop indices, `t` in tests, `w` for `http.ResponseWriter`).

## External Dependencies
- Wrap external libraries with narrow interfaces (only the methods you call). The concrete type satisfies the interface implicitly. Inject the interface into constructors; use `export_test.go` helpers to inject mocks in tests.

## Code Hygiene
- No magic numbers. Use named constants or config fields. Comment why the value was chosen.
- No magic strings for branching logic. Use typed constants, config fields, or caller-provided values.
- Prefer table-driven tests.
