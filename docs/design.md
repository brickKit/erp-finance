[English](design.md) · [中文](design.zh.md)

# erp/finance design

Conclusions only, for whoever changes this component's design. How to use it: `BRICKKIT.md`; how the code is laid out: `AGENTS.md`.

## Boundaries

This component is the event sink of the project: what happens elsewhere becomes accounting here. It owns the chart of accounts, journal entries, the receivable and payable ledgers, the accounting calendar with its period locks, and the credit each customer has used.

| Not here | Owner | Why |
|---|---|---|
| The customer's credit limit | `mdm/customer` | The limit is "how much we grant" (a business decision, master data); credit used is "how much is taken" (an accounting fact). Holding them in two components is deliberate; available credit is limit minus used |
| Orders and invoices | `erp/sales` | A posted order produces an entry; the entry is not the order. Sales changes orders, finance changes entries, neither changes the other |
| Stock quantities | `erp/inventory` | Finance books the value of an adjustment; the quantity's truth is in inventory |
| Paying and collecting money | a payment integration (not built) | Finance records receivables and payables; it does not touch payment channels |
| Several currencies | a customer fork | One currency only. Every amount table keeps a `currency` column fixed to the home currency, because adding a column to a partitioned table later costs far more than having it now |

## Owned data

| Table | Partitioned | Notes |
|---|---|---|
| `fiscal_years` | no | FY2026 (`003`) and FY2027 (`008`), created by migration |
| `accounting_periods` | no | natural key `(period, legal_entity_id)`; status `OPEN` / `CLOSED` / `LOCKED`; `last_post_seq` is the `post_no` counter |
| `accounts` | no | five accounts seeded by migration: `1122` receivables, `2202` payables, `1405` inventory, `6001` revenue, `6401` cost of sales |
| `finance_journal_entries` | **no** | the header; carries the source-document unique index (see Contract surface) and `post_no` unique per legal entity |
| `finance_journal_entry_lines` | LIST by `accounting_period` | the only table in the project not partitioned by `created_at`; one side non-zero per line (`CHECK`); `accounting_period` and `legal_entity_id` copied from the header so scope filters need no join |
| `ar_ledger` | no | amount, `reconciled_amount` (`0 ≤ reconciled ≤ amount`), `due_date`, status `OPEN` / `RECONCILED` |
| `ap_ledger` | no | same shape; no writer yet (no purchasing component) |
| `customer_credit_exposure` | no | credit used, materialised; `CHECK exposure ≥ 0` |
| `customer_credit_snapshots` | no | summary copy of each customer from `mdm.customer.*`: `credit_limit`, `name`, `version` |
| `legal_entity_access` | no | `(sub, legal_entity_id)`: who may see and change which legal entity's books |
| `command_idempotency`, `event_outbox`, `event_inbox` | outbox / inbox weekly | as in every component |

Two tables for the customer (`customer_credit_exposure`, `customer_credit_snapshots`), not one: one is this component's own posting fact, the other an external copy, and they change at different moments.

**Entries have two states, `DRAFT` and `POSTED`, and no `CANCELLED`.** A posted entry is never changed or deleted; undoing it is a new entry with debits and credits swapped (a reversal), booked in the current period, never in the original's. An entry can be reversed once: the reversal transaction locks the original header and refuses a second one. Today every entry is posted straight away; no path leaves a draft.

**Two numbers per entry.** `entry_no` comes from a sequence and may have gaps. `post_no` is `P-<period>-<sequence>`, gap-free within one legal entity and one period (an audit requirement). It is taken from `last_post_seq` on the period row the posting already holds locked, so it commits or rolls back with the entry; a sequence would leave gaps on rollback. The counter is per legal entity, so `post_no` is unique per legal entity, not globally: every legal entity has its own books.

**Periods have three states.** `OPEN` → close → `CLOSED` → lock → `LOCKED` (final); `CLOSED` → reopen → `OPEN`. Closing is a reversible routine; locking is the irreversible yearly act. A single "closed" flag would force a choice between "always changeable" and "never changeable" the first time a missed document turns up after month end.

**Amounts.** Non-negative decimal strings with at most two decimals and at most sixteen integer digits, matching `NUMERIC(18,2)`. Validation, balancing and the over-limit comparison work in exact cents; sums, outstanding amounts and aging are computed in SQL as `NUMERIC`. `float64` would accept `"NaN"` and treat a one-cent difference on large amounts as balanced.

