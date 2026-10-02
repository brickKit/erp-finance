[English](AGENTS.md) · [中文](AGENTS.zh.md)

# erp/finance

The AI guide to developing this component. How to use it, its boundaries and contracts: BRICKKIT.md. Why it is shaped this way: `docs/design.md`. Dependencies, configuration and deployment: component.yaml.

## Code map

| Path | Owns |
|---|---|
| `backend/module/module.go` | The only entry, `New(ctx, rt)`: builds repo → service → HTTP + gRPC, starts the outbox pump, the partition loop and the consumers. Same function standalone and in a shell |
| `backend/cmd/server/main.go` | One line, `besdk.RunStandalone(module.New)` |
| `backend/cmd/migrate/main.go` | One line, `migrate.Main(migrations.FS)`: the migration container's entry (`./migrate up`) |
| `backend/internal/repo/repo.go` | Sentinel errors, the claim-first idempotency helpers (`claimIdempotency`, `finalizeIdempotency`) |
| `backend/internal/repo/entry.go` | `postEntryTx`, the one posting path every entry goes through: validate → lock the period → claim the header → `post_no` → lines → outbox; `publish` |
| `backend/internal/repo/money.go` | Amounts as exact cents (`parseCents`, `formatCents`), the strict amount format |
| `backend/internal/repo/manual.go` | `PostManualEntry`, `ReverseEntry` (claim-first, one reversal per entry) |
| `backend/internal/repo/entry_read.go` | `GetEntry`, `ListEntries` (filters, legal-entity scope, keyset paging) |
| `backend/internal/repo/autoentry.go` | Entries from events: sales order (receivable, credit used, over-limit event), inventory adjustment (placeholder amount) |
| `backend/internal/repo/arledger.go` | Receivable ledger: insert, `ListARLedger` (customer name, outstanding, due date), `SummarizeARLedger` (totals and aging) |
| `backend/internal/repo/period.go` | Period state machine, `CheckPeriodOpen`, `lockOpenPeriodForDate`, `nextPostNo` |
| `backend/internal/repo/credit.go` | Credit used per customer; `snapshot.go` the customer summary copy |
| `backend/internal/repo/access.go` | `legal_entity_access`: the caller's legal entities, grant, revoke |
| `backend/internal/service/` | Input validation, the caller's legal entities, the error → gRPC status mapping (`status.go`) |
| `backend/internal/http/http.go` | REST routes, each registered with its permission key; `parseWindow` |
| `backend/internal/grpc/grpc.go` | `erp.finance.v1.FinanceService` |
| `backend/internal/consumer/consumer.go` | Subscriptions to the four consumed subjects and their payload structs |
| `backend/internal/partition/` | Weekly partitions of `event_outbox` / `event_inbox`, four weeks ahead |
| `migrations/` | SQL migrations, embedded by `migrations/embed.go`; `003` seeds accounts and the FY2026 periods, `008` opens FY2027 |
| `contracts/` | proto, OpenAPI, event schema |
| `gen/erp/finance/` | Generated Go code: a nested Go module, tagged on its own as gen/erp/finance/v1.x.y; never edited by hand |
| `scripts/` | `seed.sh` for local demo data |

| Feature | Start here | Then |
|---|---|---|
| A rule about amounts or balancing | `backend/internal/repo/money.go` | `backend/internal/repo/entry.go` (`validateBalanced`), `backend/internal/repo/money_test.go` |
| Anything that posts an entry | `backend/internal/repo/entry.go` (`postEntryTx`) | its callers in `manual.go` and `autoentry.go` |
| A new consumed event | `backend/internal/consumer/consumer.go` | a `*Tx` function in `backend/internal/repo/`, `backend/internal/consumer/consumer_test.go` |
| Receivable list or aging | `backend/internal/repo/arledger.go` | `backend/internal/http/http.go`, `contracts/finance.openapi.yaml`, `backend/internal/repo/arsummary_test.go` |
| A new REST query parameter | `backend/internal/http/http.go` | `contracts/finance.openapi.yaml`, `backend/internal/http/http_test.go` |
| Who may see which legal entity | `backend/internal/repo/access.go` | `backend/internal/service/service.go` (`allowedLegalEntityIDs`) |
| A new fiscal year | `migrations/` (a new migration: periods for every legal entity and line partitions) | `migrations/008_open_fiscal_year_2027.up.sql` for the shape |
| An error answering with the wrong status | `backend/internal/service/status.go` | the sentinel errors in `backend/internal/repo/repo.go` |

## Build and test

```bash
# tests run against the test database brickkit_test_db, never brickkit_db
export TEST_PG_DSN="postgres://postgres:<password>@localhost:5432/brickkit_test_db?sslmode=disable"
export TEST_NATS_URL=nats://localhost:4222       # the consumer tests publish real messages
make test                    # every package ends in "ok"; refuses to run without TEST_PG_DSN
go test ./... -count=1 -v | grep -c -- '--- SKIP'   # 0: no test skipped
make check-version dag-check contract-check import-scan module-check   # each prints one ✓ line
make docs-check              # "0 with errors, 0 warnings"
```

Migrations run as the login role `erp_finance_rw`: `make migrate-idempotent` with `PG_HOST PG_PORT PG_DATABASE PG_USER PG_PASSWORD PG_SCHEMA` set prints `✓ 迁移幂等`; inside the BrickEnterprise project use `make test-db-init ID=erp/finance` at the project root, which supplies them. On a real machine, from the project root: `make verify ID=erp/finance ROUTE=/erp/finance/entries FOCUS=1 SEED=1` builds the image, starts only what this component needs, checks migration, health and the permission check, loads the demo data, runs a focus run, and tears down.

