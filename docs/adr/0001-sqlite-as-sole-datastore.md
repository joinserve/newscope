# ADR 0001: SQLite as sole datastore

- Status: Accepted
- Date: 2026-04-19
- Deciders: caspar

## Context

Newscope needs durable storage for feeds, articles, embeddings, and settings.
Traditional choices (Postgres, MySQL) require a separate process, connection pooling,
and operational overhead. The initial target deployment is a single-user self-hosted
binary or small container; there is no multi-writer requirement.

## Decision

Use SQLite via `modernc.org/sqlite` (pure-Go, no CGO) as the only datastore.
All migrations are handled with `schema.sql` embedded in the binary; `ALTER TABLE ADD COLUMN`
with backfills covers safe in-place upgrades.

## Consequences

**Good:**
- Zero external dependencies — the binary ships with storage included.
- Trivial backup: `cp newscope.db`.
- Pure-Go driver means cross-compilation to arm64/musl works without CGO flags.

**Constraints to watch:**
- Single writer at a time (WAL mode gives concurrent reads, not writes). Fine for a
  personal feed aggregator; needs revisiting if multi-user or high-concurrency writes arrive.
- No built-in streaming replication. Litestream or similar would need to be layered on
  for HA.
- Vector search extensions (e.g., `sqlite-vec` for ADR 0010) must be loadable at runtime,
  which complicates the pure-Go story.
