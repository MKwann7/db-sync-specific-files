# Tests

This directory is intentionally empty of test files.

Following idiomatic Go convention, all automated tests live alongside the
code they exercise as `*_test.go` files within each package under
`internal/` (e.g. `internal/sync/decision_test.go`,
`internal/sync/engine_test.go`, `internal/state/state_test.go`, etc.).

Run the full suite with:

```bash
go test ./...
```

See the "Running the automated tests" section of the top-level README.md
for a description of what each package's tests cover.
