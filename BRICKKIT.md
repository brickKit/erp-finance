# erp/finance

## Purpose

The accounting books: chart of accounts, journal entries, the receivable and payable ledgers, the accounting calendar with its period locks, and each customer's credit used. Other components' business events turn into accounting entries here; people post manual entries, reverse entries and close periods.

**Owns**

- Journal entries (header + lines): posted once, never changed or deleted afterwards; a mistake is corrected by a reversal entry. Two numbers per entry: `entry_no` (may have gaps) and `post_no` (`P-<period>-<sequence>`, gap-free within one legal entity and one period).
- Accounting periods per legal entity, with three states: `OPEN`, `CLOSED` (reversible) and `LOCKED` (final). Postings whose date falls in a period that is not open go to the next open period.
- The receivable ledger (amount, reconciled amount, due date) and the payable ledger (no writer yet), kept apart from the general ledger so that settling a receivable never touches posted lines.
- Credit used per customer (`exposure`), and the authoritative over-limit decision when a sales order is posted.
- A summary copy of each customer (credit limit and name), kept up to date from `mdm.customer.*` events.
- Who may see and change which legal entity's books (`legal_entity_access`).
- The events `finance.voucher.posted.v1` and `finance.credit.rejected.v1`.

**Does not own**

- The customer's credit limit itself, and the customer record: `mdm/customer`. This component holds the credit used; limit minus used is what is available.
- Orders and invoices: `erp/sales`. A posted order produces an entry here; the entry is not the order, and neither may change the other.
- Stock quantities: `erp/inventory`. Adjustment events are booked at a placeholder amount (1 per unit), not a real cost.
- Paying and collecting money (bank interfaces), and the receipt flow that would settle receivables: not built yet; nothing writes `reconciled_amount` today.
- Several currencies: one currency only.

## Before you deploy

- **PostgreSQL**: a schema `erp_finance` and `erp_finance_archive` (nothing writes to the archive schema yet). A login role `erp_finance_rw` with `USAGE` and `CREATE` on both, and its password. The migration runs as this role and creates the tables, so the role owns them; the running component creates the weekly partitions of `event_outbox` / `event_inbox` itself, which needs that ownership. BrickKit creates none of this; in the BrickEnterprise assembly project `make dev-env` writes the password into `.env` and `make db-init` creates the schemas, role and grants.
- **What the migrations set up**: five accounts (`1122` receivables, `2202` payables, `1405` inventory, `6001` revenue, `6401` cost of sales), the fiscal years 2026 and 2027 with twelve monthly periods each for the legal entity `default`, and the matching partitions of the entry lines.
- **Open each new fiscal year before 1 January**: there is no API or background task that opens a fiscal year. A posting dated in a year without periods fails (`NotFound`) — and that includes every entry generated from events, which are delivered at most once, so those events are lost. Today a new year comes as a migration of this component (as FY2027 did); a deployment must have the next year's periods and partitions in place before the year starts, and should check it in the fourth quarter.
- **Grant access before anyone sees anything**: every read and write is limited to the legal entities granted to the caller in `legal_entity_access`. Grant with `POST /erp/finance/legal-entity-access/{sub}` (key `erp.finance.manage_access`); until then even an administrator sees empty lists and gets `403` on writes.
- **NATS** reachable at `NATS_URL`: the component consumes events and publishes its own through an outbox table and a background pump. It starts without NATS reachable; events published meanwhile wait in the outbox, and events sent by others meanwhile are not received.
- **Authorization** (`infra/authz`) and **identity** (`infra/iam-casdoor`, or any IAM serving a JWKS) reachable at `AUTHZ_BUNDLE_URL` and `IAM_JWKS_URL` for the REST routes to answer anything but errors. They are configuration, not dependencies: the component starts without them.
- Demo data (optional): `make seed` in the component directory, once the component is running. It publishes sample sales-order events so the receivable ledger and its aging have data; the manual entries and period examples also need `infra/iam-casdoor` and `infra/authz` in the project.

## Dependencies

None. Finance listens and is commanded; it never calls another component. It consumes events from `erp/sales` (`sales.order.created.v1`), `erp/inventory` (`erp.inventory.adjusted.v1`) and `mdm/customer` (`mdm.customer.created.v1`, `mdm.customer.updated.v1`); none of them has to be in the project for it to start, and while one is absent its events simply do not arrive. `finance.credit.rejected.v1` flows back to `erp/sales`, which puts the order on hold: an edge in the event graph, not a synchronous dependency.

The authorization bundle and the JWKS are fetched from the URLs in the configuration, not through a dependency edge. Without them every protected route fails closed: no or invalid token → `401`; `IAM_JWKS_URL` empty or unreachable → `403`; the bundle never fetched yet → `503`; a valid user without the permission key → `403`. `/healthz` stays `200` throughout: it reports only that this process is alive.

## Configuration