## Contract surface

gRPC `erp.finance.v1.FinanceService`:

| rpc | Kind | Notes |
|---|---|---|
| `CheckPeriodOpen` | read | for other components; advisory (see below) |
| `GetCreditExposure`, `BatchGetCreditExposure` | read | for other components; the batch call is how a caller asks for many customers, never a loop; missing customers count as 0 |
| `ClosePeriod`, `ReopenPeriod`, `LockPeriod` | write | idempotent by `idempotency_key` |
| `PostManualEntry`, `ReverseEntry` | write | idempotent by `idempotency_key` |
| `GetEntry`, `ListEntries`, `ListARLedger` | read | cursor paging, default window the last 90 days |

The user-facing rpcs apply the caller's legal entities, which needs a verified user identity; gRPC carries none, so over gRPC they answer `UNAUTHENTICATED` before reaching the service. They are served on REST.

REST under `/erp/finance`, every route behind a key and filtered by the caller's legal entities:

| Path | Key | Notes |
|---|---|---|
| `GET /entries` | `erp.finance.view` | `period`, `status_filter`, `source_doc_id`, `source_doc_type` (order detail → its entries), `created_after` / `created_before`, cursor |
| `GET /entries/{id}` | `erp.finance.view` | `403` when the entry belongs to a legal entity the caller may not see |
| `POST /entries` | `erp.finance.post` | manual entry, idempotent; debits must equal credits exactly |
| `POST /entries/{id}/reverse` | `erp.finance.post` | idempotent; once per entry |
| `POST /periods/{period}/close`, `/reopen`, `/lock` | `erp.finance.close` | idempotent; a transition from the wrong state is `400` |
| `GET /ar-ledger` | `erp.finance.view` | rows carry `customer_name`, `outstanding`, `due_date` |
| `GET /ar-ledger/summary` | `erp.finance.view` | totals and aging (below) |
| `GET /credit-exposure/{customer_id}` | `erp.finance.view` | credit used has no legal entity |
| `GET` / `POST /legal-entity-access/{sub}`, `DELETE /legal-entity-access/{sub}/{legal_entity_id}` | `erp.finance.manage_access` | idempotent grant and revoke |

`CheckPeriodOpen` and `BatchGetCreditExposure` are not on REST: they are protocols between components, not human operations.

**The period lock across components** has three layers. `CheckPeriodOpen` is advisory: an upstream asks before changing a historical document, to give its user a timely, decent error. The authoritative check happens inside this component's posting transaction and cannot be bypassed. A late document is not rejected: it is booked in the next open period (a later-period adjustment). The race between asking and writing is accepted, not overlooked: closing a period is a human procedure, not one API call.

**Idempotency of postings**, two layers, both needed. Write commands claim `command_idempotency` with `INSERT … ON CONFLICT DO NOTHING` before writing. Entries created from events are claimed by the header itself: `INSERT … ON CONFLICT (source_component, source_doc_type, source_doc_id, source_revision) WHERE source_component != '' DO NOTHING`, before `post_no` is taken; a conflict means another transaction already posted that document, and nothing is written. The inbox stops the same message twice; the index stops two different messages about one document. A `SELECT` before the insert would let two concurrent deliveries both proceed, and catching the unique violation instead leaves the transaction aborted. None of the reference systems has this constraint (they are single-process); it is ours.

**Receivables for the frontend.**

