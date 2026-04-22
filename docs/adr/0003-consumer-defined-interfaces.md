# ADR 0003: Consumer-defined interfaces

- Status: Accepted
- Date: 2026-04-19
- Deciders: caspar

## Context

Go interfaces are satisfied implicitly, which creates a choice: define interfaces
in the package that implements the type, or in the package that uses it. The former
pattern (common in Java/C#) couples consumers to providers and makes mocking harder.

## Decision

Interfaces are defined in the **consumer** package, not the provider. Each package
declares only the methods it actually calls:

```go
// In scheduler/scheduler.go
type Database interface {
    GetFeeds(ctx context.Context, enabledOnly bool) ([]db.Feed, error)
    UpdateFeedFetched(ctx context.Context, feedID int64, nextFetch time.Time) error
}

// In server/server.go — a different, overlapping slice of the same concrete DB
type Database interface {
    GetClassifiedItems(ctx context.Context, ...) ([]db.Item, error)
    UpdateItemFeedback(ctx context.Context, id int64, feedback string) error
}
```

Mocks are generated with `moq` via `go:generate` and stored in a `mocks` package.

## Consequences

**Good:**
- Packages depend on abstractions sized to their actual needs — no accidental coupling.
- Swapping implementations (e.g., in-memory DB for tests) requires only satisfying the
  narrow interface, not the full concrete type.
- `moq`-generated mocks stay small and typed.

**Constraints:**
- The same concrete `*repository.DB` satisfies many small interfaces; this is
  intentional but can confuse readers unfamiliar with the pattern.
- Interfaces must be kept in sync with actual usage manually; unused methods drift
  in if not pruned after refactors.