## Design decisions

- **An event sink with no outgoing edge.** No dependencies; everything arrives as events and the only thing it sends back is an event. Details in `docs/design.md`.
- **Every entry goes through `postEntryTx`**, which locks the period rows (`FOR UPDATE`) for the authoritative open/closed check and the gap-free `post_no` at once.
- **Two idempotency mechanisms**: write commands claim `command_idempotency` first; entries from events claim the source-document unique index on the header with `ON CONFLICT DO NOTHING`. The inbox stops the same message twice; the index stops two messages about the same document.
- **Amounts are exact**: parsed to cents, never `float64`; sums and outstanding amounts are computed in SQL as `NUMERIC`.
- **Legal-entity scope is local data** (`legal_entity_access`), not a JWT claim: every read pushes the list into SQL, every write checks the named legal entity.
- **Customer name is read at query time** from the customer summary copy, not copied into each receivable.

## Pitfalls

| Never | Symptom | Why |
|---|---|---|
| Add an entry to `dependencies.components` | Everything builds and passes; the event sink now sits in the call graph, and the next edge someone adds closes a cycle | Finance listens and is commanded; `make dag-check` fails on any dependency |
| Claim an event-driven entry with a `SELECT` before the `INSERT`, or catch the unique violation | Two concurrent deliveries: one fails with `finance_journal_entries_source_uniq` and the consumer drops it; catching it leaves the transaction aborted and the `COMMIT` fails | The header insert must be the claim (`ON CONFLICT … WHERE source_component != '' DO NOTHING`), before `post_no` is taken |
| Allocate `post_no` before the header is claimed, or from a sequence | Gaps in `post_no`: a rolled-back or duplicate posting consumes a number | `last_post_seq` on the locked period row commits or rolls back with the entry |
| Put a unique index or the idempotency constraint on `finance_journal_entry_lines` | PostgreSQL refuses it on a partitioned table unless the period is in the key, which would let the same document post twice in two periods | Uniqueness lives on the unpartitioned header |
| `UPDATE` / `DELETE` a posted entry | Tests may pass; the books no longer match what was reported | Posted is final; correct with `ReverseEntry` (once per entry) |
| Treat `CheckPeriodOpen` as permission to write | A posting slips through in the moment a period is being closed | It is advisory; the check that counts is inside `postEntryTx` |
| Parse an amount with `strconv.ParseFloat` | `"NaN"` posts; a one-cent difference on large amounts is accepted as balanced | Use `parseCents` / `NUMERIC` |
| Write `"erp_finance"` as a literal schema anywhere | Works here; with another `PG_SCHEMA` events land in the wrong outbox and are never sent | `publish` takes the schema from `current_schema()` |
| Call a user-facing service method from gRPC or `Start()` | `besdk.ScopeOf` panics (gRPC answers `INTERNAL`) | Only REST requests carry verified claims |
| Let the next fiscal year go unopened | From 1 January every posting, including every sales-order event, fails with `NotFound` and the event is dropped | Periods and line partitions exist up to FY2027 (`008`); each new year is a migration until an open-fiscal-year operation exists |
| Use a fixed `aggregate_id` / `idempotency_key` in a test | The second run is skipped by the inbox or replayed, and the test asserts nothing | Tests use `uniqueID` and `UnixNano` everywhere |

## Before changing code

1. Is this accounting (here) or the business document that caused it (sales, inventory, customer)? The document stays where it is.
2. Am I adding a dependency or a synchronous call? Stop: see Pitfalls.
3. Does the change post an entry? It goes through `postEntryTx`, and posted entries are never changed.
4. Does it touch money? Exact cents or `NUMERIC`, never floats; amounts in contracts are decimal strings.
5. A new read or write path takes the caller's legal entities from `allowedLegalEntityIDs`, and has a test where a second legal entity's data stays invisible.
6. Contract change? Append only. If `gen/` changes, the contract package needs a new tag and `go.mod` must require it.
7. New rule → write the failing test first (real database), then the code. Update BRICKKIT.md and `docs/design.md` in the same commit.

<!-- brickkit:managed:begin lang=en -->
<!-- maintained by brickkit (init, add, remove, upgrade, skills update): edits between these markers are overwritten -->

## BrickKit

This is a BrickKit component: `component.yaml` is all the platform reads. The rules it relies on:

- `configSchema` keys are the environment variable names the code reads. Never use a reserved name: `COMPONENT_ID`, `COMPONENT_VERSION`, `PORT`, `BRICKKIT_SERVED_MEMBERS`, `BRICKKIT_SERVED_MEMBERS_CONFIG`, or any `*_ENDPOINT`.
- Dependencies are exact versions. A dependency's address arrives as `<ID>_ENDPOINT`; an optional dependency that is absent has no variable at all, so read it with a fallback.
- `/healthz` checks only this process, never a dependency. The migration command runs from the same image and must fail on an argument it does not know.
- `BRICKKIT.md` travels to every project that uses this component and is read there without the repository: keep it in step with the code, with no relative links.
- Release: raise `metadata.version`, commit, push, `brickkit release`. `brickkit lint` checks the manifest and these docs — inside a project, run in this directory, it checks only this component (`--all` for the whole project).
- The full rules are in the `brickkit-component` skill (`.claude/skills/brickkit-component/SKILL.md` at the root of the project or repository where skills are installed; `brickkit skills update` installs it); for flags ask `brickkit <command> --help`; BrickKit's own documentation is `brickkit docs`.
<!-- brickkit:managed:end -->