| Variable | Meaning |
|---|---|
| `PG_HOST` | PostgreSQL host. Usually the project's shared value (`$var:PG_HOST`). |
| `PG_PORT` | PostgreSQL port; default `5432`. |
| `PG_DATABASE` | The database holding the `erp_finance` schema (`$var:PG_DATABASE`). |
| `PG_USER` | The login role, `erp_finance_rw` as a literal. Inside a shell the shell logs in with its own role and switches to this one per transaction (`SET LOCAL ROLE`), so the role name must be `<PG_SCHEMA>_rw`. |
| `PG_PASSWORD` | Password of `PG_USER`. Secret: write `${ERP_FINANCE_DB_PASSWORD}` (or your secret store's reference), never the value. |
| `PG_SCHEMA` | Schema of all tables, the outbox and the migration state table (`schema_migrations_erp_finance`); default `erp_finance`. Events are written to this schema's outbox. Write the literal anyway so every component's schema is visible in one place. |
| `NATS_URL` | NATS server the consumers subscribe to and the outbox pump publishes to (`$var:NATS_URL`). |
| `OTEL_BASE_URL` | OpenTelemetry collector base URL; empty (the default) exports nothing. |
| `AUTHZ_BUNDLE_URL` | URL of the authorization bundle the permission check polls, for example `http://infra-authz-2-0-0:8223/authz/bundle`. Required: without it every protected route answers `503`. Keep it in step with the authz version the project runs. |
| `IAM_JWKS_URL` | URL of the JWKS used to verify user tokens locally, for example `http://infra-iam-casdoor-2-0-0:8200/.well-known/jwks.json`. Required: without it every protected route answers `403`. |

## Contracts

- `contracts/erp/finance/v1/finance.proto` — gRPC `erp.finance.v1.FinanceService`. For other components: `CheckPeriodOpen` (advisory: the authoritative check happens inside the posting transaction), `GetCreditExposure`, `BatchGetCreditExposure` (one call for many customers; missing customers count as 0). The user-facing rpcs `ClosePeriod`, `ReopenPeriod`, `LockPeriod`, `PostManualEntry`, `ReverseEntry`, `GetEntry`, `ListEntries`, `ListARLedger` need a caller identity to apply `legal_entity_access`, which gRPC does not carry: over gRPC they answer `INTERNAL`; use REST for them. The Go package is the separate module `github.com/brickKit/erp-finance/gen/erp/finance`.
- `contracts/finance.openapi.yaml` — REST under `/erp/finance`, every route behind a permission key and filtered by the caller's legal entities:
  - `erp.finance.view`: `GET /entries` (`period`, `status_filter`, `source_doc_id`, `source_doc_type`, `created_after` / `created_before` with a default of the last 90 days, `cursor`, `page_size`), `GET /entries/{id}`, `GET /ar-ledger` (rows carry `customer_name`, `outstanding` = amount − reconciled, `due_date`; `customer_id`, time window, cursor), `GET /ar-ledger/summary` (`total_receivable`, `total_reconciled`, `outstanding`, aging of the outstanding amount by days past due: `d0_30` including not yet due, `d31_60`, `d61_90`, `d90_plus`; optional `customer_id`; no time window), `GET /credit-exposure/{customer_id}`.
  - `erp.finance.post`: `POST /entries` (manual entry, idempotent by `idempotency_key`; amounts are non-negative decimal strings with at most two decimals, debits must equal credits exactly), `POST /entries/{id}/reverse` (idempotent; an entry can be reversed once).
  - `erp.finance.close`: `POST /periods/{period}/close`, `/reopen`, `/lock` (idempotent; `LOCKED` is final, lock requires `CLOSED` first).
  - `erp.finance.manage_access`: `GET` / `POST /legal-entity-access/{sub}`, `DELETE /legal-entity-access/{sub}/{legal_entity_id}`.
  - All amounts are decimal strings; lists page by cursor, never by offset.
- `contracts/events/finance.events.json` — published through the outbox: `finance.voucher.posted.v1` (after every posting; for reporting) and `finance.credit.rejected.v1` (when posting a sales order takes the customer's credit used above a configured limit). Consumed, each once by `(subject, aggregate_id, version)` and each source document posted at most once: `sales.order.created.v1` (receivable entry, receivable ledger row due on the posting date, credit used), `erp.inventory.adjusted.v1` (inventory entry at the placeholder amount), `mdm.customer.created.v1` / `mdm.customer.updated.v1` (customer summary copy, newest version wins).
- `assembly.yaml` — this project's metadata: the four permission keys, the menu entry, the edge route `/erp/finance/**`, the schema and role, and the data scope `legal_entity` (`mode: in`) on the entries, entry lines and both ledgers.

## Shell declaration

Not a shell. It can be hosted in a Go shell (in the BrickEnterprise project, `be/go-core`) or run on its own; the code is the same either way.