- `customer_name` is read at query time from the customer summary copy (`LEFT JOIN`; empty until the customer's first event arrives, and for customers carried over from 1.x, whose copy predates the `name` column, until their next `mdm.customer.updated.v1`). Copying it into each receivable at posting time was the alternative; the sales-order event carries no name, so it would come from the same copy anyway, and reading at query time follows renames.
- `outstanding` = `amount − reconciled_amount`, computed in SQL.
- `due_date` is set when the receivable is written. Upstream events carry no payment terms, so it is the posting date (due on receipt); when sales adds terms to its event (an additive field), finance can use them.
- `GET /ar-ledger/summary` returns `total_receivable`, `total_reconciled`, `outstanding` and the outstanding amount by days past due — `as_of` (today, UTC) minus `due_date`: `d0_30` (including not yet due), `d31_60`, `d61_90`, `d90_plus`; day 30 is in the first bucket, day 31 in the second. It ignores the default 90-day list window: it is a statement of everything open, and the oldest bucket is the one that matters. All values are decimal strings with two decimals (`"0.00"` when empty).
- REST only; the gRPC messages are unchanged, so the contract package keeps its version.

## Events

Published, through the outbox in the same transaction as the posting:

| Subject | Grade | When | Payload |
|---|---|---|---|
| `finance.voucher.posted.v1` | peripheral | after every posting | entry id, entry no, post no, period, amount (the debit total) |
| `finance.credit.rejected.v1` | core | posting a sales order takes credit used above a configured limit (a limit of 0 means "not set", never "no credit") | customer id, order id, exposure, limit |

`finance.credit.rejected.v1` is consumed by `erp/sales`, which puts the order on hold. The credit check happens twice on purpose: sales pre-checks against its local copy before creating the order (cheap, catches the obvious cases), finance checks again with the authoritative figure when posting. This is stricter than Odoo or ERPNext, which only warn.

Consumed:

| Subject | From | Effect |
|---|---|---|
| `sales.order.created.v1` | `erp/sales` | receivable entry (debit `1122`, credit `6001`), a receivable row due on the posting date, credit used increased, over-limit check |
| `erp.inventory.adjusted.v1` | `erp/inventory` | inventory entry: receive debits `1405` / credits `2202`; issue and loss debit `6401` / credit `1405`; gain the reverse. Amount = abs(qty) at 1 per unit, a placeholder |
| `mdm.customer.created.v1`, `mdm.customer.updated.v1` | `mdm/customer` | customer summary copy (limit, name); applied only when the event's version is greater than the stored one, because the inbox is monotonic per subject and these are two subjects. A `credit_limit` that is not a valid amount (`NaN`, negative, exponent form) is stored as `0`, meaning no limit, with a Warn; a stored value that cannot be parsed is treated the same way when posting, so a bad upstream value never blocks a receivable |

The posting date of an event-driven entry is the date it is processed: the events carry no business date, so the period comes from that date. Upstream events have no legal entity; their entries go to the legal entity `default`.

## Dependencies

None, by design. Expected but absent:

| Not a dependency | Why |
|---|---|
| `erp/sales`, `erp/inventory` | only their events are consumed; a synchronous edge would put the sink into a call chain |
| `mdm/customer` | the credit limit is needed, but from the event copy, not a call; the copy's eventual-consistency window (milliseconds) is acceptable for "limit changed just before an order" |
| `infra/iam-casdoor`, `infra/authz` | the JWKS and the permission bundle are configuration (`IAM_JWKS_URL`, `AUTHZ_BUNDLE_URL`), not edges |
| `mdm/product` | a real cost per unit would need it; the placeholder amount avoids the edge until the costing question is settled (see Open questions) |

## Place in the synchronous call graph

A leaf. `erp/sales` holds a gRPC client for `CheckPeriodOpen` and `BatchGetCreditExposure`; finance calls no one. It is one of the three hubs (master data read by all; inventory the only writer of stock movements; finance mostly commanded and listening). Keep the two graphs apart: in the event graph finance has an outgoing edge (`finance.credit.rejected.v1` → `erp/sales`) while sales has a synchronous edge into finance. That is not a cycle; drawing the event edge into the synchronous graph is the classic analysis mistake here.

## Partitioning and archiving

Entry lines are partitioned by accounting period (LIST, one partition per `YYYY-MM`), not by time: the period is a discrete business label. There is no background task creating future partitions: opening a fiscal year is a business act, not the calendar rolling on, so a new year's periods and partitions come with a migration. The migration hands the partitioned table to `erp_finance_rw` so that archiving can later detach partitions as that role.

| Data | Hot | Archiving rule | Where |
|---|---|---|---|
| entry lines | open periods and the last 12 months | only when the period is `LOCKED`, never because it is old | `erp_finance_archive` |
| entry headers | always | never | — |
| receivable / payable rows | while not settled | settled and older than 24 months | `erp_finance_archive` |
| accounts, fiscal years, periods | always | never | — |

The asymmetry is deliberate: headers stay because the idempotency index on them must hold forever (a three-year-old document redelivered must not post again), while lines may go. Nothing is archived yet; the archive schema is empty.

## Data scopes

`legal_entity`, `mode: in`, on `finance_journal_entries`, `finance_journal_entry_lines`, `ar_ledger`, `ap_ledger`: in a group with several companies, company A's accountant must not see company B's books. `legal_entity_id` is a business column, not a permission-only column, and it exists on the partitioned lines from the start because adding a column to a partitioned table later is far more expensive. No owner or department columns.

The scope data is this component's own (`legal_entity_access`), not a JWT claim. Every read pushes the caller's list into SQL (`legal_entity_id = ANY(...)`; an empty list matches nothing, so an ungranted user sees nothing), every write checks the named legal entity, and a reversal checks the original's. Only one legal entity exists today (`default`), but the code and the tests run with several.

## Reference implementations

| Project | Version | Module consulted | What was borrowed | License | Usage |
|---|---|---|---|---|---|
| Tryton | 7.x | `modules/account/move.py` | entries only draft / posted; cancelling writes a reverse entry; `number` and `post_number` kept apart | GPL-3 | Borrowed reasoning |
| Tryton | 7.x | `modules/account/period.py`, `fiscalyear.py` | three period states, close reversible, locked final | GPL-3 | Borrowed reasoning |
| ERPNext | v14+ | `accounts/doctype/payment_ledger_entry/` | receivables and payables materialised apart from the general ledger, because settling would otherwise rewrite ledger entries | GPL-3 | Borrowed reasoning |
| ERPNext | v15 | `accounts/doctype/accounting_period/` | period control across document types | GPL-3 | Borrowed reasoning |
| Oracle Fusion | — | `GL_PERIOD_STATUSES` documentation | period status as a first-class, queryable entity | proprietary | Borrowed practice |

**Deliberately avoided**: ERPNext's `make_gl_entries` does not deduplicate (callers must reverse first), the source of recurring duplicate-GL bugs — the header unique index replaces it. ERPNext's runtime-DDL accounting dimensions — fixed columns instead. Frappe's naming-series counter row (a global hot row, gaps on rollback) — `post_no` uses the per-period row the posting already locks. Odoo's lock date without period entities, and its silent shifting of posting dates.

**Slot family candidates**: closing policy (Tryton's period entities with checks, Odoo's lock dates, ERPNext's three mechanisms) and late-document policy (reject / roll forward / privileged override) differ by customer size. Because `erp/sales` has a synchronous edge to this component, they cannot become slots; the shape would be an internal policy or a customer fork. Today: roll forward.

## Open questions

| Question | Current answer |
|---|---|
| Opening a fiscal year | Periods exist up to 2027-12-31 (migration `008`). Without the next year's periods every posting from 1 January fails with `NotFound`, including every event-driven entry, and those events are lost (delivery is at most once). Opening a year is a business act, so it needs an admin endpoint (key `erp.finance.close` or a new one) or a scheduled opener that creates the next year ahead of time, plus an alert when the next year is not open by the fourth quarter. Until then each year is a migration like `008` |
| Real cost of inventory adjustments | Placeholder 1 per unit. Standard / moving average / FIFO is a costing slot candidate, and which component should own it (amounts here, quantities in inventory) is undecided |
| Receivables are never settled | No receipt flow writes `reconciled_amount` or reduces credit used; credit used only grows. The receipt flow belongs to a payment integration |
| Credit used vs the ledger | The ledger is the truth, credit used a cached figure; a reconciliation job comparing them is not built |
| A stronger answer to the check-then-write race on periods | Accepted for now (the in-transaction check is the guard); the alternatives are a distributed lock (rejected: no Redis-class component) or a reverse scan of documents at closing, to be done with reconciliation |
| User identity on gRPC | The user-facing rpcs fail over gRPC; propagating the user's identity on gRPC is SDK work |
| Payment terms | `due_date` is the posting date; when sales publishes terms or a due date (an additive event field), finance uses it |
| Archiving | The rules above are decided; nothing is archived yet |
| Payable ledger | Structure only, waiting for a purchasing component |
| Several legal entities | The code and tests support them; no API creates a legal entity's periods, so a new legal entity also comes with a migration |
| Business date of event-driven entries | The events carry none, so the period follows the processing date: an order created on the 31st and processed after midnight lands in the next month. When sales publishes a business date (an additive field), postings should use it; the header's unique index keeps a redelivery from posting twice whatever the date |
